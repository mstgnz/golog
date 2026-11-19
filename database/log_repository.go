package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/lib/pq"
	"github.com/mstgnz/golog/v2/models"
)

const (
	defaultLimit = 100
	maxLimit     = 500

	notifyChannel = "log_channel"
)

// ErrNotFound is returned when a log id does not exist.
var ErrNotFound = errors.New("log not found")

type rowScanner interface {
	Scan(dest ...any) error
}

// scanLog tolerates a NULL timestamp: the column is nullable and rows written
// straight to the table may carry one.
func scanLog(row rowScanner) (models.Log, error) {
	var l models.Log
	var ts sql.NullTime
	if err := row.Scan(&l.ID, &ts, &l.Level, &l.Type, &l.Message); err != nil {
		return models.Log{}, err
	}
	l.Timestamp = ts.Time
	return l, nil
}

// GetLogs retrieves log entries, newest first, with optional filtering and pagination.
func (s *Store) GetLogs(ctx context.Context, filter models.LogFilter) ([]models.Log, error) {
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

	// id breaks timestamp ties so pages are stable; matches logs_timestamp_id_idx.
	query += fmt.Sprintf(" ORDER BY timestamp DESC, id DESC LIMIT $%d OFFSET $%d", argCount, argCount+1)
	args = append(args, limit, filter.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []models.Log
	for rows.Next() {
		l, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}

	return logs, rows.Err()
}

// GetLogByID returns one log entry, or ErrNotFound.
func (s *Store) GetLogByID(ctx context.Context, id int) (models.Log, error) {
	row := s.db.QueryRowContext(ctx, "SELECT id, timestamp, level, type, message FROM logs WHERE id = $1", id)
	l, err := scanLog(row)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Log{}, ErrNotFound
	}
	return l, err
}

// InsertLog inserts a new log entry and returns its ID.
func (s *Store) InsertLog(ctx context.Context, logEntry models.Log) (int, error) {
	var id int
	err := s.db.QueryRowContext(ctx,
		"INSERT INTO logs (level, type, message) VALUES ($1, $2, $3) RETURNING id",
		logEntry.Level, logEntry.Type, logEntry.Message,
	).Scan(&id)
	return id, err
}

// ListenForLogs subscribes to NOTIFY on log_channel, loads each notified row
// and sends it to ch. After a reconnect, or when a row cannot be read, it
// sends a Resync event instead. The goroutine closes ch when ctx is cancelled.
func (s *Store) ListenForLogs(ctx context.Context, ch chan<- models.LogEvent) error {
	listener := pq.NewListener(s.dsn, s.minReconnectDelay, s.maxReconnectDelay, func(ev pq.ListenerEventType, err error) {
		if err != nil {
			log.Printf("golog: listener: %v", err)
		}
	})
	// pq's Listen waits for a connection and does not look at ctx.
	listenErr := make(chan error, 1)
	go func() { listenErr <- listener.Listen(notifyChannel) }()
	select {
	case err := <-listenErr:
		if err != nil {
			listener.Close()
			return err
		}
	case <-ctx.Done():
		listener.Close() // makes the pending Listen return
		<-listenErr
		return ctx.Err()
	}
	go s.forwardNotifications(ctx, listener, ch)
	return nil
}

func (s *Store) forwardNotifications(ctx context.Context, listener *pq.Listener, ch chan<- models.LogEvent) {
	defer func() {
		close(ch)
		// pq may be blocked handing us a notification (or an in-flight Ping may
		// be waiting behind one); keep draining until Close lets it finish.
		go func() {
			for range listener.Notify {
			}
		}()
		listener.Close()
	}()

	// pq does not notice a silently dropped connection on its own; Ping makes
	// it reconnect. 90s is the interval pq's documentation suggests. Ping runs
	// off this goroutine: it waits for a reply that pq reads only after it has
	// delivered pending notifications, which this goroutine must keep draining.
	ticker := time.NewTicker(s.pingInterval)
	defer ticker.Stop()
	pinging := make(chan struct{}, 1)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			select {
			case pinging <- struct{}{}:
				go func() {
					defer func() { <-pinging }()
					if err := listener.Ping(); err != nil && ctx.Err() == nil {
						log.Printf("golog: listener ping: %v", err)
					}
				}()
			default: // the previous ping is still in flight
			}
		case n, ok := <-listener.Notify:
			if !ok {
				return
			}
			ev, send := s.eventFor(ctx, n)
			if !send {
				continue
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}
}

// eventFor turns a notification into an event. pq sends a nil notification
// after reconnecting: anything inserted while disconnected was missed.
func (s *Store) eventFor(ctx context.Context, n *pq.Notification) (models.LogEvent, bool) {
	if n == nil {
		return models.LogEvent{Resync: true}, true
	}
	id, err := strconv.Atoi(n.Extra)
	if err != nil {
		log.Printf("golog: unexpected notification payload on %s (is the trigger from an older version?)", notifyChannel)
		return models.LogEvent{}, false
	}
	entry, err := s.GetLogByID(ctx, id)
	switch {
	case err == nil:
		return models.LogEvent{Log: entry}, true
	case errors.Is(err, ErrNotFound):
		// Deleted or rolled back before we read it; nothing to show.
		return models.LogEvent{}, false
	case ctx.Err() != nil:
		return models.LogEvent{}, false
	default:
		log.Printf("golog: read log %d: %v", id, err)
		return models.LogEvent{Resync: true}, true
	}
}
