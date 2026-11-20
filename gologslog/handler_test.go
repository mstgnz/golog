package gologslog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mstgnz/golog/v2/handlers"
	"github.com/mstgnz/golog/v2/models"
)

// collector is a fake GoLog batch endpoint.
type collector struct {
	mu       sync.Mutex
	batches  [][]entry
	requests atomic.Int32
	status   func(n int) int // status for the n-th request (1-based); nil = 200
	auth     string
}

func (c *collector) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(c.requests.Add(1))
		if r.URL.Path != "/api/logs/batch" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		c.mu.Lock()
		c.auth = r.Header.Get("Authorization")
		c.mu.Unlock()
		if c.status != nil {
			if code := c.status(n); code != http.StatusOK {
				http.Error(w, "nope", code)
				return
			}
		}
		var batch []entry
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Errorf("bad batch body: %v", err)
		}
		c.mu.Lock()
		c.batches = append(c.batches, batch)
		c.mu.Unlock()
		w.Write([]byte(`{"ids":[]}`))
	})
}

func (c *collector) all() []entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []entry
	for _, b := range c.batches {
		out = append(out, b...)
	}
	return out
}

func (c *collector) sizes() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []int
	for _, b := range c.batches {
		out = append(out, len(b))
	}
	return out
}

func newTestHandler(t *testing.T, c *collector, opts Options) *Handler {
	t.Helper()
	ts := httptest.NewServer(c.handler(t))
	t.Cleanup(ts.Close)
	opts.URL = ts.URL
	if opts.FlushInterval == 0 {
		opts.FlushInterval = time.Hour // tests flush via Close unless they say otherwise
	}
	h, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(context.Background()) })
	return h
}

