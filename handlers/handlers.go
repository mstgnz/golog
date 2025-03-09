package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/mux"
	"github.com/mstgnz/golog/database"
	"github.com/mstgnz/golog/models"
)

var (
	clients      = make(map[*Client]bool)
	clientsMutex sync.Mutex
	logChan      = make(chan models.Log)
)

// Client represents a connected websocket client
type Client struct {
	send chan models.Log
}

// SetupRoutes sets up the HTTP routes
func SetupRoutes() *mux.Router {
	r := mux.NewRouter()

	// API routes
	r.HandleFunc("/api/logs", GetLogsHandler).Methods("GET")
	r.HandleFunc("/api/logs", AddLogHandler).Methods("POST")
	r.HandleFunc("/api/logs/stream", StreamLogsHandler).Methods("GET")

	// Serve static files
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("./web/static")))

	return r
}

// GetLogsHandler handles requests to get logs with optional filtering
func GetLogsHandler(w http.ResponseWriter, r *http.Request) {
	filter := models.LogFilter{
		Level: r.URL.Query().Get("level"),
		Type:  r.URL.Query().Get("type"),
	}

	var logs []models.Log
	var err error

	// Use the mock function if defined (for testing), otherwise use the real function
	if GetLogs != nil {
		logs, err = GetLogs(filter)
	} else {
		logs, err = database.GetLogs(filter)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}

// AddLogHandler handles requests to add a new log
func AddLogHandler(w http.ResponseWriter, r *http.Request) {
	var logEntry models.Log
	if err := json.NewDecoder(r.Body).Decode(&logEntry); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var id int
	var err error

	// Use the mock function if defined (for testing), otherwise use the real function
	if InsertLog != nil {
		id, err = InsertLog(logEntry)
	} else {
		id, err = database.InsertLog(logEntry)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"id": id})
}

// StreamLogsHandler handles SSE connections for real-time log streaming
func StreamLogsHandler(w http.ResponseWriter, r *http.Request) {
	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Create a new client
	client := &Client{
		send: make(chan models.Log, 256),
	}

	// Register client
	clientsMutex.Lock()
	clients[client] = true
	clientsMutex.Unlock()

	// Ensure client is removed when connection is closed
	defer func() {
		clientsMutex.Lock()
		delete(clients, client)
		close(client.send)
		clientsMutex.Unlock()
	}()

	// Filter parameters
	level := r.URL.Query().Get("level")
	logType := r.URL.Query().Get("type")

	// Stream logs to client
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	for {
		select {
		case logEntry, ok := <-client.send:
			if !ok {
				return
			}

			// Apply filters
			if (level == "" || logEntry.Level == level) && (logType == "" || logEntry.Type == logType) {
				data, err := json.Marshal(logEntry)
				if err != nil {
					log.Printf("Error marshaling log entry: %v", err)
					continue
				}

				// Write SSE format
				_, err = w.Write([]byte("data: " + string(data) + "\n\n"))
				if err != nil {
					return
				}
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

// StartLogListener starts listening for log notifications
func StartLogListener(ctx context.Context) error {
	err := database.ListenForLogs(ctx, logChan)
	if err != nil {
		return err
	}

	// Broadcast logs to all connected clients
	go func() {
		for logEntry := range logChan {
			clientsMutex.Lock()
			for client := range clients {
				select {
				case client.send <- logEntry:
				default:
					close(client.send)
					delete(clients, client)
				}
			}
			clientsMutex.Unlock()
		}
	}()

	return nil
}
