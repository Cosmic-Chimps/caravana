package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
	"github.com/cosmic-chimps/caravana/internal/store/storetest"
	"github.com/cosmic-chimps/caravana/internal/vulns"
)

// The tests of the image scanner (feature 044) run the real queue and runner
// against the fake Nomad. A scanPlayer stands in for Grype: it finds each scan
// job the service registers and ends it the way the test says.

const (
	scanSecretSentinel = "s3cret-sentinel-044-svc"
	scanWebDigest      = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
)

// scanPlay says how a scan job ends.
type scanPlay struct {
	// Text is the result file of a scan that exits 0.
	Text string
	// Exit is the task's exit code.
	Exit int
	// Stderr is what the task printed on stderr.
	Stderr string
	// Hold leaves the job running: the test ends it itself.
	Hold bool
}

// playedScan is a scan job the player saw.
type playedScan struct {
	Job, Image, Source string
	// Variable is the job's own variable as it was when the job started.
	Variable map[string]string
	// Env is the scanner task's environment.
	Env map[string]string
}

type scanPlayer struct {
	nomad *deploytest.Nomad
	mu    sync.Mutex
	seen  map[string]bool
	list  []playedScan
	play  func(p playedScan) scanPlay
}

// playScans starts a player that ends every scan job with what play says.
func playScans(t *testing.T, nomad *deploytest.Nomad, play func(p playedScan) scanPlay) *scanPlayer {
	t.Helper()
	p := &scanPlayer{nomad: nomad, seen: map[string]bool{}, play: play}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			p.step()
			time.Sleep(2 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	return p
}

func (p *scanPlayer) step() {
	for _, id := range p.nomad.JobIDs("_scan-") {
		p.mu.Lock()
		if p.seen[id] {
			p.mu.Unlock()
			continue
		}
		p.seen[id] = true
		p.mu.Unlock()
		job := p.nomad.Job(id)
		if job == nil {
			continue
		}
		source := "registry"
		if args, ok := job.TaskGroups[0].Tasks[0].Config["args"].([]any); ok && len(args) > 0 {
			first, _ := args[0].(string)
			switch {
			case first == "db":
				source = "database"
			case strings.HasPrefix(first, "sbom:"):
				source = "sbom"
			}
		}
		pl := playedScan{Job: id, Image: job.Meta["caravana.image"], Source: source, Variable: p.nomad.Variable("nomad/jobs/" + id), Env: job.TaskGroups[0].Tasks[0].Env}
		p.mu.Lock()
		p.list = append(p.list, pl)
		p.mu.Unlock()
		how := p.play(pl)
		p.nomad.StartRun(id)
		if how.Hold {
			continue
		}
		if how.Stderr != "" {
			p.nomad.SetRunLogs(id, nil, []byte(how.Stderr))
		}
		if how.Exit == 0 && how.Text != "" {
			p.nomad.WriteAllocFile(id+"-alloc", nomadjob.ScanResultFile, how.Text)
		}
		p.nomad.FinishRunExit(id, how.Exit)
	}
}

// Played returns the scan jobs seen so far, in order.
func (p *scanPlayer) Played() []playedScan {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]playedScan(nil), p.list...)
}

// PlayedOf returns the scans of one image.
func (p *scanPlayer) PlayedOf(image string) []playedScan {
	var out []playedScan
	for _, s := range p.Played() {
		if s.Image == image {
			out = append(out, s)
		}
	}
	return out
}

// matchLine is one "m" line of a result, as vulns.Template prints it.
func matchLine(advisory, severity, fixState, fixed, pkg, version string) string {
	return strings.Join([]string{"m", advisory, severity, fixState, fixed, pkg, version, "apk",
		"https://security.example/" + advisory, "", "0.5", "0.9", ""}, "\t")
}

// resultText is a whole result file for the lines.
func resultText(lines ...string) string {
	return "caravana-scan v1\n" + strings.Join(append(lines, ""), "\n") +
		"db\t2026-11-19 06:23:17 +0000 UTC\tv6.1.10\t0.117.0\n"
}

var (
	zlibLine    = matchLine("CVE-2022-37434", "Critical", "fixed", "1.2.12-r2", "zlib", "1.2.11-r3")
	busyboxLine = matchLine("CVE-2022-48174", "High", "not-fixed", "", "busybox", "1.33.1-r2")
	opensslLine = matchLine("CVE-2023-0286", "Medium", "fixed", "3.0.8-r0", "openssl", "3.0.7-r0")
)

// everyScanOK ends every scan well with the result text.
func everyScanOK(text string) func(playedScan) scanPlay {
	return func(p playedScan) scanPlay {
		if p.Source == "database" {
			return scanPlay{}
		}
		return scanPlay{Text: text}
	}
}

type scanLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *scanLogBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *scanLogBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newScanEnv is newEnv with a logger the test can read and the scanner's waits
// shortened.
func newScanEnv(t *testing.T) (*env, *scanLogBuffer) {
	t.Helper()
	st, err := store.Open(context.Background(), storetest.Dir(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	logs := &scanLogBuffer{}
	e := &env{store: st, registry: registrytest.New(t), nomad: deploytest.New(t), secrets: secretstest.New()}
	e.registry.AddIndex("traefik/whoami", "latest", whoamiDigest)
	e.registry.AddIndex("acme/web", "1", scanWebDigest)
	e.nomad.Succeed()
	e.svc = startServiceLogging(t, st, newDeployer(t, e.registry, e.nomad), e.secrets,
		slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	setScanTimings(e.svc, 20*time.Minute, 5*time.Millisecond)
	return e, logs
}

func setScanTimings(s *Service, limit, poll time.Duration) {
	s.scan.mu.Lock()
	s.scan.limit, s.scan.poll, s.scan.idle = limit, poll, 50*time.Millisecond
	s.scan.mu.Unlock()
}

// installScanner installs the scanner and waits until it is installed.
func installScanner(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.svc.InstallImageScanner(ctx); err != nil {
		t.Fatalf("InstallImageScanner: %v", err)
	}
	waitFor(t, "the image scanner to be installed", func() bool {
		st, _ := e.svc.scannerState(ctx)
		return st == store.AddonInstalled || st == store.AddonFailed
	})
	if st, _ := e.svc.scannerState(ctx); st != store.AddonInstalled {
		row, _ := e.store.GetAddon(ctx, store.AddonImageScanner)
		t.Fatalf("the install ended %s: %q", st, row.Reason)
	}
}

// deployWhoami deploys the whoami app healthy and returns its record.
func deployApp(t *testing.T, e *env, a app.App) store.DeploymentRecord {
	t.Helper()
	rec, err := e.svc.Deploy(context.Background(), a, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy %s: %v", a.ID, err)
	}
	rec = waitFinal(t, e.store, rec.Deployment.ID)
	if rec.Status != store.StatusHealthy {
		t.Fatalf("deployment of %s ended %s: %s", a.ID, rec.Status, rec.Error)
	}
	return rec
}

func webApp(id string) app.App {
	return app.App{ID: id, Name: id, Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{
		Name: "web", Image: "acme/web:1", Ports: []app.Port{{Name: "http", ContainerPort: 80}},
		Domains: []app.Domain{{Host: id + ".localhost", Port: "http"}}, Health: &app.HealthCheck{Path: "/"},
	}}}
}

// waitRun waits for the newest run of image to end and returns it.
func waitRun(t *testing.T, e *env, image string, n int) store.ScanRun {
	t.Helper()
	var got store.ScanRun
	waitFor(t, fmt.Sprintf("scan run %d of %s to end", n, image), func() bool {
		runs, _ := e.store.ListScanRuns(context.Background(), 0)
		var mine []store.ScanRun
		for i := len(runs) - 1; i >= 0; i-- { // oldest first
			if runs[i].Image == image {
				mine = append(mine, runs[i])
			}
		}
		if len(mine) < n || mine[n-1].Status == store.ScanQueued || mine[n-1].Status == store.ScanRunning {
			return false
		}
		got = mine[n-1]
		return true
	})
	return got
}

func TestNothingHappensWhileTheScannerIsNotInstalled(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	deployApp(t, e, whoami())

	if _, err := e.svc.queueScan(ctx, "x@sha256:aa", store.ScanTriggerOwner); !errors.Is(err, ErrScannerNotInstalled) {
		t.Errorf("queueScan = %v, want ErrScannerNotInstalled", err)
	}
	if _, err := e.svc.RequestScan(ctx, ""); !errors.Is(err, ErrScannerNotInstalled) {
		t.Errorf("RequestScan = %v, want ErrScannerNotInstalled", err)
	}
	if err := e.svc.RunDailyScansForTests(ctx); !errors.Is(err, ErrScannerNotInstalled) {
		t.Errorf("RunDailyScansForTests = %v, want ErrScannerNotInstalled", err)
	}
	if _, err := e.svc.SetScanSettings(ctx, ScanSettings{NotifyLevel: "critical", DailyHour: 4, RefuseFixableCritical: true}); !errors.Is(err, ErrScannerNotInstalled) {
		t.Errorf("turning the gate on = %v, want ErrScannerNotInstalled", err)
	}
	// The other settings can be kept while it isn't installed.
	if _, err := e.svc.SetScanSettings(ctx, ScanSettings{NotifyLevel: "high", DailyHour: 6}); err != nil {
		t.Errorf("saving the other settings: %v", err)
	}
	time.Sleep(60 * time.Millisecond) // let the runner look
	runs, _ := e.store.ListScanRuns(ctx, 0)
	if len(runs) != 0 || len(e.nomad.JobIDs("_scan-")) != 0 || len(e.nomad.VolumeCreates()) != 0 {
		t.Errorf("runs = %d, scan jobs = %v, volumes = %v; want none", len(runs), e.nomad.JobIDs("_scan-"), e.nomad.VolumeCreates())
	}
	if v, err := e.svc.AppScan(ctx, "whoami"); err != nil || v.Installed || len(v.Services) != 0 {
		t.Errorf("AppScan = %+v, %v", v, err)
	}
	if list, err := e.svc.ImageScans(ctx); err != nil || len(list) != 0 {
		t.Errorf("ImageScans = %+v, %v", list, err)
	}
	addons, err := e.svc.Addons(ctx)
	if err != nil || len(addons) != 1 || addons[0].State != AddonStateAvailable || addons[0].Name != "image-scanner" || addons[0].DiskBytes == 0 {
		t.Errorf("Addons = %+v, %v", addons, err)
	}
	if _, err := e.svc.Addon(ctx, "beszel"); !errors.Is(err, ErrAddonNotFound) {
		t.Errorf("Addon(beszel) = %v, want ErrAddonNotFound", err)
	}
	if _, err := e.svc.InstallAddon(ctx, "beszel"); !errors.Is(err, ErrAddonNotFound) {
		t.Errorf("InstallAddon(beszel) = %v", err)
	}
}

func TestInstallScansEveryLiveImage(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	rec := deployApp(t, e, whoami())
	image := rec.Deployment.Digests["web"]
	player := playScans(t, e.nomad, everyScanOK(resultText(zlibLine, busyboxLine, opensslLine)))

	installScanner(t, e)
	waitRun(t, e, image, 1)

	played := player.Played()
	if len(played) != 2 || played[0].Source != "database" || played[1].Source != "registry" || played[1].Image != image {
		t.Fatalf("scans played = %+v; want the database run, then a registry scan of %s", played, image)
	}
	if got := len(e.nomad.VolumeCreates()); got != 1 {
		t.Errorf("volumes created = %d, want 1", got)
	}
	runs, _ := e.store.ListScanRuns(ctx, 0)
	if len(runs) != 2 || runs[1].Trigger != store.ScanTriggerInstall || runs[1].Source != store.ScanSourceDatabase || runs[0].Trigger != store.ScanTriggerInstall || runs[0].Image != image {
		t.Errorf("runs = %+v", runs)
	}
	for _, r := range runs {
		if r.Status != store.ScanOK {
			t.Errorf("run %s ended %s: %s", r.ID, r.Status, r.Reason)
		}
	}
	view, err := e.svc.AppScan(ctx, "whoami")
	if err != nil || !view.Installed || len(view.Services) != 1 {
		t.Fatalf("AppScan = %+v, %v", view, err)
	}
	svc := view.Services[0]
	if svc.Service != "web" || svc.Image != image || svc.Scan == nil || svc.Scan.Counts.Critical != 1 || svc.Scan.Counts.High != 1 ||
		svc.Scan.Counts.Medium != 1 || svc.Scan.Counts.FixableCritical != 1 || !svc.Scan.SBOMKept || svc.Scan.Scanner != "0.117.0" || len(svc.Scan.Findings) != 3 {
		t.Errorf("service scan = %+v", svc)
	}
	if f := svc.Scan.Findings[0]; f.Package != "zlib" || !f.Fixable || f.Remediation == "" || f.FirstSeen.IsZero() {
		t.Errorf("first finding = %+v", f)
	}
	if svc.Open != nil {
		t.Errorf("open run = %+v", svc.Open)
	}

	// The job purged after reading, and the install's database date shows.
	if len(e.nomad.JobIDs("_scan-")) != 0 {
		t.Errorf("scan jobs left in Nomad: %v", e.nomad.JobIDs("_scan-"))
	}
	addon, _ := e.svc.Addon(ctx, "image-scanner")
	if addon.State != "installed" || addon.InstalledAt.IsZero() || addon.DatabaseBuilt.IsZero() {
		t.Errorf("add-on = %+v", addon)
	}
	list, err := e.svc.ImageScans(ctx)
	if err != nil || len(list) != 1 || list[0].Image != image || len(list[0].UsedBy) != 1 || list[0].UsedBy[0].App != "whoami" ||
		list[0].Scan == nil || list[0].Scan.Counts.Critical != 1 {
		t.Errorf("ImageScans = %+v, %v", list, err)
	}
	if got, _ := e.svc.ScanRuns(ctx, 10); len(got) != 2 {
		t.Errorf("ScanRuns = %+v", got)
	}
}

func TestADeployThatEndsHealthyQueuesItsDigestOnce(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	player := playScans(t, e.nomad, everyScanOK(resultText(zlibLine)))
	installScanner(t, e)

	rec := deployApp(t, e, whoami())
	image := rec.Deployment.Digests["web"]
	if r := waitRun(t, e, image, 1); r.Trigger != store.ScanTriggerDeploy || r.Status != store.ScanOK {
		t.Fatalf("run = %+v", r)
	}
	// The same image deployed again within a day is not scanned again.
	deployApp(t, e, whoami())
	time.Sleep(80 * time.Millisecond)
	if n := len(player.PlayedOf(image)); n != 1 {
		t.Errorf("scans of the unchanged image = %d, want 1", n)
	}
	// A new image is.
	web := deployApp(t, e, webApp("shop"))
	waitRun(t, e, web.Deployment.Digests["web"], 1)
	if n := len(player.PlayedOf(web.Deployment.Digests["web"])); n != 1 {
		t.Errorf("scans of the new image = %d, want 1", n)
	}
	// The same image in a second app is shared: one scan, shown on both.
	deployApp(t, e, webApp("shop2"))
	time.Sleep(80 * time.Millisecond)
	if n := len(player.PlayedOf(web.Deployment.Digests["web"])); n != 1 {
		t.Errorf("scans of an image two apps run = %d, want 1", n)
	}
	list, _ := e.svc.ImageScans(ctx)
	for _, sum := range list {
		if sum.Image == web.Deployment.Digests["web"] && len(sum.UsedBy) != 2 {
			t.Errorf("usedBy = %+v, want both apps", sum.UsedBy)
		}
	}
	// A result older than a day is scanned again by the next deploy.
	e.svc.clock.(*fakeClock).Set(time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC))
	deployApp(t, e, whoami())
	waitRun(t, e, image, 2)
}

func TestOneScanRunsAtATime(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	rec := deployApp(t, e, whoami())
	whoamiImage := rec.Deployment.Digests["web"]
	web := deployApp(t, e, webApp("shop"))
	webImage := web.Deployment.Digests["web"]
	player := playScans(t, e.nomad, func(p playedScan) scanPlay {
		if p.Source == "database" {
			return scanPlay{}
		}
		return scanPlay{Hold: true}
	})
	installScanner(t, e)

	waitFor(t, "the first scan to start", func() bool { return len(player.PlayedOf(whoamiImage))+len(player.PlayedOf(webImage)) == 1 })
	time.Sleep(60 * time.Millisecond)
	if n := len(e.nomad.JobIDs("_scan-")); n != 1 {
		t.Fatalf("scan jobs running at once = %d, want 1", n)
	}
	open, _ := e.store.OpenScanRuns(ctx)
	if len(open) != 2 || open[0].Status != store.ScanRunning || open[1].Status != store.ScanQueued {
		t.Fatalf("open runs = %+v, want one running and one queued", open)
	}
	// Ending the first starts the second.
	first := e.nomad.JobIDs("_scan-")[0]
	e.nomad.WriteAllocFile(first+"-alloc", nomadjob.ScanResultFile, resultText(zlibLine))
	e.nomad.FinishRunExit(first, 0)
	waitFor(t, "the second scan to start", func() bool { return len(player.Played()) == 3 })
	if n := len(e.nomad.JobIDs("_scan-")); n != 1 {
		t.Errorf("scan jobs after the first ended = %d, want only the second", n)
	}
}

func TestAnotherScanThatFindsANewAdvisoryKeepsFirstSeen(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	rec := deployApp(t, e, whoami())
	image := rec.Deployment.Digests["web"]
	var text atomic.Value
	text.Store(resultText(zlibLine, busyboxLine))
	playScans(t, e.nomad, func(p playedScan) scanPlay {
		if p.Source == "database" {
			return scanPlay{}
		}
		return scanPlay{Text: text.Load().(string)}
	})
	installScanner(t, e)
	waitRun(t, e, image, 1)

	day1 := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)
	first, _ := e.store.ImageFindings(ctx, image)
	if len(first) != 2 {
		t.Fatalf("findings = %d", len(first))
	}
	e.svc.clock.(*fakeClock).Set(day2)
	text.Store(resultText(zlibLine, opensslLine)) // busybox went, openssl is new
	if _, err := e.svc.RequestScan(ctx, "whoami"); err != nil {
		t.Fatal(err)
	}
	waitRun(t, e, image, 2)
	got, _ := e.store.ImageFindings(ctx, image)
	byPkg := map[string]store.StoredFinding{}
	for _, f := range got {
		byPkg[f.Package] = f
	}
	if _, ok := byPkg["busybox"]; ok || len(got) != 2 {
		t.Errorf("findings after the second scan = %+v", got)
	}
	if f := byPkg["zlib"]; !f.FirstSeen.Equal(day1) || !f.LastSeen.Equal(day2) {
		t.Errorf("zlib seen %v .. %v, want %v .. %v", f.FirstSeen, f.LastSeen, day1, day2)
	}
	if f := byPkg["openssl"]; !f.FirstSeen.Equal(day2) {
		t.Errorf("the new advisory first seen %v, want %v", f.FirstSeen, day2)
	}
}

