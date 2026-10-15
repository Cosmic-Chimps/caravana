package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// twoServices is a Compose file the fake registry can resolve: web (one
// port) and worker, with a warning (a published port).
const twoServices = `services:
  web:
    image: traefik/whoami:latest
    ports: ["8080:80"]
  worker:
    image: traefik/whoami:latest
    command: ["--port", "9000"]
`

// US1: a Compose file deploys like any app, and its warnings are in the
// deployment record.
func TestDeployCompose(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	req := compose.Request{Compose: twoServices, Services: map[string]compose.Overlay{
		"web": {Domains: []compose.Domain{{Host: "blog.example.com"}}},
	}}
	rec, err := e.svc.DeployCompose(ctx, "blog", req, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("deploy = %s (%s)", final.Status, final.Error)
	}
	if len(rec.Warnings) == 0 || rec.Warnings[0].Field != "services.web.ports[0]" {
		t.Errorf("warnings = %+v, want the published port's first", rec.Warnings)
	}
	stored, err := e.svc.GetApp(ctx, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if stored.App.Source.Kind != app.SourceCompose || len(stored.App.Services) != 2 || stored.App.Services[0].Domains[0].Host != "blog.example.com" {
		t.Errorf("stored app = %+v", stored.App)
	}
	if job := e.nomad.Job("blog"); job == nil || len(job.TaskGroups) != 2 || len(job.TaskGroups[0].Tasks) != 2 {
		t.Errorf("the job lacks the two groups with their names sidecar: %+v", job)
	}

	// A redeploy deploys the stored app: no file needed.
	again, err := e.svc.Redeploy(ctx, "blog", app.TriggerWebhook)
	if err != nil {
		t.Fatalf("Redeploy: %v", err)
	}
	if final := waitFinal(t, e.store, again.Deployment.ID); final.Status != store.StatusHealthy {
		t.Errorf("redeploy = %s", final.Status)
	}
}

// keptSentinel is a secret value that must never be kept with a Compose file.
const keptSentinel = "s3cret-sentinel-015"

// keptFile is a Compose file with a comment, blank lines and a trailing
// newline: what the store must give back byte for byte.
const keptFile = "# the blog\nservices:\n\n  web:\n    image: traefik/whoami:latest   # pinned later\n"

// Feature 015: a Compose deploy keeps the file, as typed, with the app and
// with the deployment, and never a secret value.
func TestDeployComposeKeepsFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	req := compose.Request{Compose: keptFile, Name: "Blog", Services: map[string]compose.Overlay{
		"web": {Secrets: []app.Secret{{Name: "TOKEN", Value: new(keptSentinel)}}},
	}}
	rec, err := e.svc.DeployCompose(ctx, "blog", req, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)

	want := req.WithoutSecretValues()
	stored, err := e.svc.GetApp(ctx, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.Compose, &want) {
		t.Errorf("app's kept file = %+v, want %+v", stored.Compose, want)
	}
	dep, err := e.svc.GetDeployment(ctx, "blog", rec.Deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dep.Compose, &want) {
		t.Errorf("deployment's kept file = %+v, want %+v", dep.Compose, want)
	}
	if dep.Compose.Compose != keptFile {
		t.Errorf("file text = %q, want it byte for byte", dep.Compose.Compose)
	}

	// The value went to the secret store, and nowhere the file is kept.
	if got := e.secrets.Live("blog"); got["web"]["TOKEN"] != keptSentinel {
		t.Errorf("secret store = %v, want the value deployed", got)
	}
	doc, _ := json.Marshal([]any{stored, dep})
	if strings.Contains(string(doc), keptSentinel) {
		t.Error("a secret value was kept with the Compose file")
	}

	// A second Compose deploy replaces the file.
	next := compose.Request{Compose: "services:\n  web:\n    image: traefik/whoami:latest\n"}
	again, err := e.svc.DeployCompose(ctx, "blog", next, app.TriggerManual)
	if err != nil {
		t.Fatalf("second DeployCompose: %v", err)
	}
	waitFinal(t, e.store, again.Deployment.ID)
	stored, _ = e.svc.GetApp(ctx, "blog")
	if stored.Compose == nil || stored.Compose.Compose != next.Compose {
		t.Errorf("app's kept file after the second deploy = %+v, want the new file", stored.Compose)
	}
}

