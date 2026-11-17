package main

// The console-placement step of the lab test (feature 042): a service's placement rules set and
// cleared through Configure's Placement form, against the lab's real Nomad (one node, not managed:
// no labels, no server to avoid, and nowhere to move work to). It is in its own file only because
// console_lab_test.go is long; runConsole lists it after console-servers.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// consolePlacement:
//
//  1. Configure shows the service's Placement panel: "Any server that accepts work." and the form
//     with Require and Prefer (no "Never on": the lab has no managed server).
//  2. A label no server has is refused 409 at the Require input, naming the label; the app keeps
//     its one healthy copy.
//  3. A preference is saved: 303 to the deployment, which ends healthy, and the panel says
//     "Prefers ssd=true (weight 80)"; the API's app carries the rule.
//  4. An empty form clears the rules: the panel says "Any server that accepts work." again.
//  5. The lab's node has no Move work off… (it isn't managed), and its page draws.
func (c *console) consolePlacement(t *testing.T) {
	const id = "con042"
	c.removeLabApp(id) // a leftover from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	t.Cleanup(func() { c.removeLabApp(id) })
	t.Cleanup(func() { logTail(t, c.g.target) })

	web := map[string]any{"name": "web", "image": "busybox:1.37", "command": "sh",
		"args":      []string{"-c", "mkdir -p /www; echo placed > /www/index.html; exec httpd -f -p 8080 -h /www"},
		"ports":     []map[string]any{{"name": "http", "containerPort": 8080}},
		"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		"resources": labResources}
	c.deployLabApp(t, id, labApp(web))
	waitFor(t, "a running copy of "+id, time.Minute, func() bool { return len(c.g.runningCopies(t, id)) == 1 })

	revision := func() string {
		t.Helper()
		var rec struct {
			Revision string `json:"revision"`
		}
		c.g.getJSON(t, "/v1/apps/"+id, &rec)
		return rec.Revision
	}
	action := "/apps/" + id + "/services/web/placement"

	// 1. The panel.
	resp, page := c.get(t, "/apps/"+id+"/configure")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/%s/configure = %d", id, resp.StatusCode)
	}
	for _, want := range []string{`action="` + action + `#placement-web"`, `name="require"`, `name="prefer"`, `Save placement`} {
		if !strings.Contains(page, want) {
			t.Errorf("Configure lacks %q", want)
		}
	}
	if words := htmlText(page); !strings.Contains(words, "Any server that accepts work.") || strings.Contains(words, "Never on") {
		t.Errorf("the Placement panel = %s, want \"Any server that accepts work.\" and no \"Never on\"", words)
	}

	// 2. An impossible rule.
	resp, page = c.post(t, action, url.Values{"revision": {revision()}, "require": {"gpu"}, "prefer": {""}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(page, `name="require" value="gpu" `) ||
		!strings.Contains(page, `aria-invalid="true"`) || !strings.Contains(htmlText(page), "server that accepts work has the label gpu=true") {
		t.Fatalf("an impossible rule = %d, want 409 drawn at the Require input with the label named:\n%s", resp.StatusCode, htmlText(page))
	}
	if len(c.g.runningCopies(t, id)) != 1 {
		t.Errorf("the refused rule touched the app's copies")
	}

	// 3. A preference.
	resp, page = c.post(t, action, url.Values{"revision": {revision()}, "require": {""}, "prefer": {"ssd=true 80"}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/apps/"+id+"/deployments#deployment-") {
		t.Fatalf("Save placement = %d → %q, want 303 to the deployment: %s", resp.StatusCode, loc, htmlText(page))
	}
	depID := strings.TrimPrefix(loc, "/apps/"+id+"/deployments#deployment-")
	if final := c.g.waitFinalOf(t, id, depID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("the deploy with a preference ended %s: %s", final.Status, final.Error)
	}
	_, page = c.get(t, "/apps/"+id+"/configure")
	if words := htmlText(page); !strings.Contains(words, "Prefers ssd=true (weight 80)") {
		t.Errorf("the Placement panel after the save = %s, want \"Prefers ssd=true (weight 80)\"", words)
	}
	var rec struct {
		App struct {
			Services []struct {
				Placement *struct {
					Prefer []struct {
						Label, Value string
						Weight       int
					}
				}
			}
		} `json:"app"`
	}
	c.g.getJSON(t, "/v1/apps/"+id, &rec)
	if len(rec.App.Services) != 1 || rec.App.Services[0].Placement == nil || len(rec.App.Services[0].Placement.Prefer) != 1 ||
		rec.App.Services[0].Placement.Prefer[0].Label != "ssd" || rec.App.Services[0].Placement.Prefer[0].Weight != 80 {
		t.Errorf("the API's app = %+v, want one preference ssd=true with weight 80", rec.App.Services)
	}

	// 4. Cleared.
	resp, page = c.post(t, action, url.Values{"revision": {revision()}, "require": {""}, "prefer": {""}})
	loc = resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/apps/"+id+"/deployments#deployment-") {
		t.Fatalf("clearing the rules = %d → %q: %s", resp.StatusCode, loc, htmlText(page))
	}
	depID = strings.TrimPrefix(loc, "/apps/"+id+"/deployments#deployment-")
	if final := c.g.waitFinalOf(t, id, depID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("the deploy without rules ended %s: %s", final.Status, final.Error)
	}
	_, page = c.get(t, "/apps/"+id+"/configure")
	if words := htmlText(page); !strings.Contains(words, "Any server that accepts work.") || strings.Contains(words, "Prefers ssd=true") {
		t.Errorf("the Placement panel after clearing = %s, want \"Any server that accepts work.\"", words)
	}

	// 5. The lab's node has nothing to move work off of.
	var servers struct {
		Servers []struct {
			Name string `json:"name"`
		} `json:"servers"`
	}
	c.g.getJSON(t, "/v1/servers", &servers)
	if len(servers.Servers) != 1 {
		t.Fatalf("GET /v1/servers lists %d servers, want the lab's one", len(servers.Servers))
	}
	resp, page = c.get(t, "/ops/servers/"+servers.Servers[0].Name)
	if resp.StatusCode != http.StatusOK || strings.Contains(page, "Move work off") || strings.Contains(page, "Accept work again") {
		t.Errorf("the unmanaged node's page = %d, want 200 with no move action", resp.StatusCode)
	}
	resp, _ = c.get(t, "/ops/servers/"+servers.Servers[0].Name+"/move")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET the move confirmation of an unmanaged node = %d, want 404", resp.StatusCode)
	}
}
