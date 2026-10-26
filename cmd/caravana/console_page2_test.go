package main

// The console-domains and console-backup-daily steps of the lab test (feature
// 021, page 2): two forms of the app pages that the unit tests cover against a
// fake deployer and these steps cover against real Nomad and Traefik. The
// Domains page adds and removes a domain (each a deploy of the stored app with
// that one change), and the Backups tab's "Back up daily" sets a service's
// backup policy (also a deploy). It is in its own file only because
// console_lab_test.go is long; runConsole lists both steps before
// console-delete.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// formsPosting reads the values of every form of the page that posts to
// action (the pages of feature 021 have forms with no ID), in page order. A
// select isn't read; a step that has one sets its value itself.
func formsPosting(page, action string) []url.Values {
	re := regexp.MustCompile(`(?s)<form method="post" action="` + regexp.QuoteMeta(action) + `(?:#[^"]*)?"[^>]*>(.*?)</form>`)
	var forms []url.Values
	for _, m := range re.FindAllStringSubmatch(page, -1) {
		forms = append(forms, valuesIn(m[1]))
	}
	return forms
}

// formWhere is the form among forms whose field name has that value, and
// whether there is one.
func formWhere(forms []url.Values, name, value string) (url.Values, bool) {
	for _, f := range forms {
		if f.Get(name) == value {
			return f, true
		}
	}
	return nil, false
}

// removeLabApp deletes an app the step made, ignoring the answer: a step's
// clean-up must work whether or not the app got as far as existing.
func (c *console) removeLabApp(id string) {
	if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
		resp.Body.Close()
	}
}

// deployLabApp deploys the app through the API and waits for the deployment to
// end healthy.
func (c *console) deployLabApp(t *testing.T, id, body string) {
	t.Helper()
	raw := c.g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", body)
	d := gateDeployment{}
	if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := c.g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
}

// routerRules are the Traefik rules in the service tags of the job Nomad
// holds: one per service that has a domain, "traefik.http.routers.<router>.rule=Host(`...`)".
func (c *console) routerRules(t *testing.T, jobID string) []string {
	t.Helper()
	job, _, err := c.g.target.Nomad.Jobs().Info(jobID, nil)
	if err != nil {
		t.Fatalf("read Nomad's job %s: %v", jobID, err)
	}
	var rules []string
	collect := func(services []*api.Service) {
		for _, s := range services {
			for _, tag := range s.Tags {
				if strings.Contains(tag, ".rule=") {
					rules = append(rules, tag)
				}
			}
		}
	}
	for _, tg := range job.TaskGroups {
		collect(tg.Services)
		for _, task := range tg.Tasks {
			collect(task.Services)
		}
	}
	return rules
}

// storedApp is the part of the stored app these steps read.
type storedApp struct {
	App struct {
		Services []struct {
			Name    string `json:"name"`
			Domains []struct {
				Host string `json:"host"`
				Port string `json:"port"`
			} `json:"domains"`
			Backup *struct {
				Engine   string `json:"engine"`
				Schedule string `json:"schedule"`
				Keep     int    `json:"keep"`
			} `json:"backup"`
		} `json:"services"`
	} `json:"app"`
}

// domainsOf is the hosts of the stored service with that name.
func (s storedApp) domainsOf(name string) []string {
	var hosts []string
	for _, svc := range s.App.Services {
		if svc.Name == name {
			for _, d := range svc.Domains {
				hosts = append(hosts, d.Host)
			}
		}
	}
	return hosts
}

// page2Service is a service of these steps' apps: busybox serving one page on
// 8080 (see actionsWeb), with a domain when host isn't empty.
func page2Service(name, host string) map[string]any {
	svc := actionsWeb(host, 1)
	svc["name"] = name
	return svc
}

