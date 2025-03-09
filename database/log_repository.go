package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/lib/pq"
	"github.com/mstgnz/golog/models"
)

// GetLogs retrieves logs from the database with optional filtering
func GetLogs(filter models.LogFilter) ([]models.Log, error) {
	query := "SELECT id, timestamp, level, type, message FROM logs WHERE 1=1"
	args := []interface{}{}
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

	query += " ORDER BY timestamp DESC LIMIT 100"

	rows, err := DB.Query(query, args...)
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

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return logs, nil
}

// InsertLog inserts a new log entry into the database
func InsertLog(logEntry models.Log) (int, error) {
	var id int
	err := DB.QueryRow(
		"INSERT INTO logs (level, type, message) VALUES ($1, $2, $3) RETURNING id",
		logEntry.Level, logEntry.Type, logEntry.Message,
	).Scan(&id)

	return id, err
}

// ListenForLogs listens for new log notifications and sends them to the provided channel
func ListenForLogs(ctx context.Context, logChan chan<- models.Log) error {
	listener := pq.NewListener(fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		getEnv("DB_HOST", "localhost"),
		getEnv("DB_PORT", "5432"),
		getEnv("DB_USER", "postgres"),
		getEnv("DB_PASSWORD", "postgres"),
		getEnv("DB_NAME", "golog"),
	), 10*time.Second, time.Minute, func(ev pq.ListenerEventType, err error) {
		if err != nil {
			log.Printf("Error in listener: %v\n", err)
		}
	})

	err := listener.Listen("log_channel")
	if err != nil {
		return err
	}

	log.Println("Listening for log notifications on channel: log_channel")

	go func() {
		for {
			select {
			case n := <-listener.Notify:
				var logEntry models.Log
				err := json.Unmarshal([]byte(n.Extra), &logEntry)
				if err != nil {
					log.Printf("Error unmarshaling notification: %v\n", err)
					continue
				}
				logChan <- logEntry
			case <-ctx.Done():
				listener.Close()
				close(logChan)
				return
			}
		}
	}()

	return nil
}

// Helper function to get environment variables with default values
func getEnv(key, defaultValue string) string {
	value, exists := os.LookupEnv(key)
	if !exists {
		return defaultValue
	}
	return value
}
