package nomadjob

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// Run `go test ./internal/nomadjob -update` to rewrite the golden files, then
// review the diff before committing.
var update = flag.Bool("update", false, "rewrite golden files")

// whoamiApp and whoamiDeployment are the equivalent of deploy/dev/whoami.json.
func whoamiApp() app.App { return apptest.Whoami().App }

func whoamiDeployment() app.Deployment { return apptest.Whoami().Deployment }

// labSettings matches the local Nomad lab: no Let's Encrypt, Traefik's
// default certificate.
func labSettings() Settings {
	return Settings{
		Entrypoint:         "websecure",
		CertResolver:       "",
		DefaultCPUMHz:      100,
		DefaultMemoryMB:    64,
		DefaultMemoryMaxMB: 256,
	}
}

// marshalJob returns the exact body Nomad's register endpoint takes.
func marshalJob(t *testing.T, job *api.Job) []byte {
	t.Helper()
	b, err := json.MarshalIndent(struct{ Job *api.Job }{job}, "", "  ")
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	return append(b, '\n')
}

type goldenCase struct {
	name       string
	app        app.App
	deployment app.Deployment
	settings   Settings
	// private is the registry hosts Caravana holds credentials for; nil for
	// every case that predates private registries.
	private map[string]bool
}

// goldenCases is every apptest shape with the lab settings, plus the whoami
// app with a Let's Encrypt resolver.
func goldenCases() []goldenCase {
	var cases []goldenCase
	for _, c := range apptest.Cases() {
		cases = append(cases, goldenCase{name: c.Name, app: c.App, deployment: c.Deployment, settings: labSettings()})
	}
	letsEncrypt := labSettings()
	letsEncrypt.CertResolver = "letsencrypt"
	w := apptest.Whoami()
	cases = append(cases, goldenCase{name: "letsencrypt", app: w.App, deployment: w.Deployment, settings: letsEncrypt})
	cases = append(cases, volumeBackupCases()...)
	cases = append(cases, sqliteBackupCases()...)
	cases = append(cases, openSearchBackupCases()...)
	cases = append(cases, entrypointCases()...)
	cases = append(cases, stopTimeoutCases()...)
	cases = append(cases, identityCases()...)
	cases = append(cases, dnsCases()...)
	cases = append(cases, limitsCases()...)
	cases = append(cases, filesCases()...)
	cases = append(cases, placementCases()...)
	cases = append(cases, scaleCases()...)
	cases = append(cases, disconnectCases()...)
	cases = append(cases, publishedCases()...)
	cases = append(cases, domainCases()...)
	return append(cases, privateRegistryCases()...)
}

// volumeBackupCases are the apps with a volume backup (feature 016). They live
// here, not in apptest, so the packages that run every apptest shape through
// the console's forms don't have to know the engine yet. The "volumes" case
// of apptest is the service with a volume and no backup.
func volumeBackupCases() []goldenCase {
	kuma := func(volumes ...app.Volume) app.App {
		return app.App{
			ID:     "kuma",
			Source: app.Source{Kind: app.SourceImage},
			Services: []app.Service{{
				Name:    "web",
				Image:   "louislam/uptime-kuma:2",
				Ports:   []app.Port{{Name: "http", ContainerPort: 3001}},
				Domains: []app.Domain{{Host: "kuma.example.com", Port: "http"}},
				Health:  &app.HealthCheck{Path: "/"},
				Volumes: volumes,
				Backup:  &app.Backup{Engine: app.BackupVolume, Schedule: "0 4 * * *", Keep: 5},
			}},
		}
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"web": "louislam/uptime-kuma@sha256:" + strings.Repeat("a", 64),
	}}
	return []goldenCase{
		{
			name:       "volume-backup",
			app:        kuma(app.Volume{Name: "data", Path: "/app/data"}, app.Volume{Name: "uploads", Path: "/app/uploads"}),
			deployment: dep,
			settings:   labSettings(),
		},
		{
			name:       "volume-backup-readonly",
			app:        kuma(app.Volume{Name: "data", Path: "/app/data", ReadOnly: true}),
			deployment: dep,
			settings:   labSettings(),
		},
		{
			// A read-only mount without a backup: the task can only measure it.
			name: "volume-readonly",
			app: func() app.App {
				a := kuma(app.Volume{Name: "data", Path: "/app/data", ReadOnly: true})
				a.Services[0].Backup = nil
				return a
			}(),
			deployment: dep,
			settings:   labSettings(),
		},
	}
}

