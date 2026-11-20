package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	LevelInfo    = "INFO"
	LevelWarning = "WARNING"
	LevelError   = "ERROR"
	LevelDebug   = "DEBUG"

	// Common types. Any type of up to MaxTypeLength characters is accepted.
	TypeSystem   = "SYSTEM"
	TypeAuth     = "AUTH"
	TypeDatabase = "DATABASE"
	TypeUser     = "USER"
	TypeAPI      = "API"

	MaxMessageLength = 10000 // characters, not bytes
	MaxTypeLength    = 50    // matches logs.type varchar(50)
	MaxServiceLength = 100
	MaxAttrsBytes    = 16 << 10
	MaxAttrFilters   = 5
	MaxAttrKeyLength = 100
	MaxQueryLength   = 200

	// A JSON number in attrs may have at most maxNumberLength characters and
	// an exponent within ±maxNumberExponent. That stays far inside PostgreSQL
	// numeric (jsonb refuses 1e1000000) and keeps big.Rat comparisons cheap.
	maxNumberLength   = 100
	maxNumberExponent = 1000

	// MaxClockSkew is how far in the future a client timestamp may be. A
	// later one would pin the row to the top of the list and outlive retention.
	MaxClockSkew = time.Hour
)

var ValidLevels = map[string]bool{
	LevelInfo: true, LevelWarning: true, LevelError: true, LevelDebug: true,
}

// Log represents a log entry.
type Log struct {
	ID int `json:"id"`
	// Timestamp is optional on input; zero means the server's time.
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Type      string    `json:"type"`
	Service   string    `json:"service"`
	Message   string    `json:"message"`
	// Attrs is a JSON object of extra fields (user_id, trace_id, ...).
	Attrs json.RawMessage `json:"attrs,omitempty"`
}

// CanonicalAttrs re-encodes Attrs for storage. The raw bytes may hold escapes
// Go decodes leniently but PostgreSQL jsonb refuses (a lone \ud800 becomes
// U+FFFD here), so only the re-encoded form goes to the database.
func (l *Log) CanonicalAttrs() (json.RawMessage, error) {
	if isNullJSON(l.Attrs) {
		return json.RawMessage("{}"), nil
	}
	obj, err := decodeAttrs(l.Attrs)
	if err != nil {
		return nil, err
	}
	return json.Marshal(obj) // json.Number keeps each number's literal text
}

// Validate checks that the log entry has valid field values.
func (l *Log) Validate() error {
	if l.Message == "" {
		return errors.New("message is required")
	}
	if strings.ContainsRune(l.Message, 0) {
		return errors.New("message must not contain NUL characters")
	}
	// JSON input is always valid UTF-8; a Log built in Go may not be.
	if !utf8.ValidString(l.Message) || !utf8.ValidString(l.Type) || !utf8.ValidString(l.Service) {
		return errors.New("message, type and service must be valid UTF-8")
	}
	if utf8.RuneCountInString(l.Message) > MaxMessageLength {
		return fmt.Errorf("message exceeds %d characters", MaxMessageLength)
	}
	if !ValidLevels[l.Level] {
		return errors.New("invalid level: must be one of INFO, WARNING, ERROR, DEBUG")
	}
	if l.Timestamp.After(time.Now().Add(MaxClockSkew)) {
		return errors.New("timestamp is more than an hour in the future")
	}
	if l.Type == "" {
		return errors.New("type is required")
	}
	if err := validateName("type", l.Type, MaxTypeLength); err != nil {
		return err
	}
	if err := validateName("service", l.Service, MaxServiceLength); err != nil {
		return err
	}
	return validateAttrs(l.Attrs)
}

func validateName(field, v string, max int) error {
	if utf8.RuneCountInString(v) > max {
		return fmt.Errorf("%s exceeds %d characters", field, max)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain control characters", field)
		}
	}
	return nil
}

func validateAttrs(raw json.RawMessage) error {
	if isNullJSON(raw) {
		return nil
	}
	if len(raw) > MaxAttrsBytes {
		return fmt.Errorf("attrs exceed %d bytes", MaxAttrsBytes)
	}
	obj, err := decodeAttrs(raw)
	if err != nil {
		return errors.New("attrs must be a JSON object")
	}
	// PostgreSQL jsonb cannot store U+0000; catch it here instead of as a 500.
	if containsNUL(obj) {
		return errors.New("attrs must not contain NUL characters")
	}
	if !numbersStorable(obj) {
		return fmt.Errorf("attrs contain a number with more than %d characters or an exponent beyond ±%d", maxNumberLength, maxNumberExponent)
	}
	return nil
}

// StorableNumber reports whether a JSON number literal fits the limits
// above, so PostgreSQL jsonb accepts it.
func StorableNumber(n string) bool {
	if len(n) > maxNumberLength {
		return false
	}
	i := strings.IndexAny(n, "eE")
	if i < 0 {
		return true
	}
	exp, err := strconv.Atoi(n[i+1:])
	return err == nil && exp >= -maxNumberExponent && exp <= maxNumberExponent
}

func numbersStorable(v any) bool {
	switch t := v.(type) {
	case json.Number:
		return StorableNumber(t.String())
	case map[string]any:
		for _, child := range t {
			if !numbersStorable(child) {
				return false
			}
		}
	case []any:
		for _, child := range t {
			if !numbersStorable(child) {
				return false
			}
		}
	}
	return true
}

