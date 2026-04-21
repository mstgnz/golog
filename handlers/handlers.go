package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/mstgnz/golog/models"
)

// LogStore is the interface for log persistence operations.
type LogStore interface {
	GetLogs(filter models.LogFilter) ([]models.Log, error)
	InsertLog(logEntry models.Log) (int, error)
	ListenForLogs(ctx context.Context, ch chan<- models.Log) error
}

// Client represents a connected SSE client.
type Client struct {
	send chan models.Log
}

// Server holds application state and serves HTTP requests.
type Server struct {
	store     LogStore
	clients   map[*Client]bool
	clientsMu sync.Mutex
	logChan   chan models.Log
}

// NewServer creates a Server backed by the given store.
func NewServer(store LogStore) *Server {
	return &Server{
		store:   store,
		clients: make(map[*Client]bool),
		logChan: make(chan models.Log),
	}
}

// SetupRoutes registers all HTTP routes and returns the handler.
func (s *Server) SetupRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}).Handler)

	r.Route("/api", func(r chi.Router) {
		r.Get("/logs", s.GetLogsHandler)
		r.Post("/logs", s.AddLogHandler)
		r.Get("/logs/stream", s.StreamLogsHandler)
	})

	fileServer := http.FileServer(http.Dir("./web/static"))
	r.Handle("/*", fileServer)

	return r
}

// GetLogsHandler returns log entries with optional filtering and pagination.
//
// Query parameters: level, type, limit (default 100, max 500), offset (default 0).
func (s *Server) GetLogsHandler(w http.ResponseWriter, r *http.Request) {
	filter := models.LogFilter{
		Level: r.URL.Query().Get("level"),
		Type:  r.URL.Query().Get("type"),
	}

	if filter.Level != "" && !models.ValidLevels[filter.Level] {
		http.Error(w, "invalid level", http.StatusBadRequest)
		return
	}
	if filter.Type != "" && !models.ValidTypes[filter.Type] {
		http.Error(w, "invalid type", http.StatusBadRequest)
		return
	}

	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		filter.Limit = n
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "offset must be a non-negative integer", http.StatusBadRequest)
			return
		}
		filter.Offset = n
	}

	logs, err := s.store.GetLogs(filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if logs == nil {
		logs = []models.Log{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(logs); err != nil {
		log.Printf("Error encoding logs response: %v", err)
	}
}

// AddLogHandler inserts a new log entry.
func (s *Server) AddLogHandler(w http.ResponseWriter, r *http.Request) {
	var logEntry models.Log
	if err := json.NewDecoder(r.Body).Decode(&logEntry); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := logEntry.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	id, err := s.store.InsertLog(logEntry)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]int{"id": id}); err != nil {
		log.Printf("Error encoding add log response: %v", err)
	}
}

// StreamLogsHandler streams log entries to the client using Server-Sent Events.
//
// Query parameters: level, type (optional filters applied server-side).
func (s *Server) StreamLogsHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	level := r.URL.Query().Get("level")
	logType := r.URL.Query().Get("type")

	client := &Client{send: make(chan models.Log, 256)}
	s.clientsMu.Lock()
	s.clients[client] = true
	s.clientsMu.Unlock()

	defer func() {
		s.clientsMu.Lock()
		delete(s.clients, client)
		close(client.send)
		s.clientsMu.Unlock()
	}()

	for {
		select {
		case logEntry, ok := <-client.send:
			if !ok {
				return
			}
			if (level == "" || logEntry.Level == level) && (logType == "" || logEntry.Type == logType) {
				data, err := json.Marshal(logEntry)
				if err != nil {
					log.Printf("Error marshaling log entry: %v", err)
					continue
				}
				if _, err := w.Write([]byte("data: " + string(data) + "\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

// StartLogListener subscribes to the store's notification channel and broadcasts
// each log entry to all connected SSE clients.
func (s *Server) StartLogListener(ctx context.Context) error {
	if err := s.store.ListenForLogs(ctx, s.logChan); err != nil {
		return err
	}
	go func() {
		for logEntry := range s.logChan {
			s.clientsMu.Lock()
			for client := range s.clients {
				select {
				case client.send <- logEntry:
				default:
					close(client.send)
					delete(s.clients, client)
				}
			}
			s.clientsMu.Unlock()
		}
	}()
	return nil
}