func TestAFailedScanKeepsThePreviousFindingsAndSaysWhy(t *testing.T) {
	e, logs := newScanEnv(t)
	ctx := context.Background()
	rec := deployApp(t, e, whoami())
	image := rec.Deployment.Digests["web"]
	var fail atomic.Bool
	playScans(t, e.nomad, func(p playedScan) scanPlay {
		switch {
		case p.Source == "database":
			return scanPlay{}
		case fail.Load():
			return scanPlay{Exit: 1, Stderr: "[0003] ERROR failed to catalog: GET https://index.docker.io/v2/x/y: UNAUTHORIZED: authentication required " + scanSecretSentinel + "\n"}
		}
		return scanPlay{Text: resultText(zlibLine)}
	})
	installScanner(t, e)
	waitRun(t, e, image, 1)

	fail.Store(true)
	if _, err := e.svc.RequestScan(ctx, "whoami"); err != nil {
		t.Fatal(err)
	}
	run := waitRun(t, e, image, 2)
	if run.Status != store.ScanFailed || run.Reason != "the registry refused the credentials" {
		t.Fatalf("run = %+v", run)
	}
	view, _ := e.svc.AppScan(ctx, "whoami")
	scan := view.Services[0].Scan
	if scan.Status != store.ScanFailed || scan.Reason != run.Reason || scan.Counts.Critical != 1 || len(scan.Findings) != 1 || scan.ScannedAt.IsZero() {
		t.Errorf("after a failed scan: %+v", scan.ImageScan)
	}
	assertNoSentinel(t, e, logs, scanSecretSentinel)
}

