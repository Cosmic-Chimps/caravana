package service

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The disk kind (feature 025, ruling 8): derived on every read from the stored
// host rows, open from 85 %, held down to 83 %, level failed from 95 %, and not
// judged while the newest host sample is older than 5 minutes.

// diskSamples makes a filesystem's samples a minute apart, oldest first, ending
// at end, one for each percent (of 1,000 MB, so 860 used and 140 free is 86 %).
func diskSamples(mount, holds string, end time.Time, percents ...float64) []store.DiskSample {
	var out []store.DiskSample
	for i, p := range percents {
		ts := end.Add(-time.Duration(len(percents)-1-i) * time.Minute)
		used := int64(p * 10)
		out = append(out, store.DiskSample{Node: "n1", TS: ts, Mount: mount, Holds: holds, UsedMB: used, FreeMB: 1000 - used, TotalMB: 1000})
	}
	return out
}

func TestDiskItemsOf(t *testing.T) {
	end := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	last := func(s []store.DiskSample) []store.DiskSample { return s[len(s)-1:] }
	at := func(minutesBeforeEnd int) time.Time { return end.Add(-time.Duration(minutesBeforeEnd) * time.Minute) }

	tests := []struct {
		name    string
		history []store.DiskSample
		want    []metrics.DiskLevel // the levels of the items, by mount
		when    time.Time
	}{
		{"84 opens nothing", diskSamples("/", "nomad", end, 80, 84), nil, time.Time{}},
		{"85 opens", diskSamples("/", "nomad", end, 84, 85), []metrics.DiskLevel{metrics.DiskWarning}, at(0)},
		{"86 then 96 is one failed item that began at the first", diskSamples("/", "nomad", end, 80, 86, 90, 96), []metrics.DiskLevel{metrics.DiskFailed}, at(2)},
		{"95 is failed", diskSamples("/", "nomad", end, 95), []metrics.DiskLevel{metrics.DiskFailed}, at(0)},
		{"96 back to 94 is a warning again", diskSamples("/", "nomad", end, 96, 94), []metrics.DiskLevel{metrics.DiskWarning}, at(1)},
		{"opened at 86, 84 stays open", diskSamples("/", "nomad", end, 86, 84), []metrics.DiskLevel{metrics.DiskWarning}, at(1)},
		{"83 stays open", diskSamples("/", "nomad", end, 86, 83), []metrics.DiskLevel{metrics.DiskWarning}, at(1)},
		{"82 resolves", diskSamples("/", "nomad", end, 86, 82), nil, time.Time{}},
		{"83 without an earlier 85 never opened", diskSamples("/", "nomad", end, 83, 84, 83), nil, time.Time{}},
		{"a new run after a resolve begins at its own first sample", diskSamples("/", "nomad", end, 90, 80, 86, 84), []metrics.DiskLevel{metrics.DiskWarning}, at(1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := diskItemsOf(last(tc.history), tc.history)
			var levels []metrics.DiskLevel
			for _, it := range got {
				levels = append(levels, it.Level)
				if it.Kind != AttentionDiskNearlyFull || it.AppID != "" || it.Mount != "/" || it.Detail == "" {
					t.Errorf("item = %+v", it)
				}
				if !it.When.Equal(tc.when) {
					t.Errorf("when = %v, want %v", it.When, tc.when)
				}
			}
			if !slices.Equal(levels, tc.want) {
				t.Errorf("levels = %v, want %v", levels, tc.want)
			}
		})
	}

	t.Run("the facts of an item", func(t *testing.T) {
		d := []store.DiskSample{{Node: "n1", TS: end, Mount: "/", Holds: "nomad,docker", UsedMB: 35000, FreeMB: 5600, TotalMB: 40600}}
		got := diskItemsOf(d, d)
		if len(got) != 1 {
			t.Fatalf("items = %+v", got)
		}
		it := got[0]
		if it.UsedPercent < 86.2 || it.UsedPercent > 86.3 || it.FreeBytes != 5600<<20 || !slices.Equal(it.Holds, []string{"nomad", "docker"}) ||
			it.Detail != "The disk at / is 86% full: 5.5 GB left." {
			t.Errorf("item = %+v", it)
		}
	})

	t.Run("one item for each watched filesystem, a mount no longer watched gives none", func(t *testing.T) {
		newest := append(diskSamples("/", "nomad", end, 90), diskSamples("/var/lib/docker", "docker", end, 97)...)
		history := slices.Concat(diskSamples("/", "nomad", end, 90, 90), diskSamples("/var/lib/docker", "docker", end, 97, 97), diskSamples("/old", "nomad", end.Add(-time.Minute), 99))
		got := diskItemsOf(newest, history)
		by := map[string]metrics.DiskLevel{}
		for _, it := range got {
			by[it.Mount] = it.Level
		}
		if len(got) != 2 || by["/"] != metrics.DiskWarning || by["/var/lib/docker"] != metrics.DiskFailed {
			t.Errorf("items = %+v", got)
		}
	})

	t.Run("without history the newest sample alone decides", func(t *testing.T) {
		if got := diskItemsOf(diskSamples("/", "nomad", end, 86), nil); len(got) != 1 {
			t.Errorf("items = %+v", got)
		}
		if got := diskItemsOf(diskSamples("/", "nomad", end, 84), nil); len(got) != 0 {
			t.Errorf("items = %+v", got)
		}
	})
}

