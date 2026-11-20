package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/vulns"
)

const (
	scanImageA = "docker.io/library/alpine@sha256:aaaa"
	scanImageB = "ghcr.io/acme/web@sha256:bbbb"
)

func mkFinding(id, pkg string, sev vulns.Severity, fixed ...string) vulns.Finding {
	f := vulns.Finding{ID: id, Advisory: "CVE-2022-" + id, CVE: "CVE-2022-" + id, Package: pkg, Version: "1.0", Type: "apk",
		Severity: sev, URL: "https://example.com/" + id}
	if len(fixed) > 0 {
		f.FixState, f.FixedIn = vulns.FixStateFixed, fixed
	}
	return f
}

func mkResult(fs ...vulns.Finding) vulns.Result {
	return vulns.Result{Findings: fs, DBBuilt: time.Date(2026, 11, 19, 6, 0, 0, 0, time.UTC), DBSchema: "v6.1.10", Scanner: "0.117.0"}
}

func TestAddonLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.GetAddon(ctx, AddonImageScanner); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetAddon with none = %v, want ErrNotFound", err)
	}
	if err := s.SaveAddon(ctx, Addon{Name: AddonImageScanner, State: AddonInstalling, Version: "0.117.0"}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	if err := s.SaveAddon(ctx, Addon{Name: AddonImageScanner, State: AddonInstalled, Version: "0.117.0", InstalledAt: at}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAddon(ctx, AddonImageScanner)
	if err != nil || got.State != AddonInstalled || !got.InstalledAt.Equal(at) || got.UpdatedAt.IsZero() {
		t.Fatalf("add-on = %+v, %v", got, err)
	}
	if list, err := s.ListAddons(ctx); err != nil || len(list) != 1 {
		t.Errorf("ListAddons = %+v, %v", list, err)
	}
	if err := s.DeleteAddon(ctx, AddonImageScanner); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAddon(ctx, AddonImageScanner); err != nil {
		t.Errorf("deleting twice: %v", err)
	}
	if _, err := s.GetAddon(ctx, AddonImageScanner); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAddon after delete = %v", err)
	}
}

func TestScanQueue(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	queue := func(id, image, trigger string) (ScanRun, bool) {
		t.Helper()
		run, created, err := s.QueueScanRun(ctx, ScanRun{ID: id, Image: image, Trigger: trigger, Source: ScanSourceRegistry})
		if err != nil {
			t.Fatalf("QueueScanRun(%s): %v", id, err)
		}
		return run, created
	}
	if _, err := s.NextQueuedScanRun(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("NextQueuedScanRun of nothing = %v", err)
	}
	a, created := queue("r1", scanImageA, ScanTriggerDeploy)
	if !created || a.Status != ScanQueued {
		t.Fatalf("first run = %+v, %v", a, created)
	}
	// The same image again is the run already there.
	dup, created := queue("r2", scanImageA, ScanTriggerOwner)
	if created || dup.ID != "r1" {
		t.Errorf("a second queue of the same image = %+v, %v; want r1 and not created", dup, created)
	}
	queue("r3", scanImageB, ScanTriggerDaily)
	queue("r4", "ghcr.io/acme/db@sha256:cccc", ScanTriggerGate)

	next, err := s.NextQueuedScanRun(ctx)
	if err != nil || next.ID != "r4" {
		t.Fatalf("next = %+v, %v; want the gate run first", next, err)
	}
	if err := s.StartScanRun(ctx, "r4", ScanSourceSBOM, s.now()); err != nil {
		t.Fatal(err)
	}
	if err := s.StartScanRun(ctx, "r4", ScanSourceSBOM, s.now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("starting twice = %v, want ErrNotFound", err)
	}
	// A run that is running keeps its image from being queued again.
	if _, created := queue("r5", "ghcr.io/acme/db@sha256:cccc", ScanTriggerOwner); created {
		t.Error("an image whose run is running was queued again")
	}
	next, _ = s.NextQueuedScanRun(ctx)
	if next.ID != "r1" {
		t.Errorf("next after the gate run started = %s, want r1 (oldest)", next.ID)
	}

	built := time.Date(2026, 11, 19, 6, 0, 0, 0, time.UTC)
	if err := s.EndScanRun(ctx, "r4", ScanOK, "", "0.117.0", built, s.now()); err != nil {
		t.Fatal(err)
	}
	run, _ := s.GetScanRun(ctx, "r4")
	if run.Source != ScanSourceSBOM {
		t.Errorf("source = %q, want the one the run started with", run.Source)
	}
	if run.Status != ScanOK || run.Scanner != "0.117.0" || !run.DBBuilt.Equal(built) || run.StartedAt.IsZero() || run.EndedAt.IsZero() {
		t.Errorf("settled run = %+v", run)
	}
	if err := s.EndScanRun(ctx, "r4", ScanFailed, "x", "", time.Time{}, s.now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("settling twice = %v, want ErrNotFound", err)
	}
	if err := s.EndScanRun(ctx, "r1", ScanRunning, "", "", time.Time{}, s.now()); err == nil {
		t.Error("ending a run as running should fail")
	}

	failed, err := s.FailOpenScanRuns(ctx, true, false, "the image scanner was removed", s.now())
	if err != nil || len(failed) != 2 {
		t.Fatalf("FailOpenScanRuns = %+v, %v; want r1 and r3", failed, err)
	}
	open, _ := s.OpenScanRuns(ctx)
	if len(open) != 0 {
		t.Errorf("open after failing the queue = %+v", open)
	}
	if r, _ := s.GetScanRun(ctx, "r1"); r.Status != ScanFailed || r.Reason != "the image scanner was removed" {
		t.Errorf("r1 = %+v", r)
	}
}

