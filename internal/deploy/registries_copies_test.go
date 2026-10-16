package deploy

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// The per-task copies of registry credentials (feature 017, T009). The
// registry's own behavior is tested in registries_test.go; here a Nomad that
// keeps variables and jobs shows what is written, and what is not.

const (
	privHost = "registry.example.com:5000"
	// privImage is the pinned image of a service pulled from privHost.
	privImage = privHost + "/acme/shop@" + whoamiDigest
	// copyPath is the pull-credentials variable of privApp's service "web".
	copyPath = "nomad/jobs/whoami/web/web"
)

var privHosts = map[string]bool{privHost: true}

// privApp is whoami with its image on privHost, and the deployment that pins it.
func privApp() (app.App, app.Deployment) {
	a := whoami()
	a.Services[0].Image = privHost + "/acme/shop:v1"
	return a, app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{"web": privImage}}
}

// jobJSON is the registered job as Nomad stores it, to search for secrets.
func jobJSON(t *testing.T, raw any) string {
	t.Helper()
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRegisterPrivateImage(t *testing.T) {
	ctx := context.Background()

	t.Run("a deploy writes the copy and the job has the placeholders", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		a, dep := privApp()
		if err := d.SaveRegistrySecret(ctx, privHost, "deploy", registrySecret); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Register(ctx, a, dep, ownsIDs(), privHosts); err != nil {
			t.Fatalf("Register() = %v", err)
		}

		want := map[string]string{
			"registry":                   privHost,
			nomadjob.RegistryUsernameEnv: "deploy",
			nomadjob.RegistryPasswordEnv: registrySecret,
		}
		if got := nomad.Variable(copyPath); !maps.Equal(got, want) {
			t.Errorf("the copy at %s = %v, want the host and both credentials", copyPath, got)
		}

		task := nomad.Job("whoami").TaskGroups[0].Tasks[0]
		auth, _ := task.Config["auth"].([]any)
		if len(auth) != 1 {
			t.Fatalf("auth = %v, want one block", task.Config["auth"])
		}
		block, _ := auth[0].(map[string]any)
		if block["username"] != "${CARAVANA_REGISTRY_USERNAME}" || block["password"] != "${CARAVANA_REGISTRY_PASSWORD}" || block["server_address"] != privHost {
			t.Errorf("auth block = %v, want the placeholders and the host", block)
		}
		if len(task.Templates) != 1 || task.Templates[0].DestPath == nil || *task.Templates[0].DestPath != "secrets/registry.env" {
			t.Errorf("templates = %+v, want the one that renders secrets/registry.env", task.Templates)
		}

		// FR-002: nothing the job holds is a credential.
		if raw := jobJSON(t, nomad.Job("whoami")); strings.Contains(raw, registrySecret) || strings.Contains(raw, `"deploy"`) {
			t.Errorf("the registered job holds a credential:\n%s", raw)
		}
	})

	t.Run("two services on different registries each get their own copy", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		a, dep := privApp()
		a.Services = append(a.Services, app.Service{Name: "worker", Image: "traefik/whoami:latest"})
		dep.Digests["worker"] = "traefik/whoami@" + whoamiDigest // public
		if err := d.SaveRegistrySecret(ctx, privHost, "deploy", registrySecret); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Register(ctx, a, dep, ownsIDs(), privHosts); err != nil {
			t.Fatalf("Register() = %v", err)
		}
		if got := nomad.VariablePaths(); len(got) != 2 || got[0] != "caravana/registries/registry~example~com_5000" || got[1] != copyPath {
			t.Errorf("variables = %v, want the stored credentials and web's copy, none for the public worker", got)
		}
	})

	t.Run("a public app gets no copy and no auth", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if _, err := d.Register(ctx, whoami(), deployment("d1"), ownsIDs(), privHosts); err != nil {
			t.Fatalf("Register() = %v", err)
		}
		if got := nomad.VariablePaths(); len(got) != 0 {
			t.Errorf("variables = %v, want none", got)
		}
		task := nomad.Job("whoami").TaskGroups[0].Tasks[0]
		if _, has := task.Config["auth"]; has || len(task.Templates) != 0 {
			t.Errorf("a public task has auth %v and templates %v, want neither", task.Config["auth"], task.Templates)
		}
	})

	t.Run("credentials gone between the read and the register leave the copy empty", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		a, dep := privApp()
		if _, err := d.Register(ctx, a, dep, ownsIDs(), privHosts); err != nil {
			t.Fatalf("Register() = %v", err)
		}
		want := map[string]string{"registry": privHost, nomadjob.RegistryUsernameEnv: "", nomadjob.RegistryPasswordEnv: ""}
		if got := nomad.Variable(copyPath); !maps.Equal(got, want) {
			t.Errorf("the copy = %v, want the host with both credentials empty (Nomad refuses an empty variable)", got)
		}
	})

	t.Run("an unreadable credential store fails the deploy before the job", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		a, dep := privApp()
		nomad.FailVariables(403)
		if _, err := d.Register(ctx, a, dep, ownsIDs(), privHosts); err == nil {
			t.Fatal("Register() = nil, want an error")
		}
		if nomad.Registrations() != 0 {
			t.Error("a job was registered although its credentials couldn't be written")
		}
	})
}