// writeDisk stores a host sample and a filesystem's reading one minute before
// the clock.
func (e *notifyEnv) writeDisk(t *testing.T, mount string, pct float64) {
	t.Helper()
	ts := e.svc.clock.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	used := int64(pct * 10)
	err := e.store.WriteMetricsTick(context.Background(), store.MetricsTick{Minute: store.MetricsRows{
		Hosts: []store.HostSample{hostRow("n1", ts, 1000, 900, 4096)},
		Disks: []store.DiskSample{{Node: "n1", TS: ts, Mount: mount, Holds: "nomad,docker", UsedMB: used * 40, FreeMB: (1000 - used) * 40, TotalMB: 40000}},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *notifyEnv) step(t *testing.T, d time.Duration, mount string, pct float64) {
	t.Helper()
	e.svc.clock.(*fakeClock).Advance(d)
	e.writeDisk(t, mount, pct)
}

func problemTypes(ds []store.Delivery) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.EventType)
	}
	return out
}

// The story of a disk: 85 sends one "opened", 86 sends nothing and 96 sends one
// "worsened" (a repeat of 96 none), 84 keeps it open, and a reading under 83
// resolves it after the delay.
func TestCheckProblemsDisk(t *testing.T) {
	t0 := time.Date(2026, 10, 25, 3, 0, 30, 0, time.UTC)
	e := newNotifyEnv(t)
	e.clockAt(t0)
	e.svc.publicURL = "https://caravana.example.com"
	e.svc.resolveAfter = 5 * time.Minute
	const key = "disk_nearly_full|||/"

	e.writeDisk(t, "/", 84)
	e.check(t)
	if got := e.deliveriesOf(t, e.problems); len(got) != 0 || len(e.sentKeys(t)) != 0 {
		t.Fatalf("84%% sent %v", problemTypes(got))
	}

	e.step(t, time.Minute, "/", 85)
	e.check(t)
	got := e.deliveriesOf(t, e.problems)
	if len(got) != 1 || got[0].EventType != notify.EventProblemOpened {
		t.Fatalf("85%%: %v, want one problem.opened", problemTypes(got))
	}
	ev := payloadOf(t, got[0])
	if ev.Kind != "disk_nearly_full" || ev.Mount != "/" || ev.AppID != "" || ev.Level != "warning" || ev.UsedPercent != 85 ||
		ev.FreeBytes == nil || *ev.FreeBytes != 6000<<20 || !slices.Equal(ev.Holds, []string{"nomad", "docker"}) ||
		ev.URL != "https://caravana.example.com/monitoring#host" || ev.Title != "Disk nearly full: / is 85% full" ||
		ev.Text != "The disk at / is 85% full: 5.9 GB left." || ev.Detail != "" {
		t.Errorf("message = %+v", ev)
	}
	if keys := e.sentKeys(t); !slices.Equal(keys, []string{key}) {
		t.Fatalf("sent problems = %v, want %v", keys, key)
	}
	// Nothing for the channel that doesn't want problems, and none for the paused one.
	if n := len(e.deliveriesOf(t, e.onlyDeploy)) + len(e.deliveriesOf(t, e.paused)); n != 0 {
		t.Errorf("other channels got %d messages", n)
	}

	for _, pct := range []float64{86, 96, 96} {
		e.step(t, time.Minute, "/", pct)
		e.check(t)
	}
	if got := e.deliveriesOf(t, e.problems); len(got) != 2 || got[1].EventType != notify.EventProblemWorsened {
		t.Fatalf("86 then 96: %v, want no second opened, and one worsened", problemTypes(got))
	}

	e.step(t, time.Minute, "/", 84) // under 85 but not under 83: still open
	e.check(t)
	e.step(t, 10*time.Minute, "/", 84)
	e.check(t)
	if got := e.deliveriesOf(t, e.problems); len(got) != 2 || len(e.sentKeys(t)) != 1 {
		t.Fatalf("84%%: %v, want the item open and nothing sent", problemTypes(got))
	}
	att, err := e.svc.Attention(context.Background())
	if err != nil || att.DisksUnknown || len(att.Items) != 1 || att.Items[0].Kind != AttentionDiskNearlyFull || att.Items[0].Level != metrics.DiskWarning {
		t.Fatalf("Attention at 84%% = %+v, %v", att, err)
	}

	e.step(t, time.Minute, "/", 82) // resolved on the list; the message waits for the delay
	e.check(t)
	e.step(t, 3*time.Minute, "/", 82)
	e.check(t)
	if got := e.deliveriesOf(t, e.problems); len(got) != 2 {
		t.Fatalf("82%% for 4 minutes since it was last on the list: %v, want nothing yet", problemTypes(got))
	}
	e.step(t, 2*time.Minute, "/", 82)
	e.check(t)
	e.check(t)
	got = e.deliveriesOf(t, e.problems)
	if len(got) != 3 || got[2].EventType != notify.EventProblemResolved {
		t.Fatalf("82%% for the delay: %v, want opened, worsened, resolved", problemTypes(got))
	}
	if ev := payloadOf(t, got[2]); ev.Title != "Resolved: Disk nearly full: / is 85% full" || ev.URL != "https://caravana.example.com/monitoring#host" {
		t.Errorf("resolved = %+v", ev)
	}
	if keys := e.sentKeys(t); len(keys) != 0 {
		t.Errorf("sent problems after resolving = %v", keys)
	}
}

