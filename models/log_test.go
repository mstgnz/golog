package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLogJSON(t *testing.T) {
	// Create a test log with a fixed timestamp
	timestamp := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
	log := Log{
		ID:        1,
		Timestamp: timestamp,
		Level:     "ERROR",
		Type:      "DATABASE",
		Message:   "Connection failed",
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(log)
	if err != nil {
		t.Fatalf("Failed to marshal Log to JSON: %v", err)
	}

	// Unmarshal back to a new struct
	var unmarshaledLog Log
	err = json.Unmarshal(jsonData, &unmarshaledLog)
	if err != nil {
		t.Fatalf("Failed to unmarshal JSON to Log: %v", err)
	}

	// Check fields
	if unmarshaledLog.ID != log.ID {
		t.Errorf("ID mismatch: got %d, want %d", unmarshaledLog.ID, log.ID)
	}

	if !unmarshaledLog.Timestamp.Equal(log.Timestamp) {
		t.Errorf("Timestamp mismatch: got %v, want %v", unmarshaledLog.Timestamp, log.Timestamp)
	}

	if unmarshaledLog.Level != log.Level {
		t.Errorf("Level mismatch: got %s, want %s", unmarshaledLog.Level, log.Level)
	}

	if unmarshaledLog.Type != log.Type {
		t.Errorf("Type mismatch: got %s, want %s", unmarshaledLog.Type, log.Type)
	}

	if unmarshaledLog.Message != log.Message {
		t.Errorf("Message mismatch: got %s, want %s", unmarshaledLog.Message, log.Message)
	}
}

func TestLogFilter(t *testing.T) {
	// Test empty filter
	filter := LogFilter{}
	if filter.Level != "" || filter.Type != "" {
		t.Errorf("Empty filter should have empty fields, got Level=%s, Type=%s", filter.Level, filter.Type)
	}

	// Test filter with values
	filter = LogFilter{
		Level: "ERROR",
		Type:  "DATABASE",
	}

	if filter.Level != "ERROR" {
		t.Errorf("Level mismatch: got %s, want %s", filter.Level, "ERROR")
	}

	if filter.Type != "DATABASE" {
		t.Errorf("Type mismatch: got %s, want %s", filter.Type, "DATABASE")
	}

	// Test JSON marshaling/unmarshaling
	jsonData, err := json.Marshal(filter)
	if err != nil {
		t.Fatalf("Failed to marshal LogFilter to JSON: %v", err)
	}

	var unmarshaledFilter LogFilter
	err = json.Unmarshal(jsonData, &unmarshaledFilter)
	if err != nil {
		t.Fatalf("Failed to unmarshal JSON to LogFilter: %v", err)
	}

	if unmarshaledFilter.Level != filter.Level {
		t.Errorf("Level mismatch after JSON: got %s, want %s", unmarshaledFilter.Level, filter.Level)
	}

	if unmarshaledFilter.Type != filter.Type {
		t.Errorf("Type mismatch after JSON: got %s, want %s", unmarshaledFilter.Type, filter.Type)
	}
}
