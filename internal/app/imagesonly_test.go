package app

import "testing"

func TestImagesOnlyChange(t *testing.T) {
	base := func() App {
		return App{ID: "web", Source: Source{Kind: SourceImage}, Services: []Service{
			{Name: "web", Image: "traefik/whoami:latest", Env: []EnvVar{{Name: "A", Value: "1"}}, Secrets: []Secret{{Name: "DB"}},
				Domains: []Domain{{Host: "web.example.com", Port: "http"}}},
			{Name: "worker", Image: "acme/worker:1"},
		}}
	}
	secretValue := "s3cret"
	tests := []struct {
		name   string
		change func(*App)
		field  string
		ok     bool
	}{
		{"the same app", func(*App) {}, "", true},
		{"one image", func(a *App) { a.Services[0].Image = "traefik/whoami:v2" }, "", true},
		{"both images", func(a *App) { a.Services[0].Image = "x/y:1"; a.Services[1].Image = "x/z:2" }, "", true},
		{"nil and empty are the same", func(a *App) { a.Services[1].Env = []EnvVar{}; a.Services[1].Ports = []Port{} }, "", true},
		{"an env var changed", func(a *App) { a.Services[0].Env[0].Value = "2" }, "services[0].env", false},
		{"an env var added", func(a *App) { a.Services[1].Env = []EnvVar{{Name: "B", Value: "1"}} }, "services[1].env", false},
		{"a secret value sent", func(a *App) { a.Services[0].Secrets[0].Value = &secretValue }, "services[0].secrets", false},
		{"a secret added", func(a *App) { a.Services[0].Secrets = append(a.Services[0].Secrets, Secret{Name: "X"}) }, "services[0].secrets", false},
		{"a domain changed", func(a *App) { a.Services[0].Domains[0].Host = "other.example.com" }, "services[0].domains", false},
		{"replicas", func(a *App) { a.Services[1].Replicas = new(3) }, "services[1].replicas", false},
		{"a command", func(a *App) { a.Services[0].Command = "/bin/sh" }, "services[0].command", false},
		{"a service renamed", func(a *App) { a.Services[1].Name = "jobs" }, "services[1].name", false},
		{"a service added", func(a *App) { a.Services = append(a.Services, Service{Name: "db", Image: "postgres:17"}) }, "services", false},
		{"a service dropped", func(a *App) { a.Services = a.Services[:1] }, "services", false},
		{"services reordered", func(a *App) { a.Services[0], a.Services[1] = a.Services[1], a.Services[0] }, "services[0].domains", false},
		{"the name", func(a *App) { a.Name = "Shop" }, "name", false},
		{"the id", func(a *App) { a.ID = "other" }, "id", false},
		{"the source", func(a *App) { a.Source.Kind = SourceCompose }, "source", false},
		{"an image and an env", func(a *App) { a.Services[0].Image = "x:1"; a.Services[0].Env = nil }, "services[0].env", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := base()
			tt.change(&next)
			field, ok := ImagesOnlyChange(base(), next)
			if ok != tt.ok || field != tt.field {
				t.Errorf("ImagesOnlyChange = %q, %v; want %q, %v", field, ok, tt.field, tt.ok)
			}
		})
	}

	t.Run("an app with no services", func(t *testing.T) {
		a := App{ID: "web", Source: Source{Kind: SourceImage}, Services: []Service{}}
		if field, ok := ImagesOnlyChange(a, App{ID: "web", Source: Source{Kind: SourceImage}}); !ok {
			t.Errorf("no services and nil services differ at %q", field)
		}
		if field, ok := ImagesOnlyChange(a, base()); ok || field != "services" {
			t.Errorf("giving an undefined app services = %q, %v; want refused at services", field, ok)
		}
	})
}
