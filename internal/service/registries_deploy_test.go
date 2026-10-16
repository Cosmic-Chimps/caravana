package service

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Deploying images from a registry that wants a login (feature 017, US1): the
// flow's three ways in (deploy, webhook, going back to a version) all end in the
// same register, and none puts a credential anywhere but its Nomad Variables.

// copyPath is the variable the service "web" of app "whoami" reads its pull
// credentials from.
const copyPath = "nomad/jobs/whoami/web/web"

// privateEnv is an env whose registry asks for a login ("deploy" and
// registryPassword) and holds acme/shop:v1, and a whoami app that runs it. The
// registry's host has a port, as a self-hosted one often does.
func privateEnv(t *testing.T) (*env, app.App) {
	t.Helper()
	e := newEnv(t)
	e.registry.RequireToken(true)
	e.registry.RequireLogin("deploy", registryPassword)
	e.registry.AddIndex("acme/shop", "v1", whoamiDigest)
	a := whoami()
	a.Services[0].Image = e.registry.Host() + "/acme/shop:v1"
	return e, a
}

// saveRegistry stores the credentials for the env's registry.
func saveRegistry(t *testing.T, e *env, password string) {
	t.Helper()
	if _, err := e.svc.SaveRegistry(context.Background(), e.registry.Host(), "deploy", &password); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
}

// pullItems is what a service's pull-credentials variable holds.
func pullItems(host, user, pass string) map[string]string {
	return map[string]string{"registry": host, nomadjob.RegistryUsernameEnv: user, nomadjob.RegistryPasswordEnv: pass}
}

func TestDeployPrivateImage(t *testing.T) {
	ctx := context.Background()
	e, a := privateEnv(t)
	host := e.registry.Host()

	t.Run("without credentials: not found, and where to add them", func(t *testing.T) {
		_, err := e.svc.Deploy(ctx, a, app.TriggerManual)
		if !errors.Is(err, deploy.ErrImageNotFound) || !strings.Contains(err.Error(), "add credentials for "+host) {
			t.Fatalf("Deploy() = %v, want ErrImageNotFound saying to add credentials for %s", err, host)
		}
	})

	t.Run("a wrong secret: the registry refused it, and nothing is registered", func(t *testing.T) {
		saveRegistry(t, e, "wrong-"+registryPassword)
		rec, err := e.svc.Deploy(ctx, a, app.TriggerManual)
		var ie *deploy.ImageError
		if !errors.Is(err, deploy.ErrRegistryAuth) || !errors.As(err, &ie) || ie.Field != "services[0].image" {
			t.Fatalf("Deploy() = %v, want an *ImageError on services[0].image matching ErrRegistryAuth", err)
		}
		if strings.Contains(err.Error(), registryPassword) {
			t.Errorf("error %q holds the secret", err)
		}
		if stored, _ := e.store.GetDeployment(ctx, rec.Deployment.ID); stored.Status != store.StatusFailed || strings.Contains(stored.Error, registryPassword) {
			t.Errorf("record = %s %q, want failed without the secret", stored.Status, stored.Error)
		}
		if e.nomad.Registrations() != 0 || len(e.nomad.VariablePaths()) != 1 {
			t.Errorf("registrations %d, variables %v; want none registered and only the stored credentials", e.nomad.Registrations(), e.nomad.VariablePaths())
		}
	})

	t.Run("the right secret: healthy, pulled with the placeholders", func(t *testing.T) {
		saveRegistry(t, e, registryPassword)
		rec, err := e.svc.Deploy(ctx, a, app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy() = %v", err)
		}
		// The pinned name keeps the registry's host and port.
		if got, want := rec.Deployment.Digests["web"], host+"/acme/shop@"+whoamiDigest; got != want {
			t.Errorf("pinned image = %q, want %q", got, want)
		}
		if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
			t.Fatalf("final = %s (%s), want healthy", final.Status, final.Error)
		}
		if got, want := e.nomad.Variable(copyPath), pullItems(host, "deploy", registryPassword); !maps.Equal(got, want) {
			t.Errorf("the copy = %v, want the host and the credentials", got)
		}
		task := e.nomad.Job("whoami").TaskGroups[0].Tasks[0]
		if auth, _ := task.Config["auth"].([]any); len(auth) != 1 {
			t.Errorf("auth = %v, want one block", task.Config["auth"])
		}

		// FR-002: the credentials are in no job, record, stored app or answer.
		raw := mustMarshal(t, e.nomad.Job("whoami")) + mustMarshal(t, rec)
		stored, err := e.svc.GetApp(ctx, "whoami")
		if err != nil {
			t.Fatal(err)
		}
		list, err := e.svc.Registries(ctx)
		if err != nil {
			t.Fatal(err)
		}
		raw += mustMarshal(t, stored) + mustMarshal(t, list)
		if strings.Contains(raw, registryPassword) {
			t.Error("the job, a record, the stored app or the registry list holds the secret")
		}
	})
}

