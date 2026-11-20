// Package gologslog sends log/slog records to a GoLog server.
//
// Records are buffered in memory and sent in batches in the background, so a
// log call never waits on the network. When GoLog is slow or down, records
// are retried a few times and then dropped; Dropped reports how many.
//
//	h, err := gologslog.New(gologslog.Options{
//		URL:     "https://logs.example.com",
//		Token:   os.Getenv("GOLOG_WRITE_TOKEN"),
//		Service: "billing",
//	})
//	if err != nil { ... }
//	defer h.Close(context.Background()) // sends what is still buffered
//	logger := slog.New(h)
package gologslog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits mirror the server's validation; an entry over them would make the
// server reject the whole batch.
const (
	maxBatchEntries  = 100
	maxBatchBytes    = 1 << 20 // well under the server's 2 MiB batch body cap
	maxMessageChars  = 10000
	maxTypeChars     = 50
	maxServiceChars  = 100
	maxAttrsBytes    = 16 << 10
	defaultType      = "APP"
	defaultBuffer    = 10000
	defaultInterval  = time.Second
	defaultRetries   = 3
	firstRetryDelay  = 200 * time.Millisecond
	emptyMessageText = "(no message)"
)

// Options configures a Handler. URL is required.
type Options struct {
	URL     string // base URL of the GoLog server
	Token   string // write token (GOLOG_WRITE_TOKEN); empty if the server has none
	Service string // sent with every record, e.g. the app name
	Type    string // sent with every record; default "APP"
	// Level is the minimum level sent; default slog.LevelInfo.
	Level slog.Leveler
	// BatchSize is the most records per request (1-100, default 100).
	BatchSize int
	// FlushInterval sends a partial batch after this long (default 1s).
	FlushInterval time.Duration
	// BufferSize is how many records may wait in memory (default 10000).
	// When it is full, new records are dropped.
	BufferSize int
	// MaxRetries for a batch on network errors, 429 and 5xx (default 3).
	MaxRetries int
	// HTTPClient sends the batches. The default has connect, TLS and
	// response timeouts.
	HTTPClient *http.Client
	// OnError, if set, is called from the sender goroutine when a batch is
	// dropped. It must not block.
	OnError func(error)
}

// Handler is a slog.Handler. Handlers made by WithAttrs and WithGroup share
// the buffer and sender of the one New returned.
type Handler struct {
	s    *sender
	goas []groupOrAttrs
}

type groupOrAttrs struct {
	group string
	attrs []slog.Attr
}

// entry is the wire format of POST /api/logs/batch.
type entry struct {
	Timestamp time.Time       `json:"timestamp"`
	Level     string          `json:"level"`
	Type      string          `json:"type"`
	Service   string          `json:"service,omitempty"`
	Message   string          `json:"message"`
	Attrs     json.RawMessage `json:"attrs,omitempty"`
}

// New validates opts and starts the background sender.
func New(opts Options) (*Handler, error) {
	u, err := url.Parse(strings.TrimRight(opts.URL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("gologslog: URL must be an http(s) URL, got %q", opts.URL)
	}
	if opts.Type == "" {
		opts.Type = defaultType
	}
	if err := checkName("Type", opts.Type, maxTypeChars); err != nil {
		return nil, err
	}
	if err := checkName("Service", opts.Service, maxServiceChars); err != nil {
		return nil, err
	}
	if opts.BatchSize == 0 {
		opts.BatchSize = maxBatchEntries
	}
	if opts.BatchSize < 1 || opts.BatchSize > maxBatchEntries {
		return nil, fmt.Errorf("gologslog: BatchSize must be 1-%d", maxBatchEntries)
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = defaultInterval
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = defaultBuffer
	}
	if opts.MaxRetries < 0 {
		return nil, errors.New("gologslog: MaxRetries must not be negative")
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = defaultRetries
	}
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = defaultClient()
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/logs/batch"

	s := &sender{
		opts:     opts,
		endpoint: u.String(),
		queue:    make(chan entry, opts.BufferSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	go s.run()
	return &Handler{s: s}, nil
}

func checkName(field, v string, max int) error {
	if utf8.RuneCountInString(v) > max {
		return fmt.Errorf("gologslog: %s exceeds %d characters", field, max)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return fmt.Errorf("gologslog: %s must not contain control characters", field)
		}
	}
	return nil
}

func defaultClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   3 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			IdleConnTimeout:       60 * time.Second,
			MaxIdleConnsPerHost:   2,
		},
	}
}

// Enabled reports whether level is at or above Options.Level.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.s.opts.Level.Level()
}