func closeNow(t *testing.T, h *Handler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Close(ctx); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestBatchesAndClose(t *testing.T) {
	c := &collector{}
	h := newTestHandler(t, c, Options{Token: "tok", Service: "billing", BatchSize: 3, Level: slog.LevelDebug - 8})
	logger := slog.New(h)
	logger.Debug("d")
	logger.Info("i")
	logger.Warn("w")
	logger.Error("e")
	logger.Log(context.Background(), slog.LevelError+4, "fatal-ish")
	logger.Log(context.Background(), slog.LevelDebug-4, "trace-ish")
	logger.Info("last")
	closeNow(t, h)

	if got := c.sizes(); len(got) != 3 || got[0] != 3 || got[1] != 3 || got[2] != 1 {
		t.Fatalf("batch sizes = %v, want [3 3 1]", got)
	}
	want := []string{"DEBUG", "INFO", "WARNING", "ERROR", "ERROR", "DEBUG", "INFO"}
	for i, e := range c.all() {
		if e.Level != want[i] || e.Type != "APP" || e.Service != "billing" || e.Timestamp.IsZero() {
			t.Errorf("entry %d = %+v, want level %s, type APP, service billing, a timestamp", i, e, want[i])
		}
	}
	if c.auth != "Bearer tok" {
		t.Fatalf("Authorization = %q", c.auth)
	}
	if h.Dropped() != 0 {
		t.Fatalf("Dropped() = %d", h.Dropped())
	}
}

func TestFlushInterval(t *testing.T) {
	c := &collector{}
	h := newTestHandler(t, c, Options{FlushInterval: 20 * time.Millisecond})
	slog.New(h).Info("soon")
	deadline := time.Now().Add(3 * time.Second)
	for len(c.all()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("partial batch not sent after FlushInterval")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLevelFilter(t *testing.T) {
	c := &collector{}
	h := newTestHandler(t, c, Options{Level: slog.LevelWarn})
	if h.Enabled(context.Background(), slog.LevelInfo) || !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Fatal("Enabled() does not follow Options.Level")
	}
	slog.New(h).Info("hidden")
	slog.New(h).Warn("shown")
	closeNow(t, h)
	if all := c.all(); len(all) != 1 || all[0].Message != "shown" {
		t.Fatalf("sent %+v", all)
	}
}

func TestHandleNeverBlocks(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer ts.Close()
	defer close(block)

	h, err := New(Options{URL: ts.URL, BufferSize: 10, BatchSize: 1, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(h)
	start := time.Now()
	for i := 0; i < 2000; i++ {
		logger.Info("flood", "i", i)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("2000 log calls took %v against a hung server; Handle must not block", elapsed)
	}
	if h.Dropped() < 1900 {
		t.Fatalf("Dropped() = %d, want most of the flood counted", h.Dropped())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := h.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() = %v, want the deadline error while the server hangs", err)
	}
}

func TestRetriesServerErrors(t *testing.T) {
	c := &collector{status: func(n int) int {
		if n <= 2 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}}
	h := newTestHandler(t, c, Options{})
	slog.New(h).Info("eventually")
	closeNow(t, h)
	if len(c.all()) != 1 || c.requests.Load() != 3 || h.Dropped() != 0 {
		t.Fatalf("sent %d after %d requests, dropped %d; want 1 after 3, 0 dropped", len(c.all()), c.requests.Load(), h.Dropped())
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	c := &collector{status: func(int) int { return http.StatusInternalServerError }}
	var errs atomic.Int32
	h := newTestHandler(t, c, Options{MaxRetries: 1, OnError: func(error) { errs.Add(1) }})
	slog.New(h).Info("lost")
	closeNow(t, h)
	if c.requests.Load() != 2 || h.Dropped() != 1 || errs.Load() != 1 {
		t.Fatalf("requests %d, dropped %d, errors %d; want 2, 1, 1", c.requests.Load(), h.Dropped(), errs.Load())
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	c := &collector{status: func(int) int { return http.StatusUnauthorized }}
	var got error
	var mu sync.Mutex
	h := newTestHandler(t, c, Options{OnError: func(err error) { mu.Lock(); got = err; mu.Unlock() }})
	slog.New(h).Info("a")
	slog.New(h).Info("b")
	closeNow(t, h)
	mu.Lock()
	defer mu.Unlock()
	if c.requests.Load() != 1 || h.Dropped() != 2 || got == nil || !strings.Contains(got.Error(), "401") {
		t.Fatalf("requests %d, dropped %d, err %v; want 1 request, 2 dropped, a 401 error", c.requests.Load(), h.Dropped(), got)
	}
}

func TestUnreachableServer(t *testing.T) {
	h, err := New(Options{URL: "http://127.0.0.1:1", MaxRetries: 1, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	slog.New(h).Info("nowhere")
	closeNow(t, h)
	if h.Dropped() != 1 {
		t.Fatalf("Dropped() = %d, want 1", h.Dropped())
	}
}

func TestHandleAfterClose(t *testing.T) {
	c := &collector{}
	h := newTestHandler(t, c, Options{})
	closeNow(t, h)
	closeNow(t, h) // twice is fine
	slog.New(h).Info("late")
	if h.Dropped() != 1 || len(c.all()) != 0 {
		t.Fatalf("dropped %d, sent %d; want the late record dropped", h.Dropped(), len(c.all()))
	}
}

// Invariant: every record is either sent or counted as dropped, even when
// Close races with loggers.
func TestEveryRecordIsSentOrDropped(t *testing.T) {
	for round := 0; round < 20; round++ {
		c := &collector{}
		h := newTestHandler(t, c, Options{BufferSize: 50, BatchSize: 7})
		logger := slog.New(h)
		var written atomic.Int64
		var stop atomic.Bool
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Keep logging through Close so calls overlap its drain.
				for !stop.Load() {
					logger.Info("x")
					written.Add(1)
				}
			}()
		}
		time.Sleep(time.Duration(round) * 100 * time.Microsecond)
		closeNow(t, h)
		stop.Store(true)
		wg.Wait()
		if sent, dropped := int64(len(c.all())), h.Dropped(); sent+dropped != written.Load() {
			t.Fatalf("round %d: sent %d + dropped %d != written %d", round, sent, dropped, written.Load())
		}
	}
}

type valuer struct{}

func (valuer) LogValue() slog.Value { return slog.StringValue("resolved") }

func TestAttrs(t *testing.T) {
	c := &collector{}
	h := newTestHandler(t, c, Options{})
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	logger := slog.New(h).With("app", "x", "nul\x00key", "a\x00b").WithGroup("req").With("id", 7)
	logger.Info("m",
		"user_id", 42,
		"ok", true,
		"ratio", 1.5,
		"nan", math.NaN(),
		"took", 1500*time.Millisecond,
		"at", when,
		"err", errors.New("boom"),
		"lazy", valuer{},
		"struct", struct{ A int }{3},
		slog.Group("inner", "k", "v"),
		slog.Group("", "inlined", 1),
		slog.Group("empty"),
		slog.Attr{}, // zero attr: ignored
	)
	closeNow(t, h)

	all := c.all()
	if len(all) != 1 {
		t.Fatalf("sent %d records", len(all))
	}
	var got map[string]any
	if err := json.Unmarshal(all[0].Attrs, &got); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	want := `{"app":"x","nul` + "�" + `key":"a` + "�" + `b","req":{"at":"2026-01-02T03:04:05Z","err":"boom","id":7,"inlined":1,"inner":{"k":"v"},"lazy":"resolved","nan":"NaN","ok":true,"ratio":1.5,"struct":{"A":3},"took":"1.5s","user_id":42}}`
	if string(b) != want {
		t.Fatalf("attrs =\n%s\nwant\n%s", b, want)
	}
}

func TestNoAttrsMeansNoAttrsField(t *testing.T) {
	c := &collector{}
	h := newTestHandler(t, c, Options{})
	slog.New(h).WithGroup("unused").Info("plain")
	closeNow(t, h)
	if a := c.all()[0].Attrs; a != nil {
		t.Fatalf("attrs = %s, want none", a)
	}
}

func TestOversizedAttrsAreReplaced(t *testing.T) {
	c := &collector{}
	h := newTestHandler(t, c, Options{})
	slog.New(h).Info("big", "blob", strings.Repeat("x", maxAttrsBytes))
	closeNow(t, h)
	if a := string(c.all()[0].Attrs); !strings.Contains(a, "golog_error") || len(a) > maxAttrsBytes {
		t.Fatalf("attrs = %.100s", a)
	}
}

func TestCleanMessage(t *testing.T) {
	if cleanMessage("") != emptyMessageText {
		t.Fatal("empty message not replaced")
	}
	if cleanMessage("a\x00b") != "a�b" {
		t.Fatal("NUL not replaced")
	}
	long := cleanMessage(strings.Repeat("ş", maxMessageChars+5))
	if n := utf8.RuneCountInString(long); n != maxMessageChars || !strings.HasSuffix(long, "…") {
		t.Fatalf("truncated to %d characters", n)
	}
}

func TestNewValidation(t *testing.T) {
	bad := []Options{
		{},
		{URL: "logs.example.com"},
		{URL: "ftp://x"},
		{URL: "http://x", Type: strings.Repeat("t", maxTypeChars+1)},
		{URL: "http://x", Type: "a\nb"},
		{URL: "http://x", Service: strings.Repeat("s", maxServiceChars+1)},
		{URL: "http://x", BatchSize: maxBatchEntries + 1},
		{URL: "http://x", BatchSize: -1},
		{URL: "http://x", MaxRetries: -1},
	}
	for _, o := range bad {
		if h, err := New(o); err == nil {
			h.Close(context.Background())
			t.Errorf("New(%+v) should fail", o)
		}
	}
}

func TestURLWithPath(t *testing.T) {
	var path atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path.Store(r.URL.Path)
		io.Copy(io.Discard, r.Body)
	}))
	defer ts.Close()
	h, err := New(Options{URL: ts.URL + "/golog/", FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	slog.New(h).Info("x")
	closeNow(t, h)
	if path.Load() != "/golog/api/logs/batch" {
		t.Fatalf("path = %v", path.Load())
	}
}

// Review finding: json.RawMessage and MarshalJSON output went through
// unchanged, so a lone surrogate or a huge number made the server refuse the
// whole batch.
func TestRawJSONValuesAreMadeStorable(t *testing.T) {
	c := &collector{}
	h := newTestHandler(t, c, Options{})
	slog.New(h).Info("m", "raw", json.RawMessage(`{"s":"x\ud800y","huge":1e1000000,"ok":1.50}`))
	closeNow(t, h)
	raw := string(c.all()[0].Attrs)
	if strings.Contains(raw, `\ud800`) || !strings.Contains(raw, `"huge":"1e1000000"`) || !strings.Contains(raw, `"ok":1.50`) {
		t.Fatalf("attrs = %s; want the surrogate replaced, the huge number as text, normal numbers kept", raw)
	}
}

// Review finding: the batch byte budget counted characters, but JSON escapes
// <, > and & as six bytes each, so batches could pass the server's 2 MiB cap.
func TestBatchesStayUnderServerBodyCap(t *testing.T) {
	var maxBody atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if n := int64(len(b)); n > maxBody.Load() {
			maxBody.Store(n)
		}
	}))
	defer ts.Close()
	h, err := New(Options{URL: ts.URL, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(h)
	msg := strings.Repeat("<", maxMessageChars)
	for i := 0; i < 60; i++ {
		logger.Info(msg, "blob", strings.Repeat("&", 2000))
	}
	closeNow(t, h)
	if got := maxBody.Load(); got > 2<<20 {
		t.Fatalf("largest request body = %d bytes, over the server's 2 MiB cap", got)
	}
}

// Review finding: one record whose time JSON cannot encode lost the batch.
func TestUnencodableTimeDoesNotLoseTheBatch(t *testing.T) {
	c := &collector{}
	h := newTestHandler(t, c, Options{})
	bad := slog.NewRecord(time.Date(10001, 1, 1, 0, 0, 0, 0, time.UTC), slog.LevelInfo, "far future", 0)
	h.Handle(context.Background(), bad)
	slog.New(h).Info("normal")
	closeNow(t, h)
	if all := c.all(); len(all) != 2 || h.Dropped() != 0 {
		t.Fatalf("sent %d, dropped %d; want both records sent", len(all), h.Dropped())
	}
}

// serverStore captures what the real server accepts.
type serverStore struct {
	mu  sync.Mutex
	got []models.Log
}

func (s *serverStore) GetLogs(context.Context, models.LogFilter) ([]models.Log, error) {
	return nil, nil
}
func (s *serverStore) InsertLog(context.Context, models.Log) (int, error) { return 0, nil }
func (s *serverStore) InsertLogs(_ context.Context, e []models.Log) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, e...)
	return make([]int, len(e)), nil
}
func (s *serverStore) Facets(context.Context) (models.Facets, error) { return models.Facets{}, nil }
func (s *serverStore) ListenForLogs(context.Context, chan<- models.LogEvent) error {
	return nil
}

// The server validates every entry and rejects the whole batch on one bad
// entry, so whatever the handler builds must pass that validation.
func TestRealServerAcceptsWhatTheHandlerSends(t *testing.T) {
	store := &serverStore{}
	srv := handlers.NewServer(store, handlers.Options{Auth: handlers.Auth{WriteToken: "write-token-0123456789"}})
	ts := httptest.NewServer(srv.SetupRoutes())
	defer ts.Close()

	var errs []error
	var mu sync.Mutex
	h, err := New(Options{
		URL: ts.URL, Token: "write-token-0123456789", Service: "svc", Type: "PAYMENT", FlushInterval: time.Hour,
		Level:   slog.LevelDebug,
		OnError: func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(h)
	logger.Info("")
	logger.Info("nul \x00 inside", "k\x00", "v\x00")
	logger.Info(strings.Repeat("ü", maxMessageChars+100))
	logger.Info("big attrs", "blob", strings.Repeat("x", maxAttrsBytes))
	logger.Info("nan", "f", math.Inf(1))
	logger.Debug("debug", slog.Group("g", "a", 1))
	logger.Info("raw json", "raw", json.RawMessage(`{"s":"\ud800","n":1e1000000}`))
	closeNow(t, h)

	mu.Lock()
	defer mu.Unlock()
	if len(errs) > 0 || h.Dropped() != 0 {
		t.Fatalf("server refused records: %v (dropped %d)", errs, h.Dropped())
	}
	if len(store.got) != 7 {
		t.Fatalf("server stored %d records, want 7", len(store.got))
	}
}
