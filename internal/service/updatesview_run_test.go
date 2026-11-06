package service

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// updRig is a notify env (with its channels and a clock to move) that runs 0.16.0, has seen 0.17.0
// and has an updater installed.
type updRig struct {
	*notifyEnv
	dir     string
	request string
}

var updNow0 = time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC)

func newUpdRig(t *testing.T) *updRig {
	t.Helper()
	r := &updRig{notifyEnv: newNotifyEnv(t), dir: t.TempDir()}
	r.request = filepath.Join(t.TempDir(), "update-request")
	r.clockAt(updNow0)
	r.svc.resolveAfter = 5 * time.Minute
	r.svc.SetPublicURL("https://caravana.example.com")
	r.svc.SetVersion("0.16.0")
	r.svc.SetUpdaterDir(r.dir)
	r.svc.SetUpdateRequestPath(r.request)
	if err := os.WriteFile(filepath.Join(r.dir, "updater"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, _ := r.store.ReleaseCheck(context.Background())
	rc.LatestTag = "v0.17.0"
	if err := r.store.SaveReleaseResult(context.Background(), rc, nil); err != nil {
		t.Fatal(err)
	}
	return r
}

// finishedRun stores a run that ended in state, `ago` before the clock.
func (r *updRig) finishedRun(t *testing.T, id, version, from, state, reason string, ago time.Duration) store.UpdateRun {
	t.Helper()
	ctx := context.Background()
	end := r.svc.clock.Now().UTC().Add(-ago)
	run, err := r.store.InsertUpdateRun(ctx, store.UpdateRun{ID: id, Version: version, FromVersion: from, Trigger: store.UpdateByConsole,
		RequestedAt: end.Add(-3 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	log := ""
	if state != store.UpdateRefused {
		log = filepath.Join(r.dir, "logs", id+".log")
	}
	if err := r.store.SetUpdateRunState(ctx, id, state, end.Add(-100*time.Second), end, reason, log); err != nil {
		t.Fatal(err)
	}
	run.State, run.Reason, run.Log, run.StartedAt, run.FinishedAt = state, reason, log, end.Add(-100*time.Second), end
	return run
}

func (r *updRig) view(t *testing.T) UpdatesView {
	t.Helper()
	v, err := r.svc.Updates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestUpdatesViewUpdater(t *testing.T) {
	ctx := context.Background()

	t.Run("installed, nothing running", func(t *testing.T) {
		r := newUpdRig(t)
		c := r.view(t).Caravana
		if !c.Updater.Installed || c.Updater.Unavailable != "" || c.Run != nil || c.Auto.Enabled || c.Auto.Hour != 3 || !c.Auto.Next.IsZero() || c.Auto.Blocked != "" {
			t.Errorf("caravana = %+v", c)
		}
	})

	t.Run("not installed says why", func(t *testing.T) {
		r := newUpdRig(t)
		if err := os.Remove(filepath.Join(r.dir, "updater")); err != nil {
			t.Fatal(err)
		}
		c := r.view(t).Caravana
		if c.Updater.Installed || !strings.Contains(c.Updater.Unavailable, "Run the installer of 0.17.0 once on the server") {
			t.Errorf("updater = %+v", c.Updater)
		}
		if c.Auto.Blocked != reasonAutoNeedsUpdater {
			t.Errorf("auto blocked = %q", c.Auto.Blocked)
		}
	})

	t.Run("a development build", func(t *testing.T) {
		r := newUpdRig(t)
		r.svc.SetVersion("dev")
		c := r.view(t).Caravana
		if c.Updater.Unavailable != reasonDevBuild || c.Auto.Blocked != reasonAutoDevBuild {
			t.Errorf("caravana = %+v", c)
		}
	})

	t.Run("the release check off blocks automatic updates only", func(t *testing.T) {
		r := newUpdRig(t)
		if err := r.svc.SetReleaseCheck(ctx, false); err != nil {
			t.Fatal(err)
		}
		c := r.view(t).Caravana
		if c.Auto.Blocked != reasonAutoNeedsCheck || c.Updater.Unavailable != "" {
			t.Errorf("caravana = %+v", c)
		}
	})

	t.Run("the run to show", func(t *testing.T) {
		r := newUpdRig(t)
		if r.view(t).Caravana.Run != nil {
			t.Fatal("a run with none made")
		}
		// A run that finished 25 hours ago isn't shown; 23 hours ago it is.
		r.finishedRun(t, "old", "0.16.0", "0.15.0", store.UpdateUpdated, "", 25*time.Hour)
		if r.view(t).Caravana.Run != nil {
			t.Error("a run from 25 hours ago is shown")
		}
		r.finishedRun(t, "recent", "0.16.1", "0.16.0", store.UpdateFailed, "x", 23*time.Hour)
		if got := r.view(t).Caravana.Run; got == nil || got.ID != "recent" {
			t.Errorf("run = %+v, want the one from 23 hours ago", got)
		}
		// An open run wins over a finished one.
		if _, err := r.svc.RequestUpdate(ctx, "0.17.0", store.UpdateByConsole, ""); err != nil {
			t.Fatal(err)
		}
		if got := r.view(t).Caravana.Run; got == nil || got.State != store.UpdateRequested || got.Version != "0.17.0" {
			t.Errorf("run = %+v, want the open one", got)
		}
	})

	t.Run("the automatic setting and its next time", func(t *testing.T) {
		r := newUpdRig(t)
		rc, _ := r.store.ReleaseCheck(ctx)
		rc.LatestTag = "v0.16.3"
		if err := r.store.SaveReleaseResult(ctx, rc, nil); err != nil {
			t.Fatal(err)
		}
		if err := r.svc.SetAutoUpdate(ctx, true, 5); err != nil {
			t.Fatal(err)
		}
		a := r.view(t).Caravana.Auto
		if !a.Enabled || a.Hour != 5 || !a.Next.Equal(time.Date(2026, 11, 7, 5, 0, 0, 0, time.UTC)) || a.Blocked != "" {
			t.Errorf("auto = %+v, want on at 5, next Nov 7 05:00 UTC", a)
		}
		// A version that had a run is never next again.
		r.finishedRun(t, "x", "0.16.3", "0.16.0", store.UpdateRolledBack, "no", time.Hour)
		if a := r.view(t).Caravana.Auto; !a.Next.IsZero() {
			t.Errorf("next = %v for a version that had a run", a.Next)
		}
	})
}

func TestUpdateHistoryWithRuns(t *testing.T) {
	r := newUpdRig(t)
	ctx := context.Background()
	t0 := updNow0

	r.finishedRun(t, "ok", "0.17.0", "0.16.0", store.UpdateUpdated, "", 20*time.Hour) // ends t0-20h
	// The new version started before the updater wrote "updated": a version event 2 minutes before it.
	for _, e := range []store.HostEvent{
		{Kind: store.HostEventVersion, At: t0.Add(-20*time.Hour - 2*time.Minute), Detail: `{"from":"0.16.0","to":"0.17.0"}`, Key: "version|0.17.0"},
		{Kind: store.HostEventVersion, At: t0.Add(-10 * time.Hour), Detail: `{"from":"0.17.0","to":"0.17.2"}`, Key: "version|0.17.2"}, // by hand
		{Kind: store.HostEventBoot, At: t0.Add(-5 * time.Hour), Detail: `{}`, Key: "boot|1"},
	} {
		if _, err := r.store.AppendHostEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	r.finishedRun(t, "bad", "0.17.3", "0.17.2", store.UpdateRolledBack, "Caravana 0.17.3 didn't answer its health check within 60 s; 0.17.2 runs again", 3*time.Hour)
	r.finishedRun(t, "lost", "0.17.4", "0.17.2", store.UpdateLost, reasonLostStart, 2*time.Hour)
	var rows []string
	for _, h := range r.view(t).History {
		rows = append(rows, h.Kind+"|"+h.State+"|"+h.Text+"|"+h.Detail)
	}
	want := []string{
		"update|lost|caravana 0.17.2 → 0.17.4|didn't finish",
		"update|rolled_back|caravana 0.17.2 → 0.17.3|rolled back · Caravana 0.17.3 didn't answer its health check within 60 s…",
		"boot||The server restarted|",
		"version||caravana 0.17.0 → 0.17.2|",
		"update|updated|caravana 0.16.0 → 0.17.0|from the console · 1 min 40 s",
	}
	if len(rows) != len(want) {
		t.Fatalf("history =\n%s\nwant\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, rows[i], want[i])
		}
	}
}

func TestUpdateHistoryIsTheLastTen(t *testing.T) {
	r := newUpdRig(t)
	for i := range 12 {
		r.finishedRun(t, "run"+string(rune('a'+i)), "0.17.0", "0.16.0", store.UpdateFailed, "x", time.Duration(i+1)*time.Hour)
	}
	h := r.view(t).History
	if len(h) != historyShown {
		t.Fatalf("history has %d rows, want %d", len(h), historyShown)
	}
	for i := 1; i < len(h); i++ {
		if h[i].At.After(h[i-1].At) {
			t.Errorf("rows %d and %d are out of order", i-1, i)
		}
	}
}

func TestShortReason(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"didn't answer its health check", "didn't answer its health check"},
		{"didn't answer; 0.16.0 runs again", "didn't answer…"},
		{"Version 0.17.0 didn't start. See the log.", "Version 0.17.0 didn't start…"},
		{"ends with a stop.", "ends with a stop"},
		{strings.Repeat("a", 80), strings.Repeat("a", 60) + "…"},
	}
	for _, tc := range tests {
		if got := shortReason(tc.in); got != tc.want {
			t.Errorf("shortReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRunDuration(t *testing.T) {
	base := time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		took time.Duration
		want string
	}{
		{40 * time.Second, "40 s"}, {2 * time.Minute, "2 min"}, {100 * time.Second, "1 min 40 s"},
		{time.Hour, "1 h"}, {75 * time.Minute, "1 h 15 min"}, {0, "0 s"},
	}
	for _, tc := range tests {
		r := store.UpdateRun{StartedAt: base, FinishedAt: base.Add(tc.took)}
		if got := runDuration(r); got != tc.want {
			t.Errorf("runDuration(%v) = %q, want %q", tc.took, got, tc.want)
		}
	}
	// Without a start it is counted from the request.
	if got := runDuration(store.UpdateRun{RequestedAt: base, FinishedAt: base.Add(3 * time.Minute)}); got != "3 min" {
		t.Errorf("from the request = %q", got)
	}
}

func TestUpdateItemOf(t *testing.T) {
	fin := updNow0
	run := func(state string) store.UpdateRun {
		return store.UpdateRun{ID: updHex, Version: "0.17.0", FromVersion: "0.16.0", State: state, Reason: "the reason", FinishedAt: fin}
	}
	tests := []struct {
		name    string
		run     store.UpdateRun
		running string
		ok      bool
		level   metrics.DiskLevel
		text    string
	}{
		{"rolled back", run("rolled_back"), "0.16.0", true, metrics.DiskWarning, "caravana 0.17.0 didn't start, so 0.16.0 runs again."},
		{"failed", run("failed"), "0.16.0", true, metrics.DiskFailed, "The update to caravana 0.17.0 failed: the reason."},
		{"refused", run("refused"), "0.16.0", true, metrics.DiskWarning, "The server's updater refused caravana 0.17.0: the reason."},
		{"lost", run("lost"), "0.16.0", true, metrics.DiskWarning, "The update to caravana 0.17.0 didn't finish: the reason."},
		{"updated has none", run("updated"), "0.16.0", false, "", ""},
		{"open has none", run("running"), "0.16.0", false, "", ""},
		{"the running version caught up", run("failed"), "0.17.0", false, "", ""},
		{"the running version passed it", run("rolled_back"), "0.17.2", false, "", ""},
		{"a development build has none", run("failed"), "dev", false, "", ""},
		{"no version set", run("failed"), "", false, "", ""},
		{"a reason with a full stop", func() store.UpdateRun { r := run("failed"); r.Reason = "It broke."; return r }(), "0.16.0", true, metrics.DiskFailed,
			"The update to caravana 0.17.0 failed: It broke."},
		{"no reason", func() store.UpdateRun { r := run("failed"); r.Reason = ""; return r }(), "0.16.0", true, metrics.DiskFailed,
			"The update to caravana 0.17.0 failed."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it, ok := updateItemOf(tc.run, tc.running)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if it.Kind != AttentionCaravanaUpdateFailed || it.Level != tc.level || it.Detail != tc.text || it.AppID != "" ||
				!it.When.Equal(fin) || it.UpdateRunID != updHex || problemKey(it) != "caravana_update_failed|||"+updHex {
				t.Errorf("item = %+v, key %q", it, problemKey(it))
			}
			if got := plainText(ProblemParts(it, func(s string) string { return s })); got != tc.text {
				t.Errorf("ProblemParts = %q, want %q", got, tc.text)
			}
		})
	}
}

func TestUpdateItemPartsHaveMonoVersions(t *testing.T) {
	it, _ := updateItemOf(store.UpdateRun{ID: updHex, Version: "0.17.0", FromVersion: "0.16.0", State: store.UpdateRolledBack, FinishedAt: updNow0}, "0.16.0")
	var monos []string
	for _, p := range ProblemParts(it, nil) {
		if p.Mono {
			monos = append(monos, p.Text)
		}
	}
	if !slices.Equal(monos, []string{"0.17.0", "0.16.0"}) {
		t.Errorf("mono parts = %v", monos)
	}
}

// The story of a failed update: it opens with one message, nothing while it lasts, and one "resolved"
// once a later run succeeds (or the running version catches up) and the item has been gone for the
// delay.
func TestUpdateFailedStory(t *testing.T) {
	ctx := context.Background()
	r := newUpdRig(t)

	r.check(t)
	if got := r.deliveriesOf(t, r.problems); len(got) != 0 {
		t.Fatalf("nothing failed yet: %v", problemTypes(got))
	}

	r.finishedRun(t, updHex, "0.17.0", "0.16.0", store.UpdateRolledBack, "Caravana 0.17.0 didn't answer its health check; 0.16.0 runs again", time.Minute)
	att, err := r.svc.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var item *AttentionItem
	for i := range att.Items {
		if att.Items[i].Kind == AttentionCaravanaUpdateFailed {
			item = &att.Items[i]
		}
	}
	if item == nil || item.AppID != "" || item.Detail != "caravana 0.17.0 didn't start, so 0.16.0 runs again." {
		t.Fatalf("attention = %+v, want the update item", att.Items)
	}

	r.check(t)
	r.check(t)
	got := r.deliveriesOf(t, r.problems)
	if len(got) != 1 || got[0].EventType != notify.EventProblemOpened {
		t.Fatalf("after the failure: %v, want one problem.opened", problemTypes(got))
	}
	ev := payloadOf(t, got[0])
	if ev.Kind != "caravana_update_failed" || ev.Title != "caravana update failed" ||
		ev.Text != "caravana 0.17.0 didn't start, so 0.16.0 runs again." || ev.URL != "https://caravana.example.com/ops/updates" {
		t.Errorf("message = %+v", ev)
	}
	if keys := r.sentKeys(t); !slices.Equal(keys, []string{"caravana_update_failed|||" + updHex}) {
		t.Errorf("sent keys = %v", keys)
	}

	// A later run succeeds: it is the newest finished run, so the item goes, and after the delay
	// one "resolved" is sent.
	r.svc.clock.(*fakeClock).Advance(10 * time.Minute)
	r.finishedRun(t, "9a8b7c6d5e4f30211a2b3c4d5e6f7a8b", "0.17.0", "0.16.0", store.UpdateUpdated, "", time.Minute)
	r.svc.SetVersion("0.17.0")
	r.check(t)
	r.svc.clock.(*fakeClock).Advance(6 * time.Minute)
	r.check(t)
	r.check(t)
	if got := r.deliveriesOf(t, r.problems); len(got) != 2 || got[1].EventType != notify.EventProblemResolved {
		t.Fatalf("after the success: %v, want opened then resolved", problemTypes(got))
	}
	if keys := r.sentKeys(t); len(keys) != 0 {
		t.Errorf("sent keys = %v, want none", keys)
	}
}

func TestUpdateFailedEndsWhenTheRunningVersionCatchesUp(t *testing.T) {
	r := newUpdRig(t)
	r.finishedRun(t, updHex, "0.17.0", "0.16.0", store.UpdateFailed, "x", time.Minute)
	if att, _ := r.svc.Attention(context.Background()); !hasAttentionKind(att, AttentionCaravanaUpdateFailed) {
		t.Fatal("no item for a failed run")
	}
	r.svc.SetVersion("0.17.0") // updated by hand
	if att, _ := r.svc.Attention(context.Background()); hasAttentionKind(att, AttentionCaravanaUpdateFailed) {
		t.Error("the item stays after the running version caught up")
	}
}

func hasAttentionKind(a Attention, k AttentionKind) bool {
	for _, it := range a.Items {
		if it.Kind == k {
			return true
		}
	}
	return false
}

func TestActivityUpdateRuns(t *testing.T) {
	r := newUpdRig(t)
	ctx := context.Background()
	r.finishedRun(t, "a1", "0.17.0", "0.16.0", store.UpdateUpdated, "", 4*time.Hour)
	_, _ = r.store.AppendHostEvent(ctx, store.HostEvent{Kind: store.HostEventVersion, At: updNow0.Add(-4*time.Hour - time.Minute),
		Detail: `{"from":"0.16.0","to":"0.17.0"}`, Key: "version|0.17.0"})
	r.finishedRun(t, "a2", "0.17.1", "0.17.0", store.UpdateRolledBack, "Caravana 0.17.1 didn't start", 3*time.Hour)
	r.finishedRun(t, "a3", "0.17.2", "0.17.0", store.UpdateFailed, "The installer stopped with exit code 4.", 2*time.Hour)
	r.finishedRun(t, "a4", "0.17.3", "0.17.0", store.UpdateRefused, "Couldn't download it.", time.Hour)
	r.finishedRun(t, "a5", "0.17.4", "0.17.0", store.UpdateLost, "The updater stopped without saying how it went.", 30*time.Minute)

	entries, err := r.svc.Activity(ctx, ActivityQuery{Limit: 20, Server: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Status+"|"+e.Sentence)
		if e.AppID != "" {
			t.Errorf("entry %q has an app", e.Sentence)
		}
		if e.Kind != ActivityServer {
			t.Errorf("entry %q has kind %q, want server", e.Sentence, e.Kind)
		}
	}
	want := []string{
		"warning|The update to caravana 0.17.4 didn't finish: The updater stopped without saying how it went.",
		"warning|The server's updater refused caravana 0.17.3: Couldn't download it.",
		"failed|The update to caravana 0.17.2 failed: The installer stopped with exit code 4.",
		"warning|The update to caravana 0.17.1 was rolled back: Caravana 0.17.1 didn't start.",
		"healthy|caravana was updated from 0.16.0 to 0.17.0 from the console.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("activity =\n%s\nwant\n%s\n(the version event the run explains must not be listed again)", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
