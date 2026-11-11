package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

// withVolume is whoami, one copy, with volumes on its web service.
func withVolume(names ...string) app.App {
	a := whoami()
	a.Services[0].Replicas = nil
	for _, n := range names {
		a.Services[0].Volumes = append(a.Services[0].Volumes, app.Volume{Name: n, Path: "/" + n})
	}
	return a
}

func TestEnsureVolumes(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	if err := d.EnsureVolumes(ctx, withVolume("data", "cache")); err != nil {
		t.Fatalf("EnsureVolumes: %v", err)
	}
	want := []string{"whoami_cache", "whoami_data"}
	if got := nomad.HostVolumes(); !slices.Equal(got, want) {
		t.Fatalf("volumes = %v, want %v", got, want)
	}
	// A second deploy uses the volumes it finds: none is created again.
	if err := d.EnsureVolumes(ctx, withVolume("data", "cache")); err != nil {
		t.Fatalf("second EnsureVolumes: %v", err)
	}
	if got := nomad.HostVolumes(); !slices.Equal(got, want) {
		t.Errorf("volumes after the second deploy = %v, want %v", got, want)
	}
	// An app without volumes asks nothing.
	if err := d.EnsureVolumes(ctx, whoami()); err != nil {
		t.Errorf("EnsureVolumes without volumes: %v", err)
	}
}

// twoServices is whoami with a second service, each with the volumes given.
func twoServices(web, db []string) app.App {
	a := withVolume(web...)
	second := a.Services[0]
	second.Name = "db"
	second.Volumes = nil
	for _, n := range db {
		second.Volumes = append(second.Volumes, app.Volume{Name: n, Path: "/" + n})
	}
	a.Services = append(a.Services, second)
	return a
}

// With several servers, Nomad would place each volume on its own; a service's
// volumes must share one node (feature 031, research L12 and D9).
func TestEnsureVolumesOneNodePerService(t *testing.T) {
	ctx := context.Background()

	t.Run("two volumes of one service get one node", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetVolumeNodes("node-a", "node-b") // asked for no node, Nomad would alternate
		if err := d.EnsureVolumes(ctx, withVolume("data", "cache")); err != nil {
			t.Fatalf("EnsureVolumes: %v", err)
		}
		// Volumes are created in the order the service lists them.
		want := []deploytest.VolumeCreate{
			{Name: "whoami_data", NodePool: "default"},
			{Name: "whoami_cache", NodeID: "node-a"},
		}
		if got := nomad.VolumeCreates(); !slices.Equal(got, want) {
			t.Errorf("creates = %+v, want %+v", got, want)
		}
		if a, b := nomad.HostVolumeNode("whoami_data"), nomad.HostVolumeNode("whoami_cache"); a != "node-a" || b != "node-a" {
			t.Errorf("nodes = %q and %q, want both node-a", a, b)
		}
	})

	t.Run("a service with an existing volume puts a new one beside it", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetVolumeNodes("node-a", "node-b")
		nomad.AddHostVolumeOn("whoami_data", "node-b")
		if err := d.EnsureVolumes(ctx, withVolume("data", "cache", "logs")); err != nil {
			t.Fatalf("EnsureVolumes: %v", err)
		}
		want := []deploytest.VolumeCreate{
			{Name: "whoami_cache", NodeID: "node-b"},
			{Name: "whoami_logs", NodeID: "node-b"},
		}
		if got := nomad.VolumeCreates(); !slices.Equal(got, want) {
			t.Errorf("creates = %+v, want %+v", got, want)
		}
		if got := nomad.HostVolumeNode("whoami_data"); got != "node-b" {
			t.Errorf("the existing volume moved to %q", got)
		}
	})

	t.Run("two services may be on different nodes", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetVolumeNodes("node-a", "node-b")
		if err := d.EnsureVolumes(ctx, twoServices([]string{"data", "cache"}, []string{"pg", "wal"})); err != nil {
			t.Fatalf("EnsureVolumes: %v", err)
		}
		node := nomad.HostVolumeNode
		if node("whoami_data") != node("whoami_cache") || node("whoami_pg") != node("whoami_wal") {
			t.Errorf("a service is split: data %q cache %q pg %q wal %q", node("whoami_data"), node("whoami_cache"), node("whoami_pg"), node("whoami_wal"))
		}
		if node("whoami_data") == node("whoami_pg") {
			t.Errorf("both services are on %q; the fake should have placed them on different nodes", node("whoami_data"))
		}
	})

	t.Run("a service already split fails naming it, and creates nothing", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddHostVolumeOn("whoami_data", "node-a")
		nomad.AddHostVolumeOn("whoami_cache", "node-b")
		// The other service has a volume to create: it must not be.
		err := d.EnsureVolumes(ctx, twoServices([]string{"data", "cache"}, []string{"pg"}))
		if err == nil {
			t.Fatal("EnsureVolumes = nil, want an error")
		}
		for _, want := range []string{"web", "whoami_data", "whoami_cache"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q doesn't name %q", err, want)
			}
		}
		if got := nomad.VolumeCreates(); len(got) != 0 {
			t.Errorf("creates = %+v, want none", got)
		}
	})

	t.Run("a second deploy creates nothing", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetVolumeNodes("node-a", "node-b")
		for range 2 {
			if err := d.EnsureVolumes(ctx, withVolume("data", "cache")); err != nil {
				t.Fatalf("EnsureVolumes: %v", err)
			}
		}
		if got := len(nomad.VolumeCreates()); got != 2 {
			t.Errorf("creates = %d, want 2", got)
		}
	})
}

func TestEnsureVolumesErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("nomad down", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Down()
		if err := d.EnsureVolumes(ctx, withVolume("data")); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("EnsureVolumes = %v, want ErrNomadUnavailable", err)
		}
	})
	t.Run("token refused", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.FailVolumes(403)
		err := d.EnsureVolumes(ctx, withVolume("data"))
		if !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("EnsureVolumes = %v, want ErrNomadUnavailable", err)
		}
	})
	t.Run("a foreign job creates nothing", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.AddForeignJob(&api.Job{ID: new("whoami"), Name: new("whoami")})
		owns := func(context.Context, string) (bool, error) { return false, nil }
		dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{"web": "traefik/whoami@" + whoamiDigest}}
		if _, err := d.Register(ctx, withVolume("data"), dep, owns, nil); !errors.Is(err, ErrJobConflict) {
			t.Fatalf("Register = %v, want ErrJobConflict", err)
		}
		if got := nomad.HostVolumes(); len(got) != 0 {
			t.Errorf("volumes = %v, want none", got)
		}
	})
}

func TestHostVolumes(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	// None yet: an empty answer, not an error.
	got, err := d.HostVolumes(ctx, "whoami")
	if err != nil || len(got) != 0 {
		t.Fatalf("HostVolumes of nothing = %v, %v", got, err)
	}

	dataPath := nomad.AddHostVolume("whoami_data")
	nomad.AddHostVolume("whoami_old")
	nomad.AddHostVolume("whoami2_data") // another app's volume is not listed
	got, err = d.HostVolumes(ctx, "whoami")
	if err != nil {
		t.Fatalf("HostVolumes: %v", err)
	}
	if len(got) != 2 || got["data"].HostPath != dataPath || got["old"].HostPath == "" || got["data"].NodeID != "node-1" {
		t.Errorf("HostVolumes = %v, want data at %s and old", got, dataPath)
	}

	t.Run("nomad down", func(t *testing.T) {
		nomad.Down()
		if _, err := d.HostVolumes(ctx, "whoami"); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("HostVolumes = %v, want ErrNomadUnavailable", err)
		}
	})
}

func TestDeleteVolumes(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	d.Timing.VolumeDeleteWait = 2 * time.Second

	// Nothing to delete is fine.
	if err := d.DeleteVolumes(ctx, "whoami"); err != nil {
		t.Fatalf("DeleteVolumes of nothing: %v", err)
	}

	a := withVolume("data")
	dep := app.Deployment{ID: "d1", Trigger: app.TriggerManual, Digests: map[string]string{"web": "traefik/whoami@" + whoamiDigest}}
	owns := func(context.Context, string) (bool, error) { return true, nil }
	if _, err := d.Register(ctx, a, dep, owns, nil); err != nil {
		t.Fatal(err)
	}
	nomad.SetCopies("whoami", api.AllocationListStub{ClientStatus: "running", TaskGroup: "web"})
	other := withVolume("data")
	other.ID = "whoami2" // another app's volume must stay
	if err := d.EnsureVolumes(ctx, other); err != nil {
		t.Fatal(err)
	}

	// The copy lets go of the volume a moment after the purge.
	go func() {
		time.Sleep(50 * time.Millisecond)
		nomad.SetCopies("whoami")
	}()
	if err := d.DeleteVolumes(ctx, "whoami"); err != nil {
		t.Fatalf("DeleteVolumes: %v", err)
	}
	if got := nomad.HostVolumes(); !slices.Equal(got, []string{"whoami2_data"}) {
		t.Errorf("volumes = %v, want only the other app's", got)
	}

	t.Run("still in use when the wait ends", func(t *testing.T) {
		d.Timing.VolumeDeleteWait = 20 * time.Millisecond
		dep.ID = "d2"
		if _, err := d.Register(ctx, withVolume("data"), dep, owns, nil); err != nil {
			t.Fatal(err)
		}
		nomad.SetCopies("whoami", api.AllocationListStub{ClientStatus: "running", TaskGroup: "web"})
		if err := d.DeleteVolumes(ctx, "whoami"); !errors.Is(err, ErrNomadRejected) {
			t.Errorf("DeleteVolumes = %v, want ErrNomadRejected (in use)", err)
		}
	})
}