// entrypointCases is a service that overrides the image's entrypoint (feature
// 026), with and without a command and arguments after it. They live here, not
// in apptest, like the volume backup cases.
func entrypointCases() []goldenCase {
	cache := app.App{
		ID:     "shop",
		Source: app.Source{Kind: app.SourceCompose},
		Services: []app.Service{
			{
				Name:       "cache",
				Image:      "docker.io/library/redis:8.6",
				Entrypoint: []string{"/bin/sh"},
				Command:    "-c",
				Args:       []string{"redis-server --requirepass $REDIS_PASSWORD"},
				Ports:      []app.Port{{Name: "p6379", ContainerPort: 6379}},
				Health:     &app.HealthCheck{},
			},
			{
				Name:       "tool",
				Image:      "ghcr.io/acme/tool:main",
				Entrypoint: []string{"/usr/bin/tool", "--serve"},
			},
		},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"cache": "redis@sha256:" + strings.Repeat("a", 64),
		"tool":  "ghcr.io/acme/tool@sha256:" + strings.Repeat("b", 64),
	}}
	return []goldenCase{{name: "entrypoint", app: cache, deployment: dep, settings: labSettings()}}
}

// identityCases is a service with two workload identities, one with a TTL and
// one on the default (spec 038), beside a service with none.
func identityCases() []goldenCase {
	shop := app.App{
		ID:     "shop",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{
				Name:  "api",
				Image: "ghcr.io/acme/api:main",
				Identities: []app.Identity{
					{Name: "bella", Audience: []string{"bella-spiffe/env-1"}},
					{Name: "billing", Audience: []string{"billing", "audit"}, TTLSeconds: 120},
				},
			},
			{Name: "worker", Image: "ghcr.io/acme/worker:main"},
		},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"api":    "ghcr.io/acme/api@sha256:" + strings.Repeat("d", 64),
		"worker": "ghcr.io/acme/worker@sha256:" + strings.Repeat("e", 64),
	}}
	return []goldenCase{{name: "identity", app: shop, deployment: dep, settings: labSettings()}}
}

// publishedCases is a service with a TCP+UDP port published beside a dynamic
// TCP health port, and a second service with a domain, which keeps its canaries.
func publishedCases() []goldenCase {
	a := publishedApp()
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"turn": "ghcr.io/acme/turn@sha256:" + strings.Repeat("a", 64),
		"web":  "ghcr.io/acme/web@sha256:" + strings.Repeat("b", 64),
	}}
	return []goldenCase{{name: "published", app: a, deployment: dep, settings: labSettings()}}
}

func publishedApp() app.App {
	return app.App{
		ID:     "calls",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{
				Name:  "turn",
				Image: "ghcr.io/acme/turn:1",
				Ports: []app.Port{
					{Name: "http", ContainerPort: 8080},
					{Name: "turn", ContainerPort: 3478, Protocol: app.ProtocolTCPUDP, Published: 3478},
					{Name: "relay", ContainerPort: 7882, Protocol: app.ProtocolUDP, Published: 7882},
				},
				Health: &app.HealthCheck{Port: "http", Path: "/healthz"},
			},
			{
				Name:    "web",
				Image:   "ghcr.io/acme/web:1",
				Ports:   []app.Port{{Name: "http", ContainerPort: 3000}},
				Domains: []app.Domain{{Host: "calls.example.com", Port: "http"}},
				Health:  &app.HealthCheck{Path: "/"},
			},
		},
	}
}

// dnsCases is a service with its own resolvers, search domains and options,
// beside a service with none.
func dnsCases() []goldenCase {
	shop := app.App{
		ID:     "shop",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{
				Name:  "api",
				Image: "ghcr.io/acme/api:main",
				DNS: &app.DNS{
					Servers:  []string{"1.1.1.1", "2606:4700:4700::1111"},
					Searches: []string{"corp.example.com"},
					Options:  []string{"ndots:2", "edns0"},
				},
			},
			{Name: "worker", Image: "ghcr.io/acme/worker:main", DNS: &app.DNS{Servers: []string{"10.0.0.2"}}},
			{Name: "plain", Image: "ghcr.io/acme/plain:main"},
		},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"api":    "ghcr.io/acme/api@sha256:" + strings.Repeat("d", 64),
		"worker": "ghcr.io/acme/worker@sha256:" + strings.Repeat("e", 64),
		"plain":  "ghcr.io/acme/plain@sha256:" + strings.Repeat("f", 64),
	}}
	return []goldenCase{{name: "dns", app: shop, deployment: dep, settings: labSettings()}}
}

