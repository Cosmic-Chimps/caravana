package service

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// vulnerableItems are the image_vulnerable items of the needs-you list.
func vulnerableItems(t *testing.T, e *env) []AttentionItem {
	t.Helper()
	att, err := e.svc.Attention(context.Background())
	if err != nil {
		t.Fatalf("Attention: %v", err)
	}
	var out []AttentionItem
	for _, it := range att.Items {
		if it.Kind == AttentionImageVulnerable {
			out = append(out, it)
		}
	}
	return out
}

// scannedWhoami installs the scanner, makes every scan return the text in text and deploys whoami,
// and waits for its first scan. It returns the image.
func scannedWhoami(t *testing.T, e *env, text *atomic.Value) string {
	t.Helper()
	playScans(t, e.nomad, func(p playedScan) scanPlay {
		if p.Source == "database" {
			return scanPlay{}
		}
		return scanPlay{Text: text.Load().(string)}
	})
	installScanner(t, e)
	rec := deployApp(t, e, whoami())
	image := rec.Deployment.Digests["web"]
	waitRun(t, e, image, 1)
	return image
}

func kevLine(advisory, severity, fixed, pkg, version string) string {
	l := strings.Split(matchLine(advisory, severity, "fixed", fixed, pkg, version), "\t")
	l[len(l)-1] = "kev"
	return strings.Join(l, "\t")
}

func TestImageVulnerableOpensOneItemPerPackage(t *testing.T) {
	e, _ := newScanEnv(t)
	var text atomic.Value
	zlib2 := matchLine("CVE-2022-37435", "Critical", "fixed", "1.2.12-r3", "zlib", "1.2.11-r3")
	curl := matchLine("CVE-2023-0001", "High", "fixed", "8.1", "curl", "8.0")
	unfixed := matchLine("CVE-2024-5535", "Critical", "not-fixed", "", "libssl", "1.1")
	text.Store(resultText(zlibLine, zlib2, curl, unfixed, busyboxLine, opensslLine))
	image := scannedWhoami(t, e, &text)

	items := vulnerableItems(t, e)
	if len(items) != 1 {
		t.Fatalf("items at the critical level = %+v, want only zlib", items)
	}
	it := items[0]
	if it.Package != "zlib" || it.PackageVersion != "1.2.11-r3" || it.Severity != "critical" || it.Advisory != "CVE-2022-37434" ||
		len(it.FixedIn) != 1 || it.FixedIn[0] != "1.2.12-r2" || it.MoreAdvisories != 1 || it.Image != image ||
		it.AppID != "whoami" || it.Service != "web" || it.Level != metrics.DiskFailed || it.KEV || it.When.IsZero() {
		t.Errorf("item = %+v", it)
	}
	if key := problemKey(it); key != "image_vulnerable|whoami|web|zlib" {
		t.Errorf("key = %q", key)
	}
	if want := "zlib 1.2.11-r3 in whoami/web has CVE-2022-37434 (critical), fixed in 1.2.12-r2 and 1 more advisory."; it.Detail != want {
		t.Errorf("detail = %q, want %q", it.Detail, want)
	}
	// The same words in the feed and the notification.
	if got := plainText(ProblemParts(it, func(s string) string { return s })); got != it.Detail {
		t.Errorf("ProblemParts = %q", got)
	}
	if ev := problemEvent(it, "https://c.example.com", time.Now()); ev.Text != it.Detail || ev.Package != "zlib" || ev.PackageVersion != "1.2.11-r3" ||
		ev.Advisory != "CVE-2022-37434" || ev.Severity != "critical" || ev.Image != image || len(ev.FixedIn) != 1 ||
		ev.Title != "whoami/web: zlib has CVE-2022-37434 (critical)" || ev.Kind != "image_vulnerable" ||
		ev.URL != "https://c.example.com/apps/whoami/deployments" || ev.Detail != "" {
		t.Errorf("event = %+v", ev)
	}

	// At the high level the highs with a fix count too: curl is a warning of its own,
	// and zlib's group is the same item (key and level), now with one more advisory.
	if _, err := e.svc.SetScanSettings(context.Background(), ScanSettings{NotifyLevel: "high", DailyHour: 4}); err != nil {
		t.Fatal(err)
	}
	items = vulnerableItems(t, e)
	byPkg := map[string]AttentionItem{}
	for _, it := range items {
		byPkg[it.Package] = it
	}
	if len(items) != 2 || byPkg["curl"].Level != metrics.DiskWarning || byPkg["curl"].Severity != "high" || byPkg["curl"].MoreAdvisories != 0 ||
		byPkg["zlib"].Level != metrics.DiskFailed || byPkg["zlib"].MoreAdvisories != 1 {
		t.Errorf("items at the high level = %+v", items)
	}
	if want := "curl 8.0 in whoami/web has CVE-2023-0001 (high), fixed in 8.1."; byPkg["curl"].Detail != want {
		t.Errorf("curl detail = %q, want %q", byPkg["curl"].Detail, want)
	}
}

