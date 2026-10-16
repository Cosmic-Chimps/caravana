package deploy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
)

// whoamiDigest is the digest part of apptest.WhoamiDigest.
const whoamiDigest = "sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44"

var labSettings = nomadjob.Settings{Entrypoint: "websecure", DefaultCPUMHz: 100, DefaultMemoryMB: 64, DefaultMemoryMaxMB: 256}

// fastTiming makes every wait a few milliseconds.
var fastTiming = Timing{
	ImageTimeout:   time.Second,
	PollGap:        time.Millisecond,
	BackoffStart:   time.Millisecond,
	BackoffMax:     5 * time.Millisecond,
	VersionRetries: []time.Duration{time.Millisecond, time.Millisecond},
}

// newTestDeployer returns a Deployer wired to a fake registry and a fake
// Nomad.
func newTestDeployer(t *testing.T) (*Deployer, *registrytest.Server, *deploytest.Nomad) {
	t.Helper()
	reg := registrytest.New(t)
	nomad := deploytest.New(t)
	client, err := NewNomadClient(nomad.Addr(), "")
	if err != nil {
		t.Fatalf("NewNomadClient: %v", err)
	}
	d := New(client, &registry.Client{Endpoints: reg.Endpoints()}, labSettings)
	d.Timing = fastTiming
	return d, reg, nomad
}

func whoami() app.App { return apptest.Whoami().App }

func TestValidate(t *testing.T) {
	d, _, _ := newTestDeployer(t)
	tests := []struct {
		name      string
		mutate    func(a *app.App)
		wantField string // "" means valid
	}{
		{"valid", func(a *app.App) {}, ""},
		{"app rule", func(a *app.App) { a.Services[0].Name = "Web" }, "services[0].name"},
		{"nomad limit", func(a *app.App) { a.Services[0].Resources.MemoryMB = 5 }, "services[0].resources.memoryMB"},
		{"unparseable image", func(a *app.App) { a.Services[0].Image = "Traefik/whoami" }, "services[0].image"},
		{"invalid registry host", func(a *app.App) { a.Services[0].Image = "bad_host.example.com/x/y:1" }, "services[0].image"},
		{"registry on another host is valid", func(a *app.App) { a.Services[0].Image = "quay.io/x/y:1" }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := whoami()
			tc.mutate(&a)
			err := d.Validate(a)
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v", err)
				}
				return
			}
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tc.wantField {
				t.Errorf("Validate() = %v, want a *app.FieldError on %s", err, tc.wantField)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	ctx := context.Background()

	t.Run("pins every service", func(t *testing.T) {
		d, reg, _ := newTestDeployer(t)
		reg.AddIndex("traefik/whoami", "latest", whoamiDigest)
		a := whoami()
		a.Services = append(a.Services, app.Service{Name: "worker", Image: "ghcr.io/acme/jobs@" + whoamiDigest})

		digests, warnings, err := d.Resolve(ctx, a)
		if err != nil {
			t.Fatalf("Resolve() = %v", err)
		}
		want := map[string]string{"web": apptest.WhoamiDigest, "worker": "ghcr.io/acme/jobs@" + whoamiDigest}
		if len(digests) != 2 || digests["web"] != want["web"] || digests["worker"] != want["worker"] {
			t.Errorf("digests = %v, want %v", digests, want)
		}
		if len(warnings) != 0 {
			t.Errorf("warnings = %v, want none", warnings)
		}
		if reg.Requests() != 1 {
			t.Errorf("registry requests = %d, want 1 (the digest-only image needs none)", reg.Requests())
		}
	})

	t.Run("tag and digest warns", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		a := whoami()
		a.Services[0].Image = "traefik/whoami:v1@" + whoamiDigest
		digests, warnings, err := d.Resolve(ctx, a)
		if err != nil || digests["web"] != apptest.WhoamiDigest {
			t.Fatalf("Resolve() = %v, %v", digests, err)
		}
		if len(warnings) != 1 || warnings[0].Field != "services[0].image" {
			t.Errorf("warnings = %v, want one on services[0].image", warnings)
		}
	})

	t.Run("missing tag", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		_, _, err := d.Resolve(ctx, whoami())
		var ie *ImageError
		if !errors.As(err, &ie) || ie.Field != "services[0].image" || ie.Image != "traefik/whoami:latest" {
			t.Fatalf("Resolve() = %v, want an *ImageError on services[0].image", err)
		}
		if !errors.Is(err, ErrImageNotFound) || !errors.Is(err, registry.ErrNotFound) || errors.Is(err, ErrRegistryUnavailable) {
			t.Errorf("error %v should match ErrImageNotFound and registry.ErrNotFound only", err)
		}
	})

	t.Run("registry failing", func(t *testing.T) {
		d, reg, _ := newTestDeployer(t)
		reg.Fail("traefik/whoami", "latest", 429)
		_, _, err := d.Resolve(ctx, whoami())
		if !errors.Is(err, ErrRegistryUnavailable) || errors.Is(err, ErrImageNotFound) {
			t.Errorf("Resolve() = %v, want ErrRegistryUnavailable", err)
		}
	})

	t.Run("first failing service stops it", func(t *testing.T) {
		d, reg, _ := newTestDeployer(t)
		reg.AddIndex("traefik/whoami", "latest", whoamiDigest)
		a := whoami()
		a.Services = append(a.Services, app.Service{Name: "worker", Image: "ghcr.io/acme/jobs:main"})
		_, _, err := d.Resolve(ctx, a)
		var ie *ImageError
		if !errors.As(err, &ie) || ie.Field != "services[1].image" {
			t.Errorf("Resolve() = %v, want an *ImageError on services[1].image", err)
		}
	})
}

