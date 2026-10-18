package main

// The console-create-only step of the lab test (feature 020, plan.md "Lab"):
// an app created without deploying it, with only its name and with a
// definition, and what gives each its first deploy. It is in its own file only
// because console_lab_test.go is long; runConsole lists it with the others.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// jobAbsent reports whether Nomad has no job with this ID.
func (c *console) jobAbsent(id string) bool {
	_, _, err := c.g.target.Nomad.Jobs().Info(id, nil)
	return err != nil && strings.Contains(err.Error(), "404")
}

// deploymentCount is how many deployments the API lists for the app.
func (c *console) deploymentCount(t *testing.T, appID string) int {
	t.Helper()
	var list struct {
		Deployments []gateDeployment `json:"deployments"`
	}
	c.g.getJSON(t, "/v1/apps/"+appID+"/deployments", &list)
	return len(list.Deployments)
}

// webhookToken makes the app's webhook token through the API.
func (c *console) webhookToken(t *testing.T, appID string) string {
	t.Helper()
	resp := c.g.api(t, http.MethodPost, "/v1/apps/"+appID+"/webhook", "", "")
	defer resp.Body.Close()
	var created struct{ Token string }
	_ = json.NewDecoder(resp.Body).Decode(&created)
	if resp.StatusCode != http.StatusCreated || created.Token == "" {
		t.Fatalf("create the webhook token of %s: %d", appID, resp.StatusCode)
	}
	return created.Token
}

