package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/mstgnz/golog/v2/models"
)

const (
	// maxBodyBytes fits one maximum entry: 10000 characters escaped as \uXXXX
	// (6 bytes each) plus 16 KiB of attrs and JSON overhead.
	maxBodyBytes = 128 << 10
	// maxBatchBodyBytes and maxBatchEntries bound POST /api/logs/batch.
	maxBatchBodyBytes = 2 << 20
	maxBatchEntries   = 100
	// clientBuffer is how many events a stream client may lag before it is dropped.
	clientBuffer = 256
	// defaultHeartbeat keeps idle streams alive through proxies that cut
	// silent connections (commonly after 60s).
	defaultHeartbeat = 30 * time.Second
	// defaultStreamWriteTimeout bounds one SSE write, so a client that stops
	// reading releases its handler instead of holding it (and shutdown) forever.
	defaultStreamWriteTimeout = 10 * time.Second
)

// LogStore is the interface for log persistence operations.
type LogStore interface {
	GetLogs(ctx context.Context, filter models.LogFilter) ([]models.Log, error)
	InsertLog(ctx context.Context, logEntry models.Log) (int, error)
	// InsertLogs inserts all entries or none and returns ids in input order.
	InsertLogs(ctx context.Context, entries []models.Log) ([]int, error)
	Facets(ctx context.Context) (models.Facets, error)
	// ListenForLogs must close ch when ctx is cancelled.
	ListenForLogs(ctx context.Context, ch chan<- models.LogEvent) error
}

// Options configures optional server behaviour.
type Options struct {
	// CORSAllowedOrigins enables CORS for these origins. Empty means no CORS
	// headers: the bundled dashboard is same-origin and does not need them.
	CORSAllowedOrigins []string
	// Static is served at / when set.
	Static fs.FS
	Auth   Auth
}

// Server holds application state and serves HTTP requests.
type Server struct {
	store        LogStore
	opts         Options
	hub          *hub
	logins       *loginLimiter
	heartbeat    time.Duration
	writeTimeout time.Duration // per write on a stream
}

// NewServer creates a Server backed by the given store.
func NewServer(store LogStore, opts Options) *Server {
	return &Server{
		store:        store,
		opts:         opts,
		hub:          newHub(clientBuffer),
		logins:       newLoginLimiter(),
		heartbeat:    defaultHeartbeat,
		writeTimeout: defaultStreamWriteTimeout,
	}
}

// SetupRoutes registers all HTTP routes and returns the handler.
func (s *Server) SetupRoutes() http.Handler {
	return s.router()
}

func (s *Server) router() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	if len(s.opts.CORSAllowedOrigins) > 0 {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins: s.opts.CORSAllowedOrigins,
			AllowedMethods: []string{"GET", "POST", "OPTIONS"},
			AllowedHeaders: []string{"Accept", "Authorization", "Content-Type"},
			MaxAge:         300,
		}))
	}

	r.Route("/api", func(r chi.Router) {
		// Public: the dashboard needs these to log in.
		r.Get("/session", s.GetSessionHandler)
		r.Post("/session", s.LoginHandler)
		r.Delete("/session", s.LogoutHandler)

		r.Group(func(r chi.Router) {
			r.Use(s.requireRead)
			r.Get("/logs", s.GetLogsHandler)
			r.Get("/logs/stream", s.StreamLogsHandler)
			r.Get("/facets", s.FacetsHandler)
		})
		r.Group(func(r chi.Router) {
			r.Use(s.requireWrite)
			r.Post("/logs", s.AddLogHandler)
			r.Post("/logs/batch", s.AddLogsBatchHandler)
		})
	})

	if s.opts.Static != nil {
		r.Handle("/*", http.FileServer(http.FS(s.opts.Static)))
	}

	return r
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

const attrParamPrefix = "attr."

// parseFilter reads level, type, service, q, attr.<key>, limit and offset
// from the query string.
func parseFilter(r *http.Request) (models.LogFilter, error) {
	q := r.URL.Query()
	filter := models.LogFilter{
		Level:   q.Get("level"),
		Type:    q.Get("type"),
		Service: q.Get("service"),
		Query:   q.Get("q"),
	}
	for key, vals := range q {
		name, ok := strings.CutPrefix(key, attrParamPrefix)
		if !ok {
			continue
		}
		if len(vals) != 1 {
			return filter, fmt.Errorf("%s given more than once", key)
		}
		if filter.Attrs == nil {
			filter.Attrs = map[string]string{}
		}
		filter.Attrs[name] = vals[0]
	}
	if err := filter.Validate(); err != nil {
		return filter, err
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return filter, errors.New("limit must be a positive integer")
		}
		filter.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return filter, errors.New("offset must be a non-negative integer")
		}
		filter.Offset = n
	}
	return filter, nil
}

