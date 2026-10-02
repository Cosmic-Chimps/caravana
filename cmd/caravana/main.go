// Command caravana is the Caravana control plane: HTTP API, web UI and deploy engine.
//
// main only wires things together: config, store, HTTP server. Behavior lives
// in the internal packages.
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cosmic-chimps/caravana/internal/api"
	"github.com/cosmic-chimps/caravana/internal/config"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// shutdownTimeout is how long in-flight requests get to finish on SIGINT or
// SIGTERM.
const shutdownTimeout = 10 * time.Second

func main() {
	// ctx is cancelled on Ctrl-C or SIGTERM, which starts a clean shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stderr))
}

// run starts the control plane and blocks until ctx is cancelled or the
// server fails. It returns the process exit code. Taking its inputs as
// arguments lets tests run it in-process.
func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))

	cfg, err := config.Load(args, getenv)
	if err != nil {
		log.Error("invalid configuration", "error", err)
		return 1
	}
	log.Info("starting", "listen", cfg.Listen, "dataDir", cfg.DataDir)
	if !cfg.Loopback() {
		log.Warn("listening on a non-loopback address; the API has no authentication yet", "listen", cfg.Listen)
	}

	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		log.Error("open store", "error", err)
		return 1
	}
	defer st.Close()
	log.Info("store ready", "dataDir", cfg.DataDir, "migrationsApplied", st.MigrationsApplied())

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(st, log),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// ListenAndServe blocks, so it runs in its own goroutine and reports how
	// it ended on a channel.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Listen)

	select {
	case err := <-serveErr:
		log.Error("server stopped", "error", err)
		return 1
	case <-ctx.Done():
	}

	log.Info("shutting down", "timeout", shutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown did not finish in time; open requests were cut off", "error", err)
		return 1
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		log.Error("server stopped", "error", err)
		return 1
	}
	log.Info("stopped")
	return 0
}
