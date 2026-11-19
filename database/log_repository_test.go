package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/mstgnz/golog/v2/models"
)

var logColumns = []string{"id", "timestamp", "level", "type", "message"}

func newTestStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create mock DB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return newStore(db, ""), mock
}

const byIDQuery = `SELECT id, timestamp, level, type, message FROM logs WHERE id = \$1`

func TestGetLogByID(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		store, mock := newTestStore(t)
		ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		mock.ExpectQuery(byIDQuery).WithArgs(7).
			WillReturnRows(sqlmock.NewRows(logColumns).AddRow(7, ts, "INFO", "API", "hello"))

		got, err := store.GetLogByID(context.Background(), 7)
		if err != nil {
			t.Fatalf("GetLogByID() error: %v", err)
		}
		want := models.Log{ID: 7, Timestamp: ts, Level: "INFO", Type: "API", Message: "hello"}
		if got != want {
			t.Fatalf("GetLogByID() = %+v, want %+v", got, want)
		}
	})

	t.Run("not found", func(t *testing.T) {
		store, mock := newTestStore(t)
		mock.ExpectQuery(byIDQuery).WithArgs(8).WillReturnRows(sqlmock.NewRows(logColumns))

		if _, err := store.GetLogByID(context.Background(), 8); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetLogByID() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("null timestamp does not fail the read", func(t *testing.T) {
		store, mock := newTestStore(t)
		mock.ExpectQuery(byIDQuery).WithArgs(9).
			WillReturnRows(sqlmock.NewRows(logColumns).AddRow(9, nil, "INFO", "API", "no ts"))

		got, err := store.GetLogByID(context.Background(), 9)
		if err != nil {
			t.Fatalf("GetLogByID() error: %v", err)
		}
		if !got.Timestamp.IsZero() {
			t.Fatalf("Timestamp = %v, want zero", got.Timestamp)
		}
	})
}

func TestEventFor(t *testing.T) {
	ctx := context.Background()

	t.Run("nil notification means reconnect", func(t *testing.T) {
		store, _ := newTestStore(t)
		ev, send := store.eventFor(ctx, nil)
		if !send || !ev.Resync {
			t.Fatalf("eventFor(nil) = %+v, %v; want resync", ev, send)
		}
	})

	t.Run("id payload loads the row", func(t *testing.T) {
		store, mock := newTestStore(t)
		mock.ExpectQuery(byIDQuery).WithArgs(42).
			WillReturnRows(sqlmock.NewRows(logColumns).AddRow(42, time.Now(), "ERROR", "DATABASE", "boom"))

		ev, send := store.eventFor(ctx, &pq.Notification{Extra: "42"})
		if !send || ev.Resync || ev.Log.ID != 42 || ev.Log.Message != "boom" {
			t.Fatalf("eventFor() = %+v, %v", ev, send)
		}
	})

	t.Run("non-numeric payload is skipped", func(t *testing.T) {
		store, _ := newTestStore(t)
		if _, send := store.eventFor(ctx, &pq.Notification{Extra: `{"id":1}`}); send {
			t.Fatal("eventFor() sent an event for a legacy JSON payload")
		}
	})

	t.Run("row already gone is skipped", func(t *testing.T) {
		store, mock := newTestStore(t)
		mock.ExpectQuery(byIDQuery).WithArgs(5).WillReturnRows(sqlmock.NewRows(logColumns))
		if _, send := store.eventFor(ctx, &pq.Notification{Extra: "5"}); send {
			t.Fatal("eventFor() sent an event for a missing row")
		}
	})

	t.Run("read failure asks for resync", func(t *testing.T) {
		store, mock := newTestStore(t)
		mock.ExpectQuery(byIDQuery).WithArgs(6).WillReturnError(errors.New("connection reset"))
		ev, send := store.eventFor(ctx, &pq.Notification{Extra: "6"})
		if !send || !ev.Resync {
			t.Fatalf("eventFor() = %+v, %v; want resync", ev, send)
		}
	})

	t.Run("cancelled context sends nothing", func(t *testing.T) {
		store, mock := newTestStore(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		mock.ExpectQuery(byIDQuery).WithArgs(6).WillReturnError(context.Canceled)
		if _, send := store.eventFor(cctx, &pq.Notification{Extra: "6"}); send {
			t.Fatal("eventFor() sent an event after cancellation")
		}
	})
}

func TestOpenUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Open(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=2"); err == nil {
		t.Fatal("Open() should fail when the database is unreachable")
	}
}

// Review finding: pq's Listen waits for a connection without looking at ctx,
// so a server that could not reach the database ignored SIGTERM at startup.
func TestListenForLogsRespectsContext(t *testing.T) {
	s := newStore(nil, "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	s.minReconnectDelay = 50 * time.Millisecond
	s.maxReconnectDelay = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.ListenForLogs(ctx, make(chan models.LogEvent)) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ListenForLogs() should fail when ctx ends before it connects")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenForLogs() ignored ctx while the database was unreachable")
	}
}

func TestGetLogsQueryError(t *testing.T) {
	store, mock := newTestStore(t)
	mock.ExpectQuery(`SELECT id`).WillReturnError(errors.New("db down"))
	if _, err := store.GetLogs(context.Background(), models.LogFilter{}); err == nil {
		t.Fatal("GetLogs() should return the query error")
	}
}

