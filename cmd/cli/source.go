package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/mstgnz/golog/v2/config"
	"github.com/mstgnz/golog/v2/database"
	"github.com/mstgnz/golog/v2/models"
)

// source delivers the recent history and the live stream of logs.
type source interface {
	History(ctx context.Context, filter models.LogFilter) ([]models.Log, error)
	// Stream starts sending events to ch and closes ch when ctx ends. An
	// error means the stream could not be started at all.
	Stream(ctx context.Context, filter models.LogFilter, ch chan<- models.LogEvent) error
	Close() error
}

// dbSource reads PostgreSQL directly, as golog-cli always did.
type dbSource struct{ store *database.Store }

func openDBSource(ctx context.Context) (*dbSource, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	store, err := database.Open(ctx, cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &dbSource{store: store}, nil
}

func (s *dbSource) History(ctx context.Context, filter models.LogFilter) ([]models.Log, error) {
	logs, err := s.store.GetLogs(ctx, filter)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "42P01" { // undefined_table
		return nil, fmt.Errorf("the logs table does not exist yet; start golog-server once to create it: %w", err)
	}
	return logs, err
}

func (s *dbSource) Stream(ctx context.Context, _ models.LogFilter, ch chan<- models.LogEvent) error {
	return s.store.ListenForLogs(ctx, ch) // unfiltered; the printer filters
}

func (s *dbSource) Close() error { return s.store.Close() }

const (
	historyTimeout = 30 * time.Second
	// streamIdleTimeout: the server pings every 30s, so three missed pings
	// mean the connection is dead even if TCP has not noticed.
	streamIdleTimeout = 90 * time.Second
	minRetryDelay     = time.Second
	maxRetryDelay     = 30 * time.Second
	// maxEventBytes fits one maximum log entry as JSON.
	maxEventBytes = 1 << 20
)

// httpSource reads a golog-server over its API with the read token.
type httpSource struct {
	base   *url.URL
	token  string
	client *http.Client
	// idleTimeout and retry delays are fields so tests can shorten them.
	idleTimeout time.Duration
	minRetry    time.Duration
	maxRetry    time.Duration
}

func newHTTPSource(rawURL, token string) (*httpSource, error) {
	u, err := url.Parse(strings.TrimRight(rawURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("-url must be an http(s) URL like https://logs.example.com, got %q", rawURL)
	}
	if u.User != nil {
		return nil, errors.New("-url must not contain credentials; put the read token in GOLOG_READ_TOKEN")
	}
	// No overall Timeout: the stream is meant to stay open. Each phase that
	// can hang has its own limit instead.
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	}
	return &httpSource{
		base: u, token: token, client: &http.Client{Transport: transport},
		idleTimeout: streamIdleTimeout, minRetry: minRetryDelay, maxRetry: maxRetryDelay,
	}, nil
}

func (s *httpSource) Close() error {
	s.client.CloseIdleConnections()
	return nil
}

// filterQuery encodes a filter the way the server's parseFilter reads it.
func filterQuery(f models.LogFilter) url.Values {
	q := url.Values{}
	for k, v := range map[string]string{"level": f.Level, "type": f.Type, "service": f.Service, "q": f.Query} {
		if v != "" {
			q.Set(k, v)
		}
	}
	for k, v := range f.Attrs {
		q.Set("attr."+k, v)
	}
	return q
}

func (s *httpSource) newRequest(ctx context.Context, path string, q url.Values) (*http.Request, error) {
	u := *s.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	return req, nil
}

// statusError turns a non-200 response into a readable error.
func statusError(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("the server refused the read token (401); set GOLOG_READ_TOKEN")
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	return fmt.Errorf("server answered %s: %s", resp.Status, sanitize(strings.TrimSpace(string(body))))
}

func (s *httpSource) History(ctx context.Context, filter models.LogFilter) ([]models.Log, error) {
	ctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	req, err := s.newRequest(ctx, "/api/logs", filterQuery(filter))
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	var logs []models.Log
	if err := json.NewDecoder(resp.Body).Decode(&logs); err != nil {
		return nil, fmt.Errorf("decode logs: %w", err)
	}
	return logs, nil
}

// Stream connects once synchronously, so a wrong URL or token fails right
// away, then keeps the stream alive in the background: after a lost
// connection it reconnects with backoff and sends Resync.
func (s *httpSource) Stream(ctx context.Context, filter models.LogFilter, ch chan<- models.LogEvent) error {
	q := filterQuery(filter)
	body, err := s.connect(ctx, q)
	if err != nil {
		return err
	}
	go func() {
		defer close(ch)
		delay := s.minRetry
		for {
			s.readEvents(ctx, body, ch)
			body.Close()
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
				if body, err = s.connect(ctx, q); err == nil {
					break
				}
				delay = min(delay*2, s.maxRetry)
			}
			delay = s.minRetry
			select {
			case ch <- models.LogEvent{Resync: true}:
			case <-ctx.Done():
				body.Close()
				return
			}
		}
	}()
	return nil
}

func (s *httpSource) connect(ctx context.Context, q url.Values) (io.ReadCloser, error) {
	req, err := s.newRequest(ctx, "/api/logs/stream", q)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, statusError(resp)
	}
	return resp.Body, nil
}

// readEvents parses SSE until the body ends, ctx ends, or nothing (not even
// a ping) arrives for idleTimeout.
func (s *httpSource) readEvents(ctx context.Context, body io.ReadCloser, ch chan<- models.LogEvent) {
	idle := time.AfterFunc(s.idleTimeout, func() { body.Close() })
	defer idle.Stop()
	stop := context.AfterFunc(ctx, func() { body.Close() })
	defer stop()

	// sc.Err() is not checked: EOF, a read error or an oversized line all
	// end this connection the same way, and the caller reconnects.
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), maxEventBytes)
	var event, data string
	for sc.Scan() {
		idle.Reset(s.idleTimeout)
		line := sc.Text()
		switch {
		case line == "":
			if ev, ok := toEvent(event, data); ok {
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
			event, data = "", ""
		case strings.HasPrefix(line, ":"): // comment / heartbeat
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data += strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
		}
	}
}

func toEvent(event, data string) (models.LogEvent, bool) {
	switch event {
	case "resync":
		return models.LogEvent{Resync: true}, true
	case "", "message":
		var l models.Log
		if data == "" || json.Unmarshal([]byte(data), &l) != nil {
			return models.LogEvent{}, false
		}
		return models.LogEvent{Log: l}, true
	}
	return models.LogEvent{}, false
}

// plainHTTPWithToken reports a token about to travel unencrypted to a
// host other than this machine.
func plainHTTPWithToken(rawURL, token string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || token == "" || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// attrFlag collects repeated -attr key=value flags.
type attrFlag map[string]string

func (a attrFlag) String() string {
	parts := make([]string, 0, len(a))
	for k, v := range a {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func (a attrFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("want key=value, got %q", s)
	}
	if _, dup := a[k]; dup {
		return fmt.Errorf("attr %q given twice", k)
	}
	a[k] = v
	return nil
}
