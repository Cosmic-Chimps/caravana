// Command caravana is the Caravana control plane: HTTP API, web UI and deploy engine.
//
// main only wires things together: config, store, the deploy engine and the
// HTTP server. Behavior lives in the internal packages.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/cosmic-chimps/caravana/internal/api"
	"github.com/cosmic-chimps/caravana/internal/config"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/ui"
)

// version is this build's version. Releases set it with
// -ldflags "-X main.version=<version>" (tools/release.sh); a plain go build
// says "dev".
var version = "dev"

// shutdownTimeout is how long in-flight requests get to finish on SIGINT or
// SIGTERM.
const shutdownTimeout = 10 * time.Second

func main() {
	// ctx is cancelled on Ctrl-C or SIGTERM, which starts a clean shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr))
}

// run starts the control plane and blocks until ctx is cancelled or the
// server fails. It returns the process exit code. `caravana admin ...` is the
// host CLI instead (admin.go), and `caravana mcp` the agent's MCP server
// (mcp.go). Taking its inputs as arguments lets tests run
// it in-process.
func run(ctx context.Context, args []string, getenv func(string) string,
	stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "admin" {
		return runAdmin(ctx, args[1:], getenv, stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "mcp" {
		return runMCP(ctx, args[1:], getenv, stdin, stdout, stderr)
	}
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(stdout, version)
		return 0
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil))

	cfg, err := config.Load(args, getenv)
	if err != nil {
		log.Error("invalid configuration", "error", err)
		return 1
	}
	// Never log cfg.NomadToken: it prints as [redacted] anyway.
	log.Info("starting", "version", version, "listen", cfg.Listen, "dataDir", cfg.DataDir, "nomadAddr", cfg.NomadAddr)

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
	svc := service.New(st, deploy.New(nomad, &registry.Client{}, cfg.Settings), deploy.NewVariables(nomad), log)
	svc.SetVolumeBackupTimeout(cfg.VolumeBackupTimeout)
	svc.SetPublicURL(cfg.PublicURL)
	svc.SetNotifyResolveAfter(cfg.NotifyResolveAfter)
	auth := service.NewAuth(st, log, service.AuthOptions{})
	if _, err := auth.AdminEmail(ctx); errors.Is(err, service.ErrNoAdmin) {
		log.Warn("no admin yet: every API route refuses requests until one exists. On this host, run: caravana admin init --email <email>")
	} else if err != nil {
		log.Error("read admin", "error", err)
		return 1
	}
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

	pages, err := ui.New(ui.Deps{Auth: auth, Service: svc, Proxies: cfg.TrustedProxies, Log: log, Version: version})
	if err != nil {
		log.Error("web pages", "error", err)
		return 1
	}
	apiHandler := api.New(api.Deps{Service: svc, Auth: auth, Store: st, Proxies: cfg.TrustedProxies, Log: log, Version: version})
	// streamCtx is the parent of every request's context. shutdownAll cancels
	// it first, which ends the open live streams; Shutdown alone waits for
	// active requests, and a stream never finishes by itself.
	streamCtx, endStreams := context.WithCancel(context.Background())
	defer endStreams()
	servers := []*http.Server{newServer(cfg.Listen, httpx.LogRequests(log, root(apiHandler, pages)), streamCtx)}
	if cfg.WebhookListen != "" {
		// The webhook-only listener: just POST /v1/webhooks/{id}, so it can be
		// exposed while the API above stays private.
		servers = append(servers, newServer(cfg.WebhookListen, api.NewWebhook(svc, log), streamCtx))
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
		shutdownAll(endStreams, servers)
		return 1
	case <-ctx.Done():
	}

	log.Info("shutting down", "timeout", shutdownTimeout.String())
	if err := shutdownAll(endStreams, servers); err != nil {
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

// root sends /v1 to the API and everything else to the pages. It decides on
// the cleaned path, not by a ServeMux: a mux would redirect "//v1/health" to
// "/v1/health", and the API never redirects (it answers those with a JSON 404).
func root(apiHandler, pages http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := path.Clean(r.URL.Path); p == "/v1" || strings.HasPrefix(p, "/v1/") {
			apiHandler.ServeHTTP(w, r)
			return
		}
		pages.ServeHTTP(w, r)
	})
}

// newServer is an HTTP server with Caravana's timeouts. Every request's
// context is derived from base, so cancelling base cancels all of them.
func newServer(addr string, h http.Handler, base context.Context) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		BaseContext:       func(net.Listener) context.Context { return base },
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// shutdownAll stops every server, letting open requests finish within one
// shared timeout. endStreams runs first: it cancels the context the servers'
// requests derive from, so event streams end now instead of holding Shutdown
// until the timeout.
func shutdownAll(endStreams context.CancelFunc, servers []*http.Server) error {
	endStreams()
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var errs []error
	for _, s := range servers {
		errs = append(errs, s.Shutdown(ctx))
	}
	return errors.Join(errs...)
}
