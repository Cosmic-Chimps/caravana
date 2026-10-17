package service

import (
	"context"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// FR-011: what tells the owner something names where the app is.

// The needs-you list carries the stored place of each item's app; the
// installation-wide item has none.
func TestAttentionItemsCarryThePlace(t *testing.T) {
	staging := appRec("shop--staging--web", at(0), "web")
	staging.Place = app.Place{Project: "shop", Environment: "staging", Name: "web"}
	blog := appRec("blog", at(0), "web")
	blog.Place = app.Place{Project: "default", Environment: "production", Name: "blog"}
	apps := []store.AppRecord{staging, blog}
	latest := map[string]store.DeploymentRecord{
		"shop--staging--web": dep("shop--staging--web", "d1", store.StatusFailed, nil, at(5), "x"),
	}
	backups := []store.BackupRecord{{AppID: "blog", Service: "web", Status: store.BackupFailed, StartedAt: at(1)}}
	// The apps ask for backups and nowhere to put them.
	apps[1].App.Services[0].Backup = &app.Backup{Engine: app.BackupPostgres}

	got := attentionOf(attNow, apps, latest, nil, nil, backups, nil, false)
	byKind := map[AttentionKind]AttentionItem{}
	for _, it := range got.Items {
		byKind[it.Kind] = it
	}
	if it := byKind[AttentionDeployFailed]; it.Project != "shop" || it.Environment != "staging" || it.AppName != "web" {
		t.Errorf("failed deploy = %+v, want shop/staging/web", it)
	}
	if it := byKind[AttentionBackupFailed]; it.Project != "default" || it.Environment != "production" || it.AppName != "blog" {
		t.Errorf("failed backup = %+v, want default/production/blog", it)
	}
	if it, ok := byKind[AttentionNoBackupDestination]; !ok || it.Project != "" || it.Environment != "" || it.AppName != "" {
		t.Errorf("no destination = %+v, want an item with no place", it)
	}
}

// Events carry the place of their app, and a promotion says where it came from.
func TestEventsCarryThePlace(t *testing.T) {
	place := app.Place{Project: "shop", Environment: "production", Name: "web"}

	t.Run("a problem", func(t *testing.T) {
		ev := problemEvent(AttentionItem{Kind: AttentionDeployFailed, AppID: "shop--production--web", Deployment: "d1",
			Project: "shop", Environment: "production", AppName: "web", DeploymentStatus: store.StatusFailed}, publicBase, notedAt)
		if ev.Project != "shop" || ev.Environment != "production" || ev.AppName != "web" || ev.Title != "shop--production--web: deploy failed" {
			t.Errorf("event = %+v, want the place and the app ID in the title", ev)
		}
	})

	t.Run("a resolved problem", func(t *testing.T) {
		ev := inPlace(resolvedEvent(store.SentProblem{Key: "backup_failed|shop--production--web|db|", Kind: "backup_failed",
			AppID: "shop--production--web", Service: "db", Summary: "x"}, publicBase, notedAt), place)
		if ev.Project != "shop" || ev.Environment != "production" || ev.AppName != "web" {
			t.Errorf("event = %+v", ev)
		}
		// An event of no app has no place to say.
		if ev := inPlace(notify.Event{Type: notify.EventProblemResolved}, app.Place{}); ev.Project != "" || ev.Environment != "" || ev.AppName != "" {
			t.Errorf("event = %+v, want no place", ev)
		}
	})

	rec := func(sourceApp, sourceEnv string, status store.Status) store.DeploymentRecord {
		return store.DeploymentRecord{
			Deployment: app.Deployment{ID: "dep_7", Trigger: app.TriggerConsole}, AppID: "shop--production--web",
			Status: status, JobVersion: ver(3), Place: place, SourceApp: sourceApp, SourceEnvironment: sourceEnv, UpdatedAt: notedAt,
		}
	}
	t.Run("a deploy", func(t *testing.T) {
		ev, _ := deployEvent(rec("", "", store.StatusHealthy), publicBase)
		if ev.Project != "shop" || ev.Environment != "production" || ev.AppName != "web" || ev.PromotedFrom != "" {
			t.Errorf("event = %+v, want the place and no promotion", ev)
		}
		if ev.Text != "shop--production--web's deploy v3 is healthy, started from the console." {
			t.Errorf("text = %q", ev.Text)
		}
	})
	t.Run("a promotion says where it came from, whatever its outcome", func(t *testing.T) {
		for status, want := range map[store.Status]string{
			store.StatusHealthy: "shop--production--web's deploy v3 is healthy, promoted from staging, started from the console.",
			store.StatusFailed:  "shop--production--web's deploy v3 failed, promoted from staging, started from the console.",
		} {
			ev, _ := deployEvent(rec("shop--staging--web", "staging", status), publicBase)
			if ev.PromotedFrom != "staging" || ev.Text != want {
				t.Errorf("%s: PromotedFrom %q, text %q, want staging and %q", status, ev.PromotedFrom, ev.Text, want)
			}
			if ev.Title[:len("shop--production--web:")] != "shop--production--web:" {
				t.Errorf("title = %q, want the app ID kept", ev.Title)
			}
		}
	})
}

// A promotion that ends is told to the channels with the place and its source:
// the deploy event of the target, queued by the real flow.
func TestPromotionNotifies(t *testing.T) {
	ctx := context.Background()
	e := newDeployNotifyEnv(t)
	if _, err := e.svc.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	setDestination(t, e.env)
	deployHealthy(t, e.env, production())
	deployHealthy(t, e.env, staging())
	e.settle()
	drain := len(e.events(t, e.both))

	rec, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	e.settle()

	evs := e.events(t, e.both)
	if len(evs) != drain+1 {
		t.Fatalf("events = %d, want %d plus the promotion's", len(evs), drain)
	}
	ev := evs[len(evs)-1]
	if ev.Type != notify.EventDeploySucceeded || ev.AppID != productionID || ev.Project != "shop" || ev.Environment != "production" ||
		ev.AppName != "web" || ev.PromotedFrom != "staging" || ev.Deployment != rec.Deployment.ID {
		t.Errorf("event = %+v, want the production deploy, promoted from staging", ev)
	}
}
