package app

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var testDigest = "ghcr.io/acme/shop@sha256:" + strings.Repeat("a", 64)

// validApp is a web service with a domain plus a worker without ports.
// Index paths in the cases below assume web is services[0], worker services[1].
func validApp() App {
	return App{
		ID:     "shop",
		Name:   "Shop",
		Source: Source{Kind: SourceImage},
		Services: []Service{
			{
				Name:    "web",
				Image:   "ghcr.io/acme/shop:main",
				Ports:   []Port{{Name: "http", ContainerPort: 8080}},
				Domains: []Domain{{Host: "shop.example.com", Port: "http"}},
				Health:  &HealthCheck{Path: "/healthz"},
			},
			{
				Name:    "worker",
				Image:   "ghcr.io/acme/shop:main",
				Command: "/app/worker",
			},
		},
	}
}

func validDeployment() Deployment {
	return Deployment{
		ID:      "d1",
		Trigger: TriggerManual,
		Digests: map[string]string{"web": testDigest, "worker": testDigest},
	}
}

func TestValidate(t *testing.T) {
	sha := "@sha256:" + strings.Repeat("a", 64)

	tests := []struct {
		name      string
		mutate    func(a *App, d *Deployment)
		wantField string // empty means valid
	}{
		// Valid input.
		{"valid", func(a *App, d *Deployment) {}, ""},
		{"valid registry with port", func(a *App, d *Deployment) { d.Digests["web"] = "localhost:5000/shop" + sha }, ""},
		{"valid docker hub digest", func(a *App, d *Deployment) { d.Digests["web"] = "traefik/whoami" + sha }, ""},
		{"valid single-label host", func(a *App, d *Deployment) { a.Services[0].Domains[0].Host = "localhost" }, ""},
		{"valid args without command", func(a *App, d *Deployment) {
			a.Services[1].Command = ""
			a.Services[1].Args = []string{"--once"}
		}, ""},
		{"valid webhook trigger", func(a *App, d *Deployment) { d.Trigger = TriggerWebhook }, ""},
		{"valid console trigger", func(a *App, d *Deployment) { d.Trigger = TriggerConsole }, ""},
		{"valid replicas", func(a *App, d *Deployment) { a.Services[0].Replicas = new(3) }, ""},
		{"valid empty name", func(a *App, d *Deployment) { a.Name = "" }, ""},
		{"valid TCP check on only port", func(a *App, d *Deployment) {
			a.Services[0].Domains = nil
			a.Services[0].Health = &HealthCheck{}
		}, ""},
		{"valid check on second port", func(a *App, d *Deployment) {
			a.Services[0].Ports = append(a.Services[0].Ports, Port{Name: "metrics", ContainerPort: 9100})
			a.Services[0].Health.Port = "metrics"
		}, ""},

		// App.
		{"id empty", func(a *App, d *Deployment) { a.ID = "" }, "id"},
		{"id uppercase", func(a *App, d *Deployment) { a.ID = "Shop" }, "id"},
		{"id leading hyphen", func(a *App, d *Deployment) { a.ID = "-shop" }, "id"},
		{"id underscore", func(a *App, d *Deployment) { a.ID = "shop_1" }, "id"},
		{"id too long", func(a *App, d *Deployment) { a.ID = strings.Repeat("a", 64) }, "id"},
		{"name with NUL", func(a *App, d *Deployment) { a.Name = "bad\x00name" }, "name"},
		{"name with newline", func(a *App, d *Deployment) { a.Name = "line\nbreak" }, "name"},
		{"source kind empty", func(a *App, d *Deployment) { a.Source.Kind = "" }, "source.kind"},
		{"no services", func(a *App, d *Deployment) { a.Services = nil }, "services"},

		// Service name and image.
		{"service name empty", func(a *App, d *Deployment) { a.Services[0].Name = "" }, "services[0].name"},
		{"service name uppercase", func(a *App, d *Deployment) { a.Services[0].Name = "Web" }, "services[0].name"},
		{"service name duplicate", func(a *App, d *Deployment) { a.Services[1].Name = "web" }, "services[1].name"},
		{"app and service name too long", func(a *App, d *Deployment) {
			a.ID = strings.Repeat("a", 40)
			a.Services[0].Name = strings.Repeat("b", 30)
		}, "services[0].name"},
		{"image empty", func(a *App, d *Deployment) { a.Services[0].Image = "" }, "services[0].image"},
		{"image with space", func(a *App, d *Deployment) { a.Services[0].Image = "ghcr.io/acme/shop main" }, "services[0].image"},

		// Replicas and resources.
		{"replicas zero", func(a *App, d *Deployment) { a.Services[0].Replicas = new(0) }, "services[0].replicas"},
		{"replicas negative", func(a *App, d *Deployment) { a.Services[0].Replicas = new(-1) }, "services[0].replicas"},
		{"cpu negative", func(a *App, d *Deployment) { a.Services[0].Resources.CPUMHz = -1 }, "services[0].resources.cpuMHz"},
		{"memory negative", func(a *App, d *Deployment) { a.Services[0].Resources.MemoryMB = -1 }, "services[0].resources.memoryMB"},

		// Env.
		{"env name empty", func(a *App, d *Deployment) { a.Services[0].Env = []EnvVar{{Name: "", Value: "x"}} }, "services[0].env[0].name"},
		{"env name with =", func(a *App, d *Deployment) { a.Services[0].Env = []EnvVar{{Name: "A=B"}} }, "services[0].env[0].name"},
		{"env name with NUL", func(a *App, d *Deployment) { a.Services[0].Env = []EnvVar{{Name: "A\x00"}} }, "services[0].env[0].name"},
		{"env name duplicate", func(a *App, d *Deployment) {
			a.Services[0].Env = []EnvVar{{Name: "A", Value: "1"}, {Name: "A", Value: "2"}}
		}, "services[0].env[1].name"},

		// Secrets.
		{"valid secrets, kept and set", func(a *App, d *Deployment) {
			a.Services[0].Secrets = []Secret{{Name: "DATABASE_URL"}, {Name: "_api_key2", Value: new("line\n\"q\" $X {{y}} é")}}
		}, ""},
		{"valid empty secret value", func(a *App, d *Deployment) { a.Services[0].Secrets = []Secret{{Name: "A", Value: new("")}} }, ""},
		{"valid same secret name in two services", func(a *App, d *Deployment) {
			a.Services[0].Secrets = []Secret{{Name: "A"}}
			a.Services[1].Secrets = []Secret{{Name: "A"}}
		}, ""},
		{"secret name empty", func(a *App, d *Deployment) { a.Services[0].Secrets = []Secret{{Name: ""}} }, "services[0].secrets[0].name"},
		{"secret name starts with a digit", func(a *App, d *Deployment) { a.Services[0].Secrets = []Secret{{Name: "1A"}} }, "services[0].secrets[0].name"},
		{"secret name with a dot", func(a *App, d *Deployment) { a.Services[1].Secrets = []Secret{{Name: "A.B"}} }, "services[1].secrets[0].name"},
		{"secret name duplicate", func(a *App, d *Deployment) {
			a.Services[0].Secrets = []Secret{{Name: "A"}, {Name: "A", Value: new("x")}}
		}, "services[0].secrets[1].name"},
		{"secret name also in env", func(a *App, d *Deployment) {
			a.Services[0].Env = []EnvVar{{Name: "PORT", Value: "80"}}
			a.Services[0].Secrets = []Secret{{Name: "PORT", Value: new("x")}}
		}, "services[0].secrets[0].name"},
		{"secret value with NUL", func(a *App, d *Deployment) { a.Services[0].Secrets = []Secret{{Name: "A", Value: new("a\x00b")}} }, "services[0].secrets[0].value"},
		{"secret value not UTF-8", func(a *App, d *Deployment) { a.Services[0].Secrets = []Secret{{Name: "A", Value: new("\xff")}} }, "services[0].secrets[0].value"},

		// Source kinds and volumes.
		{"valid compose source", func(a *App, d *Deployment) { a.Source.Kind = SourceCompose }, ""},
		{"unknown source", func(a *App, d *Deployment) { a.Source.Kind = "git" }, "source.kind"},
		{"valid volumes", func(a *App, d *Deployment) {
			a.Services[1].Volumes = []Volume{{Name: "db_data-1", Path: "/var/lib/data"}, {Name: "cache", Path: "/cache", ReadOnly: true}}
		}, ""},
		{"volume name uppercase", func(a *App, d *Deployment) { a.Services[1].Volumes = []Volume{{Name: "Data", Path: "/d"}} }, "services[1].volumes[0].name"},
		{"volume name with a dot", func(a *App, d *Deployment) { a.Services[1].Volumes = []Volume{{Name: "a.b", Path: "/d"}} }, "services[1].volumes[0].name"},
		{"volume path relative", func(a *App, d *Deployment) { a.Services[1].Volumes = []Volume{{Name: "d", Path: "data"}} }, "services[1].volumes[0].path"},
		{"volume path root", func(a *App, d *Deployment) { a.Services[1].Volumes = []Volume{{Name: "d", Path: "/"}} }, "services[1].volumes[0].path"},
		{"volume path not clean", func(a *App, d *Deployment) { a.Services[1].Volumes = []Volume{{Name: "d", Path: "/a/../b"}} }, "services[1].volumes[0].path"},
		{"volume path twice", func(a *App, d *Deployment) {
			a.Services[1].Volumes = []Volume{{Name: "a", Path: "/d"}, {Name: "b", Path: "/d"}}
		}, "services[1].volumes[1].path"},
		{"volume in two services", func(a *App, d *Deployment) {
			a.Services[0].Volumes = []Volume{{Name: "shared", Path: "/d"}}
			a.Services[1].Volumes = []Volume{{Name: "shared", Path: "/d"}}
		}, "services[1].volumes[0].name"},
		{"volume with two copies", func(a *App, d *Deployment) {
			a.Services[1].Volumes = []Volume{{Name: "d", Path: "/d"}}
			a.Services[1].Replicas = new(2)
		}, "services[1].replicas"},

		// Backups (web has a port; the worker has none).
		{"valid backup with a password in env", func(a *App, d *Deployment) {
			a.Services[0].Env = []EnvVar{{Name: "POSTGRES_PASSWORD", Value: "pw"}}
			a.Services[0].Backup = &Backup{Engine: BackupPostgres, Schedule: "*/15 * * * *", Keep: 3}
		}, ""},
		{"valid backup with a password secret", func(a *App, d *Deployment) {
			a.Services[0].Secrets = []Secret{{Name: "POSTGRES_PASSWORD"}}
			a.Services[0].Backup = &Backup{Engine: BackupPostgres}
		}, ""},
		{"backup engine mysql", func(a *App, d *Deployment) {
			a.Services[0].Env = []EnvVar{{Name: "POSTGRES_PASSWORD", Value: "pw"}}
			a.Services[0].Backup = &Backup{Engine: "mysql"}
		}, "services[0].backup.engine"},
		{"backup keep too many", func(a *App, d *Deployment) {
			a.Services[0].Env = []EnvVar{{Name: "POSTGRES_PASSWORD", Value: "pw"}}
			a.Services[0].Backup = &Backup{Engine: BackupPostgres, Keep: 101}
		}, "services[0].backup.keep"},
		{"backup schedule not cron", func(a *App, d *Deployment) {
			a.Services[0].Env = []EnvVar{{Name: "POSTGRES_PASSWORD", Value: "pw"}}
			a.Services[0].Backup = &Backup{Engine: BackupPostgres, Schedule: "daily"}
		}, "services[0].backup.schedule"},
		{"backup without ports", func(a *App, d *Deployment) {
			a.Services[1].Env = []EnvVar{{Name: "POSTGRES_PASSWORD", Value: "pw"}}
			a.Services[1].Backup = &Backup{Engine: BackupPostgres}
		}, "services[1].backup"},
		{"backup without a password", func(a *App, d *Deployment) { a.Services[0].Backup = &Backup{Engine: BackupPostgres} }, "services[0].backup"},

		// Ports.
		{"port name empty", func(a *App, d *Deployment) { a.Services[0].Ports[0].Name = "" }, "services[0].ports[0].name"},
		{"port name uppercase", func(a *App, d *Deployment) { a.Services[0].Ports[0].Name = "HTTP" }, "services[0].ports[0].name"},
		{"port name hyphen", func(a *App, d *Deployment) { a.Services[0].Ports[0].Name = "my-port" }, "services[0].ports[0].name"},
		{"port name leading digit", func(a *App, d *Deployment) { a.Services[0].Ports[0].Name = "1http" }, "services[0].ports[0].name"},
		{"port name duplicate", func(a *App, d *Deployment) {
			a.Services[0].Ports = append(a.Services[0].Ports, Port{Name: "http", ContainerPort: 8081})
		}, "services[0].ports[1].name"},
		{"container port zero", func(a *App, d *Deployment) { a.Services[0].Ports[0].ContainerPort = 0 }, "services[0].ports[0].containerPort"},
		{"container port too high", func(a *App, d *Deployment) { a.Services[0].Ports[0].ContainerPort = 65536 }, "services[0].ports[0].containerPort"},

		// Domain hosts.
		{"host empty", func(a *App, d *Deployment) { a.Services[0].Domains[0].Host = "" }, "services[0].domains[0].host"},
		{"host uppercase", func(a *App, d *Deployment) { a.Services[0].Domains[0].Host = "Shop.example.com" }, "services[0].domains[0].host"},
		{"host with space", func(a *App, d *Deployment) { a.Services[0].Domains[0].Host = "a b.com" }, "services[0].domains[0].host"},
		{"host with backtick", func(a *App, d *Deployment) { a.Services[0].Domains[0].Host = "a`b.com" }, "services[0].domains[0].host"},
		{"host wildcard", func(a *App, d *Deployment) { a.Services[0].Domains[0].Host = "*.example.com" }, "services[0].domains[0].host"},
		{"host trailing dot", func(a *App, d *Deployment) { a.Services[0].Domains[0].Host = "example.com." }, "services[0].domains[0].host"},
		{"host label too long", func(a *App, d *Deployment) {
			a.Services[0].Domains[0].Host = strings.Repeat("a", 64) + ".com"
		}, "services[0].domains[0].host"},
		{"host too long", func(a *App, d *Deployment) {
			label := strings.Repeat("a", 63)
			a.Services[0].Domains[0].Host = strings.Join([]string{label, label, label, label}, ".")
		}, "services[0].domains[0].host"},
		{"host duplicate in service", func(a *App, d *Deployment) {
			a.Services[0].Domains = append(a.Services[0].Domains, Domain{Host: "shop.example.com", Port: "http"})
		}, "services[0].domains[1].host"},
		{"host duplicate across services", func(a *App, d *Deployment) {
			a.Services[1].Ports = []Port{{Name: "http", ContainerPort: 8080}}
			a.Services[1].Domains = []Domain{{Host: "shop.example.com", Port: "http"}}
			a.Services[1].Health = &HealthCheck{}
		}, "services[1].domains[0].host"},

		// Domain ports.
		{"domain port empty", func(a *App, d *Deployment) { a.Services[0].Domains[0].Port = "" }, "services[0].domains[0].port"},
		{"domain port undeclared", func(a *App, d *Deployment) { a.Services[0].Domains[0].Port = "nope" }, "services[0].domains[0].port"},
		{"domains on two ports", func(a *App, d *Deployment) {
			a.Services[0].Ports = append(a.Services[0].Ports, Port{Name: "admin", ContainerPort: 9000})
			a.Services[0].Domains = append(a.Services[0].Domains, Domain{Host: "admin.example.com", Port: "admin"})
		}, "services[0].domains[1].port"},
		{"domain on service without ports", func(a *App, d *Deployment) {
			a.Services[1].Domains = []Domain{{Host: "jobs.example.com", Port: "http"}}
		}, "services[1].domains[0].port"},

		// Health check.
		{"health missing with ports", func(a *App, d *Deployment) { a.Services[0].Health = nil }, "services[0].health"},
		{"health on service without ports", func(a *App, d *Deployment) { a.Services[1].Health = &HealthCheck{} }, "services[1].health"},
		{"health path without slash", func(a *App, d *Deployment) { a.Services[0].Health.Path = "healthz" }, "services[0].health.path"},
		{"health port undeclared", func(a *App, d *Deployment) { a.Services[0].Health.Port = "nope" }, "services[0].health.port"},
		{"health port ambiguous", func(a *App, d *Deployment) {
			a.Services[0].Ports = append(a.Services[0].Ports, Port{Name: "metrics", ContainerPort: 9100})
			a.Services[0].Domains = nil
		}, "services[0].health.port"},
		{"health interval negative", func(a *App, d *Deployment) { a.Services[0].Health.Interval = -time.Second }, "services[0].health.interval"},
		{"health timeout negative", func(a *App, d *Deployment) { a.Services[0].Health.Timeout = -time.Second }, "services[0].health.timeout"},

		// Deployment.
		{"deployment id empty", func(a *App, d *Deployment) { d.ID = "" }, "deployment.id"},
		{"trigger empty", func(a *App, d *Deployment) { d.Trigger = "" }, "deployment.trigger"},
		{"trigger unknown", func(a *App, d *Deployment) { d.Trigger = "rollback" }, "deployment.trigger"},
		{"digest missing", func(a *App, d *Deployment) { delete(d.Digests, "web") }, "deployment.digests[web]"},
		{"digests nil", func(a *App, d *Deployment) { d.Digests = nil }, "deployment.digests[web]"},
		{"digest is a tag", func(a *App, d *Deployment) { d.Digests["web"] = "traefik/whoami:latest" }, "deployment.digests[web]"},
		{"digest too short", func(a *App, d *Deployment) { d.Digests["web"] = "traefik/whoami@sha256:abc" }, "deployment.digests[web]"},
		{"digest uppercase hex", func(a *App, d *Deployment) {
			d.Digests["web"] = "traefik/whoami@sha256:" + strings.Repeat("A", 64)
		}, "deployment.digests[web]"},
		{"digest with tag", func(a *App, d *Deployment) { d.Digests["web"] = "ghcr.io/acme/shop:main" + sha }, "deployment.digests[web]"},
		{"digest for unknown service", func(a *App, d *Deployment) { d.Digests["db"] = testDigest }, "deployment.digests[db]"},
		{"extra digests reported sorted", func(a *App, d *Deployment) {
			d.Digests["b"] = testDigest
			d.Digests["a"] = testDigest
		}, "deployment.digests[a]"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Fresh fixtures per case, so one case's mutation can't leak into another.
			a, d := validApp(), validDeployment()
			tc.mutate(&a, &d)

			err := Validate(a, d)

			// ValidateApp reports the same first error for every app-level
			// problem, and none for a deployment problem.
			appErr := ValidateApp(a)
			if strings.HasPrefix(tc.wantField, "deployment.") || tc.wantField == "" {
				if appErr != nil {
					t.Errorf("ValidateApp() = %v, want nil", appErr)
				}
			} else if appErr == nil || appErr.Error() != err.Error() {
				t.Errorf("ValidateApp() = %v, want the same error as Validate: %v", appErr, err)
			}

			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}

			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("Validate() = %v, want a *FieldError for %q", err, tc.wantField)
			}
			if fe.Field != tc.wantField {
				t.Errorf("Field = %q, want %q (error: %v)", fe.Field, tc.wantField, err)
			}
			if fe.Message == "" {
				t.Errorf("Message is empty for %q", fe.Field)
			}
		})
	}
}

func TestSecretErrorsHideValues(t *testing.T) {
	marker := "s3cr3t-marker"
	for _, v := range []string{marker + "\x00", marker + "\xff"} {
		a := validApp()
		a.Services[0].Secrets = []Secret{{Name: "A", Value: new(v)}}
		err := ValidateApp(a)
		if err == nil || strings.Contains(err.Error(), marker) {
			t.Errorf("ValidateApp = %v, want an error without the value", err)
		}
	}
}

func TestWithoutSecretValues(t *testing.T) {
	a := validApp()
	a.Services[0].Secrets = []Secret{{Name: "A", Value: new("one")}, {Name: "B"}}
	got := a.WithoutSecretValues()
	if len(got.Services[0].Secrets) != 2 || got.Services[0].Secrets[0].Name != "A" {
		t.Fatalf("names = %+v, want A and B", got.Services[0].Secrets)
	}
	for _, s := range got.Services[0].Secrets {
		if s.Value != nil {
			t.Errorf("secret %s kept its value", s.Name)
		}
	}
	if v := a.Services[0].Secrets[0].Value; v == nil || *v != "one" {
		t.Error("WithoutSecretValues changed the original app")
	}
}
