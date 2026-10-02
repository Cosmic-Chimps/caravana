package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// US1: deploy whoami and see it reach healthy.
func TestDeploy(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy() = %v", err)
	}
	if rec.Status != store.StatusRunning || rec.JobVersion == nil || *rec.JobVersion != 0 {
		t.Errorf("record = %s, version %v; want running at version 0", rec.Status, rec.JobVersion)
	}
	if got := rec.Deployment.Digests["web"]; got != apptest.WhoamiDigest {
		t.Errorf("digest = %q, want %q", got, apptest.WhoamiDigest)
	}
	if rec.Warnings == nil || len(rec.Warnings) != 0 {
		t.Errorf("warnings = %#v, want an empty list", rec.Warnings)
	}
	if stored, err := e.svc.GetApp(ctx, "whoami"); err != nil || !reflect.DeepEqual(stored.App, whoami()) {
		t.Errorf("stored app = %+v, %v; want the app as sent", stored.App, err)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy || final.Error != "" {
		t.Errorf("final = %s %q, want healthy with no error", final.Status, final.Error)
	}
	if image := e.nomad.Job("whoami").TaskGroups[0].Tasks[0].Config["image"]; image != apptest.WhoamiDigest {
		t.Errorf("Nomad's image = %v, want the digest", image)
	}
}

// Research R11: the deploy outlives its request.
func TestDeployOutlivesRequest(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	cancel()
	if err != nil {
		t.Fatalf("Deploy() = %v", err)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Errorf("final = %s, want healthy", final.Status)
	}
}

// US2: same payload twice, redeploys, moved tags.
func TestRedeploy(t *testing.T) {
	ctx := context.Background()

	t.Run("same payload twice", func(t *testing.T) {
		e := newEnv(t)
		first, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("first Deploy() = %v", err)
		}
		waitFinal(t, e.store, first.Deployment.ID)
		before, _ := e.svc.GetApp(ctx, "whoami")

		second, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("second Deploy() = %v", err)
		}
		if second.Deployment.ID == first.Deployment.ID || *second.JobVersion != *first.JobVersion+1 {
			t.Errorf("second deploy = %s v%d, want a new record at version %d", second.Deployment.ID, *second.JobVersion, *first.JobVersion+1)
		}
		after, _ := e.svc.GetApp(ctx, "whoami")
		if !reflect.DeepEqual(before, after) {
			t.Errorf("stored app changed: %+v -> %+v (SC-003: updated time included)", before, after)
		}
		if list, _ := e.svc.ListDeployments(ctx, "whoami", 0); len(list) != 2 {
			t.Errorf("records = %d, want 2", len(list))
		}
	})

	t.Run("redeploy the stored app", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual); err != nil {
			t.Fatalf("Deploy() = %v", err)
		}
		rec, err := e.svc.Redeploy(ctx, "whoami", app.TriggerWebhook)
		if err != nil {
			t.Fatalf("Redeploy() = %v", err)
		}
		if rec.Deployment.Trigger != app.TriggerWebhook || *rec.JobVersion != 1 {
			t.Errorf("redeploy = trigger %s v%d, want webhook at version 1", rec.Deployment.Trigger, *rec.JobVersion)
		}
	})

	t.Run("redeploy an unknown app", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Redeploy(ctx, "ghost", app.TriggerWebhook); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Redeploy(ghost) = %v, want store.ErrNotFound", err)
		}
		if e.nomad.Registrations() != 0 {
			t.Errorf("registrations = %d, want 0", e.nomad.Registrations())
		}
	})

	t.Run("moved tag", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual); err != nil {
			t.Fatalf("Deploy() = %v", err)
		}
		e.registry.AddIndex("traefik/whoami", "latest", movedDigest)
		rec, err := e.svc.Redeploy(ctx, "whoami", app.TriggerManual)
		if err != nil {
			t.Fatalf("Redeploy() = %v", err)
		}
		if got := rec.Deployment.Digests["web"]; got != "traefik/whoami@"+movedDigest {
			t.Errorf("digest = %q, want the moved one", got)
		}
		if stored, _ := e.svc.GetApp(ctx, "whoami"); stored.App.Services[0].Image != "traefik/whoami:latest" {
			t.Errorf("stored image = %q, want the tag as written", stored.App.Services[0].Image)
		}
	})

	t.Run("redeploy after a failed deploy fails again", func(t *testing.T) {
		e := newEnv(t)
		a := whoami()
		a.Services[0].Image = "traefik/whoami:nope"
		if _, err := e.svc.Deploy(ctx, a, app.TriggerManual); !errors.Is(err, deploy.ErrImageNotFound) {
			t.Fatalf("Deploy() = %v, want ErrImageNotFound", err)
		}
		_, err := e.svc.Redeploy(ctx, "whoami", app.TriggerWebhook)
		var de *DeployError
		if !errors.As(err, &de) || !errors.Is(err, deploy.ErrImageNotFound) {
			t.Fatalf("Redeploy() = %v, want a *DeployError for the same missing image", err)
		}
		if rec, _ := e.store.GetDeployment(ctx, de.DeploymentID); rec.Status != store.StatusFailed {
			t.Errorf("new record = %s, want failed", rec.Status)
		}
	})
}

