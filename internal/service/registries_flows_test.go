package service

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The other ways into a deploy than the form (feature 017, T010): a webhook and
// going back to a version end in the same register as a deploy, so a private
// image gets its copy, and a registry that turns the credentials down gives the
// same error, with the same field.

// US1: a webhook redeploys a private image the same way, and says the same
// thing when the registry refuses the credentials.
func TestWebhookPrivateImage(t *testing.T) {
	ctx := context.Background()
	e, a := privateEnv(t)
	host := e.registry.Host()
	saveRegistry(t, e, registryPassword)
	deployHealthy(t, e, a)
	tok, err := e.svc.CreateWebhookToken(ctx, "whoami")
	if err != nil {
		t.Fatal(err)
	}

	// The tag moved, and the stored secret went wrong.
	e.registry.AddIndex("acme/shop", "v1", movedDigest)
	saveRegistry(t, e, "wrong-"+registryPassword)
	_, err = e.svc.Webhook(ctx, "whoami", tok.Token)
	var de *DeployError
	var ie *deploy.ImageError
	if !errors.Is(err, deploy.ErrRegistryAuth) || !errors.As(err, &de) || !errors.As(err, &ie) || ie.Field != "services[0].image" {
		t.Fatalf("Webhook() with a refused secret = %v, want a *DeployError matching ErrRegistryAuth on services[0].image", err)
	}
	if stored, _ := e.store.GetDeployment(ctx, de.DeploymentID); stored.Status != store.StatusFailed || strings.Contains(stored.Error, registryPassword) {
		t.Errorf("record = %s %q, want failed without the secret", stored.Status, stored.Error)
	}

	// Fixed: the redeploy goes through with its copy.
	saveRegistry(t, e, registryPassword)
	res, err := e.svc.Webhook(ctx, "whoami", tok.Token)
	if err != nil || !res.Changed {
		t.Fatalf("Webhook() = changed %v, %v; want a redeploy", res.Changed, err)
	}
	if want := host + "/acme/shop@" + movedDigest; res.Deployment.Deployment.Digests["web"] != want {
		t.Errorf("digest = %q, want %q", res.Deployment.Deployment.Digests["web"], want)
	}
	if final := waitFinal(t, e.store, res.Deployment.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("final = %s (%s), want healthy", final.Status, final.Error)
	}
	if got := e.nomad.Variable(copyPath); !maps.Equal(got, pullItems(host, "deploy", registryPassword)) {
		t.Errorf("the copy = %v", got)
	}
}

// US1: going back to a past version of a private image checks the digest with
// the stored credentials, refuses with the same error when the registry turns
// them down, and deploys with the copy when it accepts them.
func TestRedeployVersionPrivateImage(t *testing.T) {
	ctx := context.Background()
	e, a := privateEnv(t)
	host := e.registry.Host()
	saveRegistry(t, e, registryPassword)
	past := deployHealthy(t, e, withEnv(a, "a"))
	deployHealthy(t, e, withEnv(a, "b"))
	before := countDeployments(t, e)

	saveRegistry(t, e, "wrong-"+registryPassword)
	_, err := e.svc.RedeployVersion(ctx, "whoami", past.Deployment.ID, app.TriggerConsole)
	var ie *deploy.ImageError
	if !errors.Is(err, deploy.ErrRegistryAuth) || !errors.As(err, &ie) || ie.Field != "services[0].image" {
		t.Fatalf("RedeployVersion() with a refused secret = %v, want an *ImageError matching ErrRegistryAuth", err)
	}
	if countDeployments(t, e) != before {
		t.Error("a refused redeploy left a deployment behind")
	}

	saveRegistry(t, e, registryPassword)
	rec, err := e.svc.RedeployVersion(ctx, "whoami", past.Deployment.ID, app.TriggerConsole)
	if err != nil {
		t.Fatalf("RedeployVersion() = %v", err)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("final = %s (%s), want healthy", final.Status, final.Error)
	}
	if got := e.nomad.Variable(copyPath); !maps.Equal(got, pullItems(host, "deploy", registryPassword)) {
		t.Errorf("the copy = %v", got)
	}
	if _, has := e.nomad.Job("whoami").TaskGroups[0].Tasks[0].Config["auth"]; !has {
		t.Error("the redeployed job has no auth block")
	}
}
