package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID is the pg_advisory_lock key that serialises migrations
// across golog instances sharing a database. Arbitrary but fixed.
const migrationLockID int64 = 7_351_402_118

const noTransactionMarker = "-- golog:no-transaction"

type migration struct {
	version int
	name    string
	sql     string
	noTx    bool
}

// loadMigrations reads NNNN_name.sql files from fsys, sorted by version.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version <= 0 {
			return nil, fmt.Errorf("migration %q: name must start with a positive number and '_'", e.Name())
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %q and %q share version %d", other, e.Name(), version)
		}
		seen[version] = e.Name()
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		text := string(body)
		out = append(out, migration{
			version: version,
			name:    e.Name(),
			sql:     text,
			noTx:    strings.HasPrefix(strings.TrimSpace(text), noTransactionMarker),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Migrate applies the embedded schema migrations that are not yet recorded in
// golog_schema_migrations. Safe to call on every start and from several
// instances at once.
func (s *Store) Migrate(ctx context.Context) error {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	migrations, err := loadMigrations(sub)
	if err != nil {
		return err
	}
	return migrate(ctx, s.db, migrations)
}

func migrate(ctx context.Context, db *sql.DB, migrations []migration) error {
	// Session-level advisory locks belong to one connection, so pin one.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := acquireMigrationLock(ctx, conn); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS golog_schema_migrations (
		version integer PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create golog_schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			if m.noTx {
				// A failed or interrupted CREATE INDEX CONCURRENTLY leaves an
				// index behind that makes every retry fail.
				return fmt.Errorf("migration %s: %w (this migration runs outside a transaction; if it creates an index, run DROP INDEX CONCURRENTLY IF EXISTS on that index and restart)", m.name, err)
			}
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

// acquireMigrationLock polls pg_try_advisory_lock instead of blocking in
// pg_advisory_lock: a session blocked inside a statement holds a snapshot,
// and CREATE INDEX CONCURRENTLY in the lock holder would wait on it forever.
func acquireMigrationLock(ctx context.Context, conn *sql.Conn) error {
	for {
		var locked bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", migrationLockID).Scan(&locked); err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if locked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func appliedVersions(ctx context.Context, conn *sql.Conn) (map[int]bool, error) {
	rows, err := conn.QueryContext(ctx, "SELECT version FROM golog_schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

func applyMigration(ctx context.Context, conn *sql.Conn, m migration) error {
	const record = "INSERT INTO golog_schema_migrations (version) VALUES ($1)"
	if m.noTx {
		if _, err := conn.ExecContext(ctx, m.sql); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, record, m.version)
		return err
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, record, m.version); err != nil {
		return err
	}
	return tx.Commit()
}
