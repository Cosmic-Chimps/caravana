package ui

import (
	"net/url"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// The Add panel's service select (the critique's ruling 7): drawn on /domains only once an app is
// picked, a service whose port isn't HTTP listed disabled, and the app named as the filter names it.

func TestNotHTTPPort(t *testing.T) {
	tcp := &app.HealthCheck{}
	tests := []struct {
		name string
		svc  app.Service
		port int
		want bool
	}{
		{"postgres port", app.Service{Image: "ghcr.io/acme/db:1"}, 5432, true},
		{"mysql port", app.Service{}, 3306, true},
		{"redis port", app.Service{}, 6379, true},
		{"mongo port", app.Service{}, 27017, true},
		{"rabbitmq port", app.Service{}, 5672, true},
		{"memcached port", app.Service{}, 11211, true},
		{"web", app.Service{Image: "traefik/whoami:latest", Health: &app.HealthCheck{Path: "/"}}, 80, false},
		{"redis image, TCP check", app.Service{Image: "redis:7", Health: tcp}, 6380, true},
		{"mariadb image, TCP check", app.Service{Image: "docker.io/library/MariaDB:11", Health: tcp}, 3307, true},
		{"postgres image, HTTP check", app.Service{Image: "postgres:17", Health: &app.HealthCheck{Path: "/health"}}, 8008, false},
		{"postgres image, no check", app.Service{Image: "postgres:17"}, 8008, false},
		{"web on a TCP check", app.Service{Image: "acme/web:1", Health: tcp}, 8080, false},
	}
	for _, tc := range tests {
		if got := notHTTPPort(tc.svc, tc.port); got != tc.want {
			t.Errorf("%s: notHTTPPort = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// stackApp is web (HTTP on :80) and db (Postgres on :5432, a TCP check) in one app.
func stackApp() app.App {
	return app.App{ID: "stack", Name: "Stack", Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{Name: "db", Image: "postgres:17", Ports: []app.Port{{Name: "pg", ContainerPort: 5432}}, Health: &app.HealthCheck{}},
			{Name: "web", Image: "traefik/whoami:latest", Ports: []app.Port{{Name: "http", ContainerPort: 80}},
				Domains: []app.Domain{{Host: "stack.example.com", Port: "http"}}, Health: &app.HealthCheck{Path: "/"}},
		}}
}

func TestDomainAddServiceSelect(t *testing.T) {
	e := addEnv(t)
	e.Reg.AddIndex("library/postgres", "17", otherDigest)
	e.deploy(t, stackApp())

	// Before a pick: no select, the default in words.
	panel := addPanel(e.pageOf(t, "/domains"))
	if strings.Contains(panel, `id="domain-service"`) || !strings.Contains(text(panel), "The app's first service.") {
		t.Errorf("before a pick:\n%s", text(panel))
	}
	// The app select redraws the field when it changes, by id.
	if !strings.Contains(panel, `hx-get="/parts/domains/add-service" hx-trigger="change" hx-target="#domain-service-field" hx-swap="outerHTML"`) ||
		!strings.Contains(panel, `id="domain-service-field"`) {
		t.Errorf("the app select doesn't redraw the service field:\n%s", panel)
	}

	// A pick: the part is the field alone, the database listed disabled and the web service chosen.
	part := e.pageOf(t, "/parts/domains/add-service?app=stack")
	for _, want := range []string{`id="domain-service-field"`, `<option value="db" disabled>db · :5432 · not an HTTP port</option>`,
		`<option value="web" selected>web · :80</option>`} {
		if !strings.Contains(part, want) {
			t.Errorf("the part lacks %s:\n%s", want, part)
		}
	}
	if strings.Contains(part, `<section`) || strings.Contains(part, `id="domain-app"`) {
		t.Errorf("the part is more than the field:\n%s", part)
	}
	// One service: its port as text. No app: the default's words.
	if part := e.pageOf(t, "/parts/domains/add-service?app=whoami"); !strings.Contains(text(part), ":80, set in Configure") || strings.Contains(part, `<select`) {
		t.Errorf("whoami's part:\n%s", part)
	}
	if part := e.pageOf(t, "/parts/domains/add-service"); !strings.Contains(text(part), "The app's first service.") {
		t.Errorf("the part without an app:\n%s", part)
	}

	// Check now draws the same select, without script.
	form := addFormOf(t, e.pageOf(t, "/domains"))
	panel, _ = e.checkNow(t, form, url.Values{"host": {"here.example.com"}, "app": {"stack"}})
	if !strings.Contains(panel, `<option value="db" disabled>db · :5432 · not an HTTP port</option>`) {
		t.Errorf("Check now's panel:\n%s", panel)
	}

	// An add with no service named goes to the first HTTP service, not the database.
	form = addFormOf(t, e.pageOf(t, "/domains"))
	set := url.Values{"host": {"new.stack.example.com"}, "app": {"stack"}, "service": {""}, "action": {"add"}}
	if resp, body := e.submit(t, form, set); resp.StatusCode != 303 {
		t.Fatalf("add = %d:\n%s", resp.StatusCode, text(body))
	}
	rec, err := e.Svc.GetApp(t.Context(), "stack")
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range rec.App.Services {
		for _, d := range svc.Domains {
			if d.Host == "new.stack.example.com" && svc.Name != "web" {
				t.Errorf("the domain went to %s, want web", svc.Name)
			}
		}
	}
}

// An app whose only target isn't HTTP is listed, disabled, in the app select.
func TestDomainAddAppWithoutHTTP(t *testing.T) {
	e := addEnv(t)
	e.Reg.AddIndex("library/postgres", "17", otherDigest)
	db := stackApp()
	db.ID, db.Services = "pg", db.Services[:1]
	e.deploy(t, db)
	panel := addPanel(e.pageOf(t, "/domains"))
	if !strings.Contains(panel, `<option value="pg" disabled>pg (no HTTP port)</option>`) {
		t.Errorf("pg in the app select:\n%s", panel)
	}
}

// The filter's project select and the app selects use the same words: the project's display name,
// the environment, the app.
func TestDomainAppLabelsUseTheDisplayName(t *testing.T) {
	e := shopConsole(t)
	e.deploy(t, shopWeb("staging", "latest"))
	body := e.pageOf(t, "/domains")
	if !strings.Contains(body, `<option value="shop">Shop Floor</option>`) {
		t.Errorf("the project select:\n%s", body)
	}
	id := shopWeb("staging", "latest").ID
	filter := body[strings.Index(body, `id="domains-filter"`):]
	filter = filter[:strings.Index(filter, "</form>")]
	if !strings.Contains(filter, `<option value="`+id+`">Shop Floor / staging / web</option>`) {
		t.Errorf("the filter's app select:\n%s", filter)
	}
	if panel := addPanel(body); !strings.Contains(panel, `<option value="`+id+`" selected>Shop Floor / staging / web</option>`) &&
		!strings.Contains(panel, `>Shop Floor / staging / web</option>`) {
		t.Errorf("the Add panel's app select:\n%s", panel)
	}
}
