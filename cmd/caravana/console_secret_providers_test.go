package main

// console-secret-providers (feature 037, tasks.md C3): what the owner does in
// Settings › Secret providers and on an app's Variables page, over HTTP the way
// a browser does it, against the lab's fake Bella.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func (c *console) secretProviders(t *testing.T) {
	bella := c.g.target.Bella
	if bella == nil {
		t.Skip("the fake Bella runs beside an in-process Caravana only: the lab")
	}
	const (
		id       = "con037"
		provider = "con037-bella"
	)
	c.ensureSignedIn(t)
	sentinel := "s3cret-sentinel-037-ui-" + c.g.run
	rotated := sentinel + "-rotated"
	bella.Set("CON_REF", sentinel)

	remove := func() {
		for _, p := range []string{"/v1/apps/" + id, "/v1/secret-providers/" + provider} {
			if resp, err := c.g.apiRequest(http.MethodDelete, p, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // leftovers from an aborted run, if any
	c.appGone(t, id)
	t.Cleanup(func() {
		remove()
		c.appGone(t, id)
	})
	pages := map[string]string{} // every page read, searched for secrets at the end
	get := func(path string) string {
		t.Helper()
		resp, body := c.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d:\n%s", path, resp.StatusCode, pageText(body))
		}
		pages[path] = body
		return body
	}
	post303 := func(path string, form url.Values, wantPrefix string) string {
		t.Helper()
		resp, body := c.post(t, path, form)
		loc := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, wantPrefix) {
			t.Fatalf("POST %s = %d → %q, want 303 to %s…:\n%s", path, resp.StatusCode, loc, wantPrefix, pageText(body))
		}
		pages["POST "+path] = body
		return loc
	}

	// 1. Add the provider through the form, then Test it.
	get("/settings/secret-providers")
	post303("/settings/secret-providers", url.Values{
		"name": {provider}, "url": {bella.URL()}, "project": {bella.Project()}, "environment": {bella.Environment()},
		"apiKey": {bella.APIKey()}, "privateKey": {bella.PrivateKeyPEM()}, "allowed": {""},
	}, "/settings/secret-providers")
	if page := get("/settings/secret-providers"); !strings.Contains(page, provider) {
		t.Fatalf("the saved provider isn't listed:\n%s", pageText(page))
	}
	post303("/settings/secret-providers/test", url.Values{"name": {provider}}, "/settings/secret-providers")
	if page := get("/settings/secret-providers"); !strings.Contains(pageText(page), "1 secret readable") {
		t.Errorf("the provider's test doesn't say 1 secret readable:\n%s", pageText(page))
	}

	// 2. An app, then a reference added on its Variables page.
	raw := c.g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(actionsWeb("", 1)))
	var first gateDeployment
	if err := json.Unmarshal(raw, &first); err != nil || first.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := c.g.waitFinalOf(t, id, first.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	page := get("/apps/" + id + "/variables")
	form := formValuesAction(t, page, "/apps/"+id+"/variables")
	form.Set("name", "CON_REF")
	form.Set("source", "provider")
	form.Set("provider", provider)
	form.Set("path", "CON_REF")
	form.Set("secret", "1")
	form.Del("value")
	post303("/apps/"+id+"/variables", form, "/apps/"+id+"/deployments")
	waitNewestHealthy := func(what string) {
		t.Helper()
		var deps struct {
			Deployments []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  string `json:"error"`
			} `json:"deployments"`
		}
		c.g.getJSON(t, "/v1/apps/"+id+"/deployments", &deps)
		if len(deps.Deployments) == 0 {
			t.Fatalf("%s: no deployment", what)
		}
		if final := c.g.waitFinalOf(t, id, deps.Deployments[0].ID, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("%s ended %s: %s", what, final.Status, final.Error)
		}
	}
	waitNewestHealthy("the deploy that added the reference")
	page = get("/apps/" + id + "/variables")
	text := pageText(page)
	if !strings.Contains(text, provider) || !strings.Contains(text, "CON_REF") || !strings.Contains(page, `action="/apps/`+id+`/secrets/refresh"`) {
		t.Errorf("the Variables page lacks the reference row or Refresh secrets:\n%s", text)
	}
	if !strings.Contains(text, "Where secrets come from") {
		t.Errorf("the Variables page lacks the Where secrets come from panel:\n%s", text)
	}

	// 3. Refresh secrets: unchanged, then changed in Bella.
	loc := post303("/apps/"+id+"/secrets/refresh", url.Values{}, "/apps/"+id+"/variables?refresh=unchanged")
	if page := get(loc); !strings.Contains(pageText(page), "Secrets unchanged") {
		t.Errorf("an unchanged refresh doesn't say so:\n%s", pageText(page))
	}
	bella.Set("CON_REF", rotated)
	post303("/apps/"+id+"/secrets/refresh", url.Values{}, "/apps/"+id+"/deployments")
	waitNewestHealthy("the refresh")
	if page := get("/apps/" + id + "/deployments"); !strings.Contains(pageText(page), "Refreshed secrets") {
		t.Errorf("the history doesn't say Refreshed secrets:\n%s", pageText(page))
	}

	// 4. Remove is refused while the app uses the provider, then works.
	resp, body := c.post(t, "/settings/secret-providers/remove", url.Values{"name": {provider}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(pageText(body), id) {
		t.Errorf("Remove while in use = %d, want 409 naming %s:\n%s", resp.StatusCode, id, pageText(body))
	}
	pages["remove refused"] = body
	c.g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+id, "")
	c.appGone(t, id)
	post303("/settings/secret-providers/remove", url.Values{"name": {provider}}, "/settings/secret-providers")
	if page := get("/settings/secret-providers"); strings.Contains(page, provider) {
		t.Errorf("the removed provider is still listed:\n%s", pageText(page))
	}

	// 5. No value or key in any page.
	secrets := map[string]string{
		"the value read from Bella":     sentinel,
		"the rotated value":             rotated,
		"the API key":                   bella.APIKey(),
		"the private key's base64 body": privateKeyBody(bella.PrivateKeyPEM()),
	}
	for where, html := range pages {
		for what, secret := range secrets {
			if strings.Contains(html, secret) {
				t.Errorf("%s holds %s", where, what)
			}
		}
	}
}