// Feature 015: a comment-only edit is a change of the app's kept file, so a
// deploy that read the app before it is refused as stale.
func TestDeployIfStaleAfterCommentEdit(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	first := compose.Request{Compose: "services:\n  web:\n    image: traefik/whoami:latest\n"}
	rec, err := e.svc.DeployCompose(ctx, "blog", first, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	before, _ := e.svc.GetApp(ctx, "blog")

	edited := compose.Request{Compose: "# a comment\n" + first.Compose}
	rec, err = e.svc.DeployComposeIf(ctx, "blog", edited, Condition{IfRevision: before.Revision}, app.TriggerManual)
	if err != nil {
		t.Fatalf("comment-only edit: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	after, _ := e.svc.GetApp(ctx, "blog")
	if after.Revision == before.Revision {
		t.Fatal("a comment-only edit left the revision as it was")
	}

	// The second tab still holds the old revision.
	_, err = e.svc.DeployIf(ctx, before.App, Condition{IfRevision: before.Revision}, app.TriggerManual)
	if !errors.Is(err, ErrAppChanged) {
		t.Fatalf("DeployIf with the stale revision = %v, want ErrAppChanged", err)
	}
}

// Feature 015, US2: an app made from Compose before the file was kept takes
// the file with its next Compose deploy, and that deploy adds no drop warning.
func TestDeployComposeOnAppWithoutFileKeepsIt(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	first, err := e.svc.DeployCompose(ctx, "blog", compose.Request{Compose: twoServices}, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	waitFinal(t, e.store, first.Deployment.ID)

	// Make it what a 014 app is: a stored Compose app with no file.
	stored, err := e.svc.GetApp(ctx, "blog")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := e.store.SaveApp(ctx, stored.App, nil)
	if err != nil || legacy.Compose != nil {
		t.Fatalf("SaveApp(nil file) = %+v, %v; want an app with no file", legacy, err)
	}

	req := compose.Request{Compose: keptFile}
	rec, err := e.svc.DeployComposeIf(ctx, "blog", req, Condition{IfRevision: legacy.Revision}, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployComposeIf: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	after, err := e.svc.GetApp(ctx, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if after.Compose == nil || after.Compose.Compose != keptFile {
		t.Errorf("kept file after the deploy = %+v, want the pasted file byte for byte", after.Compose)
	}
	if n := droppedFile(rec); n != 0 {
		t.Errorf("warnings = %+v, want no drop warning", rec.Warnings)
	}
}

// FR-005: a refused file saves nothing and deploys nothing.
func TestDeployComposeRefused(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	_, err := e.svc.DeployCompose(ctx, "blog", compose.Request{Compose: "services:\n  web:\n    build: .\n"}, app.TriggerManual)
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "services.web.build" {
		t.Fatalf("DeployCompose = %v, want a field error on services.web.build", err)
	}
	if _, err := e.svc.GetApp(ctx, "blog"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetApp = %v, want nothing saved", err)
	}
	if e.nomad.Registrations() != 0 {
		t.Error("Nomad got a job")
	}
}

// Feature 014: a Compose deploy can be conditional on the stored app too.
func TestDeployComposeIf(t *testing.T) {
	ctx := context.Background()
	req := compose.Request{Compose: twoServices}

	t.Run("IfNew creates and then refuses", func(t *testing.T) {
		e := newEnv(t)
		rec, err := e.svc.DeployComposeIf(ctx, "blog", req, Condition{IfNew: true}, app.TriggerManual)
		if err != nil {
			t.Fatalf("DeployComposeIf() = %v", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		before, _ := e.svc.GetApp(ctx, "blog")

		_, err = e.svc.DeployComposeIf(ctx, "blog", req, Condition{IfNew: true}, app.TriggerManual)
		if !errors.Is(err, ErrAppExists) {
			t.Fatalf("second DeployComposeIf() = %v, want ErrAppExists", err)
		}
		if after, _ := e.svc.GetApp(ctx, "blog"); !reflect.DeepEqual(after, before) || e.nomad.Registrations() != 1 {
			t.Errorf("a refused create changed something (%d registrations)", e.nomad.Registrations())
		}
	})

	t.Run("IfRevision", func(t *testing.T) {
		e := newEnv(t)
		rec, err := e.svc.DeployCompose(ctx, "blog", req, app.TriggerManual)
		if err != nil {
			t.Fatalf("DeployCompose() = %v", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		stored, _ := e.svc.GetApp(ctx, "blog")

		if _, err := e.svc.DeployComposeIf(ctx, "blog", req, Condition{IfRevision: "0000000000000000"}, app.TriggerManual); !errors.Is(err, ErrAppChanged) {
			t.Fatalf("with a stale revision = %v, want ErrAppChanged", err)
		}
		if e.nomad.Registrations() != 1 {
			t.Errorf("registrations = %d, want 1", e.nomad.Registrations())
		}

		again, err := e.svc.DeployComposeIf(ctx, "blog", req, Condition{IfRevision: stored.Revision}, app.TriggerManual)
		if err != nil {
			t.Fatalf("with the current revision = %v", err)
		}
		waitFinal(t, e.store, again.Deployment.ID)
	})

	t.Run("a refused file names its path and saves nothing", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.svc.DeployComposeIf(ctx, "blog", compose.Request{Compose: "services:\n  web:\n    build: .\n"}, Condition{IfNew: true}, app.TriggerManual)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "services.web.build" {
			t.Fatalf("DeployComposeIf() = %v, want a field error on services.web.build", err)
		}
		if _, err := e.svc.GetApp(ctx, "blog"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp() = %v, want nothing saved", err)
		}
	})

	t.Run("a secret with no value is refused on create", func(t *testing.T) {
		e := newEnv(t)
		withSecret := compose.Request{Compose: twoServices, Services: map[string]compose.Overlay{
			"web": {Secrets: []app.Secret{{Name: "TOKEN"}}},
		}}
		var fe *app.FieldError
		if _, err := e.svc.DeployComposeIf(ctx, "blog", withSecret, Condition{IfNew: true}, app.TriggerManual); !errors.As(err, &fe) {
			t.Fatalf("DeployComposeIf() = %v, want *app.FieldError", err)
		}
		if _, err := e.svc.GetApp(ctx, "blog"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp() = %v, want nothing saved", err)
		}
	})
}

// FR-002: a preview answers the app and its warnings, with secret names only,
// and changes nothing.
func TestPreviewCompose(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	req := compose.Request{Compose: twoServices, Services: map[string]compose.Overlay{
		"web": {Secrets: []app.Secret{{Name: "API_KEY", Value: new("k1")}}},
	}}
	a, warnings, err := e.svc.PreviewCompose(ctx, "blog", req)
	if err != nil {
		t.Fatalf("PreviewCompose: %v", err)
	}
	if len(a.Services) != 2 || len(warnings) == 0 {
		t.Errorf("preview = %+v, %+v", a, warnings)
	}
	if s := a.Services[0].Secrets; len(s) != 1 || s[0].Value != nil {
		t.Errorf("preview secrets = %+v, want the name only", s)
	}
	if _, err := e.svc.GetApp(ctx, "blog"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetApp = %v, want nothing saved", err)
	}
	if e.nomad.Registrations() != 0 || e.secrets.Replaces() != 0 || len(e.nomad.HostVolumes()) != 0 {
		t.Error("a preview reached Nomad or the secret store")
	}
}

// A refusal of the app's own rules names its path in the file, as a deploy's does.
func TestPreviewComposeRefusalNamesFilePath(t *testing.T) {
	e := newEnv(t)
	req := compose.Request{Compose: twoServices, Services: map[string]compose.Overlay{
		"web": {Health: &compose.Health{Interval: 5 * time.Second, Timeout: 9 * time.Second}},
	}}
	_, _, err := e.svc.PreviewCompose(context.Background(), "blog", req)
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "services.web.health.timeout" {
		t.Errorf("PreviewCompose = %v, want a field error at services.web.health.timeout", err)
	}
}
