package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mstgnz/golog/v2/database"
)

// syncBuffer lets the test read output while run() is still writing it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForOutput(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("output never contained %q:\n%s", want, out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// schemaDSN creates a throwaway schema in GOLOG_TEST_DSN and points
// DATABASE_URL at it.
func schemaDSN(t *testing.T) (*sql.DB, string) {
	t.Helper()
	base := os.Getenv("GOLOG_TEST_DSN")
	if base == "" {
		t.Skip("GOLOG_TEST_DSN not set; skipping integration test")
	}
	admin, err := sql.Open("postgres", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := fmt.Sprintf("golog_cli_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE") })

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	t.Setenv("DATABASE_URL", u.String())
	return admin, u.String()
}

func TestIntegrationRunStreamsNewLogs(t *testing.T) {
	_, dsn := schemaDSN(t)
	store, err := database.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO logs (level, type, message) VALUES ('ERROR', 'API', 'history-entry')"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"-level=error"}, out, io.Discard) }()

	waitForOutput(t, out, "Listening for new logs")
	if !strings.Contains(out.String(), "history-entry") {
		t.Fatalf("history not printed:\n%s", out.String())
	}

	if _, err := db.Exec("INSERT INTO logs (level, type, message) VALUES ('INFO', 'API', 'filtered-entry'), ('ERROR', 'AUTH', 'live-entry')"); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, out, "live-entry")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after cancel")
	}
	if strings.Contains(out.String(), "filtered-entry") {
		t.Fatalf("filtered entry printed:\n%s", out.String())
	}
	if strings.Count(out.String(), "history-entry") != 1 {
		t.Fatalf("history entry printed more than once:\n%s", out.String())
	}
}

func TestIntegrationRunWithoutSchema(t *testing.T) {
	schemaDSN(t) // empty schema: golog-server never ran
	err := run(context.Background(), nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "start golog-server once") {
		t.Fatalf("run() = %v, want a hint to start the server", err)
	}
}
