package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const marker = "s3cr3t-marker"

// withSecrets is whoami with the given secrets on its web service.
func withSecrets(list ...app.Secret) app.App {
	a := whoami()
	a.Services[0].Secrets = list
	return a
}

func set(name, value string) app.Secret { return app.Secret{Name: name, Value: &value} }
func keep(name string) app.Secret       { return app.Secret{Name: name} }

// deployHealthy deploys a and waits for the rollout to end healthy.
func deployHealthy(t *testing.T, e *env, a app.App) store.DeploymentRecord {
	t.Helper()
	rec, err := e.svc.Deploy(context.Background(), a, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	final := waitFinal(t, e.store, rec.Deployment.ID)
	if final.Status != store.StatusHealthy {
		t.Fatalf("deployment = %s (%s), want healthy", final.Status, final.Error)
	}
	waitFor(t, "the secrets to be committed", func() bool { owner, _ := e.secrets.Kept(a.ID); return owner == "" })
	return final
}

func TestDeploySecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	deployHealthy(t, e, withSecrets(set("DATABASE_URL", marker), set("API_KEY", "k1")))
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"DATABASE_URL": marker, "API_KEY": "k1"}}) {
		t.Fatalf("live = %v", got)
	}

	// Only names reach the database and the job.
	stored, err := e.svc.GetApp(ctx, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stored.App.Services[0].Secrets {
		if s.Value != nil {
			t.Errorf("stored secret %s has a value", s.Name)
		}
	}
	doc, _ := json.Marshal(stored)
	job, _ := json.Marshal(e.nomad.Job("whoami"))
	if strings.Contains(string(doc), marker) || strings.Contains(string(job), marker) {
		t.Error("a secret value reached the stored app or the Nomad job")
	}

	// A deploy that leaves API_KEY out removes it; a new value replaces.
	deployHealthy(t, e, withSecrets(set("DATABASE_URL", "db2")))
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"DATABASE_URL": "db2"}}) {
		t.Errorf("live after the second deploy = %v", got)
	}

	// No secrets at all: nothing is left.
	deployHealthy(t, e, whoami())
	if got := e.secrets.Live("whoami"); len(got) != 0 {
		t.Errorf("live after a deploy without secrets = %v", got)
	}
}

func TestDeploySecretsPerService(t *testing.T) {
	e := newEnv(t)
	a := whoami()
	worker := app.Service{Name: "worker", Image: "traefik/whoami:latest", Secrets: []app.Secret{set("DATABASE_URL", "worker-db")}}
	a.Services[0].Secrets = []app.Secret{set("DATABASE_URL", "web-db")}
	a.Services = append(a.Services, worker)
	deployHealthy(t, e, a)
	want := secrets.Sets{"web": {"DATABASE_URL": "web-db"}, "worker": {"DATABASE_URL": "worker-db"}}
	if got := e.secrets.Live("whoami"); !got.Equal(want) {
		t.Errorf("live = %v, want %v", got, want)
	}

	// Removing a service removes its secrets.
	deployHealthy(t, e, withSecrets(keep("DATABASE_URL")))
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"DATABASE_URL": "web-db"}}) {
		t.Errorf("live after removing worker = %v", got)
	}
}

func TestDeploySecretsKeepWithoutValue(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	_, err := e.svc.Deploy(ctx, withSecrets(keep("API_KEY")), app.TriggerManual)
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "services[0].secrets[0].value" {
		t.Fatalf("Deploy = %v, want a field error on services[0].secrets[0].value", err)
	}
	if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetApp = %v, want the app not saved", err)
	}
}

func TestDeploySecretStoreUnavailable(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withSecrets(set("API_KEY", "k1")))
	registered := e.nomad.Registrations()

	e.secrets.SetUnavailable(true)
	before := deployCount(e, "whoami")
	_, err := e.svc.Deploy(ctx, withSecrets(set("API_KEY", "k2")), app.TriggerManual)
	if !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("Deploy = %v, want ErrUnavailable", err)
	}
	if deployCount(e, "whoami") != before {
		t.Error("a deployment was recorded although nothing could be deployed")
	}

	// An app without secrets still needs the store (old secrets may have
	// to go): that deploy is recorded and fails.
	rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	var de *DeployError
	if !errors.Is(err, secrets.ErrUnavailable) || !errors.As(err, &de) || rec.Status != store.StatusFailed {
		t.Errorf("Deploy without secrets = %v, %s; want a failed deployment record", err, rec.Status)
	}
	if e.nomad.Registrations() != registered {
		t.Error("Nomad got the job although the secrets couldn't be written")
	}
	e.secrets.SetUnavailable(false)
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"API_KEY": "k1"}}) {
		t.Errorf("live = %v, want the running value", got)
	}
}

func TestDeploySecretsRegisterFails(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withSecrets(set("API_KEY", "k1")))

	e.nomad.RejectNext(400, "job is invalid")
	if _, err := e.svc.Deploy(ctx, withSecrets(set("API_KEY", "k2")), app.TriggerManual); err == nil {
		t.Fatal("Deploy = nil, want Nomad's rejection")
	}
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"API_KEY": "k1"}}) {
		t.Errorf("live = %v, want the previous value put back", got)
	}
	if owner, _ := e.secrets.Kept("whoami"); owner != "" {
		t.Errorf("kept copy still owned by %q", owner)
	}
}

func TestSupersededDeployWritesNoSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	release := e.registry.Hold("traefik/whoami", "latest")

	aDone := make(chan store.DeploymentRecord, 1)
	go func() {
		rec, _ := e.svc.Deploy(ctx, withSecrets(set("API_KEY", "from-a")), app.TriggerManual)
		aDone <- rec
	}()
	waitFor(t, "A to reach the registry", func() bool { return e.registry.Requests() >= 1 })
	list, _ := e.store.ListDeployments(ctx, "whoami", 0)
	aID := list[0].Deployment.ID

	bDone := make(chan store.DeploymentRecord, 1)
	go func() {
		rec, _ := e.svc.Deploy(ctx, withSecrets(set("API_KEY", "from-b")), app.TriggerManual)
		bDone <- rec
	}()
	waitFor(t, "B to be the latest request", func() bool { return e.svc.latestOf("whoami") != aID })
	release()

	if a := <-aDone; a.Status != store.StatusSuperseded {
		t.Fatalf("A = %s, want superseded", a.Status)
	}
	b := <-bDone
	waitFinal(t, e.store, b.Deployment.ID)
	if n := e.secrets.Replaces(); n != 1 {
		t.Errorf("Replace calls = %d, want only B's", n)
	}
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"API_KEY": "from-b"}}) {
		t.Errorf("live = %v, want B's value", got)
	}
}
