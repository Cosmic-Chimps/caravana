package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The pieces of TestDiffProblems: a needs-you item and a row of sent_problems.
func item(kind AttentionKind, appID, service string) AttentionItem {
	return AttentionItem{Kind: kind, AppID: appID, Service: service}
}

func failedDeploy(appID, deployment string) AttentionItem {
	return AttentionItem{Kind: AttentionDeployFailed, AppID: appID, Deployment: deployment}
}

func sentRow(it AttentionItem, lastSeen time.Time) store.SentProblem {
	return store.SentProblem{
		Key: problemKey(it), Kind: string(it.Kind), AppID: it.AppID, Service: it.Service,
		OpenedAt: lastSeen.Add(-time.Hour), LastSeenAt: lastSeen, Summary: "title",
	}
}

func TestProblemKey(t *testing.T) {
	tests := []struct {
		it   AttentionItem
		want string
	}{
		{failedDeploy("hollo", "dep_1"), "deploy_failed|hollo||dep_1"},
		{item(AttentionBackupFailed, "kuma", "db"), "backup_failed|kuma|db|"},
		{item(AttentionNoBackupDestination, "", ""), "no_backup_destination|||"},
		// The copies are not part of the identity: a crash loop swaps them.
		{AttentionItem{Kind: AttentionRestarting, AppID: "blog", Service: "web", Copies: []string{"a1", "a2"}}, "restarting|blog|web|"},
	}
	for _, tc := range tests {
		if got := problemKey(tc.it); got != tc.want {
			t.Errorf("problemKey(%+v) = %q, want %q", tc.it, got, tc.want)
		}
	}
}