// Handle queues the record. It never blocks: if the buffer is full or the
// handler is closed, the record is dropped and counted.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	e := entry{
		Timestamp: r.Time,
		Level:     levelName(r.Level),
		Type:      h.s.opts.Type,
		Service:   h.s.opts.Service,
		Message:   cleanMessage(r.Message),
		Attrs:     h.attrsJSON(r),
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	// The read lock makes "not closed" and "queued" one step, so Close
	// cannot drain the queue between them and strand this record.
	h.s.mu.RLock()
	defer h.s.mu.RUnlock()
	if h.s.closed {
		h.s.dropped.Add(1)
		return nil
	}
	select {
	case h.s.queue <- e:
	default:
		h.s.dropped.Add(1)
	}
	return nil
}

// WithAttrs returns a Handler that adds attrs to every record.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.with(groupOrAttrs{attrs: attrs})
}

// WithGroup returns a Handler that nests later attrs under name.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.with(groupOrAttrs{group: name})
}

func (h *Handler) with(g groupOrAttrs) *Handler {
	goas := make([]groupOrAttrs, len(h.goas), len(h.goas)+1)
	copy(goas, h.goas)
	return &Handler{s: h.s, goas: append(goas, g)}
}

// Dropped returns how many records were not delivered: buffer full, handler
// closed, or the server refused or could not be reached after retries.
func (h *Handler) Dropped() int64 { return h.s.dropped.Load() }

// Close stops accepting records and sends what is buffered, waiting until
// ctx ends. Records still unsent then are dropped. Safe to call twice.
func (h *Handler) Close(ctx context.Context) error {
	h.s.mu.Lock()
	if !h.s.closed {
		h.s.closed = true
		close(h.s.stop)
	}
	h.s.mu.Unlock()
	select {
	case <-h.s.done:
		return nil
	case <-ctx.Done():
		h.s.cancel() // abort in-flight requests and retries
		<-h.s.done
		return ctx.Err()
	}
}

func levelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DEBUG"
	case l < slog.LevelWarn:
		return "INFO"
	case l < slog.LevelError:
		return "WARNING"
	default:
		return "ERROR"
	}
}

// cleanMessage makes the message acceptable to the server: not empty, no
// NUL, at most maxMessageChars characters.
func cleanMessage(m string) string {
	if m == "" {
		return emptyMessageText
	}
	m = strings.ReplaceAll(m, "\x00", "�")
	if utf8.RuneCountInString(m) > maxMessageChars {
		r := []rune(m)
		m = string(r[:maxMessageChars-1]) + "…"
	}
	return m
}

func (h *Handler) attrsJSON(r slog.Record) json.RawMessage {
	root := map[string]any{}
	cur := root
	for _, g := range h.goas {
		if g.group != "" {
			next := map[string]any{}
			cur[cleanKey(g.group)] = next
			cur = next
			continue
		}
		addAttrs(cur, g.attrs)
	}
	r.Attrs(func(a slog.Attr) bool {
		addAttrs(cur, []slog.Attr{a})
		return true
	})
	pruneEmpty(root)
	if len(root) == 0 {
		return nil
	}
	b, err := json.Marshal(root)
	if err != nil {
		b, _ = json.Marshal(map[string]string{"golog_error": "attrs could not be encoded: " + err.Error()})
	}
	if len(b) > maxAttrsBytes {
		b, _ = json.Marshal(map[string]string{"golog_error": fmt.Sprintf("attrs dropped: %d bytes is over the %d byte limit", len(b), maxAttrsBytes)})
	}
	return b
}

func addAttrs(m map[string]any, attrs []slog.Attr) {
	for _, a := range attrs {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			group := v.Group()
			if len(group) == 0 {
				continue
			}
			if a.Key == "" { // inline, per slog rules
				addAttrs(m, group)
				continue
			}
			sub, ok := m[cleanKey(a.Key)].(map[string]any)
			if !ok {
				sub = map[string]any{}
				m[cleanKey(a.Key)] = sub
			}
			addAttrs(sub, group)
			continue
		}
		if a.Key == "" {
			continue
		}
		m[cleanKey(a.Key)] = value(v)
	}
}

func pruneEmpty(m map[string]any) {
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			pruneEmpty(sub)
			if len(sub) == 0 {
				delete(m, k)
			}
		}
	}
}

// cleanKey and cleanString remove NUL, which PostgreSQL jsonb cannot store.
func cleanKey(k string) string { return strings.ReplaceAll(k, "\x00", "�") }

func cleanString(s string) string { return strings.ReplaceAll(s, "\x00", "�") }

