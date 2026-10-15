package ui

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// editEnv has duo (a web service with an address and a health check, and a
// worker with arguments) deployed and healthy through the service, so its job
// is in the fake Nomad.
func editEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.deploy(t, duoApp())
	return e
}

// deploy deploys a through the service and waits for the rollout to end.
func (e *consoleEnv) deploy(t *testing.T, a app.App) store.DeploymentRecord {
	t.Helper()
	rec, err := e.Svc.Deploy(context.Background(), a, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy(%s): %v", a.ID, err)
	}
	e.waitDeploy(t, rec.Deployment.ID)
	return rec
}

// configureForm is the Configure form of the app as its page draws it.
func (e *consoleEnv) configureForm(t *testing.T, id string) plainForm {
	t.Helper()
	resp, body := e.get(t, "/apps/"+id+"/configure")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/%s/configure = %d:\n%s", id, resp.StatusCode, body)
	}
	return formWith(t, body, "/apps/"+id+"/configure")
}

// Feature 014, US1: a save lays the form over the stored app and deploys it.
func TestSaveConfigure(t *testing.T) {
	t.Run("an edit deploys once and changes only what the form changed", func(t *testing.T) {
		e := editEnv(t)
		before := e.storedApp(t, "duo")
		regs, deployments := e.Nomad.Registrations(), len(e.deployments(t, "duo"))

		f := e.configureForm(t, "duo")
		resp, body := e.submit(t, f, url.Values{
			"svc.web.env":     {"# the new list\r\nNEW=1\r\nOTHER=a=b\r\n"}, // MODE is removed
			"svc.web.memory":  {"192"},
			"svc.web.domains": {"duo.example.com\r\nwww.duo.example.com"},
		})
		recs := e.deployments(t, "duo")
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/duo/deployments#deployment-"+recs[0].Deployment.ID {
			t.Fatalf("POST = %d to %q, want 303 to the new deployment:\n%s", resp.StatusCode, location(t, resp), body)
		}
		if len(recs) != deployments+1 || recs[0].Deployment.Trigger != app.TriggerConsole || e.Nomad.Registrations() != regs+1 {
			t.Errorf("%d deployments (newest %q), %d registrations; want one more of each, from the console",
				len(recs), recs[0].Deployment.Trigger, e.Nomad.Registrations()-regs)
		}

		want := before
		want.Services = append([]app.Service(nil), before.Services...)
		want.Services[0].Env = []app.EnvVar{{Name: "NEW", Value: "1"}, {Name: "OTHER", Value: "a=b"}}
		want.Services[0].Resources.MemoryMB = 192
		want.Services[0].Domains = []app.Domain{{Host: "duo.example.com", Port: "http"}, {Host: "www.duo.example.com", Port: "http"}}
		if got := e.storedApp(t, "duo"); !reflect.DeepEqual(got, want) {
			t.Errorf("the stored app\n got: %+v\nwant: %+v", got, want)
		}
		// The deployment records what it deployed.
		if recs[0].Definition == nil || !reflect.DeepEqual(*recs[0].Definition, want) {
			t.Errorf("the deployment's definition = %+v, want the edited app", recs[0].Definition)
		}
	})

	t.Run("volumes, backup and secrets survive", func(t *testing.T) {
		e := backupsEnv(t, true) // duo's web is a backed-up postgres service
		a := e.storedApp(t, "duo")
		a.Services[0].Volumes = []app.Volume{{Name: "pgdata", Path: "/var/lib/postgresql/data"}}
		a.Services[0].Replicas = nil
		a.Services[0].Secrets = []app.Secret{{Name: "TOKEN", Value: new("secret-one")}}
		e.deploy(t, a)
		before := e.storedApp(t, "duo")

		resp, body := e.submit(t, e.configureForm(t, "duo"), url.Values{"svc.web.memory": {"200"}, "svc.worker.image": {"traefik/whoami:latest"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST = %d:\n%s", resp.StatusCode, text(body))
		}
		got := e.storedApp(t, "duo")
		web := got.Services[0]
		if !reflect.DeepEqual(web.Volumes, before.Services[0].Volumes) || !reflect.DeepEqual(web.Backup, before.Services[0].Backup) ||
			!reflect.DeepEqual(web.Secrets, before.Services[0].Secrets) || web.Resources.MemoryMB != 200 {
			t.Errorf("web = %+v, want the volume, backup and secret names kept and 200 MB", web)
		}
		if v := e.Nomad.Variable("nomad/jobs/duo/web"); v["TOKEN"] != "secret-one" {
			t.Errorf("the secret's value didn't survive: %v", v)
		}
		if strings.Contains(body, "secret-one") {
			t.Error("a secret value is in a page")
		}
	})

	t.Run("a refused value comes back where it was typed", func(t *testing.T) {
		e := editEnv(t)
		before, regs := e.storedApp(t, "duo"), e.Nomad.Registrations()
		f := e.configureForm(t, "duo")

		// One value the form can't read, and one the model refuses (a reservation above the ceiling).
		resp, body := e.submit(t, f, url.Values{"svc.web.copies": {"two"}, "svc.web.image": {"traefik/whoami:latest "}, "svc.web.env": {"A=1\nB=2"}})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d, want 422:\n%s", resp.StatusCode, text(body))
		}
		for _, want := range []string{
			`aria-invalid="true" aria-describedby="svc-web-copies-error"`,
			`id="svc-web-copies-error" role="alert"`, // the first error is announced
			`value="two"`,                            // what was typed, kept
			"A=1\nB=2",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the page lacks %q:\n%s", want, body)
			}
		}
		if !strings.Contains(text(body), "Enter a whole number, 1 or more.") || strings.Count(body, " autofocus") != 1 {
			t.Errorf("the message or the focus is wrong:\n%s", text(body))
		}

		resp, body = e.submit(t, f, url.Values{"svc.web.memory": {"512"}, "svc.web.memoryMax": {"256"}})
		if resp.StatusCode != http.StatusUnprocessableEntity ||
			!strings.Contains(body, `aria-describedby="svc-web-memory-error"`) || !strings.Contains(text(body), "The memory reservation reserves 512 MB") {
			t.Errorf("a reservation above the ceiling = %d, want 422 with the message at the memory input:\n%s", resp.StatusCode, text(body))
		}

		if e.Nomad.Registrations() != regs {
			t.Error("a refused save registered a job")
		}
		if got := e.storedApp(t, "duo"); !reflect.DeepEqual(got, before) {
			t.Error("a refused save changed the stored app")
		}

		// A path no input is for (a health check on a port the service doesn't have) goes to the
		// form's notice, naming it.
		a := duoApp()
		a.Services[0].Health.Port = "nope"
		e.seedOverwrite(t, a)
		resp, body = e.submit(t, e.configureForm(t, "duo"), nil)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "caravana can't deploy this app: services[0].health.port") {
			t.Errorf("a bad health port = %d, want 422 with the path in the notice:\n%s", resp.StatusCode, text(body))
		}

	})

	t.Run("a save from a stale form is refused, and the form shows what changed", func(t *testing.T) {
		e := editEnv(t)
		f := e.configureForm(t, "duo") // opened

		// Another writer saves a change.
		other := e.storedApp(t, "duo")
		other.Services[0].Resources.MemoryMB = 300
		other.Services[0].Env = []app.EnvVar{{Name: "MODE", Value: "someone-else"}}
		e.deploy(t, other)
		regs, deployments := e.Nomad.Registrations(), len(e.deployments(t, "duo"))
		stored := e.storedApp(t, "duo")

		resp, body := e.submit(t, f, url.Values{"svc.web.copies": {"3"}, "svc.web.env": {"MODE=mine"}})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("POST = %d, want 409:\n%s", resp.StatusCode, text(body))
		}
		got := text(body)
		for _, want := range []string{
			"This app changed since you opened the form.",
			"web Memory reserved (MB) 300", // the current value, where it differs from what was typed
			"web Environment MODE=someone-else",
			"Save again to deploy what is typed below",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the page lacks %q:\n%s", want, got)
			}
		}
		// What was typed is kept, and the form carries the current revision.
		fresh := formWith(t, body, "/apps/duo/configure")
		rec, _ := e.Store.GetApp(context.Background(), "duo")
		if fresh.Values.Get("svc.web.copies") != "3" || fresh.Values.Get("svc.web.env") != "MODE=mine" || fresh.Values.Get("revision") != rec.Revision {
			t.Errorf("the form came back with copies %q, env %q, revision %q (now %q)",
				fresh.Values.Get("svc.web.copies"), fresh.Values.Get("svc.web.env"), fresh.Values.Get("revision"), rec.Revision)
		}
		if e.Nomad.Registrations() != regs || len(e.deployments(t, "duo")) != deployments || !reflect.DeepEqual(e.storedApp(t, "duo"), stored) {
			t.Error("a refused stale save changed something")
		}

		// Saving again, from the form that came back, deploys what is typed over the new app: the
		// form replaces its fields whole, so the memory the other writer set goes back to what the
		// form has (the panel said so). The owner could have typed it first.
		resp, body = e.submit(t, fresh, url.Values{"svc.web.memory": {"300"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("second POST = %d:\n%s", resp.StatusCode, text(body))
		}
		after := e.storedApp(t, "duo")
		if after.Services[0].Replicas == nil || *after.Services[0].Replicas != 3 || after.Services[0].Resources.MemoryMB != 300 ||
			!reflect.DeepEqual(after.Services[0].Env, []app.EnvVar{{Name: "MODE", Value: "mine"}}) {
			t.Errorf("web = %+v, want 3 copies, 300 MB and MODE=mine", after.Services[0])
		}
	})

	t.Run("an image that doesn't exist: the message is at the image, the change is saved", func(t *testing.T) {
		e := editEnv(t)
		resp, body := e.submit(t, e.configureForm(t, "duo"), url.Values{"svc.web.image": {"traefik/whoami:nope"}})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d, want 422:\n%s", resp.StatusCode, text(body))
		}
		if !strings.Contains(body, `aria-describedby="svc-web-image-error"`) || !strings.Contains(text(body), "image not found") ||
			!strings.Contains(text(body), "The changes are saved; the failed attempt is on the Deployments tab.") {
			t.Errorf("the page lacks the message at the image field:\n%s", text(body))
		}
		// The failed attempt is recorded, and the form that came back saves again.
		if recs := e.deployments(t, "duo"); recs[0].Status != store.StatusFailed {
			t.Errorf("newest deployment is %s, want failed", recs[0].Status)
		}
		fixed := formWith(t, body, "/apps/duo/configure")
		resp, body = e.submit(t, fixed, url.Values{"svc.web.image": {"traefik/whoami:latest"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("fixing it = %d, want 303 (the form kept its revision up to date):\n%s", resp.StatusCode, text(body))
		}
	})

	t.Run("Nomad down", func(t *testing.T) {
		e := editEnv(t)
		e.Nomad.Down()
		resp, body := e.submit(t, e.configureForm(t, "duo"), url.Values{"svc.web.memory": {"200"}})
		if resp.StatusCode != http.StatusBadGateway || !strings.Contains(text(body), "Nomad can't be reached, so nothing was deployed.") {
			t.Errorf("POST = %d, want 502 with the notice:\n%s", resp.StatusCode, text(body))
		}
		if !strings.Contains(body, `value="200"`) {
			t.Error("the typed value was lost")
		}
	})

	t.Run("a plain post and an htmx post", func(t *testing.T) {
		e := editEnv(t)
		f := e.configureForm(t, "duo")
		if !f.HTMX {
			t.Error("the form doesn't post in place when script runs")
		}
		// htmx: the answer to a good post is a redirect it follows, to the entry.
		values := url.Values{}
		for k, v := range f.Values {
			values[k] = v
		}
		resp, _ := e.do(t, request{method: http.MethodPost, path: f.Action, form: values, cookies: []*http.Cookie{e.Cookie},
			headers: map[string]string{"Sec-Fetch-Site": "same-origin", "HX-Request": "true"}})
		if resp.StatusCode != http.StatusNoContent || !strings.HasPrefix(resp.Header.Get("HX-Redirect"), "/apps/duo/deployments#deployment-") {
			t.Errorf("htmx POST = %d with HX-Redirect %q, want 204 and the entry", resp.StatusCode, resp.Header.Get("HX-Redirect"))
		}
		// htmx: a refusal is the form alone, to swap in place.
		values.Set("svc.web.copies", "zero")
		resp, body := e.do(t, request{method: http.MethodPost, path: f.Action, form: values, cookies: []*http.Cookie{e.Cookie},
			headers: map[string]string{"Sec-Fetch-Site": "same-origin", "HX-Request": "true"}})
		if resp.StatusCode != http.StatusUnprocessableEntity || strings.Contains(body, "<html") || !strings.HasPrefix(strings.TrimSpace(body), "<form") ||
			!strings.Contains(body, `aria-describedby="svc-web-copies-error"`) {
			t.Errorf("htmx refusal = %d, want 422 with the form alone:\n%s", resp.StatusCode, body)
		}
	})

	t.Run("bad requests", func(t *testing.T) {
		e := editEnv(t)
		regs := e.Nomad.Registrations()
		resp, body := e.postForm(t, "/apps/duo/configure", url.Values{"svc.web.image": {"traefik/whoami:latest"}}) // no revision
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST without a revision = %d, want 400:\n%s", resp.StatusCode, text(body))
		}
		resp, _ = e.postForm(t, "/apps/ghost/configure", url.Values{"revision": {"x"}})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST for an unknown app = %d, want 404", resp.StatusCode)
		}
		resp, _ = e.postForm(t, "/apps/duo/configure", url.Values{"revision": {"x"}, "svc.web.env": {strings.Repeat("A=1\n", 80_000)}})
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("POST of 320 KB = %d, want 413", resp.StatusCode)
		}
		if e.Nomad.Registrations() != regs {
			t.Error("a bad request registered a job")
		}
	})
}

// seedOverwrite stores a over the stored app of the same ID without deploying it.
func (e *consoleEnv) seedOverwrite(t *testing.T, a app.App) {
	t.Helper()
	if _, err := e.Store.SaveApp(context.Background(), a, nil); err != nil {
		t.Fatalf("SaveApp(%s): %v", a.ID, err)
	}
}