func TestDiffProblems(t *testing.T) {
	at := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	const delay = 5 * time.Minute
	ago := func(d time.Duration) time.Time { return at.Add(-d) }
	exists := map[string]bool{"blog": true, "hollo": true, "kuma": true}

	web := item(AttentionRestarting, "blog", "web")
	oom := item(AttentionOutOfMemory, "blog", "web")
	backup := item(AttentionBackupFailed, "kuma", "db")
	dep := failedDeploy("hollo", "dep_1")
	noDest := item(AttentionNoBackupDestination, "", "")
	ghost := item(AttentionBackupFailed, "ghost", "db")
	ghostWeb := item(AttentionRestarting, "ghost", "web")

	tests := []struct {
		name        string
		prev        []store.SentProblem
		now         []AttentionItem
		copiesKnown bool
		opened      []string // keys
		resolved    []string
		seen        []string
		dropped     []string
	}{
		{name: "an empty list and an empty table", copiesKnown: true},
		{
			name: "a new item is opened", copiesKnown: true,
			now: []AttentionItem{backup}, opened: []string{problemKey(backup)},
		},
		{
			name: "an item already sent is only seen", copiesKnown: true,
			prev: []store.SentProblem{sentRow(backup, ago(time.Hour))}, now: []AttentionItem{backup},
			seen: []string{problemKey(backup)},
		},
		{
			name: "a restart finds everything in the table: nothing is opened", copiesKnown: true,
			prev: []store.SentProblem{sentRow(backup, ago(time.Hour)), sentRow(dep, ago(time.Hour)), sentRow(noDest, ago(time.Hour))},
			now:  []AttentionItem{noDest, dep, backup},
			seen: []string{problemKey(noDest), problemKey(dep), problemKey(backup)},
		},
		{
			name: "a problem from while Caravana was down is opened, one that ended is resolved", copiesKnown: true,
			prev:     []store.SentProblem{sentRow(dep, ago(time.Hour))},
			now:      []AttentionItem{backup},
			opened:   []string{problemKey(backup)},
			resolved: []string{problemKey(dep)},
		},
		{
			name: "gone for less than the delay: kept, nothing sent", copiesKnown: true,
			prev: []store.SentProblem{sentRow(backup, ago(delay-time.Second))},
		},
		{
			name: "gone for exactly the delay: resolved", copiesKnown: true,
			prev: []store.SentProblem{sentRow(backup, ago(delay))}, resolved: []string{problemKey(backup)},
		},
		{
			name: "gone for longer than the delay: resolved", copiesKnown: true,
			prev: []store.SentProblem{sentRow(backup, ago(time.Hour))}, resolved: []string{problemKey(backup)},
		},
		{
			name: "flapping: back inside the delay is seen, not opened again", copiesKnown: true,
			prev: []store.SentProblem{sentRow(backup, ago(delay-time.Minute))}, now: []AttentionItem{backup},
			seen: []string{problemKey(backup)},
		},
		{
			name: "back after it was resolved is a new problem", copiesKnown: true,
			now: []AttentionItem{backup}, opened: []string{problemKey(backup)},
		},
		{
			name: "Nomad unreadable: copy items are neither opened nor resolved, the rest is judged", copiesKnown: false,
			prev: []store.SentProblem{sentRow(oom, ago(24*time.Hour)), sentRow(dep, ago(24*time.Hour))},
			// The list leaves copies out when it can't read them; a stray one is not opened either.
			now:    []AttentionItem{web, backup},
			opened: []string{problemKey(backup)}, resolved: []string{problemKey(dep)},
		},
		{
			name: "Nomad readable again: a copy problem that stayed is seen, one that went resolves", copiesKnown: true,
			prev: []store.SentProblem{sentRow(oom, ago(24*time.Hour)), sentRow(web, ago(24*time.Hour))},
			now:  []AttentionItem{web},
			seen: []string{problemKey(web)}, resolved: []string{problemKey(oom)},
		},
		{
			name: "an app that is gone: its problems are dropped, not resolved, however old", copiesKnown: true,
			prev:    []store.SentProblem{sentRow(ghost, ago(24*time.Hour)), sentRow(ghostWeb, ago(time.Second))},
			dropped: []string{problemKey(ghost), problemKey(ghostWeb)},
		},
		{
			name: "an item of an app that is gone is not opened", copiesKnown: true,
			now: []AttentionItem{ghost},
		},
		{
			name: "an installation-wide problem has no app to be gone", copiesKnown: true,
			prev: []store.SentProblem{sentRow(noDest, ago(time.Hour))}, resolved: []string{problemKey(noDest)},
		},
		{
			name: "the same item twice is one problem", copiesKnown: true,
			now: []AttentionItem{backup, backup}, opened: []string{problemKey(backup)},
		},
		{
			name: "a new failed deploy is a new problem next to the old one", copiesKnown: true,
			prev:   []store.SentProblem{sentRow(failedDeploy("hollo", "dep_1"), ago(time.Minute))},
			now:    []AttentionItem{failedDeploy("hollo", "dep_1"), failedDeploy("hollo", "dep_2")},
			opened: []string{problemKey(failedDeploy("hollo", "dep_2"))}, seen: []string{problemKey(failedDeploy("hollo", "dep_1"))},
		},
	}
	keysOf := func(items []AttentionItem) []string {
		var out []string
		for _, it := range items {
			out = append(out, problemKey(it))
		}
		return out
	}
	rowKeys := func(rows []store.SentProblem) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Key)
		}
		return out
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := diffProblems(tc.prev, tc.now, judged{Copies: tc.copiesKnown, Disks: true}, exists, at, delay)
			for _, c := range []struct {
				what      string
				got, want []string
			}{
				{"opened", keysOf(got.Opened), tc.opened},
				{"resolved", rowKeys(got.Resolved), tc.resolved},
				{"seen", got.Seen, tc.seen},
				{"dropped", got.Dropped, tc.dropped},
			} {
				if !slices.Equal(c.got, c.want) {
					t.Errorf("%s = %v, want %v", c.what, c.got, c.want)
				}
			}
		})
	}
}

// parkSender stops a started service's sender from sending anything, so the
// tests of the notifier can look at what it queued: the sender starts a worker
// only when it can take a token from the queue's slots, and here they are all
// taken.
func parkSender(svc *Service) {
	for range maxParallelSends {
		svc.queue.slots <- struct{}{}
	}
}

