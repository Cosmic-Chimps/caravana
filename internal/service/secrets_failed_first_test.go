package service

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// A failed deploy never leaves a service without the values its definition
// names when there is nothing earlier to put back.

// settled waits until the app's kept copy of secrets is gone: the failed
// deployment's Restore has run.
func settled(t *testing.T, e *env, appID string) {
	t.Helper()
	waitFor(t, "the secrets to be settled", func() bool { owner, _ := e.secrets.Kept(appID); return owner == "" })
}

// missingImage is a with a tag the fake registry doesn't have on its first service.
func missingImage(a app.App) app.App {
	a.Services[0].Image = "traefik/whoami:nope"
	return a
}

func TestFirstDeployThatCannotResolveItsImageKeepsItsSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	_, err := e.svc.Deploy(ctx, missingImage(withSecrets(set("DATABASE_URL", marker), set("API_KEY", "k1"))), app.TriggerManual)
	if !errors.Is(err, deploy.ErrImageNotFound) {
		t.Fatalf("Deploy = %v, want ErrImageNotFound", err)
	}
	want := secrets.Sets{"web": {"DATABASE_URL": marker, "API_KEY": "k1"}}
	if got := e.secrets.Live("whoami"); !got.Equal(want) {
		t.Fatalf("live after the failed first deploy = %v, want the values sent", got)
	}
	if owner, _ := e.secrets.Kept("whoami"); owner != "" {
		t.Errorf("kept copy still owned by %q", owner)
	}
	if e.nomad.Registrations() != 0 {
		t.Error("Nomad got a job although the image couldn't be resolved")
	}

	// Deploying again with the image fixed and the secrets as keeps: the values
	// are still there.
	deployHealthy(t, e, withSecrets(keep("DATABASE_URL"), keep("API_KEY")))
	if got := e.secrets.Live("whoami"); !got.Equal(want) {
		t.Errorf("live after the second deploy = %v, want the same values", got)
	}
}

func TestFirstDeployWhoseRolloutFailsKeepsItsSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	desc := "Failed due to unhealthy allocations - no stable job version to auto revert to"
	e.nomad.Script("whoami", 0, api.Deployment{Status: api.DeploymentStatusFailed, StatusDescription: desc})

	rec, err := e.svc.Deploy(ctx, withSecrets(set("DATABASE_URL", marker)), app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy = %v", err)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusFailed {
		t.Fatalf("deployment = %s, want failed", final.Status)
	}
	settled(t, e, "whoami")
	want := secrets.Sets{"web": {"DATABASE_URL": marker}}
	if got := e.secrets.Live("whoami"); !got.Equal(want) {
		t.Fatalf("live after the failed rollout = %v, want the values sent", got)
	}

	// An empty-body redeploy works and keeps the value.
	again, err := e.svc.Redeploy(ctx, "whoami", app.TriggerManual)
	if err != nil {
		t.Fatalf("Redeploy = %v", err)
	}
	if final := waitFinal(t, e.store, again.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("redeploy = %s (%s), want healthy", final.Status, final.Error)
	}
	if got := e.secrets.Live("whoami"); !got.Equal(want) {
		t.Errorf("live after the redeploy = %v", got)
	}
}

// What the running version had still comes back when a later deploy fails.
func TestFailedDeployPutsBackTheValuesOfAServiceThatHadSome(t *testing.T) {
	ctx := context.Background()
	old := secrets.Sets{"web": {"API_KEY": "k1"}}

	t.Run("the image can't be resolved", func(t *testing.T) {
		e := newEnv(t)
		deployHealthy(t, e, withSecrets(set("API_KEY", "k1")))
		if _, err := e.svc.Deploy(ctx, missingImage(withSecrets(set("API_KEY", "k2"))), app.TriggerManual); !errors.Is(err, deploy.ErrImageNotFound) {
			t.Fatalf("Deploy = %v, want ErrImageNotFound", err)
		}
		if got := e.secrets.Live("whoami"); !got.Equal(old) {
			t.Errorf("live = %v, want the previous value", got)
		}
		if owner, _ := e.secrets.Kept("whoami"); owner != "" {
			t.Errorf("kept copy still owned by %q", owner)
		}
	})

	t.Run("the rollout fails", func(t *testing.T) {
		e := newEnv(t)
		deployHealthy(t, e, withSecrets(set("API_KEY", "k1")))
		desc := "Failed due to unhealthy allocations - rolling back to job version 0"
		e.nomad.Script("whoami", 1, api.Deployment{Status: api.DeploymentStatusFailed, StatusDescription: desc})
		rec, err := e.svc.Deploy(ctx, withSecrets(set("API_KEY", "k2")), app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy = %v", err)
		}
		if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusReverted {
			t.Fatalf("deployment = %s, want reverted", final.Status)
		}
		settled(t, e, "whoami")
		if got := e.secrets.Live("whoami"); !got.Equal(old) {
			t.Errorf("live = %v, want the previous value", got)
		}
	})

	t.Run("a new service keeps its values beside it", func(t *testing.T) {
		e := newEnv(t)
		deployHealthy(t, e, withSecrets(set("API_KEY", "k1")))
		a := withSecrets(set("API_KEY", "k2"))
		a.Services = append(a.Services, app.Service{Name: "worker", Image: "traefik/whoami:nope", Secrets: []app.Secret{set("TOKEN", "t1")}})
		if _, err := e.svc.Deploy(ctx, a, app.TriggerManual); !errors.Is(err, deploy.ErrImageNotFound) {
			t.Fatalf("Deploy = %v, want ErrImageNotFound", err)
		}
		want := secrets.Sets{"web": {"API_KEY": "k1"}, "worker": {"TOKEN": "t1"}}
		if got := e.secrets.Live("whoami"); !got.Equal(want) {
			t.Errorf("live = %v, want web's previous value and worker's new one", got)
		}
	})
}

// A deploy that fails after a newer one was requested leaves the secrets to
// that newer deploy: its values must not outlive it.
func TestKeepNewSecretsLeavesThemToANewerDeploy(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := withSecrets(set("API_KEY", "old-deploy"))

	e.svc.markLatest(a.ID, "newer")
	e.svc.keepNewSecrets(ctx, a, "older")
	if got := e.secrets.Live(a.ID); len(got) != 0 {
		t.Errorf("live after a superseded deploy's keep = %v, want nothing", got)
	}

	e.svc.markLatest(a.ID, "older")
	e.svc.keepNewSecrets(ctx, a, "older")
	if got := e.secrets.Live(a.ID); !got.Equal(secrets.Sets{"web": {"API_KEY": "old-deploy"}}) {
		t.Errorf("live after the newest deploy's keep = %v, want its values", got)
	}
}