// assertNoSentinel looks for text in the log, the runs, the scans and the findings.
func assertNoSentinel(t *testing.T, e *env, logs *scanLogBuffer, text string) {
	t.Helper()
	ctx := context.Background()
	if strings.Contains(logs.String(), text) {
		t.Errorf("the log holds %q", text)
	}
	runs, _ := e.store.ListScanRuns(ctx, 0)
	scans, _ := e.store.ListImageScans(ctx)
	all, _ := json.Marshal([]any{runs, scans})
	for _, sc := range scans {
		f, _ := e.store.ImageFindings(ctx, sc.Image)
		b, _ := json.Marshal(f)
		all = append(all, b...)
	}
	if strings.Contains(string(all), text) {
		t.Errorf("the store's scan rows hold %q", text)
	}
}

func TestEachFailureClassHasItsWords(t *testing.T) {
	cases := []struct {
		name, stderr, want string
		exit               int
	}{
		{"database", "[0002] ERROR failed to load vulnerability db: database does not exist\n", "the vulnerability database couldn't be downloaded or loaded", 1},
		{"old database", "[0002] ERROR failed to load vulnerability db: the vulnerability database was built 10 hours ago (max allowed age is 1 hour)\n", "the vulnerability database is more than 5 days old", 1},
		{"image", "[0004] ERROR failed to catalog: MANIFEST_UNKNOWN: manifest unknown\n", "the image couldn't be read from its registry", 1},
		{"no class", "[0004] ERROR nobody foresaw this\n", "the scanner stopped without a result", 1},
		{"exit 0 without a file", "", "the scanner stopped without a result", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := newScanEnv(t)
			rec := deployApp(t, e, whoami())
			image := rec.Deployment.Digests["web"]
			playScans(t, e.nomad, func(p playedScan) scanPlay {
				if p.Source == "database" {
					return scanPlay{}
				}
				return scanPlay{Exit: c.exit, Stderr: c.stderr}
			})
			installScanner(t, e)
			if run := waitRun(t, e, image, 1); run.Status != store.ScanFailed || run.Reason != c.want {
				t.Errorf("run = %+v, want failed with %q", run, c.want)
			}
		})
	}
}

func TestAScanThatRunsTooLongIsStopped(t *testing.T) {
	e, _ := newScanEnv(t)
	rec := deployApp(t, e, whoami())
	image := rec.Deployment.Digests["web"]
	player := playScans(t, e.nomad, func(p playedScan) scanPlay {
		if p.Source == "database" {
			return scanPlay{}
		}
		return scanPlay{Hold: true}
	})
	setScanTimings(e.svc, 80*time.Millisecond, 5*time.Millisecond)
	installScanner(t, e)
	run := waitRun(t, e, image, 1)
	if run.Status != store.ScanFailed || run.Reason != "took longer than 20 minutes" {
		t.Fatalf("run = %+v", run)
	}
	job := player.PlayedOf(image)[0].Job
	if e.nomad.Stops(job) != 1 || !e.nomad.Purged(job) {
		t.Errorf("the job: stops %d, purged %v; want stopped, then purged", e.nomad.Stops(job), e.nomad.Purged(job))
	}
}