func isNullJSON(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func decodeAttrs(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, errors.New("not an object")
	}
	return obj, nil
}

func containsNUL(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.ContainsRune(t, 0)
	case map[string]any:
		for k, child := range t {
			if strings.ContainsRune(k, 0) || containsNUL(child) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if containsNUL(child) {
				return true
			}
		}
	}
	return false
}

// LogFilter represents filters for querying logs. The database (SQL) and
// Matches (Go, for the live stream) must agree on every field.
type LogFilter struct {
	Level   string            `json:"level,omitempty"`
	Type    string            `json:"type,omitempty"`
	Service string            `json:"service,omitempty"`
	Query   string            `json:"q,omitempty"`     // message contains, case-insensitive
	Attrs   map[string]string `json:"attrs,omitempty"` // key -> value, see AttrCandidates
	Limit   int               `json:"limit,omitempty"`
	Offset  int               `json:"offset,omitempty"`
}

// Validate rejects filter values that no log can match or that are too large.
func (f LogFilter) Validate() error {
	if f.Level != "" && !ValidLevels[f.Level] {
		return errors.New("invalid level")
	}
	if utf8.RuneCountInString(f.Type) > MaxTypeLength {
		return errors.New("type filter too long")
	}
	if utf8.RuneCountInString(f.Service) > MaxServiceLength {
		return errors.New("service filter too long")
	}
	if utf8.RuneCountInString(f.Query) > MaxQueryLength {
		return fmt.Errorf("q exceeds %d characters", MaxQueryLength)
	}
	if len(f.Attrs) > MaxAttrFilters {
		return fmt.Errorf("at most %d attr filters", MaxAttrFilters)
	}
	for k, v := range f.Attrs {
		if k == "" || utf8.RuneCountInString(k) > MaxAttrKeyLength {
			return errors.New("invalid attr filter key")
		}
		if strings.ContainsRune(k, 0) || strings.ContainsRune(v, 0) {
			return errors.New("attr filter must not contain NUL characters")
		}
	}
	if strings.ContainsRune(f.Type+f.Service+f.Query, 0) {
		return errors.New("filter must not contain NUL characters")
	}
	// Query-string values are not cleaned the way JSON strings are, and
	// PostgreSQL refuses invalid UTF-8 text with an error, not a non-match.
	valid := utf8.ValidString(f.Type) && utf8.ValidString(f.Service) && utf8.ValidString(f.Query)
	for k, v := range f.Attrs {
		valid = valid && utf8.ValidString(k) && utf8.ValidString(v)
	}
	if !valid {
		return errors.New("filter must be valid UTF-8")
	}
	return nil
}

// Matches reports whether l passes every filter, with the same rules the
// database applies in GetLogs.
func (f LogFilter) Matches(l Log) bool {
	if (f.Level != "" && l.Level != f.Level) ||
		(f.Type != "" && l.Type != f.Type) ||
		(f.Service != "" && l.Service != f.Service) {
		return false
	}
	if f.Query != "" && !strings.Contains(strings.ToLower(l.Message), strings.ToLower(f.Query)) {
		return false
	}
	if len(f.Attrs) == 0 {
		return true
	}
	if isNullJSON(l.Attrs) {
		return false
	}
	attrs, err := decodeAttrs(l.Attrs)
	if err != nil {
		return false
	}
	for k, want := range f.Attrs {
		got, ok := attrs[k]
		if !ok || !matchesAny(got, AttrCandidates(want)) {
			return false
		}
	}
	return true
}

// AttrCandidates lists the JSON values an attr filter value stands for: the
// string itself and, when it parses as a JSON number, true, false or null,
// that literal too. So attr.user_id=42 finds both 42 and "42".
func AttrCandidates(v string) []any {
	out := []any{v}
	dec := json.NewDecoder(strings.NewReader(v))
	dec.UseNumber()
	var lit any
	if err := dec.Decode(&lit); err != nil {
		return out
	}
	if _, err := dec.Token(); err != io.EOF { // trailing input, e.g. "42abc"
		return out
	}
	switch n := lit.(type) {
	case json.Number:
		if StorableNumber(n.String()) { // else the ::jsonb cast would fail
			return append(out, lit)
		}
	case bool, nil:
		return append(out, lit)
	}
	return out
}

// matchesAny compares like jsonb containment of a scalar: numbers by value
// (42 == 42.0), everything else exactly.
func matchesAny(got any, candidates []any) bool {
	for _, c := range candidates {
		switch want := c.(type) {
		case string:
			if s, ok := got.(string); ok && s == want {
				return true
			}
		case json.Number:
			if n, ok := got.(json.Number); ok && numbersEqual(n, want) {
				return true
			}
		case bool:
			if b, ok := got.(bool); ok && b == want {
				return true
			}
		case nil:
			if got == nil {
				return true
			}
		}
	}
	return false
}

func numbersEqual(a, b json.Number) bool {
	x, okX := new(big.Rat).SetString(a.String())
	y, okY := new(big.Rat).SetString(b.String())
	return okX && okY && x.Cmp(y) == 0
}

// Facets are the filter values seen in recent logs.
type Facets struct {
	Types    []string `json:"types"`
	Services []string `json:"services"`
}

// LogEvent is what a listener delivers. Resync means notifications may have
// been lost (the listener reconnected or a read failed) and consumers should
// reload the list instead of trusting the stream to be complete.
type LogEvent struct {
	Log    Log
	Resync bool
}