// domains: the Domains page's Add and Remove forms, on a two-service app
// (web, with one domain, and api, with none), against real Nomad and Traefik.
//
//  1. The page lists web's domain, and the "Sends traffic to" select offers both
//     services.
//  2. Adding a new domain to api is 303 to the new deployment, which ends
//     healthy. The stored app has the domain on api and not on web, Nomad's job
//     has api's Traefik rule for the host, the host answers over HTTPS through
//     Traefik, and the page lists it with the service it goes to.
//  3. Three refusals draw the page again with the field's reason (422) and
//     deploy nothing: a host that isn't a host name, one the app already has
//     and, when the lab has a console address, the console's own.
//  4. Removing the domain (the form in its confirmation) is 303 to a deployment
//     that ends healthy: the stored app, the job's rules and the page no longer
//     have it, it stops answering, and web's own domain still answers. The
//     same form posted again is refused, the domain being gone.
//
// The app is the step's own, so whoami's state is left as the other steps expect
// it; the clean-up deletes it.
func (c *console) domains(t *testing.T) {
	const id = "con021dom"
	// The hosts are unique to this lab run, so a name left by an aborted one
	// can't make a request answer for the wrong app.
	first, added := c.g.extraHost("p2a"+c.g.run), c.g.extraHost("p2b"+c.g.run)
	c.removeLabApp(id) // a leftover from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	t.Cleanup(func() { c.removeLabApp(id) })

	c.deployLabApp(t, id, labApp(page2Service("web", first), page2Service("api", "")))
	c.g.waitHostAnswers(t, first)

	// 1. The page and its form.
	domainsPath := "/apps/" + id + "/domains"
	resp, page := c.get(t, domainsPath)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `title="`+first+`"`) || !strings.Contains(pageText(page), "1 domain") {
		t.Fatalf("GET %s = %d, want 200 listing %s as the one domain:\n%s", domainsPath, resp.StatusCode, first, pageText(page))
	}
	for _, option := range []string{`<option value="web"`, `<option value="api"`} {
		if !strings.Contains(page, option) {
			t.Fatalf("the Sends traffic to select of a two-service app lacks %s:\n%s", option, page)
		}
	}
	forms := formsPosting(page, domainsPath)
	if len(forms) != 1 || forms[0].Get("revision") == "" || !strings.Contains(page, `name="host"`) {
		t.Fatalf("the Add form = %v, want one form with a revision and a host field:\n%s", forms, page)
	}

	// 2. Add the domain to api.
	form := forms[0]
	form.Set("host", added)
	form.Set("service", "api") // a select: valuesIn doesn't read it
	resp, body := c.post(t, domainsPath, form)
	c.deployedFrom(t, id, resp, body)

	var stored storedApp
	c.g.getJSON(t, "/v1/apps/"+id, &stored)
	if got := stored.domainsOf("api"); len(got) != 1 || got[0] != added {
		t.Errorf("api's stored domains = %v, want [%s]", got, added)
	}
	if got := stored.domainsOf("web"); len(got) != 1 || got[0] != first {
		t.Errorf("web's stored domains = %v, want [%s] only", got, first)
	}
	apiRule := false
	for _, rule := range c.routerRules(t, id) {
		if strings.Contains(rule, "routers."+id+"-api.rule=") && strings.Contains(rule, "Host(`"+added+"`)") {
			apiRule = true
		}
	}
	if !apiRule {
		t.Errorf("Nomad's job %s has no Traefik rule on router %s-api for %s: %v", id, id, added, c.routerRules(t, id))
	}
	c.g.waitHostAnswers(t, added)

	resp, page = c.get(t, domainsPath)
	if got := pageText(page); resp.StatusCode != http.StatusOK || !strings.Contains(page, `title="`+added+`"`) ||
		!strings.Contains(got, "2 domains") || !strings.Contains(got, "api :8080") {
		t.Errorf("GET %s after the add = %d, want both domains, %s going to api :8080:\n%s", domainsPath, resp.StatusCode, added, got)
	}

	// 3. Refusals: the page again with the reason, and nothing deployed.
	before := c.deploymentCount(t, id)
	refuse := func(host, want string) {
		t.Helper()
		_, page := c.get(t, domainsPath) // the page's own revision, so only the host is at fault
		form := formsPosting(page, domainsPath)[0]
		form.Set("host", host)
		form.Set("service", "api")
		resp, body := c.post(t, domainsPath, form)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(pageText(body), want) {
			t.Errorf("POST the Add form with host %q = %d, want 422 saying %q:\n%s", host, resp.StatusCode, want, pageText(body))
		}
		if !strings.Contains(body, `value="`+host+`"`) {
			t.Errorf("the page after refusing %q doesn't keep what was typed", host)
		}
		if got := c.deploymentCount(t, id); got != before {
			t.Errorf("refusing host %q left %d deployments, want %d", host, got, before)
		}
	}
	refuse("not a host", "is not a lowercase hostname")
	refuse(first, "is already used in this app")
	// The lab's Caravana reads CARAVANA_PUBLIC_URL from the test's environment;
	// when it is set, its host is the console's own and no app may take it.
	if host := consoleHostFromEnv(); host != "" {
		refuse(host, "is the console's own address")
	}

	// 4. Remove it, from the form in its confirmation (since 023 the row's detail holds it, at
	// POST /domains/{host}/remove with the app, its service and the page's revision).
	removePath := "/domains/" + added + "/remove"
	resp, page = c.get(t, domainsPath+"?open="+added)
	forms = formsPosting(page, removePath)
	if resp.StatusCode != http.StatusOK || len(forms) != 1 || forms[0].Get("app") != id || forms[0].Get("service") != "api" || forms[0].Get("revision") == "" {
		t.Fatalf("the Remove form of %s = %v, want one with the app, service api and a revision:\n%s", added, forms, pageText(page))
	}
	remove := forms[0]
	resp, body = c.post(t, removePath, remove)
	c.deployedFrom(t, id, resp, body)

	// A fresh value: decoding into the one read before would keep api's old domains, since an
	// empty list is left out of the JSON and the decoder then leaves the field as it was.
	stored = storedApp{}
	c.g.getJSON(t, "/v1/apps/"+id, &stored)
	if got := stored.domainsOf("api"); len(got) != 0 {
		t.Errorf("api's stored domains after the remove = %v, want none", got)
	}
	if got := stored.domainsOf("web"); len(got) != 1 || got[0] != first {
		t.Errorf("web's stored domains after the remove = %v, want [%s]", got, first)
	}
	for _, rule := range c.routerRules(t, id) {
		if strings.Contains(rule, added) {
			t.Errorf("Nomad's job still has the rule %q after the remove", rule)
		}
	}
	waitFor(t, "https://"+added+" to stop answering", time.Minute, func() bool {
		status, _, err := c.g.hostStatus(added)
		return err != nil || status != http.StatusOK
	})
	c.g.waitHostAnswers(t, first)
	if _, page = c.get(t, domainsPath); strings.Contains(page, `title="`+added+`"`) || !strings.Contains(page, `title="`+first+`"`) {
		t.Errorf("the Domains page after the remove should list %s and not %s:\n%s", first, added, pageText(page))
	}

	// The same form again: the domain is gone, so there is nothing to remove.
	again := c.deploymentCount(t, id)
	resp, body = c.post(t, removePath, remove)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(pageText(body), "no domain") {
		t.Errorf("POST the Remove form of a domain already gone = %d, want 422 saying the app has no such domain:\n%s", resp.StatusCode, pageText(body))
	}
	if got := c.deploymentCount(t, id); got != again {
		t.Errorf("removing a domain that is gone made %d deployments, want none", got-again)
	}
}