func TestDatabaseRunFailureFailsTheInstallAndItCanBeTriedAgain(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	var failing atomic.Bool
	failing.Store(true)
	playScans(t, e.nomad, func(p playedScan) scanPlay {
		if p.Source == "database" && failing.Load() {
			return scanPlay{Exit: 1, Stderr: "[0002] ERROR failed to load vulnerability db: database does not exist\n"}
		}
		return scanPlay{}
	})
	if _, err := e.svc.InstallImageScanner(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the install to fail", func() bool {
		st, _ := e.svc.scannerState(ctx)
		return st == store.AddonFailed
	})
	row, _ := e.store.GetAddon(ctx, store.AddonImageScanner)
	if row.Reason != "the vulnerability database couldn't be downloaded or loaded" {
		t.Errorf("reason = %q", row.Reason)
	}
	failing.Store(false)
	installScanner(t, e) // tries again
}

func TestInstallingTwiceIsBusyAndInstalledIsLeftAlone(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	var hold atomic.Bool
	hold.Store(true)
	playScans(t, e.nomad, func(p playedScan) scanPlay { return scanPlay{Hold: hold.Load()} })
	if _, err := e.svc.InstallImageScanner(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.InstallImageScanner(ctx); !errors.Is(err, ErrAddonBusy) {
		t.Errorf("a second install = %v, want ErrAddonBusy", err)
	}
	if _, err := e.svc.RemoveImageScanner(ctx); !errors.Is(err, ErrAddonBusy) {
		t.Errorf("remove while installing = %v, want ErrAddonBusy", err)
	}
	waitFor(t, "the database run", func() bool { return len(e.nomad.JobIDs("_scan-")) == 1 })
	id := e.nomad.JobIDs("_scan-")[0]
	hold.Store(false)
	e.nomad.FinishRunExit(id, 0)
	waitFor(t, "the install to end", func() bool {
		st, _ := e.svc.scannerState(ctx)
		return st == store.AddonInstalled
	})
	view, err := e.svc.InstallImageScanner(ctx)
	if err != nil || view.State != "installed" {
		t.Errorf("installing an installed add-on = %+v, %v", view, err)
	}
}

func TestDailyRescansFromTheKeptSBOMAndFallsBackToTheRegistry(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	rec := deployApp(t, e, whoami())
	image := rec.Deployment.Digests["web"]
	var sbomMissing atomic.Bool
	player := playScans(t, e.nomad, func(p playedScan) scanPlay {
		switch {
		case p.Source == "database":
			return scanPlay{}
		case p.Source == "sbom" && sbomMissing.Load():
			return scanPlay{Exit: 1, Stderr: "[0003] ERROR failed to catalog: unable to open file /cache/sbom/x.cdx.json: no such file or directory\n"}
		}
		return scanPlay{Text: resultText(zlibLine)}
	})
	installScanner(t, e)
	waitRun(t, e, image, 1)

	if err := e.svc.RunDailyScansForTests(ctx); err != nil {
		t.Fatal(err)
	}
	if r := waitRun(t, e, image, 2); r.Status != store.ScanOK || r.Trigger != store.ScanTriggerDaily || r.Source != store.ScanSourceSBOM {
		t.Fatalf("the daily run = %+v, want an ok SBOM rescan", r)
	}
	played := player.PlayedOf(image)
	if len(played) != 2 || played[1].Source != "sbom" || len(played[1].Variable) != 0 {
		t.Errorf("played = %+v; the rescan reads the SBOM and no variable", played)
	}

	sbomMissing.Store(true)
	if err := e.svc.RunDailyScansForTests(ctx); err != nil {
		t.Fatal(err)
	}
	if r := waitRun(t, e, image, 3); r.Status != store.ScanFailed || r.Source != store.ScanSourceSBOM {
		t.Fatalf("the rescan with no SBOM = %+v", r)
	}
	r4 := waitRun(t, e, image, 4)
	if r4.Status != store.ScanOK || r4.Source != store.ScanSourceRegistry || r4.Trigger != store.ScanTriggerDaily {
		t.Errorf("the fallback = %+v, want an ok registry scan", r4)
	}
	if scan, _ := e.store.GetImageScan(ctx, image); !scan.SBOMKept {
		t.Error("the registry scan didn't keep the SBOM again")
	}

	// The pass writes the day, and a stopped app isn't rescanned.
	set, _ := e.store.GetScanSettings(ctx)
	if set.LastDaily.IsZero() {
		t.Error("last_daily wasn't written")
	}
	if _, err := e.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
		t.Fatalf("StopApp: %v", err)
	}
	before := len(player.Played())
	if err := e.svc.RunDailyScansForTests(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if n := len(player.Played()); n != before {
		t.Errorf("a stopped app was rescanned (%d scans before, %d after)", before, n)
	}
}

func TestDropFindingForTestsLetsTheDailyRescanFindItAgain(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	rec := deployApp(t, e, whoami())
	image := rec.Deployment.Digests["web"]
	playScans(t, e.nomad, everyScanOK(resultText(zlibLine)))
	installScanner(t, e)
	waitRun(t, e, image, 1)

	before, err := e.store.ImageFindings(ctx, image)
	if err != nil || len(before) == 0 {
		t.Fatalf("findings after the first scan = %d (%v), want some", len(before), err)
	}
	advisory := before[0].Advisory
	if n, err := e.svc.DropFindingForTests(ctx, image, advisory); err != nil || n != 1 {
		t.Fatalf("DropFindingForTests = %d, %v, want 1", n, err)
	}
	if left, _ := e.store.ImageFindings(ctx, image); len(left) != len(before)-1 {
		t.Fatalf("after the drop %d findings are left, want %d", len(left), len(before)-1)
	}

	e.svc.clock.(*fakeClock).Set(e.svc.clock.Now().Add(24 * time.Hour)) // the next day
	if err := e.svc.RunDailyScansForTests(ctx); err != nil {
		t.Fatal(err)
	}
	waitRun(t, e, image, 2)
	after, _ := e.store.ImageFindings(ctx, image)
	var back *store.StoredFinding
	for i := range after {
		if after[i].Advisory == advisory {
			back = &after[i]
		}
	}
	if back == nil {
		t.Fatalf("the daily rescan didn't find %s again: %+v", advisory, after)
	}
	if !back.FirstSeen.After(before[0].FirstSeen) {
		t.Errorf("the finding's first_seen = %v, want after the first scan's %v (it is new again)", back.FirstSeen, before[0].FirstSeen)
	}
}

func TestPrivateRegistryCredentialsReachOnlyTheScanJobsVariable(t *testing.T) {
	e, logs := newScanEnv(t)
	ctx := context.Background()
	const host, image = "registry.example.com:5000", "registry.example.com:5000/team/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := e.svc.SaveRegistry(ctx, host, "deploy", new(scanSecretSentinel)); err != nil {
		t.Fatalf("SaveRegistry: %v", err)
	}
	player := playScans(t, e.nomad, everyScanOK(resultText(zlibLine)))
	installScanner(t, e)
	run, err := e.svc.queueScan(ctx, image, store.ScanTriggerOwner)
	if err != nil {
		t.Fatal(err)
	}
	done, err := e.svc.ScanWait(ctx, run.ID)
	if err != nil || done.Status != store.ScanOK {
		t.Fatalf("scan = %+v, %v", done, err)
	}
	played := player.PlayedOf(image)
	if len(played) != 1 || played[0].Variable["GRYPE_REGISTRY_AUTH_PASSWORD"] != scanSecretSentinel || played[0].Variable["GRYPE_REGISTRY_AUTH_USERNAME"] != "deploy" {
		t.Fatalf("the job's variable = %v", played)
	}
	// Gone with the job, and nowhere else.
	if e.nomad.Variable("nomad/jobs/"+played[0].Job) != nil {
		t.Error("the credentials stayed in Nomad after the scan")
	}
	assertNoSentinel(t, e, logs, scanSecretSentinel)
	views, _ := json.Marshal(mustImageScans(t, e))
	if strings.Contains(string(views), scanSecretSentinel) {
		t.Error("ImageScans holds the password")
	}
}

