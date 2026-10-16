package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func TestNewVolumeRequest(t *testing.T) {
	now := time.Date(2026, 10, 15, 3, 0, 7, 400_000_000, time.UTC)
	tests := []struct {
		name                    string
		service, action, object string
		limit, grace            time.Duration
		want                    volumeRequest
	}{
		{
			name: "a backup", service: "web", action: "backup", limit: 30 * time.Minute, grace: 5 * time.Minute,
			want: volumeRequest{ID: "web-1792033207", Action: "backup", TS: "1792033207", NotAfter: now.Add(35 * time.Minute), Limit: 30 * time.Minute},
		},
		{
			name: "a restore names the backup", service: "db-1", action: "restore", object: "b/kuma/web/1791/",
			limit: time.Minute, grace: time.Second,
			want: volumeRequest{ID: "db-1-1792033207", Action: "restore", Object: "b/kuma/web/1791/", TS: "1792033207",
				NotAfter: now.Add(time.Minute + time.Second), Limit: time.Minute},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newVolumeRequest(tt.service, tt.action, tt.object, now, tt.limit, tt.grace)
			if got != tt.want {
				t.Errorf("newVolumeRequest = %+v\nwant %+v", got, tt.want)
			}
			// The ID ends in the timestamp the script names its directory by.
			if !strings.HasSuffix(got.ID, "-"+got.TS) {
				t.Errorf("ID %q doesn't end in TS %q", got.ID, got.TS)
			}
			n := got.forNomad()
			if n.ID != got.ID || n.Action != got.Action || n.TS != got.TS || n.Object != got.Object || !n.NotAfter.Equal(got.NotAfter) || n.Limit != got.Limit {
				t.Errorf("forNomad = %+v, want the same fields as %+v", n, got)
			}
		})
	}
}

// volumeLab is an env with a volume-backed app deployed and one running copy,
// "c1", whose caravana-volumes task the test plays: it renders the request
// file, and answers a restart by writing a result file.
type volumeLab struct {
	*env
	t     *testing.T
	t0    time.Time
	clock *fakeClock
}

const (
	labCopy = "c1"
	labVar  = "nomad/jobs/whoami/web/caravana-volumes"
)

func newVolumeLab(t *testing.T) *volumeLab {
	t.Helper()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, kuma(false))
	e.nomad.SetCopies("whoami", api.AllocationListStub{ID: labCopy, TaskGroup: "web", ClientStatus: "running", JobVersion: 1})
	e.nomad.SetRestartStop(20 * time.Millisecond)
	clock := e.svc.clock.(*fakeClock)
	return &volumeLab{env: e, t: t, t0: clock.Now(), clock: clock}
}

// id is the ID of the n-th run of the clock's day (the fake clock doesn't move,
// so a second run in the same second takes the next second's ID).
func (l *volumeLab) id(n int) string { return fmt.Sprintf("web-%d", l.t0.Unix()+int64(n)) }

// render plays the template: the copy's request file shows the request.
func (l *volumeLab) render(id string) {
	l.nomad.WriteAllocFile(labCopy, "caravana-volumes/local/request.env", "ACTION='backup'\nREQUEST='"+id+"'\n")
}

// answer makes a restart of the copy leave result as the run's result file.
func (l *volumeLab) answer(id, result string) {
	l.nomad.OnRestart(func(copyID string) {
		l.nomad.WriteAllocFile(copyID, "alloc/data/caravana/"+id+".result", result)
	})
}

// settled waits until the backup is no longer running and returns it.
func (l *volumeLab) settled(id string) store.BackupRecord {
	l.t.Helper()
	var rec store.BackupRecord
	waitFor(l.t, "backup "+id+" to settle", func() bool {
		b, err := l.store.GetBackup(context.Background(), "whoami", id)
		rec = b
		return err == nil && b.Status != store.BackupRunning
	})
	return rec
}

// released waits for the run's goroutine to be done with the lock, by taking
// the lock the way a deploy does, and checks nothing else holds it.
func (l *volumeLab) released() {
	l.t.Helper()
	deployHealthy(l.t, l.env, kuma(false))
}

// request is the request the variable holds now ("" for none).
func (l *volumeLab) request() string {
	l.t.Helper()
	v := l.nomad.Variable(labVar)
	if v == nil {
		l.t.Fatal("the task's variable is gone")
	}
	return v["REQUEST"]
}

