package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mstgnz/golog/v2/handlers"
	"github.com/mstgnz/golog/v2/models"
)

const cliReadToken = "cli-read-token-0123456789"

// apiStore backs a real handlers.Server for HTTP-mode tests.
type apiStore struct {
	logs   []models.Log
	source chan models.LogEvent
}

func (s *apiStore) GetLogs(_ context.Context, f models.LogFilter) ([]models.Log, error) {
	out := []models.Log{}
	for _, l := range s.logs {
		if f.Matches(l) {
			out = append(out, l)
		}
	}
	return out, nil
}
func (s *apiStore) InsertLog(context.Context, models.Log) (int, error) { return 0, nil }
func (s *apiStore) InsertLogs(context.Context, []models.Log) ([]int, error) {
	return nil, nil
}
func (s *apiStore) Facets(context.Context) (models.Facets, error) { return models.Facets{}, nil }
func (s *apiStore) ListenForLogs(ctx context.Context, ch chan<- models.LogEvent) error {
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-s.source:
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return nil
}

func startAPI(t *testing.T, store *apiStore) string {
	t.Helper()
	srv := handlers.NewServer(store, handlers.Options{Auth: handlers.Auth{ReadToken: cliReadToken, WriteToken: "cli-write-token-0123456789"}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.StartLogListener(ctx); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.SetupRoutes())
	t.Cleanup(ts.Close)
	return ts.URL
}

func envWith(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func TestRunHTTPMode(t *testing.T) {
	store := &apiStore{
		logs: []models.Log{
			{ID: 2, Level: "ERROR", Type: "API", Service: "web", Message: "history match"},
			{ID: 1, Level: "ERROR", Type: "API", Message: "history other"},
		},
		source: make(chan models.LogEvent),
	}
	url := startAPI(t, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &syncBuffer{}
	done := make(chan error, 1)
	env := envWith(map[string]string{"GOLOG_URL": url, "GOLOG_READ_TOKEN": cliReadToken})
	go func() { done <- run(ctx, []string{"-q=match"}, env, out, io.Discard) }()

	waitForOutput(t, out, "Listening for new logs")
	if !strings.Contains(out.String(), "history match") || strings.Contains(out.String(), "history other") {
		t.Fatalf("history not filtered by the server:\n%s", out.String())
	}

	store.source <- models.LogEvent{Log: models.Log{ID: 3, Level: "ERROR", Type: "API", Message: "live but filtered out"}}
	store.source <- models.LogEvent{Log: models.Log{ID: 4, Level: "INFO", Type: "API", Service: "svc", Message: "live match", Attrs: []byte(`{"k":1}`)}}
	waitForOutput(t, out, `live match {"k":1}`)
	if strings.Contains(out.String(), "live but filtered out") {
		t.Fatalf("stream not filtered:\n%s", out.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after cancel")
	}
}

func TestRunHTTPModeWrongToken(t *testing.T) {
	url := startAPI(t, &apiStore{source: make(chan models.LogEvent)})
	env := envWith(map[string]string{"GOLOG_URL": url, "GOLOG_READ_TOKEN": "wrong-token-0123456789"})
	err := run(context.Background(), nil, env, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "GOLOG_READ_TOKEN") {
		t.Fatalf("run() = %v, want a token error", err)
	}
}

// sseServer serves a scripted stream per connection.
func sseServer(t *testing.T, script func(n int, w http.ResponseWriter, f http.Flusher)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var conns atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		n := int(conns.Add(1))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		f.Flush()
		script(n, w, f)
	}))
	t.Cleanup(ts.Close)
	return ts, &conns
}

func fastSource(t *testing.T, url string) *httpSource {
	t.Helper()
	s, err := newHTTPSource(url, "tok")
	if err != nil {
		t.Fatal(err)
	}
	s.minRetry, s.maxRetry, s.idleTimeout = 10*time.Millisecond, 50*time.Millisecond, 5*time.Second
	return s
}

func nextEv(t *testing.T, ch <-chan models.LogEvent) models.LogEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return models.LogEvent{}
}

