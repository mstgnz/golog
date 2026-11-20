package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"unicode"

	"github.com/mstgnz/golog/v2/models"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	switch {
	case errors.Is(err, flag.ErrHelp):
		return
	case err != nil:
		fmt.Fprintf(os.Stderr, "golog-cli: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	filter models.LogFilter
	url    string // empty: read the database directly
}

func parseFlags(args []string, getenv func(string) string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("golog-cli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	level := fs.String("level", "", "Filter by level: INFO, WARNING, ERROR, DEBUG (any case)")
	typ := fs.String("type", "", "Filter by type (exact, case-sensitive)")
	service := fs.String("service", "", "Filter by service (exact)")
	query := fs.String("q", "", "Filter by text in the message (case-insensitive)")
	attrs := attrFlag{}
	fs.Var(attrs, "attr", "Filter by attribute, key=value; repeat for more")
	serverURL := fs.String("url", getenv("GOLOG_URL"),
		"golog-server URL; uses the API with GOLOG_READ_TOKEN instead of the database (env GOLOG_URL)")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	filter := models.LogFilter{
		Level: strings.ToUpper(*level), Type: *typ, Service: *service, Query: *query,
	}
	if len(attrs) > 0 {
		filter.Attrs = attrs
	}
	if err := filter.Validate(); err != nil {
		return options{}, fmt.Errorf("%w (see -h)", err)
	}
	return options{filter: filter, url: *serverURL}, nil
}

func openSource(ctx context.Context, opts options, getenv func(string) string, stderr io.Writer) (source, error) {
	if opts.url == "" {
		return openDBSource(ctx)
	}
	token := getenv("GOLOG_READ_TOKEN") // never a flag: it would land in shell history
	if plainHTTPWithToken(opts.url, token) {
		fmt.Fprintln(stderr, "golog-cli: WARNING: sending the read token over plain http; use https")
	}
	return newHTTPSource(opts.url, token)
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	opts, err := parseFlags(args, getenv, stderr)
	if err != nil {
		return err
	}
	src, err := openSource(ctx, opts, getenv, stderr)
	if err != nil {
		return err
	}
	defer src.Close()

	ctx, cancel := context.WithCancel(ctx) // stops the stream on every return path
	defer cancel()

	// Listen before reading history so nothing written in between is lost;
	// the printer drops the overlap by id.
	events := make(chan models.LogEvent, 64)
	if err := src.Stream(ctx, opts.filter, events); err != nil {
		return fmt.Errorf("listen for logs: %w", err)
	}
	logs, err := src.History(ctx, opts.filter)
	if err != nil {
		return fmt.Errorf("read logs: %w", err)
	}

	p := newPrinter(stdout, opts.filter)
	p.history(logs)
	for ev := range events { // closed when ctx is cancelled
		p.handle(ev)
	}
	fmt.Fprintln(stdout, "\nShutting down...")
	return nil
}

type printer struct {
	out    io.Writer
	filter models.LogFilter
	// seen holds ids printed from history that may come again from the
	// listener. Each can repeat at most once, so it is deleted on match.
	seen map[int]struct{}
}

func newPrinter(out io.Writer, filter models.LogFilter) *printer {
	return &printer{out: out, filter: filter, seen: map[int]struct{}{}}
}

// history prints logs (newest first, as the store returns them) oldest first.
func (p *printer) history(logs []models.Log) {
	fmt.Fprintln(p.out, "=== GoLog - Real-time Log Monitoring ===")
	for _, f := range []struct{ name, value string }{
		{"Level", p.filter.Level}, {"Type", p.filter.Type}, {"Service", p.filter.Service}, {"Text", p.filter.Query},
	} {
		if f.value != "" {
			fmt.Fprintf(p.out, "%s filter: %s\n", f.name, sanitize(f.value))
		}
	}
	if len(p.filter.Attrs) > 0 {
		fmt.Fprintf(p.out, "Attr filter: %s\n", sanitize(attrFlag(p.filter.Attrs).String()))
	}
	fmt.Fprintln(p.out, "======================================")
	if len(logs) == 0 {
		fmt.Fprintln(p.out, "No logs found")
	}
	for i := len(logs) - 1; i >= 0; i-- {
		printLog(p.out, logs[i])
		p.seen[logs[i].ID] = struct{}{}
	}
	fmt.Fprintln(p.out, "======================================")
	fmt.Fprintln(p.out, "Listening for new logs... (Press Ctrl+C to exit)")
}

func (p *printer) handle(ev models.LogEvent) {
	if ev.Resync {
		fmt.Fprintln(p.out, "-- reconnected; logs written while disconnected are not shown --")
		return
	}
	if _, dup := p.seen[ev.Log.ID]; dup {
		delete(p.seen, ev.Log.ID)
		return
	}
	if p.filter.Matches(ev.Log) {
		printLog(p.out, ev.Log)
	}
}

// sanitize replaces control characters (ESC, CR, C1 ...) so a log message
// cannot move the cursor or rewrite the terminal. Newlines and tabs stay.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return '?'
	}, s)
}

func printLog(out io.Writer, logEntry models.Log) {
	timestamp := logEntry.Timestamp.Format("2006-01-02 15:04:05")

	var levelColor string
	switch logEntry.Level {
	case models.LevelInfo:
		levelColor = "\033[32m"
	case models.LevelWarning:
		levelColor = "\033[33m"
	case models.LevelError:
		levelColor = "\033[31m"
	case models.LevelDebug:
		levelColor = "\033[36m"
	default:
		levelColor = "\033[0m"
	}

	service := ""
	if logEntry.Service != "" {
		service = " " + sanitize(logEntry.Service)
	}
	attrs := ""
	if a := compactAttrs(logEntry.Attrs); a != "" {
		attrs = " " + sanitize(a)
	}
	fmt.Fprintf(out, "[%s] %s%s\033[0m [%s]%s: %s%s\n",
		timestamp, levelColor, sanitize(logEntry.Level), sanitize(logEntry.Type), service, sanitize(logEntry.Message), attrs)
}

// compactAttrs returns attrs as one-line JSON, or "" when there are none.
func compactAttrs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(raw)
	}
	if s := b.String(); s != "{}" && s != "null" {
		return s
	}
	return ""
}
