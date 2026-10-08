package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/store"
)

func TestExport(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	if _, _, err := e.svc.Export(ctx, "whoami", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown app: err = %v, want store.ErrNotFound", err)
	}

	first := deployHealthy(t, e, withEnv(whoami(), "a"))
	live := deployHealthy(t, e, withEnv(whoami(), "b"))

	t.Run("the live version", func(t *testing.T) {
		_, hcl, err := e.svc.Export(ctx, "whoami", "")
		if err != nil {
			t.Fatalf("Export() = %v", err)
		}
		if !strings.Contains(string(hcl), `"MODE" = "b"`) || !strings.Contains(string(hcl), live.Deployment.ID) {
			t.Errorf("the live export isn't deployment %s:\n%s", live.Deployment.ID, hcl)
		}
	})

	recordDeployment(t, e, "old", nil, store.StatusHealthy) // now the newest healthy, with no definition

	t.Run("a past deployment", func(t *testing.T) {
		name, hcl, err := e.svc.Export(ctx, "whoami", first.Deployment.ID)
		if err != nil {
			t.Fatalf("Export() = %v", err)
		}
		if name != "whoami.nomad.hcl" {
			t.Errorf("name = %q", name)
		}
		if !strings.Contains(string(hcl), `"MODE" = "a"`) || !strings.Contains(string(hcl), first.Deployment.ID) {
			t.Errorf("the export isn't deployment %s:\n%s", first.Deployment.ID, hcl)
		}
	})

	t.Run("a deployment without a definition", func(t *testing.T) {
		if _, _, err := e.svc.Export(ctx, "whoami", "old"); !errors.Is(err, ErrDefinitionNotRecorded) {
			t.Errorf("err = %v, want ErrDefinitionNotRecorded", err)
		}
	})

	t.Run("a live version without a definition", func(t *testing.T) {
		// The live export is the newest healthy deployment, even when it
		// can't be exported: it never falls back to an older one.
		if _, _, err := e.svc.Export(ctx, "whoami", ""); !errors.Is(err, ErrDefinitionNotRecorded) {
			t.Errorf("err = %v, want ErrDefinitionNotRecorded", err)
		}
	})

	t.Run("no healthy deployment", func(t *testing.T) {
		other := whoami()
		other.ID = "other"
		if _, err := e.store.SaveApp(ctx, other); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.svc.Export(ctx, "other", ""); !errors.Is(err, ErrNoLiveDeployment) {
			t.Errorf("err = %v, want ErrNoLiveDeployment", err)
		}
	})

	t.Run("another app's deployment", func(t *testing.T) {
		if _, _, err := e.svc.Export(ctx, "other", first.Deployment.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("err = %v, want store.ErrNotFound", err)
		}
	})
}
