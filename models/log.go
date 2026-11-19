package models

import (
	"errors"
	"time"
)

const (
	LevelInfo    = "INFO"
	LevelWarning = "WARNING"
	LevelError   = "ERROR"
	LevelDebug   = "DEBUG"

	TypeSystem   = "SYSTEM"
	TypeAuth     = "AUTH"
	TypeDatabase = "DATABASE"
	TypeUser     = "USER"
	TypeAPI      = "API"

	MaxMessageLength = 10000
)

var (
	ValidLevels = map[string]bool{
		LevelInfo: true, LevelWarning: true, LevelError: true, LevelDebug: true,
	}
	ValidTypes = map[string]bool{
		TypeSystem: true, TypeAuth: true, TypeDatabase: true, TypeUser: true, TypeAPI: true,
	}
)

// Log represents a log entry
type Log struct {
	ID        int       `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
}

// Validate checks that the log entry has valid field values.
func (l *Log) Validate() error {
	if l.Message == "" {
		return errors.New("message is required")
	}
	if len(l.Message) > MaxMessageLength {
		return errors.New("message exceeds maximum length")
	}
	if !ValidLevels[l.Level] {
		return errors.New("invalid level: must be one of INFO, WARNING, ERROR, DEBUG")
	}
	if !ValidTypes[l.Type] {
		return errors.New("invalid type: must be one of SYSTEM, AUTH, DATABASE, USER, API")
	}
	return nil
}

// LogFilter represents filters for querying logs.
type LogFilter struct {
	Level  string `json:"level"`
	Type   string `json:"type"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

// Validate rejects level and type values that no log can have.
func (f LogFilter) Validate() error {
	if f.Level != "" && !ValidLevels[f.Level] {
		return errors.New("invalid level")
	}
	if f.Type != "" && !ValidTypes[f.Type] {
		return errors.New("invalid type")
	}
	return nil
}

// Matches reports whether l passes the level and type filters.
func (f LogFilter) Matches(l Log) bool {
	return (f.Level == "" || l.Level == f.Level) && (f.Type == "" || l.Type == f.Type)
}

// LogEvent is what a listener delivers. Resync means notifications may have
// been lost (the listener reconnected or a read failed) and consumers should
// reload the list instead of trusting the stream to be complete.
type LogEvent struct {
	Log    Log
	Resync bool
}
