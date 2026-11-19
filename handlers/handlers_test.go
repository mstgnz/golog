package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mstgnz/golog/v2/models"
)

// mockStore implements LogStore. Events sent on source reach the server's
// listener; ListenForLogs closes its channel on cancel, as the contract says.
type mockStore struct {
	logs      []models.Log
	getErr    error
	gotFilter models.LogFilter
	insertID  int
	insertErr error
	listenErr error
	source    chan models.LogEvent
}

func (m *mockStore) GetLogs(_ context.Context, filter models.LogFilter) ([]models.Log, error) {
	m.gotFilter = filter
	return m.logs, m.getErr
}

func (m *mockStore) InsertLog(_ context.Context, _ models.Log) (int, error) {
	return m.insertID, m.insertErr
}

func (m *mockStore) ListenForLogs(ctx context.Context, ch chan<- models.LogEvent) error {
	if m.listenErr != nil {
		return m.listenErr
	}
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-m.source:
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

func newTestServer(store LogStore) *Server {
	return NewServer(store, Options{})
}

// sseStream reads a live SSE response event by event.
type sseStream struct {
	resp   *http.Response
	events chan string
}

func openStream(t *testing.T, url string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		resp.Body.Close()
	})
	s := &sseStream{resp: resp, events: make(chan string, 64)}
	go func() {
		defer close(s.events)
		r := bufio.NewReader(resp.Body)
		var event strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\n" {
				select {
				case s.events <- event.String():
				case <-ctx.Done():
					return
				}
				event.Reset()
				continue
			}
			event.WriteString(line)
		}
	}()
	return s
}

// next returns the next event, or ok=false when the stream ended.
func (s *sseStream) next(t *testing.T) (event string, ok bool) {
	t.Helper()
	select {
	case ev, ok := <-s.events:
		return ev, ok
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an SSE event")
		return "", false
	}
}

// startServer runs srv with its listener and returns a test HTTP server.
func startServer(t *testing.T, srv *Server, configure func(*http.Server)) (*httptest.Server, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.StartLogListener(ctx); err != nil {
		t.Fatalf("StartLogListener: %v", err)
	}
	ts := httptest.NewUnstartedServer(srv.SetupRoutes())
	if configure != nil {
		configure(ts.Config)
	}
	ts.Start()
	t.Cleanup(ts.Close)
	return ts, cancel
}

func TestGetLogsHandler(t *testing.T) {
	fixedTime := time.Now()
	tests := []struct {
		name       string
		query      string
		storeLogs  []models.Log
		statusCode int
		wantCount  int
	}{
		{
			name:  "no filters",
			query: "",
			storeLogs: []models.Log{
				{ID: 1, Timestamp: fixedTime, Level: "ERROR", Type: "DATABASE", Message: "oops"},
				{ID: 2, Timestamp: fixedTime, Level: "INFO", Type: "SYSTEM", Message: "ok"},
			},
			statusCode: http.StatusOK,
			wantCount:  2,
		},
		{
			name:       "empty result returns array not null",
			query:      "",
			storeLogs:  nil,
			statusCode: http.StatusOK,
			wantCount:  0,
		},
		{
			name:       "valid level filter",
			query:      "level=ERROR",
			storeLogs:  []models.Log{{ID: 1, Timestamp: fixedTime, Level: "ERROR", Type: "DATABASE", Message: "oops"}},
			statusCode: http.StatusOK,
			wantCount:  1,
		},
		{
			name:       "invalid level filter",
			query:      "level=TRACE",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "invalid type filter",
			query:      "type=NETWORK",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "invalid limit",
			query:      "limit=-1",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "invalid offset",
			query:      "offset=-5",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "pagination params accepted",
			query:      "limit=10&offset=20",
			storeLogs:  []models.Log{},
			statusCode: http.StatusOK,
			wantCount:  0,
		},
		{
			name:       "non-numeric limit",
			query:      "limit=ten",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "zero limit",
			query:      "limit=0",
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "non-numeric offset",
			query:      "offset=x",
			statusCode: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(&mockStore{logs: tc.storeLogs})
			req := httptest.NewRequest("GET", "/api/logs?"+tc.query, nil)
			rr := httptest.NewRecorder()
			srv.GetLogsHandler(rr, req)

			if rr.Code != tc.statusCode {
				t.Fatalf("status = %d, want %d (body: %s)", rr.Code, tc.statusCode, rr.Body.String())
			}
			if tc.statusCode == http.StatusOK {
				var result []models.Log
				if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if len(result) != tc.wantCount {
					t.Errorf("got %d logs, want %d", len(result), tc.wantCount)
				}
			}
		})
	}
}

