package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// getVolumes reads GET /v1/apps/{id}/volumes and checks each element against
// the contract's AppVolume schema (Volume is the definition's own).
func getVolumes(t *testing.T, e *testEnv, appID string) []volumeJSON {
	t.Helper()
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/"+appID+"/volumes", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET volumes = %d %s", status, raw)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("GET volumes answered %s, want a JSON array: %v", raw, err)
	}
	doc := loadContract(t)
	for _, item := range items {
		checkSchema(t, doc, item, "AppVolume")
	}
	return decodeAs[[]volumeJSON](t, raw)
}

// Feature 016, US3: GET /v1/apps/{id}/volumes lists the definition's volumes
// with their mounts, host path, size and last backup, then the volumes Nomad
// has that no service mounts; an app without volumes gets [], an unknown one
// 404, and a Nomad that can't be read still answers.
func TestListVolumes(t *testing.T) {
	e := newTestEnv(t, "")
	if status, _, raw := call(t, http.MethodPut, e.URL+"/v1/backups/destination", "application/json", destBody); status != http.StatusOK {
		t.Fatalf("PUT destination = %d %s", status, raw)
	}
	if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/shop/compose", "application/json", volumeBackupBody(t)); status != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", status, raw)
	}
	// The deploy creates the volume in the background.
	var got []volumeJSON
	waitUntil(t, func() bool {
		got = getVolumes(t, e, "shop")
		return len(got) == 1 && got[0].HostPath != ""
	})

	t.Run("deployed, never measured, never backed up", func(t *testing.T) {
		v := got[0]
		if v.Name != "kuma-data" || !v.Mounted || !v.BackedUp || v.Size != nil || v.LastBackup != nil ||
			len(v.Mounts) != 1 || v.Mounts[0] != (volumeMountJSON{Service: "kuma", Path: "/app/data"}) {
			t.Errorf("volume = %+v, want kuma-data mounted at kuma:/app/data, backed up by policy, no size and no backup yet", v)
		}
	})

	t.Run("measured, backed up, and a volume no service mounts", func(t *testing.T) {
		started := time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC)
		e.Nomad.SetCopies("shop", api.AllocationListStub{ID: "c1", TaskGroup: "kuma", ClientStatus: "running", JobVersion: 1,
			TaskStates: map[string]*api.TaskState{"kuma": {StartedAt: started}}})
		e.Nomad.WriteAllocFile("c1", "alloc/data/caravana/sizes", "kuma-data 312\n")
		e.Nomad.AddHostVolume("shop_old")
		if err := e.Store.SaveBackup(t.Context(), store.BackupRecord{AppID: "shop", ID: "kuma-1792119600", Service: "kuma",
			Engine: "volume", Status: store.BackupSucceeded, StartedAt: started}); err != nil {
			t.Fatal(err)
		}

		_, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/shop/volumes", "", "")
		got := decodeAs[[]volumeJSON](t, raw)
		if len(got) != 2 {
			t.Fatalf("volumes = %s, want kuma-data and old", raw)
		}
		v, old := got[0], got[1]
		if v.Size == nil || v.Size.Bytes != 312*1024 || !v.Size.MeasuredAt.Equal(started) ||
			v.LastBackup == nil || v.LastBackup.ID != "kuma-1792119600" || v.LastBackup.Status != "succeeded" {
			t.Errorf("kuma-data = %+v, want 312 KiB measured at the copy's start and its last backup", v)
		}
		if old.Name != "old" || old.Mounted || len(old.Mounts) != 0 || old.HostPath == "" || old.BackedUp || old.Size != nil {
			t.Errorf("old = %+v, want an unmounted volume with a host path", old)
		}
		// An empty mounts list is [], not null: clients iterate it.
		var rawOld []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rawOld); err != nil || string(rawOld[1]["mounts"]) != "[]" {
			t.Errorf("unmounted volume's mounts = %s, want []", rawOld[1]["mounts"])
		}
	})

	t.Run("Nomad down: no host paths, nothing else lost", func(t *testing.T) {
		e.Nomad.Down()
		got := getVolumes(t, e, "shop")
		if len(got) != 1 || got[0].Name != "kuma-data" || got[0].HostPath != "" || got[0].Size == nil || got[0].Size.Bytes != 312*1024 {
			t.Errorf("volumes with Nomad down = %+v, want kuma-data with its stored size and no host path", got)
		}
	})

	t.Run("an unknown app", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/nope/volumes", "", "")
		if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != "not_found" {
			t.Errorf("GET volumes of an unknown app = %d %s, want 404 not_found", status, raw)
		}
	})
}

// An app with no volumes answers an empty array, not null.
func TestListVolumesEmpty(t *testing.T) {
	e := newTestEnv(t, "")
	deployOK(t, e, "whoami", whoamiBody)
	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami/volumes", "", "")
	if status != http.StatusOK || string(raw) != "[]\n" && string(raw) != "[]" {
		t.Errorf("GET volumes of an app without volumes = %d %q, want 200 []", status, raw)
	}
}
