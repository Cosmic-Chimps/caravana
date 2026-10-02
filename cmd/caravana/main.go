// Command caravana is the Caravana control plane: HTTP API, web UI and deploy engine.
//
// main only wires things together: config, store, the deploy engine and the
// HTTP server. Behavior lives in the internal packages.
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
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/service"
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
	// Never log cfg.NomadToken: it prints as [redacted] anyway.
	log.Info("starting", "listen", cfg.Listen, "dataDir", cfg.DataDir, "nomadAddr", cfg.NomadAddr)
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

	nomad, err := deploy.NewNomadClient(cfg.NomadAddr, cfg.NomadToken.Reveal())
	if err != nil {
		log.Error("nomad client", "error", err)
		return 1
	}
	svc := service.New(st, deploy.New(nomad, &registry.Client{}, cfg.Settings), log)
	// Rollout watchers live until bgCtx is cancelled. Deferred calls run
	// last-in, first-out, so on the way out the HTTP server has already shut
	// down, then the watchers stop and are waited for, then the store closes.
	bgCtx, stopWatchers := context.WithCancel(context.Background())
	defer func() {
		stopWatchers()
		svc.Wait()
	}()
	if err := svc.Start(bgCtx); err != nil {
		log.Error("start deploy service", "error", err)
		return 1
	}

	servers := []*http.Server{newServer(cfg.Listen, api.New(svc, st, log))}
	if cfg.WebhookListen != "" {
		// The webhook-only listener: just POST /v1/webhooks/{id}, so it can be
		// exposed while the API above stays private.
		servers = append(servers, newServer(cfg.WebhookListen, api.NewWebhook(svc, log)))
	}

	// ListenAndServe blocks, so each server runs in its own goroutine and
	// reports how it ended on a shared channel.
	serveErr := make(chan error, len(servers))
	for _, s := range servers {
		go func() { serveErr <- s.ListenAndServe() }()
		log.Info("listening", "addr", s.Addr)
	}

	select {
	case err := <-serveErr:
		log.Error("server stopped", "error", err)
		shutdownAll(servers)
		return 1
	case <-ctx.Done():
	}

	log.Info("shutting down", "timeout", shutdownTimeout.String())
	if err := shutdownAll(servers); err != nil {
		log.Error("shutdown did not finish in time; open requests were cut off", "error", err)
		return 1
	}
	for range servers {
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			log.Error("server stopped", "error", err)
			return 1
		}
	}
	log.Info("stopped")
	return 0
}

// newServer is an HTTP server with Caravana's timeouts.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// shutdownAll stops every server, letting open requests finish within one
// shared timeout.
func shutdownAll(servers []*http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var errs []error
	for _, s := range servers {
		errs = append(errs, s.Shutdown(ctx))
	}
	return errors.Join(errs...)
}
