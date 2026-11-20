package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mstgnz/golog/v2/models"
)

func TestPrintLog(t *testing.T) {
	var buf bytes.Buffer
	printLog(&buf, models.Log{
		ID:        1,
		Timestamp: time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC),
		Level:     "ERROR",
		Type:      "DATABASE",
		Message:   "Connection failed",
	})
	want := "[2023-01-01 12:00:00] \033[31mERROR\033[0m [DATABASE]: Connection failed\n"
	if buf.String() != want {
		t.Fatalf("printLog() = %q, want %q", buf.String(), want)
	}
}

func TestPrintLogNeutralisesTerminalEscapes(t *testing.T) {
	var buf bytes.Buffer
	printLog(&buf, models.Log{
		Level:   "INFO",
		Type:    "API\x1b[2J",
		Message: "line1\nline2\tx\x1b]0;pwned\x07\rfake\u009b31m",
	})
	out := buf.String()
	// The only ESC bytes left are the ones printLog itself writes for colour.
	if strings.Count(out, "\x1b") != 2 || strings.ContainsAny(out, "\r\x07\u009b") {
		t.Fatalf("control characters leaked: %q", out)
	}
	if !strings.Contains(out, "line1\nline2\tx") {
		t.Fatalf("newlines and tabs should be kept: %q", out)
	}
}

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    models.LogFilter
		wantErr string
	}{
		{name: "no flags", args: nil},
		{name: "level", args: []string{"-level=ERROR"}, want: models.LogFilter{Level: "ERROR"}},
		{name: "level any case, type kept as typed", args: []string{"-level=error", "-type=database"}, want: models.LogFilter{Level: "ERROR", Type: "database"}},
		{name: "all filters", args: []string{"-service=billing", "-q=card declined", "-attr", "user_id=42", "-attr=trace=a=b"},
			want: models.LogFilter{Service: "billing", Query: "card declined", Attrs: map[string]string{"user_id": "42", "trace": "a=b"}}},
		{name: "unknown level", args: []string{"-level=TRACE"}, wantErr: "invalid level"},
		{name: "type too long", args: []string{"-type=" + strings.Repeat("t", models.MaxTypeLength+1)}, wantErr: "type filter too long"},
		{name: "attr without value", args: []string{"-attr=user_id"}, wantErr: "want key=value"},
		{name: "attr with empty key", args: []string{"-attr==1"}, wantErr: "want key=value"},
		{name: "attr twice", args: []string{"-attr=a=1", "-attr=a=2"}, wantErr: "given twice"},
		{name: "stray argument", args: []string{"ERROR"}, wantErr: "unexpected argument"},
		{name: "unknown flag", args: []string{"-colour"}, wantErr: "not defined"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFlags(tc.args, noEnv, io.Discard)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseFlags() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got.filter, tc.want) || got.url != "" {
				t.Fatalf("parseFlags() = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func noEnv(string) string { return "" }

func TestParseFlagsURL(t *testing.T) {
	env := func(k string) string {
		if k == "GOLOG_URL" {
			return "https://from-env.example"
		}
		return ""
	}
	got, err := parseFlags(nil, env, io.Discard)
	if err != nil || got.url != "https://from-env.example" {
		t.Fatalf("url from env = %q, %v", got.url, err)
	}
	got, err = parseFlags([]string{"-url=https://flag.example"}, env, io.Discard)
	if err != nil || got.url != "https://flag.example" {
		t.Fatalf("flag should win over env, got %q, %v", got.url, err)
	}
}

func TestParseFlagsHelp(t *testing.T) {
	if _, err := parseFlags([]string{"-h"}, noEnv, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlags(-h) = %v, want flag.ErrHelp", err)
	}
}

func TestPrintLogServiceAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	printLog(&buf, models.Log{
		Timestamp: time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC), Level: "INFO", Type: "PAYMENT", Service: "billing",
		Message: "ok", Attrs: json.RawMessage("{ \"user_id\": 42,\n \"note\": \"\\u001b[2J\" }"),
	})
	want := "[2023-01-01 12:00:00] \033[32mINFO\033[0m [PAYMENT] billing: ok {\"user_id\":42,\"note\":\"\\u001b[2J\"}\n"
	if buf.String() != want {
		t.Fatalf("printLog() = %q, want %q", buf.String(), want)
	}

	buf.Reset()
	printLog(&buf, models.Log{Level: "INFO", Type: "API", Message: "m", Attrs: json.RawMessage(`{}`)})
	if strings.Contains(buf.String(), "{}") {
		t.Fatalf("empty attrs printed: %q", buf.String())
	}
}

func TestPrinterHeaderShowsAllFilters(t *testing.T) {
	var buf bytes.Buffer
	newPrinter(&buf, models.LogFilter{Service: "billing", Query: "x", Attrs: map[string]string{"k": "v"}}).history(nil)
	for _, want := range []string{"Service filter: billing", "Text filter: x", "Attr filter: k=v"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("header misses %q:\n%s", want, buf.String())
		}
	}
}

func TestPrinter(t *testing.T) {
	var buf bytes.Buffer
	p := newPrinter(&buf, models.LogFilter{Level: "ERROR"})
	// The store returns newest first.
	p.history([]models.Log{
		{ID: 2, Level: "ERROR", Type: "API", Message: "second"},
		{ID: 1, Level: "ERROR", Type: "API", Message: "first"},
	})
	if strings.Index(buf.String(), "first") > strings.Index(buf.String(), "second") {
		t.Fatalf("history should print oldest first:\n%s", buf.String())
	}

	buf.Reset()
	p.handle(models.LogEvent{Log: models.Log{ID: 2, Level: "ERROR", Message: "second"}}) // overlap with history
	p.handle(models.LogEvent{Log: models.Log{ID: 3, Level: "INFO", Message: "filtered"}})
	p.handle(models.LogEvent{Log: models.Log{ID: 4, Level: "ERROR", Message: "fourth"}})
	p.handle(models.LogEvent{Resync: true})
	out := buf.String()

	if strings.Contains(out, "second") {
		t.Errorf("entry already shown from history was printed again:\n%s", out)
	}
	if strings.Contains(out, "filtered") {
		t.Errorf("filtered entry was printed:\n%s", out)
	}
	if !strings.Contains(out, "fourth") || !strings.Contains(out, "reconnected") {
		t.Errorf("missing new entry or resync notice:\n%s", out)
	}
}

func TestPrinterEmptyHistory(t *testing.T) {
	var buf bytes.Buffer
	newPrinter(&buf, models.LogFilter{Type: "AUTH"}).history(nil)
	if !strings.Contains(buf.String(), "No logs found") || !strings.Contains(buf.String(), "Type filter: AUTH") {
		t.Fatalf("unexpected header:\n%s", buf.String())
	}
}

func TestRunRejectsBadFlagsBeforeConnecting(t *testing.T) {
	// No database is reachable here; the flag error must come first.
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	err := run(context.Background(), []string{"-level=nope"}, noEnv, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid level") {
		t.Fatalf("run() = %v, want a flag error", err)
	}
}

func TestRunDatabaseError(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=2")
	err := run(context.Background(), nil, noEnv, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "connect to database") {
		t.Fatalf("run() = %v, want a connection error", err)
	}
}
