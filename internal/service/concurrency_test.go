package service

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// deployCount is how many deployment records the app has.
func deployCount(e *env, appID string) int {
	list, _ := e.store.ListDeployments(context.Background(), appID, 0)
	return len(list)
}

// FR-019: an older deploy never registers after a newer one.
func TestNewerWins(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	release := e.registry.Hold("traefik/whoami", "latest")

	// A takes the app lock and waits on the registry.
	type result struct {
		rec store.DeploymentRecord
		err error
	}
	aDone := make(chan result, 1)
	go func() {
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		aDone <- result{rec, err}
	}()
	waitFor(t, "A to reach the registry", func() bool { return e.registry.Requests() >= 1 })
	list, _ := e.store.ListDeployments(ctx, "whoami", 0)
	aID := list[0].Deployment.ID

	// B is requested while A is held; it blocks on the app lock.
	bDone := make(chan result, 1)
	go func() {
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		bDone <- result{rec, err}
	}()
	waitFor(t, "B to be the latest request", func() bool { return e.svc.latestOf("whoami") != aID })

	release()
	a, b := <-aDone, <-bDone
	if a.err != nil || b.err != nil {
		t.Fatalf("Deploy errors: A %v, B %v", a.err, b.err)
	}
	if a.rec.Status != store.StatusSuperseded || a.rec.JobVersion != nil {
		t.Errorf("A = %s v%v, want superseded and never registered", a.rec.Status, a.rec.JobVersion)
	}
	if e.nomad.Registrations() != 1 || e.nomad.Job("whoami").Meta["caravana.deployment"] != b.rec.Deployment.ID {
		t.Errorf("registrations = %d, job's deployment %q; want only B's (%s)",
			e.nomad.Registrations(), e.nomad.Job("whoami").Meta["caravana.deployment"], b.rec.Deployment.ID)
	}
	if final := waitFinal(t, e.store, b.rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Errorf("B = %s, want healthy", final.Status)
	}
}

// SC-006: of five deploys at once, exactly one goes live; none fail.
func TestFiveAtOnce(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	release := e.registry.Hold("traefik/whoami", "latest")

	var wg sync.WaitGroup
	results := make([]store.DeploymentRecord, 5)
	errs := make([]error, 5)
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		}()
	}
	waitFor(t, "five records", func() bool { return deployCount(e, "whoami") == 5 })
	time.Sleep(20 * time.Millisecond) // let the last one mark itself latest
	release()
	wg.Wait()

	latest := e.svc.latestOf("whoami")
	var live []string
	for i, rec := range results {
		if errs[i] != nil {
			t.Fatalf("deploy %d: %v", i, errs[i])
		}
		final := waitFinal(t, e.store, rec.Deployment.ID)
		switch final.Status {
		case store.StatusSuperseded:
		case store.StatusHealthy:
			live = append(live, rec.Deployment.ID)
		default:
			t.Errorf("deploy %d ended %s (%q), want healthy or superseded", i, final.Status, final.Error)
		}
	}
	if !slices.Equal(live, []string{latest}) {
		t.Errorf("live = %v, want exactly the latest request %s", live, latest)
	}
	if e.nomad.Registrations() != 1 || e.nomad.Job("whoami").Meta["caravana.deployment"] != latest {
		t.Errorf("registrations = %d, want 1 carrying %s", e.nomad.Registrations(), latest)
	}
}

// FR-020: deploys of different apps don't wait on each other.
func TestDifferentAppsDontWait(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	release := e.registry.Hold("traefik/whoami", "latest")
	defer release()
	e.registry.AddIndex("traefik/other", "latest", movedDigest)

	go func() { _, _ = e.svc.Deploy(ctx, whoami(), app.TriggerManual) }()
	waitFor(t, "app whoami to reach the registry", func() bool { return e.registry.Requests() >= 1 })

	other := whoami()
	other.ID, other.Name = "other", "other"
	other.Services[0].Image = "traefik/other:latest"
	other.Services[0].Domains[0].Host = "other.localhost"

	done := make(chan error, 1)
	go func() {
		_, err := e.svc.Deploy(ctx, other, app.TriggerManual)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Deploy(other) = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deploying app other waited for app whoami")
	}
}

// Research R11: waiting for the lock doesn't use up the next deploy's budget.
func TestBudgetStartsAfterLock(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.svc.budget = 200 * time.Millisecond
	release := e.registry.Hold("traefik/whoami", "latest")

	aDone := make(chan error, 1)
	go func() { _, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual); aDone <- err }()
	waitFor(t, "A to reach the registry", func() bool { return e.registry.Requests() >= 1 })
	list, _ := e.store.ListDeployments(ctx, "whoami", 0)
	aID := list[0].Deployment.ID

	bDone := make(chan store.DeploymentRecord, 1)
	go func() {
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Errorf("B: %v", err)
		}
		bDone <- rec
	}()
	waitFor(t, "B to be the latest request", func() bool { return e.svc.latestOf("whoami") != aID })

	time.Sleep(300 * time.Millisecond) // longer than the budget: A times out, B waits
	release()
	<-aDone
	b := <-bDone
	if final := waitFinal(t, e.store, b.Deployment.ID); final.Status != store.StatusHealthy {
		t.Errorf("B = %s (%q), want healthy: its budget starts once it holds the lock", final.Status, final.Error)
	}
}