// A host that stops being sampled changes nothing: no "opened" for a reading
// that is old, no "resolved" for a disk nobody is looking at.
func TestCheckProblemsDiskStale(t *testing.T) {
	t0 := time.Date(2026, 10, 25, 3, 0, 30, 0, time.UTC)
	e := newNotifyEnv(t)
	e.clockAt(t0)
	e.svc.resolveAfter = 5 * time.Minute

	// A stale 90 % opens nothing: the only sample is 20 minutes old.
	e.writeDisk(t, "/", 90)
	e.svc.clock.(*fakeClock).Advance(20 * time.Minute)
	e.check(t)
	att, err := e.svc.Attention(context.Background())
	if err != nil || !att.DisksUnknown || len(att.Items) != 0 {
		t.Fatalf("Attention with an old sample = %+v, %v; want the disks unknown and no item", att, err)
	}
	if got := e.deliveriesOf(t, e.problems); len(got) != 0 {
		t.Fatalf("a stale 90%% sent %v", problemTypes(got))
	}

	// A fresh one opens it.
	e.step(t, time.Minute, "/", 90)
	e.check(t)
	if got := e.deliveriesOf(t, e.problems); len(got) != 1 {
		t.Fatalf("90%%: %v, want one opened", problemTypes(got))
	}

	// The sampler stops for an hour: not resolved, not opened again, the row stays.
	for range 12 {
		e.svc.clock.(*fakeClock).Advance(5 * time.Minute)
		e.check(t)
	}
	if got := e.deliveriesOf(t, e.problems); len(got) != 1 || len(e.sentKeys(t)) != 1 {
		t.Fatalf("an outage: %v and %d rows, want 1 and 1", problemTypes(got), len(e.sentKeys(t)))
	}

	// It comes back low: the item was last seen long before the outage, so the
	// first check that can judge it again resolves it, once.
	e.step(t, time.Minute, "/", 70)
	e.check(t)
	e.check(t)
	if got := e.deliveriesOf(t, e.problems); len(got) != 2 || got[1].EventType != notify.EventProblemResolved {
		t.Fatalf("after the outage: %v, want opened then resolved", problemTypes(got))
	}
}