func TestCheckDigests(t *testing.T) {
	ctx := context.Background()
	const shopDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	twoServices := func() app.App {
		a := whoami()
		a.Services = append(a.Services, app.Service{Name: "worker", Image: "ghcr.io/acme/jobs:main"})
		return a
	}
	digests := map[string]string{"web": apptest.WhoamiDigest, "worker": "ghcr.io/acme/jobs@" + shopDigest}

	t.Run("all still there", func(t *testing.T) {
		d, reg, _ := newTestDeployer(t)
		reg.AddIndex("traefik/whoami", "latest", whoamiDigest)
		reg.AddManifest("acme/jobs", "main", shopDigest)
		if err := d.CheckDigests(ctx, twoServices(), digests); err != nil {
			t.Errorf("CheckDigests() = %v, want nil", err)
		}
	})

	t.Run("one deleted", func(t *testing.T) {
		d, reg, _ := newTestDeployer(t)
		reg.AddIndex("traefik/whoami", "latest", whoamiDigest)
		err := d.CheckDigests(ctx, twoServices(), digests)
		var ie *ImageError
		if !errors.As(err, &ie) || ie.Field != "services[1].image" || ie.Image != digests["worker"] {
			t.Fatalf("CheckDigests() = %v, want an *ImageError on services[1].image", err)
		}
		if !errors.Is(err, ErrImageNotFound) {
			t.Errorf("error %v should match ErrImageNotFound", err)
		}
	})

	t.Run("registry failing", func(t *testing.T) {
		d, reg, _ := newTestDeployer(t)
		reg.Fail("traefik/whoami", whoamiDigest, 503)
		if err := d.CheckDigests(ctx, whoami(), digests); !errors.Is(err, ErrRegistryUnavailable) {
			t.Errorf("CheckDigests() = %v, want ErrRegistryUnavailable", err)
		}
	})

	t.Run("no digest recorded for a service", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		var fe *app.FieldError
		if err := d.CheckDigests(ctx, whoami(), map[string]string{}); !errors.As(err, &fe) || fe.Field != "services[0].image" {
			t.Errorf("CheckDigests() = %v, want a field error on services[0].image", err)
		}
	})
}
