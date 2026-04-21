package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mstgnz/golog/models"
)

// mockStore implements LogStore for testing.
type mockStore struct {
	logs      []models.Log
	insertID  int
	insertErr error
	listenFn  func(ctx context.Context, ch chan<- models.Log) error
}

func (m *mockStore) GetLogs(filter models.LogFilter) ([]models.Log, error) {
	return m.logs, nil
}

func (m *mockStore) InsertLog(logEntry models.Log) (int, error) {
	return m.insertID, m.insertErr
}

func (m *mockStore) ListenForLogs(ctx context.Context, ch chan<- models.Log) error {
	if m.listenFn != nil {
		return m.listenFn(ctx, ch)
	}
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return nil
}

func newTestServer(store LogStore) *Server {
	return NewServer(store)
}

func TestSetupRoutes(t *testing.T) {
	srv := newTestServer(&mockStore{})
	if srv.SetupRoutes() == nil {
		t.Fatal("SetupRoutes() returned nil")
	}
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

func TestStreamLogsHandler(t *testing.T) {
	// listenCh lets the test inject log entries into the stream.
	listenCh := make(chan models.Log, 1)
	ms := &mockStore{
		listenFn: func(ctx context.Context, ch chan<- models.Log) error {
			go func() {
				for {
					select {
					case l := <-listenCh:
						ch <- l
					case <-ctx.Done():
						close(ch)
						return
					}
				}
			}()
			return nil
		},
	}

	srv := newTestServer(ms)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.StartLogListener(ctx); err != nil {
		t.Fatalf("StartLogListener: %v", err)
	}

	// Use a short-lived request context so the handler terminates on its own.
	reqCtx, reqCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer reqCancel()

	req := httptest.NewRequest("GET", "/api/logs/stream", nil).WithContext(reqCtx)
	rr := httptest.NewRecorder()

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		srv.StreamLogsHandler(rr, req)
	}()

	// Wait for client registration before sending.
	time.Sleep(20 * time.Millisecond)

	listenCh <- models.Log{ID: 1, Level: "INFO", Type: "SYSTEM", Message: "stream-test"}

	<-handlerDone

	body := rr.Body.String()
	if !strings.Contains(body, "stream-test") {
		t.Errorf("SSE body = %q, want to contain 'stream-test'", body)
	}
	if !strings.HasPrefix(body, "data: ") {
		t.Errorf("SSE body should start with 'data: ', got: %q", body)
	}
}

func TestStreamLogsHandlerFilter(t *testing.T) {
	listenCh := make(chan models.Log, 2)
	ms := &mockStore{
		listenFn: func(ctx context.Context, ch chan<- models.Log) error {
			go func() {
				for {
					select {
					case l := <-listenCh:
						ch <- l
					case <-ctx.Done():
						close(ch)
						return
					}
				}
			}()
			return nil
		},
	}

	srv := newTestServer(ms)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.StartLogListener(ctx); err != nil {
		t.Fatalf("StartLogListener: %v", err)
	}

	reqCtx, reqCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer reqCancel()

	req := httptest.NewRequest("GET", "/api/logs/stream?level=ERROR", nil).WithContext(reqCtx)
	rr := httptest.NewRecorder()

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		srv.StreamLogsHandler(rr, req)
	}()

	time.Sleep(20 * time.Millisecond)

	// This should be filtered out (INFO != ERROR).
	listenCh <- models.Log{ID: 1, Level: "INFO", Type: "SYSTEM", Message: "should-be-filtered"}
	// This should pass through.
	listenCh <- models.Log{ID: 2, Level: "ERROR", Type: "DATABASE", Message: "should-appear"}

	<-handlerDone

	body := rr.Body.String()
	if strings.Contains(body, "should-be-filtered") {
		t.Errorf("filtered message appeared in SSE stream: %q", body)
	}
	if !strings.Contains(body, "should-appear") {
		t.Errorf("expected message missing from SSE stream: %q", body)
	}
}

func TestStartLogListenerError(t *testing.T) {
	ms := &mockStore{
		listenFn: func(ctx context.Context, ch chan<- models.Log) error {
			return context.DeadlineExceeded
		},
	}
	srv := newTestServer(ms)
	if err := srv.StartLogListener(context.Background()); err == nil {
		t.Fatal("expected error from StartLogListener, got nil")
	}
}