func TestProblemKeyAndEventOfADisk(t *testing.T) {
	it := AttentionItem{Kind: AttentionDiskNearlyFull, Mount: "/var/lib/docker", Holds: []string{"docker"}, UsedPercent: 96.2,
		FreeBytes: 1500 << 20, Level: metrics.DiskFailed, Detail: "The disk at /var/lib/docker is 96% full: 1.5 GB left."}
	if got := problemKey(it); got != "disk_nearly_full|||/var/lib/docker" {
		t.Errorf("problemKey = %q", got)
	}
	if got := problemSubject(it); got != "/var/lib/docker" {
		t.Errorf("problemSubject = %q", got)
	}
	ev := problemEvent(it, "https://c.example.com", time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC))
	if ev.Title != "Disk nearly full: /var/lib/docker is 96% full" || ev.Text != it.Detail || ev.Level != "failed" ||
		ev.URL != "https://c.example.com/monitoring#host" || ev.Type != notify.EventProblemOpened || *ev.FreeBytes != 1500<<20 {
		t.Errorf("event = %+v", ev)
	}
	// The event's text, the item's detail (the API's) and the console's row are one sentence.
	if want := notify.DiskSentence("/var/lib/docker", 96.2, 1500<<20); ev.Text != want || it.Detail != want {
		t.Errorf("text %q, detail %q, want %q", ev.Text, it.Detail, want)
	}
	// Without the console's address there is no link.
	if got := problemEvent(it, "", time.Time{}); got.URL != "" {
		t.Errorf("url without a public address = %q", got.URL)
	}
	// A disk isn't judged while the host isn't sampled, whatever Nomad says.
	if !notJudged(AttentionDiskNearlyFull, judged{Copies: true}) || notJudged(AttentionDiskNearlyFull, judged{Disks: true}) {
		t.Error("notJudged for the disk kind follows Disks only")
	}
	if notJudged(AttentionRestarting, judged{Copies: true}) || !notJudged(AttentionRestarting, judged{Disks: true}) {
		t.Error("notJudged for a copy kind follows Copies only")
	}
}

// diffProblems leaves a disk problem alone while the disks weren't judged,
// as it does a copy problem while Nomad wasn't read.
func TestDiffProblemsDisks(t *testing.T) {
	at := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	const delay = 5 * time.Minute
	disk := AttentionItem{Kind: AttentionDiskNearlyFull, Mount: "/"}
	oom := item(AttentionOutOfMemory, "blog", "web")
	exists := map[string]bool{"blog": true}
	old := []store.SentProblem{sentRow(disk, at.Add(-time.Hour)), sentRow(oom, at.Add(-time.Hour))}
	rowKeys := func(rows []store.SentProblem) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.Key)
		}
		return out
	}

	// Nothing judged: neither is resolved, and an item of either kind isn't opened.
	got := diffProblems(old, nil, judged{}, exists, at, delay)
	if len(got.Resolved) != 0 || len(got.Opened) != 0 {
		t.Errorf("nothing judged: %+v", got)
	}
	got = diffProblems(nil, []AttentionItem{disk, oom}, judged{}, exists, at, delay)
	if len(got.Opened) != 0 {
		t.Errorf("opened %+v while nothing was judged", got.Opened)
	}
	// Only the disks judged: the disk is resolved, the copy kind is kept.
	got = diffProblems(old, nil, judged{Disks: true}, exists, at, delay)
	if keys := rowKeys(got.Resolved); !slices.Equal(keys, []string{problemKey(disk)}) {
		t.Errorf("only disks judged: resolved %v", keys)
	}
	// Only Nomad read: the copy kind is resolved, the disk is kept.
	got = diffProblems(old, nil, judged{Copies: true}, exists, at, delay)
	if keys := rowKeys(got.Resolved); !slices.Equal(keys, []string{problemKey(oom)}) {
		t.Errorf("only copies judged: resolved %v", keys)
	}
}

