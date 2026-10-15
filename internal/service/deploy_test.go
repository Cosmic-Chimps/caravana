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
	"github.com/cosmic-chimps/caravana/internal/compose"
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

// Feature 011: every deployment records what it deployed, secret values
// left out.
func TestDeployRecordsDefinition(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := whoami()
	a.Services[0].Secrets = []app.Secret{set("TOKEN", marker)}

	rec, err := e.svc.Deploy(ctx, a, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy() = %v", err)
	}
	want := a.WithoutSecretValues()
	if !reflect.DeepEqual(rec.Definition, &want) || rec.Source != "" {
		t.Errorf("record: Definition = %+v, Source = %q; want the app without values and no source", rec.Definition, rec.Source)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
}

// Feature 011: a pinned deploy uses its digests as they are, even when the
// tag has moved since, and names its source.
func TestDeployPinned(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.registry.AddIndex("traefik/whoami", "latest", movedDigest) // the tag moved

	pin := &pinned{digests: map[string]string{"web": apptest.WhoamiDigest}, source: "dep-past"}
	rec, err := e.svc.saveAndDeploy(ctx, whoami(), Condition{}, app.TriggerManual, made{}, pin)
	if err != nil {
		t.Fatalf("saveAndDeploy() = %v", err)
	}
	if got := rec.Deployment.Digests["web"]; got != apptest.WhoamiDigest {
		t.Errorf("digest = %q, want the pinned %q", got, apptest.WhoamiDigest)
	}
	if rec.Source != "dep-past" {
		t.Errorf("Source = %q, want dep-past", rec.Source)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Errorf("final = %s, want healthy", final.Status)
	}
	if image := e.nomad.Job("whoami").TaskGroups[0].Tasks[0].Config["image"]; image != apptest.WhoamiDigest {
		t.Errorf("Nomad's image = %v, want the pinned digest", image)
	}
}

// Feature 011: a deploy that drops a secret says its value is deleted.
func TestDeployWarnsRemovedSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := whoami()
	a.Services[0].Secrets = []app.Secret{set("TOKEN", "one"), set("KEEP", "two")}
	first, err := e.svc.Deploy(ctx, a, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy() = %v", err)
	}
	waitFinal(t, e.store, first.Deployment.ID)

	a.Services[0].Secrets = []app.Secret{{Name: "KEEP"}}
	rec, err := e.svc.Deploy(ctx, a, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy() = %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if len(rec.Warnings) != 1 || !strings.Contains(rec.Warnings[0].Message, `secret "TOKEN"`) {
		t.Errorf("warnings = %+v, want one naming TOKEN", rec.Warnings)
	}
}

// Feature 014: a deploy can be conditional on the stored app.
func TestDeployIf(t *testing.T) {
	ctx := context.Background()

	// storedApp deploys whoami and returns its revision.
	storedApp := func(t *testing.T, e *env) string {
		t.Helper()
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy() = %v", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		stored, err := e.svc.GetApp(ctx, "whoami")
		if err != nil {
			t.Fatalf("GetApp() = %v", err)
		}
		return stored.Revision
	}
	edited := func() app.App {
		a := whoami()
		a.Services[0].Replicas = new(5)
		return a
	}

	t.Run("matching revision deploys the edit", func(t *testing.T) {
		e := newEnv(t)
		rev := storedApp(t, e)
		rec, err := e.svc.DeployIf(ctx, edited(), Condition{IfRevision: rev}, app.TriggerManual)
		if err != nil {
			t.Fatalf("DeployIf() = %v", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		if stored, _ := e.svc.GetApp(ctx, "whoami"); !reflect.DeepEqual(stored.App, edited()) {
			t.Errorf("stored app = %+v, want the edit", stored.App)
		}
		if e.nomad.Registrations() != 2 {
			t.Errorf("registrations = %d, want 2", e.nomad.Registrations())
		}
	})

	t.Run("stale revision saves and deploys nothing", func(t *testing.T) {
		e := newEnv(t)
		storedApp(t, e)
		before, _ := e.svc.GetApp(ctx, "whoami")
		_, err := e.svc.DeployIf(ctx, edited(), Condition{IfRevision: "0000000000000000"}, app.TriggerManual)
		if !errors.Is(err, ErrAppChanged) {
			t.Fatalf("DeployIf() = %v, want ErrAppChanged", err)
		}
		if !strings.Contains(err.Error(), before.Revision) {
			t.Errorf("error %q doesn't name the current revision %s", err, before.Revision)
		}
		after, _ := e.svc.GetApp(ctx, "whoami")
		if !reflect.DeepEqual(after, before) {
			t.Errorf("a refused deploy changed the stored app: %+v", after.App)
		}
		if e.nomad.Registrations() != 1 {
			t.Errorf("registrations = %d, want only the first deploy's", e.nomad.Registrations())
		}
		if recs, _ := e.store.ListDeployments(ctx, "whoami", 10); len(recs) != 1 {
			t.Errorf("%d deployment records, want 1", len(recs))
		}
	})

	t.Run("revision on an unknown app", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.svc.DeployIf(ctx, whoami(), Condition{IfRevision: "0000000000000000"}, app.TriggerManual)
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeployIf() = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("IfNew creates a free ID", func(t *testing.T) {
		e := newEnv(t)
		rec, err := e.svc.DeployIf(ctx, whoami(), Condition{IfNew: true}, app.TriggerManual)
		if err != nil {
			t.Fatalf("DeployIf() = %v", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		if _, err := e.svc.GetApp(ctx, "whoami"); err != nil {
			t.Errorf("GetApp() = %v, want the app saved", err)
		}
	})

	t.Run("IfNew refuses a taken ID", func(t *testing.T) {
		e := newEnv(t)
		storedApp(t, e)
		before, _ := e.svc.GetApp(ctx, "whoami")
		_, err := e.svc.DeployIf(ctx, edited(), Condition{IfNew: true}, app.TriggerManual)
		if !errors.Is(err, ErrAppExists) {
			t.Fatalf("DeployIf() = %v, want ErrAppExists", err)
		}
		after, _ := e.svc.GetApp(ctx, "whoami")
		if !reflect.DeepEqual(after, before) || e.nomad.Registrations() != 1 {
			t.Errorf("a refused create changed something: app %+v, %d registrations", after.App, e.nomad.Registrations())
		}
	})

	t.Run("both conditions is an error", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.svc.DeployIf(ctx, whoami(), Condition{IfNew: true, IfRevision: "abc"}, app.TriggerManual)
		if err == nil {
			t.Fatal("DeployIf() with both conditions succeeded")
		}
		if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp() = %v, want nothing saved", err)
		}
	})

	t.Run("validation and secret checks come before any save", func(t *testing.T) {
		for name, cond := range map[string]func(rev string) Condition{
			"IfNew":      func(string) Condition { return Condition{IfNew: true} },
			"IfRevision": func(rev string) Condition { return Condition{IfRevision: rev} },
		} {
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				id := "other"
				rev := ""
				if name == "IfRevision" {
					id, rev = "whoami", storedApp(t, e)
				}
				replaces := e.secrets.Replaces()

				// A secret with no value anywhere is refused naming it.
				a := whoami()
				a.ID = id
				a.Services[0].Secrets = []app.Secret{{Name: "TOKEN"}}
				var fe *app.FieldError
				if _, err := e.svc.DeployIf(ctx, a, cond(rev), app.TriggerManual); !errors.As(err, &fe) {
					t.Fatalf("DeployIf() with a valueless secret = %v, want *app.FieldError", err)
				}
				// An invalid app is refused too.
				bad := whoami()
				bad.ID = id
				bad.Services[0].Image = ""
				if _, err := e.svc.DeployIf(ctx, bad, cond(rev), app.TriggerManual); !errors.As(err, &fe) {
					t.Fatalf("DeployIf() with no image = %v, want *app.FieldError", err)
				}

				if e.secrets.Replaces() != replaces {
					t.Error("the secret store was written")
				}
				if name == "IfNew" {
					if _, err := e.svc.GetApp(ctx, id); !errors.Is(err, store.ErrNotFound) {
						t.Errorf("GetApp() = %v, want nothing saved", err)
					}
				} else if stored, _ := e.svc.GetApp(ctx, id); stored.Revision != rev {
					t.Errorf("the stored app moved to revision %s", stored.Revision)
				}
			})
		}
	})
}

func TestRemovedSecrets(t *testing.T) {
	svc := func(name string, secrets ...string) app.Service {
		s := app.Service{Name: name}
		for _, n := range secrets {
			s.Secrets = append(s.Secrets, app.Secret{Name: n})
		}
		return s
	}
	tests := []struct {
		name      string
		old, next []app.Service
		want      []string
	}{
		{"same", []app.Service{svc("web", "A")}, []app.Service{svc("web", "A")}, nil},
		{"one dropped", []app.Service{svc("web", "A", "B")}, []app.Service{svc("web", "B")}, []string{`"web"`, `"A"`}},
		{"service gone", []app.Service{svc("web"), svc("db", "P")}, []app.Service{svc("web")}, []string{`"db"`, `"P"`}},
		{"moved to another service", []app.Service{svc("web", "A")}, []app.Service{svc("api", "A")}, []string{`"web"`, `"A"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemovedSecrets(app.App{Services: tt.old}, app.App{Services: tt.next})
			if len(tt.want) == 0 {
				if len(got) != 0 {
					t.Errorf("got %+v, want none", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %+v, want one warning", got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got[0].Message, w) {
					t.Errorf("%q doesn't name %s", got[0].Message, w)
				}
			}
		})
	}
}

// droppedFile counts the "Compose file was dropped" warnings of a record.
func droppedFile(rec store.DeploymentRecord) int {
	n := 0
	for _, w := range rec.Warnings {
		if w.Message == droppedFileWarning && w.Field == "" {
			n++
		}
	}
	return n
}

// Feature 015: a deploy from a definition replaces an app that kept its
// Compose file, so the file is dropped and the warning says so, once.
func TestDeployDefinitionDropsKeptFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	first, err := e.svc.DeployCompose(ctx, "blog", compose.Request{Compose: twoServices}, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	waitFinal(t, e.store, first.Deployment.ID)
	stored, err := e.svc.GetApp(ctx, "blog")
	if err != nil || stored.Compose == nil {
		t.Fatalf("GetApp() = %+v, %v; want the app to keep its file", stored, err)
	}

	rec, err := e.svc.Deploy(ctx, stored.App, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if n := droppedFile(rec); n != 1 {
		t.Errorf("warnings = %+v, want the drop warning once", rec.Warnings)
	}
	after, _ := e.svc.GetApp(ctx, "blog")
	if after.Compose != nil {
		t.Errorf("kept file after a definition deploy = %+v, want none", after.Compose)
	}
	if dep, _ := e.svc.GetDeployment(ctx, "blog", rec.Deployment.ID); dep.Compose != nil {
		t.Errorf("the deployment records a file: %+v", dep.Compose)
	}

	// The next definition deploy has no file left to drop.
	again, err := e.svc.Deploy(ctx, after.App, app.TriggerManual)
	if err != nil {
		t.Fatalf("second Deploy: %v", err)
	}
	waitFinal(t, e.store, again.Deployment.ID)
	if n := droppedFile(again); n != 0 {
		t.Errorf("second deploy warnings = %+v, want no drop warning", again.Warnings)
	}
}

// Feature 015: an app that never had a file gets no drop warning.
func TestDeployWithoutFileNoDropWarning(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	for range 2 {
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		if n := droppedFile(rec); n != 0 {
			t.Errorf("warnings = %+v, want no drop warning", rec.Warnings)
		}
	}
}

// Feature 015: a redeploy of the stored app changes no file. The app keeps
// the one it has, the new deployment records it, and no warning says it was
// dropped.
func TestRedeployKeepsFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	req := compose.Request{Compose: keptFile, Services: map[string]compose.Overlay{
		"web": {Secrets: []app.Secret{{Name: "TOKEN", Value: new(keptSentinel)}}},
	}}
	composeHealthy(t, e, req)
	want := req.WithoutSecretValues()
	before, _ := e.svc.GetApp(ctx, "whoami")

	rec, err := e.svc.Redeploy(ctx, "whoami", app.TriggerManual)
	if err != nil {
		t.Fatalf("Redeploy() = %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)

	if n := droppedFile(rec); n != 0 {
		t.Errorf("warnings = %+v, want no drop warning", rec.Warnings)
	}
	dep, _ := e.svc.GetDeployment(ctx, "whoami", rec.Deployment.ID)
	if !reflect.DeepEqual(dep.Compose, &want) {
		t.Errorf("the redeploy records file %+v, want %+v", dep.Compose, want)
	}
	after, _ := e.svc.GetApp(ctx, "whoami")
	if !reflect.DeepEqual(after.Compose, &want) || after.Revision != before.Revision {
		t.Errorf("after the redeploy: file %+v, revision %s (was %s); want both unchanged", after.Compose, after.Revision, before.Revision)
	}
}
