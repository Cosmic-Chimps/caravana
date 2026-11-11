package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// aptRun is one block of apt's history.
func aptRun(start, command, lines string) string {
	return "\nStart-Date: " + start + "\nCommandline: " + command + "\n" + lines + "\nEnd-Date: " + start + "\n"
}

const (
	autoRun   = "2026-10-24  06:06:23"
	manualRun = "2026-10-26  09:14:02"
	kernelRun = "2026-10-28  06:12:44"
)

// hostFiles is a host with three apt runs (one automatic and harmless, one by
// hand, one automatic that installed the kernel the marker names) and, when
// marked, a pending reboot written at since.
func hostFiles(marked bool, since time.Time) fstest.MapFS {
	history := aptRun(autoRun, "/usr/bin/unattended-upgrade", "Upgrade: libc6:amd64 (1, 2), curl:amd64 (3, 4)") +
		aptRun(manualRun, "apt install htop", "Install: htop:amd64 (3.3.0-4build1), libnl:amd64 (1, automatic)") +
		aptRun("2026-10-27  07:30:00", "apt autoremove", "Remove: old:amd64 (1)") +
		aptRun(kernelRun, "/usr/bin/unattended-upgrade", "Upgrade: linux-image-virtual:amd64 (1, 2)\nInstall: linux-image-6.8.0-149-generic:amd64 (6.8.0-149.149, automatic)")
	files := fstest.MapFS{"var/log/apt/history.log": {Data: []byte(history)}}
	if marked {
		files["run/reboot-required"] = &fstest.MapFile{Data: []byte("*** System restart required ***\n"), ModTime: since}
		files["run/reboot-required.pkgs"] = &fstest.MapFile{Data: []byte("linux-image-6.8.0-149-generic\n")}
	}
	return files
}

type osDetail struct {
	Packages int
	By       string
	Reboot   bool
}

func osEvents(t *testing.T, st *store.Store) (events []store.HostEvent, details []osDetail) {
	t.Helper()
	events, err := st.HostEvents(context.Background(), store.HostEventOSPackages, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		var d osDetail
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
			t.Fatalf("detail %q: %v", e.Detail, err)
		}
		details = append(details, d)
	}
	return events, details
}

// drained says whether a ChangeHost notice is waiting.
func drained(notices <-chan Change) bool {
	saw := false
	for {
		select {
		case c := <-notices:
			saw = saw || c.What == ChangeHost
		default:
			return saw
		}
	}
}

func TestSamplerRecordsPackageRuns(t *testing.T) {
	l := nodeLab(t)
	l.svc.SetHostRoot(hostFiles(true, time.Date(2026, 10, 28, 6, 13, 30, 0, time.UTC)))
	notices, stop := l.svc.Subscribe("")
	defer stop()

	if v, err := l.svc.Host(context.Background(), ""); err != nil || v.Reboot != nil {
		t.Fatalf("Host before a tick: Reboot = %+v, %v; want nil", v.Reboot, err)
	}
	l.tick(t, 1)

	events, details := osEvents(t, l.store)
	// Newest first; the autoremove changed no package and is not an update.
	if len(events) != 3 {
		t.Fatalf("package events = %+v, want 3", events)
	}
	want := []struct {
		at  string
		key string
		d   osDetail
	}{
		{"2026-10-28T06:12:44Z", "os|2026-10-28T06:12:44Z", osDetail{2, "unattended", true}},
		{"2026-10-26T09:14:02Z", "os|2026-10-26T09:14:02Z", osDetail{2, "hand", false}},
		{"2026-10-24T06:06:23Z", "os|2026-10-24T06:06:23Z", osDetail{2, "unattended", false}},
	}
	for i, w := range want {
		if got := events[i].At.UTC().Format(time.RFC3339); got != w.at || events[i].Key != w.key || details[i] != w.d {
			t.Errorf("event %d = %s %s %+v, want %s %s %+v", i, got, events[i].Key, details[i], w.at, w.key, w.d)
		}
	}
	if !drained(notices) {
		t.Error("no ChangeHost notice after recording package runs")
	}

	// The reboot state is kept for the host view.
	v, err := l.svc.Host(context.Background(), "")
	if err != nil || v.Reboot == nil {
		t.Fatalf("Host after a tick: Reboot = %+v, %v", v.Reboot, err)
	}
	if !v.Reboot.Required || len(v.Reboot.Packages) != 1 || v.Reboot.InstalledKernel != "6.8.0-149-generic" {
		t.Errorf("reboot = %+v", v.Reboot)
	}
	// What the view hands out is a copy.
	v.Reboot.Packages[0] = "changed"
	if again := l.svc.hostReboot(); again.Packages[0] != "linux-image-6.8.0-149-generic" {
		t.Errorf("the view's list shares the sampler's: %v", again.Packages)
	}

	// Another tick with nothing new records nothing and tells nobody.
	l.tick(t, 2)
	if got, _ := osEvents(t, l.store); len(got) != 3 {
		t.Errorf("events after an unchanged tick = %d, want 3", len(got))
	}
	if drained(notices) {
		t.Error("a ChangeHost notice for nothing new")
	}

	// A restart reads the same history again and the keys stop the repeats.
	l.svc.sampler.osSeen = time.Time{}
	l.tick(t, 3)
	if got, _ := osEvents(t, l.store); len(got) != 3 {
		t.Errorf("events after a re-read = %d, want 3", len(got))
	}
}