// consoleHostFromEnv is the host of CARAVANA_PUBLIC_URL as the lab's Caravana
// reads it (it takes the test process's environment), or "" when it isn't set.
func consoleHostFromEnv() string {
	raw := strings.TrimSpace(os.Getenv("CARAVANA_PUBLIC_URL"))
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// backupDaily: the Backups tab's "Back up daily" on an app with two services,
// each with a volume and no backup (web, which serves its volume, and worker,
// which only sleeps).
//
//  1. The tab has a gap row and a "Back up daily" form for each service, both
//     drawn on the same revision.
//  2. The form of web is 303 to the new deployment, which ends healthy, and the
//     stored web has backup {volume, "0 3 * * *", 7}.
//  3. The form of worker, drawn on the revision from before that deploy, is
//     refused with 409 saying the app changed, and deploys nothing.
//  4. The tab no longer has a gap row for web but a backed-up row with the
//     schedule in words; worker's gap row is still there.
//  5. The web form again (its service has a policy now) is refused with 409 and
//     deploys nothing.
//
// It doesn't press "Back up now": the policy is a deploy that doesn't need a
// destination, and console-volumes already runs a volume backup through the
// console against the bucket job. The clean-up deletes the app and its volumes.
func (c *console) backupDaily(t *testing.T) {
	const id = "con021bak"
	c.removeLabApp(id) // a leftover from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool { return c.jobAbsent(id) })
	t.Cleanup(func() { c.removeLabApp(id) })

	web := page2Service("web", "")
	web["args"] = []string{"-c", "[ -f /data/v ] || printf A > /data/v; exec httpd -f -p 8080 -h /data"}
	web["health"] = map[string]any{"path": "/v", "interval": "5s", "timeout": "2s"}
	web["volumes"] = []map[string]any{{"name": "data", "path": "/data"}}
	worker := map[string]any{"name": "worker", "image": "busybox:1.37", "command": "sh", "args": []string{"-c", "exec sleep 86400"},
		"resources": labResources, "volumes": []map[string]any{{"name": "cache", "path": "/cache"}}}
	c.deployLabApp(t, id, labApp(web, worker))

	// 1. A gap row and a form for each service.
	backupsPath, policyPath := "/apps/"+id+"/backups", "/apps/"+id+"/backups/policy"
	resp, page := c.get(t, backupsPath)
	text := pageText(page)
	forms := formsPosting(page, policyPath)
	webForm, okWeb := formWhere(forms, "service", "web")
	workerForm, okWorker := formWhere(forms, "service", "worker")
	if resp.StatusCode != http.StatusOK || !strings.Contains(text, "web · /data has no backup.") || !strings.Contains(text, "worker · /cache has no backup.") ||
		!strings.Contains(text, "Back up daily") || !okWeb || !okWorker || len(forms) != 2 ||
		webForm.Get("revision") == "" || webForm.Get("revision") != workerForm.Get("revision") {
		t.Fatalf("GET %s = %d, want a gap row and a Back up daily form (on one revision) for web and worker:\n%s", backupsPath, resp.StatusCode, text)
	}

	// 2. web: the policy is a deploy, and the stored app has the default one.
	resp, body := c.post(t, policyPath, webForm)
	c.deployedFrom(t, id, resp, body)
	var stored storedApp
	c.g.getJSON(t, "/v1/apps/"+id, &stored)
	for _, svc := range stored.App.Services {
		switch {
		case svc.Name == "web" && (svc.Backup == nil || svc.Backup.Engine != "volume" || svc.Backup.Schedule != "0 3 * * *" || svc.Backup.Keep != 7):
			t.Errorf("web's stored backup = %+v, want {volume, 0 3 * * *, 7}", svc.Backup)
		case svc.Name == "worker" && svc.Backup != nil:
			t.Errorf("worker's stored backup = %+v, want none: its form wasn't posted", svc.Backup)
		}
	}

	// 3. worker's form is from before that deploy.
	before := c.deploymentCount(t, id)
	resp, body = c.post(t, policyPath, workerForm)
	if got := pageText(body); resp.StatusCode != http.StatusConflict || !strings.Contains(got, "This app changed since you opened this page.") {
		t.Errorf("POST worker's form from before the deploy = %d, want 409 saying the app changed:\n%s", resp.StatusCode, got)
	}
	if got := c.deploymentCount(t, id); got != before {
		t.Errorf("a stale Back up daily made %d deployments, want none", got-before)
	}

	// 4. The tab: web has a row, worker still has its gap.
	resp, page = c.get(t, backupsPath)
	text = pageText(page)
	if resp.StatusCode != http.StatusOK || strings.Contains(text, "web · /data has no backup.") || !strings.Contains(page, `<span class="cv-bname">web</span>`) ||
		!strings.Contains(text, "Every day at 03:00 UTC") || !strings.Contains(text, "7 backups") || !strings.Contains(text, "worker · /cache has no backup.") {
		t.Errorf("GET %s after the policy = %d, want a row for web (every day at 03:00 UTC, 7 backups) and worker's gap:\n%s", backupsPath, resp.StatusCode, text)
	}

	// 5. web's form again: its service has a policy now.
	before = c.deploymentCount(t, id)
	resp, body = c.post(t, policyPath, webForm)
	if got := pageText(body); resp.StatusCode != http.StatusConflict || !strings.Contains(got, "already has a backup policy") {
		t.Errorf("POST web's form again = %d, want 409 saying it already has a backup policy:\n%s", resp.StatusCode, got)
	}
	if got := c.deploymentCount(t, id); got != before {
		t.Errorf("Back up daily on a service with a policy made %d deployments, want none", got-before)
	}
}
