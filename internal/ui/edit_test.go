package ui

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// postOf is what a browser would post for the form: every shown input under
// its name, and the revision.
func postOf(f editForm) url.Values {
	post := url.Values{"revision": {f.Revision}}
	for _, s := range f.Services {
		for _, in := range s.fields() {
			if s.shows(in.Name) {
				post.Set(inputName(s.Name, in.Name), in.Field.Value)
			}
		}
	}
	return post
}

// richApp has a service with everything the form shows and everything it
// doesn't (two ports, a volume, a backup, secrets, a command and args, a
// health check on the second port), and a worker beside it.
func richApp() app.App {
	return app.App{
		ID: "shop", Name: "The Shop", Source: app.Source{Kind: app.SourceCompose},
		Services: []app.Service{
			{
				Name: "web", Image: "ghcr.io/acme/shop:1", Command: "/bin/server", Args: []string{"--port", "8080"},
				Env:       []app.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}, {Name: "FEATURES", Value: "a=1,b=2"}},
				Secrets:   []app.Secret{{Name: "API_KEY"}},
				Volumes:   []app.Volume{{Name: "uploads", Path: "/uploads"}},
				Ports:     []app.Port{{Name: "http", ContainerPort: 8080}, {Name: "metrics", ContainerPort: 9100}},
				Domains:   []app.Domain{{Host: "shop.example.com", Port: "http"}, {Host: "www.shop.example.com", Port: "http"}},
				Health:    &app.HealthCheck{Path: "/healthz", Port: "metrics", Interval: 15 * time.Second, Timeout: 1500 * time.Millisecond},
				Resources: app.Resources{CPUMHz: 250, MemoryMB: 256, MemoryMaxMB: 1024},
				Replicas:  new(1),
			},
			{
				Name: "db", Image: "postgres:17", Ports: []app.Port{{Name: "pg", ContainerPort: 5432}},
				Health: &app.HealthCheck{}, Backup: &app.Backup{Engine: app.BackupPostgres, Keep: 3},
				Secrets: []app.Secret{{Name: "POSTGRES_PASSWORD"}},
			},
			{Name: "worker", Image: "ghcr.io/acme/worker:1", Command: "/app/worker"},
		},
	}
}

// formApps is every example app plus richApp, secret values left out as stored.
func formApps() map[string]app.App {
	out := map[string]app.App{"rich": richApp()}
	for _, c := range apptest.Cases() {
		out[c.Name] = c.App.WithoutSecretValues()
	}
	return out
}

func TestFormRoundTrip(t *testing.T) {
	for name, stored := range formApps() {
		t.Run(name, func(t *testing.T) {
			want := formOf(stored, "rev1")
			got, ok := parseEditForm(postOf(want), stored)
			if !ok {
				t.Fatalf("the form drawn from the app doesn't parse: %+v", got)
			}
			if got.Revision != "rev1" {
				t.Errorf("Revision = %q, want rev1", got.Revision)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("parse(post(formOf(app))) differs from formOf(app)\n got: %+v\nwant: %+v", got, want)
			}
		})
	}
}

func TestFormShowsWhatTheServiceHas(t *testing.T) {
	f := formOf(richApp(), "r")
	want := map[string][3]bool{ // port, domains, health
		"web":    {false, true, true}, // two ports: no port input
		"db":     {true, true, true},
		"worker": {false, false, false}, // no ports
	}
	for _, s := range f.Services {
		if got := [3]bool{s.ShowPort, s.ShowDomains, s.ShowHealth}; got != want[s.Name] {
			t.Errorf("%s shows port/domains/health = %v, want %v", s.Name, got, want[s.Name])
		}
	}

	web, _ := f.service("web")
	for field, want := range map[string]string{
		"image":          "ghcr.io/acme/shop:1",
		"env":            "LOG_LEVEL=debug\nFEATURES=a=1,b=2",
		"domains":        "shop.example.com\nwww.shop.example.com",
		"copies":         "1",
		"healthPath":     "/healthz",
		"healthInterval": "15",
		"healthTimeout":  "1.5",
		"cpu":            "250",
		"memory":         "256",
		"memoryMax":      "1024",
	} {
		in, ok := f.field(inputName("web", field))
		if !ok || in.Value != want {
			t.Errorf("web %s = %q, want %q", field, in.Value, want)
		}
	}
	if web.Image.ID != "svc-web-image" || web.Memory.ID != "svc-web-memory" {
		t.Errorf("IDs = %q, %q; want svc-web-image and svc-web-memory", web.Image.ID, web.Memory.ID)
	}
	// Defaults show as empty inputs.
	db, _ := f.service("db")
	if db.Copies.Value != "" || db.CPU.Value != "" || db.HealthInterval.Value != "" {
		t.Errorf("db defaults = %q, %q, %q; want empty", db.Copies.Value, db.CPU.Value, db.HealthInterval.Value)
	}
}

