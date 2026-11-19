package database

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadMigrations(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_index.sql":  {Data: []byte("-- golog:no-transaction\nCREATE INDEX CONCURRENTLY x ON y (z);")},
		"0001_init.sql":   {Data: []byte("CREATE TABLE y (z int);")},
		"README.md":       {Data: []byte("ignored")},
		"0010_later.sql":  {Data: []byte("  \n-- golog:no-transaction\nSELECT 1;")},
		"subdir/0003.sql": {Data: []byte("ignored, not top level")},
	}
	got, err := loadMigrations(fsys)
	if err != nil {
		t.Fatalf("loadMigrations() error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("loaded %d migrations, want 3", len(got))
	}
	wantVersions := []int{1, 2, 10}
	wantNoTx := []bool{false, true, true}
	for i, m := range got {
		if m.version != wantVersions[i] || m.noTx != wantNoTx[i] {
			t.Errorf("migration %d = {version %d, noTx %v}, want {%d, %v}", i, m.version, m.noTx, wantVersions[i], wantNoTx[i])
		}
	}
}

func TestLoadMigrationsErrors(t *testing.T) {
	tests := []struct {
		name string
		fsys fstest.MapFS
		want string
	}{
		{name: "no version prefix", fsys: fstest.MapFS{"init.sql": {}}, want: "must start with"},
		{name: "zero version", fsys: fstest.MapFS{"0000_init.sql": {}}, want: "must start with"},
		{name: "duplicate version", fsys: fstest.MapFS{"0001_a.sql": {}, "1_b.sql": {}}, want: "share version 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadMigrations(tc.fsys)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("loadMigrations() error = %v, want %q", err, tc.want)
			}
		})
	}
}

// The shipped migrations must parse, and a no-transaction file must hold a
// single statement: lib/pq runs a multi-statement string as one implicit
// transaction, where CREATE INDEX CONCURRENTLY is refused.
func TestEmbeddedMigrations(t *testing.T) {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(sub)
	if err != nil {
		t.Fatalf("embedded migrations do not load: %v", err)
	}
	if len(migrations) < 2 {
		t.Fatalf("found %d embedded migrations, want at least 2", len(migrations))
	}
	for i, m := range migrations {
		if m.version != i+1 {
			t.Errorf("migration %s has version %d, want %d (no gaps)", m.name, m.version, i+1)
		}
		if m.noTx && strings.Count(stripSQLComments(m.sql), ";") > 1 {
			t.Errorf("no-transaction migration %s has more than one statement", m.name)
		}
	}
}

func stripSQLComments(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
