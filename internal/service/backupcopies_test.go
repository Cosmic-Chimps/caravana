package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// copyEnv is an env with the destinations main (the default, made by setDestination) and offsite, and
// the postgres service of db() copying its backups to offsite.
func newCopyEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	setDestination(t, e)
	saveDestination(t, e, "offsite", "SK-offsite")
	deployHealthy(t, e, dbWith(func(b *app.Backup) { b.CopyTo = []string{"offsite"} }))
	return e
}

// seedBackup records a succeeded backup of whoami/web in main, with a copy to offsite pending when
// copyTo is true, and returns it. n gives it a stamp of its own.
func seedBackup(t *testing.T, e *env, n int, copyTo bool) store.BackupRecord {
	t.Helper()
	ctx := context.Background()
	stamp := 1700000000 + n
	done := time.Date(2026, 11, 23, 3, 0, n, 0, time.UTC)
	rec := store.BackupRecord{AppID: "whoami", ID: fmt.Sprintf("web-%d", stamp), Service: "web", Engine: app.BackupPostgres,
		Status: store.BackupSucceeded, Object: fmt.Sprintf("backups/caravana/whoami/web/%d.dump", stamp), Bytes: 100,
		StartedAt: done, FinishedAt: &done, Destination: "main"}
	if err := e.store.SaveBackup(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if copyTo {
		if err := e.store.QueueBackupCopies(ctx, "whoami", rec.ID, []string{"offsite"}); err != nil {
			t.Fatal(err)
		}
	}
	return rec
}

// copyOf is the stored copy of backup id to offsite.
func copyOf(t *testing.T, e *env, id string) store.BackupCopy {
	t.Helper()
	copies, err := e.store.ListBackupCopies(context.Background(), "whoami")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range copies[id] {
		if c.Destination == "offsite" {
			return c
		}
	}
	t.Fatalf("no copy of %s to offsite", id)
	return store.BackupCopy{}
}

// freezeCopies keeps the watcher's own copy passes out while the test plays Nomad: the watcher
// runs one every few milliseconds, which the test's passes would otherwise race. The test
// makes its passes with passNow. The lock is given back when the test ends.
func freezeCopies(t *testing.T, e *env) {
	t.Helper()
	e.svc.copyMu.Lock()
	t.Cleanup(e.svc.copyMu.Unlock)
}

// passNow is one copy pass, for a test that froze the watcher's.
func passNow(e *env) { e.svc.runCopyPass(context.Background()) }

func copyJobs(e *env) []string { return e.nomad.JobIDs("whoami_web_copy-") }

// A dump backup that succeeds is copied to the destination its policy names: a job is made
// with both destinations' keys in its own variable, and when it ends the copy is recorded, the
// job and its variable are gone, and what the copy's retention removed is marked.
func TestBackupIsCopiedToItsDestination(t *testing.T) {
	ctx := context.Background()
	e := newCopyEnv(t)
	older := seedBackup(t, e, 0, false)
	// The older copy is queued and marked copied with the watcher's passes held off: a pass between
	// the two writes would start a job for it (a race the release's -race run hit on 2026-11-25).
	e.svc.copyMu.Lock()
	if err := e.store.QueueBackupCopies(ctx, "whoami", older.ID, []string{"offsite"}); err != nil {
		e.svc.copyMu.Unlock()
		t.Fatal(err)
	}
	err := e.store.SaveBackupCopy(ctx, store.BackupCopy{AppID: "whoami", BackupID: older.ID, Destination: "offsite",
		Status: store.CopySucceeded, Object: "offsite-bucket/caravana/whoami/web/1700000000.dump"})
	e.svc.copyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	rec, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	stamp := rec.JobID[strings.LastIndex(rec.JobID, "-")+1:]
	e.nomad.FinishRun(rec.JobID, true, "", "caravana-backup key=backups/caravana/whoami/web/"+stamp+".dump bytes=77 dest=main\n", "")

	// The watcher records the backup and queues its copy, and the pass starts the job.
	waitFor(t, "the copy to be running", func() bool {
		copies, err := e.store.ListBackupCopies(ctx, "whoami")
		return err == nil && len(copies[rec.ID]) == 1 && copies[rec.ID][0].Status == store.CopyRunning
	})
	jobID := copyJobs(e)[0]
	c := copyOf(t, e, rec.ID)
	if c.Status != store.CopyRunning || c.JobID != jobID || c.StartedAt == nil {
		t.Fatalf("copy = %+v, want running with its job %s", c, jobID)
	}
	v := e.nomad.Variable("nomad/jobs/" + jobID)
	if v["SRC_OBJECT"] != "backups/caravana/whoami/web/"+stamp+".dump" || v["DEST_OBJECT"] != "offsite-bucket/caravana/whoami/web/"+stamp+".dump" ||
		v["DEST_PATH"] != "offsite-bucket/caravana/whoami/web" || v["KEEP"] != "2" {
		t.Errorf("variable = %v", v)
	}
	if v["RCLONE_CONFIG_SRC_SECRET_ACCESS_KEY"] != "SK-"+marker || v["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK-offsite" {
		t.Error("the variable doesn't hold both destinations' keys")
	}
	job := e.nomad.Job(jobID)
	if raw := mustMarshal(t, job); strings.Contains(raw, "SK-") {
		t.Errorf("the job holds a key: %s", raw)
	}
	if job.TaskGroups[0].Tasks[0].Env["KIND"] != "file" || job.TaskGroups[0].Tasks[0].Env["EXT"] != ".dump" {
		t.Errorf("env = %v", job.TaskGroups[0].Tasks[0].Env)
	}

	e.nomad.FinishRun(jobID, true, "", "caravana-copy: done key=offsite-bucket/caravana/whoami/web/"+stamp+".dump bytes=77\ncaravana-copy: expired 1700000000.dump\n", "")
	waitFor(t, "the copy to be recorded", func() bool { return copyOf(t, e, rec.ID).Status == store.CopySucceeded })
	c = copyOf(t, e, rec.ID)
	if c.Object != "offsite-bucket/caravana/whoami/web/"+stamp+".dump" || c.Bytes != 77 || c.FinishedAt == nil || c.Message != "" {
		t.Errorf("copy = %+v", c)
	}
	if !copyOf(t, e, older.ID).Expired {
		t.Error("what the copy's retention deleted isn't marked expired")
	}
	waitFor(t, "the copy's job to be purged", func() bool { return e.nomad.Purged(jobID) && e.nomad.Variable("nomad/jobs/"+jobID) == nil })

	// The backup is recorded with its copy, and no second copy is queued by a later pass.
	e.svc.copyPass(ctx)
	got, err := e.store.GetBackup(ctx, "whoami", rec.ID)
	if err != nil || len(got.Copies) != 1 || got.Destination != "main" {
		t.Errorf("backup = %+v, %v", got, err)
	}
	if len(copyJobs(e)) != 0 {
		t.Errorf("jobs = %v, want none: the copy is done", copyJobs(e))
	}
}

func TestOnlyASucceededBackupWithCopiesQueuesThem(t *testing.T) {
	ctx := context.Background()
	e := newCopyEnv(t)
	failed, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	e.nomad.FinishRun(failed.JobID, false, "dump", "", "pg_dump: error: boom\n")
	waitFor(t, "the failure to be recorded", func() bool {
		b, err := e.store.GetBackup(ctx, "whoami", failed.ID)
		return err == nil && b.Status == store.BackupFailed
	})
	if b, _ := e.store.GetBackup(ctx, "whoami", failed.ID); len(b.Copies) != 0 {
		t.Errorf("a failed backup has copies: %+v", b.Copies)
	}

	// A policy without copies queues none.
	deployHealthy(t, e, db())
	plain, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	e.nomad.FinishRun(plain.JobID, true, "", "caravana-backup key=backups/caravana/whoami/web/9.dump bytes=1 dest=main\n", "")
	waitFor(t, "the backup to be recorded", func() bool {
		b, err := e.store.GetBackup(ctx, "whoami", plain.ID)
		return err == nil && b.Status == store.BackupSucceeded
	})
	e.svc.copyPass(ctx)
	if b, _ := e.store.GetBackup(ctx, "whoami", plain.ID); len(b.Copies) != 0 || len(copyJobs(e)) != 0 {
		t.Errorf("copies = %+v, jobs = %v; want none", b.Copies, copyJobs(e))
	}
}

// At most two copies run at once, oldest first; the rest wait for a pass after one ends.
func TestCopiesRunTwoAtATime(t *testing.T) {
	e := newCopyEnv(t)
	freezeCopies(t, e)
	var backups []store.BackupRecord
	for n := range 3 {
		backups = append(backups, seedBackup(t, e, n, true))
	}
	passNow(e)
	jobs := copyJobs(e)
	if len(jobs) != 2 {
		t.Fatalf("jobs = %v, want 2 running", jobs)
	}
	if copyOf(t, e, backups[0].ID).Status != store.CopyRunning || copyOf(t, e, backups[1].ID).Status != store.CopyRunning ||
		copyOf(t, e, backups[2].ID).Status != store.CopyPending {
		t.Errorf("statuses = %s, %s, %s; want the oldest two running and the newest pending",
			copyOf(t, e, backups[0].ID).Status, copyOf(t, e, backups[1].ID).Status, copyOf(t, e, backups[2].ID).Status)
	}
	passNow(e)
	if len(copyJobs(e)) != 2 {
		t.Errorf("a pass with two running started another: %v", copyJobs(e))
	}

	// One ends: its slot goes to the third in the same pass.
	first := copyOf(t, e, backups[0].ID)
	e.nomad.FinishRun(first.JobID, true, "", "caravana-copy: done key=o bytes=1\n", "")
	passNow(e)
	if copyOf(t, e, backups[0].ID).Status != store.CopySucceeded || copyOf(t, e, backups[2].ID).Status != store.CopyRunning {
		t.Errorf("statuses = %s and %s; want the first done and the third started",
			copyOf(t, e, backups[0].ID).Status, copyOf(t, e, backups[2].ID).Status)
	}
}

func TestCopiesThatCannotBeMade(t *testing.T) {
	ctx := context.Background()

	t.Run("a backup retention already deleted", func(t *testing.T) {
		e := newCopyEnv(t)
		freezeCopies(t, e)
		b := seedBackup(t, e, 0, true)
		if err := e.store.ExpireBackups(ctx, "whoami", []string{b.ID}); err != nil {
			t.Fatal(err)
		}
		passNow(e)
		c := copyOf(t, e, b.ID)
		if c.Status != store.CopyFailed || c.Message != "the backup was deleted from main by retention before it was copied" || len(copyJobs(e)) != 0 {
			t.Errorf("copy = %+v, jobs %v", c, copyJobs(e))
		}
	})
	t.Run("a destination that was removed", func(t *testing.T) {
		e := newCopyEnv(t)
		freezeCopies(t, e)
		b := seedBackup(t, e, 0, true)
		// The service refuses to remove a destination in use, so the store does it.
		if err := e.store.DeleteBackupDestination(ctx, "offsite"); err != nil {
			t.Fatal(err)
		}
		passNow(e)
		if c := copyOf(t, e, b.ID); c.Status != store.CopyFailed || c.Message != "the destination offsite was removed" {
			t.Errorf("copy = %+v", c)
		}
	})
	t.Run("a service that lost its policy", func(t *testing.T) {
		e := newCopyEnv(t)
		freezeCopies(t, e)
		b := seedBackup(t, e, 0, true)
		plain := db()
		plain.Services[0].Backup = nil
		deployHealthy(t, e, plain)
		passNow(e)
		if c := copyOf(t, e, b.ID); c.Status != store.CopyFailed || c.Message != "the service no longer has a backup policy" {
			t.Errorf("copy = %+v", c)
		}
	})
	t.Run("a backup that is already in the destination", func(t *testing.T) {
		e := newCopyEnv(t)
		freezeCopies(t, e)
		b := seedBackup(t, e, 0, true)
		b.Destination = "offsite"
		if err := e.store.SaveBackup(ctx, b); err != nil {
			t.Fatal(err)
		}
		passNow(e)
		if c := copyOf(t, e, b.ID); c.Status != store.CopyFailed || c.Message != "the backup is already in offsite" {
			t.Errorf("copy = %+v", c)
		}
	})
	t.Run("a backup whose object is not under the source's bucket any more", func(t *testing.T) {
		e := newCopyEnv(t)
		freezeCopies(t, e)
		b := seedBackup(t, e, 0, true)
		b.Object = "old-bucket/x/1700000000.dump"
		if err := e.store.SaveBackup(ctx, b); err != nil {
			t.Fatal(err)
		}
		passNow(e)
		if c := copyOf(t, e, b.ID); c.Status != store.CopyFailed || !strings.Contains(c.Message, "isn't under the bucket and prefix") {
			t.Errorf("copy = %+v", c)
		}
	})
	t.Run("Nomad refuses the job", func(t *testing.T) {
		e := newCopyEnv(t)
		freezeCopies(t, e)
		b := seedBackup(t, e, 0, true)
		e.nomad.RejectNext(400, "job rejected")
		passNow(e)
		c := copyOf(t, e, b.ID)
		if c.Status != store.CopyFailed || !strings.HasPrefix(c.Message, "Nomad refused the copy") {
			t.Errorf("copy = %+v", c)
		}
		if paths := e.nomad.VariablePaths(); len(paths) != 0 && strings.Contains(strings.Join(paths, " "), "_copy-") {
			t.Errorf("a refused job left its variable: %v", paths)
		}
	})
}

func TestCopiesThatFail(t *testing.T) {
	ctx := context.Background()

	t.Run("the job fails: the message is rclone's, with no key, and the job is purged", func(t *testing.T) {
		e := newCopyEnv(t)
		freezeCopies(t, e)
		b := seedBackup(t, e, 0, true)
		passNow(e)
		c := copyOf(t, e, b.ID)
		e.nomad.FinishRun(c.JobID, false, "copy", "", "Failed to copyto: AccessDenied: SK-offsite is not allowed\n")
		passNow(e)
		c = copyOf(t, e, b.ID)
		if c.Status != store.CopyFailed || !strings.Contains(c.Message, "AccessDenied") || strings.Contains(c.Message, "SK-") {
			t.Errorf("copy = %+v", c)
		}
		if !e.nomad.Purged(c.JobID) || e.nomad.Variable("nomad/jobs/"+c.JobID) != nil {
			t.Error("the failed copy's job or variable is still there")
		}
	})
	t.Run("a copy that runs for six hours is failed", func(t *testing.T) {
		e := newCopyEnv(t)
		freezeCopies(t, e)
		b := seedBackup(t, e, 0, true)
		passNow(e)
		c := copyOf(t, e, b.ID)
		clock := e.svc.clock.(*fakeClock)
		clock.Set(clock.Now().Add(copyLimit - time.Minute))
		passNow(e)
		if got := copyOf(t, e, b.ID); got.Status != store.CopyRunning {
			t.Fatalf("copy = %+v, want it still running before the limit", got)
		}
		clock.Set(clock.Now().Add(2 * time.Minute))
		passNow(e)
		got := copyOf(t, e, b.ID)
		if got.Status != store.CopyFailed || got.Message != "the copy didn't finish in 6h0m0s" || !e.nomad.Purged(c.JobID) {
			t.Errorf("copy = %+v, purged %v", got, e.nomad.Purged(c.JobID))
		}
	})
	t.Run("a job Nomad has forgotten", func(t *testing.T) {
		e := newCopyEnv(t)
		freezeCopies(t, e)
		b := seedBackup(t, e, 0, true)
		passNow(e)
		c := copyOf(t, e, b.ID)
		if err := e.svc.deployer.PurgeBackupCopy(ctx, c.JobID); err != nil {
			t.Fatal(err)
		}
		passNow(e)
		if got := copyOf(t, e, b.ID); got.Status != store.CopyFailed || got.Message != "Nomad has no record of this copy" {
			t.Errorf("copy = %+v", got)
		}
	})
}

// A restart of Caravana loses nothing: a copy that ended while it was down is
// settled by the new process, and a pending one is started.
func TestCopiesSurviveARestart(t *testing.T) {
	ctx := context.Background()
	e := newCopyEnv(t)
	freezeCopies(t, e)
	running := seedBackup(t, e, 0, true)
	passNow(e)
	runningCopy := copyOf(t, e, running.ID)
	pending := seedBackup(t, e, 1, true)
	extra := seedBackup(t, e, 2, true) // takes the second slot...
	passNow(e)
	blocked := seedBackup(t, e, 3, true) // ...and this one has to wait
	if copyOf(t, e, blocked.ID).Status != store.CopyPending || copyOf(t, e, pending.ID).Status != store.CopyRunning ||
		copyOf(t, e, extra.ID).Status != store.CopyPending {
		t.Fatalf("setup: statuses %s %s %s", copyOf(t, e, pending.ID).Status, copyOf(t, e, extra.ID).Status, copyOf(t, e, blocked.ID).Status)
	}

	// While Caravana is down the first copy ends.
	e.nomad.FinishRun(runningCopy.JobID, true, "", "caravana-copy: done key=offsite-bucket/caravana/whoami/web/1700000000.dump bytes=5\n", "")

	// The new process has only the store and Nomad to go on.
	restarted := New(e.store, newDeployer(t, e.registry, e.nomad), e.secrets, discardLogger())
	restarted.clock = e.svc.clock
	restarted.copyPass(ctx)
	if got := copyOf(t, e, running.ID); got.Status != store.CopySucceeded || got.Bytes != 5 {
		t.Errorf("the copy that ended while it was down = %+v, want succeeded", got)
	}
	if got := copyOf(t, e, extra.ID); got.Status != store.CopyRunning {
		t.Errorf("a pending copy = %+v, want it started in the freed slot", got)
	}
	if got := copyOf(t, e, blocked.ID); got.Status != store.CopyPending {
		t.Errorf("the last copy = %+v, want it waiting: two are running", got)
	}
}

// Volume and sqlite backups queue their copies when they succeed, as dump
// backups do.
func TestVolumeBackupQueuesItsCopies(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	saveDestination(t, e, "offsite", "SK-offsite")
	a := kuma(false)
	a.Services[0].Backup.CopyTo = []string{"offsite"}
	deployHealthy(t, e, a)
	e.nomad.SetCopies("whoami", api.AllocationListStub{ID: labCopy, TaskGroup: "web", ClientStatus: "running", JobVersion: 1})
	e.nomad.SetRestartStop(20 * time.Millisecond)
	clock := e.svc.clock.(*fakeClock)
	l := &volumeLab{env: e, t: t, t0: clock.Now(), clock: clock}
	id := l.id(0)
	l.render(id)
	l.answer(id, labSuccess)

	rec, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	l.settled(rec.ID)
	waitFor(t, "the copy to be queued and started", func() bool {
		b, err := e.store.GetBackup(ctx, "whoami", rec.ID)
		return err == nil && len(b.Copies) == 1 && b.Copies[0].Status == store.CopyRunning
	})
	jobID := copyJobs(e)[0]
	v := e.nomad.Variable("nomad/jobs/" + jobID)
	if v["SRC_OBJECT"] != "backups/caravana/whoami/web/1791990000/" || v["DEST_OBJECT"] != "offsite-bucket/caravana/whoami/web/1791990000/" {
		t.Errorf("variable = %v, want the volume backup's directory", v)
	}
	if got := e.nomad.Job(jobID).TaskGroups[0].Tasks[0].Env; got["KIND"] != "dir" || got["EXT"] != "" {
		t.Errorf("env = %v", got)
	}
}

func TestSQLiteBackupQueuesItsCopies(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	saveDestination(t, e, "offsite", "SK-offsite")
	a := sqliteApp()
	a.Services[0].Backup.CopyTo = []string{"offsite"}
	deployHealthy(t, e, a)
	e.nomad.SetCopies("whoami", api.AllocationListStub{ID: labCopy, TaskGroup: "web", ClientStatus: "running", JobVersion: 1})
	e.nomad.SetRestartStop(20 * time.Millisecond)
	clock := e.svc.clock.(*fakeClock)
	l := &volumeLab{env: e, t: t, t0: clock.Now(), clock: clock}
	id := l.id(0)
	l.onSQLite(l.sqliteOK(id))

	rec, err := e.svc.BackUpNow(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	l.settled(rec.ID)
	waitFor(t, "the copy to be queued and started", func() bool {
		b, err := e.store.GetBackup(ctx, "whoami", rec.ID)
		return err == nil && len(b.Copies) == 1 && b.Copies[0].Status == store.CopyRunning
	})
	jobID := copyJobs(e)[0]
	if got := e.nomad.Job(jobID).TaskGroups[0].Tasks[0].Env; got["KIND"] != "file" || got["EXT"] != ".sqlite.gz" {
		t.Errorf("env = %v", got)
	}
	ts := strings.TrimPrefix(id, "web-")
	if v := e.nomad.Variable("nomad/jobs/" + jobID); v["DEST_OBJECT"] != "offsite-bucket/caravana/whoami/web/"+ts+".sqlite.gz" {
		t.Errorf("variable = %v", v)
	}
}

// A copy job's events wake the backup watcher, as a backup's do, and an unrelated job's don't.
func TestCopyJobEventsWakeTheBackupWatcher(t *testing.T) {
	e := newEnv(t)
	for _, tt := range []struct {
		job  string
		wake bool
	}{
		{"whoami_web_copy-0123456789ab", true},
		{"whoami_web_backup", true},
		{"whoami", false},
	} {
		woken := e.svc.wakes.channel(backupsKey)
		e.svc.onEvents(deploy.EventBatch{Jobs: []string{tt.job}})
		select {
		case <-woken:
			if !tt.wake {
				t.Errorf("an event of %s woke the backup watcher", tt.job)
			}
		case <-time.After(50 * time.Millisecond):
			if tt.wake {
				t.Errorf("an event of %s didn't wake the backup watcher", tt.job)
			}
		}
	}
}
