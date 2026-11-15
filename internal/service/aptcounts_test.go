package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// fakeApt is an apt command that gives the answer it is told to, and counts how often it is run.
type fakeApt struct {
	mu    sync.Mutex
	out   string
	err   error
	calls int
}

func (a *fakeApt) Run(context.Context) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return []byte(a.out), a.err
}

func (a *fakeApt) set(out string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.out, a.err = out, err
}

func (a *fakeApt) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

const debianUpgradable = "WARNING: apt does not have a stable CLI interface.\n\nListing...\n" +
	"curl/bookworm-security 7.88.1-10+deb12u8 amd64 [upgradable from: 7.88.1-10+deb12u5]\n" +
	"libc6/bookworm,bookworm-security 2.36-9+deb12u9 arm64 [upgradable from: 2.36-9+deb12u7]\n" +
	"tzdata/bookworm-updates 2024b-0+deb12u1 all [upgradable from: 2024a-0+deb12u1]\n"

// debianHost is a Debian 12 host: no updates-available file, apt's own update stamp.
func debianHost(stamp time.Time) fstest.MapFS {
	return fstest.MapFS{
		"etc/os-release": {Data: []byte("ID=debian\nVERSION_ID=\"12\"\n")},
		"var/lib/apt/periodic/update-success-stamp": {ModTime: stamp},
		"var/lib/dpkg/status":                       {ModTime: stamp.Add(-time.Hour)},
	}
}

func TestNoteOSCountsWaitingUpdatesFromAptOnDebian(t *testing.T) {
	l := nodeLab(t)
	stamp := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	files := debianHost(stamp)
	l.svc.SetHostRoot(files)
	apt := &fakeApt{out: debianUpgradable}
	l.svc.SetAptRunner(apt)

	l.svc.noteOS(context.Background())
	state, _, ok := l.svc.osState()
	if !ok || !state.Updates.Known || state.Updates.Total != 3 || state.Updates.Security != 2 || !state.Updates.CheckedAt.Equal(stamp) {
		t.Fatalf("after the first tick: %+v", state.Updates)
	}

	// Nothing the files say has changed: apt is not run again, the counts stay.
	l.svc.noteOS(context.Background())
	l.svc.noteOS(context.Background())
	if apt.count() != 1 {
		t.Errorf("apt ran %d times with the files unchanged, want 1", apt.count())
	}
	if got := l.svc.readOSNow().Updates; !got.Known || got.Total != 3 {
		t.Errorf("a fresh read lost the counts: %+v", got)
	}

	// An `apt update` touches the stamp: apt runs again and the new answer is used.
	apt.set("Listing...\n", nil)
	newStamp := stamp.Add(24 * time.Hour)
	files["var/lib/apt/periodic/update-success-stamp"] = &fstest.MapFile{ModTime: newStamp}
	l.svc.noteOS(context.Background())
	state, _, _ = l.svc.osState()
	if apt.count() != 2 || !state.Updates.Known || state.Updates.Total != 0 || !state.Updates.CheckedAt.Equal(newStamp) {
		t.Errorf("after the stamp moved: apt ran %d times, %+v", apt.count(), state.Updates)
	}
}

func TestNoteOSLeavesTheCountsUnknownWhenAptFails(t *testing.T) {
	l := nodeLab(t)
	stamp := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	l.svc.SetHostRoot(debianHost(stamp))
	apt := &fakeApt{err: errors.New("apt list exited with code 100")}
	l.svc.SetAptRunner(apt)

	l.svc.noteOS(context.Background())
	state, _, _ := l.svc.osState()
	if state.Updates.Known || state.Updates.Total != 0 {
		t.Errorf("a failed apt gave counts: %+v", state.Updates)
	}
	if got := l.svc.readOSNow().Updates; got.Known {
		t.Errorf("a fresh read made counts up: %+v", got)
	}

	// Not tried again within the hour while the files stay the same.
	l.svc.noteOS(context.Background())
	if apt.count() != 1 {
		t.Errorf("apt ran %d times within the hour after a failure, want 1", apt.count())
	}

	// Over an hour later it is tried again, and when it works the counts appear.
	apt.set(debianUpgradable, nil)
	clock := l.svc.clock.(*fakeClock)
	clock.Set(clock.Now().Add(2 * time.Hour))
	l.svc.noteOS(context.Background())
	state, _, _ = l.svc.osState()
	if apt.count() != 2 || !state.Updates.Known || state.Updates.Total != 3 {
		t.Errorf("after the retry: apt ran %d times, %+v", apt.count(), state.Updates)
	}
}

func TestNoteOSWithoutAptRunnerOrWithUbuntusFile(t *testing.T) {
	l := nodeLab(t)
	l.svc.SetHostRoot(debianHost(time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)))
	l.svc.noteOS(context.Background())
	if state, _, _ := l.svc.osState(); state.Updates.Known {
		t.Errorf("no runner set, yet counts are known: %+v", state.Updates)
	}

	// Ubuntu's own file is the answer: apt is never run.
	apt := &fakeApt{out: debianUpgradable}
	l.svc.SetAptRunner(apt)
	l.svc.SetHostRoot(ubuntuHost())
	l.svc.noteOS(context.Background())
	if apt.count() != 0 {
		t.Errorf("apt ran %d times on a host with updates-available", apt.count())
	}
	if state, _, _ := l.svc.osState(); !state.Updates.Known {
		t.Error("Ubuntu's counts were lost")
	}
}
