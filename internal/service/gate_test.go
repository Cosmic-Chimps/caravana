package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// gateOn saves the settings with the gate on (the scanner must be installed).
func gateOn(t *testing.T, e *env) {
	t.Helper()
	if _, err := e.svc.SetScanSettings(context.Background(), ScanSettings{NotifyLevel: "critical", DailyHour: 4, RefuseFixableCritical: true}); err != nil {
		t.Fatalf("turn the gate on: %v", err)
	}
}

func gateRuns(t *testing.T, e *env) []store.ScanRun {
	t.Helper()
	var out []store.ScanRun
	runs, _ := e.store.ListScanRuns(context.Background(), 0)
	for _, r := range runs {
		if r.Trigger == store.ScanTriggerGate {
			out = append(out, r)
		}
	}
	return out
}

func secretWhoami() app.App {
	a := whoami()
	a.Services[0].Secrets = []app.Secret{{Name: "TOKEN", Value: new("v1")}}
	return a
}

func TestGateRefusesAFixableCriticalBeforeAnythingIsRegistered(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	second := matchLine("CVE-2023-1111", "Critical", "fixed", "2.0", "openssl", "1.0")
	player := playScans(t, e.nomad, everyScanOK(resultText(busyboxLine, zlibLine, second, opensslLine)))
	installScanner(t, e)
	gateOn(t, e)

	rec, err := e.svc.Deploy(ctx, secretWhoami(), app.TriggerManual)
	var vuln *ImageVulnerableError
	if !errors.As(err, &vuln) || !errors.Is(err, ErrImageVulnerable) {
		t.Fatalf("Deploy error = %v, want an ImageVulnerableError", err)
	}
	if vuln.Field != "services[0].image" || vuln.Service != "web" || vuln.More != 1 || vuln.Finding.Package == "" {
		t.Errorf("error = %+v", vuln)
	}
	if want := "web's image has CVE-2022-37434 (critical) in zlib 1.2.11-r3, fixed in 1.2.12-r2 and 1 more"; vuln.Error() != want &&
		!strings.HasPrefix(vuln.Error(), "web's image has CVE-2023-1111 (critical) in openssl 1.0, fixed in 2.0") {
		t.Errorf("message = %q", vuln.Error())
	}
	if rec.Status != store.StatusFailed || !strings.Contains(rec.Error, "web's image has") {
		t.Errorf("record = %s %q", rec.Status, rec.Error)
	}
	if e.nomad.Job("whoami") != nil {
		t.Error("the refused deploy reached Nomad")
	}
	if e.secrets.Replaces() != 0 {
		t.Error("the refused deploy touched the secrets")
	}
	if n := len(player.PlayedOf(vuln.Image)); n != 1 {
		t.Errorf("scans of the image = %d, want 1", n)
	}
	if runs := gateRuns(t, e); len(runs) != 1 || runs[0].Status != store.ScanOK {
		t.Errorf("gate runs = %+v", runs)
	}
	got, _ := e.store.GetDeployment(ctx, rec.Deployment.ID)
	if got.Status != store.StatusFailed {
		t.Errorf("stored status = %s", got.Status)
	}

	// The result is fresh now: a second try is refused without another scan.
	if _, err := e.svc.Deploy(ctx, secretWhoami(), app.TriggerManual); !errors.Is(err, ErrImageVulnerable) {
		t.Fatalf("second deploy = %v", err)
	}
	if n := len(player.PlayedOf(vuln.Image)); n != 1 {
		t.Errorf("scans after the second try = %d, want the cached result used", n)
	}
}

func TestGateLetsUnfixableAndLowerFindingsThroughAndUsesTheCache(t *testing.T) {
	e, _ := newScanEnv(t)
	unfixed := matchLine("CVE-2024-5535", "Critical", "not-fixed", "", "libssl", "1.1")
	playScans(t, e.nomad, everyScanOK(resultText(unfixed, busyboxLine, opensslLine)))
	installScanner(t, e)
	gateOn(t, e)

	rec := deployApp(t, e, whoami())
	if len(rec.Warnings) != 0 {
		t.Errorf("warnings = %+v", rec.Warnings)
	}
	deployApp(t, e, whoami())
	if n := len(gateRuns(t, e)); n != 1 {
		t.Errorf("gate scans = %d, want 1: the second deploy reads the fresh result", n)
	}
	// After a day the result is stale and the gate scans again. (The deploys' own
	// after-deploy check runs a moment after they end healthy: let it finish first.)
	time.Sleep(80 * time.Millisecond)
	e.svc.clock.(*fakeClock).Set(time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC))
	deployApp(t, e, whoami())
	if n := len(gateRuns(t, e)); n != 2 {
		runs, _ := e.store.ListScanRuns(context.Background(), 0)
		t.Errorf("gate scans after a day = %d, want 2 (runs %+v)", n, runs)
	}
}