func TestParseEditFormErrors(t *testing.T) {
	stored := richApp()
	tests := []struct {
		name, field, value string
		wantOn             string // the input the error lands on
		wantText           string // what the message says
	}{
		{"empty image", "image", "  ", "image", "Enter an image"},
		{"env line without =", "env", "A=1\n\nB", "env", "Line 3 has no"},
		{"env line without a name", "env", "# note\n=x", "env", "Line 2 has no name"},
		{"copies zero", "copies", "0", "copies", "1 or more"},
		{"copies text", "copies", "two", "copies", "whole number"},
		{"interval text", "healthInterval", "soon", "healthInterval", "seconds"},
		{"interval negative", "healthInterval", "-5", "healthInterval", "seconds"},
		{"timeout zero", "healthTimeout", "0", "healthTimeout", "seconds"},
		{"cpu text", "cpu", "fast", "cpu", "whole number"},
		{"cpu negative", "cpu", "-1", "cpu", "whole number"},
		{"memory decimal", "memory", "1.5", "memory", "whole number"},
		{"ceiling text", "memoryMax", "lots", "memoryMax", "whole number"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			post := postOf(formOf(stored, "r"))
			post.Set(inputName("web", tc.field), tc.value)
			f, ok := parseEditForm(post, stored)
			if ok {
				t.Fatal("ok = true, want an error")
			}
			// Exactly one input has an error, the right one, and it says why.
			var withErrors []string
			for _, s := range f.Services {
				for _, in := range s.fields() {
					if len(in.Field.Error) > 0 {
						withErrors = append(withErrors, inputName(s.Name, in.Name))
						if !strings.Contains(in.Field.Error.String(), tc.wantText) {
							t.Errorf("error = %q, want it to mention %q", in.Field.Error, tc.wantText)
						}
					}
				}
			}
			if want := inputName("web", tc.wantOn); !reflect.DeepEqual(withErrors, []string{want}) {
				t.Errorf("inputs with errors = %v, want [%s]", withErrors, want)
			}
			// The typed value is kept, untouched but for surrounding spaces.
			if in, _ := f.field(inputName("web", tc.field)); in.Value != strings.TrimSpace(tc.value) && tc.field != "env" {
				t.Errorf("kept value = %q, want what was typed (%q)", in.Value, tc.value)
			}
		})
	}

	t.Run("port of a one-port service", func(t *testing.T) {
		for _, bad := range []string{"", "abc", "0", "70000"} {
			post := postOf(formOf(stored, "r"))
			post.Set(inputName("db", "port"), bad)
			f, ok := parseEditForm(post, stored)
			db, _ := f.service("db")
			if ok || len(db.Port.Error) == 0 {
				t.Errorf("port %q: ok = %v, error = %q; want an error on the port", bad, ok, db.Port.Error)
			}
		}
	})

	t.Run("a domain for a service with several ports and none yet", func(t *testing.T) {
		a := richApp()
		a.Services[0].Domains = nil
		post := postOf(formOf(a, "r"))
		post.Set(inputName("web", "domains"), "new.example.com")
		f, ok := parseEditForm(post, a)
		web, _ := f.service("web")
		if ok || len(web.Domains.Error) == 0 {
			t.Errorf("ok = %v, error = %q; want an error on the domains", ok, web.Domains.Error)
		}
	})
}

func TestParseEditFormValues(t *testing.T) {
	stored := richApp()
	post := postOf(formOf(stored, "r"))
	// A browser posts textarea line breaks as CRLF.
	post.Set(inputName("web", "env"), "# comment\r\nNEW=a=b\r\n\r\n  SPACED = padded value \r\nEMPTY=\r\n")
	post.Set(inputName("web", "domains"), " a.example.com \r\n\r\nb.example.com\r\n")
	post.Set(inputName("web", "image"), "  ghcr.io/acme/shop:2  ")
	post.Set(inputName("web", "copies"), "3")
	post.Set(inputName("web", "healthInterval"), "30")
	post.Set(inputName("web", "healthTimeout"), "")
	post.Set(inputName("web", "cpu"), "")
	f, ok := parseEditForm(post, stored)
	if !ok {
		t.Fatalf("parse failed: %+v", f)
	}
	web, _ := f.service("web")
	want := serviceEdit{
		Image: "ghcr.io/acme/shop:2",
		Env: []app.EnvVar{
			{Name: "NEW", Value: "a=b"}, {Name: "SPACED", Value: " padded value "}, {Name: "EMPTY", Value: ""},
		},
		Domains:        []string{"a.example.com", "b.example.com"},
		Replicas:       new(3),
		HealthPath:     "/healthz",
		HealthInterval: 30 * time.Second,
		MemoryMB:       256,
		MemoryMaxMB:    1024,
	}
	if !reflect.DeepEqual(web.Edit, want) {
		t.Errorf("Edit = %+v\nwant   %+v", web.Edit, want)
	}
}

func TestParseEditFormMissingService(t *testing.T) {
	stored := richApp()
	// A form drawn before the worker was added knows nothing of it.
	post := postOf(formOf(stored, "r"))
	for name := range post {
		if strings.HasPrefix(name, "svc.worker.") {
			post.Del(name)
		}
	}
	f, ok := parseEditForm(post, stored)
	if !ok {
		t.Fatalf("parse failed: %+v", f)
	}
	if !reflect.DeepEqual(f, formOf(stored, "r")) {
		t.Error("a service missing from the post wasn't taken as stored")
	}
}

func TestEditFormField(t *testing.T) {
	f := formOf(richApp(), "r")
	for name, want := range map[string]bool{
		"svc.web.image": true, "svc.db.port": true, "svc.web.memoryMax": true,
		"svc.web.nope": false, "svc.nope.image": false, "image": false, "svc.web": false,
	} {
		if _, ok := f.field(name); ok != want {
			t.Errorf("field(%q) ok = %v, want %v", name, ok, want)
		}
	}

	f.Services[1].Port.Error = sentence{plain("x")}
	f.Services[0].Memory.Error = sentence{plain("y")}
	f.markFirstError()
	if f.Focus != "svc.web.memory" || !f.Services[0].Memory.Alert || f.Services[1].Port.Alert {
		t.Errorf("Focus = %q; want the first error in form order, announced alone", f.Focus)
	}
}
