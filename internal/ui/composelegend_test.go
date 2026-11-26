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
	keptLegendFirst = "This app is edited through its Compose file. Change the file, preview what changes, then deploy. Health checks are on Services, secrets on Variables & secrets, domains on Domains and backups on the Backups tab: a deploy from here keeps them as they are."
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
	_, body := e.get(t, "/apps/shop/configure/source")
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
	_, body = e.get(t, "/apps/shop/configure/source")
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
	_, body = e.get(t, "/apps/shop/configure/source")
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

// The form of an app that exists has no secret rows (feature 059): its secrets are kept by name and
// set on Variables & secrets. New app's Compose tab keeps them: a service's secrets are rows, the
// name is the label of its password input, and the add rows are a name input and a value input,
// each with a label for screen readers. No password input has a value.
func TestComposeFormSecretRows(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	_, source := e.get(t, "/apps/shop/configure/source")
	if strings.Contains(source, `type="password"`) || strings.Contains(source, "cv-secret-rows") || strings.Contains(source, "Leave a value empty to keep it.") {
		t.Error("the form of an app that exists draws secret rows")
	}
	if strings.Contains(source, sentinel015) {
		t.Error("a secret value is on the page")
	}

	ne := composeEnv(t)
	_, body := ne.previewNew(t, "fresh", shopCompose)
	for _, want := range []string{
		`class="cv-secret-rows"`,
		`class="cv-secret-label" for="svc-web-newSecret-0">Name of new secret 1</label>`,
		`class="cv-secret-label" for="svc-web-newSecret-0-value">Value of new secret 1</label>`,
		`class="cv-secret-label" for="svc-web-newSecret-2-value">Value of new secret 3</label>`,
		`class="cv-secret-row cv-secret-heads" aria-hidden="true"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the new app's form lacks %q", want)
		}
	}
	inputs := regexp.MustCompile(`<input[^>]*type="password"[^>]*>`).FindAllString(body, -1)
	if len(inputs) == 0 {
		t.Fatal("no password input on the new app's page")
	}
	for _, in := range inputs {
		if strings.Contains(in, " value=") {
			t.Errorf("a password input has a value: %s", in)
		}
	}
}

// A stored secret keeps its value when left empty, so only a secret that does not exist yet asks for one.
func TestComposeFormStoredSecretAsksForNothing(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	_, body := e.get(t, "/apps/shop/configure/source")
	// Every existing secret keeps its value when left empty, so no row asks for one.
	if strings.Contains(body, "Type its value.") {
		t.Error("a stored secret asks for its value")
	}
}
