package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mstgnz/golog/v2/models"
)

// Integration tests run against GOLOG_TEST_DSN (a postgres:// URL to a
// throwaway database) and are skipped when it is empty. Each test works in its
// own schema, so tests never see each other's rows.

type testDB struct {
	store  *Store
	admin  *sql.DB
	schema string
	app    string // application_name of the store's connections
}

func newIntegrationDB(t *testing.T) *testDB {
	t.Helper()
	base := os.Getenv("GOLOG_TEST_DSN")
	if base == "" {
		t.Skip("GOLOG_TEST_DSN not set; skipping integration test")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" {
		t.Fatalf("GOLOG_TEST_DSN must be a postgres:// URL: %v", err)
	}

	admin, err := sql.Open("postgres", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })

	schema := fmt.Sprintf("golog_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})

	q := u.Query()
	q.Set("search_path", schema)
	q.Set("application_name", schema)
	u.RawQuery = q.Encode()

	store, err := Open(context.Background(), u.String())
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return &testDB{store: store, admin: admin, schema: schema, app: schema}
}

func (d *testDB) migrate(t *testing.T) {
	t.Helper()
	if err := d.store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate(): %v", err)
	}
}

func listen(t *testing.T, s *Store) <-chan models.LogEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan models.LogEvent, 16)
	if err := s.ListenForLogs(ctx, ch); err != nil {
		cancel()
		t.Fatalf("ListenForLogs(): %v", err)
	}
	t.Cleanup(func() {
		cancel()
		for range ch { // wait for the goroutine to close ch
		}
	})
	return ch
}

func nextEvent(t *testing.T, ch <-chan models.LogEvent) models.LogEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("listener channel closed")
		}
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a log event")
	}
	return models.LogEvent{}
}

func TestIntegrationMigrateIsIdempotent(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	d.migrate(t)

	var versions []int
	rows, err := d.store.db.Query("SELECT version FROM golog_schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(versions) != "[1 2 3 4]" {
		t.Fatalf("applied versions = %v, want [1 2 3 4]", versions)
	}

	for _, idx := range []string{"logs_timestamp_id_idx", "logs_attrs_idx"} {
		var valid bool
		err = d.admin.QueryRow(`
			SELECT i.indisvalid FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2`, d.schema, idx).Scan(&valid)
		if err != nil {
			t.Fatalf("index %s not found: %v", idx, err)
		}
		if !valid {
			t.Fatalf("index %s is INVALID", idx)
		}
	}
}

