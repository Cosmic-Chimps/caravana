package ui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The Deployments tab's Deploy form changes only images, so an app made from a Compose file
// keeps its file: the new image is written into it and Configure still shows the Compose
// form, not the "paste it once" notice.
func TestDeployFormImageKeepsComposeFile(t *testing.T) {
	e, _ := keptSentinelEnv(t)
	e.Reg.AddIndex("traefik/whoami", "v1.10", otherDigest)

	resp, body := e.postForm(t, "/apps/shop/deploy", url.Values{
		"image.web":    {"traefik/whoami:v1.10"},
		"image.worker": {"traefik/whoami:latest"},
		"image.db":     {"postgres:17"},
	})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/shop/deployments" {
		t.Fatalf("POST = %d to %q, want 303 to /apps/shop/deployments:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	recs := e.deployments(t, "shop")
	e.waitDeploy(t, recs[0].Deployment.ID)

	_, page := e.get(t, "/apps/shop/configure/source")
	got := text(page)
	if strings.Contains(got, "Paste it once") || strings.Contains(got, "wasn't kept") {
		t.Errorf("Configure asks for the file after an images-only deploy:\n%s", got)
	}
	f := formWith(t, page, "/apps/shop/compose/preview")
	want := strings.Replace(keptShopFile, "traefik/whoami:latest", "traefik/whoami:v1.10", 1)
	if got := f.Values.Get("compose"); got != want {
		t.Errorf("the kept file = %q, want the old text with web's image replaced:\n%q", got, want)
	}
	if !strings.Contains(got, "This app is edited through its Compose file.") {
		t.Errorf("the definition should still be read-only:\n%s", got)
	}
	if strings.Contains(page, sentinel015) {
		t.Error("a secret value is on the Configure page")
	}

	_, history := e.get(t, "/apps/shop/deployments")
	if strings.Contains(text(entry(t, history, recs[0].Deployment.ID)), "Compose file was dropped") {
		t.Errorf("the history entry says the file was dropped:\n%s", text(entry(t, history, recs[0].Deployment.ID)))
	}
}
