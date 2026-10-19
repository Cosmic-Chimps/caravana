package service

import (
	"context"
	"reflect"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

var ciOrigin = app.Origin{
	Commit: "abc1234def", Ref: "refs/heads/main", Actor: "ana",
	Run: "https://github.com/acme/shop/actions/runs/212", Repository: "acme/shop",
}

// originOf reads a deployment's recorded origin from the store.
func originOf(t *testing.T, e *env, id string) *app.Origin {
	t.Helper()
	rec, err := e.store.GetDeployment(context.Background(), id)
	if err != nil {
		t.Fatalf("GetDeployment(%s): %v", id, err)
	}
	return rec.Deployment.Origin
}

func TestDeployRecordsOrigin(t *testing.T) {
	e := newEnv(t)
	ctx := WithOrigin(context.Background(), ciOrigin)

	// A deploy, with and without the origin in the context.
	rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if got := originOf(t, e, rec.Deployment.ID); got == nil || !reflect.DeepEqual(*got, ciOrigin) {
		t.Errorf("origin of a deploy = %+v, want %+v", got, ciOrigin)
	}
	if rec.Deployment.Origin == nil || rec.Deployment.Trigger != app.TriggerManual {
		t.Errorf("the returned record lacks the origin or lost its trigger: %+v", rec.Deployment)
	}
	waitFinal(t, e.store, rec.Deployment.ID)

	plain, err := e.svc.Deploy(context.Background(), whoami(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if got := originOf(t, e, plain.Deployment.ID); got != nil {
		t.Errorf("origin of a deploy without one = %+v, want none", got)
	}
	waitFinal(t, e.store, plain.Deployment.ID)

	// An empty origin is no origin.
	empty, err := e.svc.Deploy(WithOrigin(context.Background(), app.Origin{Ref: "  "}), whoami(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if got := originOf(t, e, empty.Deployment.ID); got != nil {
		t.Errorf("origin of a deploy with a blank one = %+v, want none", got)
	}
	waitFinal(t, e.store, empty.Deployment.ID)

	// The webhook's redeploy (the tag moved, so it deploys) records it too, and
	// the trigger stays webhook.
	e.registry.AddIndex("traefik/whoami", "latest", movedDigest)
	res, err := e.svc.WebhookRedeploy(ctx, "whoami")
	if err != nil || !res.Changed {
		t.Fatalf("WebhookRedeploy = %+v, %v; want a new deployment", res, err)
	}
	if got := originOf(t, e, res.Deployment.Deployment.ID); got == nil || got.Commit != "abc1234def" {
		t.Errorf("origin of a webhook deploy = %+v", got)
	}
	if res.Deployment.Deployment.Trigger != app.TriggerWebhook {
		t.Errorf("trigger = %s, want webhook", res.Deployment.Deployment.Trigger)
	}
}

func TestDeployComposeRecordsOrigin(t *testing.T) {
	e := newEnv(t)
	ctx := WithOrigin(context.Background(), ciOrigin)
	req := compose.Request{Compose: "services:\n  web:\n    image: traefik/whoami:latest\n"}
	rec, err := e.svc.DeployCompose(ctx, "blog", req, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	if got := originOf(t, e, rec.Deployment.ID); got == nil || got.Actor != "ana" {
		t.Errorf("origin of a Compose deploy = %+v", got)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
}

func TestPromoteRecordsOrigin(t *testing.T) {
	e := shopEnv(t)
	both(t, e)
	ctx := WithOrigin(context.Background(), ciOrigin)
	rec, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got := originOf(t, e, rec.Deployment.ID); got == nil || got.Run != ciOrigin.Run {
		t.Errorf("origin of a promotion = %+v", got)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
}

func TestDeployEventCarriesOrigin(t *testing.T) {
	rec := store.DeploymentRecord{
		Deployment: app.Deployment{ID: "dep1", Trigger: app.TriggerManual, Origin: &ciOrigin},
		AppID:      "whoami", Status: store.StatusHealthy,
	}
	ev, ok := deployEvent(rec, "https://caravana.example.com")
	if !ok || ev.Origin == nil || ev.Origin.Commit != "abc1234def" || ev.Origin.Run != ciOrigin.Run || ev.Origin.Repository != "acme/shop" {
		t.Errorf("event origin = %+v (ok %v), want the deployment's", ev.Origin, ok)
	}
	rec.Deployment.Origin = nil
	if ev, _ := deployEvent(rec, ""); ev.Origin != nil {
		t.Errorf("event origin = %+v, want none", ev.Origin)
	}
}
