package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/mstgnz/golog/v2/models"
)

const (
	defaultLimit = 100
	maxLimit     = 500
	// facetWindow bounds the facets query to the newest rows, so its cost
	// does not grow with the table.
	facetWindow = 10000

	notifyChannel = "log_channel"
	selectColumns = "id, timestamp, level, type, service, message, attrs"
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
	var attrs []byte // *[]byte copies; scanning into *json.RawMessage would alias the driver buffer
	if err := row.Scan(&l.ID, &ts, &l.Level, &l.Type, &l.Service, &l.Message, &attrs); err != nil {
		return models.Log{}, err
	}
	l.Timestamp = ts.Time
	l.Attrs = json.RawMessage(attrs)
	return l, nil
}

// escapeLike makes s match literally inside a LIKE/ILIKE pattern.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// whereClause turns a filter into SQL with the same rules as
// models.LogFilter.Matches. Placeholders start at $1.
func whereClause(f models.LogFilter) (string, []any, error) {
	var conds []string
	var args []any
	// add replaces each ? in cond, left to right, with the next placeholder.
	add := func(cond string, vals ...any) {
		for _, v := range vals {
			args = append(args, v)
			cond = strings.Replace(cond, "?", fmt.Sprintf("$%d", len(args)), 1)
		}
		conds = append(conds, cond)
	}
	if f.Level != "" {
		add("level = ?", f.Level)
	}
	if f.Type != "" {
		add("type = ?", f.Type)
	}
	if f.Service != "" {
		add("service = ?", f.Service)
	}
	if f.Query != "" {
		add("message ILIKE ?", "%"+escapeLike(f.Query)+"%")
	}

	keys := make([]string, 0, len(f.Attrs))
	for k := range f.Attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys) // stable SQL text for the same filter
	for _, k := range keys {
		var alts []string
		var vals []any
		for _, c := range models.AttrCandidates(f.Attrs[k]) {
			doc, err := json.Marshal(map[string]any{k: c})
			if err != nil {
				return "", nil, err
			}
			alts = append(alts, "attrs @> ?::jsonb")
			vals = append(vals, string(doc))
		}
		add("("+strings.Join(alts, " OR ")+")", vals...)
	}

	if len(conds) == 0 {
		return "", nil, nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args, nil
}

// GetLogs retrieves log entries, newest first, with optional filtering and pagination.
func (s *Store) GetLogs(ctx context.Context, filter models.LogFilter) ([]models.Log, error) {
	where, args, err := whereClause(filter)
	if err != nil {
		return nil, err
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	// id breaks timestamp ties so pages are stable; matches logs_timestamp_id_idx.
	query := "SELECT " + selectColumns + " FROM logs" + where +
		fmt.Sprintf(" ORDER BY timestamp DESC, id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
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
	row := s.db.QueryRowContext(ctx, "SELECT "+selectColumns+" FROM logs WHERE id = $1", id)
	l, err := scanLog(row)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Log{}, ErrNotFound
	}
	return l, err
}

// InsertLog inserts a new log entry and returns its ID.
func (s *Store) InsertLog(ctx context.Context, logEntry models.Log) (int, error) {
	ids, err := s.InsertLogs(ctx, []models.Log{logEntry})
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}

// InsertLogs inserts all entries in one statement (all or nothing) and
// returns their ids in input order.
func (s *Store) InsertLogs(ctx context.Context, entries []models.Log) ([]int, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	ids, err := s.reserveIDs(ctx, len(entries))
	if err != nil {
		return nil, err
	}

	const cols = 7
	var b strings.Builder
	b.WriteString("INSERT INTO logs (id, timestamp, level, type, service, message, attrs) VALUES ")
	args := make([]any, 0, len(entries)*cols)
	for i, e := range entries {
		if i > 0 {
			b.WriteString(", ")
		}
		n := i * cols
		// A client timestamp is kept (batched senders deliver late); none means now().
		fmt.Fprintf(&b, "($%d, COALESCE($%d::timestamptz, now()), $%d, $%d, $%d, $%d, $%d::jsonb)", n+1, n+2, n+3, n+4, n+5, n+6, n+7)
		var ts any
		if !e.Timestamp.IsZero() {
			ts = e.Timestamp
		}
		attrs, err := e.CanonicalAttrs() // never the raw bytes: see CanonicalAttrs
		if err != nil {
			return nil, fmt.Errorf("entry %d attrs: %w", i, err)
		}
		args = append(args, ids[i], ts, e.Level, e.Type, e.Service, e.Message, string(attrs))
	}

	res, err := s.db.ExecContext(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil || n != int64(len(entries)) {
		return nil, fmt.Errorf("inserted %d rows, expected %d (%v)", n, len(entries), err)
	}
	return ids, nil
}

// reserveIDs takes n ids from the logs id sequence, ascending. Inserting
// with them makes ids[i] belong to entries[i] by construction; the order of
// RETURNING rows is not documented. Unused ids after a failure are gaps,
// as with any sequence.
func (s *Store) reserveIDs(ctx context.Context, n int) ([]int, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT nextval(pg_get_serial_sequence('logs', 'id')) FROM generate_series(1, $1)", n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int, 0, n)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) != n {
		return nil, fmt.Errorf("reserved %d ids, expected %d", len(ids), n)
	}
	sort.Ints(ids) // ascending, so ids follow input order like plain inserts do
	return ids, nil
}

// Facets lists the distinct types and services among the newest
// facetWindow entries, for filter dropdowns.
func (s *Store) Facets(ctx context.Context) (models.Facets, error) {
	types, err := s.distinctRecent(ctx, "type")
	if err != nil {
		return models.Facets{}, err
	}
	services, err := s.distinctRecent(ctx, "service")
	if err != nil {
		return models.Facets{}, err
	}
	return models.Facets{Types: types, Services: services}, nil
}

// distinctRecent: column is one of two constants above, never user input.
func (s *Store) distinctRecent(ctx context.Context, column string) ([]string, error) {
	query := fmt.Sprintf(`SELECT DISTINCT %[1]s FROM (
		SELECT %[1]s FROM logs ORDER BY timestamp DESC, id DESC LIMIT $1
	) recent WHERE %[1]s <> '' ORDER BY 1`, column)
	rows, err := s.db.QueryContext(ctx, query, facetWindow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
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
