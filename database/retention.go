package database

import (
	"context"
	"log"
	"time"
)

const (
	// retentionLockID lets one instance run a retention round at a time.
	retentionLockID int64 = 7_351_402_119
	// retentionBatch keeps each DELETE short so it never holds locks for long.
	retentionBatch = 5000
)

// DeleteOlderThan deletes logs whose timestamp is before cutoff, batch rows
// per statement, and returns how many it deleted. NULL timestamps never match.
func (s *Store) DeleteOlderThan(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	var total int64
	for {
		res, err := s.db.ExecContext(ctx, `DELETE FROM logs WHERE id IN (
			SELECT id FROM logs WHERE timestamp < $1 ORDER BY timestamp LIMIT $2
		)`, cutoff, batch)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(batch) {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// RunRetention deletes logs older than keep now and then every interval,
// until ctx ends. Errors are logged; the next round tries again.
func (s *Store) RunRetention(ctx context.Context, keep, interval time.Duration) {
	round := func() {
		n, ran, err := s.retentionRound(ctx, keep)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Printf("golog: retention: %v", err)
		case ran && n > 0:
			log.Printf("golog: retention deleted %d logs older than %s", n, keep)
		}
	}
	round()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			round()
		}
	}
}

// retentionRound returns ran=false when another instance holds the lock.
func (s *Store) retentionRound(ctx context.Context, keep time.Duration) (deleted int64, ran bool, err error) {
	conn, err := s.db.Conn(ctx) // session-level advisory lock needs one connection
	if err != nil {
		return 0, false, err
	}
	defer conn.Close()

	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", retentionLockID).Scan(&locked); err != nil {
		return 0, false, err
	}
	if !locked {
		return 0, false, nil
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", retentionLockID)
	}()

	// The database clock decides what is old: an app server with a clock
	// running ahead must not delete fresh logs.
	var now time.Time
	if err := conn.QueryRowContext(ctx, "SELECT now()").Scan(&now); err != nil {
		return 0, true, err
	}
	n, err := s.DeleteOlderThan(ctx, now.Add(-keep), retentionBatch)
	return n, true, err
}