func TestGetLogsHandlerPassesFilter(t *testing.T) {
	ms := &mockStore{}
	req := httptest.NewRequest("GET", "/api/logs?level=ERROR&type=AUTH&limit=5&offset=10", nil)
	newTestServer(ms).GetLogsHandler(httptest.NewRecorder(), req)
	want := models.LogFilter{Level: "ERROR", Type: "AUTH", Limit: 5, Offset: 10}
	if ms.gotFilter != want {
		t.Fatalf("store got filter %+v, want %+v", ms.gotFilter, want)
	}
}

func TestStoreErrorsAreNotLeaked(t *testing.T) {
	secret := errors.New(`pq: relation "logs" does not exist at host db.internal`)

	t.Run("get", func(t *testing.T) {
		rr := httptest.NewRecorder()
		newTestServer(&mockStore{getErr: secret}).GetLogsHandler(rr, httptest.NewRequest("GET", "/api/logs", nil))
		assertGeneric500(t, rr)
	})
	t.Run("insert", func(t *testing.T) {
		rr := httptest.NewRecorder()
		body := bytes.NewBufferString(`{"level":"INFO","type":"SYSTEM","message":"hi"}`)
		newTestServer(&mockStore{insertErr: secret}).AddLogHandler(rr, httptest.NewRequest("POST", "/api/logs", body))
		assertGeneric500(t, rr)
	})
}

func assertGeneric500(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if body := rr.Body.String(); strings.Contains(body, "pq:") || strings.Contains(body, "db.internal") {
		t.Fatalf("500 body leaks the store error: %q", body)
	}
}

func TestAddLogHandlerBodyTooLarge(t *testing.T) {
	msg := strings.Repeat("x", maxBodyBytes)
	body := bytes.NewBufferString(`{"level":"INFO","type":"SYSTEM","message":"` + msg + `"}`)
	rr := httptest.NewRecorder()
	newTestServer(&mockStore{}).AddLogHandler(rr, httptest.NewRequest("POST", "/api/logs", body))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rr.Code)
	}
}

// A maximum-length message of 4-byte runes must still fit under the body cap.
func TestAddLogHandlerAcceptsLargestValidMessage(t *testing.T) {
	msg := strings.Repeat("😀", models.MaxMessageLength/4)
	payload, _ := json.Marshal(models.Log{Level: "INFO", Type: "SYSTEM", Message: msg})
	rr := httptest.NewRecorder()
	newTestServer(&mockStore{insertID: 1}).AddLogHandler(rr, httptest.NewRequest("POST", "/api/logs", bytes.NewReader(payload)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rr.Code, rr.Body.String())
	}
}

func TestAddLogHandler(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		insertID   int
		insertErr  error
		statusCode int
		wantID     int
	}{
		{
			name:       "valid log",
			body:       `{"level":"INFO","type":"SYSTEM","message":"hello"}`,
			insertID:   42,
			statusCode: http.StatusOK,
			wantID:     42,
		},
		{
			name:       "missing message",
			body:       `{"level":"INFO","type":"SYSTEM","message":""}`,
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "invalid level",
			body:       `{"level":"TRACE","type":"SYSTEM","message":"test"}`,
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "invalid type",
			body:       `{"level":"INFO","type":"NETWORK","message":"test"}`,
			statusCode: http.StatusBadRequest,
		},
		{
			name:       "malformed JSON",
			body:       `not-json`,
			statusCode: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(&mockStore{insertID: tc.insertID, insertErr: tc.insertErr})
			req := httptest.NewRequest("POST", "/api/logs", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.AddLogHandler(rr, req)

			if rr.Code != tc.statusCode {
				t.Fatalf("status = %d, want %d (body: %s)", rr.Code, tc.statusCode, rr.Body.String())
			}
			if tc.statusCode == http.StatusOK {
				var resp map[string]int
				if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if resp["id"] != tc.wantID {
					t.Errorf("id = %d, want %d", resp["id"], tc.wantID)
				}
			}
		})
	}
}

