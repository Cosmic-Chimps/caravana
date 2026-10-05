package ui

import (
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// postedForm is what a browser sends for a filled-in New app form.
func postedForm(name, image, port, domain, health string) url.Values {
	return url.Values{"name": {name}, "image": {image}, "port": {port}, "domain": {domain}, "healthPath": {health}}
}

// whoamiForm is whoami with a port, a domain and a health path.
func whoamiForm() url.Values {
	return postedForm("whoami", "traefik/whoami:latest", "80", "whoami.example.com", "/")
}

func TestNewAppFormRenders(t *testing.T) {
	e := newConsoleEnv(t)
	resp, body := e.get(t, "/apps/new")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /apps/new = %d:\n%s", resp.StatusCode, body)
	}
	if got := h1Text(t, body); got != "New app" {
		t.Errorf("headline = %q, want New app", got)
	}

	for _, want := range []string{
		// one form, a plain post that htmx improves in place
		`<form method="post" action="/apps" hx-post="/apps" hx-target="this" hx-swap="outerHTML"`,
		// the five fields, labels above them
		`name="name"`, `name="image"`, `name="port"`, `name="domain"`, `name="healthPath" value="/"`,
		">Name<", ">Image<", ">Container port<", ">Domain<", ">Health check path<",
		// hints and the note about workers
		"Lowercase letters, digits and dashes. It names the app and its job.",
		"e.g. <span class=\"cv-inline-mono\">ghcr.io/you/app:1.4</span>",
		"Needs a port. HTTPS is set up for it.",
		"Without a port this runs as a worker: no address, no health check.",
		`>Create and deploy</button>`,
		// the shell: Apps stays the current nav entry, breadcrumbs end here
		`<a href="/apps" class="cv-nav-link" aria-current="page">Apps</a>`, `aria-current="page">New app</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `role="alert"`) || strings.Contains(body, `aria-invalid`) {
		t.Error("a fresh form shows errors")
	}
	if strings.Contains(body, "sse-connect") {
		t.Error("the form opens a live stream")
	}
}

func TestNewAppActionIsOffered(t *testing.T) {
	e := newConsoleEnv(t)
	s := newTestServer(t, e.uiEnv)
	if !s.newApp {
		t.Error("newServer leaves the New app action off")
	}

	_, empty := e.get(t, "/apps")
	if !strings.Contains(empty, `<a href="/apps/new" class="cv-btn cv-btn-primary">New app</a>`) {
		t.Errorf("the empty Apps page doesn't offer New app:\n%s", empty)
	}
	e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerConsole, Status: store.StatusHealthy, Version: 1})
	_, listed := e.get(t, "/apps")
	if !strings.Contains(listed, `<a href="/apps/new" class="cv-btn cv-btn-primary cv-btn-sm">New app</a>`) {
		t.Errorf("the Apps page doesn't offer New app in its top bar:\n%s", listed)
	}
}

func TestCreateAppFromTheConsole(t *testing.T) {
	t.Run("a valid form creates the app and lands on its Deployments tab", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.postForm(t, "/apps", whoamiForm())
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/whoami/deployments" {
			t.Fatalf("POST /apps = %d to %q, want 303 to /apps/whoami/deployments:\n%s", resp.StatusCode, location(t, resp), body)
		}
		want := app.App{ID: "whoami", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{
			Name: "whoami", Image: "traefik/whoami:latest",
			Ports:   []app.Port{{Name: "http", ContainerPort: 80}},
			Domains: []app.Domain{{Host: "whoami.example.com", Port: "http"}},
			Health:  &app.HealthCheck{Path: "/"},
		}}}
		if got := e.storedApp(t, "whoami"); !reflect.DeepEqual(got, want) {
			t.Errorf("stored app:\n got %+v\nwant %+v", got, want)
		}
		recs := e.deployments(t, "whoami")
		if len(recs) != 1 || recs[0].Deployment.Trigger != app.TriggerConsole {
			t.Fatalf("deployments = %+v, want one with trigger console", recs)
		}
		if e.Nomad.Registrations() != 1 {
			t.Errorf("registrations = %d, want 1", e.Nomad.Registrations())
		}
		_, page := e.get(t, location(t, resp))
		if !strings.Contains(page, `id="deployment-`+recs[0].Deployment.ID+`"`) || !strings.Contains(text(page), "from the console") {
			t.Errorf("the first deployment isn't on the tab:\n%s", text(page))
		}
	})

	t.Run("an empty health path is a TCP check", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.postForm(t, "/apps", postedForm("whoami", "traefik/whoami:latest", "80", "", ""))
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST = %d:\n%s", resp.StatusCode, body)
		}
		got := e.storedApp(t, "whoami").Services[0]
		if got.Health == nil || got.Health.Path != "" || len(got.Domains) != 0 || len(got.Ports) != 1 {
			t.Errorf("service = %+v, want a port, no domain, a health check without a path", got)
		}
	})

	t.Run("no port makes a worker: no ports, domains or health", func(t *testing.T) {
		e := newConsoleEnv(t)
		// The health path is still posted (the field has its default); without a port it has no meaning.
		resp, body := e.postForm(t, "/apps", postedForm("whoami", "traefik/whoami:latest", "", "", "/"))
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST = %d:\n%s", resp.StatusCode, body)
		}
		got := e.storedApp(t, "whoami").Services[0]
		if len(got.Ports) != 0 || len(got.Domains) != 0 || got.Health != nil {
			t.Errorf("service = %+v, want a worker", got)
		}
	})

	t.Run("a taken name is refused on Name and the app is untouched", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.seed(t, whoamiApp(), seedDeploy{ID: "w1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
		before := e.storedApp(t, "whoami")
		regs := e.Nomad.Registrations()

		resp, body := e.postForm(t, "/apps", postedForm("whoami", "traefik/whoami:latest", "8080", "other.example.com", "/up"))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d, want 422:\n%s", resp.StatusCode, body)
		}
		field := fieldBlock(t, body, "name")
		if got := text(field); !strings.Contains(got, "An app called whoami already exists.") || !strings.Contains(field, `role="alert"`) || !strings.Contains(field, `aria-invalid="true"`) {
			t.Errorf("the Name field doesn't say so: %s", field)
		}
		// What was typed stays.
		for _, want := range []string{`name="image" value="traefik/whoami:latest"`, `name="port" value="8080"`, `name="domain" value="other.example.com"`, `name="healthPath" value="/up"`} {
			if !strings.Contains(body, want) {
				t.Errorf("the refused form lost %q:\n%s", want, body)
			}
		}
		if after := e.storedApp(t, "whoami"); !reflect.DeepEqual(after, before) {
			t.Errorf("the stored app changed:\n got %+v\nwant %+v", after, before)
		}
		if n := len(e.deployments(t, "whoami")); n != 1 || e.Nomad.Registrations() != regs {
			t.Errorf("%d deployments, %d new registrations; want 1 and 0", n, e.Nomad.Registrations()-regs)
		}
	})

	t.Run("each problem shows on its own field, with what was typed kept, and nothing is saved", func(t *testing.T) {
		tests := []struct {
			name   string
			form   url.Values
			field  string // the form field that carries the error
			want   string
			status int
		}{
			{"name that isn't a DNS label", postedForm("My App", "traefik/whoami:latest", "80", "", "/"), "name", "is not a lowercase DNS label", 422},
			{"name too long for a service name", postedForm(strings.Repeat("a", 32), "traefik/whoami:latest", "", "", ""), "name", "at most 31 characters", 422},
			{"no name", postedForm("", "traefik/whoami:latest", "", "", ""), "name", "Enter a name", 422},
			{"no image", postedForm("whoami", "", "", "", ""), "image", "Enter an image", 422},
			{"image with a space", postedForm("whoami", "traefik whoami", "", "", ""), "image", "The image must not contain spaces", 422},
			{"unsupported registry", postedForm("whoami", "quay.io/x/y:1", "", "", ""), "image", "Docker Hub and GHCR only", 422},
			{"port that isn't a number", postedForm("whoami", "traefik/whoami:latest", "http", "", "/"), "port", "a number from 1 to 65535", 422},
			{"port out of range", postedForm("whoami", "traefik/whoami:latest", "70000", "", "/"), "port", "a number from 1 to 65535", 422},
			{"domain without a port", postedForm("whoami", "traefik/whoami:latest", "", "whoami.example.com", "/"), "domain", "needs a port", 422},
			{"domain that isn't a hostname", postedForm("whoami", "traefik/whoami:latest", "80", "Not A Host", "/"), "domain", "is not a lowercase hostname", 422},
			{"health path without a slash", postedForm("whoami", "traefik/whoami:latest", "80", "", "healthz"), "healthPath", "must start with", 422},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				e := newConsoleEnv(t)
				resp, body := e.postForm(t, "/apps", tc.form)
				if resp.StatusCode != tc.status {
					t.Fatalf("POST = %d, want %d:\n%s", resp.StatusCode, tc.status, body)
				}
				field := fieldBlock(t, body, tc.field)
				if got := text(field); !strings.Contains(got, tc.want) || !strings.Contains(field, `aria-invalid="true"`) {
					t.Errorf("the %s field doesn't say %q: %s", tc.field, tc.want, got)
				}
				if n := strings.Count(body, `aria-invalid="true"`); n != 1 {
					t.Errorf("%d fields are marked invalid, want only %s", n, tc.field)
				}
				// The first error is the one announced, and its field is where focus lands.
				if !strings.Contains(field, `role="alert"`) || !strings.Contains(field, "autofocus") {
					t.Errorf("the first error isn't announced and focused: %s", field)
				}
				for name, vals := range tc.form {
					if !strings.Contains(body, `name="`+name+`" value="`+template2(vals[0])+`"`) {
						t.Errorf("the refused form lost %s = %q", name, vals[0])
					}
				}
				if _, err := e.Store.GetApp(t.Context(), tc.form.Get("name")); err == nil {
					t.Error("a refused form saved the app")
				}
				if e.Nomad.Registrations() != 0 {
					t.Error("a refused form registered a job")
				}
			})
		}
	})

	t.Run("a port that isn't a number is the one problem shown: the domain is judged once the port is right", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.postForm(t, "/apps", postedForm("whoami", "traefik/whoami:latest", "http", "whoami.example.com", "/"))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d:\n%s", resp.StatusCode, body)
		}
		if n := strings.Count(body, `aria-invalid="true"`); n != 1 {
			t.Errorf("%d fields are invalid, want 1 (the port)", n)
		}
		if n := strings.Count(body, `role="alert"`); n != 1 {
			t.Errorf("%d alerts, want 1", n)
		}
	})

	t.Run("an image that isn't there: the error is on Image, and the form says the app was saved", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.postForm(t, "/apps", postedForm("whoami", "traefik/whoami:nope", "80", "", "/"))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d, want 422:\n%s", resp.StatusCode, body)
		}
		if got := text(fieldBlock(t, body, "image")); !strings.Contains(got, "image not found") {
			t.Errorf("the Image field doesn't say so: %s", got)
		}
		// The first deploy was recorded, so the name is taken now: the form points at the tab that can retry.
		if got := text(body); !strings.Contains(got, "whoami was saved, but its first deploy didn't start") || !strings.Contains(body, `href="/apps/whoami/deployments"`) {
			t.Errorf("the form doesn't point at the app's Deployments tab: %s", got)
		}
		recs := e.deployments(t, "whoami")
		if len(recs) != 1 || recs[0].Status != store.StatusFailed {
			t.Errorf("deployments = %+v, want one failed", recs)
		}
	})

	t.Run("Nomad down: a sentence, and the form points at the Deployments tab", func(t *testing.T) {
		e := newConsoleEnv(t)
		e.Nomad.Down()
		resp, body := e.postForm(t, "/apps", whoamiForm())
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("POST = %d, want 502:\n%s", resp.StatusCode, body)
		}
		if got := text(body); !strings.Contains(got, "Nomad couldn't be reached") || !strings.Contains(got, "whoami was saved, but its first deploy didn't start") {
			t.Errorf("the page doesn't explain it: %s", got)
		}
		if !strings.Contains(body, `role="alert"`) {
			t.Error("the sentence isn't announced")
		}
	})
}

// template2 is a value as html/template writes it into an attribute: the few characters it
// escapes there. The test values only need the ones that appear in them.
func template2(v string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;").Replace(v)
}

// fieldBlock is the <label> of the form field with the given name: its input and its error.
func fieldBlock(t *testing.T, body, name string) string {
	t.Helper()
	i := strings.Index(body, `name="`+name+`"`)
	if i < 0 {
		t.Fatalf("no field %q in:\n%s", name, body)
	}
	start := strings.LastIndex(body[:i], "<label")
	end := strings.Index(body[i:], "</label>")
	if start < 0 || end < 0 {
		t.Fatalf("field %q isn't inside a label:\n%s", name, body)
	}
	return body[start : i+end]
}

func TestCreateAppWorksAsAPlainFormPost(t *testing.T) {
	e := newConsoleEnv(t)
	_, page := e.get(t, "/apps/new")
	m := regexp.MustCompile(`<form method="post" action="(/apps)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no New app form:\n%s", page)
	}
	// What a browser sends: the fields, urlencoded, and nothing from htmx.
	resp, _ := e.postForm(t, m[1], whoamiForm())
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/whoami/deployments" || resp.Header.Get("HX-Redirect") != "" {
		t.Errorf("POST = %d to %q (HX-Redirect %q), want a plain 303", resp.StatusCode, location(t, resp), resp.Header.Get("HX-Redirect"))
	}
	// A refusal draws the whole page, with the shell.
	resp, body := e.postForm(t, "/apps", postedForm("My App", "traefik/whoami:latest", "", "", ""))
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "<html") || !strings.Contains(body, "cv-side") {
		t.Errorf("a refused plain post = %d, want the whole page:\n%s", resp.StatusCode, body)
	}
}