// internalError logs the cause and sends the client a generic message.
func internalError(w http.ResponseWriter, what string, err error) {
	log.Printf("golog: %s: %v", what, err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// GetLogsHandler returns log entries with optional filtering and pagination.
//
// Query parameters: level, type, service, q, attr.<key>, limit (default 100,
// max 500), offset (default 0).
func (s *Server) GetLogsHandler(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	logs, err := s.store.GetLogs(r.Context(), filter)
	if err != nil {
		internalError(w, "get logs", err)
		return
	}
	if logs == nil {
		logs = []models.Log{}
	}

	writeJSON(w, http.StatusOK, logs)
}

// FacetsHandler returns the types and services seen in recent logs.
func (s *Server) FacetsHandler(w http.ResponseWriter, r *http.Request) {
	facets, err := s.store.Facets(r.Context())
	if err != nil {
		internalError(w, "facets", err)
		return
	}
	writeJSON(w, http.StatusOK, facets)
}

// decodeBody reads JSON into v with a size cap. It writes the error response
// itself and reports whether decoding succeeded.
func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// AddLogHandler inserts a new log entry.
func (s *Server) AddLogHandler(w http.ResponseWriter, r *http.Request) {
	var logEntry models.Log
	if !decodeBody(w, r, maxBodyBytes, &logEntry) {
		return
	}
	if err := logEntry.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	id, err := s.store.InsertLog(r.Context(), logEntry)
	if err != nil {
		internalError(w, "insert log", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"id": id})
}

// AddLogsBatchHandler inserts up to maxBatchEntries entries, all or none.
// Body: a JSON array of entries. Response: {"ids": [...]} in input order.
func (s *Server) AddLogsBatchHandler(w http.ResponseWriter, r *http.Request) {
	var entries []models.Log
	if !decodeBody(w, r, maxBatchBodyBytes, &entries) {
		return
	}
	if len(entries) == 0 || len(entries) > maxBatchEntries {
		http.Error(w, fmt.Sprintf("a batch holds 1 to %d entries", maxBatchEntries), http.StatusBadRequest)
		return
	}
	for i := range entries {
		if err := entries[i].Validate(); err != nil {
			http.Error(w, fmt.Sprintf("entry %d: %v", i, err), http.StatusBadRequest)
			return
		}
	}

	ids, err := s.store.InsertLogs(r.Context(), entries)
	if err != nil {
		internalError(w, "insert logs", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]int{"ids": ids})
}

// StreamLogsHandler streams log entries to the client using Server-Sent Events.
//
// Query parameters: the same filters as GetLogsHandler, applied server-side.
// Besides `data:` events it sends `event: resync` when entries may have been
// missed (the client should reload the list) and `: ping` comments as heartbeat.
func (s *Server) StreamLogsHandler(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rc := http.NewResponseController(w)
	// The stream outlives the server's WriteTimeout, so each write gets its own
	// deadline instead. (net/http already clears the read deadline once the
	// handler runs.) ErrNotSupported (e.g. a test recorder) is harmless.
	extendWriteDeadline := func() error {
		err := rc.SetWriteDeadline(time.Now().Add(s.writeTimeout))
		if errors.Is(err, http.ErrNotSupported) {
			return nil
		}
		return err
	}
	if err := extendWriteDeadline(); err != nil {
		internalError(w, "stream write deadline", err)
		return
	}

	c, ok := s.hub.add()
	if !ok {
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	defer s.hub.remove(c)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // stop nginx from buffering the stream
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		log.Printf("golog: stream flush: %v", err)
		return
	}

	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()

	for {
		var payload []byte
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			payload = []byte(": ping\n\n")
		case ev, ok := <-c.send:
			if !ok {
				return
			}
			switch {
			case ev.Resync:
				payload = []byte("event: resync\ndata: {}\n\n")
			case filter.Matches(ev.Log):
				data, err := json.Marshal(ev.Log)
				if err != nil {
					log.Printf("golog: marshal log %d: %v", ev.Log.ID, err)
					continue
				}
				payload = append(append([]byte("data: "), data...), '\n', '\n')
			default:
				continue
			}
		}
		if err := extendWriteDeadline(); err != nil {
			return
		}
		if _, err := w.Write(payload); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

// StartLogListener subscribes to the store and broadcasts each event to all
// connected SSE clients. When ctx is cancelled the store closes the channel,
// every stream ends and new streams get 503, so a graceful HTTP shutdown is
// not held open by long-lived connections.
func (s *Server) StartLogListener(ctx context.Context) error {
	events := make(chan models.LogEvent, 64)
	if err := s.store.ListenForLogs(ctx, events); err != nil {
		return err
	}
	go func() {
		for ev := range events {
			s.hub.broadcast(ev)
		}
		s.hub.close()
	}()
	return nil
}
