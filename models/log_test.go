package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLogJSON(t *testing.T) {
	// Create a test log with a fixed timestamp
	timestamp := time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC)
	log := Log{
		ID:        1,
		Timestamp: timestamp,
		Level:     "ERROR",
		Type:      "DATABASE",
		Message:   "Connection failed",
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(log)
	if err != nil {
		t.Fatalf("Failed to marshal Log to JSON: %v", err)
	}

	// Unmarshal back to a new struct
	var unmarshaledLog Log
	err = json.Unmarshal(jsonData, &unmarshaledLog)
	if err != nil {
		t.Fatalf("Failed to unmarshal JSON to Log: %v", err)
	}

	// Check fields
	if unmarshaledLog.ID != log.ID {
		t.Errorf("ID mismatch: got %d, want %d", unmarshaledLog.ID, log.ID)
	}

	if !unmarshaledLog.Timestamp.Equal(log.Timestamp) {
		t.Errorf("Timestamp mismatch: got %v, want %v", unmarshaledLog.Timestamp, log.Timestamp)
	}

	if unmarshaledLog.Level != log.Level {
		t.Errorf("Level mismatch: got %s, want %s", unmarshaledLog.Level, log.Level)
	}

	if unmarshaledLog.Type != log.Type {
		t.Errorf("Type mismatch: got %s, want %s", unmarshaledLog.Type, log.Type)
	}

	if unmarshaledLog.Message != log.Message {
		t.Errorf("Message mismatch: got %s, want %s", unmarshaledLog.Message, log.Message)
	}
}

func TestLogValidate(t *testing.T) {
	tests := []struct {
		name    string
		log     Log
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid log",
			log:     Log{Level: LevelInfo, Type: TypeSystem, Message: "ok"},
			wantErr: false,
		},
		{
			name:    "missing message",
			log:     Log{Level: LevelError, Type: TypeAuth},
			wantErr: true,
			errMsg:  "message is required",
		},
		{
			name:    "invalid level",
			log:     Log{Level: "TRACE", Type: TypeAPI, Message: "test"},
			wantErr: true,
			errMsg:  "invalid level",
		},
		{
			name: "free-form type and service with attrs",
			log: Log{Level: LevelDebug, Type: "PAYMENT", Service: "billing", Message: "test",
				Attrs: json.RawMessage(`{"user_id":42,"nested":{"ok":true},"tags":["a"]}`)},
		},
		{
			name: "client timestamp in the past",
			log:  Log{Timestamp: time.Now().Add(-48 * time.Hour), Level: LevelInfo, Type: "API", Message: "late"},
		},
		{
			name: "client timestamp slightly ahead",
			log:  Log{Timestamp: time.Now().Add(MaxClockSkew - time.Minute), Level: LevelInfo, Type: "API", Message: "skew"},
		},
		{
			name:    "client timestamp far in the future",
			log:     Log{Timestamp: time.Now().Add(MaxClockSkew + time.Minute), Level: LevelInfo, Type: "API", Message: "future"},
			wantErr: true,
			errMsg:  "future",
		},
		{
			name:    "missing type",
			log:     Log{Level: LevelDebug, Message: "test"},
			wantErr: true,
			errMsg:  "type is required",
		},
		{
			name:    "type too long",
			log:     Log{Level: LevelDebug, Type: strings.Repeat("t", MaxTypeLength+1), Message: "test"},
			wantErr: true,
			errMsg:  "type exceeds",
		},
		{
			name: "type at the limit in multi-byte characters",
			log:  Log{Level: LevelDebug, Type: strings.Repeat("ş", MaxTypeLength), Message: "test"},
		},
		{
			name:    "control character in type",
			log:     Log{Level: LevelDebug, Type: "API\x1b[2J", Message: "test"},
			wantErr: true,
			errMsg:  "type must not contain control characters",
		},
		{
			name:    "service too long",
			log:     Log{Level: LevelDebug, Type: "API", Service: strings.Repeat("s", MaxServiceLength+1), Message: "test"},
			wantErr: true,
			errMsg:  "service exceeds",
		},
		{
			name:    "control character in service",
			log:     Log{Level: LevelDebug, Type: "API", Service: "bill\ning", Message: "test"},
			wantErr: true,
			errMsg:  "service must not contain control characters",
		},
		{
			name:    "message too long",
			log:     Log{Level: LevelInfo, Type: TypeUser, Message: strings.Repeat("x", MaxMessageLength+1)},
			wantErr: true,
			errMsg:  "message exceeds",
		},
		{
			name: "message limit counts characters, not bytes",
			log:  Log{Level: LevelInfo, Type: TypeUser, Message: strings.Repeat("ü", MaxMessageLength)},
		},
		{
			name:    "NUL in message",
			log:     Log{Level: LevelInfo, Type: TypeUser, Message: "a\x00b"},
			wantErr: true,
			errMsg:  "NUL",
		},
		{
			name:    "invalid UTF-8 in a Log built in Go",
			log:     Log{Level: LevelInfo, Type: TypeUser, Message: "bad \xff byte"},
			wantErr: true,
			errMsg:  "UTF-8",
		},
		{
			name: "null attrs are empty",
			log:  Log{Level: LevelInfo, Type: TypeUser, Message: "x", Attrs: json.RawMessage(`null`)},
		},
		{
			name:    "attrs not an object",
			log:     Log{Level: LevelInfo, Type: TypeUser, Message: "x", Attrs: json.RawMessage(`[1,2]`)},
			wantErr: true,
			errMsg:  "attrs must be a JSON object",
		},
		{
			name:    "attrs string",
			log:     Log{Level: LevelInfo, Type: TypeUser, Message: "x", Attrs: json.RawMessage(`"x"`)},
			wantErr: true,
			errMsg:  "attrs must be a JSON object",
		},
		{
			name:    "attrs too large",
			log:     Log{Level: LevelInfo, Type: TypeUser, Message: "x", Attrs: json.RawMessage(`{"k":"` + strings.Repeat("v", MaxAttrsBytes) + `"}`)},
			wantErr: true,
			errMsg:  "attrs exceed",
		},
		{
			name:    "NUL deep inside attrs",
			log:     Log{Level: LevelInfo, Type: TypeUser, Message: "x", Attrs: json.RawMessage(`{"a":[{"b":"x\u0000y"}]}`)},
			wantErr: true,
			errMsg:  "attrs must not contain NUL",
		},
		{
			name:    "NUL in attrs key",
			log:     Log{Level: LevelInfo, Type: TypeUser, Message: "x", Attrs: json.RawMessage(`{"a\u0000":1}`)},
			wantErr: true,
			errMsg:  "attrs must not contain NUL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.log.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Validate() expected error, got nil")
				}
				if tc.errMsg != "" && !strings.Contains(err.Error(), tc.errMsg) {
					t.Errorf("Validate() error = %q, want to contain %q", err.Error(), tc.errMsg)
				}
			} else if err != nil {
				t.Fatalf("Validate() unexpected error: %v", err)
			}
		})
	}
}

