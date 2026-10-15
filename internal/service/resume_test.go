package service

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// leftover sets up what a previous run left behind: the stored app and one
// deployment record in status, and (if inNomad) a Nomad job version that
// carries the deployment's ID.
func leftover(t *testing.T, status store.Status, version *uint64, inNomad bool) (*store.Store, *deploytest.Nomad, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.SaveApp(ctx, whoami(), nil); err != nil {
		t.Fatal(err)
	}
	id := "leftover"
	if _, err := st.CreateDeployment(ctx, "whoami", app.Deployment{ID: id, Trigger: app.TriggerManual, Digests: map[string]string{}}, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if status == store.StatusRunning {
		if _, err := st.UpdateDeployment(ctx, id, store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: version}); err != nil {
			t.Fatal(err)
		}
	}

	nomad := deploytest.New(t)
	nomad.Succeed()
	if inNomad {
		nomad.AddForeignJob(&api.Job{ID: new("whoami"), Name: new("whoami"),
			Meta: map[string]string{"caravana.app": "whoami", "caravana.deployment": id}})
	}
	return st, nomad, id
}

func TestResume(t *testing.T) {
	reg := func(t *testing.T) *registrytest.Server { return registrytest.New(t) }

	t.Run("running is watched again", func(t *testing.T) {
		st, nomad, id := leftover(t, store.StatusRunning, new(uint64(0)), true)
		startService(t, st, reg(t), nomad, secretstest.New())
		if final := waitFinal(t, st, id); final.Status != store.StatusHealthy {
			t.Errorf("final = %s, want healthy", final.Status)
		}
	})

	t.Run("pending that reached Nomad", func(t *testing.T) {
		st, nomad, id := leftover(t, store.StatusPending, nil, true)
		startService(t, st, reg(t), nomad, secretstest.New())
		final := waitFinal(t, st, id)
		if final.Status != store.StatusHealthy || final.JobVersion == nil || *final.JobVersion != 0 {
			t.Errorf("final = %s v%v, want healthy at version 0", final.Status, final.JobVersion)
		}
	})

	t.Run("pending that never reached Nomad", func(t *testing.T) {
		st, nomad, id := leftover(t, store.StatusPending, nil, false)
		startService(t, st, reg(t), nomad, secretstest.New())
		final := waitFinal(t, st, id)
		if final.Status != store.StatusFailed || final.Error != "Caravana stopped before registering this deployment" {
			t.Errorf("final = %s %q, want failed: stopped before registering", final.Status, final.Error)
		}
	})

	t.Run("nomad down at start", func(t *testing.T) {
		st, nomad, id := leftover(t, store.StatusRunning, new(uint64(0)), true)
		nomad.Down()
		start := time.Now()
		startService(t, st, reg(t), nomad, secretstest.New())
		if time.Since(start) > time.Second {
			t.Errorf("Start took %v; it must not wait on Nomad", time.Since(start))
		}
		time.Sleep(20 * time.Millisecond)
		if rec, _ := st.GetDeployment(context.Background(), id); rec.Status != store.StatusRunning {
			t.Errorf("status while Nomad is down = %s, want running", rec.Status)
		}
		nomad.Up()
		if final := waitFinal(t, st, id); final.Status != store.StatusHealthy {
			t.Errorf("final = %s, want healthy", final.Status)
		}
	})

	t.Run("shutdown leaves it running", func(t *testing.T) {
		st, nomad, id := leftover(t, store.StatusRunning, new(uint64(0)), true)
		nomad.Default() // no rollout ever appears
		svc := New(st, newDeployer(t, reg(t), nomad), secretstest.New(), discardLogger())
		ctx, cancel := context.WithCancel(context.Background())
		if err := svc.Start(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		cancel()
		done := make(chan struct{})
		go func() { svc.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Wait did not return after the context was cancelled")
		}
		if rec, _ := st.GetDeployment(context.Background(), id); rec.Status != store.StatusRunning {
			t.Errorf("status = %s, want running for the next start", rec.Status)
		}
	})
}