const labSuccess = "status=succeeded\nmessage=\nobject=backups/caravana/whoami/web/1791990000/\n" +
	"volume.data.bytes=2000\nvolume.data.size=5000\nvolume.uploads.bytes=300\nvolume.uploads.size=900\nexpired=1700000000\n"

func TestVolumeBackupRun(t *testing.T) {
	ctx := context.Background()

	t.Run("success records size, volumes, stop time and what retention removed", func(t *testing.T) {
		l := newVolumeLab(t)
		id := l.id(0)
		l.render(id)
		l.answer(id, labSuccess)
		old := store.BackupRecord{AppID: "whoami", ID: "web-1700000000", Service: "web", Engine: app.BackupVolume,
			Status: store.BackupSucceeded, Object: "backups/caravana/whoami/web/1700000000/", StartedAt: l.t0.Add(-100 * time.Hour)}
		if err := l.store.SaveBackup(ctx, old); err != nil {
			t.Fatal(err)
		}

		rec, err := l.svc.BackUpNow(ctx, "whoami", "web")
		if err != nil {
			t.Fatal(err)
		}
		if rec.ID != id || rec.Status != store.BackupRunning || rec.Engine != app.BackupVolume ||
			rec.JobID != fmt.Sprintf("whoami_web_volumes/%d", l.t0.Unix()) {
			t.Fatalf("started record = %+v", rec)
		}
		got := l.settled(id)
		if got.Status != store.BackupSucceeded || got.Message != "" || got.Object != "backups/caravana/whoami/web/1791990000/" {
			t.Fatalf("settled record = %+v", got)
		}
		if got.Bytes != 2300 || len(got.Volumes) != 2 || got.Volumes[0] != (store.VolumeArchive{Name: "data", Bytes: 2000, SizeKB: 5000}) {
			t.Errorf("bytes %d, volumes %+v; want the sum 2300 and both volumes", got.Bytes, got.Volumes)
		}
		if got.Stopped == nil || *got.Stopped != 20*time.Millisecond {
			t.Errorf("stopped = %v, want 20ms from the task's events", got.Stopped)
		}
		if got.FinishedAt == nil {
			t.Error("no finish time")
		}
		if o, _ := l.store.GetBackup(ctx, "whoami", "web-1700000000"); !o.Expired {
			t.Error("the backup retention removed isn't marked expired")
		}
		sizes, err := l.store.VolumeSizes(ctx, "whoami")
		if err != nil || sizes["data"].KB != 5000 || sizes["uploads"].KB != 900 {
			t.Errorf("volume sizes = %+v, %v; want the run's", sizes, err)
		}

		// The copy was restarted once, whole, and the job didn't move.
		if rs := l.nomad.Restarts(); len(rs) != 1 || rs[0].Copy != labCopy || !rs[0].AllTasks {
			t.Errorf("restarts = %+v, want one restart of all tasks of %s", rs, labCopy)
		}
		// The request is cleared and the destination stays for the next run.
		l.released()
		if l.request() != "" {
			t.Errorf("request = %q after the run, want none", l.request())
		}
		if v := l.nomad.Variable(labVar); v["KEEP"] != "3" || v["BACKUP_PATH"] != "backups/caravana/whoami/web" || v["RCLONE_CONFIG_DEST_ACCESS_KEY_ID"] != "AK" {
			t.Errorf("variable after the run = %v, want the destination and KEEP kept", v)
		}
	})

	t.Run("while it runs the variable holds the request", func(t *testing.T) {
		l := newVolumeLab(t)
		id := l.id(0)
		l.render(id)
		inRestart, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { once.Do(func() { close(release) }) })
		l.nomad.OnRestart(func(copyID string) {
			close(inRestart)
			<-release
			l.nomad.WriteAllocFile(copyID, "alloc/data/caravana/"+id+".result", "status=succeeded\n")
		})
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		<-inRestart
		v := l.nomad.Variable(labVar)
		if v["REQUEST"] != id || v["ACTION"] != "backup" || v["TS"] != fmt.Sprint(l.t0.Unix()) || v["NOT_AFTER"] == "" || v["LIMIT"] != "60" {
			t.Errorf("variable during the run = %v", v)
		}
		once.Do(func() { close(release) })
		if got := l.settled(id); got.Status != store.BackupSucceeded {
			t.Errorf("record = %+v", got)
		}
		l.released()
	})

	t.Run("a failed result records its message", func(t *testing.T) {
		l := newVolumeLab(t)
		id := l.id(0)
		l.render(id)
		l.answer(id, "status=failed\nmessage=upload of logs: boom\n")
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		got := l.settled(id)
		if got.Status != store.BackupFailed || got.Message != "upload of logs: boom" || got.Bytes != 0 || len(got.Volumes) != 0 || got.Object != "" {
			t.Errorf("record = %+v, want failed with the message and no archives", got)
		}
		if got.Stopped == nil {
			t.Error("a failed run still records how long the service was stopped")
		}
		l.released()
		if l.request() != "" {
			t.Errorf("request = %q, want it cleared", l.request())
		}
	})

	t.Run("no result in time is a timeout, with the request cleared and the lock released", func(t *testing.T) {
		l := newVolumeLab(t)
		l.svc.volume.limit = 20 * time.Millisecond // and the grace, 150 ms, is all there is to wait
		id := l.id(0)
		l.render(id)
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		got := l.settled(id)
		if got.Status != store.BackupFailed || !strings.Contains(got.Message, "timed out") {
			t.Errorf("record = %+v, want failed with a timeout", got)
		}
		if got.Stopped == nil || *got.Stopped < 150*time.Millisecond {
			t.Errorf("stopped = %v, want at least what was waited", got.Stopped)
		}
		l.released()
		if l.request() != "" {
			t.Errorf("request = %q, want it cleared", l.request())
		}
	})

	t.Run("a request the copy never rendered fails the run without a restart", func(t *testing.T) {
		l := newVolumeLab(t) // nothing renders the request
		rec, err := l.svc.BackUpNow(ctx, "whoami", "web")
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != store.BackupFailed || !strings.Contains(rec.Message, "didn't pick up the request") || rec.FinishedAt == nil {
			t.Errorf("record = %+v, want failed because the copy didn't pick up the request", rec)
		}
		if got, err := l.store.GetBackup(ctx, "whoami", rec.ID); err != nil || got.Status != store.BackupFailed {
			t.Errorf("stored record = %+v, %v; want failed", got, err)
		}
		if rs := l.nomad.Restarts(); len(rs) != 0 {
			t.Errorf("restarts = %+v, want none: the copy would run nothing", rs)
		}
		l.released()
		if l.request() != "" {
			t.Errorf("request = %q, want it cleared", l.request())
		}
	})

	t.Run("a restart Nomad refuses fails the run and clears the request", func(t *testing.T) {
		l := newVolumeLab(t)
		id := l.id(0)
		l.render(id)
		l.nomad.FailRestart(403)
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		got := l.settled(id)
		if got.Status != store.BackupFailed || !strings.Contains(got.Message, "alloc-lifecycle") {
			t.Errorf("record = %+v, want failed, naming the missing capability", got)
		}
		l.released()
		if l.request() != "" {
			t.Errorf("request = %q, want it cleared", l.request())
		}
	})

	t.Run("no running copy is refused, and leaves nothing behind", func(t *testing.T) {
		l := newVolumeLab(t)
		l.nomad.SetCopies("whoami")
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); !errors.Is(err, ErrNoRunningCopy) {
			t.Fatalf("BackUpNow = %v, want ErrNoRunningCopy", err)
		}
		if backups, _ := l.store.ListBackups(ctx, "whoami", 0); len(backups) != 0 {
			t.Errorf("records = %+v, want none", backups)
		}
		l.released()
		if l.request() != "" {
			t.Errorf("request = %q, want none", l.request())
		}
		// A copy that is only starting isn't one either.
		l.nomad.SetCopies("whoami", api.AllocationListStub{ID: labCopy, TaskGroup: "web", ClientStatus: "pending", JobVersion: 1})
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); !errors.Is(err, ErrNoRunningCopy) {
			t.Errorf("BackUpNow with a starting copy = %v, want ErrNoRunningCopy", err)
		}
	})

	t.Run("a second run during the first is refused at once", func(t *testing.T) {
		l := newVolumeLab(t)
		id := l.id(0)
		l.render(id)
		inRestart, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { once.Do(func() { close(release) }) })
		l.nomad.OnRestart(func(copyID string) {
			close(inRestart)
			<-release
			l.nomad.WriteAllocFile(copyID, "alloc/data/caravana/"+id+".result", "status=succeeded\n")
		})
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		<-inRestart
		done := make(chan error, 1)
		go func() { _, err := l.svc.BackUpNow(ctx, "whoami", "web"); done <- err }()
		select {
		case err := <-done:
			if !errors.Is(err, ErrBackupRunning) {
				t.Errorf("second BackUpNow = %v, want ErrBackupRunning", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the second BackUpNow waited for the first run instead of being refused")
		}
		once.Do(func() { close(release) })
		l.settled(id)
		l.released()
	})

	t.Run("a deploy during a run waits until it ends", func(t *testing.T) {
		l := newVolumeLab(t)
		id := l.id(0)
		l.render(id)
		inRestart, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { once.Do(func() { close(release) }) })
		l.nomad.OnRestart(func(copyID string) {
			close(inRestart)
			<-release
			l.nomad.WriteAllocFile(copyID, "alloc/data/caravana/"+id+".result", "status=succeeded\n")
		})
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		<-inRestart
		registered := l.nomad.Registrations()

		type deployed struct {
			rec store.DeploymentRecord
			err error
		}
		done := make(chan deployed, 1)
		go func() {
			rec, err := l.svc.Deploy(ctx, kuma(false), app.TriggerManual)
			done <- deployed{rec, err}
		}()
		select {
		case d := <-done:
			t.Fatalf("the deploy went ahead during the run: %+v, %v", d.rec, d.err)
		case <-time.After(150 * time.Millisecond):
		}
		if got := l.nomad.Registrations(); got != registered {
			t.Fatalf("registrations went from %d to %d during the run", registered, got)
		}

		once.Do(func() { close(release) })
		select {
		case d := <-done:
			if d.err != nil {
				t.Fatalf("deploy after the run: %v", d.err)
			}
			if final := waitFinal(t, l.store, d.rec.Deployment.ID); final.Status != store.StatusHealthy {
				t.Errorf("deployment = %s (%s), want healthy", final.Status, final.Error)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the deploy never went ahead after the run")
		}
		if got := l.nomad.Registrations(); got != registered+1 {
			t.Errorf("registrations = %d, want %d: the deploy registers once the run is over", got, registered+1)
		}
		if got := l.settled(id); got.Status != store.BackupSucceeded {
			t.Errorf("record = %+v", got)
		}
	})

	t.Run("deleting the app during a run waits for it, and doesn't deadlock", func(t *testing.T) {
		l := newVolumeLab(t)
		id := l.id(0)
		l.render(id)
		inRestart, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { once.Do(func() { close(release) }) })
		l.nomad.OnRestart(func(copyID string) {
			close(inRestart)
			<-release
			l.nomad.WriteAllocFile(copyID, "alloc/data/caravana/"+id+".result", "status=succeeded\n")
		})
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		<-inRestart
		done := make(chan error, 1)
		go func() { done <- l.svc.DeleteApp(ctx, "whoami") }()
		select {
		case err := <-done:
			t.Fatalf("DeleteApp finished during the run: %v", err)
		case <-time.After(150 * time.Millisecond):
		}
		if _, err := l.store.GetApp(ctx, "whoami"); err != nil {
			t.Fatalf("the app is gone during the run: %v", err)
		}
		once.Do(func() { close(release) })
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("DeleteApp after the run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("DeleteApp never finished after the run ended")
		}
		if _, err := l.store.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp after the delete = %v, want ErrNotFound", err)
		}
	})

	t.Run("two runs in the same second get their own IDs", func(t *testing.T) {
		l := newVolumeLab(t)
		for n := 0; n < 2; n++ {
			id := l.id(n)
			l.render(id)
			l.answer(id, "status=succeeded\n")
			rec, err := l.svc.BackUpNow(ctx, "whoami", "web")
			if err != nil {
				t.Fatalf("run %d: %v", n, err)
			}
			if rec.ID != id {
				t.Fatalf("run %d: ID = %s, want %s", n, rec.ID, id)
			}
			l.settled(id)
			l.released()
		}
	})
}

