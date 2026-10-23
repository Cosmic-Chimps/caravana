package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 022 T015: the schedule kinds of the needs-you list, and their
// notifications.

// schedOutput is what the runs of these tests printed. No item, event or
// delivery may hold it.
const schedOutput = "s3cret-sentinel-022-attention"

func schedRec(id, appID, name string) store.ScheduleRecord {
	return store.ScheduleRecord{ID: id, AppID: appID, Service: "web", Name: name, Command: []string{"true"},
		Cron: "0 3 * * *", StopAfter: 5 * time.Minute, MemoryMB: 256, SkipIfRunning: true}
}

// endedRun is a run of schedule s that ended at at(m) with status and words.
func endedRun(s store.ScheduleRecord, id, status, words string, code *int, m int) store.ScheduleRunRecord {
	return store.ScheduleRunRecord{ID: id, ScheduleID: s.ID, AppID: s.AppID, Service: s.Service, Trigger: RunTriggerTimer,
		Status: status, ExitCode: code, Error: words, StartedAt: at(m - 1), FinishedAt: tp(at(m)),
		OutputKept: true, Stdout: schedOutput, Stderr: schedOutput}
}

// The pure rules: which runs make which item, with which words.
func TestScheduleItems(t *testing.T) {
	apps := []store.AppRecord{appRec("blog", at(0), "web")}
	rates := schedRec("aaaaaaaaaaaa", "blog", "rates-sync")
	other := schedRec("bbbbbbbbbbbb", "blog", "digest")
	gone := schedRec("cccccccccccc", "ghost", "orphan") // its app was deleted

	tests := []struct {
		name   string
		facts  scheduleFacts
		want   []AttentionItem // Kind, Schedule, ScheduleID, Service, ExitCode, When, Detail
		absent bool
	}{
		{name: "an ok run", facts: scheduleFacts{Schedules: []store.ScheduleRecord{rates},
			Latest: []store.ScheduleRunRecord{endedRun(rates, "r1", store.RunOK, "", new(0), 5)}}},
		{name: "exit 3", facts: scheduleFacts{Schedules: []store.ScheduleRecord{rates},
			Latest: []store.ScheduleRunRecord{endedRun(rates, "r1", store.RunFailed, "exit 3", new(3), 5)}},
			want: []AttentionItem{{Kind: AttentionScheduleFailed, Schedule: "rates-sync", ScheduleID: rates.ID,
				ExitCode: new(3), When: at(5), Detail: "Exited with code 3."}}},
		{name: "at its limit", facts: scheduleFacts{Schedules: []store.ScheduleRecord{rates},
			Latest: []store.ScheduleRunRecord{endedRun(rates, "r1", store.RunStoppedAtLimit, "stopped after 5 minutes", new(143), 5)}},
			want: []AttentionItem{{Kind: AttentionScheduleFailed, Schedule: "rates-sync", ScheduleID: rates.ID,
				When: at(5), Detail: "Stopped at the 5 min limit."}}},
		{name: "Nomad refused it", facts: scheduleFacts{Schedules: []store.ScheduleRecord{rates},
			Latest: []store.ScheduleRunRecord{endedRun(rates, "r1", store.RunCouldNotStart, "Nomad refused the job", nil, 5)}},
			want: []AttentionItem{{Kind: AttentionScheduleFailed, Schedule: "rates-sync", ScheduleID: rates.ID,
				When: at(5), Detail: "Couldn't start: Nomad refused the job."}}},
		{name: "never started", facts: scheduleFacts{Schedules: []store.ScheduleRecord{rates},
			Latest: []store.ScheduleRunRecord{endedRun(rates, "r1", store.RunCouldNotStart, "could not start", nil, 5)}},
			want: []AttentionItem{{Kind: AttentionScheduleFailed, Schedule: "rates-sync", ScheduleID: rates.ID,
				When: at(5), Detail: "Couldn't start."}}},
		{name: "out of memory has no exit code to tell", facts: scheduleFacts{Schedules: []store.ScheduleRecord{rates},
			Latest: []store.ScheduleRunRecord{endedRun(rates, "r1", store.RunFailed, "ran out of memory", new(137), 5)}},
			want: []AttentionItem{{Kind: AttentionScheduleFailed, Schedule: "rates-sync", ScheduleID: rates.ID,
				When: at(5), Detail: "Ran out of memory."}}},
		{name: "two failing schedules are two items", facts: scheduleFacts{Schedules: []store.ScheduleRecord{rates, other},
			Latest: []store.ScheduleRunRecord{endedRun(rates, "r1", store.RunFailed, "exit 1", new(1), 5),
				endedRun(other, "r2", store.RunFailed, "exit 2", new(2), 5)}},
			want: []AttentionItem{
				{Kind: AttentionScheduleFailed, Schedule: "rates-sync", ScheduleID: rates.ID, ExitCode: new(1), When: at(5), Detail: "Exited with code 1."},
				{Kind: AttentionScheduleFailed, Schedule: "digest", ScheduleID: other.ID, ExitCode: new(2), When: at(5), Detail: "Exited with code 2."},
			}},
		{name: "a removed schedule's run", facts: scheduleFacts{Schedules: []store.ScheduleRecord{other},
			Latest: []store.ScheduleRunRecord{endedRun(rates, "r1", store.RunFailed, "exit 3", new(3), 5)}}},
		{name: "a schedule of an app that is gone", facts: scheduleFacts{Schedules: []store.ScheduleRecord{gone},
			Latest: []store.ScheduleRunRecord{endedRun(gone, "r1", store.RunFailed, "exit 3", new(3), 5)}}},
		{name: "skipping", facts: scheduleFacts{Schedules: []store.ScheduleRecord{rates},
			Skipping: map[string]time.Time{rates.ID: at(9)}},
			want: []AttentionItem{{Kind: AttentionScheduleSkipping, Schedule: "rates-sync", ScheduleID: rates.ID, When: at(9),
				Detail: "rates-sync has been skipped 3 times: each run takes longer than the time between runs."}}},
		{name: "a gone app's skipping", facts: scheduleFacts{Schedules: []store.ScheduleRecord{gone},
			Skipping: map[string]time.Time{gone.ID: at(9)}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := attentionOf(attNow, apps, nil, nil, nil, nil, nil, true, tc.facts).Items
			if len(got) != len(tc.want) {
				t.Fatalf("items = %+v, want %d", got, len(tc.want))
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Kind != w.Kind || g.AppID != "blog" || g.Service != "web" || g.Schedule != w.Schedule ||
					g.ScheduleID != w.ScheduleID || !g.When.Equal(w.When) || g.Detail != w.Detail ||
					(g.ExitCode == nil) != (w.ExitCode == nil) || (g.ExitCode != nil && *g.ExitCode != *w.ExitCode) ||
					len(g.Copies) != 0 {
					t.Errorf("item %d = %+v, want %+v", i, g, w)
				}
				if strings.Contains(fmt.Sprint(g), schedOutput) {
					t.Errorf("item %d holds the run's output: %+v", i, g)
				}
			}
		})
	}
}

