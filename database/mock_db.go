package database

import (
	"database/sql"
	"database/sql/driver"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// SetupMockDB sets up a mock database for testing
func SetupMockDB() (*sql.DB, sqlmock.Sqlmock, error) {
	db, mock, err := sqlmock.New()
	if err != nil {
		return nil, nil, err
	}

	// Replace the global DB variable with our mock
	DB = db
	return db, mock, nil
}

// AnyTime is a custom matcher for timestamp
type AnyTime struct{}

// Match satisfies sqlmock.Argument interface
func (a AnyTime) Match(v driver.Value) bool {
	_, ok := v.(time.Time)
	return ok
}
