package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"unicode"

	"github.com/lib/pq"
	"github.com/mstgnz/golog/v2/config"
	"github.com/mstgnz/golog/v2/database"
	"github.com/mstgnz/golog/v2/models"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	switch {
	case errors.Is(err, flag.ErrHelp):
		return
	case err != nil:
		fmt.Fprintf(os.Stderr, "golog-cli: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags(args []string, stderr io.Writer) (models.LogFilter, error) {
	fs := flag.NewFlagSet("golog-cli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	level := fs.String("level", "", "Filter logs by level (INFO, WARNING, ERROR, DEBUG)")
	typ := fs.String("type", "", "Filter logs by type (SYSTEM, AUTH, DATABASE, USER, API)")
	if err := fs.Parse(args); err != nil {
		return models.LogFilter{}, err
	}
	if fs.NArg() > 0 {
		return models.LogFilter{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	filter := models.LogFilter{Level: strings.ToUpper(*level), Type: strings.ToUpper(*typ)}
	if err := filter.Validate(); err != nil {
		return models.LogFilter{}, fmt.Errorf("%w (see -h)", err)
	}
	return filter, nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	filter, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	store, err := database.Open(ctx, cfg.DSN())
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer store.Close()

	ctx, cancel := context.WithCancel(ctx) // stops the listener on every return path
	defer cancel()

	// Listen before reading history so nothing written in between is lost;
	// the printer drops the overlap by id.
	events := make(chan models.LogEvent, 64)
	if err := store.ListenForLogs(ctx, events); err != nil {
		return fmt.Errorf("listen for logs: %w", err)
	}
	logs, err := store.GetLogs(ctx, filter)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "42P01" { // undefined_table
			return fmt.Errorf("the logs table does not exist yet; start golog-server once to create it: %w", err)
		}
		return fmt.Errorf("read logs: %w", err)
	}

	p := newPrinter(stdout, filter)
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
	if p.filter.Level != "" {
		fmt.Fprintf(p.out, "Level filter: %s\n", p.filter.Level)
	}
	if p.filter.Type != "" {
		fmt.Fprintf(p.out, "Type filter: %s\n", p.filter.Type)
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
		fmt.Fprintln(p.out, "-- reconnected to the database; logs written while disconnected are not shown --")
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

	fmt.Fprintf(out, "[%s] %s%s\033[0m [%s]: %s\n",
		timestamp, levelColor, sanitize(logEntry.Level), sanitize(logEntry.Type), sanitize(logEntry.Message))
}
