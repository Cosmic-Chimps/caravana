package store

import (
	"context"
	"testing"
	"time"
)

// The person who started something is kept with it (feature 053, D9); what no person started has none,
// and a record that was made before people reads back with none.
func TestActorRoundTrips(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)

	t.Run("a deployment", func(t *testing.T) {
		s := storeWithApp(t)
		d := newDeployment("d1")
		d.Actor = "ana@example.com"
		rec, err := s.CreateDeployment(ctx, "whoami", d, nil, nil, "")
		if err != nil || rec.Deployment.Actor != "ana@example.com" {
			t.Fatalf("CreateDeployment = %+v, %v", rec.Deployment.Actor, err)
		}
		for name, read := range map[string]func() (DeploymentRecord, error){
			"one":    func() (DeploymentRecord, error) { return s.GetDeployment(ctx, "d1") },
			"recent": func() (DeploymentRecord, error) { l, err := s.ListRecentDeployments(ctx, 5); return l[0], err },
			"app":    func() (DeploymentRecord, error) { l, err := s.ListDeployments(ctx, "whoami", 5); return l[0], err },
		} {
			if got, err := read(); err != nil || got.Deployment.Actor != "ana@example.com" {
				t.Errorf("%s: actor = %q, %v", name, got.Deployment.Actor, err)
			}
		}
		if rec, err := s.CreateDeployment(ctx, "whoami", newDeployment("d2"), nil, nil, ""); err != nil || rec.Deployment.Actor != "" {
			t.Errorf("a deployment with none = %q, %v", rec.Deployment.Actor, err)
		}
	})

	t.Run("an action", func(t *testing.T) {
		s := storeWithApp(t)
		for i, actor := range []string{"ana@example.com", ""} {
			a := ActionRecord{ID: "act_" + string(rune('a'+i)), AppID: "whoami", Action: ActionRestart, Trigger: "console", Status: ActionRunning,
				StartedAt: t0.Add(time.Duration(i) * time.Minute), Actor: actor}
			if err := s.InsertAction(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		list, err := s.ListActions(ctx, "whoami", 5)
		if err != nil || len(list) != 2 || list[0].Actor != "" || list[1].Actor != "ana@example.com" {
			t.Errorf("actions = %+v, %v; want the newest with none and the older Ana's", list, err)
		}
	})

	t.Run("a backup keeps the person through the watcher's later write", func(t *testing.T) {
		s := storeWithApp(t)
		b := BackupRecord{AppID: "whoami", ID: "b1", Service: "web", JobID: "j1", Status: BackupRunning, StartedAt: t0,
			Trigger: BackupTriggerOwner, Actor: "ana@example.com"}
		if err := s.SaveBackup(ctx, b); err != nil {
			t.Fatal(err)
		}
		done := t0.Add(time.Minute)
		finished := BackupRecord{AppID: "whoami", ID: "b1", Service: "web", JobID: "j1", Status: BackupSucceeded, StartedAt: t0, FinishedAt: &done, Bytes: 9}
		if err := s.SaveBackup(ctx, finished); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetBackup(ctx, "whoami", "b1")
		if err != nil || got.Actor != "ana@example.com" || got.Status != BackupSucceeded || got.Trigger != BackupTriggerOwner {
			t.Errorf("backup = %+v, %v; want finished, the owner's and Ana's", got, err)
		}

		// The watcher may finish a run before Back up now writes its own record: the person still lands.
		early := BackupRecord{AppID: "whoami", ID: "b2", Service: "web", JobID: "j2", Status: BackupSucceeded, StartedAt: t0, FinishedAt: &done}
		if err := s.SaveBackup(ctx, early); err != nil {
			t.Fatal(err)
		}
		late := BackupRecord{AppID: "whoami", ID: "b2", Service: "web", JobID: "j2", Status: BackupRunning, StartedAt: t0, Trigger: BackupTriggerOwner, Actor: "ben@example.com"}
		if err := s.SaveBackup(ctx, late); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetBackup(ctx, "whoami", "b2"); got.Actor != "ben@example.com" || got.Status != BackupSucceeded {
			t.Errorf("backup = %+v; want it finished and Ben's", got)
		}

		timer := BackupRecord{AppID: "whoami", ID: "b3", Service: "web", JobID: "j3", Status: BackupSucceeded, StartedAt: t0}
		if err := s.SaveBackup(ctx, timer); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetBackup(ctx, "whoami", "b3"); got.Actor != "" {
			t.Errorf("a timed backup has actor %q", got.Actor)
		}
		backups, restores, err := s.LatestBackupRuns(ctx)
		if err != nil || len(backups) != 1 || len(restores) != 0 {
			t.Fatalf("LatestBackupRuns = %d, %d, %v", len(backups), len(restores), err)
		}
	})

	t.Run("a restore", func(t *testing.T) {
		s := storeWithApp(t)
		r := RestoreRecord{ID: "r1", AppID: "whoami", Service: "web", BackupID: "b1", Status: BackupRunning, CreatedAt: t0, Actor: "ana@example.com"}
		if err := s.SaveRestore(ctx, r); err != nil {
			t.Fatal(err)
		}
		done := t0.Add(time.Minute)
		r.Status, r.FinishedAt, r.Actor = BackupSucceeded, &done, ""
		if err := s.SaveRestore(ctx, r); err != nil {
			t.Fatal(err)
		}
		list, err := s.ListRestores(ctx, "whoami", 5)
		if err != nil || len(list) != 1 || list[0].Actor != "ana@example.com" || list[0].Status != BackupSucceeded {
			t.Errorf("restores = %+v, %v; want finished and Ana's", list, err)
		}
	})
}
