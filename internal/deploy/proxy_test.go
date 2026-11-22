package deploy

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/hashicorp/nomad/api"
)

func traefikStub(id, status string, created int64) api.AllocationListStub {
	return api.AllocationListStub{ID: id, TaskGroup: "traefik", ClientStatus: status, DesiredStatus: "run", CreateTime: created}
}

func TestProxyCopy(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	if _, err := d.ProxyCopy(ctx); !errors.Is(err, ErrNoProxy) {
		t.Fatalf("no job: err = %v, want ErrNoProxy", err)
	}
	nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true")
	if _, err := d.ProxyCopy(ctx); !errors.Is(err, ErrNoProxy) {
		t.Fatalf("no allocation: err = %v, want ErrNoProxy", err)
	}
	// Only a running allocation counts; with two, the newest.
	nomad.SetCopies("traefik",
		traefikStub("old-finished", "complete", 300),
		traefikStub("old-running", "running", 100),
		traefikStub("new-running", "running", 200),
		traefikStub("pending", "pending", 400))
	got, err := d.ProxyCopy(ctx)
	if err != nil {
		t.Fatalf("ProxyCopy: %v", err)
	}
	if got.ID != "new-running" || got.Service != ProxyTask {
		t.Errorf("ProxyCopy = %q (service %q), want new-running and %q", got.ID, got.Service, ProxyTask)
	}
	if got.CreatedAt == nil {
		t.Error("CreatedAt is nil")
	}

	// A copy Nomad is stopping is not the one to read.
	stopping := traefikStub("stopping", "running", 900)
	stopping.DesiredStatus = "stop"
	nomad.SetCopies("traefik", traefikStub("new-running", "running", 200), stopping)
	if got, err := d.ProxyCopy(ctx); err != nil || got.ID != "new-running" {
		t.Errorf("with one stopping: %q, %v; want new-running", got.ID, err)
	}

	nomad.Down()
	defer nomad.Up()
	if _, err := d.ProxyCopy(ctx); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("Nomad down: err = %v, want ErrNomadUnavailable", err)
	}
}

func TestProxyCopies(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	if _, err := d.ProxyCopies(ctx); !errors.Is(err, ErrNoProxy) {
		t.Fatalf("no job: err = %v, want ErrNoProxy", err)
	}
	nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true")
	// Only finished allocations: none is running.
	nomad.SetCopies("traefik", traefikStub("done", "complete", 100))
	if _, err := d.ProxyCopies(ctx); !errors.Is(err, ErrNoProxy) {
		t.Fatalf("no running allocation: err = %v, want ErrNoProxy", err)
	}

	stopping := traefikStub("stopping", "running", 250)
	stopping.DesiredStatus = "stop"
	nomad.SetCopies("traefik",
		traefikStub("finished-old", "complete", 100),
		traefikStub("lost", "lost", 150),
		traefikStub("pending", "pending", 400),
		traefikStub("unknown", "unknown", 450),
		traefikStub("failed", "failed", 300),
		stopping,
		traefikStub("running", "running", 500))
	copies, err := d.ProxyCopies(ctx)
	if err != nil {
		t.Fatalf("ProxyCopies: %v", err)
	}
	var ids []string
	for _, c := range copies {
		ids = append(ids, c.ID)
	}
	want := []string{"running", "failed", "stopping", "lost", "finished-old"}
	if !slices.Equal(ids, want) {
		t.Fatalf("order = %v, want %v", ids, want)
	}
	if copies[0].Gone || !copies[1].Gone || !copies[4].Gone {
		t.Errorf("Gone = %v %v %v, want false, true, true", copies[0].Gone, copies[1].Gone, copies[4].Gone)
	}
	if first, err := d.ProxyCopy(ctx); err != nil || first.ID != "running" {
		t.Errorf("ProxyCopy = %q, %v; want running", first.ID, err)
	}

	nomad.Down()
	defer nomad.Up()
	if _, err := d.ProxyCopies(ctx); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("Nomad down: err = %v, want ErrNomadUnavailable", err)
	}
}

func TestProxyAccessLog(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	if _, err := d.ProxyAccessLog(ctx); !errors.Is(err, ErrNoProxy) {
		t.Fatalf("no job: err = %v, want ErrNoProxy", err)
	}
	for _, tt := range []struct {
		name string
		args []string
		want bool
	}{
		{"no arguments", nil, false},
		{"the installer's job before 047", []string{"--metrics.prometheus=true", "--log.level=INFO"}, false},
		{"access log on", []string{"--api.dashboard=true", "--accesslog=true", "--accesslog.format=json"}, true},
		{"access log fields without it", []string{"--accesslog.format=json"}, false},
		{"access log off", []string{"--accesslog=false"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			nomad.SetTraefikJob("traefik:v3.7.13", tt.args...)
			got, err := d.ProxyAccessLog(ctx)
			if err != nil || got != tt.want {
				t.Errorf("ProxyAccessLog = %v, %v; want %v", got, err, tt.want)
			}
		})
	}

	nomad.Down()
	defer nomad.Up()
	if _, err := d.ProxyAccessLog(ctx); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("Nomad down: err = %v, want ErrNomadUnavailable", err)
	}
}