// notifyEnv is an env with a channel for each of the filters, and a clock to
// move.
type notifyEnv struct {
	*env
	problems   store.NotificationChannel // wants problems
	onlyDeploy store.NotificationChannel // wants deploy_failed only: never gets a problem
	paused     store.NotificationChannel // wants problems, paused
}

func newNotifyEnv(t *testing.T) *notifyEnv {
	t.Helper()
	ctx := context.Background()
	e := &notifyEnv{env: newEnv(t)}
	parkSender(e.svc)
	add := func(name string, events []string, paused bool) store.NotificationChannel {
		in := channelInput(notify.KindNtfy, "https://ntfy.example.com/"+name)
		in.Name, in.Events, in.Paused = name, events, paused
		got, err := e.svc.CreateNotificationChannel(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return got.NotificationChannel.NotificationChannel
	}
	e.problems = add("problems", []string{EventProblems}, false)
	e.onlyDeploy = add("only-deploy", []string{EventDeployFailed}, false)
	e.paused = add("paused", []string{EventProblems}, true)
	return e
}

func (e *notifyEnv) clockAt(t time.Time) { e.svc.clock.(*fakeClock).Set(t) }

// deliveriesOf returns a channel's deliveries, oldest first.
func (e *notifyEnv) deliveriesOf(t *testing.T, ch store.NotificationChannel) []store.Delivery {
	t.Helper()
	got, err := e.store.ListDeliveries(context.Background(), ch.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(got)
	return got
}

func (e *notifyEnv) check(t *testing.T) {
	t.Helper()
	if err := e.svc.checkProblems(context.Background()); err != nil {
		t.Fatalf("checkProblems: %v", err)
	}
}

func (e *notifyEnv) sentKeys(t *testing.T) []string {
	t.Helper()
	rows, err := e.store.ListSentProblems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	slices.Sort(keys)
	return keys
}

func payloadOf(t *testing.T, d store.Delivery) notify.Event {
	t.Helper()
	var ev notify.Event
	if err := json.Unmarshal([]byte(d.Payload), &ev); err != nil {
		t.Fatalf("payload %q: %v", d.Payload, err)
	}
	return ev
}

// The story of a problem (US1): sent once when it opens, nothing while it lasts
// or across a restart, once more when it has been gone for the delay.
func TestCheckProblems(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)

	e := newNotifyEnv(t)
	e.clockAt(t0)
	e.svc.publicURL = "https://caravana.example.com"
	e.svc.resolveAfter = 5 * time.Minute

	first, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, first.Deployment.ID)
	e.check(t)
	if got := e.deliveriesOf(t, e.problems); len(got) != 0 {
		t.Fatalf("a healthy app sent %v", got)
	}

	// A deploy that Nomad reverts.
	desc := "Failed due to unhealthy allocations - rolling back to job version 0"
	e.nomad.Script("whoami", 1, api.Deployment{Status: api.DeploymentStatusFailed, StatusDescription: desc})
	second, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, second.Deployment.ID)

	e.clockAt(t0.Add(time.Minute))
	e.check(t)
	got := e.deliveriesOf(t, e.problems)
	if len(got) != 1 || got[0].Status != store.DeliveryPending || got[0].EventType != notify.EventProblemOpened {
		t.Fatalf("after the failed deploy: %+v, want one pending problem.opened", got)
	}
	ev := payloadOf(t, got[0])
	if ev.Kind != "deploy_failed" || ev.AppID != "whoami" || ev.Detail != desc ||
		ev.URL != "https://caravana.example.com/apps/whoami/deployments#deployment-"+second.Deployment.ID {
		t.Errorf("the message = %+v", ev)
	}
	if !got[0].NextAt.Equal(t0.Add(time.Minute)) || !got[0].CreatedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("queued at %v, due %v; want the clock's %v", got[0].CreatedAt, got[0].NextAt, t0.Add(time.Minute))
	}
	// A paused channel got nothing. The channel that wants only failed deploys
	// got the deploy's own message (feature 018 US2) and no problem.
	if n := len(e.deliveriesOf(t, e.paused)); n != 0 {
		t.Errorf("a paused channel got %d messages", n)
	}
	for _, d := range e.deliveriesOf(t, e.onlyDeploy) {
		if d.EventType != notify.EventDeployFailed {
			t.Errorf("a channel that doesn't want problems got a %s", d.EventType)
		}
	}
	if n := len(e.deliveriesOf(t, e.onlyDeploy)); n != 1 {
		t.Errorf("the failed-deploys channel got %d messages, want the deploy's one", n)
	}
	wantKey := "deploy_failed|whoami||" + second.Deployment.ID
	if keys := e.sentKeys(t); !slices.Equal(keys, []string{wantKey}) {
		t.Fatalf("sent problems = %v, want %v", keys, wantKey)
	}

	// An hour later it is still there: nothing more is sent.
	e.clockAt(t0.Add(time.Hour))
	e.check(t)
	e.check(t)
	if got := e.deliveriesOf(t, e.problems); len(got) != 1 {
		t.Fatalf("while the problem lasts: %d messages, want 1", len(got))
	}
	rows, _ := e.store.ListSentProblems(ctx)
	if len(rows) != 1 || !rows[0].LastSeenAt.Equal(t0.Add(time.Hour)) || !rows[0].OpenedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("row = %+v, want last seen at the check and opened at the first", rows)
	}

	// Caravana restarts: another Service on the same store sends nothing again.
	again := New(e.store, e.svc.deployer, e.secrets, discardLogger())
	again.clock = e.svc.clock
	if err := again.checkProblems(ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.deliveriesOf(t, e.problems); len(got) != 1 {
		t.Fatalf("after a restart: %d messages, want 1", len(got))
	}

	// A healthy deploy clears it. Gone for a minute: nothing yet.
	third, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, third.Deployment.ID)
	e.clockAt(t0.Add(time.Hour + time.Minute))
	e.check(t)
	if got := e.deliveriesOf(t, e.problems); len(got) != 1 || len(e.sentKeys(t)) != 1 {
		t.Fatalf("gone for a minute: %d messages and %d rows, want 1 and 1", len(got), len(e.sentKeys(t)))
	}

	// Gone for the delay: one "resolved", and the row is deleted.
	e.clockAt(t0.Add(time.Hour + 5*time.Minute))
	e.check(t)
	e.check(t)
	got = e.deliveriesOf(t, e.problems)
	if len(got) != 2 || got[1].EventType != notify.EventProblemResolved {
		t.Fatalf("after the delay: %+v, want opened then resolved", got)
	}
	if ev := payloadOf(t, got[1]); ev.Title != "Resolved: "+payloadOf(t, got[0]).Title || ev.AppID != "whoami" {
		t.Errorf("resolved message = %+v", ev)
	}
	if keys := e.sentKeys(t); len(keys) != 0 {
		t.Errorf("sent problems after resolving = %v, want none", keys)
	}
}

