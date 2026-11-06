package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const updHex = "3f6c0d2a9b7e41c58d0f2e6a1b9c7d40"

func TestParseUpdaterStatus(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 14, 3, 0, 0, time.UTC)
	tests := []struct {
		name string
		in   string
		want updaterStatus
	}{
		{"running", "ID=" + updHex + "\nVERSION=0.17.0\nFROM=0.16.0\nSTATE=running\nSTARTED=2026-11-06T14:03:00Z\nFINISHED=\nREASON=\n",
			updaterStatus{ID: updHex, Version: "0.17.0", From: "0.16.0", State: "running", Started: t0}},
		{"rolled back with a reason", "ID=" + updHex + "\nVERSION=0.17.0\nFROM=0.16.0\nSTATE=rolled_back\nSTARTED=2026-11-06T14:03:00Z\nFINISHED=2026-11-06T14:05:12Z\nREASON=0.17.0 didn't answer; 0.16.0 runs again\n",
			updaterStatus{ID: updHex, Version: "0.17.0", From: "0.16.0", State: "rolled_back", Started: t0,
				Finished: time.Date(2026, 11, 6, 14, 5, 12, 0, time.UTC), Reason: "0.17.0 didn't answer; 0.16.0 runs again"}},
		{"any order, an unknown key, CRLF", "REASON=x\r\nSTATE=failed\r\nCOLOR=blue\r\nID=" + updHex + "\r\n",
			updaterStatus{ID: updHex, State: "failed", Reason: "x"}},
		{"a state outside the five is left empty", "ID=" + updHex + "\nSTATE=lost\n", updaterStatus{ID: updHex}},
		{"a malformed time is zero", "ID=" + updHex + "\nSTATE=running\nSTARTED=yesterday\n", updaterStatus{ID: updHex, State: "running"}},
		{"a time with an offset is read as UTC", "ID=" + updHex + "\nSTATE=running\nSTARTED=2026-11-06T16:03:00+02:00\n",
			updaterStatus{ID: updHex, State: "running", Started: t0}},
		{"the reason keeps printable ASCII only", "ID=" + updHex + "\nSTATE=failed\nREASON=a\x1b[31mb\x00c\tdé\n",
			updaterStatus{ID: updHex, State: "failed", Reason: "a[31mbcd"}},
		{"the reason is cut at 300 bytes", "ID=" + updHex + "\nSTATE=failed\nREASON=" + strings.Repeat("x", 500) + "\n",
			updaterStatus{ID: updHex, State: "failed", Reason: strings.Repeat("x", 300)}},
		{"a value may hold an equals sign", "ID=" + updHex + "\nSTATE=failed\nREASON=a=b\n", updaterStatus{ID: updHex, State: "failed", Reason: "a=b"}},
		{"garbage", "\x00\x01 not a status\n\n=\nSTATE\n", updaterStatus{}},
		{"empty", "", updaterStatus{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseUpdaterStatus([]byte(tc.in)); got != tc.want {
				t.Errorf("parseUpdaterStatus() = %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestReadUpdaterStatusFile(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readUpdaterStatus(dir); ok {
		t.Error("a directory with no status file gave a status")
	}
	if _, ok := readUpdaterStatus(""); ok {
		t.Error("no directory gave a status")
	}
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ID=" + updHex + "\nSTATE=updated\n")
	if st, ok := readUpdaterStatus(dir); !ok || st.State != "updated" || st.ID != updHex {
		t.Errorf("good file = %+v, %v", st, ok)
	}
	// More than the updater ever writes is ignored, whatever it holds.
	write("ID=" + updHex + "\nSTATE=updated\nREASON=" + strings.Repeat("x", 5000) + "\n")
	if _, ok := readUpdaterStatus(dir); ok {
		t.Error("an oversized file was read")
	}
	// A symlink isn't a plain file.
	if err := os.Remove(filepath.Join(dir, "status")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("ID="+updHex+"\nSTATE=updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "status")); err != nil {
		t.Fatal(err)
	}
	if _, ok := readUpdaterStatus(dir); ok {
		t.Error("a symlinked status was read")
	}
}

// updEnv is a service that runs 0.16.0 and has seen 0.17.0, with an updater directory and a data
// directory of its own.
type updEnv struct {
	*env
	dir     string // the updater's directory, installed
	request string // the request file's path
	t0      time.Time
}

func newUpdEnv(t *testing.T) *updEnv {
	t.Helper()
	e := &updEnv{env: newEnv(t), dir: t.TempDir(), t0: time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC)}
	e.request = filepath.Join(t.TempDir(), "update-request")
	e.svc.clock.(*fakeClock).Set(e.t0)
	e.svc.SetVersion("0.16.0")
	e.svc.SetUpdaterDir(e.dir)
	e.svc.SetUpdateRequestPath(e.request)
	e.install(t)
	e.seen(t, "v0.17.0")
	return e
}

func (e *updEnv) install(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, "updater"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seen records the newest release the check has seen.
func (e *updEnv) seen(t *testing.T, tag string) {
	t.Helper()
	rc, err := e.store.ReleaseCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rc.LatestTag = tag
	if err := e.store.SaveReleaseResult(context.Background(), rc, nil); err != nil {
		t.Fatal(err)
	}
}

func (e *updEnv) status(t *testing.T, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, "status"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *updEnv) advance(d time.Duration) { e.svc.clock.(*fakeClock).Advance(d) }

func (e *updEnv) run(t *testing.T, id string) store.UpdateRun {
	t.Helper()
	runs, err := e.store.UpdateRuns(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no run %q in %v", id, runs)
	return store.UpdateRun{}
}

func (e *updEnv) follow(t *testing.T) {
	t.Helper()
	if err := e.svc.followUpdateRun(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (e *updEnv) requestExists() bool {
	_, err := os.Lstat(e.request)
	return err == nil
}

func TestRequestUpdate(t *testing.T) {
	ctx := context.Background()

	t.Run("a request writes the row and the file", func(t *testing.T) {
		e := newUpdEnv(t)
		run, err := e.svc.RequestUpdate(ctx, "v0.17.0", store.UpdateByAPI, "ci-token")
		if err != nil {
			t.Fatal(err)
		}
		if len(run.ID) != 32 || strings.Trim(run.ID, "0123456789abcdef") != "" {
			t.Errorf("ID = %q, want 32 lowercase hex characters", run.ID)
		}
		if run.Version != "0.17.0" || run.FromVersion != "0.16.0" || run.State != store.UpdateRequested ||
			run.Trigger != store.UpdateByAPI || run.TokenName != "ci-token" || !run.RequestedAt.Equal(e.t0) {
			t.Errorf("run = %+v", run)
		}
		data, err := os.ReadFile(e.request)
		if err != nil {
			t.Fatal(err)
		}
		if want := "ID=" + run.ID + "\nVERSION=0.17.0\n"; string(data) != want {
			t.Errorf("request = %q, want %q", data, want)
		}
		if fi, err := os.Stat(e.request); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("request mode = %v, %v; want 0600", fi.Mode().Perm(), err)
		}
		if _, err := os.Lstat(e.request + ".tmp"); err == nil {
			t.Error("the temporary file was left behind")
		}
		stored := e.run(t, run.ID)
		if stored.Trigger != store.UpdateByAPI || stored.State != store.UpdateRequested {
			t.Errorf("stored run = %+v", stored)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		tests := []struct {
			name    string
			setup   func(t *testing.T, e *updEnv)
			version string
			is      error
			field   string
			text    string
		}{
			{name: "malformed", version: "0.17", field: "version"},
			{name: "a command", version: "0.17.0;id", field: "version"},
			{name: "empty", version: "", field: "version"},
			{name: "a path", version: "../0.17.0", field: "version"},
			{name: "development build", setup: func(t *testing.T, e *updEnv) { e.svc.SetVersion("dev") },
				version: "0.17.0", is: ErrUpdateUnavailable, text: "development build"},
			{name: "a dev release build", setup: func(t *testing.T, e *updEnv) { e.svc.SetVersion("0.0.0-dev.20261106") },
				version: "0.17.0", is: ErrUpdateUnavailable, text: "development build"},
			{name: "updater not installed", setup: func(t *testing.T, e *updEnv) {
				if err := os.Remove(filepath.Join(e.dir, "updater")); err != nil {
					t.Fatal(err)
				}
			}, version: "0.17.0", is: ErrUpdateUnavailable, text: "Run the installer of 0.17.0 once on the server"},
			{name: "updater marker is not a regular file", setup: func(t *testing.T, e *updEnv) {
				p := filepath.Join(e.dir, "updater")
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p, 0o755); err != nil {
					t.Fatal(err)
				}
			}, version: "0.17.0", is: ErrUpdateUnavailable},
			{name: "already the running version", setup: func(t *testing.T, e *updEnv) { e.svc.SetVersion("0.17.0") },
				version: "0.17.0", is: ErrUpdateNotOffered, text: "caravana 0.17.0 already runs here."},
			{name: "older than the running version", setup: func(t *testing.T, e *updEnv) { e.svc.SetVersion("0.18.0") },
				version: "0.17.0", is: ErrUpdateNotOffered, text: "isn't newer"},
			{name: "not the newest seen", version: "0.16.1", is: ErrUpdateNotOffered,
				text: "0.16.1 isn't the newest release caravana has seen (0.17.0). Check for releases first."},
			{name: "a newer one than seen", version: "0.17.1", is: ErrUpdateNotOffered, text: "isn't the newest release caravana has seen (0.17.0)"},
			{name: "no release seen", setup: func(t *testing.T, e *updEnv) { e.seen(t, "") },
				version: "0.17.0", is: ErrUpdateNotOffered, text: "hasn't seen a release yet"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				e := newUpdEnv(t)
				if tc.setup != nil {
					tc.setup(t, e)
				}
				_, err := e.svc.RequestUpdate(ctx, tc.version, store.UpdateByConsole, "")
				switch {
				case err == nil:
					t.Fatal("want a refusal")
				case tc.field != "":
					var fe *app.FieldError
					if !errors.As(err, &fe) || fe.Field != tc.field {
						t.Errorf("err = %v, want a FieldError on %q", err, tc.field)
					}
				case !errors.Is(err, tc.is):
					t.Errorf("err = %v, want %v", err, tc.is)
				}
				if tc.text != "" && !strings.Contains(err.Error(), tc.text) {
					t.Errorf("err = %q, want it to say %q", err, tc.text)
				}
				if e.requestExists() {
					t.Error("a refused request left a file")
				}
				if runs, _ := e.store.UpdateRuns(ctx, 5); len(runs) != 0 {
					t.Errorf("a refused request left rows: %v", runs)
				}
			})
		}
	})

	t.Run("a second request while one is open", func(t *testing.T) {
		e := newUpdEnv(t)
		first, err := e.svc.RequestUpdate(ctx, "0.17.0", store.UpdateByConsole, "")
		if err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(e.request)
		_, err = e.svc.RequestUpdate(ctx, "0.17.0", store.UpdateByConsole, "")
		var ur *UpdateRunningError
		if !errors.Is(err, ErrUpdateRunning) || !errors.As(err, &ur) || ur.Version != "0.17.0" ||
			err.Error() != "An update to 0.17.0 is already under way." {
			t.Errorf("err = %v, want update_running for 0.17.0", err)
		}
		if after, _ := os.ReadFile(e.request); string(after) != string(before) {
			t.Error("the open run's request file was rewritten")
		}
		if runs, _ := e.store.UpdateRuns(ctx, 5); len(runs) != 1 || runs[0].ID != first.ID {
			t.Errorf("runs = %v, want the first only", runs)
		}
	})

	t.Run("two at once make one run", func(t *testing.T) {
		e := newUpdEnv(t)
		errs := make(chan error, 8)
		for range cap(errs) {
			go func() {
				_, err := e.svc.RequestUpdate(ctx, "0.17.0", store.UpdateByConsole, "")
				errs <- err
			}()
		}
		ok := 0
		for range cap(errs) {
			err := <-errs
			switch {
			case err == nil:
				ok++
			case !errors.Is(err, ErrUpdateRunning):
				t.Errorf("err = %v, want nil or update_running", err)
			}
		}
		if ok != 1 {
			t.Errorf("%d requests went through, want exactly 1", ok)
		}
	})

	t.Run("a file that can't be written removes the row", func(t *testing.T) {
		e := newUpdEnv(t)
		e.svc.SetUpdateRequestPath(filepath.Join(t.TempDir(), "missing", "update-request")) // no such directory
		e.install(t)
		_, err := e.svc.RequestUpdate(ctx, "0.17.0", store.UpdateByConsole, "")
		if err == nil {
			t.Fatal("want an error")
		}
		for _, bad := range []error{ErrUpdateUnavailable, ErrUpdateRunning, ErrUpdateNotOffered} {
			if errors.Is(err, bad) {
				t.Errorf("err = %v, is a refusal; want a plain error", err)
			}
		}
		if runs, _ := e.store.UpdateRuns(ctx, 5); len(runs) != 0 {
			t.Errorf("the row stayed: %v", runs)
		}
		// A later request works once the directory is there.
		e.svc.SetUpdateRequestPath(e.request)
		if _, err := e.svc.RequestUpdate(ctx, "0.17.0", store.UpdateByConsole, ""); err != nil {
			t.Errorf("request after the failure = %v", err)
		}
	})

	t.Run("a leftover temporary file or request is replaced", func(t *testing.T) {
		e := newUpdEnv(t)
		for _, p := range []string{e.request + ".tmp", e.request} {
			if err := os.WriteFile(p, []byte("OLD"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		run, err := e.svc.RequestUpdate(ctx, "0.17.0", store.UpdateByConsole, "")
		if err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(e.request); string(data) != "ID="+run.ID+"\nVERSION=0.17.0\n" {
			t.Errorf("request = %q", data)
		}
		if fi, _ := os.Stat(e.request); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
	})

	t.Run("a request is a notice for open pages", func(t *testing.T) {
		e := newUpdEnv(t)
		ch, cancel := e.svc.Subscribe("")
		defer cancel()
		if _, err := e.svc.RequestUpdate(ctx, "0.17.0", store.UpdateByConsole, ""); err != nil {
			t.Fatal(err)
		}
		if !sawChange(ch, ChangeUpdates) {
			t.Error("no updates notice after a request")
		}
	})
}

func TestUpdateRunWatcher(t *testing.T) {
	ctx := context.Background()
	logOf := func(e *updEnv, id string) string { return filepath.Join(e.dir, "logs", id+".log") }

	request := func(t *testing.T, e *updEnv) store.UpdateRun {
		t.Helper()
		run, err := e.svc.RequestUpdate(ctx, "0.17.0", store.UpdateByConsole, "")
		if err != nil {
			t.Fatal(err)
		}
		return run
	}

	t.Run("requested, running, updated", func(t *testing.T) {
		e := newUpdEnv(t)
		run := request(t, e)
		e.follow(t)
		if got := e.run(t, run.ID); got.State != store.UpdateRequested {
			t.Fatalf("with no status the run is %q, want requested", got.State)
		}

		started := e.t0.Add(40 * time.Second)
		e.status(t, "ID="+run.ID, "VERSION=0.17.0", "FROM=0.16.0", "STATE=running", "STARTED="+started.Format(time.RFC3339))
		e.advance(time.Minute)
		e.follow(t)
		got := e.run(t, run.ID)
		if got.State != store.UpdateRunning || !got.StartedAt.Equal(started) || !got.FinishedAt.IsZero() {
			t.Errorf("running run = %+v", got)
		}
		if !e.requestExists() {
			t.Error("the request file was removed while the run is going")
		}

		finished := e.t0.Add(3 * time.Minute)
		e.status(t, "ID="+run.ID, "VERSION=0.17.0", "FROM=0.16.0", "STATE=updated",
			"STARTED="+started.Format(time.RFC3339), "FINISHED="+finished.Format(time.RFC3339), "REASON=")
		e.follow(t)
		got = e.run(t, run.ID)
		if got.State != store.UpdateUpdated || !got.FinishedAt.Equal(finished) || got.Reason != "" || got.Log != logOf(e, run.ID) {
			t.Errorf("updated run = %+v", got)
		}
		if e.requestExists() {
			t.Error("the request file stays after the run is final")
		}
		if _, open, _ := e.store.OpenUpdateRun(ctx); open {
			t.Error("a final run is open")
		}
	})

	t.Run("each final state, with its reason", func(t *testing.T) {
		for _, tc := range []struct {
			state, reason, wantLog string
		}{
			{store.UpdateRolledBack, "0.17.0 didn't answer; 0.16.0 runs again", "log"},
			{store.UpdateFailed, "The installer stopped with exit code 4.", "log"},
			{store.UpdateRefused, "Couldn't download caravana 0.17.0's release files.", ""},
		} {
			t.Run(tc.state, func(t *testing.T) {
				e := newUpdEnv(t)
				run := request(t, e)
				e.status(t, "ID="+run.ID, "VERSION=0.17.0", "FROM=0.16.0", "STATE="+tc.state, "STARTED="+e.t0.Format(time.RFC3339),
					"FINISHED="+e.t0.Add(time.Minute).Format(time.RFC3339), "REASON="+tc.reason)
				e.follow(t)
				got := e.run(t, run.ID)
				wantLog := ""
				if tc.wantLog != "" {
					wantLog = logOf(e, run.ID)
				}
				if got.State != tc.state || got.Reason != tc.reason || got.Log != wantLog || got.FinishedAt.IsZero() {
					t.Errorf("run = %+v, want state %s reason %q log %q", got, tc.state, tc.reason, wantLog)
				}
				if e.requestExists() {
					t.Error("the request file stays after the run is final")
				}
			})
		}
	})

	t.Run("a refusal before the run started has no start time", func(t *testing.T) {
		e := newUpdEnv(t)
		run := request(t, e)
		e.status(t, "ID="+run.ID, "VERSION=0.17.0", "STATE=refused", "FINISHED="+e.t0.Add(time.Minute).Format(time.RFC3339),
			"REASON=The request's version isn't a release version.")
		e.follow(t)
		got := e.run(t, run.ID)
		if got.State != store.UpdateRefused || !got.StartedAt.IsZero() || got.Log != "" {
			t.Errorf("run = %+v", got)
		}
	})

	t.Run("an older run's status is ignored", func(t *testing.T) {
		e := newUpdEnv(t)
		run := request(t, e)
		e.status(t, "ID=00000000000000000000000000000000", "VERSION=0.16.5", "STATE=updated", "FINISHED="+e.t0.Format(time.RFC3339))
		e.advance(time.Minute)
		e.follow(t)
		if got := e.run(t, run.ID); got.State != store.UpdateRequested {
			t.Errorf("state = %q, want requested", got.State)
		}
	})

	t.Run("nothing started within 5 minutes is lost", func(t *testing.T) {
		e := newUpdEnv(t)
		run := request(t, e)
		e.advance(4*time.Minute + 59*time.Second)
		e.follow(t)
		if got := e.run(t, run.ID); got.State != store.UpdateRequested {
			t.Fatalf("after 4:59 the run is %q, want requested", got.State)
		}
		e.advance(time.Second)
		e.follow(t)
		got := e.run(t, run.ID)
		if got.State != store.UpdateLost || got.Reason != reasonLostStart || got.Log != "" || !got.FinishedAt.Equal(e.t0.Add(5*time.Minute)) {
			t.Errorf("run = %+v, want lost with the start sentence and no log", got)
		}
		if !strings.Contains(got.Reason, "caravana-update.timer") {
			t.Errorf("reason = %q, want it to name the timer", got.Reason)
		}
		if e.requestExists() {
			t.Error("the request file stays after the run is lost")
		}
	})

	t.Run("running for 45 minutes is lost", func(t *testing.T) {
		e := newUpdEnv(t)
		run := request(t, e)
		e.status(t, "ID="+run.ID, "STATE=running", "STARTED="+e.t0.Format(time.RFC3339))
		e.follow(t)
		e.advance(44 * time.Minute)
		e.follow(t)
		if got := e.run(t, run.ID); got.State != store.UpdateRunning {
			t.Fatalf("at 44 min the run is %q, want running", got.State)
		}
		e.advance(time.Minute)
		e.follow(t)
		got := e.run(t, run.ID)
		if got.State != store.UpdateLost || got.Reason != reasonLostEnd || got.Log != logOf(e, run.ID) {
			t.Errorf("run = %+v, want lost with the end sentence and the log path", got)
		}
		if e.requestExists() {
			t.Error("the request file stays after the run is lost")
		}
	})

	t.Run("running, then the status goes: the 45 minutes still count", func(t *testing.T) {
		e := newUpdEnv(t)
		run := request(t, e)
		e.status(t, "ID="+run.ID, "STATE=running", "STARTED="+e.t0.Format(time.RFC3339))
		e.follow(t)
		if err := os.Remove(filepath.Join(e.dir, "status")); err != nil {
			t.Fatal(err)
		}
		e.advance(10 * time.Minute)
		e.follow(t)
		if got := e.run(t, run.ID); got.State != store.UpdateRunning {
			t.Fatalf("state = %q, want running", got.State)
		}
		e.advance(36 * time.Minute)
		e.follow(t)
		if got := e.run(t, run.ID); got.State != store.UpdateLost {
			t.Errorf("state = %q, want lost", got.State)
		}
	})

	t.Run("a status written by another process between ticks", func(t *testing.T) {
		e := newUpdEnv(t)
		run := request(t, e)
		// Running and final in one gap: the pass goes straight to the final state.
		e.status(t, "ID="+run.ID, "STATE=failed", "STARTED="+e.t0.Format(time.RFC3339),
			"FINISHED="+e.t0.Add(time.Minute).Format(time.RFC3339), "REASON=boom")
		e.follow(t)
		if got := e.run(t, run.ID); got.State != store.UpdateFailed || got.Reason != "boom" {
			t.Errorf("run = %+v", got)
		}
	})

	t.Run("a new Service over the same store carries on mid-run", func(t *testing.T) {
		e := newUpdEnv(t)
		run := request(t, e)
		e.status(t, "ID="+run.ID, "STATE=running", "STARTED="+e.t0.Format(time.RFC3339))
		e.follow(t)

		// Caravana restarts: a new process with the same store, directories and data.
		next := startService(t, e.store, e.registry, e.nomad, e.secrets)
		next.clock.(*fakeClock).Set(e.t0.Add(2 * time.Minute))
		next.SetVersion("0.17.0")
		next.SetUpdaterDir(e.dir)
		next.SetUpdateRequestPath(e.request)
		got, open, err := e.store.OpenUpdateRun(ctx)
		if err != nil || !open || got.ID != run.ID || got.State != store.UpdateRunning {
			t.Fatalf("the open run after the restart = %+v, %v, %v", got, open, err)
		}
		e.status(t, "ID="+run.ID, "VERSION=0.17.0", "FROM=0.16.0", "STATE=updated", "STARTED="+e.t0.Format(time.RFC3339),
			"FINISHED="+e.t0.Add(90*time.Second).Format(time.RFC3339))
		if err := next.followUpdateRun(ctx); err != nil {
			t.Fatal(err)
		}
		if final := e.run(t, run.ID); final.State != store.UpdateUpdated || final.Log != logOf(e, run.ID) {
			t.Errorf("run = %+v, want updated", final)
		}
		if e.requestExists() {
			t.Error("the request file stays")
		}
	})

	t.Run("a rolled-back process finds the request in its copied data", func(t *testing.T) {
		e := newUpdEnv(t)
		run := request(t, e)
		// The old binary runs again with the data as it was when the update began: the run is
		// "requested" there, and the updater's status says what happened.
		e.status(t, "ID="+run.ID, "VERSION=0.17.0", "FROM=0.16.0", "STATE=rolled_back", "STARTED="+e.t0.Format(time.RFC3339),
			"FINISHED="+e.t0.Add(2*time.Minute).Format(time.RFC3339), "REASON=0.17.0 didn't answer")
		next := startService(t, e.store, e.registry, e.nomad, e.secrets)
		next.clock.(*fakeClock).Set(e.t0.Add(3 * time.Minute))
		next.SetVersion("0.16.0")
		next.SetUpdaterDir(e.dir)
		next.SetUpdateRequestPath(e.request)
		if err := next.followUpdateRun(ctx); err != nil {
			t.Fatal(err)
		}
		if got := e.run(t, run.ID); got.State != store.UpdateRolledBack || got.Reason != "0.17.0 didn't answer" {
			t.Errorf("run = %+v", got)
		}
	})

	t.Run("a final run raises the page and needs-you notices", func(t *testing.T) {
		e := newUpdEnv(t)
		run := request(t, e)
		ch, cancel := e.svc.Subscribe("")
		defer cancel()
		e.status(t, "ID="+run.ID, "STATE=failed", "FINISHED="+e.t0.Format(time.RFC3339), "REASON=x")
		e.follow(t)
		if !sawChange(ch, ChangeUpdates) || !sawChange(ch, ChangeAttention) {
			t.Error("a final run must publish updates and attention")
		}
	})

	t.Run("no open run is a cheap pass", func(t *testing.T) {
		e := newUpdEnv(t)
		e.follow(t)
	})
}

// sawChange reads what is already queued on ch, without waiting, and says whether a notice of
// the kind is among it.
func sawChange(ch <-chan Change, what ChangeKind) bool {
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return false
			}
			if c.What == what {
				return true
			}
		default:
			return false
		}
	}
}

func TestAutoUpdateDue(t *testing.T) {
	at3 := time.Date(2026, 11, 7, 3, 20, 0, 0, time.UTC)
	good := autoUpdateInput{
		Now: at3, Enabled: true, Hour: 3, CheckEnabled: true, Running: "0.16.0", Latest: "v0.16.3",
		Installed: true,
	}
	tests := []struct {
		name   string
		mod    func(in *autoUpdateInput)
		want   string
		wantOK bool
	}{
		{name: "everything holds", want: "0.16.3", wantOK: true},
		{name: "off", mod: func(in *autoUpdateInput) { in.Enabled = false }},
		{name: "before the hour", mod: func(in *autoUpdateInput) { in.Now = time.Date(2026, 11, 7, 2, 59, 0, 0, time.UTC) }},
		{name: "after the hour", mod: func(in *autoUpdateInput) { in.Now = time.Date(2026, 11, 7, 4, 0, 0, 0, time.UTC) }},
		{name: "the hour in another zone is read as UTC", mod: func(in *autoUpdateInput) { in.Now = in.Now.In(time.FixedZone("x", 2*3600)) }, want: "0.16.3", wantOK: true},
		{name: "a minor release waits", mod: func(in *autoUpdateInput) { in.Latest = "v0.17.0" }},
		{name: "a major release waits", mod: func(in *autoUpdateInput) { in.Latest = "1.0.0" }},
		{name: "a suffix on the new one", mod: func(in *autoUpdateInput) { in.Latest = "v0.16.3-rc.1" }},
		{name: "a suffix on the running one", mod: func(in *autoUpdateInput) { in.Running = "0.16.0-rc.1" }},
		{name: "the same version", mod: func(in *autoUpdateInput) { in.Latest = "v0.16.0" }},
		{name: "an older release", mod: func(in *autoUpdateInput) { in.Running = "0.16.5" }},
		{name: "the release check is off", mod: func(in *autoUpdateInput) { in.CheckEnabled = false }},
		{name: "the updater is missing", mod: func(in *autoUpdateInput) { in.Installed = false }},
		{name: "a run is open", mod: func(in *autoUpdateInput) { in.Open = true }},
		{name: "a run was made for that version", mod: func(in *autoUpdateInput) { in.Ran = true }},
		{name: "a development build", mod: func(in *autoUpdateInput) { in.Running = "dev" }},
		{name: "a dev release build", mod: func(in *autoUpdateInput) { in.Running = "0.0.0-dev.20261106" }},
		{name: "no release seen", mod: func(in *autoUpdateInput) { in.Latest = "" }},
		{name: "ten is newer than nine", mod: func(in *autoUpdateInput) { in.Running, in.Latest = "0.16.9", "v0.16.10" }, want: "0.16.10", wantOK: true},
		{name: "hour 0", mod: func(in *autoUpdateInput) { in.Hour = 0; in.Now = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC) }, want: "0.16.3", wantOK: true},
		{name: "hour 23", mod: func(in *autoUpdateInput) { in.Hour = 23; in.Now = time.Date(2026, 11, 7, 23, 59, 0, 0, time.UTC) }, want: "0.16.3", wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := good
			if tc.mod != nil {
				tc.mod(&in)
			}
			got, ok := autoUpdateDue(in)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("autoUpdateDue() = %q, %v; want %q, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestAutoUpdateNext(t *testing.T) {
	base := autoUpdateInput{Enabled: true, Hour: 3, CheckEnabled: true, Running: "0.16.0", Latest: "v0.16.3", Installed: true}
	tests := []struct {
		name string
		now  time.Time
		mod  func(in *autoUpdateInput)
		want time.Time
		ok   bool
	}{
		{"later today", time.Date(2026, 11, 6, 1, 30, 0, 0, time.UTC), nil, time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC), true},
		{"tomorrow", time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC), nil, time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC), true},
		{"inside the hour it is now", time.Date(2026, 11, 6, 3, 10, 0, 0, time.UTC), nil, time.Date(2026, 11, 6, 3, 10, 0, 0, time.UTC), true},
		{"a month end", time.Date(2026, 11, 30, 14, 0, 0, 0, time.UTC), nil, time.Date(2026, 12, 1, 3, 0, 0, 0, time.UTC), true},
		{"nothing due: a minor release", time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC), func(in *autoUpdateInput) { in.Latest = "v0.17.0" }, time.Time{}, false},
		{"nothing due: off", time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC), func(in *autoUpdateInput) { in.Enabled = false }, time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.Now = tc.now
			if tc.mod != nil {
				tc.mod(&in)
			}
			got, ok := autoUpdateNext(in)
			if ok != tc.ok || !got.Equal(tc.want) {
				t.Errorf("autoUpdateNext() = %v, %v; want %v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestAutoUpdateRequests(t *testing.T) {
	ctx := context.Background()
	e := newUpdEnv(t)
	e.seen(t, "v0.16.3")
	e.svc.clock.(*fakeClock).Set(time.Date(2026, 11, 7, 3, 5, 0, 0, time.UTC))

	// Off: nothing happens.
	if err := e.svc.autoUpdate(ctx); err != nil {
		t.Fatal(err)
	}
	if runs, _ := e.store.UpdateRuns(ctx, 5); len(runs) != 0 {
		t.Fatalf("an automatic pass with the setting off made %v", runs)
	}

	if err := e.svc.SetAutoUpdate(ctx, true, 3); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.autoUpdate(ctx); err != nil {
		t.Fatal(err)
	}
	runs, _ := e.store.UpdateRuns(ctx, 5)
	if len(runs) != 1 || runs[0].Trigger != store.UpdateByAutomatic || runs[0].Version != "0.16.3" || runs[0].TokenName != "" {
		t.Fatalf("runs = %+v, want one automatic run for 0.16.3", runs)
	}
	if data, err := os.ReadFile(e.request); err != nil || !strings.Contains(string(data), "VERSION=0.16.3\n") {
		t.Errorf("request = %q, %v", data, err)
	}

	// The same pass again, and after the run failed: the version isn't tried again by itself.
	if err := e.svc.autoUpdate(ctx); err != nil {
		t.Fatal(err)
	}
	e.status(t, "ID="+runs[0].ID, "STATE=rolled_back", "STARTED="+e.t0.Format(time.RFC3339), "FINISHED="+e.t0.Format(time.RFC3339), "REASON=x")
	e.follow(t)
	e.advance(24 * time.Hour)
	if err := e.svc.autoUpdate(ctx); err != nil {
		t.Fatal(err)
	}
	if runs, _ := e.store.UpdateRuns(ctx, 5); len(runs) != 1 {
		t.Errorf("a failed version was requested again: %d runs", len(runs))
	}
}

func TestAutoUpdatePassRunsOncePerMinute(t *testing.T) {
	ctx := context.Background()
	e := newUpdEnv(t)
	e.seen(t, "v0.16.3")
	e.svc.clock.(*fakeClock).Set(time.Date(2026, 11, 7, 3, 5, 10, 0, time.UTC))
	if err := e.svc.SetAutoUpdate(ctx, true, 3); err != nil {
		t.Fatal(err)
	}
	e.svc.updatePass(ctx)
	if runs, _ := e.store.UpdateRuns(ctx, 5); len(runs) != 1 {
		t.Fatalf("the pass made %d runs, want 1", len(runs))
	}
	if e.svc.updater.lastAuto.IsZero() {
		t.Error("the pass didn't note the minute it checked")
	}
}

func TestSetAutoUpdateHour(t *testing.T) {
	e := newUpdEnv(t)
	for _, hour := range []int{-1, 24, 99} {
		var fe *app.FieldError
		if err := e.svc.SetAutoUpdate(context.Background(), true, hour); !errors.As(err, &fe) || fe.Field != "autoUpdateHour" {
			t.Errorf("hour %d: err = %v, want a FieldError on autoUpdateHour", hour, err)
		}
	}
	if err := e.svc.SetAutoUpdate(context.Background(), true, 23); err != nil {
		t.Errorf("hour 23: %v", err)
	}
}