// US3: how rollouts end is recorded truthfully.
func TestRolloutOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("reverted", func(t *testing.T) {
		e := newEnv(t)
		first, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy() = %v", err)
		}
		waitFinal(t, e.store, first.Deployment.ID)
		desc := "Failed due to unhealthy allocations - rolling back to job version 0"
		e.nomad.Script("whoami", 1, api.Deployment{Status: api.DeploymentStatusFailed, StatusDescription: desc})

		second, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("second Deploy() = %v", err)
		}
		if final := waitFinal(t, e.store, second.Deployment.ID); final.Status != store.StatusReverted || final.Error != desc {
			t.Errorf("second = %s %q, want reverted with Nomad's description", final.Status, final.Error)
		}
		if rec, _ := e.store.GetDeployment(ctx, first.Deployment.ID); rec.Status != store.StatusHealthy {
			t.Errorf("first = %s, want still healthy", rec.Status)
		}
	})

	t.Run("first deploy fails without a rollback", func(t *testing.T) {
		e := newEnv(t)
		desc := "Failed due to unhealthy allocations - no stable job version to auto revert to"
		e.nomad.Script("whoami", 0, api.Deployment{Status: api.DeploymentStatusFailed, StatusDescription: desc})
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy() = %v", err)
		}
		if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusFailed || final.Error != desc {
			t.Errorf("final = %s %q, want failed with Nomad's description", final.Status, final.Error)
		}
	})
}

// US4: every failure is loud and leaves nothing half-done.
func TestDeployFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("validation saves nothing", func(t *testing.T) {
		for name, mutate := range map[string]func(a *app.App){
			"app rule":             func(a *app.App) { a.Services[0].Health = nil },
			"unparseable image":    func(a *app.App) { a.Services[0].Image = "Traefik/whoami" },
			"unsupported registry": func(a *app.App) { a.Services[0].Image = "quay.io/x/y:1" },
		} {
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				a := whoami()
				mutate(&a)
				_, err := e.svc.Deploy(ctx, a, app.TriggerManual)
				var fe *app.FieldError
				if !errors.As(err, &fe) {
					t.Fatalf("Deploy() = %v, want *app.FieldError", err)
				}
				if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("app was saved (GetApp = %v)", err)
				}
			})
		}
	})

	tests := []struct {
		name    string
		setup   func(e *env, a *app.App)
		want    error
		wantMsg string
	}{
		{"unknown tag", func(e *env, a *app.App) { a.Services[0].Image = "traefik/whoami:nope" }, deploy.ErrImageNotFound, "or private"},
		{"registry down", func(e *env, a *app.App) { e.registry.Fail("traefik/whoami", "latest", http.StatusBadGateway) }, deploy.ErrRegistryUnavailable, "502"},
		{"nomad down", func(e *env, a *app.App) { e.nomad.Down() }, deploy.ErrNomadUnavailable, "unreachable"},
		{"foreign job", func(e *env, a *app.App) {
			e.nomad.AddForeignJob(&api.Job{ID: new("whoami"), Name: new("whoami"),
				Meta: map[string]string{"caravana.app": "whoami", "caravana.deployment": "lab-manual"}})
		}, deploy.ErrJobConflict, "didn't create"},
		{"nomad rejects", func(e *env, a *app.App) { e.nomad.RejectNext(http.StatusBadRequest, "group web: bad driver") }, deploy.ErrNomadRejected, "group web: bad driver"},
		{"second image missing", func(e *env, a *app.App) {
			a.Services = append(a.Services, app.Service{Name: "worker", Image: "ghcr.io/acme/jobs:main"})
		}, deploy.ErrImageNotFound, "ghcr.io"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			a := whoami()
			tc.setup(e, &a)
			jobBefore := e.nomad.Job("whoami")

			rec, err := e.svc.Deploy(ctx, a, app.TriggerManual)
			var de *DeployError
			if !errors.As(err, &de) || !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("Deploy() = %v, want a *DeployError matching %v and mentioning %q", err, tc.want, tc.wantMsg)
			}
			if de.DeploymentID == "" || de.DeploymentID != rec.Deployment.ID || de.AppID != "whoami" {
				t.Errorf("DeployError = %+v, record %s", de, rec.Deployment.ID)
			}
			stored, _ := e.store.GetDeployment(ctx, de.DeploymentID)
			if stored.Status != store.StatusFailed || stored.Error != err.Error() {
				t.Errorf("record = %s %q, want failed with the client's message", stored.Status, stored.Error)
			}
			if _, err := e.svc.GetApp(ctx, "whoami"); err != nil {
				t.Errorf("app not saved: %v (it passed validation)", err)
			}
			if e.nomad.Registrations() != 0 || e.nomad.Job("whoami") != jobBefore {
				t.Errorf("something was registered with Nomad")
			}
		})
	}

	t.Run("second image missing names its field", func(t *testing.T) {
		e := newEnv(t)
		a := whoami()
		a.Services = append(a.Services, app.Service{Name: "worker", Image: "ghcr.io/acme/jobs:main"})
		_, err := e.svc.Deploy(ctx, a, app.TriggerManual)
		var ie *deploy.ImageError
		if !errors.As(err, &ie) || ie.Field != "services[1].image" {
			t.Errorf("Deploy() = %v, want an *ImageError on services[1].image", err)
		}
	})

	t.Run("version can't be read back", func(t *testing.T) {
		e := newEnv(t)
		e.nomad.FailVersions(3) // every try Register makes
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy() = %v, want no error: Nomad accepted the job", err)
		}
		if rec.Status != store.StatusRunning || rec.JobVersion != nil {
			t.Errorf("record = %s v%v, want running without a version", rec.Status, rec.JobVersion)
		}
		final := waitFinal(t, e.store, rec.Deployment.ID)
		if final.Status != store.StatusHealthy || final.JobVersion == nil || *final.JobVersion != 0 {
			t.Errorf("final = %s v%v, want healthy at version 0", final.Status, final.JobVersion)
		}
	})
}