// A problem that flaps inside the delay sends one message, not three.
func TestCheckProblemsFlapping(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	e := newNotifyEnv(t)
	e.clockAt(t0)
	setDestination(t, e.env)
	deployHealthy(t, e.env, db())

	run := func(id string, status string, started time.Time) {
		t.Helper()
		if err := e.store.SaveBackup(ctx, store.BackupRecord{AppID: "whoami", ID: id, Service: "web",
			Status: status, Message: "boom", StartedAt: started, FinishedAt: &started}); err != nil {
			t.Fatal(err)
		}
	}
	run("web-1", store.BackupFailed, t0)
	e.check(t) // opened

	run("web-2", store.BackupSucceeded, t0.Add(time.Minute))
	e.clockAt(t0.Add(2 * time.Minute))
	e.check(t) // gone for 2 minutes: kept

	run("web-3", store.BackupFailed, t0.Add(3*time.Minute))
	e.clockAt(t0.Add(4 * time.Minute))
	e.check(t) // back, inside the delay

	e.clockAt(t0.Add(time.Hour))
	e.check(t)
	var types []string
	for _, d := range e.deliveriesOf(t, e.problems) {
		types = append(types, d.EventType)
	}
	if !slices.Equal(types, []string{notify.EventProblemOpened}) {
		t.Errorf("a flapping problem sent %v, want one problem.opened", types)
	}
}

