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

const shutdownTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx)
	stop()
	if err != nil {
		log.Printf("golog: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
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

	app := handlers.NewServer(store, handlers.Options{
		CORSAllowedOrigins: cfg.CORSAllowedOrigins,
		Static:             web.Static(),
	})

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return err
	}
	log.Printf("golog: listening on %s", ln.Addr())
	return serve(ctx, ln, app)
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
