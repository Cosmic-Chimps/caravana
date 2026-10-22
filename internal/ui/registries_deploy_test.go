package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 017, T010: a registry that turns the stored credentials down is its
// own failure in the console, with its own fix.

// refusedAttempt is the error a deploy stores when the registry refuses the
// credentials, as the deploy flow words it.
const refusedAttempt = "ghcr.io: ghcr.io/acme/shop:v1: the registry refused the stored credentials (ghcr.io answered 401)"

func TestRefusedCredentialsInTheHistory(t *testing.T) {
	e := newConsoleEnv(t)
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusFailed, Error: refusedAttempt})
	_, body := e.get(t, "/apps/whoami/deployments")
	got := text(body)

	for _, want := range []string{"This deploy didn't start: the registry refused the stored credentials.", refusedAttempt} {
		if !strings.Contains(got, want) {
			t.Errorf("the tab lacks %q:\n%s", want, got)
		}
	}
	if n := strings.Count(body, `class="cv-deploy-action"`); n != 1 {
		t.Errorf("%d actions on the entry, want 1", n)
	}
	if !strings.Contains(body, `<a href="/settings/registries" class="cv-btn cv-btn-secondary cv-btn-sm">Check the registry&#39;s credentials</a>`) {
		t.Errorf("no action to Settings > Registries:\n%s", body)
	}
	if strings.Contains(body, ">Deploy again</a>") || strings.Contains(body, ">Change the image</a>") {
		t.Errorf("the entry offers another fix too:\n%s", body)
	}
}

// The form: a deploy whose image the registry won't give with the stored
// credentials is a 422 (the typed value or the stored secret is wrong, not the
// registry down), with the sentence at the image field, the attempt in the
// history with its fix, and the secret nowhere.
func TestDeployFormRefusedCredentials(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	e.Reg.RequireToken(true)
	e.Reg.RequireLogin("deploy", "the-right-"+registrySecret)
	e.Reg.AddIndex("acme/shop", "v1", sampleDigest)
	host := e.Reg.Host()
	wrong := registrySecret
	if _, err := e.Svc.SaveRegistry(ctx, host, "deploy", &wrong); err != nil {
		t.Fatal(err)
	}
	e.seed(t, duoApp(), seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})

	resp, body := e.postForm(t, "/apps/duo/deploy", url.Values{"image.web": {host + "/acme/shop:v1"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST = %d, want 422:\n%s", resp.StatusCode, text(body))
	}
	field := body[strings.Index(body, `name="image.web"`):]
	if got := text(field[:strings.Index(field, "</label>")]); !strings.Contains(got, "refused the stored credentials") {
		t.Errorf("the image field has no error about the credentials: %s", got)
	}
	if !strings.Contains(body, `<a href="/settings/registries"`) {
		t.Errorf("the attempt in the history doesn't send the owner to the registries:\n%s", body)
	}
	if recs := e.deployments(t, "duo"); len(recs) != 2 || recs[0].Status != store.StatusFailed {
		t.Errorf("deployments = %+v, want the failed attempt recorded", recs)
	}
	leaks(t, "the refused deploy", body, e.Logs.String())
}