// FR-008: saving or removing credentials changes the copies the apps hold, and
// nothing else: no job is registered, no deployment is made.
func TestRegistryChangesDeployNothing(t *testing.T) {
	ctx := context.Background()
	e, a := privateEnv(t)
	host := e.registry.Host()
	saveRegistry(t, e, registryPassword)
	deployHealthy(t, e, a)
	registrations, deployments := e.nomad.Registrations(), deploymentCount(t, e)
	version := *e.nomad.Job("whoami").Version

	unchanged := func(t *testing.T) {
		t.Helper()
		if e.nomad.Registrations() != registrations || deploymentCount(t, e) != deployments || *e.nomad.Job("whoami").Version != version {
			t.Errorf("a job was registered or a deployment made: registrations %d (was %d), deployments %d (was %d)",
				e.nomad.Registrations(), registrations, deploymentCount(t, e), deployments)
		}
	}

	t.Run("new credentials reach the copy", func(t *testing.T) {
		saveRegistry(t, e, "new-"+registryPassword)
		if got, want := e.nomad.Variable(copyPath), pullItems(host, "deploy", "new-"+registryPassword); !maps.Equal(got, want) {
			t.Errorf("the copy = %v, want the new credentials", got)
		}
		unchanged(t)
	})

	t.Run("removal empties the copy and the app keeps running", func(t *testing.T) {
		if err := e.svc.DeleteRegistry(ctx, host); err != nil {
			t.Fatal(err)
		}
		if got, want := e.nomad.Variable(copyPath), pullItems(host, "", ""); !maps.Equal(got, want) {
			t.Errorf("the copy = %v, want the host with empty credentials", got)
		}
		unchanged(t)
	})

	t.Run("the next deploy without credentials fails and leaves the copy as removal left it", func(t *testing.T) {
		_, err := e.svc.Deploy(ctx, a, app.TriggerManual)
		if !errors.Is(err, deploy.ErrImageNotFound) {
			t.Fatalf("Deploy() = %v, want ErrImageNotFound", err)
		}
		if got := e.nomad.Variable(copyPath); !maps.Equal(got, pullItems(host, "", "")) {
			t.Errorf("the copy = %v", got)
		}
	})
}

// A backed-up Postgres service on a private registry: its backup job's
// variable carries the pull credentials, and a restore job gets them too.
func TestPrivatePostgresBackup(t *testing.T) {
	ctx := context.Background()
	e, _ := privateEnv(t)
	host := e.registry.Host()
	saveRegistry(t, e, registryPassword)
	setDestination(t, e)
	a := db()
	a.Services[0].Image = host + "/acme/shop:v1"
	deployHealthy(t, e, a)

	v := e.nomad.Variable("nomad/jobs/whoami_web_backup")
	if v[nomadjob.RegistryUsernameEnv] != "deploy" || v[nomadjob.RegistryPasswordEnv] != registryPassword {
		t.Errorf("the backup variable = %v, want the pull credentials", v)
	}
	if raw := mustMarshal(t, e.nomad.Job("whoami_web_backup")); strings.Contains(raw, registryPassword) {
		t.Error("the backup job holds the secret")
	}

	b, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	e.nomad.FinishRun(b.JobID, true, "", "caravana-backup key=backups/caravana/whoami/web/1.dump bytes=1\n", "")
	waitFor(t, "the backup to be recorded", func() bool {
		got, err := e.store.GetBackup(ctx, "whoami", b.ID)
		return err == nil && got.Status == store.BackupSucceeded
	})
	if _, err := e.svc.Restore(ctx, "whoami", b.ID); err != nil {
		t.Fatalf("Restore() = %v", err)
	}
	if r := e.nomad.Variable("nomad/jobs/whoami_web_restore"); r[nomadjob.RegistryPasswordEnv] != registryPassword {
		t.Errorf("the restore variable = %v, want the pull credentials", r)
	}
}
