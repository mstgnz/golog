package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mstgnz/golog/models"
)

// MockGetLogs is a mock function for database.GetLogs
var MockGetLogs func(filter models.LogFilter) ([]models.Log, error)

// MockInsertLog is a mock function for database.InsertLog
var MockInsertLog func(logEntry models.Log) (int, error)

// Mock the database package
func init() {
	// Set up mock functions
	MockGetLogs = func(filter models.LogFilter) ([]models.Log, error) {
		return []models.Log{}, nil
	}

	MockInsertLog = func(logEntry models.Log) (int, error) {
		return 1, nil
	}

	// Set the mock functions
	GetLogs = MockGetLogs
	InsertLog = MockInsertLog
}

func TestSetupRoutes(t *testing.T) {
	router := SetupRoutes()

	// Check if router is not nil
	if router == nil {
		t.Fatal("SetupRoutes() returned nil")
	}
}

func TestGetLogsHandler(t *testing.T) {
	// Setup test cases
	testCases := []struct {
		name       string
		filter     models.LogFilter
		logs       []models.Log
		statusCode int
	}{
		{
			name:   "NoFilters",
			filter: models.LogFilter{},
			logs: []models.Log{
				{ID: 1, Timestamp: time.Now(), Level: "ERROR", Type: "DATABASE", Message: "Connection failed"},
				{ID: 2, Timestamp: time.Now(), Level: "INFO", Type: "SYSTEM", Message: "System started"},
			},
			statusCode: http.StatusOK,
		},
		{
			name:   "WithLevelFilter",
			filter: models.LogFilter{Level: "ERROR"},
			logs: []models.Log{
				{ID: 1, Timestamp: time.Now(), Level: "ERROR", Type: "DATABASE", Message: "Connection failed"},
			},
			statusCode: http.StatusOK,
		},
	}

	// Save original function to restore later
	originalGetLogs := GetLogs
	defer func() {
		GetLogs = originalGetLogs
	}()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set up the mock function
			GetLogs = func(filter models.LogFilter) ([]models.Log, error) {
				// Check if filter matches expected
				if filter.Level != tc.filter.Level || filter.Type != tc.filter.Type {
					t.Errorf("Filter mismatch: got {%s, %s}, want {%s, %s}",
						filter.Level, filter.Type, tc.filter.Level, tc.filter.Type)
				}
				return tc.logs, nil
			}

			// Create request
			req, err := http.NewRequest("GET", "/api/logs", nil)
			if err != nil {
				t.Fatal(err)
			}

			// Add query parameters if needed
			q := req.URL.Query()
			if tc.filter.Level != "" {
				q.Add("level", tc.filter.Level)
			}
			if tc.filter.Type != "" {
				q.Add("type", tc.filter.Type)
			}
			req.URL.RawQuery = q.Encode()

			// Create response recorder
			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(GetLogsHandler)

			// Call handler
			handler.ServeHTTP(rr, req)

			// Check status code
			if status := rr.Code; status != tc.statusCode {
				t.Errorf("Handler returned wrong status code: got %v want %v",
					status, tc.statusCode)
			}

			// Check response body
			var response []models.Log
			err = json.Unmarshal(rr.Body.Bytes(), &response)
			if err != nil {
				t.Fatalf("Failed to unmarshal response: %v", err)
			}

			// Check number of logs
			if len(response) != len(tc.logs) {
				t.Errorf("Handler returned wrong number of logs: got %d want %d",
					len(response), len(tc.logs))
			}
		})
	}
}

func TestAddLogHandler(t *testing.T) {
	// Test log entry
	logEntry := models.Log{
		Level:   "ERROR",
		Type:    "DATABASE",
		Message: "Connection failed",
	}

	// Save original function to restore later
	originalInsertLog := InsertLog
	defer func() {
		InsertLog = originalInsertLog
	}()

	// Set up the mock function
	InsertLog = func(log models.Log) (int, error) {
		// Check if log matches expected
		if log.Level != logEntry.Level || log.Type != logEntry.Type || log.Message != logEntry.Message {
			t.Errorf("Log mismatch: got {%s, %s, %s}, want {%s, %s, %s}",
				log.Level, log.Type, log.Message,
				logEntry.Level, logEntry.Type, logEntry.Message)
		}
		return 1, nil
	}

	// Create request body
	body, err := json.Marshal(logEntry)
	if err != nil {
		t.Fatal(err)
	}

	// Create request
	req, err := http.NewRequest("POST", "/api/logs", bytes.NewBuffer(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Create response recorder
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(AddLogHandler)

	// Call handler
	handler.ServeHTTP(rr, req)

	// Check status code
	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Handler returned wrong status code: got %v want %v",
			status, http.StatusOK)
	}

	// Check response body
	var response map[string]int
	err = json.Unmarshal(rr.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	// Check ID
	if id, ok := response["id"]; !ok || id != 1 {
		t.Errorf("Handler returned wrong ID: got %d want %d", id, 1)
	}
}
