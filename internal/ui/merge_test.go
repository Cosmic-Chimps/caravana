package ui

import (
	"reflect"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// SC-005: saving the form untouched changes nothing, for every example app,
// whether the form is read from the model or posted and parsed.
func TestApplyEditUntouched(t *testing.T) {
	for name, stored := range formApps() {
		t.Run(name, func(t *testing.T) {
			before := stored.WithoutSecretValues()
			form := formOf(stored, "r")
			if got := applyEdit(stored, form); !reflect.DeepEqual(got, stored) {
				t.Errorf("applyEdit(formOf(app)) changed the app\n got: %+v\nwant: %+v", got, stored)
			}
			posted, ok := parseEditForm(postOf(form), stored)
			if !ok {
				t.Fatal("the form doesn't parse")
			}
			if got := applyEdit(stored, posted); !reflect.DeepEqual(got, stored) {
				t.Errorf("applyEdit(parse(post)) changed the app\n got: %+v\nwant: %+v", got, stored)
			}
			if !reflect.DeepEqual(stored, before) {
				t.Error("applyEdit changed the stored app it was given")
			}
		})
	}
}

// A changed field changes only that field.
func TestApplyEditOneField(t *testing.T) {
	tests := []struct {
		name, input, value string
		change             func(a *app.App) // what the stored app becomes
	}{
		{"image", "web.image", "ghcr.io/acme/shop:2", func(a *app.App) { a.Services[0].Image = "ghcr.io/acme/shop:2" }},
		{"env", "web.env", "ONLY=this", func(a *app.App) { a.Services[0].Env = []app.EnvVar{{Name: "ONLY", Value: "this"}} }},
		{"env emptied", "web.env", "", func(a *app.App) { a.Services[0].Env = nil }},
		{"domains", "web.domains", "one.example.com\ntwo.example.com\nthree.example.com", func(a *app.App) {
			a.Services[0].Domains = []app.Domain{{Host: "one.example.com", Port: "http"}, {Host: "two.example.com", Port: "http"}, {Host: "three.example.com", Port: "http"}}
		}},
		{"domains emptied", "web.domains", "", func(a *app.App) { a.Services[0].Domains = nil }},
		{"port", "db.port", "5433", func(a *app.App) { a.Services[1].Ports[0].ContainerPort = 5433 }},
		{"copies", "web.copies", "4", func(a *app.App) { a.Services[0].Replicas = new(4) }},
		{"copies default", "web.copies", "", func(a *app.App) { a.Services[0].Replicas = nil }},
		{"health path", "web.healthPath", "/up", func(a *app.App) { a.Services[0].Health.Path = "/up" }},
		{"health TCP", "web.healthPath", "", func(a *app.App) { a.Services[0].Health.Path = "" }},
		{"health interval", "web.healthInterval", "20", func(a *app.App) { a.Services[0].Health.Interval = 20 * time.Second }},
		{"health timeout", "web.healthTimeout", "3", func(a *app.App) { a.Services[0].Health.Timeout = 3 * time.Second }},
		{"cpu", "web.cpu", "500", func(a *app.App) { a.Services[0].Resources.CPUMHz = 500 }},
		{"memory", "web.memory", "512", func(a *app.App) { a.Services[0].Resources.MemoryMB = 512 }},
		{"ceiling", "web.memoryMax", "", func(a *app.App) { a.Services[0].Resources.MemoryMaxMB = 0 }},
		{"worker image", "worker.image", "ghcr.io/acme/worker:2", func(a *app.App) { a.Services[2].Image = "ghcr.io/acme/worker:2" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stored := richApp()
			post := postOf(formOf(stored, "r"))
			post.Set("svc."+tc.input, tc.value)
			f, ok := parseEditForm(post, stored)
			if !ok {
				t.Fatalf("parse failed: %+v", f)
			}
			want := richApp()
			tc.change(&want)
			if got := applyEdit(stored, f); !reflect.DeepEqual(got, want) {
				t.Errorf("applyEdit\n got: %+v\nwant: %+v", got, want)
			}
		})
	}
}

// FR-003: whatever else the form changes, the fields it doesn't offer stay.
func TestApplyEditKeepsWhatTheFormDoesNotShow(t *testing.T) {
	stored := richApp()
	post := postOf(formOf(stored, "r"))
	for _, svc := range stored.Services {
		post.Set(inputName(svc.Name, "image"), "other/image:9")
		post.Set(inputName(svc.Name, "env"), "X=1")
		post.Set(inputName(svc.Name, "copies"), "1")
		post.Set(inputName(svc.Name, "cpu"), "999")
		post.Set(inputName(svc.Name, "memory"), "999")
		post.Set(inputName(svc.Name, "memoryMax"), "999")
	}
	post.Set(inputName("db", "port"), "6543")
	f, ok := parseEditForm(post, stored)
	if !ok {
		t.Fatalf("parse failed: %+v", f)
	}
	got := applyEdit(stored, f)

	if got.ID != stored.ID || got.Name != stored.Name || got.Source != stored.Source || len(got.Services) != len(stored.Services) {
		t.Errorf("the app's own fields or service list changed: %+v", got)
	}
	for i, want := range stored.Services {
		svc := got.Services[i]
		if svc.Name != want.Name || svc.Command != want.Command || !reflect.DeepEqual(svc.Args, want.Args) ||
			!reflect.DeepEqual(svc.Entrypoint, want.Entrypoint) ||
			!reflect.DeepEqual(svc.Secrets, want.Secrets) || !reflect.DeepEqual(svc.Volumes, want.Volumes) ||
			!reflect.DeepEqual(svc.Backup, want.Backup) {
			t.Errorf("service %s lost something the form doesn't show:\n got: %+v\nwant: %+v", want.Name, svc, want)
		}
		if svc.Image != "other/image:9" || svc.Resources.CPUMHz != 999 {
			t.Errorf("service %s didn't take the edit: %+v", want.Name, svc)
		}
	}
	web := got.Services[0]
	if !reflect.DeepEqual(web.Ports, stored.Services[0].Ports) {
		t.Errorf("ports of a two-port service changed: %+v", web.Ports)
	}
	if web.Health == nil || web.Health.Port != "metrics" {
		t.Errorf("the health check lost its port: %+v", web.Health)
	}
	if db := got.Services[1]; db.Ports[0].ContainerPort != 6543 || db.Ports[0].Name != "pg" {
		t.Errorf("db port = %+v, want 6543 on pg", db.Ports)
	}
	// And the stored app is untouched, including its shared slices.
	if !reflect.DeepEqual(stored, richApp()) {
		t.Error("applyEdit changed the stored app it was given")
	}
}

// New domains go on the port the service's domains already use, else on its
// only port.
func TestApplyEditDomainPort(t *testing.T) {
	a := richApp()
	a.Services[0].Domains = []app.Domain{{Host: "old.example.com", Port: "metrics"}}
	a.Services[1].Domains = nil
	post := postOf(formOf(a, "r"))
	post.Set(inputName("web", "domains"), "new.example.com")
	post.Set(inputName("db", "domains"), "db.example.com")
	f, ok := parseEditForm(post, a)
	if !ok {
		t.Fatalf("parse failed: %+v", f)
	}
	got := applyEdit(a, f)
	if want := []app.Domain{{Host: "new.example.com", Port: "metrics"}}; !reflect.DeepEqual(got.Services[0].Domains, want) {
		t.Errorf("web domains = %+v, want %+v", got.Services[0].Domains, want)
	}
	if want := []app.Domain{{Host: "db.example.com", Port: "pg"}}; !reflect.DeepEqual(got.Services[1].Domains, want) {
		t.Errorf("db domains = %+v, want %+v", got.Services[1].Domains, want)
	}
}

func TestFieldFor(t *testing.T) {
	a := richApp()
	tests := []struct {
		path string
		want string // "" for no input
	}{
		{"services[0].image", "svc.web.image"},
		{"services[0].env[1].name", "svc.web.env"},
		{"services[2].env[0].name", "svc.worker.env"},
		{"services[1].ports[0].containerPort", "svc.db.port"},
		{"services[1].ports[0].name", "svc.db.port"},
		{"services[0].domains[1].host", "svc.web.domains"},
		{"services[0].domains[0].port", "svc.web.domains"},
		{"services[0].replicas", "svc.web.copies"},
		{"services[0].health", "svc.web.healthPath"},
		{"services[0].health.path", "svc.web.healthPath"},
		{"services[0].health.interval", "svc.web.healthInterval"},
		{"services[1].health.timeout", "svc.db.healthTimeout"},
		{"services[0].resources.cpuMHz", "svc.web.cpu"},
		{"services[0].resources.memoryMB", "svc.web.memory"},
		{"services[0].resources.memoryMaxMB", "svc.web.memoryMax"},

		// Nothing in the form is for these.
		{"services[0].ports[1].containerPort", ""}, // web has two ports: no port input
		{"services[0].health.port", ""},
		{"services[2].health.path", ""}, // worker has no ports
		{"services[2].domains[0].host", ""},
		{"services[0].volumes[0].path", ""},
		{"services[0].secrets[0].name", ""},
		{"services[1].backup.keep", ""},
		{"services[0].name", ""},
		{"services[0].environment", ""}, // only a word that starts like env
		{"services[7].image", ""},       // no such service
		{"services[99999999999999999999].image", ""},
		{"id", ""},
		{"source.kind", ""},
		{"services", ""},
	}
	for _, tc := range tests {
		got, ok := fieldFor(tc.path, a)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("fieldFor(%q) = %q, %v; want %q", tc.path, got, ok, tc.want)
		}
	}

	// Every mapped name is an input of the form.
	f := formOf(a, "r")
	for _, tc := range tests {
		if got, ok := fieldFor(tc.path, a); ok {
			if _, found := f.field(got); !found {
				t.Errorf("fieldFor(%q) = %q, which the form has no input for", tc.path, got)
			}
		}
	}
}