func TestGetLogsScanError(t *testing.T) {
	store, mock := newTestStore(t)
	mock.ExpectQuery(`SELECT id`).
		WillReturnRows(sqlmock.NewRows(logColumns).AddRow("not-an-int", time.Now(), "INFO", "API", "x"))
	if _, err := store.GetLogs(context.Background(), models.LogFilter{}); err == nil {
		t.Fatal("GetLogs() should return the scan error")
	}
}

func TestGetLogs(t *testing.T) {
	t.Run("NoFilters", func(t *testing.T) {
		store, mock := newTestStore(t)

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 ORDER BY timestamp DESC, id DESC LIMIT \$1 OFFSET \$2`).
			WithArgs(100, 0).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed").
				AddRow(2, time.Now(), "INFO", "SYSTEM", "System started"))

		logs, err := store.GetLogs(context.Background(), models.LogFilter{})
		if err != nil {
			t.Fatalf("GetLogs() error: %v", err)
		}
		if len(logs) != 2 {
			t.Errorf("GetLogs() = %d logs, want 2", len(logs))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %v", err)
		}
	})

	t.Run("LevelFilter", func(t *testing.T) {
		store, mock := newTestStore(t)

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 AND level = \$1 ORDER BY timestamp DESC, id DESC LIMIT \$2 OFFSET \$3`).
			WithArgs("ERROR", 100, 0).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed"))

		logs, err := store.GetLogs(context.Background(), models.LogFilter{Level: "ERROR"})
		if err != nil {
			t.Fatalf("GetLogs() error: %v", err)
		}
		if len(logs) != 1 {
			t.Errorf("GetLogs() = %d logs, want 1", len(logs))
		}
		if logs[0].Level != "ERROR" {
			t.Errorf("Log level = %s, want ERROR", logs[0].Level)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %v", err)
		}
	})

	t.Run("TypeFilter", func(t *testing.T) {
		store, mock := newTestStore(t)

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 AND type = \$1 ORDER BY timestamp DESC, id DESC LIMIT \$2 OFFSET \$3`).
			WithArgs("DATABASE", 100, 0).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed"))

		logs, err := store.GetLogs(context.Background(), models.LogFilter{Type: "DATABASE"})
		if err != nil {
			t.Fatalf("GetLogs() error: %v", err)
		}
		if len(logs) != 1 {
			t.Errorf("GetLogs() = %d logs, want 1", len(logs))
		}
		if logs[0].Type != "DATABASE" {
			t.Errorf("Log type = %s, want DATABASE", logs[0].Type)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %v", err)
		}
	})

	t.Run("BothFilters", func(t *testing.T) {
		store, mock := newTestStore(t)

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 AND level = \$1 AND type = \$2 ORDER BY timestamp DESC, id DESC LIMIT \$3 OFFSET \$4`).
			WithArgs("ERROR", "DATABASE", 100, 0).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed"))

		logs, err := store.GetLogs(context.Background(), models.LogFilter{Level: "ERROR", Type: "DATABASE"})
		if err != nil {
			t.Fatalf("GetLogs() error: %v", err)
		}
		if len(logs) != 1 || logs[0].Level != "ERROR" || logs[0].Type != "DATABASE" {
			t.Errorf("GetLogs() returned unexpected result")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %v", err)
		}
	})

	t.Run("Pagination", func(t *testing.T) {
		store, mock := newTestStore(t)

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 ORDER BY timestamp DESC, id DESC LIMIT \$1 OFFSET \$2`).
			WithArgs(10, 20).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(21, time.Now(), "INFO", "SYSTEM", "paged"))

		logs, err := store.GetLogs(context.Background(), models.LogFilter{Limit: 10, Offset: 20})
		if err != nil {
			t.Fatalf("GetLogs() error: %v", err)
		}
		if len(logs) != 1 {
			t.Errorf("GetLogs() = %d logs, want 1", len(logs))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %v", err)
		}
	})

	t.Run("LimitCappedAtMax", func(t *testing.T) {
		store, mock := newTestStore(t)

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 ORDER BY timestamp DESC, id DESC LIMIT \$1 OFFSET \$2`).
			WithArgs(maxLimit, 0).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}))

		_, err := store.GetLogs(context.Background(), models.LogFilter{Limit: 9999})
		if err != nil {
			t.Fatalf("GetLogs() error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %v", err)
		}
	})
}

func TestInsertLog(t *testing.T) {
	store, mock := newTestStore(t)

	logEntry := models.Log{Level: "ERROR", Type: "DATABASE", Message: "Connection failed"}

	mock.ExpectQuery(`INSERT INTO logs \(level, type, message\) VALUES \(\$1, \$2, \$3\) RETURNING id`).
		WithArgs(logEntry.Level, logEntry.Type, logEntry.Message).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	id, err := store.InsertLog(context.Background(), logEntry)
	if err != nil {
		t.Fatalf("InsertLog() error: %v", err)
	}
	if id != 1 {
		t.Errorf("InsertLog() id = %d, want 1", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("Unfulfilled expectations: %v", err)
	}
}