func TestFailOpenScanRunsCanIncludeRunning(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, id := range []string{"a", "b"} {
		if _, _, err := s.QueueScanRun(ctx, ScanRun{ID: id, Image: "img-" + id, Trigger: ScanTriggerDeploy, Source: ScanSourceRegistry}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.StartScanRun(ctx, "a", ScanSourceRegistry, s.now()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.FailOpenScanRuns(ctx, true, false, "x", s.now()); err != nil || len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("without running: %+v, %v", got, err)
	}
	if got, err := s.FailOpenScanRuns(ctx, false, true, "Caravana restarted during the scan", s.now()); err != nil || len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("with running: %+v, %v", got, err)
	}
}

func TestScanRunHistoryKeeps200(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for i := range 205 {
		id := fmt.Sprintf("run-%03d", i)
		if _, _, err := s.QueueScanRun(ctx, ScanRun{ID: id, Image: id, Trigger: ScanTriggerDaily, Source: ScanSourceSBOM}); err != nil {
			t.Fatal(err)
		}
		if err := s.EndScanRun(ctx, id, ScanOK, "", "0.117.0", time.Time{}, s.now()); err != nil {
			t.Fatal(err)
		}
	}
	// A queued one older than all must survive the pruning.
	if _, _, err := s.QueueScanRun(ctx, ScanRun{ID: "queued", Image: "q", Trigger: ScanTriggerDaily, Source: ScanSourceSBOM,
		CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if err := s.EndScanRun(ctx, "run-204", ScanOK, "", "", time.Time{}, s.now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("setup: %v", err)
	}
	all, err := s.ListScanRuns(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 201 { // 200 finished and the queued one
		t.Errorf("runs kept = %d, want 200 finished + 1 queued", len(all))
	}
	if all[0].ID != "run-204" {
		t.Errorf("newest = %s", all[0].ID)
	}
	if got, _ := s.ListScanRuns(ctx, 5); len(got) != 5 {
		t.Errorf("limit 5 gave %d", len(got))
	}
}

func TestSaveScanResultKeepsFirstSeen(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	t1 := time.Date(2026, 11, 19, 4, 0, 0, 0, time.UTC)
	t2 := t1.Add(24 * time.Hour)

	zlib := mkFinding("0001", "zlib", vulns.Critical, "1.2.12-r2")
	busybox := mkFinding("0002", "busybox", vulns.High)
	if err := s.SaveScanResult(ctx, scanImageA, mkResult(zlib, busybox), true, t1); err != nil {
		t.Fatal(err)
	}
	// Second scan: busybox gone, a new advisory appears.
	openssl := mkFinding("0003", "openssl", vulns.Medium)
	if err := s.SaveScanResult(ctx, scanImageA, mkResult(zlib, openssl), true, t2); err != nil {
		t.Fatal(err)
	}
	got, err := s.ImageFindings(ctx, scanImageA)
	if err != nil || len(got) != 2 {
		t.Fatalf("findings = %+v, %v", got, err)
	}
	byID := map[string]StoredFinding{}
	for _, f := range got {
		byID[f.ID] = f
	}
	if f := byID["0001"]; !f.FirstSeen.Equal(t1) || !f.LastSeen.Equal(t2) {
		t.Errorf("the finding both scans saw: first %v last %v; want %v and %v", f.FirstSeen, f.LastSeen, t1, t2)
	}
	if f := byID["0003"]; !f.FirstSeen.Equal(t2) || !f.LastSeen.Equal(t2) {
		t.Errorf("the new finding: first %v last %v; want both %v", f.FirstSeen, f.LastSeen, t2)
	}
	if _, gone := byID["0002"]; gone {
		t.Error("a finding the newest scan doesn't have is still stored")
	}
	// Order: the critical first.
	if got[0].ID != "0001" {
		t.Errorf("order = %s first", got[0].ID)
	}
	scan, err := s.GetImageScan(ctx, scanImageA)
	if err != nil || scan.Counts.Critical != 1 || scan.Counts.Medium != 1 || scan.Counts.High != 0 || scan.Counts.FixableCritical != 1 ||
		!scan.SBOMKept || scan.Status != ScanOK || !scan.ScannedAt.Equal(t2) || scan.Scanner != "0.117.0" || scan.DBSchema != "v6.1.10" {
		t.Errorf("scan = %+v, %v", scan, err)
	}

	// The same scan again at the same time keeps everything and deletes nothing.
	if err := s.SaveScanResult(ctx, scanImageA, mkResult(zlib, openssl), true, t2); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ImageFindings(ctx, scanImageA); len(got) != 2 {
		t.Errorf("after a repeat: %d findings", len(got))
	}
}

func TestFindingFieldsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	f := mkFinding("0009", "zlib", vulns.Critical, "1.2.12-r2", "1.3-r0")
	f.EPSS = &vulns.EPSS{Score: 0.19243, Percentile: 0.97389}
	f.KEV = true
	at := time.Date(2026, 11, 19, 4, 0, 0, 0, time.UTC)
	if err := s.SaveScanResult(ctx, scanImageA, mkResult(f), false, at); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ImageFindings(ctx, scanImageA)
	if len(got) != 1 {
		t.Fatalf("findings = %+v", got)
	}
	g := got[0]
	if g.Package != "zlib" || g.Severity != vulns.Critical || g.FixState != "fixed" || len(g.FixedIn) != 2 || g.FixedIn[1] != "1.3-r0" ||
		g.EPSS == nil || g.EPSS.Score != 0.19243 || g.EPSS.Percentile != 0.97389 || !g.KEV || g.URL != f.URL || g.CVE != f.CVE || g.Type != "apk" {
		t.Errorf("round trip = %+v", g)
	}
	none := mkFinding("0010", "busybox", vulns.Low)
	_ = s.SaveScanResult(ctx, scanImageB, mkResult(none), false, at)
	got, _ = s.ImageFindings(ctx, scanImageB)
	if got[0].EPSS != nil || got[0].KEV || len(got[0].FixedIn) != 0 {
		t.Errorf("a plain finding = %+v", got[0])
	}
}

func TestRecordScanTryKeepsFindings(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	t1 := time.Date(2026, 11, 19, 4, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)

	// An image never scanned gets a row with no result.
	if err := s.RecordScanTry(ctx, scanImageA, ScanSkipped, "not enough free disk", t1); err != nil {
		t.Fatal(err)
	}
	scan, err := s.GetImageScan(ctx, scanImageA)
	if err != nil || scan.HasResult() || scan.Status != ScanSkipped || scan.Reason != "not enough free disk" {
		t.Fatalf("scan = %+v, %v", scan, err)
	}

	if err := s.SaveScanResult(ctx, scanImageA, mkResult(mkFinding("0001", "zlib", vulns.High)), true, t1); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordScanTry(ctx, scanImageA, ScanFailed, "the registry refused the credentials", t2); err != nil {
		t.Fatal(err)
	}
	scan, _ = s.GetImageScan(ctx, scanImageA)
	if scan.Status != ScanFailed || scan.Reason == "" || !scan.TriedAt.Equal(t2) || !scan.ScannedAt.Equal(t1) || scan.Counts.High != 1 || !scan.SBOMKept {
		t.Errorf("after a failed try: %+v", scan)
	}
	if got, _ := s.ImageFindings(ctx, scanImageA); len(got) != 1 {
		t.Errorf("a failed try changed the findings: %+v", got)
	}
	if err := s.RecordScanTry(ctx, scanImageA, ScanOK, "", t2); err == nil {
		t.Error("recording a try as ok should fail: SaveScanResult does that")
	}
	if err := s.ClearSBOMKept(ctx, scanImageA); err != nil {
		t.Fatal(err)
	}
	if scan, _ := s.GetImageScan(ctx, scanImageA); scan.SBOMKept {
		t.Error("the SBOM is still kept")
	}
	// A good result clears the failure.
	if err := s.SaveScanResult(ctx, scanImageA, mkResult(), false, t2.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if scan, _ := s.GetImageScan(ctx, scanImageA); scan.Status != ScanOK || scan.Reason != "" {
		t.Errorf("after a good result: %+v", scan)
	}
}

func TestFixableFindings(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	at := time.Date(2026, 11, 19, 4, 0, 0, 0, time.UTC)
	_ = s.SaveScanResult(ctx, scanImageA, mkResult(
		mkFinding("0001", "zlib", vulns.Critical, "1.2.12-r2"),
		mkFinding("0002", "busybox", vulns.Critical), // no fix
		mkFinding("0003", "openssl", vulns.High, "3.0.8"),
		mkFinding("0004", "curl", vulns.Medium, "8.0"),
	), false, at)
	_ = s.SaveScanResult(ctx, scanImageB, mkResult(mkFinding("0005", "zlib", vulns.Critical, "1.2.12-r2")), false, at)

	got, err := s.FixableFindings(ctx, []string{scanImageA}, vulns.Critical)
	if err != nil || len(got) != 1 || len(got[scanImageA]) != 1 || got[scanImageA][0].Package != "zlib" {
		t.Fatalf("critical fixable of A = %+v, %v", got, err)
	}
	got, _ = s.FixableFindings(ctx, []string{scanImageA, scanImageB}, vulns.High)
	if len(got[scanImageA]) != 2 || got[scanImageA][0].Package != "zlib" || got[scanImageA][1].Package != "openssl" || len(got[scanImageB]) != 1 {
		t.Errorf("high and up = %+v", got)
	}
	if got, _ := s.FixableFindings(ctx, nil, vulns.Critical); len(got) != 0 {
		t.Errorf("no images = %+v", got)
	}
	if got, _ := s.FixableFindings(ctx, []string{"unknown@sha256:0"}, vulns.Info); len(got) != 0 {
		t.Errorf("an unscanned image = %+v", got)
	}
}

func TestPruneAndForgetScans(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	at := time.Date(2026, 11, 19, 4, 0, 0, 0, time.UTC)
	_ = s.SaveScanResult(ctx, scanImageA, mkResult(mkFinding("0001", "zlib", vulns.High)), false, at)
	_ = s.SaveScanResult(ctx, scanImageB, mkResult(mkFinding("0002", "zlib", vulns.High)), false, at)
	if n, err := s.PruneImageScans(ctx, []string{scanImageA}); err != nil || n != 1 {
		t.Fatalf("PruneImageScans = %d, %v", n, err)
	}
	if _, err := s.GetImageScan(ctx, scanImageB); !errors.Is(err, ErrNotFound) {
		t.Errorf("B after prune = %v", err)
	}
	if got, _ := s.ImageFindings(ctx, scanImageB); len(got) != 0 {
		t.Errorf("B's findings outlived it: %+v", got)
	}
	if got, _ := s.ImageFindings(ctx, scanImageA); len(got) != 1 {
		t.Errorf("A lost its findings: %+v", got)
	}

	_, _, _ = s.QueueScanRun(ctx, ScanRun{ID: "r", Image: scanImageA, Trigger: ScanTriggerOwner, Source: ScanSourceRegistry})
	_ = s.SetLastDaily(ctx, at)
	if err := s.SaveScanSettings(ctx, ScanSettings{NotifyLevel: "high", DailyHour: 6, RefuseFixableCritical: true}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"image_vulnerable", "failed_deploy"} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO sent_problems (key, kind, opened_at, last_seen_at, summary) VALUES (?, ?, ?, ?, 'x')`,
			kind+"|a|web|z", kind, formatTime(at), formatTime(at)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ForgetScans(ctx); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, _ := s.db.QueryContext(ctx, `SELECT kind FROM sent_problems`)
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		left = append(left, k)
	}
	rows.Close()
	if len(left) != 1 || left[0] != "failed_deploy" {
		t.Errorf("notifier rows after forget = %v, want only the other kind's", left)
	}
	if all, _ := s.ListImageScans(ctx); len(all) != 0 {
		t.Errorf("scans after forget = %+v", all)
	}
	if runs, _ := s.ListScanRuns(ctx, 0); len(runs) != 0 {
		t.Errorf("runs after forget = %+v", runs)
	}
	set, err := s.GetScanSettings(ctx)
	if err != nil || set.NotifyLevel != "high" || set.DailyHour != 6 || !set.RefuseFixableCritical || !set.LastDaily.IsZero() {
		t.Errorf("settings after forget = %+v, %v; want them kept, last daily cleared", set, err)
	}
}

func TestScanSettingsDefaultsAndSave(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	set, err := s.GetScanSettings(ctx)
	if err != nil || set.NotifyLevel != "critical" || set.RefuseFixableCritical || set.DailyHour != 4 || set.DatabaseURL != "" || !set.LastDaily.IsZero() {
		t.Fatalf("defaults = %+v, %v", set, err)
	}
	at := time.Date(2026, 11, 19, 4, 5, 0, 0, time.UTC)
	if err := s.SetLastDaily(ctx, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveScanSettings(ctx, ScanSettings{NotifyLevel: "high", DailyHour: 23, DatabaseURL: "https://mirror.example/latest.json"}); err != nil {
		t.Fatal(err)
	}
	set, _ = s.GetScanSettings(ctx)
	if set.NotifyLevel != "high" || set.DailyHour != 23 || set.DatabaseURL != "https://mirror.example/latest.json" || !set.LastDaily.Equal(at) {
		t.Errorf("saved = %+v; saving settings must keep the last daily", set)
	}
	if err := s.SaveScanSettings(ctx, ScanSettings{NotifyLevel: "low"}); err == nil {
		t.Error("a bad notify level was accepted")
	}
	if err := s.SaveScanSettings(ctx, ScanSettings{NotifyLevel: "high", DailyHour: 24}); err == nil {
		t.Error("an hour of 24 was accepted")
	}
}