// callWebhook posts to the app's webhook with the token in the Authorization
// header, as CI does, and returns the status and the body.
func (c *console) callWebhook(t *testing.T, appID, token string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, c.g.target.Webhook+"/v1/webhooks/"+appID, strings.NewReader(`{"push_data":{"tag":"main"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.g.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// createOnly: creating an app without deploying it.
//
//  1. New app with only a name and "Create without deploying": 303 to the app's
//     page with the created notice; the page says there is no definition; no
//     Nomad job and no deployment; the list says "not defined yet"; the needs-you
//     list has nothing for it; its webhook answers 409 app_not_defined and
//     records nothing.
//  2. Configure's "Define the app" form gives it an image and a port and
//     deploys it with the revision from the page: healthy. (It reserves the
//     default memory, so the app is deleted before the next part.)
//  3. A second app created with a definition through the API's deploy=false:
//     201, no Nomad job, its page says it waits for its first deploy; the
//     webhook, called as CI would, deploys it as stored (trigger webhook) and
//     it ends healthy with one deployment.
//
// Budget: two deploys, each waited for up to 4 minutes, normally under one.
func (c *console) createOnly(t *testing.T) {
	const named, defined = "con020a", "con020b"
	remove := func() {
		for _, id := range []string{named, defined} {
			if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // a leftover from an aborted run, if any
	t.Cleanup(remove)

	// 1. A name only.
	form := url.Values{"name": {named}, "image": {""}, "port": {""}, "domain": {""}, "healthPath": {"/"}, "action": {"create"}}
	resp, body := c.post(t, "/apps", form)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/apps/"+named+"?created=1" {
		t.Fatalf("POST /apps with Create without deploying = %d → %q, want 303 to /apps/%s?created=1: %s", resp.StatusCode, resp.Header.Get("Location"), named, body)
	}
	resp, body = c.get(t, "/apps/"+named+"?created=1")
	got := pageText(body)
	for _, want := range []string{"Created " + named + ". Nothing is deployed yet.", "No definition yet.", "Define it in Configure"} {
		if resp.StatusCode != http.StatusOK || !strings.Contains(got, want) {
			t.Fatalf("GET the new app's page = %d, want %q:\n%s", resp.StatusCode, want, got)
		}
	}
	if strings.Contains(got, "Deploy now") {
		t.Errorf("the page offers Deploy now for an app with no definition:\n%s", got)
	}
	if !c.jobAbsent(named) {
		t.Errorf("creating %s without deploying registered a Nomad job", named)
	}
	if n := c.deploymentCount(t, named); n != 0 {
		t.Errorf("%s has %d deployments, want none", named, n)
	}
	var rec struct {
		Defined  bool   `json:"defined"`
		Revision string `json:"revision"`
	}
	c.g.getJSON(t, "/v1/apps/"+named, &rec)
	if rec.Defined {
		t.Errorf("GET /v1/apps/%s says defined", named)
	}
	if _, body = c.get(t, "/apps"); !strings.Contains(pageText(body), "not defined yet") {
		t.Errorf("the Apps page doesn't say %s is not defined yet:\n%s", named, pageText(body))
	}
	if items, _ := c.attention(t); len(items) > 0 {
		for _, it := range items {
			if it.AppID == named {
				t.Errorf("the needs-you list has an item for %s: %+v", named, it)
			}
		}
	}
	token := c.webhookToken(t, named)
	status, raw := c.callWebhook(t, named, token)
	var refused struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(raw, &refused)
	if status != http.StatusConflict || refused.Error.Code != "app_not_defined" {
		t.Errorf("the webhook of an app with no definition = %d %s, want 409 app_not_defined", status, raw)
	}
	if n := c.deploymentCount(t, named); n != 0 || !c.jobAbsent(named) {
		t.Errorf("the refused webhook left %d deployments (job absent: %v), want none", n, c.jobAbsent(named))
	}

	// 2. Define it through Configure's form.
	resp, body = c.get(t, "/apps/"+named+"/configure")
	revision := regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(body)
	if got := pageText(body); resp.StatusCode != http.StatusOK || revision == nil || !strings.Contains(got, "Define the app") {
		t.Fatalf("GET Configure of %s = %d, want the Define the app form with a revision:\n%s", named, resp.StatusCode, got)
	}
	resp, body = c.post(t, "/apps/"+named+"/define", url.Values{"revision": {"0000000000000000"}, "image": {"traefik/whoami:v1.10"},
		"port": {"80"}, "domain": {""}, "healthPath": {"/"}})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("POST the define form with a stale revision = %d, want 409: %s", resp.StatusCode, pageText(body))
	}
	if n := c.deploymentCount(t, named); n != 0 {
		t.Fatalf("a refused define recorded %d deployments", n)
	}
	resp, body = c.post(t, "/apps/"+named+"/define", url.Values{"revision": {revision[1]}, "image": {"traefik/whoami:v1.10"},
		"port": {"80"}, "domain": {""}, "healthPath": {"/"}})
	c.deployedFrom(t, named, resp, body)
	c.g.getJSON(t, "/v1/apps/"+named, &rec)
	if !rec.Defined {
		t.Errorf("GET /v1/apps/%s after defining it says not defined", named)
	}
	if _, body = c.get(t, "/apps/"+named+"/configure"); strings.Contains(pageText(body), "Define the app") {
		t.Errorf("Configure of %s still offers the Define form after it was defined", named)
	}
	// It reserved the default memory: free it for the next part.
	if resp, body = c.post(t, "/apps/"+named+"/delete", url.Values{"confirm": {named}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST the delete of %s = %d: %s", named, resp.StatusCode, body)
	}
	waitFor(t, named+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(named) })

	// 3. A definition, never deployed, then the webhook.
	service := labWorker("worker")
	raw, _ = json.Marshal(map[string]any{"id": defined, "source": map[string]string{"kind": "image"}, "services": []any{service}})
	resp2 := c.g.api(t, http.MethodPost, "/v1/apps?deploy=false", "application/json", string(raw))
	created, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated || resp2.Header.Get("Location") != "/v1/apps/"+defined || resp2.Header.Get("ETag") == "" {
		t.Fatalf("POST /v1/apps?deploy=false = %d (Location %q, ETag %q), want 201 with both: %s",
			resp2.StatusCode, resp2.Header.Get("Location"), resp2.Header.Get("ETag"), created)
	}
	if !c.jobAbsent(defined) || c.deploymentCount(t, defined) != 0 {
		t.Fatalf("creating %s without deploying registered a job or a deployment", defined)
	}
	if resp, body = c.get(t, "/apps/"+defined); resp.StatusCode != http.StatusOK ||
		!strings.Contains(pageText(body), "Waiting for its first deploy.") || !strings.Contains(pageText(body), "Deploy now") {
		t.Errorf("GET the page of %s = %d, want it waiting for its first deploy with Deploy now:\n%s", defined, resp.StatusCode, pageText(body))
	}
	var sum struct {
		Defined bool `json:"defined"`
	}
	c.g.getJSON(t, "/v1/apps/"+defined, &sum)
	if !sum.Defined {
		t.Errorf("GET /v1/apps/%s says not defined", defined)
	}

	token = c.webhookToken(t, defined)
	status, raw = c.callWebhook(t, defined, token)
	var hooked struct {
		Changed    bool
		Deployment gateDeployment
	}
	_ = json.Unmarshal(raw, &hooked)
	if status != http.StatusAccepted || !hooked.Changed || hooked.Deployment.ID == "" {
		t.Fatalf("the webhook of a defined app that was never deployed = %d %s, want 202 changed", status, raw)
	}
	if final := c.g.waitFinalOf(t, defined, hooked.Deployment.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("the webhook's first deploy of %s ended %s: %s", defined, final.Status, final.Error)
	}
	if n := c.deploymentCount(t, defined); n != 1 {
		t.Errorf("%s has %d deployments, want the webhook's one", defined, n)
	}
	var first struct {
		Trigger string `json:"trigger"`
	}
	c.g.getJSON(t, "/v1/apps/"+defined+"/deployments/"+hooked.Deployment.ID, &first)
	if first.Trigger != "webhook" {
		t.Errorf("the first deployment's trigger = %q, want webhook", first.Trigger)
	}

	// Clean up: the job goes with the app.
	if resp, body = c.post(t, "/apps/"+defined+"/delete", url.Values{"confirm": {defined}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST the delete of %s = %d: %s", defined, resp.StatusCode, body)
	}
	waitFor(t, defined+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(defined) })
}