func TestSamplerReboot(t *testing.T) {
	l := nodeLab(t)
	notices, stop := l.svc.Subscribe("")
	defer stop()
	since := time.Date(2026, 10, 28, 6, 13, 30, 0, time.UTC)

	// No files: not required, and nothing recorded.
	l.svc.SetHostRoot(fstest.MapFS{})
	l.tick(t, 1)
	v, _ := l.svc.Host(context.Background(), "")
	if v.Reboot == nil || v.Reboot.Required {
		t.Fatalf("an empty host: Reboot = %+v", v.Reboot)
	}
	drained(notices)

	// The marker appears: required, and pages are told.
	l.svc.SetHostRoot(hostFiles(true, since))
	l.tick(t, 2)
	if v, _ = l.svc.Host(context.Background(), ""); !v.Reboot.Required || !v.Reboot.Since.Equal(since) {
		t.Errorf("after the marker: %+v", v.Reboot)
	}
	if !drained(notices) {
		t.Error("no ChangeHost notice when a reboot became needed")
	}

	// A boot clears it: required goes back to false, and pages are told again.
	l.svc.SetHostRoot(hostFiles(false, time.Time{}))
	l.tick(t, 3)
	if v, _ = l.svc.Host(context.Background(), ""); v.Reboot.Required {
		t.Errorf("after the boot: %+v", v.Reboot)
	}
	if !drained(notices) {
		t.Error("no ChangeHost notice when the reboot marker went away")
	}
}

// A marker older than the run can't have been made by it, so an old run is not
// said to need a reboot because of a marker written after a later one.
func TestPackageRunRebootNeedsAMarkerNotOlderThanTheRun(t *testing.T) {
	l := nodeLab(t)
	l.svc.SetHostRoot(hostFiles(true, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)))
	l.tick(t, 1)
	_, details := osEvents(t, l.store)
	for i, d := range details {
		if d.Reboot {
			t.Errorf("run %d says a reboot is needed from a marker older than it", i)
		}
	}
}

// A host with a damaged rotated log still records the current one.
func TestSamplerSurvivesAnUnreadableHistory(t *testing.T) {
	l := nodeLab(t)
	files := hostFiles(false, time.Time{})
	files["var/log/apt/history.log.1.gz"] = &fstest.MapFile{Data: []byte("not gzip")}
	l.svc.SetHostRoot(files)
	l.tick(t, 1)
	if got, _ := osEvents(t, l.store); len(got) != 3 {
		t.Errorf("events = %d, want 3", len(got))
	}
}

func TestOSPackagesWords(t *testing.T) {
	for _, tc := range []struct {
		d      osPackagesDetail
		want   string
		status string
	}{
		{osPackagesDetail{Packages: 14, By: "unattended"}, "OS packages updated by Ubuntu's automatic updates: 14 packages.", ActivityIdle},
		{osPackagesDetail{Packages: 3, By: "hand"}, "OS packages updated by hand: 3 packages.", ActivityIdle},
		{osPackagesDetail{Packages: 1, By: "hand"}, "OS packages updated by hand: 1 package.", ActivityIdle},
		{osPackagesDetail{Packages: 14, By: "unattended", Reboot: true}, "OS packages updated by Ubuntu's automatic updates: 14 packages, a reboot is needed.", ActivityWarning},
		{osPackagesDetail{By: "hand"}, "OS packages updated by hand.", ActivityIdle},
	} {
		status, p := osPackagesWords(tc.d)
		if got := plainText(p); got != tc.want || status != tc.status {
			t.Errorf("%+v: %q (%s), want %q (%s)", tc.d, got, status, tc.want, tc.status)
		}
	}
}

// The service reads the host root through hostos.Dir, which a folder of fixtures
// stands in for.
func TestHostRootFromAFolder(t *testing.T) {
	l := nodeLab(t)
	l.svc.SetHostRoot(hostos.Dir("../hostos/testdata/host"))
	l.tick(t, 1)
	events, _ := osEvents(t, l.store)
	if len(events) != 5 { // the fixture has five runs that changed packages
		t.Fatalf("events = %d, want 5", len(events))
	}
	if r := l.svc.hostReboot(); r == nil || !r.Required || !strings.HasPrefix(r.InstalledKernel, "6.8.0-149") {
		t.Errorf("reboot = %+v", r)
	}
}
