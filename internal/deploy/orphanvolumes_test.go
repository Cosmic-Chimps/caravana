package deploy

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestAllHostVolumesListsEveryAppsAndNoAppsVolumes(t *testing.T) {
	d, _, nomad := newTestDeployer(t)
	nomad.AddHostVolumeOn("shop_uploads", "node-1")
	nomad.AddHostVolumeOn("blog_data", "node-2")
	got, err := d.AllHostVolumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, v := range got {
		names = append(names, v.Name)
		if v.ID == "" || v.NodeID == "" || v.State != "ready" {
			t.Errorf("volume %+v lacks a field", v)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"blog_data", "shop_uploads"}) {
		t.Errorf("volumes = %v", names)
	}

	nomad.FailVolumes(403)
	if _, err := d.AllHostVolumes(context.Background()); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("a token Nomad refuses = %v, want ErrNomadUnavailable", err)
	}
}

func TestHostVolumeDetailAndDelete(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	path := nomad.AddHostVolumeOn("shop_uploads", "node-2")
	all, _ := d.AllHostVolumes(ctx)
	id := all[0].ID

	got, err := d.HostVolumeDetail(ctx, id)
	if err != nil || got.Name != "shop_uploads" || got.NodeID != "node-2" || got.HostPath != path || len(got.Allocations) != 0 {
		t.Fatalf("HostVolumeDetail = %+v, %v", got, err)
	}
	if _, err := d.HostVolumeDetail(ctx, "nope"); !errors.Is(err, ErrVolumeNotFound) {
		t.Errorf("HostVolumeDetail(nope) = %v, want ErrVolumeNotFound", err)
	}

	// A volume a copy claims is listed with it and is not deleted.
	nomad.ClaimHostVolume("shop_uploads", "alloc-1")
	got, _ = d.HostVolumeDetail(ctx, id)
	if len(got.Allocations) != 1 || got.Allocations[0] != "alloc-1" {
		t.Errorf("allocations = %v", got.Allocations)
	}
	if err := d.DeleteHostVolume(ctx, id); !errors.Is(err, ErrVolumeInUse) {
		t.Errorf("DeleteHostVolume of a claimed volume = %v, want ErrVolumeInUse", err)
	}
	if !slices.Equal(nomad.HostVolumes(), []string{"shop_uploads"}) {
		t.Error("a claimed volume was deleted")
	}

	nomad.ClaimHostVolume("shop_uploads")
	if err := d.DeleteHostVolume(ctx, id); err != nil {
		t.Fatalf("DeleteHostVolume = %v", err)
	}
	if got := nomad.HostVolumes(); len(got) != 0 {
		t.Errorf("volumes after the delete: %v", got)
	}
	// Already gone counts as deleted.
	if err := d.DeleteHostVolume(ctx, id); err != nil {
		t.Errorf("deleting a volume that is gone = %v", err)
	}
	nomad.FailVolumes(500)
	if err := d.DeleteHostVolume(ctx, id); err == nil || errors.Is(err, ErrVolumeInUse) {
		t.Errorf("Nomad's failure = %v", err)
	}
}
