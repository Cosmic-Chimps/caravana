package service

import (
	"context"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The person who asked is recorded on what they started (feature 053, D9): deployments (the
// rights tests check theirs), actions, backups taken by hand and restores. What Caravana did
// itself has none.
func TestActorIsRecorded(t *testing.T) {
	ctx := context.Background()
	ana := WithPrincipal(ctx, Principal{PersonID: "ana", Email: "ana@example.com", OrgRole: OrgAdmin, TokenName: "ci"})
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, db())

	t.Run("a deployment, through a token or a session alike", func(t *testing.T) {
		rec, err := e.svc.Redeploy(ana, "whoami", app.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		got, _ := e.store.GetDeployment(ctx, rec.Deployment.ID)
		if got.Deployment.Actor != "ana@example.com" || got.Deployment.TokenName != "ci" {
			t.Errorf("stored deployment: actor %q, token %q; want ana@example.com through ci", got.Deployment.Actor, got.Deployment.TokenName)
		}
		// Caravana itself (the webhook, a schedule) leaves none.
		rec, err = e.svc.Redeploy(ctx, "whoami", app.TriggerWebhook)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		if got, _ := e.store.GetDeployment(ctx, rec.Deployment.ID); got.Deployment.Actor != "" {
			t.Errorf("a webhook deployment has actor %q, want none", got.Deployment.Actor)
		}
	})

	t.Run("an action", func(t *testing.T) {
		if _, err := e.svc.StopApp(ana, "whoami", app.TriggerConsole); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.StartApp(ctx, "whoami", app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		acts, err := e.svc.ListActions(ctx, "whoami", 10)
		if err != nil || len(acts) != 2 {
			t.Fatalf("actions = %+v, %v", acts, err)
		}
		// Newest first: the start Caravana ran, then the stop Ana asked for.
		if acts[0].Action != store.ActionStart || acts[0].Actor != "" || acts[1].Action != store.ActionStop || acts[1].Actor != "ana@example.com" {
			t.Errorf("actions = %+v; want the stop by ana@example.com and the start by no one", acts)
		}
	})

	t.Run("a backup by hand and the restore of it", func(t *testing.T) {
		b, err := e.svc.BackUpNow(ana, "whoami", "web")
		if err != nil {
			t.Fatal(err)
		}
		if b.Actor != "ana@example.com" {
			t.Errorf("BackUpNow's record: actor %q", b.Actor)
		}
		e.nomad.FinishRun(b.JobID, true, "", "caravana-backup key=backups/caravana/whoami/web/1.dump bytes=1499\n", "")
		waitFor(t, "the backup to be recorded", func() bool {
			got, err := e.store.GetBackup(ctx, "whoami", b.ID)
			return err == nil && got.Status == store.BackupSucceeded
		})
		// The watcher saved the finished record without knowing who asked: the person stays.
		got, _ := e.store.GetBackup(ctx, "whoami", b.ID)
		if got.Actor != "ana@example.com" || got.Trigger != store.BackupTriggerOwner {
			t.Errorf("the finished backup: actor %q, trigger %q; want ana@example.com by the owner", got.Actor, got.Trigger)
		}

		r, err := e.svc.Restore(ana, "whoami", b.ID, "")
		if err != nil || r.Actor != "ana@example.com" {
			t.Fatalf("Restore = %+v, %v; want actor ana@example.com", r, err)
		}
		e.nomad.FinishRun("whoami_web_restore", true, "", "", "")
		waitFor(t, "the restore to be recorded", func() bool {
			_, restores, err := e.svc.ListBackups(ctx, "whoami")
			return err == nil && len(restores) == 1 && restores[0].Status == store.BackupSucceeded
		})
		_, restores, _ := e.svc.ListBackups(ctx, "whoami")
		if restores[0].Actor != "ana@example.com" {
			t.Errorf("the finished restore's actor = %q", restores[0].Actor)
		}

		// A backup the host asks for has none.
		e.nomad.FinishRun("whoami_web_restore", true, "", "", "")
		b2, err := e.svc.BackUpNow(ctx, "whoami", "web")
		if err != nil || b2.Actor != "" {
			t.Errorf("BackUpNow with no principal = %+v, %v; want no actor", b2, err)
		}
	})
}