func TestSyncRegistryCopies(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a, dep := privApp()
	if err := d.SaveRegistrySecret(ctx, privHost, "deploy", registrySecret); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Register(ctx, a, dep, ownsIDs(), privHosts); err != nil {
		t.Fatal(err)
	}
	// Another registry's copy, and the other kinds of variable an app has, which a
	// sync must leave alone.
	other := map[string]string{"registry": "ghcr.io", nomadjob.RegistryUsernameEnv: "bot", nomadjob.RegistryPasswordEnv: "ghp"}
	nomad.SetVariable("nomad/jobs/other/web/web", other)
	nomad.SetVariable("nomad/jobs/whoami/web", map[string]string{"TOKEN": "x"})
	nomad.SetVariable("nomad/jobs/whoami/web/caravana-volumes", map[string]string{"LIMIT": "1800"})
	registrations, version := nomad.Registrations(), *nomad.Job("whoami").Version

	t.Run("new credentials reach the copy and nothing is deployed", func(t *testing.T) {
		if err := d.SaveRegistrySecret(ctx, privHost, "deploy2", "new-"+registrySecret); err != nil {
			t.Fatal(err)
		}
		if err := d.SyncRegistryCopies(ctx, privHost); err != nil {
			t.Fatalf("SyncRegistryCopies() = %v", err)
		}
		want := map[string]string{"registry": privHost, nomadjob.RegistryUsernameEnv: "deploy2", nomadjob.RegistryPasswordEnv: "new-" + registrySecret}
		if got := nomad.Variable(copyPath); !maps.Equal(got, want) {
			t.Errorf("the copy = %v, want the new credentials", got)
		}
		if nomad.Registrations() != registrations || *nomad.Job("whoami").Version != version {
			t.Errorf("a sync registered a job (%d registrations, version %d; were %d, %d): it must touch variables only",
				nomad.Registrations(), *nomad.Job("whoami").Version, registrations, version)
		}
		if !maps.Equal(nomad.Variable("nomad/jobs/other/web/web"), other) {
			t.Error("another registry's copy was rewritten")
		}
		if nomad.Variable("nomad/jobs/whoami/web")["TOKEN"] != "x" || nomad.Variable("nomad/jobs/whoami/web/caravana-volumes")["LIMIT"] != "1800" {
			t.Error("a secrets or volumes variable was rewritten")
		}
	})

	t.Run("removal empties the credentials and keeps the variable", func(t *testing.T) {
		if err := d.DeleteRegistrySecret(ctx, privHost); err != nil {
			t.Fatal(err)
		}
		if err := d.SyncRegistryCopies(ctx, privHost); err != nil {
			t.Fatalf("SyncRegistryCopies() = %v", err)
		}
		want := map[string]string{"registry": privHost, nomadjob.RegistryUsernameEnv: "", nomadjob.RegistryPasswordEnv: ""}
		if got := nomad.Variable(copyPath); !maps.Equal(got, want) {
			t.Errorf("the copy = %v, want the host and empty credentials (the template must still find a variable)", got)
		}
		if nomad.Registrations() != registrations {
			t.Error("a sync registered a job")
		}
		if raw := jobJSON(t, nomad.Job("whoami")); strings.Contains(raw, "new-"+registrySecret) {
			t.Error("the job holds a credential")
		}
	})

	t.Run("a registry with no copies is nothing to do", func(t *testing.T) {
		if err := d.SyncRegistryCopies(ctx, "quay.io"); err != nil {
			t.Errorf("SyncRegistryCopies() = %v", err)
		}
	})

	t.Run("an unreadable store is an error", func(t *testing.T) {
		nomad.FailVariables(403)
		defer nomad.FailVariables(0)
		if err := d.SyncRegistryCopies(ctx, privHost); err == nil {
			t.Error("SyncRegistryCopies() = nil, want an error")
		}
	})
}

// privBackedUp is a Postgres service on privHost with a backup policy.
func privBackedUp() (app.App, map[string]string) {
	a := backedUp()
	a.Services[0].Image = privHost + "/acme/pg:17"
	return a, map[string]string{"db": privHost + "/acme/pg@" + whoamiDigest}
}

func TestPrivateBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a, digests := privBackedUp()
	creds := map[string]BackupCredentials{"db": {User: "postgres", Password: "pw", Database: "postgres"}}
	if err := d.SaveRegistrySecret(ctx, privHost, "deploy", registrySecret); err != nil {
		t.Fatal(err)
	}

	if err := d.SyncBackups(ctx, a, digests, creds, testDest, privHosts); err != nil {
		t.Fatalf("SyncBackups() = %v", err)
	}
	v := nomad.Variable("nomad/jobs/shop_db_backup")
	if v[nomadjob.RegistryUsernameEnv] != "deploy" || v[nomadjob.RegistryPasswordEnv] != registrySecret || v["PGPASSWORD"] != "pw" {
		t.Errorf("the backup variable = %v, want the pull credentials next to the database's", v)
	}
	dump := nomad.Job("shop_db_backup").TaskGroups[0].Tasks[0]
	if auth, _ := dump.Config["auth"].([]any); len(auth) != 1 {
		t.Errorf("the dump task has auth %v, want one block", dump.Config["auth"])
	}
	if raw := jobJSON(t, nomad.Job("shop_db_backup")); strings.Contains(raw, registrySecret) {
		t.Errorf("the backup job holds a credential:\n%s", raw)
	}

	// The restore job reads its own copy, with the pull credentials of the
	// image it restores with.
	if err := d.Restore(ctx, "shop", a.Services[0], digests["db"], "backups/1.dump", privHosts); err != nil {
		t.Fatalf("Restore() = %v", err)
	}
	r := nomad.Variable("nomad/jobs/shop_db_restore")
	if r[nomadjob.RegistryUsernameEnv] != "deploy" || r[nomadjob.RegistryPasswordEnv] != registrySecret || r["PGPASSWORD"] != "pw" {
		t.Errorf("the restore variable = %v", r)
	}
	if auth, _ := nomad.Job("shop_db_restore").TaskGroups[0].Tasks[1].Config["auth"].([]any); len(auth) != 1 {
		t.Error("the restore task has no auth block")
	}

	t.Run("a sync rewrites both and registers nothing", func(t *testing.T) {
		registrations := nomad.Registrations()
		if err := d.SaveRegistrySecret(ctx, privHost, "deploy", "new-"+registrySecret); err != nil {
			t.Fatal(err)
		}
		if err := d.SyncRegistryCopies(ctx, privHost); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"nomad/jobs/shop_db_backup", "nomad/jobs/shop_db_restore"} {
			got := nomad.Variable(path)
			if got[nomadjob.RegistryPasswordEnv] != "new-"+registrySecret || got["PGPASSWORD"] != "pw" || got["BACKUP_PATH"] == "" && path == "nomad/jobs/shop_db_backup" {
				t.Errorf("%s = %v, want the new pull credentials and everything else as it was", path, got)
			}
		}
		if nomad.Registrations() != registrations {
			t.Error("a sync registered a job")
		}
	})

	t.Run("removal empties them", func(t *testing.T) {
		if err := d.DeleteRegistrySecret(ctx, privHost); err != nil {
			t.Fatal(err)
		}
		if err := d.SyncRegistryCopies(ctx, privHost); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"nomad/jobs/shop_db_backup", "nomad/jobs/shop_db_restore"} {
			got := nomad.Variable(path)
			if got[nomadjob.RegistryUsernameEnv] != "" || got[nomadjob.RegistryPasswordEnv] != "" || got["PGPASSWORD"] != "pw" {
				t.Errorf("%s = %v, want empty pull credentials and the database's kept", path, got)
			}
		}
	})

	t.Run("a destination change keeps the pull credentials", func(t *testing.T) {
		if err := d.SaveRegistrySecret(ctx, privHost, "deploy", registrySecret); err != nil {
			t.Fatal(err)
		}
		if err := d.SyncBackups(ctx, a, digests, creds, testDest, privHosts); err != nil {
			t.Fatal(err)
		}
		moved := testDest
		moved.Bucket = "other"
		if err := d.SetBackupDestination(ctx, moved); err != nil {
			t.Fatal(err)
		}
		if v := nomad.Variable("nomad/jobs/shop_db_backup"); v[nomadjob.RegistryPasswordEnv] != registrySecret || v["BACKUP_PATH"] != "other/caravana/shop/db" {
			t.Errorf("the backup variable = %v", v)
		}
	})

	t.Run("a service that stopped being private loses them", func(t *testing.T) {
		if err := d.SyncBackups(ctx, a, digests, creds, testDest, nil); err != nil {
			t.Fatal(err)
		}
		v := nomad.Variable("nomad/jobs/shop_db_backup")
		if _, has := v[nomadjob.RegistryUsernameEnv]; has || v["PGPASSWORD"] != "pw" {
			t.Errorf("the backup variable = %v, want no pull credentials", v)
		}
		if _, has := nomad.Job("shop_db_backup").TaskGroups[0].Tasks[0].Config["auth"]; has {
			t.Error("the dump task still has its auth block")
		}
	})
}

func TestPublicBackupHasNoPullCredentials(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	a := backedUp()
	digests := map[string]string{"db": "postgres@" + whoamiDigest}
	if err := d.SyncBackups(ctx, a, digests, map[string]BackupCredentials{"db": {User: "postgres", Password: "pw", Database: "postgres"}}, testDest, privHosts); err != nil {
		t.Fatal(err)
	}
	v := nomad.Variable("nomad/jobs/shop_db_backup")
	_, hasUser := v[nomadjob.RegistryUsernameEnv]
	_, hasPass := v[nomadjob.RegistryPasswordEnv]
	if hasUser || hasPass || v["PGPASSWORD"] != "pw" {
		t.Errorf("the backup variable of a public image = %v, want the database's items and no pull credentials", v)
	}
}