func TestStreamDeliversMatchingLogs(t *testing.T) {
	ms := &mockStore{source: make(chan models.LogEvent)}
	ts, _ := startServer(t, newTestServer(ms), nil)
	s := openStream(t, ts.URL+"/api/logs/stream?level=ERROR")

	if ct := s.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	// The client is registered once headers arrive, so no sleep is needed.
	ms.source <- models.LogEvent{Log: models.Log{ID: 1, Level: "INFO", Type: "SYSTEM", Message: "filtered-out"}}
	ms.source <- models.LogEvent{Log: models.Log{ID: 2, Level: "ERROR", Type: "DATABASE", Message: `<img src=x onerror=alert(1)>`}}

	ev, ok := s.next(t)
	if !ok {
		t.Fatal("stream ended")
	}
	if !strings.HasPrefix(ev, "data: ") {
		t.Fatalf("event = %q, want a data event", ev)
	}
	var got models.Log
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(ev), "data: ")), &got); err != nil {
		t.Fatalf("event is not a JSON log: %v (%q)", err, ev)
	}
	if got.ID != 2 || got.Message != `<img src=x onerror=alert(1)>` {
		t.Fatalf("got %+v, want log 2 with its message unchanged", got)
	}
}

func TestStreamSendsResync(t *testing.T) {
	ms := &mockStore{source: make(chan models.LogEvent)}
	ts, _ := startServer(t, newTestServer(ms), nil)
	s := openStream(t, ts.URL+"/api/logs/stream?level=ERROR")

	// A resync passes every filter: the client cannot know what it missed.
	ms.source <- models.LogEvent{Resync: true}
	if ev, _ := s.next(t); ev != "event: resync\ndata: {}\n" {
		t.Fatalf("event = %q, want resync", ev)
	}
}

func TestStreamHeartbeat(t *testing.T) {
	srv := newTestServer(&mockStore{})
	srv.heartbeat = 20 * time.Millisecond
	ts, _ := startServer(t, srv, nil)
	s := openStream(t, ts.URL+"/api/logs/stream")
	if ev, _ := s.next(t); ev != ": ping\n" {
		t.Fatalf("event = %q, want heartbeat comment", ev)
	}
}