// seedFilterRows inserts the rows the filter parity test runs against.
func seedFilterRows(t *testing.T, s *Store) []models.Log {
	t.Helper()
	rows := []models.Log{
		{Level: "ERROR", Type: "PAYMENT", Service: "billing", Message: "Card DECLINED for order 7", Attrs: json.RawMessage(`{"user_id":42,"code":"042","ok":false}`)},
		{Level: "INFO", Type: "PAYMENT", Service: "billing", Message: "card approved", Attrs: json.RawMessage(`{"user_id":"42","ratio":1.5}`)},
		{Level: "INFO", Type: "AUTH", Service: "web", Message: "50% off_sale \\ done", Attrs: json.RawMessage(`{"gone":null,"nested":{"a":1}}`)},
		{Level: "DEBUG", Type: "payment", Service: "", Message: "Grüße ÜBER alles", Attrs: json.RawMessage(`{}`)},
		{Level: "WARNING", Type: "API", Service: "web", Message: "plain", Attrs: json.RawMessage(`{"user_id":43,"tags":["a","b"]}`)},
	}
	if _, err := s.InsertLogs(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	all, err := s.GetLogs(context.Background(), models.LogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// The list (SQL) and the live stream (Go Matches) must select the same rows
// for every filter, or the dashboard shows different results on reload.
func TestIntegrationFilterParitySQLAndGo(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	all := seedFilterRows(t, d.store)

	attr := func(k, v string) map[string]string { return map[string]string{k: v} }
	filters := []models.LogFilter{
		{Level: "INFO"},
		{Type: "PAYMENT"},
		{Type: "payment"},
		{Service: "web"},
		{Query: "card declined"},
		{Query: "CARD"},
		{Query: "50%"},
		{Query: "off_sale"},
		{Query: `\`},
		{Query: "%"},
		{Query: "grüße über"},
		{Attrs: attr("user_id", "42")},
		{Attrs: attr("user_id", "42.0")},
		{Attrs: attr("user_id", "43")},
		{Attrs: attr("code", "042")},
		{Attrs: attr("code", "42")},
		{Attrs: attr("ok", "false")},
		{Attrs: attr("gone", "null")},
		{Attrs: attr("ratio", "1.50")},
		{Attrs: attr("tags", "a")},
		{Attrs: attr("nested", `{"a":1}`)},
		{Attrs: attr("missing", "x")},
		{Level: "ERROR", Service: "billing", Query: "order", Attrs: map[string]string{"user_id": "42", "ok": "false"}},
	}
	for _, f := range filters {
		got, err := d.store.GetLogs(context.Background(), f)
		if err != nil {
			t.Fatalf("GetLogs(%+v): %v", f, err)
		}
		var sqlIDs, goIDs []int
		for _, l := range got {
			sqlIDs = append(sqlIDs, l.ID)
		}
		for _, l := range all {
			if f.Matches(l) {
				goIDs = append(goIDs, l.ID)
			}
		}
		if fmt.Sprint(sqlIDs) != fmt.Sprint(goIDs) {
			t.Errorf("filter %+v: SQL selected %v, Go selected %v", f, sqlIDs, goIDs)
		}
	}
}

func TestIntegrationAttrFilterUsesGINIndex(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	if _, err := d.store.db.Exec(`
		INSERT INTO logs (level, type, message, attrs)
		SELECT 'INFO', 'API', 'm', jsonb_build_object('trace_id', 't' || g, 'n', g)
		FROM generate_series(1, 20000) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.db.Exec("ANALYZE logs"); err != nil {
		t.Fatal(err)
	}
	where, args, err := whereClause(models.LogFilter{Attrs: map[string]string{"trace_id": "t777"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := d.store.db.Query("EXPLAIN SELECT id FROM logs"+where, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "logs_attrs_idx") {
		t.Fatalf("attr filter does not use logs_attrs_idx:\n%s", plan.String())
	}
}

func TestIntegrationBatchInsertNotifiesEachRow(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	events := listen(t, d.store)

	ids, err := d.store.InsertLogs(context.Background(), []models.Log{
		{Level: "INFO", Type: "A", Message: "one"},
		{Level: "INFO", Type: "B", Service: "svc", Message: "two", Attrs: json.RawMessage(`{"k":"v"}`)},
		{Level: "INFO", Type: "C", Message: "three"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || !(ids[0] < ids[1] && ids[1] < ids[2]) {
		t.Fatalf("ids = %v, want 3 increasing ids in input order", ids)
	}
	for i, want := range []string{"one", "two", "three"} {
		ev := nextEvent(t, events)
		if ev.Log.ID != ids[i] || ev.Log.Message != want {
			t.Fatalf("event %d = %+v, want id %d %q", i, ev.Log, ids[i], want)
		}
	}
}

// ids[i] must be the row of entries[i]; the API returns ids in input order.
func TestIntegrationBatchIdsMatchEntries(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	ctx := context.Background()
	entries := make([]models.Log, 100)
	for i := range entries {
		entries[i] = models.Log{Level: "INFO", Type: "A", Message: fmt.Sprintf("entry %d", i)}
	}
	ids, err := d.store.InsertLogs(ctx, entries)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		got, err := d.store.GetLogByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Message != entries[i].Message {
			t.Fatalf("ids[%d] = %d holds %q, want %q", i, id, got.Message, entries[i].Message)
		}
		if i > 0 && id <= ids[i-1] {
			t.Fatalf("ids not increasing in input order: %v", ids[:i+1])
		}
	}

	// A plain INSERT afterwards still gets a fresh id from the same sequence.
	var next int
	if err := d.store.db.QueryRow("INSERT INTO logs (level, type, message) VALUES ('INFO','A','plain') RETURNING id").Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next <= ids[len(ids)-1] {
		t.Fatalf("plain insert got id %d, not after the batch's %d", next, ids[len(ids)-1])
	}
}

func TestIntegrationClientTimestampKept(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	ctx := context.Background()
	client := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	ids, err := d.store.InsertLogs(ctx, []models.Log{
		{Timestamp: client, Level: "INFO", Type: "A", Message: "with time"},
		{Level: "INFO", Type: "A", Message: "without time"},
	})
	if err != nil {
		t.Fatal(err)
	}
	withTime, err := d.store.GetLogByID(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if !withTime.Timestamp.Equal(client) {
		t.Fatalf("timestamp = %v, want the client's %v", withTime.Timestamp, client)
	}
	without, err := d.store.GetLogByID(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(without.Timestamp) > time.Minute || without.Timestamp.IsZero() {
		t.Fatalf("timestamp = %v, want the server's now()", without.Timestamp)
	}
}

// Review finding: the raw attrs bytes went to jsonb, which refuses escapes
// Go's decoder accepts. Storage must use the re-encoded form.
func TestIntegrationAttrsThatJsonbRefusesRaw(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	ctx := context.Background()
	entry := models.Log{Level: "INFO", Type: "A", Message: "m", Attrs: json.RawMessage(`{"a":"x\ud800y","n":1.50}`)}
	if err := entry.Validate(); err != nil {
		t.Fatalf("precondition: Validate() = %v", err)
	}
	ids, err := d.store.InsertLogs(ctx, []models.Log{entry})
	if err != nil {
		t.Fatalf("InsertLogs() = %v; a validated entry must store", err)
	}
	got, err := d.store.GetLogByID(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	var attrs map[string]any
	if err := json.Unmarshal(got.Attrs, &attrs); err != nil || attrs["a"] != "x�y" {
		t.Fatalf("stored attrs = %s", got.Attrs)
	}

	// A filter value outside numeric range must not break the query.
	if _, err := d.store.GetLogs(ctx, models.LogFilter{Attrs: map[string]string{"n": "1e999999"}}); err != nil {
		t.Fatalf("GetLogs(attr.n=1e999999) = %v", err)
	}
}

// Same class: a query-string value with invalid UTF-8 (?q=%FF) reaches
// PostgreSQL as text it refuses. Filter validation must stop it first.
func TestIntegrationInvalidUTF8FilterIsRejectedBeforeSQL(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	f := models.LogFilter{Query: "\xff"}
	if _, err := d.store.GetLogs(context.Background(), f); err == nil {
		t.Fatal("precondition: PostgreSQL should refuse invalid UTF-8")
	}
	if err := f.Validate(); err == nil {
		t.Fatal("LogFilter.Validate() must reject invalid UTF-8")
	}
}

func TestIntegrationBatchInsertIsAllOrNothing(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	_, err := d.store.InsertLogs(context.Background(), []models.Log{
		{Level: "INFO", Type: "A", Message: "kept?"},
		{Level: "INFO", Type: strings.Repeat("x", 60), Message: "type too long for varchar(50)"},
	})
	if err == nil {
		t.Fatal("InsertLogs() should fail")
	}
	logs, err := d.store.GetLogs(context.Background(), models.LogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Fatalf("a failed batch left %d rows behind", len(logs))
	}
}

func TestIntegrationFacets(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	seedFilterRows(t, d.store)
	f, err := d.store.Facets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Order follows the database collation; compare as sets.
	types := append([]string(nil), f.Types...)
	sort.Strings(types)
	if fmt.Sprint(types) != "[API AUTH PAYMENT payment]" || fmt.Sprint(f.Services) != "[billing web]" {
		t.Fatalf("facets = %+v", f)
	}
}

func TestIntegrationRetention(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	ctx := context.Background()
	if _, err := d.store.db.Exec(`
		INSERT INTO logs (timestamp, level, type, message)
		SELECT now() - interval '40 days', 'INFO', 'OLD', 'old' FROM generate_series(1, 7);
		INSERT INTO logs (timestamp, level, type, message) VALUES
			(now() - interval '1 day', 'INFO', 'NEW', 'new'),
			(NULL, 'INFO', 'NULL', 'no timestamp');`); err != nil {
		t.Fatal(err)
	}

	n, err := d.store.DeleteOlderThan(ctx, time.Now().Add(-30*24*time.Hour), 3) // 3+3+1: several batches
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("deleted %d rows, want 7", n)
	}
	left, err := d.store.GetLogs(ctx, models.LogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, l := range left {
		types = append(types, l.Type)
	}
	sort.Strings(types)
	if fmt.Sprint(types) != "[NEW NULL]" {
		t.Fatalf("rows left = %v, want the new row and the NULL-timestamp row", types)
	}
}

func TestIntegrationRetentionRound(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	ctx := context.Background()
	if _, err := d.store.db.Exec(`INSERT INTO logs (timestamp, level, type, message) VALUES (now() - interval '2 days', 'INFO', 'OLD', 'old')`); err != nil {
		t.Fatal(err)
	}

	// Another instance holds the lock: this one skips the round.
	holder, err := d.store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, "SELECT pg_advisory_lock($1)", retentionLockID); err != nil {
		t.Fatal(err)
	}
	if n, ran, err := d.store.retentionRound(ctx, 24*time.Hour); err != nil || ran || n != 0 {
		t.Fatalf("retentionRound() with the lock taken = %d, %v, %v; want a skipped round", n, ran, err)
	}
	if _, err := holder.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", retentionLockID); err != nil {
		t.Fatal(err)
	}
	holder.Close()

	// RunRetention runs a round right away and stops with ctx.
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		d.store.RunRetention(runCtx, 24*time.Hour, time.Hour)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		logs, err := d.store.GetLogs(ctx, models.LogFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(logs) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RunRetention did not delete the old row")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunRetention did not stop after cancel")
	}
}

func TestIntegrationFailedMigrationRollsBack(t *testing.T) {
	d := newIntegrationDB(t)
	ctx := context.Background()

	bad := []migration{{version: 1, name: "0001_bad.sql", sql: "CREATE TABLE half_done (x int); SELEC broken;"}}
	if err := migrate(ctx, d.store.db, bad); err == nil || !strings.Contains(err.Error(), "0001_bad.sql") {
		t.Fatalf("migrate() = %v, want an error naming the file", err)
	}
	var tables int
	if err := d.admin.QueryRow(
		"SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'half_done'",
		d.schema).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("a failed migration left its table behind; the transaction did not roll back")
	}
	var recorded int
	if err := d.store.db.QueryRow("SELECT count(*) FROM golog_schema_migrations").Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Fatal("a failed migration was recorded as applied")
	}

	badNoTx := []migration{{version: 1, name: "0001_bad_notx.sql", sql: "SELEC broken", noTx: true}}
	if err := migrate(ctx, d.store.db, badNoTx); err == nil {
		t.Fatal("migrate() should fail on a broken no-transaction migration")
	}
}

func TestIntegrationMigrateWaitsForLockAndRespectsContext(t *testing.T) {
	d := newIntegrationDB(t)
	holder, err := d.store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		t.Fatal(err)
	}
	defer holder.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockID) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := d.store.Migrate(ctx); err == nil {
		t.Fatal("Migrate() should give up when ctx ends while another instance holds the lock")
	}
}

func TestIntegrationListenerPings(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	d.store.pingInterval = 20 * time.Millisecond
	events := listen(t, d.store)

	time.Sleep(150 * time.Millisecond) // several pings on a healthy connection
	id, err := d.store.InsertLog(context.Background(), models.Log{Level: "INFO", Type: "SYSTEM", Message: "after pings"})
	if err != nil {
		t.Fatal(err)
	}
	if ev := nextEvent(t, events); ev.Resync || ev.Log.ID != id {
		t.Fatalf("event = %+v, want log %d (pings must not disturb the listener)", ev, id)
	}
}

// Review finding: a synchronous Ping in the goroutine that drains
// listener.Notify deadlocks once more than ~64 notifications arrive during the
// ping round trip. Bursts of 300 rows with a ping every few milliseconds hit it.
func TestIntegrationListenerSurvivesBurstsDuringPing(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	d.store.pingInterval = 2 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan models.LogEvent, 16)
	if err := d.store.ListenForLogs(ctx, ch); err != nil {
		cancel()
		t.Fatal(err)
	}

	const batches, perBatch = 5, 300
	for b := 0; b < batches; b++ {
		if _, err := d.store.db.Exec(
			"INSERT INTO logs (level, type, message) SELECT 'INFO', 'API', 'burst' FROM generate_series(1, $1)", perBatch); err != nil {
			cancel()
			t.Fatal(err)
		}
	}

	received := 0
	timeout := time.After(20 * time.Second)
	for received < batches*perBatch {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("listener channel closed early")
			}
			if !ev.Resync {
				received++
			}
		case <-timeout:
			cancel()
			t.Fatalf("listener stalled after %d of %d events", received, batches*perBatch)
		}
	}

	cancel()
	closed := make(chan struct{})
	go func() {
		for range ch {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not shut down")
	}
}

func TestIntegrationMigrateConcurrently(t *testing.T) {
	d := newIntegrationDB(t)
	stores := make([]*Store, 4)
	for i := range stores {
		s, err := Open(context.Background(), d.store.dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		stores[i] = s
	}

	var wg sync.WaitGroup
	errs := make([]error, len(stores))
	for i, s := range stores {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			errs[i] = s.Migrate(ctx)
		}(i, s)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("instance %d: Migrate() = %v", i, err)
		}
	}
}

// v1.0.0 through v1.1.0 shipped this schema via init.sql: the trigger put the
// whole row in the NOTIFY payload, so inserts over ~8000 bytes failed.
const v1Schema = `
CREATE TABLE logs (
    id SERIAL PRIMARY KEY,
    timestamp TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    level VARCHAR(10) NOT NULL,
    type VARCHAR(50) NOT NULL,
    message TEXT NOT NULL
);
CREATE OR REPLACE FUNCTION notify_log_change() RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('log_channel', json_build_object(
        'id', NEW.id, 'timestamp', NEW.timestamp, 'level', NEW.level,
        'type', NEW.type, 'message', NEW.message)::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER log_notify_trigger AFTER INSERT ON logs
FOR EACH ROW EXECUTE FUNCTION notify_log_change();
INSERT INTO logs (level, type, message) VALUES ('INFO', 'SYSTEM', 'from v1');
`

func TestIntegrationUpgradeFromV1(t *testing.T) {
	d := newIntegrationDB(t)
	if _, err := d.store.db.Exec(v1Schema); err != nil {
		t.Fatalf("create v1 schema: %v", err)
	}
	big := strings.Repeat("x", 9000)
	if _, err := d.store.InsertLog(context.Background(), models.Log{Level: "INFO", Type: "API", Message: big}); err == nil {
		t.Fatal("precondition: the v1 trigger should reject a 9000-byte message")
	}

	d.migrate(t)

	logs, err := d.store.GetLogs(context.Background(), models.LogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Message != "from v1" {
		t.Fatalf("existing rows not kept: %+v", logs)
	}
	if logs[0].Service != "" || string(logs[0].Attrs) != "{}" {
		t.Fatalf("old row should read as service '' and attrs {}, got %q %s", logs[0].Service, logs[0].Attrs)
	}
	if _, err := d.store.InsertLog(context.Background(), models.Log{Level: "INFO", Type: "API", Message: big}); err != nil {
		t.Fatalf("9000-byte insert after upgrade: %v", err)
	}
}

func TestIntegrationLargeMessageReachesListener(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	events := listen(t, d.store)

	msg := strings.Repeat("ü", models.MaxMessageLength/2) // 10000 bytes, multi-byte runes
	id, err := d.store.InsertLog(context.Background(), models.Log{Level: "ERROR", Type: "API", Message: msg})
	if err != nil {
		t.Fatalf("InsertLog(): %v", err)
	}
	ev := nextEvent(t, events)
	if ev.Resync || ev.Log.ID != id || ev.Log.Message != msg || ev.Log.Timestamp.IsZero() {
		t.Fatalf("event = {id %d, resync %v, len %d}, want id %d with full message", ev.Log.ID, ev.Resync, len(ev.Log.Message), id)
	}
}

func TestIntegrationPlainSQLInsertReachesListener(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	events := listen(t, d.store)

	// The README's main path: an application writes to the table directly.
	if _, err := d.store.db.Exec("INSERT INTO logs (level, type, message) VALUES ('WARNING', 'AUTH', 'direct')"); err != nil {
		t.Fatal(err)
	}
	ev := nextEvent(t, events)
	if ev.Log.Message != "direct" || ev.Log.Level != "WARNING" || ev.Log.Type != "AUTH" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestIntegrationGetLogs(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []struct {
		offset     time.Duration
		level, typ string
		message    string
	}{
		{0, "INFO", "SYSTEM", "first"},
		{time.Minute, "ERROR", "DATABASE", "second"},
		{time.Minute, "ERROR", "AUTH", "second-tie"}, // same timestamp, higher id
		{2 * time.Minute, "DEBUG", "API", "third"},
	}
	for _, r := range rows {
		if _, err := d.store.db.Exec(
			"INSERT INTO logs (timestamp, level, type, message) VALUES ($1, $2, $3, $4)",
			base.Add(r.offset), r.level, r.typ, r.message); err != nil {
			t.Fatal(err)
		}
	}

	messages := func(filter models.LogFilter) string {
		t.Helper()
		logs, err := d.store.GetLogs(ctx, filter)
		if err != nil {
			t.Fatalf("GetLogs(%+v): %v", filter, err)
		}
		var out []string
		for _, l := range logs {
			out = append(out, l.Message)
		}
		return strings.Join(out, ",")
	}

	tests := []struct {
		name   string
		filter models.LogFilter
		want   string
	}{
		{"newest first, id breaks ties", models.LogFilter{}, "third,second-tie,second,first"},
		{"level", models.LogFilter{Level: "ERROR"}, "second-tie,second"},
		{"type", models.LogFilter{Type: "DATABASE"}, "second"},
		{"level and type", models.LogFilter{Level: "ERROR", Type: "AUTH"}, "second-tie"},
		{"limit", models.LogFilter{Limit: 2}, "third,second-tie"},
		{"offset", models.LogFilter{Limit: 2, Offset: 2}, "second,first"},
		{"offset past end", models.LogFilter{Offset: 10}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := messages(tc.filter); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIntegrationListenerResyncsAfterReconnect(t *testing.T) {
	d := newIntegrationDB(t)
	d.migrate(t)
	d.store.minReconnectDelay = 50 * time.Millisecond
	d.store.maxReconnectDelay = 200 * time.Millisecond
	events := listen(t, d.store)

	// Kill the listener's backend the way a network drop or a DB restart would.
	var killed int
	deadline := time.Now().Add(5 * time.Second)
	for killed == 0 && time.Now().Before(deadline) {
		err := d.admin.QueryRow(`
			SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
			WHERE application_name = $1 AND query LIKE 'LISTEN%'`, d.app).Scan(&killed)
		if err != nil {
			t.Fatal(err)
		}
		if killed == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if killed == 0 {
		t.Fatal("listener connection not found in pg_stat_activity")
	}

	if ev := nextEvent(t, events); !ev.Resync {
		t.Fatalf("first event after reconnect = %+v, want Resync", ev)
	}

	// The listener must be subscribed again after reconnecting.
	id, err := d.store.InsertLog(context.Background(), models.Log{Level: "INFO", Type: "SYSTEM", Message: "after reconnect"})
	if err != nil {
		t.Fatal(err)
	}
	if ev := nextEvent(t, events); ev.Log.ID != id {
		t.Fatalf("event after reconnect = %+v, want id %d", ev, id)
	}
}
