package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mstgnz/golog/v2/config"
	"github.com/mstgnz/golog/v2/handlers"
	"github.com/mstgnz/golog/v2/models"
)

type fakeStore struct{ listenErr error }

func (f *fakeStore) GetLogs(context.Context, models.LogFilter) ([]models.Log, error) {
	return []models.Log{}, nil
}

func (f *fakeStore) InsertLog(context.Context, models.Log) (int, error) { return 1, nil }

func (f *fakeStore) InsertLogs(_ context.Context, entries []models.Log) ([]int, error) {
	return make([]int, len(entries)), nil
}

func (f *fakeStore) Facets(context.Context) (models.Facets, error) { return models.Facets{}, nil }

func (f *fakeStore) ListenForLogs(ctx context.Context, ch chan<- models.LogEvent) error {
	if f.listenErr != nil {
		return f.listenErr
	}
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return nil
}

func startServe(t *testing.T, store handlers.LogStore) (url string, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errc := make(chan error, 1)
	go func() { errc <- serve(ctx, ln, handlers.NewServer(store, handlers.Options{})) }()
	return "http://" + ln.Addr().String(), cancel, errc
}

// An open dashboard used to hold Shutdown until its timeout and the process
// exited with an error. Shutdown must now finish promptly and cleanly.
func TestServeShutsDownWithOpenStream(t *testing.T) {
	url, cancel, done := startServe(t, &fakeStore{})

	resp, err := http.Get(url + "/api/logs/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve() = %v, want nil", err)
		}
	case <-time.After(shutdownTimeout / 2):
		t.Fatal("serve() did not return; an SSE stream is holding shutdown open")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %v", elapsed)
	}
}

func TestServeAnswersRequests(t *testing.T) {
	url, cancel, done := startServe(t, &fakeStore{})
	resp, err := http.Get(url + "/api/logs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/logs = %d", resp.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve() = %v", err)
	}
}

func TestServeListenerError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	app := handlers.NewServer(&fakeStore{listenErr: errors.New("no db")}, handlers.Options{})
	if err := serve(context.Background(), ln, app); err == nil || !strings.Contains(err.Error(), "no db") {
		t.Fatalf("serve() = %v, want the listener error", err)
	}
	// The listener must have been released.
	if _, err := ln.Accept(); err == nil {
		t.Fatal("listener still open after serve() failed")
	}
}

func TestServeReturnsServeError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	app := handlers.NewServer(&fakeStore{}, handlers.Options{})
	if err := serve(context.Background(), ln, app); err == nil {
		t.Fatal("serve() on a closed listener should fail")
	}
}

func TestNewHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler())
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.WriteTimeout == 0 || srv.IdleTimeout == 0 || srv.MaxHeaderBytes == 0 {
		t.Fatalf("missing timeout or limit: %+v", srv)
	}
}

func TestRunConfigError(t *testing.T) {
	t.Setenv("PORT", "not-a-port")
	if err := run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "load config") {
		t.Fatalf("run() = %v, want a config error", err)
	}
}

func TestWarnIfOpen(t *testing.T) {
	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	warnIfOpen(&config.Config{})
	if !strings.Contains(buf.String(), "GOLOG_READ_TOKEN is not set") || !strings.Contains(buf.String(), "GOLOG_WRITE_TOKEN is not set") {
		t.Fatalf("missing warnings: %q", buf.String())
	}
	buf.Reset()
	warnIfOpen(&config.Config{ReadToken: "r", WriteToken: "w"})
	if buf.Len() != 0 {
		t.Fatalf("unexpected warning with both tokens set: %q", buf.String())
	}
}

func TestRunDatabaseError(t *testing.T) {
	// Port 1 on loopback refuses connections immediately.
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=2")
	if err := run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "connect to database") {
		t.Fatalf("run() = %v, want a connection error", err)
	}
}
