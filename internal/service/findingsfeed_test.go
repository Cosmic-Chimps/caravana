package service

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// findingItem is one image_vulnerable item: a package of a service of an app.
func findingItem(appID, service, pkg string, critical bool) AttentionItem {
	it := AttentionItem{Kind: AttentionImageVulnerable, AppID: appID, Service: service, Package: pkg,
		PackageVersion: "1.0", FixedIn: []string{"1.1"}, Advisory: "CVE-2024-0300", Severity: "high", Level: metrics.DiskWarning}
	if critical {
		it.Severity, it.Level = "critical", metrics.DiskFailed
	}
	return it
}

// sentOf is the sent problem a check records for an item.
func sentOf(it AttentionItem, at time.Time) store.SentProblem {
	return store.SentProblem{Key: problemKey(it), Kind: string(it.Kind), AppID: it.AppID, Service: it.Service,
		OpenedAt: at, LastSeenAt: at, Level: levelOf(it), Sentence: encodeParts(ProblemParts(it, func(s string) string { return s }))}
}

// The review fixture's 36 findings over 3 apps: 9 critical in uptime-kuma, 12 critical and 4 high in
// worker (two services), 11 high in shop › staging › web.
func thirtySixFindings() []AttentionItem {
	var items []AttentionItem
	for i := range 9 {
		items = append(items, findingItem("uptime-kuma", "web", fmt.Sprintf("pkg%d", i), true))
	}
	for i := range 12 {
		items = append(items, findingItem("worker", "web", fmt.Sprintf("pkg%d", i), true))
	}
	for i := range 4 {
		items = append(items, findingItem("worker", "jobs", fmt.Sprintf("pkg%d", i), false))
	}
	for i := range 11 {
		items = append(items, findingItem("shop--staging--web", "web", fmt.Sprintf("pkg%d", i), false))
	}
	return items
}

var findingPlaces = map[string]app.Place{
	"uptime-kuma":        {Project: app.DefaultProject, Environment: app.DefaultEnvironment, Name: "uptime-kuma"},
	"worker":             {Project: app.DefaultProject, Environment: app.DefaultEnvironment, Name: "worker"},
	"shop--staging--web": {Project: "shop", Environment: "staging", Name: "web"},
}

func shopDisplay(id string) string { return map[string]string{"shop": "Shop"}[id] }

func eventLines(events []store.ProblemEvent) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Change+" "+e.AppID+": "+e.Summary)
	}
	return out
}

