package database

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/mstgnz/golog/models"
)

func newTestStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create mock DB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{db: db}, mock
}

func TestGetLogs(t *testing.T) {
	t.Run("NoFilters", func(t *testing.T) {
		store, mock := newTestStore(t)

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 ORDER BY timestamp DESC LIMIT \$1 OFFSET \$2`).
			WithArgs(100, 0).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed").
				AddRow(2, time.Now(), "INFO", "SYSTEM", "System started"))

		logs, err := store.GetLogs(models.LogFilter{})
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

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 AND level = \$1 ORDER BY timestamp DESC LIMIT \$2 OFFSET \$3`).
			WithArgs("ERROR", 100, 0).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed"))

		logs, err := store.GetLogs(models.LogFilter{Level: "ERROR"})
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

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 AND type = \$1 ORDER BY timestamp DESC LIMIT \$2 OFFSET \$3`).
			WithArgs("DATABASE", 100, 0).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed"))

		logs, err := store.GetLogs(models.LogFilter{Type: "DATABASE"})
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

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 AND level = \$1 AND type = \$2 ORDER BY timestamp DESC LIMIT \$3 OFFSET \$4`).
			WithArgs("ERROR", "DATABASE", 100, 0).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed"))

		logs, err := store.GetLogs(models.LogFilter{Level: "ERROR", Type: "DATABASE"})
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

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 ORDER BY timestamp DESC LIMIT \$1 OFFSET \$2`).
			WithArgs(10, 20).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(21, time.Now(), "INFO", "SYSTEM", "paged"))

		logs, err := store.GetLogs(models.LogFilter{Limit: 10, Offset: 20})
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

		mock.ExpectQuery(`SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 ORDER BY timestamp DESC LIMIT \$1 OFFSET \$2`).
			WithArgs(maxLimit, 0).
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}))

		_, err := store.GetLogs(models.LogFilter{Limit: 9999})
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

	id, err := store.InsertLog(logEntry)
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
