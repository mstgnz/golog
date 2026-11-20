package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mstgnz/golog/v2/config"
	"github.com/mstgnz/golog/v2/database"
	"github.com/mstgnz/golog/v2/handlers"
	"github.com/mstgnz/golog/v2/web"
)

const (
	shutdownTimeout   = 10 * time.Second
	retentionInterval = time.Hour
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, nil)
	stop()
	if err != nil {
		log.Printf("golog: %v", err)
		os.Exit(1)
	}
}

// run serves until ctx ends. onListen, when set, receives the bound address
// (tests use PORT=0).
func run(ctx context.Context, onListen func(net.Addr)) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	store, err := database.Open(ctx, cfg.DSN())
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer store.Close()

	if cfg.AutoMigrate {
		if err := store.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate database: %w", err)
		}
	}

	if cfg.RetentionDays > 0 {
		keep := time.Duration(cfg.RetentionDays) * 24 * time.Hour
		retentionCtx, stopRetention := context.WithCancel(ctx)
		retentionDone := make(chan struct{})
		go func() {
			defer close(retentionDone)
			store.RunRetention(retentionCtx, keep, retentionInterval)
		}()
		// Runs before store.Close: no retention query may outlive the pool.
		defer func() {
			stopRetention()
			<-retentionDone
		}()
		log.Printf("golog: retention on, deleting logs older than %d days every %s", cfg.RetentionDays, retentionInterval)
	}

	warnIfOpen(cfg)
	app := handlers.NewServer(store, handlers.Options{
		CORSAllowedOrigins: cfg.CORSAllowedOrigins,
		Static:             web.Static(),
		Auth: handlers.Auth{
			WriteToken:    cfg.WriteToken,
			ReadToken:     cfg.ReadToken,
			SecureCookies: cfg.SecureCookies,
		},
	})

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return err
	}
	log.Printf("golog: listening on %s", ln.Addr())
	if onListen != nil {
		onListen(ln.Addr())
	}
	return serve(ctx, ln, app)
}

// warnIfOpen says loudly which side of the API anyone can use.
func warnIfOpen(cfg *config.Config) {
	if cfg.ReadToken == "" {
		log.Println("golog: WARNING: GOLOG_READ_TOKEN is not set; anyone who can reach this server can read all logs")
	}
	if cfg.WriteToken == "" {
		log.Println("golog: WARNING: GOLOG_WRITE_TOKEN is not set; anyone who can reach this server can write logs")
	}
}

// newHTTPServer sets timeouts for ordinary requests. The SSE handler lifts
// WriteTimeout for its own connection.
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

// serve runs the HTTP server on ln until ctx is cancelled, then shuts down.
// The log listener is stopped first: that ends every SSE stream, so Shutdown
// is not left waiting on connections that never go idle.
func serve(ctx context.Context, ln net.Listener, app *handlers.Server) error {
	listenCtx, stopListener := context.WithCancel(context.Background())
	defer stopListener()
	if err := app.StartLogListener(listenCtx); err != nil {
		ln.Close()
		return fmt.Errorf("start log listener: %w", err)
	}

	srv := newHTTPServer(app.SetupRoutes())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	log.Println("golog: shutting down")
	stopListener()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		srv.Close() // drop whatever is still open rather than leave it running
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