func value(v slog.Value) any {
	switch v.Kind() {
	case slog.KindString:
		return cleanString(v.String())
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		f := v.Float64()
		if math.IsNaN(f) || math.IsInf(f, 0) { // not representable in JSON
			return fmt.Sprint(f)
		}
		return f
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	}
	a := v.Any()
	if err, ok := a.(error); ok {
		return cleanString(err.Error())
	}
	if b, err := json.Marshal(a); err == nil {
		if s, ok := storableJSON(b); ok {
			return s
		}
	}
	return cleanString(fmt.Sprintf("%+v", a))
}

// storableJSON decodes marshalled bytes (json.RawMessage and MarshalJSON
// output pass through Marshal untouched) into values the server's jsonb
// accepts: lone surrogates become U+FFFD, NUL is replaced, and numbers jsonb
// cannot store become text.
func storableJSON(b []byte) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return storableValue(v), true
}

func storableValue(v any) any {
	switch t := v.(type) {
	case string:
		return cleanString(t)
	case json.Number:
		if storableNumber(t.String()) {
			return t
		}
		return t.String()
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[cleanKey(k)] = storableValue(child)
		}
		return out
	case []any:
		for i := range t {
			t[i] = storableValue(t[i])
		}
		return t
	}
	return v
}

// storableNumber mirrors models.StorableNumber (this package stays
// standard-library only): at most 100 characters, exponent within ±1000.
func storableNumber(n string) bool {
	if len(n) > 100 {
		return false
	}
	i := strings.IndexAny(n, "eE")
	if i < 0 {
		return true
	}
	exp, err := strconv.Atoi(n[i+1:])
	return err == nil && exp >= -1000 && exp <= 1000
}

type sender struct {
	opts     Options
	endpoint string
	queue    chan entry
	dropped  atomic.Int64

	mu     sync.RWMutex // guards closed; held for reading while enqueueing
	closed bool
	stop   chan struct{} // closed by Close
	done   chan struct{} // closed when the sender has finished
	ctx    context.Context
	cancel context.CancelFunc
}

func (s *sender) run() {
	defer close(s.done)
	defer s.cancel()
	ticker := time.NewTicker(s.opts.FlushInterval)
	defer ticker.Stop()

	// Entries are encoded one by one: the batch budget counts real bytes, and
	// one entry that cannot be encoded does not cost the others.
	var batch [][]byte
	size := 2 // the enclosing [ ]
	flush := func() {
		if len(batch) > 0 {
			s.send(batch)
			batch, size = nil, 2
		}
	}
	add := func(e entry) {
		b, err := json.Marshal(e)
		if err != nil { // e.g. a year outside 0-9999, which JSON times cannot hold
			e.Timestamp = time.Now()
			if b, err = json.Marshal(e); err != nil {
				s.drop(1, fmt.Errorf("gologslog: encode record: %w", err))
				return
			}
		}
		if len(batch) > 0 && size+len(b)+1 > maxBatchBytes {
			flush()
		}
		batch = append(batch, b)
		size += len(b) + 1
		if len(batch) >= s.opts.BatchSize {
			flush()
		}
	}

	for {
		select {
		case e := <-s.queue:
			add(e)
		case <-ticker.C:
			flush()
		case <-s.stop:
			// Handle no longer enqueues; drain what is buffered and send it.
			for {
				select {
				case e := <-s.queue:
					add(e)
				default:
					flush()
					return
				}
			}
		}
	}
}

// send delivers one batch of encoded entries, retrying network errors, 429
// and 5xx. Anything else, or running out of retries, drops the batch.
func (s *sender) send(batch [][]byte) {
	body := make([]byte, 0, len(batch)*256)
	body = append(body, '[')
	body = append(body, bytes.Join(batch, []byte{','})...)
	body = append(body, ']')
	delay := firstRetryDelay
	for attempt := 0; ; attempt++ {
		retry, err := s.post(body)
		if err == nil {
			return
		}
		if !retry || attempt >= s.opts.MaxRetries || s.ctx.Err() != nil {
			s.drop(len(batch), err)
			return
		}
		select {
		case <-time.After(delay):
			delay *= 2
		case <-s.ctx.Done():
			s.drop(len(batch), s.ctx.Err())
			return
		}
	}
}

func (s *sender) post(body []byte) (retry bool, err error) {
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.opts.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.opts.Token)
	}
	resp, err := s.opts.HTTPClient.Do(req)
	if err != nil {
		return true, fmt.Errorf("gologslog: send: %w", err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode == http.StatusOK:
		return false, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return true, fmt.Errorf("gologslog: server answered %s", resp.Status)
	default:
		return false, fmt.Errorf("gologslog: server refused the batch: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
}

func (s *sender) drop(records int, err error) {
	s.dropped.Add(int64(records))
	if s.opts.OnError != nil {
		s.opts.OnError(err)
	}
}
