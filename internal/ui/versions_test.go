package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// versionsEnv has whoami, whose stored app has a secret TOKEN, with four
// deployments: v1 healthy with a definition (no TOKEN), v2 failed with one,
// v3 healthy from before definitions were saved, and v4, the redeploy of v1.
func versionsEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	past := whoamiApp()
	current := whoamiApp()
	current.Services[0].Secrets = []app.Secret{{Name: "TOKEN"}}
	e.seed(t, current,
		seedDeploy{ID: "d1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1, Definition: &past},
		seedDeploy{ID: "d2", Trigger: app.TriggerManual, Status: store.StatusFailed, Version: 2, Definition: &current},
		seedDeploy{ID: "d3", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 3},
		seedDeploy{ID: "d4", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 4, Definition: &current, Source: "d1"})
	e.Nomad.SetVariable("nomad/jobs/whoami/web", map[string]string{"TOKEN": "t"})
	return e
}

// entry is the HTML of one history entry.
func entry(t *testing.T, body, id string) string {
	t.Helper()
	start := strings.Index(body, `<article id="deployment-`+id+`"`)
	if start < 0 {
		t.Fatalf("no entry %s in the page", id)
	}
	end := strings.Index(body[start:], "</article>")
	return body[start : start+end]
}

// US1, US3: each entry offers what its deployment allows.
func TestHistoryOffersPastVersions(t *testing.T) {
	e := versionsEnv(t)
	resp, body := e.get(t, "/apps/whoami/deployments")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", resp.StatusCode)
	}

	d1 := entry(t, body, "d1")
	for _, want := range []string{
		`<form method="post" action="/apps/whoami/deployments/d1/redeploy"`,
		`href="/apps/whoami/deployments/d1/jobspec"`,
	} {
		if !strings.Contains(d1, want) {
			t.Errorf("v1 lacks %q", want)
		}
	}
	if got := text(d1); !strings.Contains(got, `no longer has secret "TOKEN"`) || !strings.Contains(got, "looks the image tags up again") {
		t.Errorf("v1's confirmation doesn't name TOKEN or the webhook note:\n%s", got)
	}

	d2 := entry(t, body, "d2")
	if strings.Contains(d2, "/redeploy") || !strings.Contains(d2, "/deployments/d2/jobspec") {
		t.Errorf("a failed deployment offers export only:\n%s", d2)
	}
	if d3 := entry(t, body, "d3"); strings.Contains(d3, "/redeploy") || strings.Contains(d3, "/jobspec") {
		t.Errorf("a deployment from before definitions offers neither:\n%s", d3)
	}
	if d4 := entry(t, body, "d4"); !strings.Contains(text(d4), "Redeploy of d1") || strings.Contains(text(d4), "no longer has secret") {
		t.Errorf("v4 should say where it came from and remove nothing:\n%s", text(d4))
	}
}

// US1: the console redeploys a past version and goes back to the history.
func TestRedeployFromTheConsole(t *testing.T) {
	e := versionsEnv(t)
	resp, body := e.postForm(t, "/apps/whoami/deployments/d1/redeploy", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/whoami/deployments" {
		t.Fatalf("POST = %d to %q, want 303 to deployments:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	recs := e.deployments(t, "whoami")
	if newest := recs[0]; newest.Source != "d1" || newest.Deployment.Trigger != app.TriggerConsole {
		t.Errorf("newest = source %q from %s, want a console redeploy of d1", newest.Source, newest.Deployment.Trigger)
	}
	if secrets := e.storedApp(t, "whoami").Services[0].Secrets; len(secrets) != 0 {
		t.Errorf("stored secrets = %+v, want v1's (none)", secrets)
	}
}

// US1: a refusal is shown in the entry's confirmation, opened again.
func TestRedeployRefusedInTheConsole(t *testing.T) {
	e := versionsEnv(t)
	resp, body := e.postForm(t, "/apps/whoami/deployments/d2/redeploy", url.Values{})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "Only a healthy deployment can be redeployed.") {
		t.Fatalf("POST = %d, want 409 saying why at the top:\n%s", resp.StatusCode, text(body))
	}
	// A healthy entry refused for another reason shows it in its own confirmation.
	e.Reg.Delete("traefik/whoami", sampleDigest)
	resp, body = e.postForm(t, "/apps/whoami/deployments/d1/redeploy", url.Values{})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST = %d, want 422:\n%s", resp.StatusCode, text(body))
	}
	d1 := entry(t, body, "d1")
	if !strings.Contains(d1, `<details class="cv-confirm" open>`) || !strings.Contains(d1, `role="alert"`) {
		t.Errorf("the refusal isn't in v1's open confirmation:\n%s", d1)
	}
	if n := len(e.deployments(t, "whoami")); n != 4 {
		t.Errorf("deployments = %d, want 4: a refused redeploy records none", n)
	}
}

