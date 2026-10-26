package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const (
	stagingID    = "shop--staging--web"
	productionID = "shop--production--web"
	// otherDigest is where the tag points after the images were promoted: a
	// promotion must not follow it.
	otherDigest = "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	repoRef     = "traefik/whoami@"
)

// shopEnv is an installation with project shop (production and staging).
func shopEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.svc.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	return e
}

// production is the shop's production web: its own env, secret, domain,
// copies, memory and a backup.
func production() app.App {
	a := whoamiAs(productionID)
	svc := &a.Services[0]
	svc.Env = []app.EnvVar{{Name: "MODE", Value: "production"}, {Name: "POSTGRES_PASSWORD", Value: "pg"}}
	svc.Secrets = []app.Secret{set("DB_URL", "prod-db")}
	svc.Domains = []app.Domain{{Host: "shop.example.com", Port: "http"}}
	svc.Replicas = new(3)
	svc.Resources = app.Resources{MemoryMB: 512}
	svc.Backup = &app.Backup{Engine: app.BackupPostgres, Keep: 3}
	return a
}

// staging is the shop's staging web: another shape (command, arguments, port,
// health check) and its own env, secret, domain and copies.
func staging() app.App {
	a := whoamiAs(stagingID)
	svc := &a.Services[0]
	svc.Env = []app.EnvVar{{Name: "MODE", Value: "staging"}}
	svc.Secrets = []app.Secret{set("DB_URL", "stage-db")}
	svc.Domains = []app.Domain{{Host: "staging.example.com", Port: "http"}}
	svc.Replicas = new(1)
	svc.Command = "/whoami"
	svc.Args = []string{"--port", "8080"}
	svc.Ports = []app.Port{{Name: "http", ContainerPort: 8080}}
	svc.Health = &app.HealthCheck{Path: "/health"}
	return a
}

// both deploys production with the tag at whoamiDigest, then moves the tag
// and deploys staging, so the two run different images; then the tag moves
// again, to a third image neither runs. It returns the two deployments.
func both(t *testing.T, e *env) (prod, stage store.DeploymentRecord) {
	t.Helper()
	setDestination(t, e)
	prod = deployHealthy(t, e, production())
	e.registry.AddIndex("traefik/whoami", "latest", movedDigest)
	stage = deployHealthy(t, e, staging())
	e.registry.AddIndex("traefik/whoami", "latest", otherDigest)
	return prod, stage
}