// limitsCases is OpenSearch's shape: a service with ulimits and a namespaced
// sysctl, beside a service with neither, which shows nothing else changes.
func limitsCases() []goldenCase {
	shop := app.App{
		ID:     "shop",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{
				Name:  "indexer",
				Image: "opensearchproject/opensearch:2.19.1",
				Ulimits: map[string]app.Ulimit{
					"nofile":  {Soft: 65536, Hard: 65536},
					"memlock": {Soft: -1, Hard: -1},
				},
				Sysctls: map[string]string{
					"net.ipv4.tcp_keepalive_time": "600",
					"net.core.somaxconn":          "1024",
				},
			},
			{Name: "plain", Image: "ghcr.io/acme/plain:main"},
		},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"indexer": "opensearchproject/opensearch@sha256:" + strings.Repeat("d", 64),
		"plain":   "ghcr.io/acme/plain@sha256:" + strings.Repeat("f", 64),
	}}
	return []goldenCase{{name: "ulimits", app: shop, deployment: dep, settings: labSettings()}}
}

// filesCases is a service with every kind of file: content with the characters that mean something to
// Nomad's templates and to HCL, a content that holds the first delimiters, one with a carriage return, one
// owned by another user, and two secret files, one of them named by two paths; its other secret stays in the
// environment. A plain service beside it shows nothing else changes.
func filesCases() []goldenCase {
	shop := app.App{
		ID:     "shop",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{
				Name:    "indexer",
				Image:   "opensearchproject/opensearch:2.19.1",
				Secrets: []app.Secret{{Name: "TLS_KEY"}, {Name: "ADMIN_PASSWORD"}, {Name: "INTERNAL_USERS"}},
				Files: []app.File{
					{Path: "/usr/share/opensearch/config/opensearch.yml", Content: "cluster.name: shop\nnote: {{ not a template }} ${HOME} %{x} [[x]]\n"},
					{Path: "/etc/app/delims.conf", Content: "a [[cv0 b cv0]] c", Mode: "0600"},
					{Path: "/etc/app/crlf.txt", Content: "one\r\ntwo\r\n", Mode: "640", UID: new(1000), GID: new(0)},
					{Path: "/certs/node.key", Secret: "TLS_KEY", Mode: "0400"},
					{Path: "/certs/node-copy.key", Secret: "TLS_KEY", Mode: "0400"},
					{Path: "/usr/share/opensearch/config/internal_users.yml", Secret: "INTERNAL_USERS", UID: new(1000), GID: new(1000)},
				},
			},
			{Name: "plain", Image: "ghcr.io/acme/plain:main", Secrets: []app.Secret{{Name: "TOKEN"}}},
		},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"indexer": "opensearchproject/opensearch@sha256:" + strings.Repeat("d", 64),
		"plain":   "ghcr.io/acme/plain@sha256:" + strings.Repeat("f", 64),
	}}
	return []goldenCase{{name: "files", app: shop, deployment: dep, settings: labSettings()}}
}

// placementCases is a service of two copies with every kind of placement
// rule, beside a service with none, which shows nothing else changes.
func placementCases() []goldenCase {
	shop := app.App{
		ID:     "shop",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{
				Name:     "web",
				Image:    "ghcr.io/acme/web:main",
				Replicas: new(2),
				Ports:    []app.Port{{Name: "http", ContainerPort: 3000}},
				Domains:  []app.Domain{{Host: "shop.example.com", Port: "http"}},
				Health:   &app.HealthCheck{Path: "/"},
				Placement: &app.Placement{
					Require: map[string]string{"ssd": "true", "region": "eu"},
					Prefer:  []app.Preference{{Label: "ssd", Value: "true", Weight: 80}, {Label: "disk", Value: "nvme"}},
					Avoid:   []string{"three", "two"},
				},
			},
			{Name: "worker", Image: "ghcr.io/acme/worker:main"},
		},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"web":    "ghcr.io/acme/web@sha256:" + strings.Repeat("d", 64),
		"worker": "ghcr.io/acme/worker@sha256:" + strings.Repeat("e", 64),
	}}
	return []goldenCase{{name: "placement", app: shop, deployment: dep, settings: labSettings()}}
}