// schedAttentionEnv is an env with whoami deployed and a schedule of it, for
// the rules that the store's queries decide.
func schedAttentionEnv(t *testing.T) (*env, store.ScheduleRecord) {
	t.Helper()
	e := newEnv(t)
	deployHealthy(t, e, whoami())
	sc, err := e.store.InsertSchedule(context.Background(), schedRec("aaaaaaaaaaaa", "whoami", "rates-sync"))
	if err != nil {
		t.Fatal(err)
	}
	return e, sc
}

// insertRuns stores runs of sc a minute apart from base, the first the oldest.
func insertRuns(t *testing.T, e *env, sc store.ScheduleRecord, base time.Time, statuses ...string) {
	t.Helper()
	for i, status := range statuses {
		id, err := newID("")
		if err != nil {
			t.Fatal(err)
		}
		r := endedRun(sc, id, status, "", nil, 0)
		r.StartedAt, r.FinishedAt = base.Add(time.Duration(i)*time.Minute), new(base.Add(time.Duration(i)*time.Minute+time.Second))
		switch status {
		case store.RunFailed:
			r.ExitCode, r.Error = new(3), "exit 3"
		case store.RunStopped:
			r.ExitCode, r.Error = new(143), "stopped by the owner"
		case store.RunSkipped:
			r.Error, r.OutputKept, r.Stdout, r.Stderr = "the run before was still going", false, "", ""
		case store.RunOK:
			r.ExitCode = new(0)
		}
		if err := e.store.InsertRun(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
}

// The rules the store decides: which run is "the newest real one", and the
// skip streak, read by Service.Attention.
func TestAttentionSchedules(t *testing.T) {
	ctx := context.Background()
	kinds := func(t *testing.T, e *env) []AttentionKind {
		t.Helper()
		att, err := e.svc.Attention(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []AttentionKind
		for _, it := range att.Items {
			out = append(out, it.Kind)
			if strings.Contains(fmt.Sprint(it), schedOutput) {
				t.Errorf("an item holds the run's output: %+v", it)
			}
		}
		return out
	}
	tests := []struct {
		name     string
		statuses []string // oldest first
		paused   bool
		want     []AttentionKind
	}{
		{"never run", nil, false, nil},
		{"failed", []string{store.RunOK, store.RunFailed}, false, []AttentionKind{AttentionScheduleFailed}},
		{"a skipped slot since doesn't hide it", []string{store.RunFailed, store.RunSkipped}, false, []AttentionKind{AttentionScheduleFailed}},
		{"a run the owner stopped since doesn't hide it", []string{store.RunFailed, store.RunStopped}, false, []AttentionKind{AttentionScheduleFailed}},
		{"a later ok run clears it", []string{store.RunFailed, store.RunOK}, false, nil},
		{"two skips are no streak", []string{store.RunOK, store.RunSkipped, store.RunSkipped}, false, nil},
		{"three skips are", []string{store.RunOK, store.RunSkipped, store.RunSkipped, store.RunSkipped}, false,
			[]AttentionKind{AttentionScheduleSkipping}},
		{"skips after a failure: both", []string{store.RunFailed, store.RunSkipped, store.RunSkipped, store.RunSkipped}, false,
			[]AttentionKind{AttentionScheduleSkipping, AttentionScheduleFailed}},
		{"a paused schedule skips no more", []string{store.RunOK, store.RunSkipped, store.RunSkipped, store.RunSkipped}, true, nil},
		{"a paused schedule's failure stays", []string{store.RunFailed}, true, []AttentionKind{AttentionScheduleFailed}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, sc := schedAttentionEnv(t)
			insertRuns(t, e, sc, time.Now().Add(-time.Hour), tc.statuses...)
			if tc.paused {
				if err := e.store.SetSchedulePaused(ctx, sc.ID, new(time.Now())); err != nil {
					t.Fatal(err)
				}
			}
			if got := kinds(t, e); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("kinds = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("a removed schedule has no item", func(t *testing.T) {
		e, sc := schedAttentionEnv(t)
		insertRuns(t, e, sc, time.Now().Add(-time.Hour), store.RunFailed)
		if err := e.store.DeleteSchedule(ctx, sc.ID); err != nil {
			t.Fatal(err)
		}
		if got := kinds(t, e); len(got) != 0 {
			t.Errorf("kinds = %v, want none", got)
		}
	})
}

func TestScheduleProblemKeyAndEvent(t *testing.T) {
	failed := AttentionItem{Kind: AttentionScheduleFailed, AppID: "blog", Service: "web", Schedule: "rates-sync",
		ScheduleID: "aaaaaaaaaaaa", ExitCode: new(3), When: at(5), Detail: "Exited with code 3."}
	skipping := AttentionItem{Kind: AttentionScheduleSkipping, AppID: "blog", Service: "web", Schedule: "rates-sync",
		ScheduleID: "aaaaaaaaaaaa", When: at(9), Detail: "rates-sync has been skipped 3 times: each run takes longer than the time between runs."}

	if got := problemKey(failed); got != "schedule_failed|blog|web|aaaaaaaaaaaa" {
		t.Errorf("problemKey = %q", got)
	}
	// A rename keeps the key: the name is not part of it.
	renamed := failed
	renamed.Schedule = "rates"
	if problemKey(renamed) != problemKey(failed) {
		t.Error("a renamed schedule's problem has another key")
	}
	if isCopyKind(AttentionScheduleFailed) || isCopyKind(AttentionScheduleSkipping) {
		t.Error("a schedule kind is a copy kind")
	}

	tests := []struct {
		it                  AttentionItem
		title, text, detail string
		code                *int
	}{
		{failed, "blog: rates-sync failed (exit 3)", "The latest run of the schedule rates-sync, on blog/web, failed.", "Exited with code 3.", new(3)},
		{AttentionItem{Kind: AttentionScheduleFailed, AppID: "blog", Service: "web", Schedule: "rates-sync", ScheduleID: "aaaaaaaaaaaa",
			Detail: "Stopped at the 5 min limit."},
			"blog: rates-sync failed", "The latest run of the schedule rates-sync, on blog/web, failed.", "Stopped at the 5 min limit.", nil},
		{skipping, "blog: rates-sync has been skipped 3 times", skipping.Detail, "", nil},
	}
	for _, tc := range tests {
		ev := problemEvent(tc.it, "https://caravana.example.com", attNow)
		if ev.Type != notify.EventProblemOpened || ev.Kind != string(tc.it.Kind) || ev.Title != tc.title || ev.Text != tc.text ||
			ev.Detail != tc.detail || ev.Schedule != "rates-sync" || ev.URL != "https://caravana.example.com/schedules#schedule-aaaaaaaaaaaa" ||
			(ev.ExitCode == nil) != (tc.code == nil) || (ev.ExitCode != nil && *ev.ExitCode != *tc.code) {
			t.Errorf("problemEvent(%s) = %+v", tc.it.Kind, ev)
		}
	}

	resolved := resolvedEvent(sentRow(failed, attNow), "https://caravana.example.com", attNow)
	if resolved.URL != "https://caravana.example.com/schedules#schedule-aaaaaaaaaaaa" || resolved.Kind != "schedule_failed" {
		t.Errorf("resolvedEvent = %+v", resolved)
	}
}

func TestDiffProblemsSchedules(t *testing.T) {
	now := time.Date(2026, 10, 23, 3, 0, 0, 0, time.UTC)
	const delay = 5 * time.Minute
	exists := map[string]bool{"blog": true}
	rates := AttentionItem{Kind: AttentionScheduleFailed, AppID: "blog", Service: "web", Schedule: "rates-sync", ScheduleID: "aaaaaaaaaaaa"}
	digest := AttentionItem{Kind: AttentionScheduleFailed, AppID: "blog", Service: "web", Schedule: "digest", ScheduleID: "bbbbbbbbbbbb"}
	skipping := AttentionItem{Kind: AttentionScheduleSkipping, AppID: "blog", Service: "web", Schedule: "rates-sync", ScheduleID: "aaaaaaaaaaaa"}

	// Two schedules of one service failing: one problem each.
	ch := diffProblems(nil, []AttentionItem{rates, digest, skipping}, true, exists, now, delay)
	if len(ch.Opened) != 3 {
		t.Errorf("opened %d, want one per failing schedule and the warning: %+v", len(ch.Opened), ch.Opened)
	}
	// Failing again every minute: seen, never opened again.
	prev := []store.SentProblem{sentRow(rates, now.Add(-time.Minute)), sentRow(digest, now.Add(-time.Minute))}
	ch = diffProblems(prev, []AttentionItem{rates, digest}, true, exists, now, delay)
	if len(ch.Opened) != 0 || len(ch.Seen) != 2 {
		t.Errorf("a schedule that keeps failing: %+v", ch)
	}
	// A run succeeded: once it has been gone for the delay, resolved.
	prev = []store.SentProblem{sentRow(rates, now.Add(-delay)), sentRow(digest, now)}
	ch = diffProblems(prev, []AttentionItem{digest}, true, exists, now, delay)
	if len(ch.Resolved) != 1 || ch.Resolved[0].Key != problemKey(rates) || len(ch.Seen) != 1 {
		t.Errorf("after a success: %+v", ch)
	}
	// Nomad unreadable: the schedule kinds come from the store, so they are still judged.
	ch = diffProblems([]store.SentProblem{sentRow(digest, now.Add(-time.Hour))}, []AttentionItem{rates}, false, exists, now, delay)
	if len(ch.Opened) != 1 || len(ch.Resolved) != 1 {
		t.Errorf("with copies unknown: %+v", ch)
	}
	// A removed schedule's problem is resolved after the delay like any other.
	ch = diffProblems([]store.SentProblem{sentRow(rates, now.Add(-delay))}, nil, true, exists, now, delay)
	if len(ch.Resolved) != 1 {
		t.Errorf("a removed schedule: %+v", ch)
	}
}

// The notifier end to end: a failed run is one problem.opened with the
// schedule's name and exit code, a later ok run one problem.resolved, and no
// delivery, rendered for any kind of channel, holds the run's output.
func TestCheckProblemsSchedules(t *testing.T) {
	ctx := context.Background()
	e := newNotifyEnv(t)
	e.svc.publicURL = "https://caravana.example.com"
	e.svc.resolveAfter = 5 * time.Minute
	t0 := time.Now().UTC()
	e.clockAt(t0)
	deployHealthy(t, e.env, whoami())
	sc, err := e.store.InsertSchedule(ctx, schedRec("aaaaaaaaaaaa", "whoami", "rates-sync"))
	if err != nil {
		t.Fatal(err)
	}
	insertRuns(t, e.env, sc, t0.Add(-time.Hour), store.RunOK, store.RunFailed)

	e.check(t)
	e.check(t) // still failing: nothing new
	got := e.deliveriesOf(t, e.problems)
	if len(got) != 1 {
		t.Fatalf("deliveries = %+v, want one", got)
	}
	opened := payloadOf(t, got[0])
	if opened.Type != notify.EventProblemOpened || opened.Kind != "schedule_failed" || opened.Schedule != "rates-sync" ||
		opened.ExitCode == nil || *opened.ExitCode != 3 || opened.Title != "whoami: rates-sync failed (exit 3)" ||
		opened.Detail != "Exited with code 3." || opened.URL != "https://caravana.example.com/schedules#schedule-aaaaaaaaaaaa" {
		t.Errorf("opened = %+v", opened)
	}

	// A run succeeds; the problem goes once it has been gone for the delay.
	insertRuns(t, e.env, sc, t0.Add(-time.Minute), store.RunOK)
	e.check(t)
	e.clockAt(t0.Add(6 * time.Minute))
	e.check(t)
	got = e.deliveriesOf(t, e.problems)
	if len(got) != 2 {
		t.Fatalf("deliveries = %+v, want opened then resolved", got)
	}
	resolved := payloadOf(t, got[1])
	if resolved.Type != notify.EventProblemResolved || resolved.Kind != "schedule_failed" ||
		resolved.URL != "https://caravana.example.com/schedules#schedule-aaaaaaaaaaaa" {
		t.Errorf("resolved = %+v", resolved)
	}

	for _, d := range got {
		if strings.Contains(d.Payload, schedOutput) || strings.Contains(d.Title, schedOutput) {
			t.Errorf("a delivery holds the run's output: %+v", d)
		}
		ev := payloadOf(t, d)
		for _, ch := range []notify.Channel{{Kind: notify.KindWebhook, Address: "https://hooks.example.com/h", Secret: "s"},
			{Kind: notify.KindSlack, Address: "https://hooks.slack.com/x"}, {Kind: notify.KindDiscord, Address: "https://discord.com/x"},
			{Kind: notify.KindNtfy, Address: "https://ntfy.sh/x"}} {
			req, err := notify.Render(ch, ev, d.ID, t0)
			if err != nil {
				t.Fatal(err)
			}
			if dumped := dumpRequest(t, req); strings.Contains(dumped, schedOutput) {
				t.Errorf("the %s message holds the run's output: %s", ch.Kind, dumped)
			}
		}
	}
}

// dumpRequest is a rendered message as text: its URL, headers and body.
func dumpRequest(t *testing.T, req *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s\n%v\n%s", req.URL, req.Header, body)
}