// storedService returns the one service of a stored app.
func storedService(t *testing.T, e *env, id string) app.Service {
	t.Helper()
	rec, err := e.svc.GetApp(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return rec.App.Services[0]
}

// untouched checks a refused promotion left the target as it was: the same
// revision and no new deployment.
func untouched(t *testing.T, e *env, id string, revision string, deployments int) {
	t.Helper()
	rec, err := e.svc.GetApp(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Revision != revision {
		t.Errorf("%s: revision = %s, want %s (the app was saved)", id, rec.Revision, revision)
	}
	recs, _ := e.svc.ListDeployments(context.Background(), id, 0)
	if len(recs) != deployments {
		t.Errorf("%s: %d deployments, want %d", id, len(recs), deployments)
	}
}

// US3: production runs the digests and shape staging ran, whatever the tag
// points at now, and keeps what is its own.
func TestPromote(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	_, stage := both(t, e)

	rec, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("Promote() = %v", err)
	}
	if rec.AppID != productionID || rec.Deployment.Trigger != app.TriggerConsole {
		t.Errorf("record = app %s, trigger %s; want %s from the console", rec.AppID, rec.Deployment.Trigger, productionID)
	}
	if rec.Source != stage.Deployment.ID || rec.SourceApp != stagingID {
		t.Errorf("source = %q of app %q, want %s of %s", rec.Source, rec.SourceApp, stage.Deployment.ID, stagingID)
	}
	if got := rec.Deployment.Digests["web"]; got != repoRef+movedDigest {
		t.Errorf("digest = %q, want staging's %q (not the tag's %s now)", got, repoRef+movedDigest, otherDigest)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("final = %s (%s), want healthy", final.Status, final.Error)
	}

	// From staging: image, command, arguments, port, health check.
	svc := storedService(t, e, productionID)
	want := staging().Services[0]
	if svc.Image != want.Image || svc.Command != want.Command || !slices.Equal(svc.Args, want.Args) ||
		!slices.Equal(svc.Ports, want.Ports) || !reflect.DeepEqual(svc.Health, want.Health) {
		t.Errorf("shape = %+v, want staging's %+v", svc, want)
	}
	// Production's own: env, secrets (names), domains, copies, memory, backup.
	prod := production().WithoutSecretValues().Services[0]
	if !reflect.DeepEqual(svc.Env, prod.Env) || !reflect.DeepEqual(svc.Secrets, prod.Secrets) ||
		!reflect.DeepEqual(svc.Domains, prod.Domains) || !reflect.DeepEqual(svc.Replicas, prod.Replicas) ||
		svc.Resources != prod.Resources || !reflect.DeepEqual(svc.Backup, prod.Backup) {
		t.Errorf("kept = %+v, want production's %+v", svc, prod)
	}
	if got := e.secrets.Live(productionID); !got.Equal(secrets.Sets{"web": {"DB_URL": "prod-db"}}) {
		t.Errorf("production's secrets = %v, want its own value", got)
	}
	task := e.nomad.Job(productionID).TaskGroups[0].Tasks[0]
	if task.Config["image"] != repoRef+movedDigest || task.Env["MODE"] != "production" {
		t.Errorf("Nomad runs %v with MODE=%q, want staging's image and production's env", task.Config["image"], task.Env["MODE"])
	}
	// Staging is untouched.
	if got := storedService(t, e, stagingID); !reflect.DeepEqual(got.Env, staging().Services[0].Env) {
		t.Errorf("staging changed: %+v", got)
	}
}

// Feature 026: the entrypoint is part of how the image runs, so a promotion
// takes the source's, and says so when it differs.
func TestPromotedTakesTheEntrypoint(t *testing.T) {
	source, target := staging(), production()
	source.Services[0].Entrypoint = []string{"/bin/sh", "-c"}

	got, changes, _, err := promoted(target, source)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Services[0].Entrypoint, []string{"/bin/sh", "-c"}) {
		t.Errorf("entrypoint = %q, want staging's", got.Services[0].Entrypoint)
	}
	if len(changes) != 1 || !slices.Contains(changes[0].Changed, "entrypoint") {
		t.Errorf("changed = %+v, want entrypoint among them", changes)
	}
	got.Services[0].Entrypoint[0] = "changed"
	if source.Services[0].Entrypoint[0] != "/bin/sh" {
		t.Error("the promoted service shares its entrypoint with the source")
	}

	// Both without one: nothing to say.
	_, changes, _, _ = promoted(production(), staging())
	if slices.Contains(changes[0].Changed, "entrypoint") {
		t.Errorf("changed = %v, no entrypoint differs", changes[0].Changed)
	}
}

func TestPreviewPromotion(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	prod, stage := both(t, e)
	before, _ := e.svc.GetApp(ctx, productionID)
	replaces := e.secrets.Replaces()

	got, err := e.svc.PreviewPromotion(ctx, stagingID, "production")
	if err != nil {
		t.Fatalf("PreviewPromotion() = %v", err)
	}
	want := PromotionPreview{
		From: stagingID, To: productionID, FromEnvironment: "staging", ToEnvironment: "production",
		FromDeployment: stage.Deployment.ID,
		Services: []ServiceChange{{Name: "web",
			ImageFrom: "traefik/whoami:latest", ImageTo: "traefik/whoami:latest",
			DigestFrom: prod.Deployment.Digests["web"], DigestTo: repoRef + movedDigest,
			Changed: []string{"command", "args", "ports", "health"}}},
		Warnings:       []app.Warning{},
		TargetRevision: before.Revision,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("preview = %+v\nwant     %+v", got, want)
	}
	if got.Services[0].DigestFrom != repoRef+whoamiDigest {
		t.Errorf("DigestFrom = %q, want what production runs", got.Services[0].DigestFrom)
	}
	// Nothing happened.
	untouched(t, e, productionID, before.Revision, 1)
	if e.secrets.Replaces() != replaces {
		t.Error("the preview wrote secrets")
	}

	// The preview refuses as the promotion does.
	if _, err := e.svc.PreviewPromotion(ctx, stagingID, "nowhere"); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Errorf("preview to an unknown environment = %v", err)
	}
}

