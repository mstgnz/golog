package database

import (
	"context"
	"database/sql"
	"time"

	_ "github.com/lib/pq"
)

const (
	defaultPingInterval      = 90 * time.Second
	defaultMinReconnectDelay = 10 * time.Second
	defaultMaxReconnectDelay = time.Minute
)

// Store wraps a *sql.DB and provides log operations.
type Store struct {
	db  *sql.DB
	dsn string // the listener opens its own connection with it

	pingInterval      time.Duration
	minReconnectDelay time.Duration
	maxReconnectDelay time.Duration
}

// Open connects to PostgreSQL and verifies the connection.
func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return newStore(db, dsn), nil
}

func newStore(db *sql.DB, dsn string) *Store {
	return &Store{
		db:                db,
		dsn:               dsn,
		pingInterval:      defaultPingInterval,
		minReconnectDelay: defaultMinReconnectDelay,
		maxReconnectDelay: defaultMaxReconnectDelay,
	}
}

// Close closes the connection pool.
func (s *Store) Close() error {
	return s.db.Close()
}