// One line per app when its first findings appear, one when its last goes, nothing for a package that comes
// or goes while the app still has findings.
func TestFindingEventsAreOneLinePerApp(t *testing.T) {
	t0 := time.Date(2026, 11, 27, 4, 0, 0, 0, time.UTC)
	exists := map[string]bool{"uptime-kuma": true, "worker": true, "shop--staging--web": true}
	items := thirtySixFindings()

	// The scan opens 36 findings over 3 apps: 3 lines.
	changes := diffProblems(nil, items, allJudged, exists, t0, time.Minute)
	if len(changes.Opened) != 36 {
		t.Fatalf("opened %d items, want 36", len(changes.Opened))
	}
	got := eventLines(findingEvents(nil, changes, findingPlaces, shopDisplay, t0))
	want := []string{
		"opened shop--staging--web: Shop › staging › web: 11 fixable high vulnerabilities found.",
		"opened uptime-kuma: uptime-kuma: 9 fixable critical vulnerabilities found.",
		"opened worker: worker: 16 fixable vulnerabilities found: 12 critical, 4 high.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	ev := findingEvents(nil, changes, findingPlaces, shopDisplay, t0)[1]
	if ev.Key != "image_vulnerable|uptime-kuma||" || ev.Kind != "image_vulnerable" || !ev.At.Equal(t0) {
		t.Errorf("event = %+v", ev)
	}
	if p, ok := decodeParts(ev.Sentence); !ok || p[0] != mono("uptime-kuma") {
		t.Errorf("sentence parts = %+v", p)
	}

	var prev []store.SentProblem
	for _, it := range items {
		prev = append(prev, sentOf(it, t0))
	}
	t1 := t0.Add(time.Hour)

	// All of uptime-kuma's go, one of worker's goes, and shop's web gets a package more: one line.
	var now []AttentionItem
	for _, it := range items {
		if it.AppID == "uptime-kuma" || (it.AppID == "worker" && it.Package == "pkg0" && it.Service == "web") {
			continue
		}
		now = append(now, it)
	}
	now = append(now, findingItem("shop--staging--web", "web", "pkg99", true))
	changes = diffProblems(prev, now, allJudged, exists, t1, time.Minute)
	if len(changes.Resolved) != 10 || len(changes.Opened) != 1 {
		t.Fatalf("resolved %d, opened %d; want 10 and 1", len(changes.Resolved), len(changes.Opened))
	}
	got = eventLines(findingEvents(prev, changes, findingPlaces, shopDisplay, t1))
	if len(got) != 1 || got[0] != "resolved uptime-kuma: uptime-kuma has no fixable vulnerabilities left." {
		t.Errorf("events = %q, want only uptime-kuma's resolution", got)
	}

	// A package that goes while it is young (seen a moment ago) isn't resolved yet: no line either.
	if got := findingEvents(prev, diffProblems(prev, now, allJudged, exists, t0.Add(time.Second), time.Minute),
		findingPlaces, shopDisplay, t0); len(got) != 0 {
		t.Errorf("events before resolveAfter = %q", eventLines(got))
	}

	// An app that is gone: its problems are dropped and say nothing.
	gone := map[string]bool{"worker": true, "shop--staging--web": true}
	changes = diffProblems(prev, nil, allJudged, gone, t1, time.Minute)
	got = eventLines(findingEvents(prev, changes, findingPlaces, shopDisplay, t1))
	if len(got) != 2 || strings.Contains(strings.Join(got, "\n"), "uptime-kuma") {
		t.Errorf("events with uptime-kuma deleted = %q", got)
	}

	// The scanner not judged: nothing opens or resolves, so nothing is said.
	off := allJudged
	off.Scans = false
	if got := findingEvents(prev, diffProblems(prev, nil, off, exists, t1, time.Minute), findingPlaces, shopDisplay, t1); len(got) != 0 {
		t.Errorf("events while not judged = %q", eventLines(got))
	}
}

func TestFindingsFoundWords(t *testing.T) {
	who := []TextPart{mono("blog")}
	for _, tc := range []struct {
		critical, high int
		want           string
	}{
		{1, 0, "blog: 1 fixable critical vulnerability found."},
		{9, 0, "blog: 9 fixable critical vulnerabilities found."},
		{0, 1, "blog: 1 fixable high vulnerability found."},
		{1, 1, "blog: 2 fixable vulnerabilities found: 1 critical, 1 high."},
	} {
		if got := plainText(findingsFoundParts(who, tc.critical, tc.high)); got != tc.want {
			t.Errorf("%d critical, %d high = %q, want %q", tc.critical, tc.high, got, tc.want)
		}
	}
}

// Through the store and the feed: the check records the 36 problems but writes only the per-app events, and
// the feed lists those, never a package. A per-package event an older Caravana wrote isn't listed either.
func TestTheFeedHasNoLinePerPackage(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	t0 := time.Date(2026, 11, 27, 4, 0, 0, 0, time.UTC)
	exists := map[string]bool{"uptime-kuma": true, "worker": true, "shop--staging--web": true}
	items := thirtySixFindings()
	// A package name no feed line may hold.
	items[0].Package = "s3cret-sentinel-9h5g"

	// What an older Caravana wrote: one event per package.
	old := sentOf(findingItem("uptime-kuma", "web", "zlib", true), t0.Add(-time.Hour))
	if err := e.store.ApplyProblemCheck(ctx, store.ProblemCheck{Opened: []store.SentProblem{old}, SeenAt: old.OpenedAt}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.ApplyProblemCheck(ctx, store.ProblemCheck{Removed: []string{old.Key}, SeenAt: t0.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}

	changes := diffProblems(nil, items, allJudged, exists, t0, time.Minute)
	check := store.ProblemCheck{SeenAt: t0, Quiet: []string{string(AttentionImageVulnerable)},
		Events: findingEvents(nil, changes, findingPlaces, shopDisplay, t0)}
	for _, it := range changes.Opened {
		check.Opened = append(check.Opened, sentOf(it, t0))
	}
	if err := e.store.ApplyProblemCheck(ctx, check); err != nil {
		t.Fatal(err)
	}
	if sent, _ := e.store.ListSentProblems(ctx); len(sent) != 36 {
		t.Errorf("sent problems = %d, want 36: notifications stay one per package", len(sent))
	}

	feed, err := e.svc.Activity(ctx, ActivityQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, en := range feed {
		if en.Kind == ActivityNeedsYou {
			lines = append(lines, en.AppID+": "+en.Sentence)
			if strings.Contains(en.Sentence, "s3cret-sentinel-9h5g") || strings.Contains(en.Sentence, "CVE-") {
				t.Errorf("a feed line names a package or an advisory: %q", en.Sentence)
			}
		}
	}
	if len(lines) != 3 {
		t.Fatalf("feed lines = %q, want 3 (one per app, and no older per-package line)", lines)
	}
	for _, l := range lines {
		if !strings.Contains(l, "fixable") {
			t.Errorf("line = %q", l)
		}
	}

	// A worse level of a package (a quiet kind) and its resolution later write nothing.
	key := problemKey(items[30])
	if err := e.store.ApplyProblemCheck(ctx, store.ProblemCheck{SeenAt: t0.Add(time.Minute), Quiet: check.Quiet,
		Levels: map[string]string{key: string(metrics.DiskFailed)}}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.ApplyProblemCheck(ctx, store.ProblemCheck{SeenAt: t0.Add(time.Hour), Quiet: check.Quiet, Removed: []string{key}}); err != nil {
		t.Fatal(err)
	}
	events, _ := e.store.ProblemEvents(ctx, 100)
	if len(events) != 5 { // the older two, and the three per-app lines
		t.Errorf("problem events = %d, want 5", len(events))
	}
}

// The notifier's own check, with a real scan: one line for the app, the needs-you item as it was.
func TestTheCheckWritesOneFeedLinePerApp(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	var text atomic.Value
	musl := matchLine("CVE-2023-9999", "Critical", "fixed", "1.2.4", "musl", "1.2.3")
	text.Store(resultText(zlibLine, musl))
	scannedWhoami(t, e, &text)

	if err := e.svc.checkProblems(ctx); err != nil {
		t.Fatal(err)
	}
	if sent, _ := e.store.ListSentProblems(ctx); len(sent) != 2 {
		t.Fatalf("sent problems = %+v, want one per package", sent)
	}
	feed, err := e.svc.Activity(ctx, ActivityQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, en := range feed {
		if en.Kind == ActivityNeedsYou {
			lines = append(lines, en.Sentence)
		}
	}
	if len(lines) != 1 || lines[0] != "whoami: 2 fixable critical vulnerabilities found." {
		t.Errorf("feed lines = %q", lines)
	}
}
