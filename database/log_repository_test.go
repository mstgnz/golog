package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/mstgnz/golog/v2/models"
)

var logColumns = []string{"id", "timestamp", "level", "type", "service", "message", "attrs"}

func newTestStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create mock DB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return newStore(db, ""), mock
}

const byIDQuery = `SELECT id, timestamp, level, type, service, message, attrs FROM logs WHERE id = \$1`

func TestGetLogByID(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		store, mock := newTestStore(t)
		ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		mock.ExpectQuery(byIDQuery).WithArgs(7).
			WillReturnRows(sqlmock.NewRows(logColumns).AddRow(7, ts, "INFO", "API", "billing", "hello", []byte(`{"a":1}`)))

		got, err := store.GetLogByID(context.Background(), 7)
		if err != nil {
			t.Fatalf("GetLogByID() error: %v", err)
		}
		if got.ID != 7 || !got.Timestamp.Equal(ts) || got.Level != "INFO" || got.Type != "API" ||
			got.Service != "billing" || got.Message != "hello" || string(got.Attrs) != `{"a":1}` {
			t.Fatalf("GetLogByID() = %+v", got)
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
			WillReturnRows(sqlmock.NewRows(logColumns).AddRow(9, nil, "INFO", "API", "", "no ts", []byte(`{}`)))

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
			WillReturnRows(sqlmock.NewRows(logColumns).AddRow(42, time.Now(), "ERROR", "DATABASE", "", "boom", []byte(`{}`)))

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
		WillReturnRows(sqlmock.NewRows(logColumns).AddRow("not-an-int", time.Now(), "INFO", "API", "", "x", []byte(`{}`)))
	if _, err := store.GetLogs(context.Background(), models.LogFilter{}); err == nil {
		t.Fatal("GetLogs() should return the scan error")
	}
}

func TestWhereClause(t *testing.T) {
	tests := []struct {
		name     string
		filter   models.LogFilter
		wantSQL  string
		wantArgs []any
	}{
		{name: "empty", filter: models.LogFilter{}, wantSQL: "", wantArgs: nil},
		{
			name:     "exact fields",
			filter:   models.LogFilter{Level: "ERROR", Type: "PAYMENT", Service: "billing"},
			wantSQL:  " WHERE level = $1 AND type = $2 AND service = $3",
			wantArgs: []any{"ERROR", "PAYMENT", "billing"},
		},
		{
			name:     "query wildcards are escaped",
			filter:   models.LogFilter{Query: `50%_off\now`},
			wantSQL:  " WHERE message ILIKE $1",
			wantArgs: []any{`%50\%\_off\\now%`},
		},
		{
			name:     "attr number matches number or string",
			filter:   models.LogFilter{Attrs: map[string]string{"user_id": "42"}},
			wantSQL:  " WHERE (attrs @> $1::jsonb OR attrs @> $2::jsonb)",
			wantArgs: []any{`{"user_id":"42"}`, `{"user_id":42}`},
		},
		{
			name:     "attr text and sorted keys",
			filter:   models.LogFilter{Level: "INFO", Attrs: map[string]string{"z": "a", "b": "x"}},
			wantSQL:  " WHERE level = $1 AND (attrs @> $2::jsonb) AND (attrs @> $3::jsonb)",
			wantArgs: []any{"INFO", `{"b":"x"}`, `{"z":"a"}`},
		},
		{
			name:     "attr key with quotes is JSON-encoded, not spliced",
			filter:   models.LogFilter{Attrs: map[string]string{`a"}'; DROP`: "v"}},
			wantSQL:  " WHERE (attrs @> $1::jsonb)",
			wantArgs: []any{`{"a\"}'; DROP":"v"}`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sql, args, err := whereClause(tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if sql != tc.wantSQL {
				t.Errorf("sql = %q, want %q", sql, tc.wantSQL)
			}
			if fmt.Sprint(args) != fmt.Sprint(tc.wantArgs) {
				t.Errorf("args = %v, want %v", args, tc.wantArgs)
			}
		})
	}
}

func TestGetLogsLimitAndOffset(t *testing.T) {
	tests := []struct {
		name               string
		filter             models.LogFilter
		wantLimit, wantOff int
	}{
		{"default", models.LogFilter{}, defaultLimit, 0},
		{"given", models.LogFilter{Limit: 10, Offset: 20}, 10, 20},
		{"capped", models.LogFilter{Limit: 9999}, maxLimit, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := newTestStore(t)
			mock.ExpectQuery(`SELECT id, timestamp, level, type, service, message, attrs FROM logs ORDER BY timestamp DESC, id DESC LIMIT \$1 OFFSET \$2`).
				WithArgs(tc.wantLimit, tc.wantOff).
				WillReturnRows(sqlmock.NewRows(logColumns))
			if _, err := store.GetLogs(context.Background(), tc.filter); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestInsertLogsBuildsOneStatement(t *testing.T) {
	store, mock := newTestStore(t)
	ts := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	entries := []models.Log{
		{Level: "ERROR", Type: "DATABASE", Message: "first"},
		{Timestamp: ts, Level: "INFO", Type: "API", Service: "web", Message: "second", Attrs: json.RawMessage(`{"a":1}`)},
	}
	// The sequence may hand ids out of order; they are sorted before use.
	mock.ExpectQuery(`SELECT nextval\(pg_get_serial_sequence\('logs', 'id'\)\) FROM generate_series\(1, \$1\)`).
		WithArgs(2).
		WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(8).AddRow(7))
	mock.ExpectExec(`INSERT INTO logs \(id, timestamp, level, type, service, message, attrs\) VALUES \(\$1, COALESCE\(\$2::timestamptz, now\(\)\), \$3, \$4, \$5, \$6, \$7::jsonb\), \(\$8, COALESCE\(\$9::timestamptz, now\(\)\), \$10, \$11, \$12, \$13, \$14::jsonb\)$`).
		WithArgs(7, nil, "ERROR", "DATABASE", "", "first", "{}", 8, ts, "INFO", "API", "web", "second", `{"a":1}`).
		WillReturnResult(sqlmock.NewResult(0, 2))

	ids, err := store.InsertLogs(context.Background(), entries)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids) != "[7 8]" {
		t.Fatalf("ids = %v", ids)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertLogsFailures(t *testing.T) {
	two := []models.Log{{Message: "a"}, {Message: "b"}}
	reserve := `SELECT nextval`

	t.Run("reserve fails", func(t *testing.T) {
		store, mock := newTestStore(t)
		mock.ExpectQuery(reserve).WillReturnError(errors.New("db down"))
		if _, err := store.InsertLogs(context.Background(), two); err == nil {
			t.Fatal("want the reserve error")
		}
	})
	t.Run("too few ids reserved", func(t *testing.T) {
		store, mock := newTestStore(t)
		mock.ExpectQuery(reserve).WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(1))
		if _, err := store.InsertLogs(context.Background(), two); err == nil {
			t.Fatal("want an error for a short reservation")
		}
	})
	t.Run("insert affects fewer rows", func(t *testing.T) {
		store, mock := newTestStore(t)
		mock.ExpectQuery(reserve).WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(1).AddRow(2))
		mock.ExpectExec(`INSERT INTO logs`).WillReturnResult(sqlmock.NewResult(0, 1))
		if _, err := store.InsertLogs(context.Background(), two); err == nil {
			t.Fatal("want an error when fewer rows were inserted")
		}
	})
}

func TestInsertLogsEmpty(t *testing.T) {
	store, _ := newTestStore(t) // no query expected
	if ids, err := store.InsertLogs(context.Background(), nil); err != nil || ids != nil {
		t.Fatalf("InsertLogs(nil) = %v, %v", ids, err)
	}
}

func TestInsertLogError(t *testing.T) {
	store, mock := newTestStore(t)
	mock.ExpectQuery(`SELECT nextval`).WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(1))
	mock.ExpectExec(`INSERT INTO logs`).WillReturnError(errors.New("db down"))
	if _, err := store.InsertLog(context.Background(), models.Log{Message: "a"}); err == nil {
		t.Fatal("InsertLog() should return the insert error")
	}
}

func TestFacetsErrors(t *testing.T) {
	store, mock := newTestStore(t)
	mock.ExpectQuery(`SELECT DISTINCT type`).WillReturnError(errors.New("db down"))
	if _, err := store.Facets(context.Background()); err == nil {
		t.Fatal("Facets() should return the error")
	}

	store, mock = newTestStore(t)
	mock.ExpectQuery(`SELECT DISTINCT type`).WillReturnRows(sqlmock.NewRows([]string{"type"}).AddRow("API"))
	mock.ExpectQuery(`SELECT DISTINCT service`).WillReturnError(errors.New("db down"))
	if _, err := store.Facets(context.Background()); err == nil {
		t.Fatal("Facets() should return the second error")
	}
}