// A rise from warning to failed sends one problem.worsened with the "now"
// sentence; a fall sends nothing, a repeat or a further rise sends nothing more,
// and the resolve below 83 % sends the usual one once.
func TestCheckProblemsDiskWorsened(t *testing.T) {
	t0 := time.Date(2026, 10, 25, 3, 0, 30, 0, time.UTC)
	e := newNotifyEnv(t)
	e.clockAt(t0)
	e.svc.publicURL = "https://caravana.example.com"
	e.svc.resolveAfter = 5 * time.Minute
	types := func() []string { return problemTypes(e.deliveriesOf(t, e.problems)) }

	e.writeDisk(t, "/", 85)
	e.check(t)
	if got := types(); !slices.Equal(got, []string{notify.EventProblemOpened}) {
		t.Fatalf("85: %v, want one opened", got)
	}
	if rows, err := e.store.ListSentProblems(context.Background()); err != nil || len(rows) != 1 || rows[0].Level != "warning" {
		t.Fatalf("the level sent at 85 = %+v, %v; want warning", rows, err)
	}

	e.step(t, time.Minute, "/", 96)
	e.check(t)
	got := e.deliveriesOf(t, e.problems)
	if len(got) != 2 || got[1].EventType != notify.EventProblemWorsened {
		t.Fatalf("85 then 96: %v, want opened then one worsened", problemTypes(got))
	}
	ev := payloadOf(t, got[1])
	if ev.Type != notify.EventProblemWorsened || ev.Kind != "disk_nearly_full" || ev.Level != "failed" || ev.Mount != "/" || ev.UsedPercent != 96 ||
		ev.Text != "The disk at / is now 96% full: 1.6 GB left." || ev.Title != "Disk nearly full: / is now 96% full" ||
		ev.URL != "https://caravana.example.com/monitoring#host" || ev.Detail != "" {
		t.Errorf("worsened = %+v", ev)
	}
	if rows, _ := e.store.ListSentProblems(context.Background()); len(rows) != 1 || rows[0].Level != "failed" || rows[0].Summary != "Disk nearly full: / is 85% full" {
		t.Errorf("after the rise: %+v, want the level failed and the opened title kept for the resolve", rows)
	}

	// A repeat, a further rise and a fall send nothing.
	for _, pct := range []float64{96, 97, 90, 96, 99} {
		e.step(t, time.Minute, "/", pct)
		e.check(t)
	}
	if got := types(); !slices.Equal(got, []string{notify.EventProblemOpened, notify.EventProblemWorsened}) {
		t.Fatalf("96, 97, 90, 96, 99: %v, want nothing more", got)
	}

	// The resolve after the delay, once.
	e.step(t, time.Minute, "/", 82)
	e.check(t)
	e.step(t, 6*time.Minute, "/", 82)
	e.check(t)
	e.check(t)
	if got := types(); !slices.Equal(got, []string{notify.EventProblemOpened, notify.EventProblemWorsened, notify.EventProblemResolved}) {
		t.Fatalf("82: %v, want one resolved", got)
	}
	if rows, _ := e.store.ListSentProblems(context.Background()); len(rows) != 0 {
		t.Errorf("sent problems after resolving = %+v", rows)
	}

	// A disk that opened already failed sends opened only, at that level.
	e.step(t, time.Minute, "/", 97)
	e.check(t)
	e.step(t, time.Minute, "/", 98)
	e.check(t)
	if got := types(); len(got) != 4 || got[3] != notify.EventProblemOpened {
		t.Fatalf("opened at 97: %v, want just one more opened", got)
	}
}

// diffProblems finds a disk that rose from the warning that was sent to failed,
// and nothing else: not a fall, not a repeat, not a row with no level, not a kind
// with none.
func TestDiffProblemsWorsened(t *testing.T) {
	at := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	level := func(l metrics.DiskLevel) AttentionItem {
		return AttentionItem{Kind: AttentionDiskNearlyFull, Mount: "/", Level: l}
	}
	row := func(l string) []store.SentProblem {
		r := sentRow(AttentionItem{Kind: AttentionDiskNearlyFull, Mount: "/"}, at)
		r.Level = l
		return []store.SentProblem{r}
	}
	oom := item(AttentionOutOfMemory, "blog", "web")
	for _, tc := range []struct {
		name string
		prev []store.SentProblem
		now  []AttentionItem
		want int
	}{
		{"warning sent, failed now", row("warning"), []AttentionItem{level(metrics.DiskFailed)}, 1},
		{"failed sent, failed now", row("failed"), []AttentionItem{level(metrics.DiskFailed)}, 0},
		{"failed sent, warning now", row("failed"), []AttentionItem{level(metrics.DiskWarning)}, 0},
		{"warning sent, warning now", row("warning"), []AttentionItem{level(metrics.DiskWarning)}, 0},
		{"no level known, failed now", row(""), []AttentionItem{level(metrics.DiskFailed)}, 0},
		{"not sent yet is opened, not worsened", nil, []AttentionItem{level(metrics.DiskFailed)}, 0},
		{"a kind with no level", []store.SentProblem{sentRow(oom, at)}, []AttentionItem{oom}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := diffProblems(tc.prev, tc.now, allJudged, map[string]bool{"blog": true}, at, time.Minute)
			if len(ch.Worsened) != tc.want {
				t.Errorf("worsened = %v, want %d", ch.Worsened, tc.want)
			}
		})
	}
}