// A service only staging has is added whole, without its domains; its secret
// needs a value first, and the check is the deploy's own.
func TestPromoteAddsAService(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, production())

	stage := staging()
	worker := app.Service{
		Name: "api", Image: "traefik/whoami:latest",
		Ports:   []app.Port{{Name: "http", ContainerPort: 80}},
		Health:  &app.HealthCheck{Path: "/"},
		Domains: []app.Domain{{Host: "api.staging.example.com", Port: "http"}},
		Env:     []app.EnvVar{{Name: "ROLE", Value: "api"}},
		Secrets: []app.Secret{set("API_TOKEN", "stage-token")},
	}
	stage.Services = append(stage.Services, worker)
	deployHealthy(t, e, stage)
	before, _ := e.svc.GetApp(ctx, productionID)

	// No value for API_TOKEN in production: refused, by the preview too, and nothing saved.
	var fe *app.FieldError
	if _, err := e.svc.PreviewPromotion(ctx, stagingID, "production"); !errors.As(err, &fe) || !strings.Contains(fe.Message, "API_TOKEN") {
		t.Errorf("preview = %v, want the missing secret", err)
	}
	if _, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual); !errors.As(err, &fe) || !strings.Contains(fe.Field, "secrets") || !strings.Contains(fe.Message, "API_TOKEN") {
		t.Fatalf("Promote = %v, want a field error on the secret's value", err)
	}
	untouched(t, e, productionID, before.Revision, 1)

	// Give production its own value.
	e.secrets.SetLive(productionID, secrets.Sets{"web": {"DB_URL": "prod-db"}, "api": {"API_TOKEN": "prod-token"}})
	preview, err := e.svc.PreviewPromotion(ctx, stagingID, "production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(preview.Added, []string{"api"}) || len(preview.Services) != 2 || preview.Services[1].Name != "api" ||
		preview.Services[1].ImageFrom != "" || preview.Services[1].DigestFrom != "" || preview.Services[1].DigestTo == "" {
		t.Errorf("preview = %+v, want api added", preview)
	}
	rec, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("Promote() = %v", err)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("final = %s (%s)", final.Status, final.Error)
	}
	got, _ := e.svc.GetApp(ctx, productionID)
	if len(got.App.Services) != 2 || got.App.Services[1].Name != "api" {
		t.Fatalf("services = %+v, want web and api", got.App.Services)
	}
	api := got.App.Services[1]
	if len(api.Domains) != 0 || len(api.Env) != 1 || len(api.Secrets) != 1 || api.Secrets[0].Value != nil {
		t.Errorf("added api = %+v, want its env and secret name, no domains, no value", api)
	}
	if len(got.App.Services[0].Domains) != 1 || got.App.Services[0].Domains[0].Host != "shop.example.com" {
		t.Errorf("web's domains = %+v, want production's", got.App.Services[0].Domains)
	}
	if live := e.secrets.Live(productionID); !live.Equal(secrets.Sets{"web": {"DB_URL": "prod-db"}, "api": {"API_TOKEN": "prod-token"}}) {
		t.Errorf("secrets = %v", live)
	}
}

// A service only production has would be deleted: refused, naming it.
func TestPromoteRefusesToRemoveAService(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	setDestination(t, e)
	prod := production()
	prod.Services = append(prod.Services, app.Service{Name: "worker", Image: "traefik/whoami:latest",
		Ports: []app.Port{{Name: "http", ContainerPort: 80}}, Health: &app.HealthCheck{Path: "/"}})
	deployHealthy(t, e, prod)
	deployHealthy(t, e, staging())
	before, _ := e.svc.GetApp(ctx, productionID)

	for name, do := range map[string]func() error{
		"promote": func() error {
			_, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual)
			return err
		},
		"preview": func() error {
			_, err := e.svc.PreviewPromotion(ctx, stagingID, "production")
			return err
		},
	} {
		err := do()
		var re *RefusalError
		if !errors.Is(err, ErrPromotionRemovesService) || !errors.As(err, &re) || re.Field != "services.worker" {
			t.Errorf("%s = %v, want ErrPromotionRemovesService on services.worker", name, err)
		}
	}
	untouched(t, e, productionID, before.Revision, 1)
}

func TestPromoteRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("environments", func(t *testing.T) {
		e := shopEnv(t)
		both(t, e)
		var pe *PlaceError
		if _, err := e.svc.Promote(ctx, stagingID, "nowhere", Condition{}, app.TriggerManual); !errors.Is(err, ErrEnvironmentNotFound) || !errors.As(err, &pe) || pe.Field != "to" {
			t.Errorf("unknown environment = %v, want ErrEnvironmentNotFound on to", err)
		}
		var fe *app.FieldError
		if _, err := e.svc.Promote(ctx, stagingID, "staging", Condition{}, app.TriggerManual); !errors.As(err, &fe) || fe.Field != "to" {
			t.Errorf("same environment = %v, want a field error on to", err)
		}
		for _, bad := range []string{"", "Prod", "a--b"} {
			if _, err := e.svc.Promote(ctx, stagingID, bad, Condition{}, app.TriggerManual); !errors.As(err, &fe) || fe.Field != "to" {
				t.Errorf("to = %q: %v, want a field error on to", bad, err)
			}
		}
		if _, err := e.svc.Promote(ctx, "nothing", "production", Condition{}, app.TriggerManual); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unknown app = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("no app of that name there", func(t *testing.T) {
		e := shopEnv(t)
		deployHealthy(t, e, staging())
		_, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual)
		var re *RefusalError
		if !errors.Is(err, ErrAppNotInEnvironment) || !errors.As(err, &re) || re.Field != "to" {
			t.Fatalf("err = %v, want ErrAppNotInEnvironment on to", err)
		}
		if !strings.Contains(err.Error(), "copy") {
			t.Errorf("message %q should say to copy the app first", err)
		}
	})

	t.Run("the source is not healthy", func(t *testing.T) {
		e := shopEnv(t)
		both(t, e)
		before, _ := e.svc.GetApp(ctx, productionID)
		def := staging()
		d := app.Deployment{ID: "failed-newer", Trigger: app.TriggerManual, Digests: map[string]string{}}
		if _, err := e.store.CreateDeployment(ctx, stagingID, d, &def, nil, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.store.UpdateDeployment(ctx, d.ID, store.DeploymentUpdate{Status: store.StatusFailed}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual); !errors.Is(err, ErrDeploymentNotHealthy) {
			t.Errorf("failed newest = %v, want ErrDeploymentNotHealthy", err)
		}
		if _, err := e.svc.PreviewPromotion(ctx, stagingID, "production"); !errors.Is(err, ErrDeploymentNotHealthy) {
			t.Errorf("preview = %v, want ErrDeploymentNotHealthy", err)
		}
		untouched(t, e, productionID, before.Revision, 1)
	})

	t.Run("the source never deployed, or recorded no definition", func(t *testing.T) {
		e := shopEnv(t)
		setDestination(t, e)
		deployHealthy(t, e, production())
		if _, err := e.store.SaveApp(ctx, staging(), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual); !errors.Is(err, ErrNoLiveDeployment) {
			t.Errorf("never deployed = %v, want ErrNoLiveDeployment", err)
		}
		d := app.Deployment{ID: "old-no-definition", Trigger: app.TriggerManual, Digests: map[string]string{"web": repoRef + movedDigest}}
		if _, err := e.store.CreateDeployment(ctx, stagingID, d, nil, nil, ""); err != nil {
			t.Fatal(err)
		}
		for _, st := range []store.Status{store.StatusRunning, store.StatusHealthy} {
			if _, err := e.store.UpdateDeployment(ctx, d.ID, store.DeploymentUpdate{Status: st}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual); !errors.Is(err, ErrDefinitionNotRecorded) {
			t.Errorf("no definition = %v, want ErrDefinitionNotRecorded", err)
		}
	})

	t.Run("an image that is gone is refused before anything is saved", func(t *testing.T) {
		e := shopEnv(t)
		both(t, e)
		before, _ := e.svc.GetApp(ctx, productionID)
		replaces := e.secrets.Replaces()
		e.registry.Delete("traefik/whoami", movedDigest)

		for name, do := range map[string]func() error{
			"promote": func() error {
				_, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual)
				return err
			},
			"preview": func() error {
				_, err := e.svc.PreviewPromotion(ctx, stagingID, "production")
				return err
			},
		} {
			var ie *deploy.ImageError
			if err := do(); !errors.As(err, &ie) || !errors.Is(err, deploy.ErrImageNotFound) {
				t.Errorf("%s = %v, want an image-not-found error", name, err)
			}
		}
		untouched(t, e, productionID, before.Revision, 1)
		if e.secrets.Replaces() != replaces {
			t.Error("secrets were replaced for a refused promotion")
		}
	})

	t.Run("a target that stopped being valid", func(t *testing.T) {
		// Production's domain names port http; a source whose service has no
		// such port leaves it dangling: the check names the field.
		e := shopEnv(t)
		setDestination(t, e)
		deployHealthy(t, e, production())
		st := staging()
		st.Services[0].Ports = []app.Port{{Name: "web", ContainerPort: 8080}}
		st.Services[0].Domains = []app.Domain{{Host: "staging.example.com", Port: "web"}}
		deployHealthy(t, e, st)
		before, _ := e.svc.GetApp(ctx, productionID)
		var fe *app.FieldError
		if _, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual); !errors.As(err, &fe) || !strings.Contains(fe.Field, "domains") {
			t.Errorf("err = %v, want a field error on the dangling domain", err)
		}
		untouched(t, e, productionID, before.Revision, 1)
	})
}

