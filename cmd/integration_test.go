package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstgnz/golog/v2/database"
)

// Starts the real server against GOLOG_TEST_DSN (own schema) with tokens and
// retention on, and checks the wiring end to end.
func TestIntegrationRunWiresAuthAndRetention(t *testing.T) {
	base := os.Getenv("GOLOG_TEST_DSN")
	if base == "" {
		t.Skip("GOLOG_TEST_DSN not set; skipping integration test")
	}
	admin, err := sql.Open("postgres", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("golog_cmd_test_%d", time.Now().UnixNano())
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

	// An old row in place before start: retention's first round must remove it.
	pre, err := database.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := pre.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	pre.Close()
	if _, err := admin.Exec(fmt.Sprintf(`INSERT INTO %s.logs (timestamp, level, type, message) VALUES (now() - interval '2 days', 'INFO', 'OLD', 'old')`, schema)); err != nil {
		t.Fatal(err)
	}

	const readTok, writeTok = "read-token-0123456789", "write-token-0123456789"
	t.Setenv("DATABASE_URL", u.String())
	t.Setenv("PORT", "0")
	t.Setenv("GOLOG_READ_TOKEN", readTok)
	t.Setenv("GOLOG_WRITE_TOKEN", writeTok)
	t.Setenv("GOLOG_RETENTION_DAYS", "1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrc := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(a net.Addr) { addrc <- a }) }()

	var base2 string
	select {
	case a := <-addrc:
		base2 = "http://" + a.String()
	case err := <-done:
		t.Fatalf("run() exited early: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("server did not start")
	}

	call := func(method, path, tok, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base2+path, strings.NewReader(body))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, _ := call("GET", "/api/logs", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("read without token = %d, want 401 (tokens not wired?)", code)
	}
	if code, body := call("POST", "/api/logs", writeTok, `{"level":"INFO","type":"API","service":"svc","message":"hello","attrs":{"k":1}}`); code != http.StatusOK {
		t.Fatalf("write = %d %s", code, body)
	}
	if code, body := call("GET", "/api/logs?attr.k=1", readTok, ""); code != http.StatusOK || !strings.Contains(body, `"hello"`) {
		t.Fatalf("read = %d %s", code, body)
	}
	if code, body := call("GET", "/", "", ""); code != http.StatusOK || !strings.Contains(body, "GoLog") {
		t.Fatalf("dashboard = %d", code)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		var old int
		if err := admin.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s.logs WHERE type = 'OLD'`, schema)).Scan(&old); err != nil {
			t.Fatal(err)
		}
		if old == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retention did not delete the 2-day-old row (GOLOG_RETENTION_DAYS=1)")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, body := call("GET", "/api/logs", readTok, ""); !strings.Contains(body, `"hello"`) {
		t.Fatalf("retention deleted a fresh row: %d %s", code, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run() did not stop")
	}
}
