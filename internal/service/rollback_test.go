package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// recordDeployment stores a deployment of def that ended in status, without
// deploying anything.
func recordDeployment(t *testing.T, e *env, id string, def *app.App, status store.Status) {
	t.Helper()
	ctx := context.Background()
	d := app.Deployment{ID: id, Trigger: app.TriggerManual, Digests: map[string]string{"web": apptest.WhoamiDigest}}
	if _, err := e.store.CreateDeployment(ctx, "whoami", d, def, nil, ""); err != nil {
		t.Fatal(err)
	}
	steps := []store.Status{store.StatusRunning, status}
	if status == store.StatusFailed {
		steps = []store.Status{status}
	}
	for _, st := range steps {
		if _, err := e.store.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: st}); err != nil {
			t.Fatal(err)
		}
	}
}

func countDeployments(t *testing.T, e *env) int {
	t.Helper()
	recs, err := e.store.ListDeployments(context.Background(), "whoami", 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(recs)
}

func withEnv(a app.App, value string) app.App {
	a.Services[0].Env = []app.EnvVar{{Name: "MODE", Value: value}}
	return a
}

// US1: going back to version A brings back A's image digest and definition,
// whatever the tag points at now.
func TestRedeployVersion(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := deployHealthy(t, e, withEnv(whoami(), "a"))
	e.registry.AddIndex("traefik/whoami", "latest", movedDigest)
	deployHealthy(t, e, withEnv(whoami(), "b"))

	rec, err := e.svc.RedeployVersion(ctx, "whoami", a.Deployment.ID, app.TriggerConsole)
	if err != nil {
		t.Fatalf("RedeployVersion() = %v", err)
	}
	if rec.Source != a.Deployment.ID || rec.Deployment.Trigger != app.TriggerConsole {
		t.Errorf("record: source %q, trigger %s; want source %s from the console", rec.Source, rec.Deployment.Trigger, a.Deployment.ID)
	}
	if got := rec.Deployment.Digests["web"]; got != apptest.WhoamiDigest {
		t.Errorf("digest = %q, want A's %q", got, apptest.WhoamiDigest)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Errorf("final = %s, want healthy", final.Status)
	}
	task := e.nomad.Job("whoami").TaskGroups[0].Tasks[0]
	if task.Config["image"] != apptest.WhoamiDigest || task.Env["MODE"] != "a" {
		t.Errorf("Nomad runs %v with MODE=%q, want A's digest and MODE=a", task.Config["image"], task.Env["MODE"])
	}
	stored, err := e.svc.GetApp(ctx, "whoami")
	if err != nil || !reflect.DeepEqual(stored.App, withEnv(whoami(), "a")) {
		t.Errorf("stored app = %+v, %v; want A's definition", stored.App, err)
	}
}

// US1: every refusal happens before anything changes.
func TestRedeployVersionRefused(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name  string
		setup func(t *testing.T, e *env) (appID, deploymentID string)
		check func(t *testing.T, err error)
	}{
		{"not healthy", func(t *testing.T, e *env) (string, string) {
			recordDeployment(t, e, "failed-one", new(whoami()), store.StatusFailed)
			return "whoami", "failed-one"
		}, func(t *testing.T, err error) {
			if !errors.Is(err, ErrDeploymentNotHealthy) {
				t.Errorf("err = %v, want ErrDeploymentNotHealthy", err)
			}
		}},
		{"no definition recorded", func(t *testing.T, e *env) (string, string) {
			recordDeployment(t, e, "old", nil, store.StatusHealthy)
			return "whoami", "old"
		}, func(t *testing.T, err error) {
			if !errors.Is(err, ErrDefinitionNotRecorded) {
				t.Errorf("err = %v, want ErrDefinitionNotRecorded", err)
			}
		}},
		{"another app's deployment", func(t *testing.T, e *env) (string, string) {
			recordDeployment(t, e, "theirs", new(whoami()), store.StatusHealthy)
			other := whoami()
			other.ID = "other"
			if _, err := e.store.SaveApp(ctx, other, nil); err != nil {
				t.Fatal(err)
			}
			return "other", "theirs"
		}, func(t *testing.T, err error) {
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("err = %v, want store.ErrNotFound", err)
			}
		}},
		{"image gone from the registry", func(t *testing.T, e *env) (string, string) {
			recordDeployment(t, e, "gone", new(whoami()), store.StatusHealthy)
			e.registry.Delete("traefik/whoami", whoamiDigest)
			return "whoami", "gone"
		}, func(t *testing.T, err error) {
			var ie *deploy.ImageError
			if !errors.As(err, &ie) || ie.Field != "services[0].image" || !errors.Is(err, deploy.ErrImageNotFound) {
				t.Errorf("err = %v, want image_not_found on services[0].image", err)
			}
		}},
		{"a secret it uses has no value now", func(t *testing.T, e *env) (string, string) {
			def := whoami()
			def.Services[0].Secrets = []app.Secret{{Name: "TOKEN"}}
			recordDeployment(t, e, "with-secret", &def, store.StatusHealthy)
			return "whoami", "with-secret"
		}, func(t *testing.T, err error) {
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != "services[0].secrets[0].value" {
				t.Errorf("err = %v, want a field error on services[0].secrets[0].value", err)
			}
		}},
		{"fails today's rules", func(t *testing.T, e *env) (string, string) {
			def := whoami()
			def.Services[0].Env = []app.EnvVar{{Name: "BAD=NAME", Value: "x"}}
			recordDeployment(t, e, "bad", &def, store.StatusHealthy)
			return "whoami", "bad"
		}, func(t *testing.T, err error) {
			var fe *app.FieldError
			if !errors.As(err, &fe) {
				t.Errorf("err = %v, want a field error", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			deployHealthy(t, e, withEnv(whoami(), "live"))
			appID, id := tt.setup(t, e)
			before := countDeployments(t, e)

			_, err := e.svc.RedeployVersion(ctx, appID, id, app.TriggerManual)
			tt.check(t, err)

			if after := countDeployments(t, e); after != before {
				t.Errorf("deployments: %d before, %d after; a refused redeploy records none", before, after)
			}
			stored, err := e.svc.GetApp(ctx, "whoami")
			if err != nil || !reflect.DeepEqual(stored.App, withEnv(whoami(), "live")) {
				t.Errorf("stored app changed: %+v, %v", stored.App, err)
			}
			if task := e.nomad.Job("whoami").TaskGroups[0].Tasks[0]; task.Env["MODE"] != "live" {
				t.Errorf("Nomad's job changed: MODE=%q", task.Env["MODE"])
			}
		})
	}
}
