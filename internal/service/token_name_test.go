package service

import (
	"context"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// A token's name in the context is recorded by every deploy entry point, the
// way the origin is, and a context without one records none.
func TestDeployRecordsTokenName(t *testing.T) {
	e := newEnv(t)
	ctx := WithTokenName(context.Background(), "ci-github")

	rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if rec.Deployment.TokenName != "ci-github" {
		t.Errorf("returned record token name = %q, want ci-github", rec.Deployment.TokenName)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	stored, err := e.store.GetDeployment(context.Background(), rec.Deployment.ID)
	if err != nil || stored.Deployment.TokenName != "ci-github" {
		t.Errorf("stored token name = %q, %v", stored.Deployment.TokenName, err)
	}

	// A redeploy of the stored app and of a past version.
	again, err := e.svc.Redeploy(ctx, "whoami", app.TriggerManual)
	if err != nil || again.Deployment.TokenName != "ci-github" {
		t.Fatalf("Redeploy = %q, %v; want the token name", again.Deployment.TokenName, err)
	}
	waitFinal(t, e.store, again.Deployment.ID)
	past, err := e.svc.RedeployVersion(ctx, "whoami", rec.Deployment.ID, app.TriggerManual)
	if err != nil || past.Deployment.TokenName != "ci-github" {
		t.Fatalf("RedeployVersion = %q, %v; want the token name", past.Deployment.TokenName, err)
	}
	waitFinal(t, e.store, past.Deployment.ID)

	req := compose.Request{Compose: "services:\n  web:\n    image: traefik/whoami:latest\n"}
	c, err := e.svc.DeployCompose(ctx, "blog", req, app.TriggerManual)
	if err != nil || c.Deployment.TokenName != "ci-github" {
		t.Fatalf("DeployCompose = %q, %v; want the token name", c.Deployment.TokenName, err)
	}
	waitFinal(t, e.store, c.Deployment.ID)

	plain, err := e.svc.Deploy(context.Background(), whoami(), app.TriggerManual)
	if err != nil || plain.Deployment.TokenName != "" {
		t.Errorf("Deploy without a token name = %q, %v; want none", plain.Deployment.TokenName, err)
	}
	waitFinal(t, e.store, plain.Deployment.ID)
	if got := WithTokenName(context.Background(), ""); tokenNameFrom(got) != "" {
		t.Error("an empty name must change nothing")
	}
}

func TestPromoteRecordsTokenName(t *testing.T) {
	e := shopEnv(t)
	both(t, e)
	ctx := WithTokenName(context.Background(), "ci-github")
	rec, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if rec.Deployment.TokenName != "ci-github" {
		t.Errorf("token name of a promotion = %q", rec.Deployment.TokenName)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
}