func TestGateWarnsWhenTheScanFailsAndTheDeployGoesOn(t *testing.T) {
	for _, c := range []struct {
		name, stderr, reason string
	}{
		{"database", "[0002] ERROR failed to load vulnerability db: database does not exist " + scanSecretSentinel + "\n", "the vulnerability database couldn't be downloaded or loaded"},
		{"registry", "[0003] ERROR failed to catalog: UNAUTHORIZED: authentication required " + scanSecretSentinel + "\n", "the registry refused the credentials"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, logs := newScanEnv(t)
			playScans(t, e.nomad, func(p playedScan) scanPlay {
				if p.Source == "database" {
					return scanPlay{}
				}
				return scanPlay{Exit: 1, Stderr: c.stderr}
			})
			installScanner(t, e)
			gateOn(t, e)
			rec := deployApp(t, e, whoami())
			want := "web's image wasn't checked for vulnerabilities: " + c.reason + "; deployed without the check."
			var got *app.Warning
			for i := range rec.Warnings {
				if rec.Warnings[i].Message == want {
					got = &rec.Warnings[i]
				}
			}
			if got == nil || got.Field != "services[0].image" {
				t.Fatalf("warnings = %+v, want %q at services[0].image", rec.Warnings, want)
			}
			assertNoSentinel(t, e, logs, scanSecretSentinel)
			for _, w := range rec.Warnings {
				if strings.Contains(w.Message, scanSecretSentinel) {
					t.Error("a warning holds the scanner's output")
				}
			}
		})
	}
}

func TestGateWarnsWhenTheScanTakesLongerThanTheDeployCanWait(t *testing.T) {
	e, _ := newScanEnv(t)
	playScans(t, e.nomad, func(p playedScan) scanPlay {
		return scanPlay{Hold: p.Source != "database"}
	})
	installScanner(t, e)
	gateOn(t, e)
	e.svc.budget = time.Second
	rec := deployApp(t, e, whoami())
	found := false
	for _, w := range rec.Warnings {
		found = found || strings.Contains(w.Message, "wasn't checked for vulnerabilities: the scan didn't finish in time; deployed without the check.")
	}
	if !found {
		t.Errorf("warnings = %+v", rec.Warnings)
	}
}

func TestNoGateMeansNoScanBeforeTheDeploy(t *testing.T) {
	ctx := context.Background()
	t.Run("the add-on isn't installed", func(t *testing.T) {
		e, _ := newScanEnv(t)
		// A setting saved before (or left over) can't gate while nothing is installed.
		if err := e.store.SaveScanSettings(ctx, store.ScanSettings{NotifyLevel: "critical", DailyHour: 4, RefuseFixableCritical: true}); err != nil {
			t.Fatal(err)
		}
		rec := deployApp(t, e, whoami())
		time.Sleep(60 * time.Millisecond)
		runs, _ := e.store.ListScanRuns(ctx, 0)
		if len(runs) != 0 || len(e.nomad.JobIDs("_scan-")) != 0 || len(rec.Warnings) != 0 {
			t.Errorf("runs = %+v, warnings = %+v; want nothing", runs, rec.Warnings)
		}
	})
	t.Run("the gate is off", func(t *testing.T) {
		e, _ := newScanEnv(t)
		playScans(t, e.nomad, everyScanOK(resultText(zlibLine)))
		installScanner(t, e)
		rec := deployApp(t, e, whoami()) // a fixable critical goes through
		waitRun(t, e, rec.Deployment.Digests["web"], 1)
		if got := gateRuns(t, e); len(got) != 0 {
			t.Errorf("gate runs = %+v", got)
		}
	})
}

func TestRedeployAndRollbackAreGatedTheSame(t *testing.T) {
	e, _ := newScanEnv(t)
	ctx := context.Background()
	playScans(t, e.nomad, everyScanOK(resultText(zlibLine)))
	installScanner(t, e)
	first := deployApp(t, e, whoami()) // the gate is off: it goes through
	waitRun(t, e, first.Deployment.Digests["web"], 1)
	gateOn(t, e)

	if _, err := e.svc.Redeploy(ctx, "whoami", app.TriggerWebhook); !errors.Is(err, ErrImageVulnerable) {
		t.Errorf("Redeploy = %v, want ErrImageVulnerable", err)
	}
	if _, err := e.svc.RedeployVersion(ctx, "whoami", first.Deployment.ID, app.TriggerManual); !errors.Is(err, ErrImageVulnerable) {
		t.Errorf("RedeployVersion = %v, want ErrImageVulnerable", err)
	}
	// The live version is untouched: the refused deploys are failed records.
	live, err := e.store.LatestHealthyDeployment(ctx, "whoami")
	if err != nil || live.Deployment.ID != first.Deployment.ID {
		t.Errorf("live = %+v, %v", live.Deployment.ID, err)
	}
}