func TestCreateAppWithHtmx(t *testing.T) {
	htmx := map[string]string{"HX-Request": "true", "Sec-Fetch-Site": "same-origin"}

	t.Run("success sends the browser to the Deployments tab", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.do(t, request{method: http.MethodPost, path: "/apps", form: whoamiForm(), headers: htmx, cookies: []*http.Cookie{e.Cookie}})
		if resp.StatusCode/100 != 2 || resp.Header.Get("HX-Redirect") != "/apps/whoami/deployments" {
			t.Errorf("POST = %d, HX-Redirect %q; want 2xx and HX-Redirect to the tab:\n%s", resp.StatusCode, resp.Header.Get("HX-Redirect"), body)
		}
	})

	t.Run("a refusal is only the form, to swap in place", func(t *testing.T) {
		e := newConsoleEnv(t)
		resp, body := e.do(t, request{method: http.MethodPost, path: "/apps", form: postedForm("My App", "traefik/whoami:latest", "", "", ""), headers: htmx, cookies: []*http.Cookie{e.Cookie}})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("POST = %d, want 422", resp.StatusCode)
		}
		if !strings.HasPrefix(strings.TrimSpace(body), "<form") || strings.Contains(body, "<html") || !strings.Contains(body, `name="name" value="My App"`) {
			t.Errorf("the answer isn't just the form with its values:\n%s", body)
		}
		if !strings.Contains(body, `hx-post="/apps"`) {
			t.Error("the swapped-in form can't post again")
		}
	})
}

