package database

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/mstgnz/golog/models"
)

func TestGetLogs(t *testing.T) {
	// Setup mock DB
	db, mock, err := SetupMockDB()
	if err != nil {
		t.Fatalf("Failed to set up mock DB: %v", err)
	}
	defer db.Close()

	// Test case 1: No filters
	t.Run("NoFilters", func(t *testing.T) {
		// Expected query with no filters
		mock.ExpectQuery("SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 ORDER BY timestamp DESC LIMIT 100").
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed").
				AddRow(2, time.Now(), "INFO", "SYSTEM", "System started"))

		// Call the function
		logs, err := GetLogs(models.LogFilter{})
		if err != nil {
			t.Fatalf("GetLogs() failed: %v", err)
		}

		// Check results
		if len(logs) != 2 {
			t.Errorf("GetLogs() returned %d logs, want 2", len(logs))
		}

		// Check for any unfulfilled expectations
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %v", err)
		}
	})

	// Test case 2: With level filter
	t.Run("LevelFilter", func(t *testing.T) {
		// Reset mock
		db, mock, err = SetupMockDB()
		if err != nil {
			t.Fatalf("Failed to set up mock DB: %v", err)
		}
		defer db.Close()

		// Expected query with level filter
		mock.ExpectQuery("SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 AND level = \\$1 ORDER BY timestamp DESC LIMIT 100").
			WithArgs("ERROR").
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed"))

		// Call the function
		logs, err := GetLogs(models.LogFilter{Level: "ERROR"})
		if err != nil {
			t.Fatalf("GetLogs() failed: %v", err)
		}

		// Check results
		if len(logs) != 1 {
			t.Errorf("GetLogs() returned %d logs, want 1", len(logs))
		}
		if logs[0].Level != "ERROR" {
			t.Errorf("Log level = %s, want ERROR", logs[0].Level)
		}

		// Check for any unfulfilled expectations
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %v", err)
		}
	})

	// Test case 3: With type filter
	t.Run("TypeFilter", func(t *testing.T) {
		// Reset mock
		db, mock, err = SetupMockDB()
		if err != nil {
			t.Fatalf("Failed to set up mock DB: %v", err)
		}
		defer db.Close()

		// Expected query with type filter
		mock.ExpectQuery("SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 AND type = \\$1 ORDER BY timestamp DESC LIMIT 100").
			WithArgs("DATABASE").
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed"))

		// Call the function
		logs, err := GetLogs(models.LogFilter{Type: "DATABASE"})
		if err != nil {
			t.Fatalf("GetLogs() failed: %v", err)
		}

		// Check results
		if len(logs) != 1 {
			t.Errorf("GetLogs() returned %d logs, want 1", len(logs))
		}
		if logs[0].Type != "DATABASE" {
			t.Errorf("Log type = %s, want DATABASE", logs[0].Type)
		}

		// Check for any unfulfilled expectations
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %v", err)
		}
	})

	// Test case 4: With both filters
	t.Run("BothFilters", func(t *testing.T) {
		// Reset mock
		db, mock, err = SetupMockDB()
		if err != nil {
			t.Fatalf("Failed to set up mock DB: %v", err)
		}
		defer db.Close()

		// Expected query with both filters
		mock.ExpectQuery("SELECT id, timestamp, level, type, message FROM logs WHERE 1=1 AND level = \\$1 AND type = \\$2 ORDER BY timestamp DESC LIMIT 100").
			WithArgs("ERROR", "DATABASE").
			WillReturnRows(sqlmock.NewRows([]string{"id", "timestamp", "level", "type", "message"}).
				AddRow(1, time.Now(), "ERROR", "DATABASE", "Connection failed"))

		// Call the function
		logs, err := GetLogs(models.LogFilter{Level: "ERROR", Type: "DATABASE"})
		if err != nil {
			t.Fatalf("GetLogs() failed: %v", err)
		}

		// Check results
		if len(logs) != 1 {
			t.Errorf("GetLogs() returned %d logs, want 1", len(logs))
		}
		if logs[0].Level != "ERROR" {
			t.Errorf("Log level = %s, want ERROR", logs[0].Level)
		}
		if logs[0].Type != "DATABASE" {
			t.Errorf("Log type = %s, want DATABASE", logs[0].Type)
		}

		// Check for any unfulfilled expectations
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("Unfulfilled expectations: %v", err)
		}
	})
}

func TestInsertLog(t *testing.T) {
	// Setup mock DB
	db, mock, err := SetupMockDB()
	if err != nil {
		t.Fatalf("Failed to set up mock DB: %v", err)
	}
	defer db.Close()

	// Test log entry
	logEntry := models.Log{
		Level:   "ERROR",
		Type:    "DATABASE",
		Message: "Connection failed",
	}

	// Expected query
	mock.ExpectQuery("INSERT INTO logs \\(level, type, message\\) VALUES \\(\\$1, \\$2, \\$3\\) RETURNING id").
		WithArgs(logEntry.Level, logEntry.Type, logEntry.Message).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

	// Call the function
	id, err := InsertLog(logEntry)
	if err != nil {
		t.Fatalf("InsertLog() failed: %v", err)
	}

	// Check results
	if id != 1 {
		t.Errorf("InsertLog() returned id = %d, want 1", id)
	}

	// Check for any unfulfilled expectations
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("Unfulfilled expectations: %v", err)
	}
}