func TestImageVulnerableSaysKnownExploited(t *testing.T) {
	e, _ := newScanEnv(t)
	var text atomic.Value
	text.Store(resultText(kevLine("CVE-2021-44228", "Critical", "2.15.0", "log4j-core", "2.14.1")))
	scannedWhoami(t, e, &text)
	items := vulnerableItems(t, e)
	if len(items) != 1 || !items[0].KEV {
		t.Fatalf("items = %+v", items)
	}
	if want := "log4j-core 2.14.1 in whoami/web has CVE-2021-44228 (critical), fixed in 2.15.0. Known exploited."; items[0].Detail != want {
		t.Errorf("detail = %q, want %q", items[0].Detail, want)
	}
	parts := ProblemParts(items[0], func(s string) string { return s })
	monos := ""
	for _, p := range parts {
		if p.Mono {
			monos += "[" + p.Text + "]"
		}
	}
	if monos != "[log4j-core][2.14.1][whoami/web][CVE-2021-44228][2.15.0]" {
		t.Errorf("mono parts = %s", monos)
	}
}

func TestUnfixableFindingsOpenNothing(t *testing.T) {
	e, _ := newScanEnv(t)
	var text atomic.Value
	text.Store(resultText(matchLine("CVE-2024-5535", "Critical", "not-fixed", "", "libssl", "1.1"),
		matchLine("CVE-2024-5536", "Critical", "wont-fix", "", "libcrypto", "1.1"), busyboxLine))
	scannedWhoami(t, e, &text)
	if items := vulnerableItems(t, e); len(items) != 0 {
		t.Errorf("items = %+v, want none: nothing has a fix", items)
	}
}

func TestAFixedImageRemovesTheItem(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	var text atomic.Value
	text.Store(resultText(zlibLine))
	image := scannedWhoami(t, e, &text)
	if len(vulnerableItems(t, e)) != 1 {
		t.Fatal("no item before the fix")
	}
	// A redeploy with a fixed image: its scan finds nothing at that level.
	text.Store(resultText(opensslLine))
	if _, err := e.svc.RequestScan(ctx, "whoami"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, e, image, 2)
	if items := vulnerableItems(t, e); len(items) != 0 {
		t.Errorf("items after the fix = %+v", items)
	}
}