// US2, US3: exports are downloads; Configure links the live one.
func TestExportFromTheConsole(t *testing.T) {
	e := versionsEnv(t)
	resp, body := e.get(t, "/apps/whoami/deployments/d1/jobspec")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Disposition") != "attachment; filename=whoami.nomad.hcl" {
		t.Fatalf("GET = %d %q", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
	if !strings.Contains(body, `job "whoami" {`) || !strings.Contains(body, "# Deployment d1,") {
		t.Errorf("not d1's jobspec:\n%s", body)
	}

	_, conf := e.get(t, "/apps/whoami/configure")
	if !strings.Contains(conf, `href="/apps/whoami/jobspec"`) {
		t.Error("Configure doesn't link the live export")
	}
	if resp, body := e.get(t, "/apps/whoami/jobspec"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "# Deployment d4,") {
		t.Errorf("live export = %d, want d4:\n%s", resp.StatusCode, body)
	}
	if resp, _ := e.get(t, "/apps/whoami/deployments/d3/jobspec"); resp.StatusCode != http.StatusConflict {
		t.Errorf("export of d3 = %d, want 409", resp.StatusCode)
	}
}

// keptHistoryEnv has shop deployed from keptShopFile with a secret whose value
// is sentinel015, and three deployments recorded by hand: "nofile" on shop,
// made from a definition; "nodef" on shop, with a file and no recorded
// definition; and "theirs" on whoami, with a file. It returns the ID of the
// deployment the service made, which has a file and a definition.
func keptHistoryEnv(t *testing.T) (e *consoleEnv, fileID string) {
	t.Helper()
	ctx := context.Background()
	e, _ = keptSentinelEnv(t)
	fileID = e.deployments(t, "shop")[0].Deployment.ID // the only one so far
	e.seed(t, whoamiApp())
	digests := map[string]string{"web": "traefik/whoami@" + sampleDigest}
	file := compose.Request{Compose: "# recorded by hand\n" + shopCompose}
	for _, d := range []struct {
		app, id string
		file    *compose.Request
	}{{"shop", "nofile", nil}, {"shop", "nodef", &file}, {"whoami", "theirs", &file}} {
		if _, err := e.Store.CreateDeployment(ctx, d.app, app.Deployment{ID: d.id, Trigger: app.TriggerManual, Digests: digests}, nil, d.file, ""); err != nil {
			t.Fatalf("CreateDeployment(%s): %v", d.id, err)
		}
	}
	return e, fileID
}

// Feature 015, US3: only an entry made from a Compose file offers its download,
// next to the export when it has a definition, and without it when it has none.
func TestHistoryOffersComposeDownload(t *testing.T) {
	e, first := keptHistoryEnv(t)
	_, body := e.get(t, "/apps/shop/deployments")

	withFile := entry(t, body, first)
	for _, want := range []string{
		`href="/apps/shop/deployments/` + first + `/compose" class="cv-btn cv-btn-secondary cv-btn-sm" download>Download Compose file</a>`,
		`href="/apps/shop/deployments/` + first + `/jobspec"`,
	} {
		if !strings.Contains(withFile, want) {
			t.Errorf("the entry with a file lacks %q:\n%s", want, withFile)
		}
	}
	// The download doesn't depend on the definition having been recorded.
	nodef := entry(t, body, "nodef")
	if !strings.Contains(nodef, `href="/apps/shop/deployments/nodef/compose"`) || strings.Contains(nodef, "/jobspec") {
		t.Errorf("an entry with a file and no definition should offer the download only:\n%s", nodef)
	}
	if without := entry(t, body, "nofile"); strings.Contains(without, "Download Compose file") || strings.Contains(without, "/compose") {
		t.Errorf("an entry made from a definition offers a Compose download:\n%s", without)
	}
}

// Feature 015, US3: the download is the stored file byte for byte, as an
// attachment that is never cached, and holds no secret value.
func TestDownloadComposeFile(t *testing.T) {
	e, first := keptHistoryEnv(t)
	resp, body := e.get(t, "/apps/shop/deployments/"+first+"/compose")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d:\n%s", resp.StatusCode, text(body))
	}
	if body != keptShopFile {
		t.Errorf("body = %q, want the kept file byte for byte %q", body, keptShopFile)
	}
	for header, want := range map[string]string{
		"Content-Type":        "application/yaml; charset=utf-8",
		"Content-Disposition": `attachment; filename="shop-` + first + `.compose.yaml"`,
		"Cache-Control":       "no-store",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if strings.Contains(body, sentinel015) {
		t.Error("a secret value is in the downloaded file")
	}
}

// Feature 015, US3: no file, or another app's deployment, is the not-found page.
func TestDownloadComposeFileNotFound(t *testing.T) {
	e, _ := keptHistoryEnv(t)
	for name, path := range map[string]string{
		"a deployment made from a definition": "/apps/shop/deployments/nofile/compose",
		"another app's deployment":            "/apps/shop/deployments/theirs/compose",
		"no such deployment":                  "/apps/shop/deployments/nope/compose",
		"no such app":                         "/apps/nope/deployments/theirs/compose",
	} {
		resp, body := e.get(t, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: GET %s = %d, want 404", name, path, resp.StatusCode)
		}
		if strings.Contains(body, "# recorded by hand") {
			t.Errorf("%s: the answer holds a file:\n%s", name, body)
		}
		if got := resp.Header.Get("Content-Disposition"); got != "" {
			t.Errorf("%s: Content-Disposition = %q, want none", name, got)
		}
	}
}