// scaleCases is a scaled service whose starting count is 2 (count and canary
// 2, with the spread), a scaled service at one copy (it gets the spread too,
// so the copies it adds later spread), and a service that doesn't scale (no
// spread at one copy), feature 043.
func scaleCases() []goldenCase {
	shop := app.App{
		ID:     "shop",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{
				Name:     "web",
				Image:    "ghcr.io/acme/web:main",
				Replicas: new(2),
				Ports:    []app.Port{{Name: "http", ContainerPort: 3000}},
				Domains:  []app.Domain{{Host: "shop.example.com", Port: "http"}},
				Health:   &app.HealthCheck{Path: "/"},
				Scale:    &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{CPU: 70}},
			},
			{
				Name:  "api",
				Image: "ghcr.io/acme/api:main",
				Scale: &app.Scale{Min: 1, Max: 2, Target: app.ScaleTarget{CPU: 80}, CooldownSeconds: 120},
			},
			{Name: "worker", Image: "ghcr.io/acme/worker:main"},
		},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"web":    "ghcr.io/acme/web@sha256:" + strings.Repeat("d", 64),
		"api":    "ghcr.io/acme/api@sha256:" + strings.Repeat("c", 64),
		"worker": "ghcr.io/acme/worker@sha256:" + strings.Repeat("e", 64),
	}}
	return []goldenCase{{name: "scale", app: shop, deployment: dep, settings: labSettings()}}
}

// stopTimeoutCases is a service that sets its stop timeout, on a service
// without a volume, so only this setting puts a kill timeout in the task.
func stopTimeoutCases() []goldenCase {
	worker := app.App{
		ID:     "jobs",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{
				Name:        "worker",
				Image:       "ghcr.io/acme/worker:main",
				StopTimeout: 20,
			},
		},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"worker": "ghcr.io/acme/worker@sha256:" + strings.Repeat("c", 64),
	}}
	return []goldenCase{{name: "stop-timeout", app: worker, deployment: dep, settings: labSettings()}}
}

// domainCases are apps whose domains have options (feature 023), each with
// the domains the case is named for. They live here, not in apptest, like the
// other cases of one feature. "domain-all" has one domain of each kind and a
// plain one, so the base router keeps only the plain host.
func domainCases() []goldenCase {
	shop := func(domains ...app.Domain) app.App {
		return app.App{
			ID:     "shop",
			Source: app.Source{Kind: app.SourceImage},
			Services: []app.Service{{
				Name:    "web",
				Image:   "traefik/whoami:v1.10",
				Ports:   []app.Port{{Name: "http", ContainerPort: 80}},
				Domains: domains,
				Health:  &app.HealthCheck{Path: "/health"},
			}},
		}
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"web": "traefik/whoami@sha256:" + strings.Repeat("a", 64),
	}}
	return []goldenCase{
		{name: "domain-path", app: shop(app.Domain{Host: "shop.example.com", Port: "http", Path: "/api"}), deployment: dep, settings: labSettings()},
		{name: "domain-www", app: shop(app.Domain{Host: "example.com", Port: "http", WWWRedirect: true}), deployment: dep, settings: labSettings()},
		{name: "domain-team", app: shop(app.Domain{Host: "staging.example.com", Port: "http", TeamOnly: true}), deployment: dep, settings: labSettings()},
		{
			name: "domain-all",
			app: shop(
				app.Domain{Host: "plain.example.com", Port: "http"},
				app.Domain{Host: "api.example.com", Port: "http", Path: "/api"},
				app.Domain{Host: "example.com", Port: "http", WWWRedirect: true},
				app.Domain{Host: "team.example.com", Port: "http", TeamOnly: true},
			),
			deployment: dep,
			settings:   letsEncryptSettings(),
		},
	}
}

func letsEncryptSettings() Settings {
	s := labSettings()
	s.CertResolver = "letsencrypt"
	return s
}

func TestTranslate(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Translate(tc.app, tc.deployment, tc.settings, tc.private)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			if len(res.Warnings) != 0 {
				t.Errorf("warnings = %v, want none", res.Warnings)
			}

			got := marshalJob(t, res.Job)
			path := filepath.Join("testdata", tc.name+".golden.json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("output differs from %s; if the change is intended, run with -update and review the diff.\ngot:\n%s", path, got)
			}
		})
	}
}

