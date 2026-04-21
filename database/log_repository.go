package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/lib/pq"
	"github.com/mstgnz/golog/models"
)

const (
	defaultLimit = 100
	maxLimit     = 500
)

// Store wraps a *sql.DB and provides log operations.
type Store struct {
	db  *sql.DB
	dsn string
}

// NewStore creates a Store using the connection established by Connect.
func NewStore() *Store {
	return &Store{db: DB, dsn: dsn}
}

// GetLogs retrieves log entries with optional filtering and pagination.
func (s *Store) GetLogs(filter models.LogFilter) ([]models.Log, error) {
	query := "SELECT id, timestamp, level, type, message FROM logs WHERE 1=1"
	args := []any{}
	argCount := 1

	if filter.Level != "" {
		query += fmt.Sprintf(" AND level = $%d", argCount)
		args = append(args, filter.Level)
		argCount++
	}
	if filter.Type != "" {
		query += fmt.Sprintf(" AND type = $%d", argCount)
		args = append(args, filter.Type)
		argCount++
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	query += fmt.Sprintf(" ORDER BY timestamp DESC LIMIT $%d OFFSET $%d", argCount, argCount+1)
	args = append(args, limit, filter.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []models.Log
	for rows.Next() {
		var l models.Log
		if err := rows.Scan(&l.ID, &l.Timestamp, &l.Level, &l.Type, &l.Message); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}

	return logs, rows.Err()
}

// InsertLog inserts a new log entry and returns its ID.
func (s *Store) InsertLog(logEntry models.Log) (int, error) {
	var id int
	err := s.db.QueryRow(
		"INSERT INTO logs (level, type, message) VALUES ($1, $2, $3) RETURNING id",
		logEntry.Level, logEntry.Type, logEntry.Message,
	).Scan(&id)
	return id, err
}

// ListenForLogs subscribes to PostgreSQL NOTIFY on log_channel and forwards
// each notification as a Log to ch. The goroutine stops and closes ch when ctx
// is cancelled.
func (s *Store) ListenForLogs(ctx context.Context, logChan chan<- models.Log) error {
	listener := pq.NewListener(s.dsn, 10*time.Second, time.Minute, func(ev pq.ListenerEventType, err error) {
		if err != nil {
			log.Printf("Error in listener: %v\n", err)
		}
	})

	if err := listener.Listen("log_channel"); err != nil {
		return err
	}

	log.Println("Listening for log notifications on channel: log_channel")

	go func() {
		defer listener.Close()
		defer close(logChan)
		for {
			select {
			case n := <-listener.Notify:
				if n == nil {
					continue
				}
				var logEntry models.Log
				if err := json.Unmarshal([]byte(n.Extra), &logEntry); err != nil {
					log.Printf("Error unmarshaling notification: %v\n", err)
					continue
				}
				logChan <- logEntry
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}