func TestLogFilterValidate(t *testing.T) {
	tests := []struct {
		name    string
		filter  LogFilter
		wantErr string
	}{
		{name: "empty", filter: LogFilter{}},
		{name: "valid both", filter: LogFilter{Level: LevelError, Type: TypeDatabase}},
		{name: "invalid level", filter: LogFilter{Level: "TRACE"}, wantErr: "invalid level"},
		{name: "lowercase level is invalid", filter: LogFilter{Level: "error"}, wantErr: "invalid level"},
		{name: "free-form type", filter: LogFilter{Type: "NETWORK", Service: "billing", Query: "timeout"}},
		{name: "type too long", filter: LogFilter{Type: strings.Repeat("t", MaxTypeLength+1)}, wantErr: "type filter too long"},
		{name: "service too long", filter: LogFilter{Service: strings.Repeat("s", MaxServiceLength+1)}, wantErr: "service filter too long"},
		{name: "query too long", filter: LogFilter{Query: strings.Repeat("q", MaxQueryLength+1)}, wantErr: "q exceeds"},
		{name: "too many attr filters", filter: LogFilter{Attrs: map[string]string{"a": "1", "b": "1", "c": "1", "d": "1", "e": "1", "f": "1"}}, wantErr: "at most"},
		{name: "empty attr key", filter: LogFilter{Attrs: map[string]string{"": "1"}}, wantErr: "invalid attr filter key"},
		{name: "NUL in attr value", filter: LogFilter{Attrs: map[string]string{"k": "a\x00"}}, wantErr: "NUL"},
		{name: "NUL in query", filter: LogFilter{Query: "a\x00"}, wantErr: "NUL"},
		{name: "invalid UTF-8 in query", filter: LogFilter{Query: "\xff"}, wantErr: "UTF-8"},
		{name: "invalid UTF-8 in type", filter: LogFilter{Type: "a\xfe"}, wantErr: "UTF-8"},
		{name: "invalid UTF-8 in service", filter: LogFilter{Service: "\xc3"}, wantErr: "UTF-8"},
		{name: "invalid UTF-8 in attr key", filter: LogFilter{Attrs: map[string]string{"\xff": "1"}}, wantErr: "UTF-8"},
		{name: "invalid UTF-8 in attr value", filter: LogFilter{Attrs: map[string]string{"k": "\xff"}}, wantErr: "UTF-8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.filter.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestLogFilterMatches(t *testing.T) {
	entry := Log{
		Level: LevelError, Type: TypeDatabase, Service: "billing", Message: "Card DECLINED for order 7",
		Attrs: json.RawMessage(`{"user_id":42,"code":"042","ok":false,"gone":null,"ratio":1.5,"nested":{"a":1}}`),
	}
	attr := func(k, v string) LogFilter { return LogFilter{Attrs: map[string]string{k: v}} }
	tests := []struct {
		name   string
		filter LogFilter
		want   bool
	}{
		{name: "no filter", filter: LogFilter{}, want: true},
		{name: "level match", filter: LogFilter{Level: LevelError}, want: true},
		{name: "level mismatch", filter: LogFilter{Level: LevelInfo}, want: false},
		{name: "type match", filter: LogFilter{Type: TypeDatabase}, want: true},
		{name: "type is case-sensitive", filter: LogFilter{Type: "database"}, want: false},
		{name: "service match", filter: LogFilter{Service: "billing"}, want: true},
		{name: "service mismatch", filter: LogFilter{Service: "auth"}, want: false},
		{name: "query is case-insensitive", filter: LogFilter{Query: "card declined"}, want: true},
		{name: "query not found", filter: LogFilter{Query: "approved"}, want: false},
		{name: "query wildcards are literal", filter: LogFilter{Query: "card%order"}, want: false},
		{name: "attr number given as text", filter: attr("user_id", "42"), want: true},
		{name: "attr number written differently", filter: attr("user_id", "42.0"), want: true},
		{name: "attr number mismatch", filter: attr("user_id", "43"), want: false},
		{name: "attr string keeps leading zero", filter: attr("code", "042"), want: true},
		{name: "attr string is not a number match", filter: attr("code", "42"), want: false},
		{name: "attr bool", filter: attr("ok", "false"), want: true},
		{name: "attr null", filter: attr("gone", "null"), want: true},
		{name: "attr missing key", filter: attr("missing", "null"), want: false},
		{name: "attr float", filter: attr("ratio", "1.50"), want: true},
		{name: "attr object never matches a scalar", filter: attr("nested", `{"a":1}`), want: false},
		{name: "all filters together", filter: LogFilter{Level: LevelError, Service: "billing", Query: "order",
			Attrs: map[string]string{"user_id": "42", "ok": "false"}}, want: true},
		{name: "one attr of two fails", filter: LogFilter{Attrs: map[string]string{"user_id": "42", "ok": "true"}}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.filter.Matches(entry); got != tc.want {
				t.Fatalf("Matches() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMatchesWithoutAttrs(t *testing.T) {
	f := LogFilter{Attrs: map[string]string{"k": "v"}}
	for _, raw := range []string{"", "null", "{}", "not json"} {
		if f.Matches(Log{Attrs: json.RawMessage(raw)}) {
			t.Errorf("attrs %q matched an attr filter", raw)
		}
	}
}

func TestAttrCandidates(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"42", `["42",42]`},
		{"-1.5e3", `["-1.5e3",-1.5e3]`},
		{"true", `["true",true]`},
		{"null", `["null",null]`},
		{"abc", `["abc"]`},
		{"42abc", `["42abc"]`},
		{`"quoted"`, `["\"quoted\""]`},
		{`{"a":1}`, `["{\"a\":1}"]`},
		{"", `[""]`},
	}
	for _, tc := range tests {
		got, err := json.Marshal(AttrCandidates(tc.in))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("AttrCandidates(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// Review finding: Go's decoder accepts JSON that PostgreSQL jsonb refuses,
// which surfaced as a 500 (and failed whole batches).
func TestValidateRejectsNumbersPostgresCannotStore(t *testing.T) {
	for _, attrs := range []string{`{"a":1e1000000}`, `{"a":[{"b":-1e-5000}]}`, `{"a":1` + strings.Repeat("0", 200) + `}`} {
		l := Log{Level: LevelInfo, Type: "A", Message: "m", Attrs: json.RawMessage(attrs)}
		if err := l.Validate(); err == nil || !strings.Contains(err.Error(), "number") {
			t.Errorf("Validate(%.40s) = %v, want a number error", attrs, err)
		}
	}
	ok := Log{Level: LevelInfo, Type: "A", Message: "m", Attrs: json.RawMessage(`{"a":1.5e300,"b":-12345678901234567890}`)}
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate(normal numbers) = %v", err)
	}
}

func TestCanonicalAttrs(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "{}"},
		{"null", "{}"},
		{`{"a":"\ud800"}`, `{"a":"` + "�" + `"}`}, // lone surrogate: jsonb refuses the escape
		{`{ "n": 1.50, "big": 12345678901234567890 }`, `{"big":12345678901234567890,"n":1.50}`},
	}
	for _, tc := range tests {
		l := Log{Attrs: json.RawMessage(tc.in)}
		got, err := l.CanonicalAttrs()
		if err != nil || string(got) != tc.want {
			t.Errorf("CanonicalAttrs(%q) = %s, %v; want %s", tc.in, got, err, tc.want)
		}
	}
	if _, err := (&Log{Attrs: json.RawMessage(`[1]`)}).CanonicalAttrs(); err == nil {
		t.Error("CanonicalAttrs of a non-object should fail")
	}
}

func TestAttrCandidatesSkipNumbersPostgresCannotStore(t *testing.T) {
	got, _ := json.Marshal(AttrCandidates("1e999999"))
	if string(got) != `["1e999999"]` {
		t.Fatalf("AttrCandidates(1e999999) = %s, want only the string form", got)
	}
}
