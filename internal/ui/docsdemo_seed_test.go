package ui

// The made-up installation of the documentation screenshots (TestDocsDemo). The names are the ones
// the guide's pages are written against: project shop with environments staging and production;
// apps web, api and db in production and web in staging; a second person, a registry, a
// notification channel, tokens, schedules and backups. Everything is fictional: example.com names,
// made-up digests, no secret value.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// demoDigest is a made-up image digest, different for each seed.
func demoDigest(seed string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(seed)))
}

// demoDeploy is one deployment of a seeded app.
type demoDeploy struct {
	ID       string
	Ago      time.Duration // when it started
	Took     time.Duration // how long it ran before it ended
	Trigger  app.Trigger
	Status   store.Status
	Version  uint64
	Error    string
	Tag      string // the image tag it deployed, "" for the app's own
	Origin   *app.Origin
	Token    string
	Actor    string
	Warnings []app.Warning
}

// history stores a (as the app now is) and its deployments, oldest first.
func (d *docsDemo) history(t *testing.T, a app.App, ds ...demoDeploy) {
	t.Helper()
	ctx := context.Background()
	if _, err := d.Store.SaveApp(ctx, a, nil); err != nil {
		t.Fatalf("SaveApp(%s): %v", a.ID, err)
	}
	for _, x := range ds {
		started := d.ago(x.Ago)
		def := a
		def.Services = append([]app.Service(nil), a.Services...)
		digests := map[string]string{}
		for i := range def.Services {
			svc := &def.Services[i]
			if x.Tag != "" && i == 0 {
				svc.Image = imageRepo(svc.Image) + ":" + x.Tag
			}
			digest := demoDigest(a.ID + svc.Name + x.ID)
			digests[svc.Name] = imageRepo(svc.Image) + "@" + digest
			// The registry has the image, so a promotion or a rollback finds it.
			repo := strings.TrimPrefix(imageRepo(svc.Image), "ghcr.io/")
			if !strings.Contains(repo, "/") {
				repo = "library/" + repo
			}
			d.Reg.AddIndex(repo, digest, digest)
		}
		d.at(started, func() {
			dep := app.Deployment{ID: x.ID, Trigger: x.Trigger, Digests: digests, Origin: x.Origin, TokenName: x.Token, Actor: x.Actor}
			if _, err := d.Store.CreateDeployment(ctx, a.ID, dep, &def, nil, ""); err != nil {
				t.Fatalf("CreateDeployment(%s): %v", x.ID, err)
			}
		})
		if x.Version != 0 {
			d.at(started.Add(6*time.Second), func() {
				if _, err := d.Store.UpdateDeployment(ctx, x.ID, store.DeploymentUpdate{Status: store.StatusRunning, JobVersion: new(x.Version), Warnings: x.Warnings}); err != nil {
					t.Fatalf("running %s: %v", x.ID, err)
				}
			})
		}
		if x.Status != store.StatusPending && x.Status != store.StatusRunning {
			d.at(started.Add(x.Took), func() {
				if _, err := d.Store.UpdateDeployment(ctx, x.ID, store.DeploymentUpdate{Status: x.Status, Error: x.Error}); err != nil {
					t.Fatalf("%s %s: %v", x.ID, x.Status, err)
				}
			})
		}
	}
}

// imageRepo is the image without its tag or digest.
func imageRepo(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image
}

// demoUUID is a made-up allocation ID, the same for the same seed.
func demoUUID(seed string) string {
	h := strings.TrimPrefix(demoDigest("alloc/"+seed), "sha256:")
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:32]
}

// reserveMB is the memory a service reserves: the databases more than the rest.
func reserveMB(svc string) int64 {
	if svc == "db" {
		return 512
	}
	return 256
}