func TestHTTPStreamReconnectsAndResyncs(t *testing.T) {
	ts, conns := sseServer(t, func(n int, w http.ResponseWriter, f http.Flusher) {
		fmt.Fprintf(w, ": ping\n\nevent: message\ndata: {\"id\":%d,\"message\":\"conn %d\"}\n\n", n, n)
		if n == 2 {
			fmt.Fprint(w, "event: resync\ndata: {}\n\n")
		}
		f.Flush()
		// Returning ends the connection: the client must reconnect.
	})
	s := fastSource(t, ts.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan models.LogEvent, 16)
	if err := s.Stream(ctx, models.LogFilter{}, ch); err != nil {
		t.Fatal(err)
	}

	if ev := nextEv(t, ch); ev.Log.Message != "conn 1" {
		t.Fatalf("first event = %+v", ev)
	}
	if ev := nextEv(t, ch); !ev.Resync {
		t.Fatalf("after reconnect want Resync, got %+v", ev)
	}
	if ev := nextEv(t, ch); ev.Log.Message != "conn 2" {
		t.Fatalf("event on second connection = %+v", ev)
	}
	if ev := nextEv(t, ch); !ev.Resync {
		t.Fatalf("server resync event not passed on, got %+v", ev)
	}
	if conns.Load() < 2 {
		t.Fatal("client did not reconnect")
	}

	cancel()
	for range ch { // must close after cancel
	}
}

func TestHTTPStreamIdleTimeoutReconnects(t *testing.T) {
	ts, conns := sseServer(t, func(n int, w http.ResponseWriter, f http.Flusher) {
		time.Sleep(2 * time.Second) // silent: no data, no ping
	})
	s := fastSource(t, ts.URL)
	s.idleTimeout = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan models.LogEvent, 16)
	if err := s.Stream(ctx, models.LogFilter{}, ch); err != nil {
		t.Fatal(err)
	}
	if ev := nextEv(t, ch); !ev.Resync {
		t.Fatalf("want Resync after an idle connection was dropped, got %+v", ev)
	}
	if conns.Load() < 2 {
		t.Fatal("client did not reconnect after the idle timeout")
	}
}

func TestHTTPStreamFirstConnectErrors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom \x1b[2J", http.StatusBadGateway)
	}))
	defer ts.Close()
	s := fastSource(t, ts.URL)
	err := s.Stream(context.Background(), models.LogFilter{}, make(chan models.LogEvent))
	if err == nil || !strings.Contains(err.Error(), "502") || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("Stream() = %v, want a sanitized 502 error", err)
	}
	if _, err := s.History(context.Background(), models.LogFilter{}); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("History() = %v, want 502", err)
	}
}

func TestHTTPHistoryBadJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not json")
	}))
	defer ts.Close()
	if _, err := fastSource(t, ts.URL).History(context.Background(), models.LogFilter{}); err == nil {
		t.Fatal("History() should fail on a bad body")
	}
}

func TestNewHTTPSourceValidation(t *testing.T) {
	for _, bad := range []string{"logs.example.com", "ftp://x", "http://", "https://user:pw@x.example", "::"} {
		if _, err := newHTTPSource(bad, ""); err == nil {
			t.Errorf("newHTTPSource(%q) should fail", bad)
		}
	}
	s, err := newHTTPSource("https://logs.example.com/base/", "")
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.newRequest(context.Background(), "/api/logs", filterQuery(models.LogFilter{
		Level: "ERROR", Attrs: map[string]string{"user id": "4 2"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "https://logs.example.com/base/api/logs?attr.user+id=4+2&level=ERROR" {
		t.Fatalf("request URL = %s", got)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("no token was given, no header expected")
	}
}

func TestPlainHTTPWithToken(t *testing.T) {
	tests := []struct {
		url, token string
		want       bool
	}{
		{"http://logs.example.com", "t", true},
		{"http://10.0.0.5:8080", "t", true},
		{"https://logs.example.com", "t", false},
		{"http://localhost:8080", "t", false},
		{"http://127.0.0.1:8080", "t", false},
		{"http://[::1]:8080", "t", false},
		{"http://logs.example.com", "", false},
	}
	for _, tc := range tests {
		if got := plainHTTPWithToken(tc.url, tc.token); got != tc.want {
			t.Errorf("plainHTTPWithToken(%q, %q) = %v", tc.url, tc.token, got)
		}
	}
}

func TestOpenSourceWarnsOnPlainHTTP(t *testing.T) {
	var stderr strings.Builder
	src, err := openSource(context.Background(), options{url: "http://logs.example.com"},
		envWith(map[string]string{"GOLOG_READ_TOKEN": "x"}), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	src.Close()
	if !strings.Contains(stderr.String(), "plain http") {
		t.Fatalf("no warning: %q", stderr.String())
	}
}

func TestToEvent(t *testing.T) {
	if ev, ok := toEvent("resync", ""); !ok || !ev.Resync {
		t.Fatal("resync not recognized")
	}
	if ev, ok := toEvent("", `{"id":5}`); !ok || ev.Log.ID != 5 {
		t.Fatal("default event not parsed")
	}
	for _, c := range [][2]string{{"", ""}, {"", "not json"}, {"other", `{"id":1}`}} {
		if _, ok := toEvent(c[0], c[1]); ok {
			t.Errorf("toEvent(%q, %q) should be ignored", c[0], c[1])
		}
	}
}
