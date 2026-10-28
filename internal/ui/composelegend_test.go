package ui

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/service"
)

const (
	keptLegendFirst = "This app was deployed from a Compose file, so Caravana keeps the file and you edit the app through it: the file, then each service's domains, health check and secrets. An app deployed from an image is edited field by field instead."
	keptLegendCI    = "Its last deploy came from CI (acme/shop). A deploy from CI can replace changes made here, so make lasting changes in the source."
)

func keptShopRequestFromFile() compose.Request {
	req := shopRequest()
	req.Compose = keptShopFile
	return req
}

// Configure of a kept-file app says why it differs from an image app's: always
// the first sentence, and the CI sentence only when the newest deployment has
// an origin with a repository. New app's Compose tab and Deploy from Compose
// say neither.
func TestConfigureKeptFileLegend(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	_, body := e.get(t, "/apps/shop/configure")
	got := text(body)
	if !strings.Contains(got, keptLegendFirst) {
		t.Errorf("the legend is missing:\n%s", got)
	}
	if strings.Contains(got, "came from CI") {
		t.Error("the CI sentence is drawn for a deploy with no origin")
	}

	// A deploy from CI: the second sentence shows.
	ctx := service.WithOrigin(context.Background(), app.Origin{Commit: "abc1234", Repository: "acme/shop"})
	rec, err := e.Svc.DeployCompose(ctx, "shop", keptShopRequestFromFile(), app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	e.waitDeploy(t, rec.Deployment.ID)
	_, body = e.get(t, "/apps/shop/configure")
	if got := text(body); !strings.Contains(got, keptLegendFirst) || !strings.Contains(got, keptLegendCI) {
		t.Errorf("the legend lacks the CI sentence:\n%s", got)
	}

	// An origin with no repository has nothing to name.
	ctx = service.WithOrigin(context.Background(), app.Origin{Commit: "abc1234"})
	rec, err = e.Svc.DeployCompose(ctx, "shop", keptShopRequestFromFile(), app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	e.waitDeploy(t, rec.Deployment.ID)
	_, body = e.get(t, "/apps/shop/configure")
	if got := text(body); strings.Contains(got, "came from CI") {
		t.Errorf("the CI sentence is drawn with no repository:\n%s", got)
	}

	// Not on New app's Compose tab, nor on Deploy from Compose.
	for _, path := range []string{"/apps/new?from=compose", "/apps/shop/compose"} {
		_, page := e.get(t, path)
		if strings.Contains(text(page), "so Caravana keeps the file") {
			t.Errorf("GET %s draws the legend", path)
		}
	}
}

// A service's secrets are rows: the name is the label of its password input,
// the keep hint is said once per service, and the add rows are a name input and
// a value input, each with a label for screen readers. No password input has a value.
func TestComposeFormSecretRows(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	_, body := e.get(t, "/apps/shop/configure")
	if n := strings.Count(body, "Leave a value empty to keep it."); n != 2 {
		t.Errorf("the keep hint is drawn %d times, want once for each of the 2 services with secrets", n)
	}
	for _, gone := range []string{"Empty keeps the current value.", `class="cv-edit-fields">
          <label class="cv-fld" for="svc-web-secret`} {
		if strings.Contains(body, gone) {
			t.Errorf("the page still has %q", gone)
		}
	}
	for _, want := range []string{
		`class="cv-secret-rows"`,
		`class="cv-secret-name cv-data text-fg-1" for="svc-web-secret-API_KEY">API_KEY</label>`,
		`class="cv-secret-label" for="svc-web-newSecret-0">Name of new secret 1</label>`,
		`class="cv-secret-label" for="svc-web-newSecret-0-value">Value of new secret 1</label>`,
		`class="cv-secret-label" for="svc-web-newSecret-2-value">Value of new secret 3</label>`,
		`class="cv-secret-row cv-secret-heads" aria-hidden="true"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %q", want)
		}
	}
	inputs := regexp.MustCompile(`<input[^>]*type="password"[^>]*>`).FindAllString(body, -1)
	if len(inputs) == 0 {
		t.Fatal("no password input on the page")
	}
	for _, in := range inputs {
		if strings.Contains(in, " value=") {
			t.Errorf("a password input has a value: %s", in)
		}
	}
	if strings.Contains(body, sentinel015) {
		t.Error("a secret value is on the page")
	}
}

// A stored secret keeps its value when left empty, so only a secret that does not exist yet asks for one.
func TestComposeFormStoredSecretAsksForNothing(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	_, body := e.get(t, "/apps/shop/configure")
	// Every existing secret keeps its value when left empty, so no row asks for one.
	if strings.Contains(body, "Type its value.") {
		t.Error("a stored secret asks for its value")
	}
}