// TestTranslateMatchesLabJob checks the translator against the job the lab
// is known to run. Reading the lab's own file means the two can't drift.
func TestTranslateMatchesLabJob(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "deploy", "dev", "whoami.json"))
	if err != nil {
		t.Fatalf("open lab job: %v", err)
	}
	defer f.Close()

	var want struct{ Job *api.Job }
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&want); err != nil {
		t.Fatalf("decode lab job: %v", err)
	}

	res, err := Translate(whoamiApp(), whoamiDeployment(), labSettings(), nil)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	// Compared as JSON, which is what Nomad gets: a decoded task config holds
	// []any where Translate writes []string (the names sidecar's args).
	if w, g := marshalJob(t, want.Job), marshalJob(t, res.Job); !bytes.Equal(w, g) {
		t.Errorf("translated job differs from deploy/dev/whoami.json\nwant:\n%s\ngot:\n%s", w, g)
	}
}

func TestTranslateDeterministic(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			var first []byte
			for i := range 100 {
				res, err := Translate(tc.app, tc.deployment, tc.settings, tc.private)
				if err != nil {
					t.Fatalf("Translate: %v", err)
				}
				got := marshalJob(t, res.Job)
				if i == 0 {
					first = got
					continue
				}
				if !bytes.Equal(got, first) {
					t.Fatalf("run %d differs from run 0", i)
				}
			}
		})
	}
}

func TestTranslateNewDeploymentOnlyChangesMeta(t *testing.T) {
	d1 := whoamiDeployment()
	d1.ID = "d1"
	d2 := whoamiDeployment()
	d2.ID = "d2"

	r1, err := Translate(whoamiApp(), d1, labSettings(), nil)
	if err != nil {
		t.Fatalf("Translate d1: %v", err)
	}
	r2, err := Translate(whoamiApp(), d2, labSettings(), nil)
	if err != nil {
		t.Fatalf("Translate d2: %v", err)
	}

	if r1.Job.Meta["caravana.deployment"] == r2.Job.Meta["caravana.deployment"] {
		t.Fatalf("both jobs have deployment meta %q, want different", r1.Job.Meta["caravana.deployment"])
	}
	r2.Job.Meta["caravana.deployment"] = "d1"
	if !reflect.DeepEqual(r1.Job, r2.Job) {
		t.Errorf("jobs differ in more than the deployment meta\nd1:\n%s\nd2:\n%s",
			marshalJob(t, r1.Job), marshalJob(t, r2.Job))
	}
}

// TestTranslateErrors covers the rules that live in this package (settings
// and Nomad's limits) and proves app.Validate runs. The model's own rules are
// tested in package app.
func TestTranslateErrors(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(a *app.App, d *app.Deployment, s *Settings)
		wantField string
	}{
		{"entrypoint empty", func(a *app.App, d *app.Deployment, s *Settings) { s.Entrypoint = "" }, "settings.entrypoint"},
		{"entrypoint with space", func(a *app.App, d *app.Deployment, s *Settings) { s.Entrypoint = "web secure" }, "settings.entrypoint"},
		{"entrypoint list", func(a *app.App, d *app.Deployment, s *Settings) { s.Entrypoint = "a,b" }, "settings.entrypoint"},
		{"cert resolver with space", func(a *app.App, d *app.Deployment, s *Settings) { s.CertResolver = "le tls" }, "settings.certResolver"},
		{"default cpu zero", func(a *app.App, d *app.Deployment, s *Settings) { s.DefaultCPUMHz = 0 }, "settings.defaultCPUMHz"},
		{"default memory below minimum", func(a *app.App, d *app.Deployment, s *Settings) { s.DefaultMemoryMB = 9 }, "settings.defaultMemoryMB"},
		{"default ceiling below the default reservation", func(a *app.App, d *app.Deployment, s *Settings) { s.DefaultMemoryMaxMB = 32 }, "settings.defaultMemoryMaxMB"},
		{"names image with a tag", func(a *app.App, d *app.Deployment, s *Settings) { s.NamesImage = "busybox:1.37" }, "settings.namesImage"},
		{"backup image with a tag", func(a *app.App, d *app.Deployment, s *Settings) { s.BackupImage = "rclone/rclone:latest" }, "settings.backupImage"},
		{"sqlite image with a tag", func(a *app.App, d *app.Deployment, s *Settings) { s.SQLiteImage = "alpine/sqlite:latest" }, "settings.sqliteImage"},
		{"scanner image with a tag", func(a *app.App, d *app.Deployment, s *Settings) { s.ScannerImage = "anchore/grype:v0.117.0" }, "settings.scannerImage"},

		{"memory below minimum", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Resources.MemoryMB = 5
		}, "services[0].resources.memoryMB"},
		{"memory ceiling below minimum", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Resources.MemoryMaxMB = 5
		}, "services[0].resources.memoryMaxMB"},
		{"interval below minimum", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Health.Interval = 500 * time.Millisecond
		}, "services[0].health.interval"},
		{"timeout below minimum", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Health.Timeout = 500 * time.Millisecond
		}, "services[0].health.timeout"},
		{"timeout equal to default interval", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Health.Timeout = 10 * time.Second
		}, "services[0].health.timeout"},
		{"timeout longer than interval", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Health.Interval = 2 * time.Second
			a.Services[0].Health.Timeout = 3 * time.Second
		}, "services[0].health.timeout"},

		{"app rules run", func(a *app.App, d *app.Deployment, s *Settings) {
			a.Services[0].Image = ""
		}, "services[0].image"},
		{"model rules run", func(a *app.App, d *app.Deployment, s *Settings) {
			d.Digests["web"] = "traefik/whoami:latest"
		}, "deployment.digests[web]"},
		{"settings checked first", func(a *app.App, d *app.Deployment, s *Settings) {
			s.Entrypoint = ""
			d.Digests["web"] = "traefik/whoami:latest"
		}, "settings.entrypoint"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, d, s := whoamiApp(), whoamiDeployment(), labSettings()
			tc.mutate(&a, &d, &s)

			res, err := Translate(a, d, s, nil)
			var fe *app.FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("Translate() error = %v, want a *app.FieldError for %q", err, tc.wantField)
			}
			if fe.Field != tc.wantField {
				t.Errorf("Field = %q, want %q (error: %v)", fe.Field, tc.wantField, err)
			}
			if fe.Message == "" {
				t.Errorf("Message is empty for %q", fe.Field)
			}
			if res.Job != nil {
				t.Errorf("got a job together with an error")
			}

			// Check reports the same error for everything that doesn't
			// need the deployment, and nothing for deployment problems.
			checkErr := Check(a, s)
			if strings.HasPrefix(tc.wantField, "deployment.") {
				if checkErr != nil {
					t.Errorf("Check() = %v, want nil", checkErr)
				}
			} else if checkErr == nil || checkErr.Error() != err.Error() {
				t.Errorf("Check() = %v, want the same error as Translate: %v", checkErr, err)
			}
		})
	}
}