func mustImageScans(t *testing.T, e *env) []ImageScanSummary {
	t.Helper()
	list, err := e.svc.ImageScans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestRemovingTheScannerEmptiesTheQueueStopsTheScanAndForgets(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	deployApp(t, e, whoami())
	deployApp(t, e, webApp("shop"))
	player := playScans(t, e.nomad, func(p playedScan) scanPlay {
		if p.Source == "database" {
			return scanPlay{}
		}
		return scanPlay{Hold: true}
	})
	installScanner(t, e)
	waitFor(t, "a scan to be running", func() bool { return len(player.Played()) == 2 })

	if _, err := e.svc.RemoveImageScanner(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the removal to end", func() bool {
		_, err := e.store.GetAddon(ctx, store.AddonImageScanner)
		return errors.Is(err, store.ErrNotFound)
	})
	played := player.Played()
	job := played[len(played)-1].Job
	if !e.nomad.Purged(job) || e.nomad.Stops(job) != 1 {
		t.Errorf("the running scan: purged %v, stops %d; want stopped and purged", e.nomad.Purged(job), e.nomad.Stops(job))
	}
	if len(e.nomad.HostVolumes()) != 0 {
		t.Errorf("volumes after the removal = %v", e.nomad.HostVolumes())
	}
	runs, _ := e.store.ListScanRuns(ctx, 0)
	scans, _ := e.store.ListImageScans(ctx)
	if len(runs) != 0 || len(scans) != 0 {
		t.Errorf("runs = %+v, scans = %+v; want everything forgotten", runs, scans)
	}
	if v, _ := e.svc.Addon(ctx, "image-scanner"); v.State != AddonStateAvailable {
		t.Errorf("add-on after the removal = %+v", v)
	}
	if _, err := e.svc.queueScan(ctx, "x@sha256:aa", store.ScanTriggerOwner); !errors.Is(err, ErrScannerNotInstalled) {
		t.Errorf("queueScan after the removal = %v", err)
	}
	// Removing what isn't installed is done.
	if v, err := e.svc.RemoveImageScanner(ctx); err != nil || v.State != AddonStateAvailable {
		t.Errorf("removing again = %+v, %v", v, err)
	}
	// And it can be installed again.
	installScanner(t, e)
}