// A postgres service keeps its Nomad job path, and a service that has no
// volume backup is refused as before.
func TestBackUpNowKeepsTheOtherPaths(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, db())
	rec, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil || rec.Engine != "postgres" && rec.Engine != "" {
		t.Fatalf("BackUpNow of a postgres service = %+v, %v", rec, err)
	}
	if _, err := e.svc.BackUpNow(ctx, "whoami", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("BackUpNow of an unknown service = %v, want ErrNotFound", err)
	}
}

// Caravana restarting during a run settles the run: from the result file if
// the copy has one, and as failed if it has not.
func TestResumeVolumeBackups(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		startedAgo time.Duration
		copy       bool
		result     string // the result file; "" for none
		wantStatus string
		wantMsg    string
	}{
		{name: "the result file is there", startedAgo: time.Second, copy: true, result: labSuccess,
			wantStatus: store.BackupSucceeded},
		{name: "a failed result file", startedAgo: time.Second, copy: true, result: "status=failed\nmessage=boom\n",
			wantStatus: store.BackupFailed, wantMsg: "boom"},
		{name: "no result and the run's time is up", startedAgo: 10 * time.Minute, copy: true,
			wantStatus: store.BackupFailed, wantMsg: restartedMessage},
		{name: "the copy is gone", startedAgo: time.Second, copy: false,
			wantStatus: store.BackupFailed, wantMsg: restartedMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := newVolumeLab(t)
			id := l.id(0)
			started := time.Now().Add(-tt.startedAgo).UTC()
			rec := store.BackupRecord{AppID: "whoami", ID: id, Service: "web", Engine: app.BackupVolume,
				Status: store.BackupRunning, StartedAt: started}
			if err := l.store.SaveBackup(ctx, rec); err != nil {
				t.Fatal(err)
			}
			// The run left its request in the variable.
			items := l.nomad.Variable(labVar)
			items["REQUEST"], items["ACTION"] = id, "backup"
			l.nomad.SetVariable(labVar, items)
			if !tt.copy {
				l.nomad.SetCopies("whoami")
			}
			if tt.result != "" {
				l.nomad.WriteAllocFile(labCopy, "alloc/data/caravana/"+id+".result", tt.result)
			}

			startService(t, l.store, l.registry, l.nomad, l.secrets) // the new run of Caravana
			got := l.settled(id)
			if got.Status != tt.wantStatus || got.Message != tt.wantMsg {
				t.Errorf("record = %+v, want %s %q", got, tt.wantStatus, tt.wantMsg)
			}
			if tt.wantStatus == store.BackupSucceeded && (got.Bytes != 2300 || len(got.Volumes) != 2) {
				t.Errorf("a settled success lacks its archives: %+v", got)
			}
			waitFor(t, "the request to be cleared", func() bool { return l.nomad.Variable(labVar)["REQUEST"] == "" })
			// The lock was taken and given back: a deploy goes through.
			l.released()
		})
	}
}

// The first service never runs two settles at once, and an app with no run left
// running starts nothing.
func TestResumeVolumeBackupsIgnoresOthers(t *testing.T) {
	ctx := context.Background()
	l := newVolumeLab(t)
	// A postgres-style record, a finished volume record: neither is resumed.
	for _, rec := range []store.BackupRecord{
		{AppID: "whoami", ID: "web-1", Service: "web", Status: store.BackupRunning, StartedAt: l.t0}, // postgres: Nomad's watcher
		{AppID: "whoami", ID: "web-2", Service: "web", Engine: app.BackupVolume, Status: store.BackupSucceeded, StartedAt: l.t0},
	} {
		if err := l.store.SaveBackup(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	startService(t, l.store, l.registry, l.nomad, l.secrets)
	time.Sleep(50 * time.Millisecond)
	if got, _ := l.store.GetBackup(ctx, "whoami", "web-1"); got.Status != store.BackupRunning {
		t.Errorf("a postgres record was settled by the volume resume: %+v", got)
	}
	if got, _ := l.store.GetBackup(ctx, "whoami", "web-2"); got.Status != store.BackupSucceeded {
		t.Errorf("a finished volume record changed: %+v", got)
	}
}