// TestSecretsStayOutOfTheJob checks that a job names each service's variable
// and holds none of the values, and that a service without secrets gets no
// template.
func TestSecretsStayOutOfTheJob(t *testing.T) {
	marker := "s3cr3t-marker"
	a := apptest.Whoami().App
	a.Services[0].Secrets = []app.Secret{{Name: "API_KEY", Value: new(marker)}}
	d := apptest.Whoami().Deployment

	res, err := Translate(a, d, labSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(res.Job)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(marker)) {
		t.Fatal("the job holds a secret value")
	}
	tmpls := res.Job.TaskGroups[0].Tasks[0].Templates
	if len(tmpls) != 1 || !strings.Contains(*tmpls[0].EmbeddedTmpl, `"nomad/jobs/whoami/web"`) || *tmpls[0].ChangeMode != "noop" {
		t.Errorf("templates = %+v, want one noop template reading nomad/jobs/whoami/web", tmpls)
	}

	plain, err := Translate(apptest.Whoami().App, d, labSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(plain.Job.TaskGroups[0].Tasks[0].Templates); n != 0 {
		t.Errorf("a service without secrets got %d templates", n)
	}
}

// TestServicesByName checks the names sidecar and the internal registrations
// (research R3, task 2xg2): every group resolves names, every service with
// ports is registered with its place and the names it claims.
func TestServicesByName(t *testing.T) {
	single := apptest.Whoami()
	res, err := Translate(single.App, single.Deployment, labSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tg := res.Job.TaskGroups[0]
	if len(tg.Tasks) != 2 || tg.Tasks[1].Name != "names" || len(tg.Services) != 2 {
		t.Fatalf("a single-service app got %d tasks and %d registrations, want 2 (the service, names) and 2", len(tg.Tasks), len(tg.Services))
	}
	if got, want := tg.Services[1].Tags, []string{"caravana-place=default/production", "caravana-name=whoami-web", "caravana-name=whoami"}; !slices.Equal(got, want) {
		t.Errorf("whoami's internal tags = %v, want %v", got, want)
	}

	var vols apptest.Case
	for _, c := range apptest.Cases() {
		if c.Name == "volumes" {
			vols = c
		}
	}
	res, err = Translate(vols.App, vols.Deployment, labSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range res.Job.TaskGroups {
		// The db service has a volume, which adds the caravana-volumes task
		// before names (TestVolumeTask checks it).
		wantTasks := 2
		if *tg.Name == "db" {
			wantTasks = 3
		}
		last := tg.Tasks[len(tg.Tasks)-1]
		if len(tg.Tasks) != wantTasks || tg.Tasks[0].Name != *tg.Name || last.Name != "names" {
			t.Fatalf("group %s tasks = %d, want its own task first and names last", *tg.Name, len(tg.Tasks))
		}
		if last.Lifecycle == nil || last.Lifecycle.Hook != "prestart" || !last.Lifecycle.Sidecar {
			t.Errorf("group %s: names isn't a prestart sidecar", *tg.Name)
		}
		if last.Config["image"] != DefaultNamesImage {
			t.Errorf("group %s: names image = %v", *tg.Name, last.Config["image"])
		}
		tmpl := *last.Templates[0].EmbeddedTmpl
		for _, want := range []string{`nomadService "shop-web-internal"`, `nomadService "shop-db-internal"`, `contains "caravana.canary=true"`, " db\n", " web\n",
			`contains "caravana-place=default/production"`, `(ne $n "web") (ne $n "db")`} {
			if !strings.Contains(tmpl, want) {
				t.Errorf("group %s: the names template lacks %q", *tg.Name, want)
			}
		}
		internal := tg.Services[1]
		if internal.AddressMode != "alloc" || internal.Provider != "nomad" || len(internal.Checks) != 0 {
			t.Errorf("group %s: internal registration = %+v", *tg.Name, internal)
		}
		if !slices.Contains(internal.Tags, "caravana-name=shop-"+*tg.Name) || slices.Contains(internal.Tags, "caravana-name=shop") {
			t.Errorf("group %s: internal tags = %v, want shop-%s and not shop (two services have ports)", *tg.Name, internal.Tags, *tg.Name)
		}
		if want := append(slices.Clone(internal.Tags), "caravana.canary=true"); !slices.Equal(internal.CanaryTags, want) {
			t.Errorf("group %s: canary tags = %v, want %v", *tg.Name, internal.CanaryTags, want)
		}
	}

	t.Run("a place other than Default's production", func(t *testing.T) {
		a := single.App
		a.ID = "shop--staging--whoami"
		res, err := Translate(a, single.Deployment, labSettings(), nil)
		if err != nil {
			t.Fatal(err)
		}
		tg := res.Job.TaskGroups[0]
		if got, want := tg.Services[1].Tags, []string{"caravana-place=shop/staging", "caravana-name=whoami-web", "caravana-name=whoami"}; !slices.Equal(got, want) {
			t.Errorf("tags = %v, want %v", got, want)
		}
		if tmpl := *tg.Tasks[1].Templates[0].EmbeddedTmpl; !strings.Contains(tmpl, `contains "caravana-place=shop/staging"`) || strings.Contains(tmpl, "production") {
			t.Errorf("the template reads another place: %s", tmpl)
		}
	})

	t.Run("a service called names", func(t *testing.T) {
		a := single.App
		a.Services = append([]app.Service(nil), a.Services...)
		a.Services[0].Name = "names"
		d := single.Deployment
		d.Digests = map[string]string{"names": d.Digests["web"]}
		res, err := Translate(a, d, labSettings(), nil)
		if err != nil {
			t.Fatal(err)
		}
		tasks := res.Job.TaskGroups[0].Tasks
		if tasks[0].Name != "names" || tasks[1].Name != "caravana-names" {
			t.Errorf("tasks = %s, %s; want names, caravana-names", tasks[0].Name, tasks[1].Name)
		}
	})

	t.Run("a worker without ports", func(t *testing.T) {
		var worker apptest.Case
		for _, c := range apptest.Cases() {
			if c.Name == "worker" {
				worker = c
			}
		}
		res, err := Translate(worker.App, worker.Deployment, labSettings(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tg := range res.Job.TaskGroups {
			if tg.Tasks[len(tg.Tasks)-1].Name != "names" {
				t.Errorf("group %s has no names sidecar: a worker reaches its database by name too", *tg.Name)
			}
		}
	})
}

// TestInternalName: a registration name that fits is "<app>-<service>-internal"
// as before; a longer one is cut and ends with a hash, so it stays unique and
// Nomad takes it.
func TestInternalName(t *testing.T) {
	if got := internalName("shop", "web"); got != "shop-web-internal" {
		t.Errorf("internalName = %q", got)
	}
	long := strings.Repeat("a", 50)
	one, two := internalName(long, "web-one"), internalName(long, "web-two")
	if len(one) != maxServiceNameLen || !strings.HasSuffix(one, "-internal") || one == two {
		t.Errorf("long names = %q, %q", one, two)
	}
}

// TestVolumeGroupsReplaceTheirCopy checks a group with a volume: no canary,
// the volume request, and the mount (research R5).
func TestVolumeGroupsReplaceTheirCopy(t *testing.T) {
	var vols apptest.Case
	for _, c := range apptest.Cases() {
		if c.Name == "volumes" {
			vols = c
		}
	}
	res, err := Translate(vols.App, vols.Deployment, labSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	web, db := res.Job.TaskGroups[0], res.Job.TaskGroups[1]
	if *web.Update.Canary != 1 {
		t.Errorf("web canary = %d, want 1: only groups with a volume give up canaries", *web.Update.Canary)
	}
	if *db.Update.Canary != 0 {
		t.Errorf("db canary = %d, want 0", *db.Update.Canary)
	}
	v := db.Volumes["db-data"]
	if v == nil || v.Source != "shop_db-data" || v.Type != "host" || v.AccessMode != "single-node-single-writer" {
		t.Errorf("db volume = %+v, want the host volume shop_db-data, single writer", v)
	}
	m := db.Tasks[0].VolumeMounts
	if len(m) != 1 || *m[0].Volume != "db-data" || *m[0].Destination != "/var/lib/postgresql/data" {
		t.Errorf("db mounts = %+v", m)
	}
}

// Feature 013: what a task reserves and what it may burst to.
func TestTaskMemory(t *testing.T) {
	s := Settings{DefaultCPUMHz: 100, DefaultMemoryMB: 256, DefaultMemoryMaxMB: 1024}
	tests := []struct {
		name             string
		set              app.Resources
		reserve, ceiling int
	}{
		{"nothing set", app.Resources{}, 256, 1024},
		{"a reservation", app.Resources{MemoryMB: 512}, 512, 1024},
		{"a reservation above the default ceiling", app.Resources{MemoryMB: 2048}, 2048, 2048},
		{"a ceiling below the default reservation", app.Resources{MemoryMaxMB: 128}, 128, 128},
		{"a ceiling above it", app.Resources{MemoryMaxMB: 768}, 256, 768},
		{"both", app.Resources{MemoryMB: 300, MemoryMaxMB: 600}, 300, 600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := taskResources(tt.set, s)
			if *r.MemoryMB != tt.reserve || *r.MemoryMaxMB != tt.ceiling {
				t.Errorf("reserve %d, ceiling %d; want %d, %d", *r.MemoryMB, *r.MemoryMaxMB, tt.reserve, tt.ceiling)
			}
		})
	}
}

// A group that publishes a port deploys stop-then-start, as a group with a
// volume does, and a group beside it keeps its canaries (spec 041 D3).
func TestPublishedPortsStopThenStart(t *testing.T) {
	a := publishedApp()
	res, err := Translate(a, app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"turn": "ghcr.io/acme/turn@sha256:" + strings.Repeat("a", 64),
		"web":  "ghcr.io/acme/web@sha256:" + strings.Repeat("b", 64),
	}}, labSettings(), nil)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	turn, web := res.Job.TaskGroups[0], res.Job.TaskGroups[1]
	if u := turn.Update; u == nil || u.Canary == nil || *u.Canary != 0 || u.AutoPromote == nil || *u.AutoPromote {
		t.Errorf("turn update = %+v, want canary 0 and no auto-promote", u)
	}
	if u := web.Update; u == nil || u.Canary == nil || *u.Canary != 1 {
		t.Errorf("web update = %+v, want one canary", u)
	}
	n := turn.Networks[0]
	if len(n.DynamicPorts) != 1 || n.DynamicPorts[0].Label != "http" {
		t.Errorf("dynamic ports = %+v, want only http", n.DynamicPorts)
	}
	want := []api.Port{
		{Label: "turn", Value: 3478, To: 3478, HostNetwork: "public"},
		{Label: "relay", Value: 7882, To: 7882, HostNetwork: "public"},
	}
	if !reflect.DeepEqual(n.ReservedPorts, want) {
		t.Errorf("reserved ports = %+v, want %+v", n.ReservedPorts, want)
	}
	if len(web.Networks[0].ReservedPorts) != 0 {
		t.Errorf("web reserves ports: %+v", web.Networks[0].ReservedPorts)
	}
}