func TestRestartRecovery(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	st := e.store
	now := time.Now().UTC()
	_ = st.SaveAddon(ctx, store.Addon{Name: store.AddonImageScanner, State: store.AddonInstalling, Version: "0.117.0"})
	for _, id := range []string{"running1", "queued1"} {
		if _, _, err := st.QueueScanRun(ctx, store.ScanRun{ID: id, Image: "img-" + id, Trigger: store.ScanTriggerDeploy, Source: store.ScanSourceRegistry}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.StartScanRun(ctx, "running1", store.ScanSourceRegistry, now); err != nil {
		t.Fatal(err)
	}
	job := nomadjob.ScanJob("running1", nomadjob.ScanSource{Image: "img-running1"}, nomadjob.ScanOptions{}, nomadjob.Settings{}, nil)
	if err := e.svc.deployer.StartScan(ctx, job, nil); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.recoverScans(ctx); err != nil {
		t.Fatal(err)
	}
	r1, _ := st.GetScanRun(ctx, "running1")
	q1, _ := st.GetScanRun(ctx, "queued1")
	if r1.Status != store.ScanFailed || r1.Reason != "Caravana restarted during the scan" || q1.Status != store.ScanQueued {
		t.Errorf("running = %+v, queued = %+v", r1, q1)
	}
	if !e.nomad.Purged("_scan-running1") {
		t.Error("the scan's job wasn't purged")
	}
	if scan, _ := st.GetImageScan(ctx, "img-running1"); scan.Status != store.ScanFailed {
		t.Errorf("image row = %+v", scan)
	}
	row, _ := st.GetAddon(ctx, store.AddonImageScanner)
	if row.State != store.AddonFailed || row.Reason != "Caravana restarted during the install" {
		t.Errorf("add-on = %+v", row)
	}
	_ = st.SaveAddon(ctx, store.Addon{Name: store.AddonImageScanner, State: store.AddonRemoving, Version: "0.117.0"})
	if err := e.svc.recoverScans(ctx); err != nil {
		t.Fatal(err)
	}
	if row, _ := st.GetAddon(ctx, store.AddonImageScanner); row.State != store.AddonFailed || row.Reason != "Caravana restarted during the removal" {
		t.Errorf("add-on after a removal was cut = %+v", row)
	}
}

func TestPruneKeepsTheDigestsAppsRunAndScansInFlight(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	rec := deployApp(t, e, whoami())
	live := rec.Deployment.Digests["web"]
	var hold atomic.Bool
	player := playScans(t, e.nomad, func(p playedScan) scanPlay {
		if p.Source == "database" {
			return scanPlay{}
		}
		return scanPlay{Text: resultText(zlibLine), Hold: hold.Load()}
	})
	installScanner(t, e)
	waitRun(t, e, live, 1)

	at := time.Now().UTC()
	const old, inflight = "old@sha256:1", "inflight@sha256:2"
	for _, img := range []string{old, inflight} {
		if err := e.store.SaveScanResult(ctx, img, scanResultOf(zlibLine), true, at); err != nil {
			t.Fatal(err)
		}
	}
	// A scan running for an image nobody runs keeps its stored result.
	hold.Store(true)
	run, err := e.svc.queueScan(ctx, inflight, store.ScanTriggerOwner)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the scan to start", func() bool { return len(player.PlayedOf(inflight)) == 1 })
	e.svc.pruneScans(ctx)
	scans, _ := e.store.ListImageScans(ctx)
	var got []string
	for _, s := range scans {
		got = append(got, s.Image)
	}
	if len(got) != 2 || got[0] != inflight || got[1] != live {
		t.Errorf("scans kept = %v, want the live image and the one being scanned", got)
	}
	// Once it ends, and the only app that runs the live image is deleted, nothing is kept.
	e.nomad.FinishRunExit(player.PlayedOf(inflight)[0].Job, 1)
	if _, err := e.svc.ScanWait(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteApp(ctx, "whoami"); err != nil {
		t.Fatal(err)
	}
	if scans, _ := e.store.ListImageScans(ctx); len(scans) != 0 {
		t.Errorf("scans after deleting the app = %+v", scans)
	}
}

func TestSettingsAreChecked(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		in    ScanSettings
		field string
	}{
		{"notify level", ScanSettings{NotifyLevel: "low", DailyHour: 4}, "notifyLevel"},
		{"hour", ScanSettings{NotifyLevel: "high", DailyHour: 24}, "dailyHour"},
		{"negative hour", ScanSettings{NotifyLevel: "high", DailyHour: -1}, "dailyHour"},
		{"http", ScanSettings{NotifyLevel: "high", DailyHour: 4, DatabaseURL: "http://mirror.example/latest.json"}, "databaseUrl"},
		{"no host", ScanSettings{NotifyLevel: "high", DailyHour: 4, DatabaseURL: "https://"}, "databaseUrl"},
		{"credentials", ScanSettings{NotifyLevel: "high", DailyHour: 4, DatabaseURL: "https://u:p@mirror.example/latest.json"}, "databaseUrl"},
	} {
		_, err := e.svc.SetScanSettings(ctx, c.in)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: error = %v, want a field error on %s", c.name, err, c.field)
		}
	}
	got, err := e.svc.SetScanSettings(ctx, ScanSettings{NotifyLevel: "high", DailyHour: 23, DatabaseURL: " https://mirror.example/latest.json "})
	if err != nil || got.DatabaseURL != "https://mirror.example/latest.json" {
		t.Fatalf("SetScanSettings = %+v, %v", got, err)
	}
	if read, _ := e.svc.ScanSettings(ctx); read != got {
		t.Errorf("read back %+v, want %+v", read, got)
	}
	// The mirror goes to the scanner job.
	rec := deployApp(t, e, whoami())
	player := playScans(t, e.nomad, everyScanOK(resultText(zlibLine)))
	installScanner(t, e)
	waitRun(t, e, rec.Deployment.Digests["web"], 1)
	played := player.Played()
	if len(played) != 2 || played[0].Env["GRYPE_DB_UPDATE_URL"] != "https://mirror.example/latest.json" || played[1].Env["GRYPE_DB_UPDATE_URL"] != "https://mirror.example/latest.json" {
		t.Errorf("the database URL didn't reach the scan jobs: %+v", played)
	}
}

func TestRequestScanOfAnAppWithNoLiveDeployment(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	playScans(t, e.nomad, everyScanOK(resultText()))
	installScanner(t, e)
	if _, err := e.svc.RequestScan(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RequestScan of no app = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "empty", Name: "empty", Source: app.Source{Kind: app.SourceImage}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RequestScan(ctx, "empty"); !errors.Is(err, ErrNoLiveDeployment) {
		t.Errorf("RequestScan of an app never deployed = %v, want ErrNoLiveDeployment", err)
	}
}

func TestDailyDue(t *testing.T) {
	at := func(day, hour int) time.Time { return time.Date(2026, 11, day, hour, 30, 0, 0, time.UTC) }
	for _, c := range []struct {
		name string
		now  time.Time
		hour int
		last time.Time
		want bool
	}{
		{"before the hour", at(19, 3), 4, time.Time{}, false},
		{"at the hour, never ran", at(19, 4), 4, time.Time{}, true},
		{"after the hour, ran yesterday", at(19, 9), 4, at(18, 4), true},
		{"after the hour, ran today", at(19, 9), 4, at(19, 4), false},
		{"hour 0 is always past", at(19, 0), 0, at(18, 0), true},
		{"the day is the UTC day, whatever the zone", time.Date(2026, 11, 19, 2, 0, 0, 0, time.FixedZone("x", 5*3600)), 4, at(18, 4), false},
	} {
		if got := dailyDue(c.now, c.hour, c.last); got != c.want {
			t.Errorf("%s: dailyDue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRoomReason(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	host := func(age time.Duration, total, reserved int64) *store.HostSample {
		return &store.HostSample{Node: "n", TS: now.Add(-age), MemoryTotalMB: &total, MemoryReservedMB: &reserved}
	}
	disk := func(holds string, freeMB int64) []store.DiskSample {
		return []store.DiskSample{{Node: "n", Mount: "/", Holds: holds, FreeMB: freeMB}}
	}
	for _, c := range []struct {
		name  string
		h     *store.HostSample
		disks []store.DiskSample
		want  string
	}{
		{"room", host(time.Minute, 4096, 2000), disk("nomad", 20000), ""},
		{"no sample", nil, nil, ""},
		{"stale sample", host(10*time.Minute, 1000, 900), disk("nomad", 100), ""},
		{"memory", host(time.Minute, 1000, 700), nil, "not enough free memory on server lab (300 MB, the scan needs 512 MB)"},
		{"memory over-reserved", host(time.Minute, 1000, 1200), nil, "not enough free memory on server lab (0 MB, the scan needs 512 MB)"},
		{"disk", host(time.Minute, 4096, 0), disk("nomad", 2048), "not enough free disk on server lab (2.0 GB free, the scanner's database needs 4 GB)"},
		{"disk of docker only", host(time.Minute, 4096, 0), disk("docker", 100), ""},
		{"disk of both", host(time.Minute, 4096, 0), disk("nomad,docker", 100), "not enough free disk on server lab (0.1 GB free, the scanner's database needs 4 GB)"},
		{"no memory numbers", &store.HostSample{TS: now}, disk("nomad", 10000), ""},
	} {
		if got := roomReason("lab", c.h, c.disks, now); got != c.want {
			t.Errorf("%s: roomReason = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAScanIsSkippedWithoutRoom(t *testing.T) {
	for _, c := range []struct {
		name  string
		write func(at time.Time) store.MetricsRows
		want  string
	}{
		{"memory", func(at time.Time) store.MetricsRows {
			return store.MetricsRows{Hosts: []store.HostSample{{Node: "node-1", TS: at, MemoryTotalMB: new(int64(1000)), MemoryReservedMB: new(int64(800))}}}
		}, "not enough free memory on server lab (200 MB, the scan needs 512 MB)"},
		{"disk", func(at time.Time) store.MetricsRows {
			return store.MetricsRows{
				Hosts: []store.HostSample{{Node: "node-1", TS: at, MemoryTotalMB: new(int64(8000)), MemoryReservedMB: new(int64(100))}},
				Disks: []store.DiskSample{{Node: "node-1", TS: at, Mount: "/", Holds: "nomad", FreeMB: 1024, TotalMB: 50000}},
			}
		}, "not enough free disk on server lab (1.0 GB free, the scanner's database needs 4 GB)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _ := newScanEnv(t)
			ctx := context.Background()
			e.nomad.AddNode("node-1", "lab", "ready", "10.99.0.1")
			rec := deployApp(t, e, whoami())
			image := rec.Deployment.Digests["web"]
			player := playScans(t, e.nomad, everyScanOK(resultText(zlibLine)))
			at := e.svc.clock.Now().UTC().Truncate(time.Minute)
			// The install's own check sees the sample too, so write it after.
			installScanner(t, e)
			waitRun(t, e, image, 1)
			before := len(player.Played())
			if err := e.store.WriteMetricsTick(ctx, store.MetricsTick{At: at, Minute: c.write(at)}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.RequestScan(ctx, "whoami"); err != nil {
				t.Fatal(err)
			}
			run := waitRun(t, e, image, 2)
			if run.Status != store.ScanSkipped || run.Reason != c.want {
				t.Fatalf("run = %+v, want skipped with %q", run, c.want)
			}
			if n := len(player.Played()); n != before {
				t.Errorf("a skipped scan started a job (%d before, %d after)", before, n)
			}
			scan, _ := e.store.GetImageScan(ctx, image)
			if scan.Status != store.ScanSkipped || scan.Reason != c.want || scan.Counts.Critical != 1 {
				t.Errorf("image scan = %+v; the skip is recorded and the findings stay", scan)
			}
			// Room again: the next one runs.
			e.svc.clock.(*fakeClock).Set(at.Add(10 * time.Minute)) // the sample is stale now
			if _, err := e.svc.RequestScan(ctx, "whoami"); err != nil {
				t.Fatal(err)
			}
			if run := waitRun(t, e, image, 3); run.Status != store.ScanOK {
				t.Errorf("a scan with a stale sample = %+v, want it to run", run)
			}
		})
	}
}

func TestInstallFailsWithoutRoomInTheWordsAScanWouldBeSkippedWith(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	e.nomad.AddNode("node-1", "lab", "ready", "10.99.0.1")
	at := e.svc.clock.Now().UTC().Truncate(time.Minute)
	err := e.store.WriteMetricsTick(ctx, store.MetricsTick{At: at, Minute: store.MetricsRows{
		Hosts: []store.HostSample{{Node: "node-1", TS: at, MemoryTotalMB: new(int64(8000)), MemoryReservedMB: new(int64(100))}},
		Disks: []store.DiskSample{{Node: "node-1", TS: at, Mount: "/", Holds: "nomad", FreeMB: 3000, TotalMB: 50000}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.InstallImageScanner(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the install to fail", func() bool {
		st, _ := e.svc.scannerState(ctx)
		return st == store.AddonFailed
	})
	row, _ := e.store.GetAddon(ctx, store.AddonImageScanner)
	if want := "not enough free disk on server lab (2.9 GB free, the scanner's database needs 4 GB)"; row.Reason != want {
		t.Errorf("reason = %q, want %q", row.Reason, want)
	}
	if len(e.nomad.VolumeCreates()) != 0 {
		t.Error("the install created the volume before checking the room")
	}
}

func scanResultOf(lines ...string) vulns.Result {
	r, err := vulns.Parse("img", resultText(lines...))
	if err != nil {
		panic(err)
	}
	return r
}