// If-Match: the revision the owner saw in the preview.
func TestPromoteCondition(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	both(t, e)
	preview, err := e.svc.PreviewPromotion(ctx, stagingID, "production")
	if err != nil {
		t.Fatal(err)
	}

	// Someone edits production after the preview.
	edited := production()
	edited.Services[0].Env = []app.EnvVar{{Name: "MODE", Value: "edited"}, {Name: "POSTGRES_PASSWORD", Value: "pg"}}
	deployHealthy(t, e, edited)
	now, _ := e.svc.GetApp(ctx, productionID)

	_, err = e.svc.Promote(ctx, stagingID, "production", Condition{IfRevision: preview.TargetRevision}, app.TriggerManual)
	if !errors.Is(err, ErrAppChanged) || !strings.Contains(err.Error(), now.Revision) {
		t.Fatalf("stale revision = %v, want ErrAppChanged naming %s", err, now.Revision)
	}
	untouched(t, e, productionID, now.Revision, 2)

	// With the current revision it goes, and keeps the edit.
	rec, err := e.svc.Promote(ctx, stagingID, "production", Condition{IfRevision: now.Revision}, app.TriggerManual)
	if err != nil {
		t.Fatalf("with the current revision: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if got := storedService(t, e, productionID).Env[0].Value; got != "edited" {
		t.Errorf("MODE = %q, want the edit kept", got)
	}

	// IfNew means nothing for an app that exists.
	if _, err := e.svc.Promote(ctx, stagingID, "production", Condition{IfNew: true}, app.TriggerManual); err == nil {
		t.Error("IfNew was accepted")
	}
}

// Volumes come from the source with the rest of the shape.
func TestPromoteVolumes(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	prod := whoamiAs(productionID)
	prod.Services[0].Replicas = nil
	deployHealthy(t, e, prod)
	st := whoamiAs(stagingID)
	st.Services[0].Replicas = nil
	st.Services[0].Domains = []app.Domain{{Host: "staging.example.com", Port: "http"}} // a host is one app's
	st.Services[0].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
	deployHealthy(t, e, st)

	preview, err := e.svc.PreviewPromotion(ctx, stagingID, "production")
	if err != nil {
		t.Fatal(err)
	}
	if got := preview.Services[0].Changed; !slices.Equal(got, []string{"volumes"}) {
		t.Errorf("changed = %v, want [volumes]", got)
	}
	rec, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if got := storedService(t, e, productionID).Volumes; !reflect.DeepEqual(got, st.Services[0].Volumes) {
		t.Errorf("volumes = %+v, want staging's", got)
	}
}

// A target that keeps a Compose file loses it, with the warning, in the
// preview and in the deploy.
func TestPromoteDropsKeptFile(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	file := "services:\n  web:\n    image: traefik/whoami:latest\n    ports: [\"8080:80\"]\n"
	deployFile := func(id, host string) {
		t.Helper()
		req := compose.Request{Compose: file}
		if host != "" {
			req.Services = map[string]compose.Overlay{"web": {Domains: []compose.Domain{{Host: host}}}}
		}
		rec, err := e.svc.DeployCompose(ctx, id, req, app.TriggerManual)
		if err != nil {
			t.Fatalf("DeployCompose %s: %v", id, err)
		}
		if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
			t.Fatalf("%s = %s (%s)", id, final.Status, final.Error)
		}
	}
	deployFile(productionID, "shop.example.com")
	deployFile(stagingID, "")

	preview, err := e.svc.PreviewPromotion(ctx, stagingID, "production")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	dropped := func(ws []app.Warning) bool {
		return slices.ContainsFunc(ws, func(w app.Warning) bool { return w.Message == droppedFileWarning })
	}
	if !dropped(preview.Warnings) {
		t.Errorf("preview warnings = %+v, want the dropped-file warning", preview.Warnings)
	}
	rec, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !dropped(rec.Warnings) {
		t.Errorf("deploy warnings = %+v, want the dropped-file warning", rec.Warnings)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if got, _ := e.svc.GetApp(ctx, productionID); got.Compose != nil {
		t.Errorf("production still keeps a file: %+v", got.Compose)
	}
}

func TestCopyApp(t *testing.T) {
	ctx := context.Background()

	t.Run("an app is copied undeployed, without domains or secret values", func(t *testing.T) {
		e := shopEnv(t)
		setDestination(t, e)
		prod := production()
		prod.Services = append(prod.Services, app.Service{Name: "worker", Image: "traefik/whoami:latest",
			Ports: []app.Port{{Name: "http", ContainerPort: 80}}, Health: &app.HealthCheck{Path: "/"},
			Secrets: []app.Secret{set("QUEUE_KEY", "queue-s3cret")}})
		deployHealthy(t, e, prod)
		if _, err := e.store.SetWebhookToken(ctx, productionID, "hash"); err != nil {
			t.Fatal(err)
		}

		rec, missing, err := e.svc.CopyApp(ctx, productionID, "staging")
		if err != nil {
			t.Fatalf("CopyApp() = %v", err)
		}
		if rec.App.ID != stagingID || rec.Place != (app.Place{Project: "shop", Environment: "staging", Name: "web"}) {
			t.Errorf("copy = %s at %+v", rec.App.ID, rec.Place)
		}
		want := production().WithoutSecretValues()
		want.ID = stagingID
		want.Services = append(want.Services, prod.WithoutSecretValues().Services[1])
		for i := range want.Services {
			want.Services[i].Domains = nil
		}
		if !reflect.DeepEqual(rec.App, want) {
			t.Errorf("copy = %+v\nwant   %+v", rec.App, want)
		}
		wantMissing := []MissingSecret{{"web", "DB_URL"}, {"worker", "QUEUE_KEY"}}
		if !reflect.DeepEqual(missing, wantMissing) {
			t.Errorf("missing = %+v, want %+v", missing, wantMissing)
		}
		if deps, _ := e.svc.ListDeployments(ctx, stagingID, 0); len(deps) != 0 {
			t.Errorf("the copy was deployed: %+v", deps)
		}
		if e.nomad.Job(stagingID) != nil {
			t.Error("Nomad has a job for the copy")
		}
		if doc, _ := json.Marshal(rec); strings.Contains(string(doc), "s3cret") || strings.Contains(string(doc), "prod-db") {
			t.Error("a secret value reached the copy")
		}
		// The source is as it was.
		if got := storedService(t, e, productionID); len(got.Domains) != 1 {
			t.Errorf("source domains = %+v", got.Domains)
		}

		// Deploying it before the secrets have values is refused, naming them.
		var fe *app.FieldError
		if _, err := e.svc.Deploy(ctx, rec.App, app.TriggerManual); !errors.As(err, &fe) || !strings.Contains(fe.Message, "DB_URL") {
			t.Errorf("deploy without the secrets = %v, want the missing secret named", err)
		}
		// No webhook token came along.
		if _, err := e.store.GetWebhookToken(ctx, stagingID); err == nil {
			t.Error("the copy has a webhook token")
		}
	})

	t.Run("the kept Compose file comes along without its domains", func(t *testing.T) {
		e := shopEnv(t)
		file := "# prod\nservices:\n  web:\n    image: traefik/whoami:latest\n    ports: [\"8080:80\"]\n"
		req := compose.Request{Compose: file, Services: map[string]compose.Overlay{
			"web": {Domains: []compose.Domain{{Host: "shop.example.com"}}, Secrets: []app.Secret{set("DB_URL", "s3cret")}}}}
		rec, err := e.svc.DeployCompose(ctx, productionID, req, app.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)

		cp, missing, err := e.svc.CopyApp(ctx, productionID, "staging")
		if err != nil {
			t.Fatalf("CopyApp() = %v", err)
		}
		if cp.Compose == nil || cp.Compose.Compose != file {
			t.Fatalf("kept file = %+v, want the file as typed", cp.Compose)
		}
		overlay := cp.Compose.Services["web"]
		if len(overlay.Domains) != 0 || len(overlay.Secrets) != 1 || overlay.Secrets[0].Value != nil {
			t.Errorf("overlay = %+v, want no domains and the secret by name", overlay)
		}
		if len(missing) != 1 || missing[0] != (MissingSecret{"web", "DB_URL"}) {
			t.Errorf("missing = %+v", missing)
		}
		// The source's overlay still has its domain.
		if src, _ := e.svc.GetApp(ctx, productionID); len(src.Compose.Services["web"].Domains) != 1 {
			t.Errorf("the source's overlay changed: %+v", src.Compose.Services["web"])
		}
	})

	t.Run("a copy of an app that was never deployed, with no secrets", func(t *testing.T) {
		e := shopEnv(t)
		a := whoamiAs(productionID)
		if _, err := e.store.SaveApp(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
		_, missing, err := e.svc.CopyApp(ctx, productionID, "staging")
		if err != nil || len(missing) != 0 {
			t.Errorf("CopyApp = missing %+v, %v; want none", missing, err)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		e := shopEnv(t)
		deployHealthy(t, e, whoamiAs(productionID))
		if _, _, err := e.svc.CopyApp(ctx, productionID, "staging"); err != nil {
			t.Fatal(err)
		}

		var re *RefusalError
		if _, _, err := e.svc.CopyApp(ctx, productionID, "staging"); !errors.Is(err, ErrAppExists) || !errors.As(err, &re) || re.Field != "to" {
			t.Errorf("again = %v, want ErrAppExists on to", err)
		}
		var pe *PlaceError
		if _, _, err := e.svc.CopyApp(ctx, productionID, "qa"); !errors.Is(err, ErrEnvironmentNotFound) || !errors.As(err, &pe) || pe.Field != "to" {
			t.Errorf("unknown environment = %v, want ErrEnvironmentNotFound on to", err)
		}
		var fe *app.FieldError
		if _, _, err := e.svc.CopyApp(ctx, productionID, "production"); !errors.As(err, &fe) || fe.Field != "to" {
			t.Errorf("same environment = %v, want a field error on to", err)
		}
		if _, _, err := e.svc.CopyApp(ctx, "nothing", "staging"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("unknown app = %v, want store.ErrNotFound", err)
		}
		// From before projects: its name holds "--", which can't be read back.
		if _, err := e.svc.CreateEnvironment(ctx, "default", "staging"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.store.SaveApp(ctx, whoamiAs("my--app"), nil); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.svc.CopyApp(ctx, "my--app", "staging"); !errors.As(err, &fe) || fe.Field != "id" {
			t.Errorf("an app named my--app = %v, want a field error on id", err)
		}
	})

	t.Run("an ID that is too long once placed", func(t *testing.T) {
		e := shopEnv(t)
		// Staging is shorter than production: 15+42 characters here, plus "-web" is 61;
		// the copy is 18+42, and 63 is the most.
		name := strings.Repeat("a", 42)
		id := "shop--staging--" + name
		a := whoamiAs(id)
		if _, err := e.store.SaveApp(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
		var fe *app.FieldError
		if _, _, err := e.svc.CopyApp(ctx, id, "production"); !errors.As(err, &fe) {
			t.Errorf("CopyApp = %v, want a field error for the length", err)
		}
		if _, err := e.svc.GetApp(ctx, "shop--production--"+name); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("the too-long copy was stored: %v", err)
		}
	})
}

// A copy deployed once is a target: promotion needs it to exist, and then works.
func TestCopyThenPromote(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, staging())
	cp, missing, err := e.svc.CopyApp(ctx, stagingID, "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 {
		t.Fatalf("missing = %+v", missing)
	}
	// Production's secret gets its value through a deploy, as the owner would.
	a := cp.App
	a.Services[0].Secrets = []app.Secret{set("DB_URL", "prod-db")}
	a.Services[0].Domains = []app.Domain{{Host: "shop.example.com", Port: "http"}}
	deployHealthy(t, e, a)
	e.registry.AddIndex("traefik/whoami", "latest", movedDigest)
	deployHealthy(t, e, withStagingEnv(staging(), "v2"))

	rec, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("Promote() = %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if rec.SourceApp != stagingID || rec.Deployment.Digests["web"] != repoRef+movedDigest {
		t.Errorf("record = %+v", rec)
	}
}

func withStagingEnv(a app.App, v string) app.App {
	a.Services[0].Env = []app.EnvVar{{Name: "MODE", Value: v}}
	return a
}
