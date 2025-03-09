package models

import (
	"time"
)

// Log represents a log entry
type Log struct {
	ID        int       `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
}

// LogFilter represents filters for logs
type LogFilter struct {
	Level string `json:"level"`
	Type  string `json:"type"`
}
