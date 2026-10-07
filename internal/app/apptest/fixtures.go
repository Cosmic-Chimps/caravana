// Package apptest holds app fixtures shared by package tests, so the
// translator's golden tests and the store's round-trip tests use the same
// shapes. Product code never imports it.
package apptest

import (
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Case is one app shape with a matching, valid deployment.
type Case struct {
	Name       string
	App        app.App
	Deployment app.Deployment
}

// WhoamiDigest is the image digest in deploy/dev/whoami.json.
const WhoamiDigest = "traefik/whoami@sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44"

// Whoami is the app and deployment equivalent of deploy/dev/whoami.json.
func Whoami() Case {
	return Case{
		Name: "whoami",
		App: app.App{
			ID:     "whoami",
			Name:   "whoami",
			Source: app.Source{Kind: app.SourceImage},
			Services: []app.Service{{
				Name:     "web",
				Image:    "traefik/whoami:latest",
				Ports:    []app.Port{{Name: "http", ContainerPort: 80}},
				Domains:  []app.Domain{{Host: "whoami.localhost", Port: "http"}},
				Replicas: new(2),
				Health:   &app.HealthCheck{Path: "/"},
			}},
		},
		Deployment: app.Deployment{
			ID:      "lab-manual",
			Trigger: app.TriggerManual,
			Digests: map[string]string{"web": WhoamiDigest},
		},
	}
}

// Cases returns every app shape, each call with fresh values that tests may
// change.
func Cases() []Case {
	customResources := Whoami()
	customResources.Name = "custom-resources"
	customResources.App.Services[0].Resources = app.Resources{CPUMHz: 500, MemoryMB: 256}
	customResources.App.Services[0].Health = &app.HealthCheck{Path: "/", Interval: 30 * time.Second, Timeout: 5 * time.Second}

	// withSecrets has a web service with secrets (one kept, one set) and a
	// worker with secrets of its own, beside its plain env.
	secretWeb, secretWorker := blogWeb(), jobsWorker()
	secretWeb.Secrets = []app.Secret{{Name: "DATABASE_URL"}, {Name: "API_KEY", Value: new("k1")}}
	secretWorker.Secrets = []app.Secret{{Name: "DATABASE_URL", Value: new("postgres://worker")}}

	return []Case{
		Whoami(),
		{
			Name:       "minimal-web",
			App:        imageApp("blog", blogWeb()),
			Deployment: deployment(map[string]string{"web": digest("ghcr.io/acme/blog", 'b')}),
		},
		{
			Name: "multi-domain",
			App: imageApp("shop", app.Service{
				Name:  "web",
				Image: "ghcr.io/acme/shop:main",
				Ports: []app.Port{{Name: "http", ContainerPort: 3000}, {Name: "metrics", ContainerPort: 9100}},
				Domains: []app.Domain{
					{Host: "shop.example.com", Port: "http"},
					{Host: "www.shop.example.com", Port: "http"},
				},
				Health: &app.HealthCheck{Path: "/healthz", Port: "metrics"},
			}),
			Deployment: deployment(map[string]string{"web": digest("ghcr.io/acme/shop", 'c')}),
		},
		{
			Name:       "worker",
			App:        imageApp("jobs", jobsWorker()),
			Deployment: deployment(map[string]string{"worker": digest("ghcr.io/acme/jobs", 'd')}),
		},
		{
			Name: "internal-service",
			App: imageApp("api", app.Service{
				Name:   "grpc",
				Image:  "ghcr.io/acme/api:main",
				Ports:  []app.Port{{Name: "grpc", ContainerPort: 50051}},
				Health: &app.HealthCheck{},
			}),
			Deployment: deployment(map[string]string{"grpc": digest("ghcr.io/acme/api", 'e')}),
		},
		customResources,
		{
			Name: "multi-service",
			App:  imageApp("blog", blogWeb(), jobsWorker()),
			Deployment: deployment(map[string]string{
				"web":    digest("ghcr.io/acme/blog", 'b'),
				"worker": digest("ghcr.io/acme/jobs", 'd'),
			}),
		},
		{
			// A web service and a database on a volume: the database's
			// group deploys stop-then-start, and the two reach each other
			// by name.
			Name: "volumes",
			App: imageApp("shop", blogWeb(), app.Service{
				Name:    "db",
				Image:   "postgres:17",
				Ports:   []app.Port{{Name: "pg", ContainerPort: 5432}},
				Health:  &app.HealthCheck{},
				Volumes: []app.Volume{{Name: "db-data", Path: "/var/lib/postgresql/data"}},
			}),
			Deployment: deployment(map[string]string{
				"web": digest("ghcr.io/acme/blog", 'b'),
				"db":  digest("postgres", 'f'),
			}),
		},
		{
			Name: "secrets",
			App:  imageApp("blog", secretWeb, secretWorker),
			Deployment: deployment(map[string]string{
				"web":    digest("ghcr.io/acme/blog", 'b'),
				"worker": digest("ghcr.io/acme/jobs", 'd'),
			}),
		},
	}
}

// digest returns a valid pinned reference for repo, with a fixed fake hash.
func digest(repo string, hex byte) string {
	return repo + "@sha256:" + strings.Repeat(string(hex), 64)
}

func deployment(digests map[string]string) app.Deployment {
	return app.Deployment{ID: "d1", Trigger: app.TriggerWebhook, Digests: digests}
}

func imageApp(id string, services ...app.Service) app.App {
	return app.App{ID: id, Source: app.Source{Kind: app.SourceImage}, Services: services}
}

// blogWeb has only what's required: the replica count, resources and check
// timing all come from defaults, and the check is TCP.
func blogWeb() app.Service {
	return app.Service{
		Name:    "web",
		Image:   "ghcr.io/acme/blog:main",
		Ports:   []app.Port{{Name: "http", ContainerPort: 8080}},
		Domains: []app.Domain{{Host: "blog.example.com", Port: "http"}},
		Health:  &app.HealthCheck{},
	}
}

// jobsWorker has no ports, so it gets no service registration and no check.
func jobsWorker() app.Service {
	return app.Service{
		Name:    "worker",
		Image:   "ghcr.io/acme/jobs:main",
		Command: "/app/worker",
		Args:    []string{"--queue", "default"},
		Env: []app.EnvVar{
			{Name: "QUEUE_URL", Value: "redis://queue:6379"},
			{Name: "LOG_LEVEL", Value: "info"},
		},
	}
}
