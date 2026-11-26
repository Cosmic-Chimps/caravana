package service

import (
	"context"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// newTag is a second tag the fake registry knows for traefik/whoami, for the deploys that
// change only an image.
const newTag = "traefik/whoami:v1.10"

// keptFileApp deploys text as a Compose file for app "whoami" and returns the stored
// record, once healthy. The registry also knows newTag.
func keptFileApp(t *testing.T, e *env, text string) store.AppRecord {
	t.Helper()
	e.registry.AddIndex("traefik/whoami", "v1.10", whoamiDigest)
	composeHealthy(t, e, compose.Request{Compose: text})
	stored, err := e.svc.GetApp(context.Background(), "whoami")
	if err != nil || stored.Compose == nil {
		t.Fatalf("GetApp() = %+v, %v; want the app to keep its file", stored, err)
	}
	return stored
}

// withImage is the stored app with its first service's image set, sharing nothing with it.
func withImage(a app.App, image string) app.App {
	a.Services = append([]app.Service(nil), a.Services...)
	a.Services[0].Image = image
	return a
}

// An images-only deploy of an app that keeps its Compose file writes the new image into the
// file, byte for byte otherwise, and keeps it: no drop warning, the deployment records it.
func TestImagesOnlyDeployKeepsFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	stored := keptFileApp(t, e, keptFile)

	rec, err := e.svc.Deploy(ctx, withImage(stored.App, newTag), app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy() = %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)

	if n := droppedFile(rec); n != 0 {
		t.Errorf("warnings = %+v, want no drop warning", rec.Warnings)
	}
	want := strings.Replace(keptFile, "traefik/whoami:latest", newTag, 1)
	after, err := e.svc.GetApp(ctx, "whoami")
	if err != nil || after.Compose == nil {
		t.Fatalf("GetApp() = %+v, %v; want the file kept", after, err)
	}
	if after.Compose.Compose != want {
		t.Errorf("kept file = %q, want %q", after.Compose.Compose, want)
	}
	if after.App.Services[0].Image != newTag {
		t.Errorf("stored image = %q, want %q", after.App.Services[0].Image, newTag)
	}
	dep, _ := e.svc.GetDeployment(ctx, "whoami", rec.Deployment.ID)
	if dep.Compose == nil || dep.Compose.Compose != want {
		t.Errorf("the deployment records file %+v, want the new text", dep.Compose)
	}
	// What Configure reads back is the new file: translated, it is the deployed app.
	got, _, err := e.svc.PreviewCompose(ctx, "whoami", *after.Compose)
	if err != nil || got.Services[0].Image != newTag {
		t.Errorf("PreviewCompose(kept file) = %+v, %v; want the new image", got, err)
	}
}

// The same with a condition: DeployIf at the stored revision keeps the file too.
func TestImagesOnlyDeployIfKeepsFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	stored := keptFileApp(t, e, keptFile)

	rec, err := e.svc.DeployIf(ctx, withImage(stored.App, newTag), Condition{IfRevision: stored.Revision}, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployIf() = %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if n := droppedFile(rec); n != 0 {
		t.Errorf("warnings = %+v, want no drop warning", rec.Warnings)
	}
	after, _ := e.svc.GetApp(ctx, "whoami")
	if after.Compose == nil || !strings.Contains(after.Compose.Compose, "image: "+newTag+"   # pinned later") {
		t.Errorf("kept file = %+v, want the new image with its comment", after.Compose)
	}
}

// An image the file takes from a variable can't be rewritten: the file is dropped, with the
// warning, as for any definition deploy.
func TestImagesOnlyDeployWithVariableImageDrops(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	stored := keptFileApp(t, e, "services:\n  web:\n    image: ${IMAGE:-traefik/whoami:latest}\n")

	rec, err := e.svc.Deploy(ctx, withImage(stored.App, newTag), app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy() = %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if n := droppedFile(rec); n != 1 {
		t.Errorf("warnings = %+v, want the drop warning once", rec.Warnings)
	}
	if after, _ := e.svc.GetApp(ctx, "whoami"); after.Compose != nil {
		t.Errorf("kept file = %+v, want none", after.Compose)
	}
}

// A change beyond images (an env value, even with a new image) drops the file as before.
func TestDeployBeyondImagesDropsFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	stored := keptFileApp(t, e, keptFile)

	next := withImage(stored.App, newTag)
	next.Services[0].Env = []app.EnvVar{{Name: "MODE", Value: "x"}}
	rec, err := e.svc.Deploy(ctx, next, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy() = %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if n := droppedFile(rec); n != 1 {
		t.Errorf("warnings = %+v, want the drop warning once", rec.Warnings)
	}
	if after, _ := e.svc.GetApp(ctx, "whoami"); after.Compose != nil {
		t.Errorf("kept file = %+v, want none", after.Compose)
	}
}