// runningCopies puts n running copies of a service at version into the fake Nomad, started since
// the deploy, with the use a small web app has (CPU in MHz, memory in MB), and returns their IDs.
func (d *docsDemo) runningCopies(jobID, svc string, version uint64, n int, since time.Duration, cpuTicks float64, memMB uint64, nodes ...int) []string {
	if len(nodes) == 0 {
		nodes = []int{0}
	}
	var stubs []api.AllocationListStub
	var ids []string
	for i := range n {
		id := demoUUID(fmt.Sprintf("%s/%s/%d/%d", jobID, svc, version, i))
		ids = append(ids, id)
		started := d.ago(since - time.Duration(i)*time.Second)
		stubs = append(stubs, api.AllocationListStub{
			ID: id, TaskGroup: svc, JobVersion: version,
			ClientStatus: "running", DesiredStatus: "run", NodeName: demoNodes[nodes[i%len(nodes)]].name, NodeID: demoNodes[nodes[i%len(nodes)]].id,
			CreateTime: started.Add(-3 * time.Second).UnixNano(),
			TaskStates: map[string]*api.TaskState{svc: {State: "running", StartedAt: started}},
			AllocatedResources: &api.AllocatedResources{Tasks: map[string]*api.AllocatedTaskResources{svc: {
				Cpu:    api.AllocatedCpuResources{CpuShares: 100},
				Memory: api.AllocatedMemoryResources{MemoryMB: reserveMB(svc), MemoryMaxMB: 2 * reserveMB(svc)},
			}}},
		})
		d.Nomad.SetStats(id, api.AllocResourceUsage{Timestamp: 1, Tasks: map[string]*api.TaskResourceUsage{svc: {ResourceUsage: &api.ResourceUsage{
			CpuStats:    &api.CpuStats{TotalTicks: cpuTicks + float64(i*7)},
			MemoryStats: &api.MemoryStats{Usage: (memMB + uint64(i*9)) << 20, Measured: []string{"Usage"}},
		}}}})
	}
	d.Nomad.SetCopies(jobID, stubs...)
	return ids
}

func shopID(environment, name string) string {
	return app.Place{Project: "shop", Environment: environment, Name: name}.ID()
}

// seedDocsPlaces makes project shop with its two environments.
func seedDocsPlaces(t *testing.T, d *docsDemo) {
	t.Helper()
	ctx := context.Background()
	d.at(d.ago(21*24*time.Hour), func() {
		if _, err := d.Svc.CreateProject(ctx, "shop", "Shop"); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
		if _, err := d.Svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
			t.Fatalf("CreateEnvironment: %v", err)
		}
	})
}

func ptr[T any](v T) *T { return &v }

