package service

import (
	"context"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// US3: an image from a registry named with a port and a name that isn't
// loopback, reached through the client's endpoint table. The pinned name keeps
// the host and port, and the credentials are looked up under that host.
func TestDeployFromHostWithPort(t *testing.T) {
	ctx := context.Background()
	const host = "registry.example.com:5000"
	reg, nomad := registrytest.New(t), deploytest.New(t)
	reg.RequireToken(true)
	reg.RequireLogin("deploy", registryPassword)
	reg.AddIndex("acme/shop", "v1", whoamiDigest)
	nomad.Succeed()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := startServiceWith(t, st, newDeployerAt(t, map[string]string{host: reg.URL()}, nomad), secretstest.New())

	a := whoami()
	a.Services[0].Image = "Registry.Example.COM:5000/acme/shop:v1" // as typed, capitals and all
	password := registryPassword
	if _, err := svc.SaveRegistry(ctx, host, "deploy", &password); err != nil {
		t.Fatal(err)
	}
	rec, err := svc.Deploy(ctx, a, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy() = %v", err)
	}
	if got, want := rec.Deployment.Digests["web"], host+"/acme/shop@"+whoamiDigest; got != want {
		t.Errorf("pinned image = %q, want %q (host, port and path kept; lowercase host)", got, want)
	}
	if final := waitFinal(t, st, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("final = %s (%s), want healthy", final.Status, final.Error)
	}
	// The copy and the auth block carry the canonical host, "host:port".
	if got := nomad.Variable(copyPath); got["registry"] != host || got[nomadjob.RegistryPasswordEnv] != registryPassword {
		t.Errorf("the copy = %v, want registry %s", got, host)
	}
	auth, _ := nomad.Job("whoami").TaskGroups[0].Tasks[0].Config["auth"].([]any)
	if len(auth) != 1 {
		t.Fatalf("auth = %v, want one block", auth)
	}
	if block, _ := auth[0].(map[string]any); block["server_address"] != host {
		t.Errorf("auth block = %v, want server_address %s", block, host)
	}
}
