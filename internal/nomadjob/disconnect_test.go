package nomadjob

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// disconnectCases is one app with every shape of the disconnect policy: a
// routed stateless service with an HTTP check (a block that replaces, and the
// proxy's health check), the same with an override, a volume service and a
// publishing one (no replacement), a worker, and a routed service with a TCP
// check (no block, no health check tags).
func disconnectCases() []goldenCase {
	shop := app.App{
		ID:     "shop",
		Source: app.Source{Kind: app.SourceImage},
		Services: []app.Service{
			{
				Name:    "web",
				Image:   "ghcr.io/acme/web:main",
				Ports:   []app.Port{{Name: "http", ContainerPort: 3000}},
				Domains: []app.Domain{{Host: "shop.example.com", Port: "http"}},
				Health:  &app.HealthCheck{Path: "/up", Interval: 15 * time.Second, Timeout: 3 * time.Second},
			},
			{
				Name:       "api",
				Image:      "ghcr.io/acme/api:main",
				Ports:      []app.Port{{Name: "http", ContainerPort: 8080}},
				Domains:    []app.Domain{{Host: "api.example.com", Port: "http"}},
				Health:     &app.HealthCheck{Path: "/healthz"},
				Disconnect: &app.Disconnect{LostAfterSeconds: 120},
			},
			{
				Name:    "db",
				Image:   "docker.io/library/postgres:17",
				Ports:   []app.Port{{Name: "pg", ContainerPort: 5432}},
				Health:  &app.HealthCheck{},
				Volumes: []app.Volume{{Name: "data", Path: "/var/lib/postgresql/data"}},
			},
			{
				Name:  "turn",
				Image: "ghcr.io/acme/turn:1",
				Ports: []app.Port{
					{Name: "http", ContainerPort: 8081},
					{Name: "turn", ContainerPort: 3478, Protocol: app.ProtocolUDP, Published: 3478},
				},
				Health: &app.HealthCheck{Port: "http", Path: "/up"},
			},
			{Name: "worker", Image: "ghcr.io/acme/worker:main", Disconnect: &app.Disconnect{LostAfterSeconds: 3600}},
			{
				Name:    "legacy",
				Image:   "ghcr.io/acme/legacy:main",
				Ports:   []app.Port{{Name: "http", ContainerPort: 9000}},
				Domains: []app.Domain{{Host: "legacy.example.com", Port: "http"}},
				Health:  &app.HealthCheck{},
			},
		},
	}
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{
		"web":    "ghcr.io/acme/web@sha256:" + strings.Repeat("a", 64),
		"api":    "ghcr.io/acme/api@sha256:" + strings.Repeat("b", 64),
		"db":     "postgres@sha256:" + strings.Repeat("c", 64),
		"turn":   "ghcr.io/acme/turn@sha256:" + strings.Repeat("d", 64),
		"worker": "ghcr.io/acme/worker@sha256:" + strings.Repeat("e", 64),
		"legacy": "ghcr.io/acme/legacy@sha256:" + strings.Repeat("f", 64),
	}}
	return []goldenCase{{name: "disconnect", app: shop, deployment: dep, settings: labSettings()}}
}

func TestDisconnectBlocks(t *testing.T) {
	c := disconnectCases()[0]
	res, err := Translate(c.app, c.deployment, c.settings, nil)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	groups := map[string]*api.TaskGroup{}
	for _, g := range res.Job.TaskGroups {
		groups[*g.Name] = g
	}

	tests := []struct {
		group     string
		none      bool // no block
		lostAfter time.Duration
		replace   bool
	}{
		{group: "web", lostAfter: 5 * time.Minute, replace: true},
		{group: "api", lostAfter: 2 * time.Minute, replace: true},
		{group: "db", lostAfter: 5 * time.Minute, replace: false},
		{group: "turn", lostAfter: 5 * time.Minute, replace: false},
		{group: "worker", lostAfter: time.Hour, replace: true},
		{group: "legacy", none: true},
	}
	for _, tc := range tests {
		t.Run(tc.group, func(t *testing.T) {
			d := groups[tc.group].Disconnect
			if tc.none {
				if d != nil {
					t.Fatalf("Disconnect = %+v, want none", d)
				}
				return
			}
			if d == nil || d.LostAfter == nil || d.Replace == nil || d.Reconcile == nil {
				t.Fatalf("Disconnect = %+v, want every field set", d)
			}
			if *d.LostAfter != tc.lostAfter {
				t.Errorf("lost_after = %v, want %v", *d.LostAfter, tc.lostAfter)
			}
			if *d.Replace != tc.replace {
				t.Errorf("replace = %v, want %v", *d.Replace, tc.replace)
			}
			if *d.Reconcile != "keep_original" {
				t.Errorf("reconcile = %q, want keep_original", *d.Reconcile)
			}
		})
	}
}

func TestProxyHealthCheckTags(t *testing.T) {
	c := disconnectCases()[0]
	res, err := Translate(c.app, c.deployment, c.settings, nil)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	tagsOf := func(group string) []string {
		for _, g := range res.Job.TaskGroups {
			if *g.Name == group && len(g.Services) > 0 {
				return g.Services[0].Tags
			}
		}
		return nil
	}
	has := func(tags []string, prefix string) []string {
		var out []string
		for _, tag := range tags {
			if strings.Contains(tag, prefix) {
				out = append(out, tag)
			}
		}
		return out
	}
	const hc = ".loadbalancer.healthcheck."

	web := tagsOf("web")
	want := []string{
		"traefik.http.services.shop-web.loadbalancer.healthcheck.path=/up",
		"traefik.http.services.shop-web.loadbalancer.healthcheck.interval=15s",
		"traefik.http.services.shop-web.loadbalancer.healthcheck.timeout=3s",
	}
	if got := has(web, hc); !slices.Equal(got, want) {
		t.Errorf("web health check tags = %v, want %v", got, want)
	}
	// The defaults are the Nomad check's.
	wantAPI := []string{
		"traefik.http.services.shop-api.loadbalancer.healthcheck.path=/healthz",
		"traefik.http.services.shop-api.loadbalancer.healthcheck.interval=10s",
		"traefik.http.services.shop-api.loadbalancer.healthcheck.timeout=2s",
	}
	if got := has(tagsOf("api"), hc); !slices.Equal(got, wantAPI) {
		t.Errorf("api health check tags = %v, want %v", got, wantAPI)
	}
	// A service with no domain has no tags at all; a routed one the proxy
	// can't check has none for the health check.
	if got := has(tagsOf("db"), hc); len(got) != 0 {
		t.Errorf("db tags = %v, want none", got)
	}
	if got := has(tagsOf("legacy"), hc); len(got) != 0 {
		t.Errorf("legacy (TCP check) health check tags = %v, want none", got)
	}
	if len(tagsOf("legacy")) == 0 {
		t.Error("legacy has no router tags, want its routing unchanged")
	}
}

// A schedule run ends on its own: its job has no disconnect block, even for a
// service that has one.
func TestScheduleRunHasNoDisconnect(t *testing.T) {
	c := disconnectCases()[0]
	svc := c.app.Services[1] // api, with an override
	j := ScheduleRunJob(c.app, "r1", svc, c.deployment.Digests[svc.Name], []string{"echo"}, 0, c.settings, nil)
	for _, g := range j.TaskGroups {
		if g.Disconnect != nil {
			t.Errorf("schedule run group %s has a disconnect block", *g.Name)
		}
	}
}
