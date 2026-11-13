package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Scheduled restores (feature 036) in the gate: the volume and sqlite steps pin
// a backup as golden, change the data, deploy a restore schedule whose one slot
// is a minute or two ahead, and wait for Caravana's clock to restore it. These
// are the helpers they share.

// pinBackup pins backupID of appID under label and checks the answer says so.
func (g *gate) pinBackup(t *testing.T, appID, backupID, label string) {
	t.Helper()
	resp := g.api(t, http.MethodPut, "/v1/apps/"+appID+"/backups/"+backupID+"/pin", "application/json", `{"label": "`+label+`"}`)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var b struct {
		ID  string `json:"id"`
		Pin string `json:"pin"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &b) != nil || b.ID != backupID || b.Pin != label {
		t.Fatalf("pin %s as %s: %d %s", backupID, label, resp.StatusCode, raw)
	}
}

// soonSlot is a daily cron line whose next slot is at least lead from now, at a
// whole minute, and that slot.
func soonSlot(lead time.Duration) (string, time.Time) {
	slot := time.Now().UTC().Add(lead).Truncate(time.Minute).Add(time.Minute)
	return fmt.Sprintf("%d %d * * *", slot.Minute(), slot.Hour()), slot
}

// deployRestoreSchedule deploys appJSON (whose backup policy ends in
// `"keep": 2}`) with a restore of label at the next slot two minutes or more
// ahead, waits for the deploy to be healthy and returns it with the slot. The
// deploy must not warn about the label: the step pinned it first.
func (g *gate) deployRestoreSchedule(t *testing.T, appID, appJSON, label string) (gateDeployment, time.Time) {
	t.Helper()
	spec, slot := soonSlot(2 * time.Minute)
	body := strings.Replace(appJSON, `"keep": 2}`, `"keep": 2, "restore": {"schedule": "`+spec+`", "from": "`+label+`"}}`, 1)
	if body == appJSON {
		t.Fatal("the app's backup policy doesn't end in \"keep\": 2}")
	}
	resp := g.api(t, http.MethodPost, "/v1/apps/"+appID+"/deployments", "application/json", body)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var d gateDeployment
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &d) != nil {
		t.Fatalf("deploy %s with a restore schedule: %d %s", appID, resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "backup.restore.from") {
		t.Errorf("the deploy warns about the label %s, which is pinned: %s", label, raw)
	}
	if final := g.waitFinalOf(t, appID, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
	}
	if !time.Now().Before(slot) {
		t.Fatalf("the deploy took until %s, past the restore's slot %s: nothing to wait for", time.Now().UTC().Format(time.TimeOnly), slot.Format(time.TimeOnly))
	}
	t.Logf("a restore of %s is due at %s UTC (%s)", label, slot.Format(time.TimeOnly), spec)
	return d, slot
}

// waitScheduledRestore waits for the restore the clock starts at slot to
// succeed, and checks it is recorded as the schedule's, from label, of backupID.
func (g *gate) waitScheduledRestore(t *testing.T, appID, backupID, label string, slot time.Time) gateVolumeRestore {
	t.Helper()
	var restore gateVolumeRestore
	waitFor(t, "the scheduled restore to succeed", time.Until(slot)+4*time.Minute, func() bool {
		var l struct {
			Restores []gateVolumeRestore `json:"restores"`
		}
		g.getJSON(t, "/v1/apps/"+appID+"/backups", &l)
		for _, r := range l.Restores {
			if r.Trigger != "schedule" {
				continue
			}
			switch r.Status {
			case "failed", "skipped":
				t.Fatalf("the scheduled restore %s: %s", r.Status, r.Message)
			case "succeeded":
				restore = r
				return true
			}
		}
		return false
	})
	if restore.BackupID != backupID || restore.Pin != label {
		t.Errorf("the scheduled restore = %+v, want backup %s from %s", restore, backupID, label)
	}
	if late := restore.CreatedAt.Sub(slot); late < 0 || late > time.Minute {
		t.Errorf("the scheduled restore started at %s, want within a minute after its slot %s", restore.CreatedAt, slot)
	}
	return restore
}
