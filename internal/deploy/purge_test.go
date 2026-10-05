package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/nomad/api"
)

func TestPurge(t *testing.T) {
	ctx := context.Background()

	t.Run("a registered job is purged with its copies", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Succeed()
		a := whoami()
		if _, err := d.Register(ctx, a, deployment("d1"), ownsIDs()); err != nil {
			t.Fatal(err)
		}
		nomad.SetCopies(a.ID, api.AllocationListStub{ClientStatus: "running"})

		if err := d.Purge(ctx, a.ID, ownsIDs("d1")); err != nil {
			t.Fatalf("Purge = %v", err)
		}
		if !nomad.Purged(a.ID) {
			t.Error("Nomad never saw the purge")
		}
		if nomad.Job(a.ID) != nil {
			t.Error("the job is still there")
		}
		if copies, err := d.Copies(ctx, a.ID); err != nil || len(copies) != 0 {
			t.Errorf("Copies after purge = %v, %v; want none", copies, err)
		}
	})

	t.Run("a job Caravana didn't register is left alone", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddForeignJob(labJob(false))
		if err := d.Purge(ctx, "whoami", ownsIDs("d1")); err != nil {
			t.Fatalf("Purge = %v, want nil", err)
		}
		if nomad.Purged("whoami") || nomad.Job("whoami") == nil {
			t.Error("a job Caravana didn't create was purged")
		}
	})

	t.Run("a job Nomad doesn't have is not an error", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		if err := d.Purge(ctx, "ghost", ownsIDs()); err != nil {
			t.Errorf("Purge of an unknown job = %v, want nil", err)
		}
		if nomad.Purged("ghost") {
			t.Error("a purge was recorded for a job that never existed")
		}
	})

	t.Run("Nomad down is ErrNomadUnavailable", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Down()
		err := d.Purge(ctx, "whoami", ownsIDs())
		if !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("Purge with Nomad down = %v, want ErrNomadUnavailable", err)
		}
	})

	t.Run("a refused token is ErrNomadUnavailable", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.RequireToken("secret")
		err := d.Purge(ctx, "whoami", ownsIDs())
		if !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("Purge with a wrong token = %v, want ErrNomadUnavailable", err)
		}
	})
}