func TestNewAppNeedsASessionAndSameOrigin(t *testing.T) {
	e := newConsoleEnv(t)

	resp, _ := e.do(t, request{path: "/apps/new"})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login?next=") {
		t.Errorf("GET /apps/new without a session = %d to %q, want 303 to the sign-in", resp.StatusCode, location(t, resp))
	}
	resp, _ = e.do(t, request{method: http.MethodPost, path: "/apps", form: whoamiForm(), headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login") {
		t.Errorf("POST /apps without a session = %d to %q, want 303 to the sign-in", resp.StatusCode, location(t, resp))
	}
	resp, body := e.do(t, request{method: http.MethodPost, path: "/apps", form: whoamiForm(),
		headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, cookies: []*http.Cookie{e.Cookie}})
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "refused") {
		t.Errorf("cross-site POST /apps = %d, want the 403 page", resp.StatusCode)
	}
	if _, err := e.Store.GetApp(t.Context(), "whoami"); err == nil {
		t.Error("a refused post created an app")
	}
}

func TestNewAppPageObeysThePolicy(t *testing.T) {
	e := newConsoleEnv(t)
	for name, body := range map[string]string{
		"the form": func() string { _, b := e.get(t, "/apps/new"); return b }(),
		"a refusal": func() string {
			_, b := e.postForm(t, "/apps", postedForm("My App", "traefik/whoami:latest", "", "", ""))
			return b
		}(),
	} {
		for _, banned := range []string{" style=", "<style", "hx-on", "javascript:", " onclick=", " onsubmit="} {
			if strings.Contains(body, banned) {
				t.Errorf("%s contains %q", name, banned)
			}
		}
		for _, tag := range regexp.MustCompile(`(?i)<script\b[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(tag, ` src="/static/js/`) {
				t.Errorf("%s has an inline script: %s", name, tag)
			}
		}
	}
}