func TestAStoppedAppIsNotJudgedAndNotInstalledMeansNoItems(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	var text atomic.Value
	text.Store(resultText(zlibLine))
	scannedWhoami(t, e, &text)
	if len(vulnerableItems(t, e)) != 1 {
		t.Fatal("no item")
	}
	if _, err := e.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if items := vulnerableItems(t, e); len(items) != 0 {
		t.Errorf("items of a stopped app = %+v", items)
	}
	if att, _ := e.svc.Attention(ctx); att.ScansOff {
		t.Error("ScansOff while installed")
	}
	if _, err := e.svc.StartApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatal(err)
	}
	if len(vulnerableItems(t, e)) != 1 {
		t.Fatal("the item didn't come back with the app")
	}

	// Findings stored by an installation that has no scanner (a leftover) judge nothing.
	if _, err := e.svc.RemoveImageScanner(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the removal", func() bool {
		_, err := e.store.GetAddon(ctx, store.AddonImageScanner)
		return err != nil
	})
	att, _ := e.svc.Attention(ctx)
	if !att.ScansOff || len(vulnerableItems(t, e)) != 0 {
		t.Errorf("after the removal: ScansOff = %v, items = %+v", att.ScansOff, vulnerableItems(t, e))
	}
}

func TestRemovingTheScannerSendsNoResolved(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	var text atomic.Value
	text.Store(resultText(zlibLine))
	scannedWhoami(t, e, &text)
	e.svc.resolveAfter = time.Nanosecond

	if err := e.svc.checkProblems(ctx); err != nil {
		t.Fatal(err)
	}
	sent, _ := e.store.ListSentProblems(ctx)
	if len(sent) != 1 || sent[0].Kind != "image_vulnerable" || sent[0].Key != "image_vulnerable|whoami|web|zlib" {
		t.Fatalf("sent problems = %+v", sent)
	}
	if _, err := e.svc.RemoveImageScanner(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the removal", func() bool {
		_, err := e.store.GetAddon(ctx, store.AddonImageScanner)
		return err != nil
	})
	if sent, _ := e.store.ListSentProblems(ctx); len(sent) != 0 {
		t.Errorf("sent problems after the removal = %+v, want them forgotten", sent)
	}
	time.Sleep(5 * time.Millisecond)
	if err := e.svc.checkProblems(ctx); err != nil {
		t.Fatal(err)
	}
	if sent, _ := e.store.ListSentProblems(ctx); len(sent) != 0 {
		t.Errorf("sent problems after another check = %+v", sent)
	}
}

func TestDiffProblemsDoesNotJudgeImageVulnerableWithoutTheScanner(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	prev := []store.SentProblem{{Key: "image_vulnerable|whoami|web|zlib", Kind: "image_vulnerable", AppID: "whoami", Service: "web",
		OpenedAt: at.Add(-time.Hour), LastSeenAt: at.Add(-time.Hour)}}
	exists := map[string]bool{"whoami": true}
	item := AttentionItem{Kind: AttentionImageVulnerable, AppID: "whoami", Service: "web", Package: "zlib"}

	// Judged and gone for long enough: resolved, as every kind.
	if got := diffProblems(prev, nil, allJudged, exists, at, time.Minute); len(got.Resolved) != 1 {
		t.Errorf("judged, gone: %+v", got)
	}
	// Not judged (the add-on is off): neither resolved nor opened.
	off := allJudged
	off.Scans = false
	if got := diffProblems(prev, nil, off, exists, at, time.Minute); len(got.Resolved) != 0 || len(got.Opened) != 0 {
		t.Errorf("not judged, gone: %+v", got)
	}
	if got := diffProblems(nil, []AttentionItem{item}, off, exists, at, time.Minute); len(got.Opened) != 0 {
		t.Errorf("not judged, present: %+v", got)
	}
	if !notJudged(AttentionImageVulnerable, judged{Copies: true, Disks: true, OS: true, Servers: true}) ||
		notJudged(AttentionImageVulnerable, judged{Scans: true}) {
		t.Error("notJudged doesn't follow Scans")
	}
}

func TestTheStoreAndTheServiceAgreeOnTheKind(t *testing.T) {
	if store.ProblemImageVulnerable != string(AttentionImageVulnerable) {
		t.Errorf("store says %q, service says %q", store.ProblemImageVulnerable, AttentionImageVulnerable)
	}
}
