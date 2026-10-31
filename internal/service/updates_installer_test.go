package service

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cosmic-chimps/caravana/internal/hostos"
)

// The Updates view says who installed what the reboot is for: the run that the history marks as
// needing a reboot, the one the marker's list names. Without such a run it says nothing.
func TestUpdatesSaysWhoInstalledWhatNeedsTheReboot(t *testing.T) {
	ctx := context.Background()
	l := nodeLab(t)
	l.svc.SetHostRoot(hostFiles(true, time.Date(2026, 10, 28, 6, 13, 30, 0, time.UTC)))
	l.tick(t, 1)
	v, err := l.svc.Updates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := v.OS.Reboot
	if want := time.Date(2026, 10, 28, 6, 12, 44, 0, time.UTC); !r.InstalledAt.Equal(want) || r.InstalledBy != "unattended" {
		t.Errorf("installed at %v by %q, want %v by unattended", r.InstalledAt, r.InstalledBy, want)
	}

	// A marker that names nothing any run installed: no line.
	files := hostFiles(true, time.Date(2026, 10, 28, 6, 13, 30, 0, time.UTC))
	files["run/reboot-required.pkgs"] = &fstest.MapFile{Data: []byte("some-other-package\n")}
	l.svc.SetHostRoot(files)
	l.tick(t, 2)
	if v, err = l.svc.Updates(ctx); err != nil || !v.OS.Reboot.InstalledAt.IsZero() || v.OS.Reboot.InstalledBy != "" {
		t.Errorf("reboot = %+v, %v; want no installer named", v.OS.Reboot, err)
	}

	// After the boot there is nothing to say.
	l.svc.SetHostRoot(hostFiles(false, time.Time{}))
	l.tick(t, 3)
	if v, err = l.svc.Updates(ctx); err != nil || v.OS.Reboot.Required || !v.OS.Reboot.InstalledAt.IsZero() {
		t.Errorf("reboot = %+v, %v; want none", v.OS.Reboot, err)
	}
}

// The newest apt run that installed a package the reboot marker names, and who ran it.
func TestRebootRunOf(t *testing.T) {
	at := func(d int) time.Time { return time.Date(2026, 10, d, 6, 0, 0, 0, time.UTC) }
	pkg := func(names ...string) []hostos.Package {
		var out []hostos.Package
		for _, n := range names {
			out = append(out, hostos.Package{Name: n})
		}
		return out
	}
	reboot := hostos.Reboot{Required: true, Since: at(28), Packages: []string{"linux-image-7.0.0-40-generic", "libc6"}}
	runs := []hostos.Run{ // newest first
		{Start: at(30), Automatic: false, Upgraded: pkg("curl")},                                                 // names nothing of the marker's
		{Start: at(28), Automatic: true, Upgraded: pkg("libc6"), Installed: pkg("linux-image-7.0.0-40-generic")}, // the one
		{Start: at(27), Automatic: false, Upgraded: pkg("libc6")},                                                // older: not the newest
		{Start: at(20), Automatic: true, Upgraded: pkg("libc6")},
	}
	got := rebootRunOf(reboot, runs)
	if !got.Found || !got.At.Equal(at(28)) || !got.Automatic {
		t.Errorf("run = %+v, want the automatic one of Oct 28", got)
	}
	// The marker is older than a run that names a package: that run can't have made it necessary.
	older := hostos.Reboot{Required: true, Since: at(19), Packages: reboot.Packages}
	if got := rebootRunOf(older, runs); got.Found {
		t.Errorf("run = %+v for a marker older than every run, want none", got)
	}
	// By hand.
	handRuns := []hostos.Run{{Start: at(28), Automatic: false, Installed: pkg("libc6")}}
	if got := rebootRunOf(reboot, handRuns); !got.Found || got.Automatic {
		t.Errorf("run = %+v, want one by hand", got)
	}
	// No reboot pending, or no list: none.
	if got := rebootRunOf(hostos.Reboot{}, runs); got.Found {
		t.Errorf("run = %+v with no reboot pending", got)
	}
	if got := rebootRunOf(hostos.Reboot{Required: true, Since: at(29)}, runs); got.Found {
		t.Errorf("run = %+v with no packages listed", got)
	}
}