// seedDocsApps makes the apps and their history.
func seedDocsApps(t *testing.T, d *docsDemo) {
	t.Helper()
	ci := func(commit string) *app.Origin {
		return &app.Origin{Commit: commit, Ref: "refs/heads/main", Actor: "ana", Repository: "acme/shop",
			Run: "https://github.com/acme/shop/actions/runs/" + strings.Repeat("7", 10)}
	}
	hc := &app.HealthCheck{Path: "/healthz"}

	web := app.App{ID: shopID("production", "web"), Name: "web", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{
		Name: "web", Image: "ghcr.io/acme/web:1.8.2", Ports: []app.Port{{Name: "http", ContainerPort: 8080}},
		Domains: []app.Domain{{Host: "shop.example.com", Port: "http"}}, Health: hc, Replicas: ptr(2),
		Env:       []app.EnvVar{{Name: "LOG_LEVEL", Value: "info"}, {Name: "PUBLIC_URL", Value: "https://shop.example.com"}},
		Resources: app.Resources{MemoryMB: 256, MemoryMaxMB: 512},
	}}}
	d.history(t, web,
		demoDeploy{ID: "web-p1", Ago: 6 * 24 * time.Hour, Took: 41 * time.Second, Trigger: app.TriggerConsole, Actor: docsDemoEmail, Status: store.StatusHealthy, Version: 1, Tag: "1.7.0"},
		demoDeploy{ID: "web-p2", Ago: 4*24*time.Hour + 3*time.Hour, Took: 38 * time.Second, Trigger: app.TriggerManual, Token: "shop-ci", Actor: docsDemoEmail, Origin: ci("9f2c1ab"), Status: store.StatusHealthy, Version: 2, Tag: "1.7.1"},
		demoDeploy{ID: "web-p3", Ago: 2*24*time.Hour + 5*time.Hour, Took: 44 * time.Second, Trigger: app.TriggerManual, Token: "shop-ci", Actor: docsDemoEmail, Origin: ci("4be07d3"), Status: store.StatusHealthy, Version: 3, Tag: "1.8.0"},
		demoDeploy{ID: "web-p4", Ago: 26 * time.Hour, Took: 2*time.Minute + 20*time.Second, Trigger: app.TriggerManual, Token: "shop-ci", Actor: docsDemoEmail, Origin: ci("c81a5e0"), Status: store.StatusReverted, Version: 4, Tag: "1.8.1", Error: "health checks failed: the new copies didn't pass /healthz in time"},
		demoDeploy{ID: "web-p5", Ago: 3 * time.Hour, Took: 47 * time.Second, Trigger: app.TriggerManual, Token: "shop-ci", Actor: docsDemoEmail, Origin: ci("e35b9a1"), Status: store.StatusHealthy, Version: 5},
	)
	d.webIDs = d.runningCopies(web.ID, "web", 5, 2, 3*time.Hour, 52, 118, 0, 1)

	webStaging := web
	webStaging.ID, webStaging.Services = shopID("staging", "web"), append([]app.Service(nil), web.Services...)
	webStaging.Services[0].Domains = []app.Domain{{Host: "staging.shop.example.com", Port: "http"}}
	webStaging.Services[0].Replicas = nil
	webStaging.Services[0].Image = "ghcr.io/acme/web:1.9.0-rc1"
	d.history(t, webStaging,
		demoDeploy{ID: "web-s1", Ago: 30 * time.Hour, Took: 39 * time.Second, Trigger: app.TriggerManual, Token: "shop-ci", Actor: docsDemoEmail, Origin: ci("b60d2f4"), Status: store.StatusHealthy, Version: 1, Tag: "1.8.2"},
		demoDeploy{ID: "web-s2", Ago: 90 * time.Minute, Took: 42 * time.Second, Trigger: app.TriggerManual, Token: "shop-ci", Actor: docsDemoEmail, Origin: ci("77d0c3e"), Status: store.StatusHealthy, Version: 2},
	)
	d.stagingIDs = d.runningCopies(webStaging.ID, "web", 2, 1, 90*time.Minute, 31, 96, 1)

	apiApp := app.App{ID: shopID("production", "api"), Name: "api", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{
		Name: "api", Image: "ghcr.io/acme/api:2.4.0", Ports: []app.Port{{Name: "http", ContainerPort: 3000}},
		Domains: []app.Domain{{Host: "api.shop.example.com", Port: "http"}}, Health: &app.HealthCheck{Path: "/health"}, Replicas: ptr(2),
		Env:       []app.EnvVar{{Name: "NODE_ENV", Value: "production"}, {Name: "DATABASE_HOST", Value: "db"}, {Name: "LOG_LEVEL", Value: "info"}},
		Secrets:   []app.Secret{{Name: "DATABASE_URL"}, {Name: "STRIPE_API_KEY"}},
		Resources: app.Resources{MemoryMB: 256, MemoryMaxMB: 512},
	}}}
	d.history(t, apiApp,
		demoDeploy{ID: "api-p1", Ago: 5 * 24 * time.Hour, Took: 52 * time.Second, Trigger: app.TriggerConsole, Actor: docsDemoEmail, Status: store.StatusHealthy, Version: 1, Tag: "2.3.0"},
		demoDeploy{ID: "api-p2", Ago: 3 * 24 * time.Hour, Took: 49 * time.Second, Trigger: app.TriggerManual, Token: "api-ci", Actor: docsDemoEmail, Origin: ci("a19f6c2"), Status: store.StatusHealthy, Version: 2, Tag: "2.3.1"},
		demoDeploy{ID: "api-p3", Ago: 20 * time.Hour, Took: 51 * time.Second, Trigger: app.TriggerManual, Token: "api-ci", Actor: docsDemoEmail, Origin: ci("d4e8b70"), Status: store.StatusHealthy, Version: 3},
	)
	d.apiIDs = d.runningCopies(apiApp.ID, "api", 3, 2, 20*time.Hour, 64, 142, 0, 2)

	db := app.App{ID: shopID("production", "db"), Name: "db", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{
		Name: "db", Image: "postgres:16.4",
		Volumes:   []app.Volume{{Name: "pgdata", Path: "/var/lib/postgresql/data"}},
		Env:       []app.EnvVar{{Name: "POSTGRES_USER", Value: "shop"}, {Name: "POSTGRES_DB", Value: "shop"}},
		Secrets:   []app.Secret{{Name: "POSTGRES_PASSWORD"}},
		Backup:    &app.Backup{Engine: "postgres", Schedule: "0 3 * * *", Keep: 7},
		Resources: app.Resources{MemoryMB: 512, MemoryMaxMB: 1024},
	}}}
	d.history(t, db,
		demoDeploy{ID: "db-p1", Ago: 18 * 24 * time.Hour, Took: 36 * time.Second, Trigger: app.TriggerConsole, Actor: docsDemoEmail, Status: store.StatusHealthy, Version: 1},
	)
	d.runningCopies(db.ID, "db", 1, 1, 18*24*time.Hour, 24, 214)
}
