package service

import (
	"context"
	"errors"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
)

// US5, FR-014: deleting an app takes its secrets, kept ones included.
func TestDeleteAppDeletesSecrets(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withSecrets(set("API_KEY", "k1")))
	e.nomad.Script("whoami", 1, stuckRollout)
	if _, err := e.svc.Deploy(ctx, withSecrets(set("API_KEY", "k2")), app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if owner, _ := e.secrets.Kept("whoami"); owner == "" {
		t.Fatal("no kept copy during the rollout")
	}

	if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if got := e.secrets.Live("whoami"); len(got) != 0 {
		t.Errorf("live after delete = %v", got)
	}
	if owner, _ := e.secrets.Kept("whoami"); owner != "" {
		t.Errorf("kept copy after delete, owned by %s", owner)
	}

	// A new app with the same ID starts without any.
	deployHealthy(t, e, whoami())
	if got := e.secrets.Live("whoami"); len(got) != 0 {
		t.Errorf("the new whoami has secrets: %v", got)
	}
}

func TestDeleteAppKeepsTheAppWhenSecretsCantGo(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withSecrets(set("API_KEY", "k1")))
	e.secrets.SetUnavailable(true)
	if err := e.svc.DeleteApp(ctx, "whoami"); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("DeleteApp = %v, want ErrUnavailable", err)
	}
	if _, err := e.svc.GetApp(ctx, "whoami"); err != nil {
		t.Fatalf("the app is gone although its secrets weren't deleted: %v", err)
	}

	e.secrets.SetUnavailable(false)
	if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
		t.Fatalf("second DeleteApp: %v", err)
	}
	if got := e.secrets.Live("whoami"); len(got) != 0 {
		t.Errorf("live after the second delete = %v", got)
	}
}
