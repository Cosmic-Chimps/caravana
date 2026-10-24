package service

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const (
	whoamiDigest = "sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44"
	movedDigest  = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// env is a service wired to a real store, a fake registry, a fake Nomad and
// an in-memory secret store.
type env struct {
	svc      *Service
	store    *store.Store
	registry *registrytest.Server
	nomad    *deploytest.Nomad
	secrets  *secretstest.Store
}

// newEnv opens a store in a temp dir and starts a service on it. The fake
// registry knows traefik/whoami:latest; the fake Nomad's rollouts succeed.
func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{store: st, registry: registrytest.New(t), nomad: deploytest.New(t), secrets: secretstest.New()}
	e.registry.AddIndex("traefik/whoami", "latest", whoamiDigest)
	e.nomad.Succeed()
	e.svc = startService(t, st, e.registry, e.nomad, e.secrets)
	return e
}

// newDeployer returns a deployer wired to the fakes, with every wait short.
func newDeployer(t *testing.T, reg *registrytest.Server, nomad *deploytest.Nomad) *deploy.Deployer {
	t.Helper()
	return newDeployerAt(t, reg.Endpoints(), nomad)
}

// newDeployerAt is newDeployer with the registry client's endpoints given: a
// registry host (as an image writes it) → the URL of the fake that stands for it.
func newDeployerAt(t *testing.T, endpoints map[string]string, nomad *deploytest.Nomad) *deploy.Deployer {
	t.Helper()
	client, err := deploy.NewNomadClient(nomad.Addr(), "")
	if err != nil {
		t.Fatalf("nomad client: %v", err)
	}
	d := deploy.New(client, &registry.Client{Endpoints: endpoints},
		nomadjob.Settings{Entrypoint: "websecure", DefaultCPUMHz: 100, DefaultMemoryMB: 64, DefaultMemoryMaxMB: 256})
	d.Timing = deploy.Timing{
		ImageTimeout:   5 * time.Second,
		PollGap:        time.Millisecond,
		BackoffStart:   time.Millisecond,
		BackoffMax:     5 * time.Millisecond,
		VersionRetries: []time.Duration{time.Millisecond, time.Millisecond},
		RenderGap:      time.Millisecond,
		RenderWait:     120 * time.Millisecond,
	}
	return d
}

// Set moves the fake clock (defined in auth_test.go) to t. The service gets one
// in startService, for its scheduler and run IDs.
func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// startService builds and starts a service; the test's cleanup stops it and
// waits for its watchers.
func startService(t *testing.T, st *store.Store, reg *registrytest.Server, nomad *deploytest.Nomad, sec secrets.Provider) *Service {
	t.Helper()
	return startServiceWith(t, st, newDeployer(t, reg, nomad), sec)
}

// startServiceWith is startService for a deployer the test built itself.
func startServiceWith(t *testing.T, st *store.Store, d *deploy.Deployer, sec secrets.Provider) *Service {
	t.Helper()
	return startServiceLogging(t, st, d, sec, discardLogger())
}

// startServiceLogging is startServiceWith with the logger the test chose, for
// tests that read what the service logs.
func startServiceLogging(t *testing.T, st *store.Store, d *deploy.Deployer, sec secrets.Provider, log *slog.Logger) *Service {
	t.Helper()
	svc := New(st, d, sec, log)
	svc.retryGap = 5 * time.Millisecond
	svc.watchIdle = 10 * time.Millisecond
	svc.backupPoll = 10 * time.Millisecond
	// Volume runs: short waits, and no scheduler pass after the first, which
	// finds nothing yet. Tests of the scheduler call runDueVolumeBackups.
	svc.clock = &fakeClock{t: time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)}
	svc.scheduleGap = time.Hour
	svc.volume = volumeTiming{limit: time.Minute, grace: 150 * time.Millisecond, poll: 2 * time.Millisecond, comeBack: 150 * time.Millisecond}
	// Schedules: one pass each at Start, which finds nothing; tests of the
	// clock and the watcher call runDueSchedules and followScheduleRuns.
	svc.scheduleScanGap = time.Hour
	svc.scheduleWatchGap = time.Hour
	// Domains: no watcher pass, and checks that never leave the process (a
	// resolver that finds nothing, a dialer that is refused). Tests of the
	// checks give their own fakes with SetEdge.
	svc.domainIdleGap, svc.domainWatchGap = time.Hour, time.Hour
	svc.SetEdge(edgetest.FakeResolver{}, &edgetest.FakeDialer{})

	ctx, cancel := context.WithCancel(context.Background())
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		svc.Wait()
	})
	return svc
}

func whoami() app.App { return apptest.Whoami().App }

// waitFinal polls the store until the deployment has a final status.
func waitFinal(t *testing.T, st *store.Store, id string) store.DeploymentRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, err := st.GetDeployment(context.Background(), id)
		if err != nil {
			t.Fatalf("GetDeployment(%s): %v", id, err)
		}
		switch rec.Status {
		case store.StatusHealthy, store.StatusFailed, store.StatusReverted, store.StatusSuperseded:
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("deployment %s still %s after 5s", id, rec.Status)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitFor polls cond until it's true, for up to 5 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