// While Nomad can't be read, copy problems are left alone: nothing is resolved.
func TestCheckProblemsNomadDown(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	e := newNotifyEnv(t)
	deployHealthy(t, e.env, whoami())
	e.clockAt(t0)
	row := store.SentProblem{Key: "out_of_memory|whoami|web|", Kind: "out_of_memory", AppID: "whoami", Service: "web",
		OpenedAt: t0.Add(-48 * time.Hour), LastSeenAt: t0.Add(-24 * time.Hour), Summary: "whoami/web: ran out of memory"}
	if err := e.store.UpsertSentProblem(ctx, row); err != nil {
		t.Fatal(err)
	}

	e.nomad.Down()
	e.check(t)
	if keys := e.sentKeys(t); !slices.Equal(keys, []string{row.Key}) || len(e.deliveriesOf(t, e.problems)) != 0 {
		t.Fatalf("with Nomad down: rows %v, %d messages; want the row kept and nothing sent", keys, len(e.deliveriesOf(t, e.problems)))
	}

	// Back up, the copy is no longer in trouble: now it is resolved.
	e.nomad.Up()
	e.check(t)
	if got := e.deliveriesOf(t, e.problems); len(got) != 1 || got[0].EventType != notify.EventProblemResolved {
		t.Fatalf("with Nomad back: %+v, want one problem.resolved", got)
	}
}

// A row that outlived its app is dropped without a word. (DeleteApp deletes the
// rows itself; this is the check running while that happens.)
func TestCheckProblemsDeletedApp(t *testing.T) {
	ctx := context.Background()
	e := newNotifyEnv(t)
	t0 := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	e.clockAt(t0)
	deployHealthy(t, e.env, whoami())
	stale := store.SentProblem{Key: "backup_failed|ghost|db|", Kind: "backup_failed", AppID: "ghost", Service: "db",
		OpenedAt: t0.Add(-48 * time.Hour), LastSeenAt: t0.Add(-24 * time.Hour), Summary: "ghost: the backup of db failed"}
	if err := e.store.UpsertSentProblem(ctx, stale); err != nil {
		t.Fatal(err)
	}
	e.check(t)
	if keys := e.sentKeys(t); len(keys) != 0 {
		t.Errorf("rows = %v, want the ghost's dropped", keys)
	}
	if got := e.deliveriesOf(t, e.problems); len(got) != 0 {
		t.Errorf("%d messages for an app that is gone", len(got))
	}
}

