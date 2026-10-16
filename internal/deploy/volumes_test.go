package deploy

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
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
		if _, err := d.Register(ctx, withVolume("data"), dep, owns); !errors.Is(err, ErrJobConflict) {
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
	if len(got) != 2 || got["data"].HostPath != dataPath || got["old"].HostPath == "" {
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
	if _, err := d.Register(ctx, a, dep, owns); err != nil {
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
		if _, err := d.Register(ctx, withVolume("data"), dep, owns); err != nil {
			t.Fatal(err)
		}
		nomad.SetCopies("whoami", api.AllocationListStub{ClientStatus: "running", TaskGroup: "web"})
		if err := d.DeleteVolumes(ctx, "whoami"); !errors.Is(err, ErrNomadRejected) {
			t.Errorf("DeleteVolumes = %v, want ErrNomadRejected (in use)", err)
		}
	})
}
