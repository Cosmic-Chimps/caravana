package service

import (
	"context"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// saveScheduledRestore records a finished scheduled restore of whoami's web.
func saveScheduledRestore(t *testing.T, st *store.Store, id, status, message string, at time.Time) {
	t.Helper()
	done := at.Add(time.Second)
	err := st.SaveRestore(context.Background(), store.RestoreRecord{ID: id, AppID: "whoami", Service: "web", BackupID: "web-1",
		Engine: app.BackupVolume, Status: status, Message: message, CreatedAt: at, FinishedAt: &done,
		Trigger: store.RestoreTriggerSchedule, Pin: "golden"})
	if err != nil {
		t.Fatal(err)
	}
}

// restoreItems is the restore_failed items of the needs-you list.
func restoreItems(t *testing.T, svc *Service) []AttentionItem {
	t.Helper()
	att, err := svc.Attention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []AttentionItem
	for _, it := range att.Items {
		if it.Kind == AttentionRestoreFailed {
			out = append(out, it)
		}
	}
	return out
}

// A refused scheduled restore reaches the needs-you list with its reason, a
// skipped one never hides it, and a later restore that worked clears it.
func TestAttentionOfScheduledRestores(t *testing.T) {
	l := newVolumeLab(t)
	t0 := time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)
	const reason = "no backup of web is pinned as golden"

	if got := restoreItems(t, l.svc); len(got) != 0 {
		t.Fatalf("items before any restore = %+v", got)
	}
	saveScheduledRestore(t, l.store, "r1", store.BackupFailed, reason, t0)
	got := restoreItems(t, l.svc)
	if len(got) != 1 || got[0].AppID != "whoami" || got[0].Service != "web" || got[0].Detail != reason || !got[0].When.Equal(t0.Add(time.Second)) {
		t.Fatalf("items = %+v, want one restore_failed with the reason as its detail", got)
	}

	// The next slot found the service busy: skipped, and the failure stays.
	saveScheduledRestore(t, l.store, "r2", store.RestoreSkipped, "a backup or restore of web was running", t0.Add(24*time.Hour))
	if got := restoreItems(t, l.svc); len(got) != 1 || got[0].Detail != reason {
		t.Errorf("items after a skipped slot = %+v, want the failure kept", got)
	}

	// A slot that worked clears it.
	saveScheduledRestore(t, l.store, "r3", store.BackupSucceeded, "", t0.Add(48*time.Hour))
	if got := restoreItems(t, l.svc); len(got) != 0 {
		t.Errorf("items after a restore that worked = %+v, want none", got)
	}

	// A skipped slot alone is no problem.
	l2 := newVolumeLab(t)
	saveScheduledRestore(t, l2.store, "r1", store.RestoreSkipped, "a backup or restore of web was running", t0)
	if got := restoreItems(t, l2.svc); len(got) != 0 {
		t.Errorf("items with only a skipped slot = %+v, want none", got)
	}
}

// The owner's channels hear of a refused scheduled restore: problem.opened with
// the reason, once, and problem.resolved when a later restore worked.
func TestNotifyScheduledRestoreFailure(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 4, 0, 0, 0, time.UTC)
	e := newNotifyEnv(t)
	e.clockAt(t0)
	e.svc.resolveAfter = 5 * time.Minute
	setDestination(t, e.env)
	deployHealthy(t, e.env, kuma(false))
	const reason = "the backup pinned as golden was deleted by retention"

	e.check(t)
	if got := e.deliveriesOf(t, e.problems); len(got) != 0 {
		t.Fatalf("before the restore: %v", got)
	}
	saveScheduledRestore(t, e.store, "r1", store.BackupFailed, reason, t0)
	e.check(t)
	got := e.deliveriesOf(t, e.problems)
	if len(got) != 1 || got[0].EventType != notify.EventProblemOpened {
		t.Fatalf("after a refused slot: %+v, want one problem.opened", got)
	}
	if ev := payloadOf(t, got[0]); ev.Kind != "restore_failed" || ev.AppID != "whoami" || ev.Service != "web" || ev.Detail != reason {
		t.Errorf("the message = %+v, want restore_failed with the reason as its detail", ev)
	}

	// A skipped slot changes nothing: no second opening, no resolution.
	saveScheduledRestore(t, e.store, "r2", store.RestoreSkipped, "a backup or restore of web was running", t0.Add(time.Minute))
	e.clockAt(t0.Add(10 * time.Minute))
	e.check(t)
	e.check(t)
	if got := e.deliveriesOf(t, e.problems); len(got) != 1 {
		t.Fatalf("after a skipped slot: %d messages, want still 1", len(got))
	}

	// A restore that worked, gone for the delay: resolved.
	saveScheduledRestore(t, e.store, "r3", store.BackupSucceeded, "", t0.Add(11*time.Minute))
	e.clockAt(t0.Add(12 * time.Minute))
	e.check(t)
	e.clockAt(t0.Add(18 * time.Minute))
	e.check(t)
	got = e.deliveriesOf(t, e.problems)
	if len(got) != 2 || got[1].EventType != notify.EventProblemResolved {
		t.Fatalf("after a restore that worked: %+v, want opened then resolved", got)
	}
}

// The activity feed says a skipped restore was skipped, not that it is running.
func TestRunWordsSkippedRestore(t *testing.T) {
	status, parts := runWords(store.RestoreSkipped, []TextPart{txt("Shop")}, "'s restore of ", "web")
	if status != ActivityIdle || plainText(parts) != "Shop's restore of web was skipped: a backup or restore was running." {
		t.Errorf("runWords = %q, %q", status, plainText(parts))
	}
}