// More than 10 problems found by one check are one summary per channel (FR-011);
// every problem is still recorded, so none is sent again later.
func TestCheckProblemsSummary(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)

	// failApps stores n apps whose only deployment failed.
	failApps := func(e *notifyEnv, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			a := whoami()
			a.ID = fmt.Sprintf("app%02d", i)
			if _, err := e.store.SaveApp(ctx, a, nil); err != nil {
				t.Fatal(err)
			}
			dep := app.Deployment{ID: fmt.Sprintf("dep_%02d", i), Trigger: app.TriggerManual, Digests: map[string]string{}}
			if _, err := e.store.CreateDeployment(ctx, a.ID, dep, &a, nil, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := e.store.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{Status: store.StatusFailed, Error: "boom"}); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("ten are sent one by one", func(t *testing.T) {
		e := newNotifyEnv(t)
		e.clockAt(t0)
		failApps(e, 10)
		e.check(t)
		if got := e.deliveriesOf(t, e.problems); len(got) != 10 {
			t.Errorf("%d messages, want 10", len(got))
		}
	})

	t.Run("eleven are one summary", func(t *testing.T) {
		e := newNotifyEnv(t)
		e.clockAt(t0)
		e.svc.publicURL = "https://caravana.example.com"
		failApps(e, 11)
		e.check(t)
		got := e.deliveriesOf(t, e.problems)
		if len(got) != 1 {
			t.Fatalf("%d messages, want one summary", len(got))
		}
		ev := payloadOf(t, got[0])
		if ev.Type != notify.EventProblemOpened || ev.Title != "11 new problems" || ev.AppID != "" ||
			strings.Count(ev.Text, "- ") != 11 || ev.URL != "https://caravana.example.com/" {
			t.Errorf("summary = %+v", ev)
		}
		if len(e.sentKeys(t)) != 11 {
			t.Errorf("recorded %d problems, want all 11", len(e.sentKeys(t)))
		}
		e.check(t)
		if got := e.deliveriesOf(t, e.problems); len(got) != 1 {
			t.Errorf("the next check left %d messages in all, want still 1", len(got))
		}
	})

	t.Run("a long summary lists 20 and counts the rest", func(t *testing.T) {
		e := newNotifyEnv(t)
		e.clockAt(t0)
		failApps(e, 25)
		e.check(t)
		ev := payloadOf(t, e.deliveriesOf(t, e.problems)[0])
		if ev.Title != "25 new problems" || !strings.HasSuffix(ev.Text, "…and 5 more.") || strings.Count(ev.Text, "- ") != 20 {
			t.Errorf("summary text = %q", ev.Text)
		}
	})

	t.Run("a flood of resolved is one summary too", func(t *testing.T) {
		e := newNotifyEnv(t)
		e.clockAt(t0)
		failApps(e, 11)
		e.check(t)
		for i := 0; i < 11; i++ { // each app is fixed: its latest deployment is healthy
			a := whoami()
			a.ID = fmt.Sprintf("app%02d", i)
			dep := app.Deployment{ID: fmt.Sprintf("fix_%02d", i), Trigger: app.TriggerManual, Digests: map[string]string{}}
			if _, err := e.store.CreateDeployment(ctx, a.ID, dep, &a, nil, ""); err != nil {
				t.Fatal(err)
			}
			for _, status := range []store.Status{store.StatusRunning, store.StatusHealthy} {
				if _, err := e.store.UpdateDeployment(ctx, dep.ID, store.DeploymentUpdate{Status: status}); err != nil {
					t.Fatal(err)
				}
			}
		}
		e.clockAt(t0.Add(10 * time.Minute))
		e.check(t)
		got := e.deliveriesOf(t, e.problems)
		if len(got) != 2 || got[1].EventType != notify.EventProblemResolved || payloadOf(t, got[1]).Title != "11 problems resolved" {
			t.Errorf("deliveries = %+v, want the opened summary then a resolved summary", got)
		}
	})
}

// The loop checks on its timer and stops with the context, so Wait returns.
func TestNotifierLoop(t *testing.T) {
	ctx := context.Background()
	e := newNotifyEnv(t)

	// A failed backup is the problem; a service with a short gap finds it by itself.
	setDestination(t, e.env)
	deployHealthy(t, e.env, db())
	now := time.Now()
	if err := e.store.SaveBackup(ctx, store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web",
		Status: store.BackupFailed, Message: "boom", StartedAt: now, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}

	loop := New(e.store, e.svc.deployer, e.secrets, discardLogger())
	loop.notifyGap = 5 * time.Millisecond
	parkSender(loop)
	runCtx, cancel := context.WithCancel(ctx)
	if err := loop.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the notifier to queue the problem", func() bool { return len(e.deliveriesOf(t, e.problems)) == 1 })
	cancel()
	done := make(chan struct{})
	go func() { loop.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the context ended")
	}
	if got := e.deliveriesOf(t, e.problems); len(got) != 1 {
		t.Errorf("%d messages after several checks, want 1", len(got))
	}
}
