package main

// console-templates (feature 035, T012): what the owner does in New app › From a template, done over
// HTTP the way a browser does it. Uptime Kuma is installed with only a domain typed; it ends healthy,
// answers through Traefik, has its volume backup policy on and names its template on the record.

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// consoleTemplatesApp is the app the step installs, and the host it is served at.
const (
	consoleTemplatesApp  = "tpl-kuma"
	consoleTemplatesHost = "tpl-kuma.localhost"
)

// appGone waits for the app's job to leave Nomad.
func (c *console) appGone(t *testing.T, id string) {
	t.Helper()
	waitFor(t, id+"'s Nomad job to be gone", 2*time.Minute, func() bool {
		_, _, err := c.g.target.Nomad.Jobs().Info(id, nil)
		return err != nil && strings.Contains(err.Error(), "404")
	})
}

func (c *console) consoleTemplates(t *testing.T) {
	const id, host = consoleTemplatesApp, consoleTemplatesHost
	c.ensureSignedIn(t)
	remove := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	t.Cleanup(func() {
		remove()
		c.appGone(t, id)
	})

	// The grid: every template that is offered, as links; a search narrows it, with no script.
	resp, grid := c.get(t, "/apps/new?from=template")
	if resp.StatusCode != http.StatusOK || !strings.Contains(grid, `href="/apps/new/template/uptime-kuma"`) ||
		!strings.Contains(grid, `href="/apps/new/template/hollo"`) {
		t.Fatalf("GET the template tab = %d, want 200 with the templates:\n%s", resp.StatusCode, grid)
	}
	if _, found := c.get(t, "/apps/new?from=template&q=status"); !strings.Contains(found, "Uptime Kuma") || strings.Contains(found, `href="/apps/new/template/hollo"`) {
		t.Errorf("a search for status doesn't narrow the grid to Uptime Kuma:\n%s", found)
	}

	// The template's page. Without a backup destination it says so and offers the checkbox; with one, it doesn't.
	destination := c.g.api(t, http.MethodGet, "/v1/backups/destination", "", "")
	destination.Body.Close()
	_, page := c.get(t, "/apps/new/template/uptime-kuma")
	switch destination.StatusCode {
	case http.StatusNotFound:
		for _, want := range []string{"No backup destination is set yet.", `name="withoutBackups"`, `href="/settings/backup-storage"`} {
			if !strings.Contains(page, want) {
				t.Errorf("with no destination the page lacks %q:\n%s", want, page)
			}
		}
		// The volume backup half of this step needs a place to send backups to.
		c.g.startBucket(t)
	case http.StatusOK:
		t.Log("a backup destination is already set; leaving it")
	default:
		t.Fatalf("GET /v1/backups/destination = %d, want 404 or 200", destination.StatusCode)
	}
	_, page = c.get(t, "/apps/new/template/uptime-kuma")
	if strings.Contains(page, "No backup destination is set yet.") || strings.Contains(page, `name="withoutBackups"`) {
		t.Errorf("with a destination the page still asks about backups:\n%s", page)
	}
	if !strings.Contains(page, `name="in_domain"`) || strings.Contains(page, "<form") && !strings.Contains(page, `action="/apps/new/template/uptime-kuma"`) {
		t.Errorf("the page's form:\n%s", page)
	}

	// A wrong answer comes back at its input, and nothing is made.
	resp, body := c.post(t, "/apps/new/template/uptime-kuma", url.Values{"name": {id}, "in_domain": {"not a domain"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="in_domain-error"`) {
		t.Fatalf("POST a bad domain = %d, want 422 at the domain input: %s", resp.StatusCode, body)
	}
	if status := c.g.api(t, http.MethodGet, "/v1/apps/"+id, "", ""); status.StatusCode != http.StatusNotFound {
		t.Fatalf("a refused install made the app: %d", status.StatusCode)
	}

	// Install with only a domain typed.
	resp, body = c.post(t, "/apps/new/template/uptime-kuma", url.Values{"name": {id}, "in_domain": {host}})
	want := "/apps/" + id + "/deployments?created=template&v_domain=" + host
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
		t.Fatalf("POST the template form = %d → %q, want 303 to %q: %s", resp.StatusCode, resp.Header.Get("Location"), want, body)
	}
	var deployments struct {
		Deployments []struct{ ID string } `json:"deployments"`
	}
	c.g.getJSON(t, "/v1/apps/"+id+"/deployments", &deployments)
	if len(deployments.Deployments) != 1 {
		t.Fatalf("deployments after the install = %+v, want one", deployments.Deployments)
	}
	if final := c.g.waitFinalOf(t, id, deployments.Deployments[0].ID, 6*time.Minute); final.Status != "healthy" {
		t.Fatalf("the install ended %s: %s", final.Status, final.Error)
	}

	// The record names the template, and the policy is on.
	var rec struct {
		App struct {
			Services []struct {
				Name    string `json:"name"`
				Health  struct{ Path string }
				Backup  *struct{ Engine string }
				Domains []struct{ Host string }
			} `json:"services"`
		} `json:"app"`
		Template *struct{ Name, Version string } `json:"template"`
	}
	c.g.getJSON(t, "/v1/apps/"+id, &rec)
	if rec.Template == nil || rec.Template.Name != "uptime-kuma" || rec.Template.Version == "" || len(rec.App.Services) != 1 {
		t.Fatalf("the app's record = %+v, want it to name the uptime-kuma template", rec)
	}
	svc := rec.App.Services[0]
	if svc.Backup == nil || svc.Backup.Engine != "volume" || svc.Health.Path != "/robots.txt" || len(svc.Domains) != 1 || svc.Domains[0].Host != host {
		t.Errorf("the service = %+v, want a volume backup policy, the health path /robots.txt and the typed domain", svc)
	}

	// Its pages say where it came from, and the Backups tab has the policy.
	_, deps := c.get(t, want)
	if !strings.Contains(pageText(deps), "Installed from the Uptime Kuma template.") || !strings.Contains(pageText(deps), "Open https://"+host+" and create the admin account") {
		t.Errorf("the Deployments tab lacks the installed notice and the notes:\n%s", pageText(deps))
	}
	if _, conf := c.get(t, "/apps/"+id+"/configure"); !regexp.MustCompile(`Template · Uptime Kuma\s+<span[^>]*>[^<]+</span>`).MatchString(conf) &&
		!strings.Contains(pageText(conf), "Template · Uptime Kuma ") {
		t.Errorf("Configure lacks the template's fact:\n%s", pageText(conf))
	}
	if _, backups := c.get(t, "/apps/"+id+"/backups"); !strings.Contains(backups, "Every day at 03:00 UTC") {
		t.Errorf("the Backups tab doesn't show the policy:\n%s", pageText(backups))
	}

	// Traefik routes the domain to it: /robots.txt answers 200 as the health check did.
	client, base := c.g.edgeClient(host)
	waitFor(t, "Uptime Kuma to answer through Traefik", 2*time.Minute, func() bool {
		resp, err := client.Get(base + "/robots.txt")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})

	// The same domain again is refused at the domain input.
	resp, body = c.post(t, "/apps/new/template/uptime-kuma", url.Values{"name": {id + "-2"}, "in_domain": {host}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, `id="in_domain-error"`) {
		t.Errorf("POST the same domain again = %d, want 409 at the domain input: %s", resp.StatusCode, body)
	}
}