func TestStreamRejectsInvalidFilter(t *testing.T) {
	rr := httptest.NewRecorder()
	newTestServer(&mockStore{}).StreamLogsHandler(rr, httptest.NewRequest("GET", "/api/logs/stream?level=TRACE", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// Server timeouts protect normal requests; the stream must lift WriteTimeout
// or it would be cut. ReadTimeout is set too, to pin that it does no harm.
func TestStreamSurvivesServerTimeouts(t *testing.T) {
	ms := &mockStore{source: make(chan models.LogEvent)}
	ts, _ := startServer(t, newTestServer(ms), func(s *http.Server) {
		s.ReadTimeout = 100 * time.Millisecond
		s.WriteTimeout = 100 * time.Millisecond
	})
	s := openStream(t, ts.URL+"/api/logs/stream")

	time.Sleep(400 * time.Millisecond)
	ms.source <- models.LogEvent{Log: models.Log{ID: 9, Level: "INFO", Type: "API", Message: "late"}}
	ev, ok := s.next(t)
	if !ok || !strings.Contains(ev, `"late"`) {
		t.Fatalf("event = %q, ok = %v; stream was cut by server timeouts", ev, ok)
	}
}

// Review finding: with the write deadline lifted for the whole stream, a
// client that stops reading parked its handler in Write forever and held
// graceful shutdown open.
func TestStalledStreamClientDoesNotBlockShutdown(t *testing.T) {
	ms := &mockStore{source: make(chan models.LogEvent)}
	srv := newTestServer(ms)
	srv.writeTimeout = 200 * time.Millisecond
	ts, stopListener := startServer(t, srv, nil)

	// A raw connection that sends the request and then never reads.
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET /api/logs/stream HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return srv.hub.len() == 1 })

	big := strings.Repeat("x", models.MaxMessageLength)
	for i := 0; i < 3000 && srv.hub.len() == 1; i++ { // ~30 MB, far beyond socket buffers
		ms.source <- models.LogEvent{Log: models.Log{ID: i, Level: "INFO", Type: "API", Message: big}}
	}

	stopListener()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := ts.Config.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() = %v; a stalled stream handler is still running", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStreamsEndWhenListenerStops(t *testing.T) {
	srv := newTestServer(&mockStore{})
	ts, stopListener := startServer(t, srv, nil)
	s := openStream(t, ts.URL+"/api/logs/stream")

	stopListener()
	if ev, ok := s.next(t); ok {
		t.Fatalf("stream still open after listener stopped, got %q", ev)
	}

	// Late arrivals are refused instead of hanging a graceful shutdown.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := http.Get(ts.URL + "/api/logs/stream")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusServiceUnavailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %d, want 503 after listener stopped", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartLogListenerError(t *testing.T) {
	srv := newTestServer(&mockStore{listenErr: errors.New("listen failed")})
	if err := srv.StartLogListener(context.Background()); err == nil {
		t.Fatal("expected error from StartLogListener, got nil")
	}
}

func TestHubDropsSlowClientWithoutDoubleClose(t *testing.T) {
	h := newHub(1)
	slow, _ := h.add()
	fast, _ := h.add()

	h.broadcast(models.LogEvent{Log: models.Log{ID: 1}})
	<-fast.send
	h.broadcast(models.LogEvent{Log: models.Log{ID: 2}}) // slow is full: dropped

	if h.len() != 1 {
		t.Fatalf("hub has %d clients, want 1 after dropping the slow one", h.len())
	}
	<-slow.send // the buffered event is still readable
	if _, open := <-slow.send; open {
		t.Fatal("slow client's channel should be closed")
	}

	// The slow client's handler now returns and removes itself. Before the
	// fix this closed the channel a second time and panicked.
	h.remove(slow)
	h.remove(slow)

	if ev := <-fast.send; ev.Log.ID != 2 {
		t.Fatalf("fast client got %+v, want log 2", ev)
	}
}

func TestHubClose(t *testing.T) {
	h := newHub(1)
	c, _ := h.add()
	h.close()
	if _, open := <-c.send; open {
		t.Fatal("client channel should be closed")
	}
	h.remove(c)
	if _, ok := h.add(); ok {
		t.Fatal("add() after close() should fail")
	}
}

func TestSecurityHeaders(t *testing.T) {
	rr := httptest.NewRecorder()
	newTestServer(&mockStore{}).SetupRoutes().ServeHTTP(rr, httptest.NewRequest("GET", "/api/logs", nil))
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'self'; frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
	}
	for k, v := range want {
		if got := rr.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestCORS(t *testing.T) {
	request := func(srv *Server, origin string) http.Header {
		req := httptest.NewRequest("GET", "/api/logs", nil)
		req.Header.Set("Origin", origin)
		rr := httptest.NewRecorder()
		srv.SetupRoutes().ServeHTTP(rr, req)
		return rr.Header()
	}

	t.Run("off by default", func(t *testing.T) {
		h := request(newTestServer(&mockStore{}), "https://evil.example")
		if got := h.Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want none", got)
		}
	})

	srv := NewServer(&mockStore{}, Options{CORSAllowedOrigins: []string{"https://app.example"}})
	t.Run("allowed origin", func(t *testing.T) {
		h := request(srv, "https://app.example")
		if got := h.Get("Access-Control-Allow-Origin"); got != "https://app.example" {
			t.Fatalf("Access-Control-Allow-Origin = %q", got)
		}
		if got := h.Get("Access-Control-Allow-Credentials"); got != "" {
			t.Fatalf("Access-Control-Allow-Credentials = %q, want none", got)
		}
	})
	t.Run("other origin", func(t *testing.T) {
		if got := request(srv, "https://evil.example").Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want none", got)
		}
	})
}

func TestStaticFiles(t *testing.T) {
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>GoLog</h1>")}}

	rr := httptest.NewRecorder()
	NewServer(&mockStore{}, Options{Static: static}).SetupRoutes().ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "GoLog") {
		t.Fatalf("GET / = %d %q, want the index page", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	newTestServer(&mockStore{}).SetupRoutes().ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET / without static files = %d, want 404", rr.Code)
	}
}
