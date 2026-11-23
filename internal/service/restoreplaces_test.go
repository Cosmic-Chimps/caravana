package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// saveCopy records a succeeded copy of the backup to offsite with the object in its bucket.
func saveCopy(t *testing.T, e *env, b store.BackupRecord, object string) {
	t.Helper()
	ctx := context.Background()
	if err := e.store.QueueBackupCopies(ctx, b.AppID, b.ID, []string{"offsite"}); err != nil {
		t.Fatal(err)
	}
	done := time.Date(2026, 11, 23, 3, 5, 0, 0, time.UTC)
	if err := e.store.SaveBackupCopy(ctx, store.BackupCopy{AppID: b.AppID, BackupID: b.ID, Destination: "offsite",
		Status: store.CopySucceeded, Object: object, Bytes: 100, StartedAt: &done, FinishedAt: &done}); err != nil {
		t.Fatal(err)
	}
}

// locateHook answers every check job with stdout and counts how many ran. seen holds the variable
// of the last one while it ran.
func locateHook(e *env, stdout string) (calls *atomic.Int32, seen *atomic.Pointer[map[string]string]) {
	calls, seen = new(atomic.Int32), new(atomic.Pointer[map[string]string])
	e.nomad.FinishOnRegisterPrefix("_backup-check-", true, "", func(job *api.Job) (string, string) {
		v := e.nomad.Variable("nomad/jobs/" + *job.ID)
		seen.Store(&v)
		calls.Add(1)
		return stdout, ""
	})
	return calls, seen
}

// newPlacesEnv is newCopyEnv with one backup in main and a good copy of it in offsite.
func newPlacesEnv(t *testing.T) (*env, store.BackupRecord, string) {
	t.Helper()
	e := newCopyEnv(t)
	freezeCopies(t, e)
	b := seedBackup(t, e, 0, false)
	object := "offsite-bucket/caravana/whoami/web/1700000000.dump"
	saveCopy(t, e, b, object)
	return e, b, object
}

func TestRestoreFromAPlace(t *testing.T) {
	ctx := context.Background()

	t.Run("from names the place: no check job, and the restore reads that place's object with its keys", func(t *testing.T) {
		e, b, object := newPlacesEnv(t)
		calls, _ := locateHook(e, "caravana-check: found 1\n")
		r, err := e.svc.Restore(ctx, "whoami", b.ID, "offsite")
		if err != nil || r.Destination != "offsite" || r.Status != store.BackupRunning {
			t.Fatalf("Restore = %+v, %v", r, err)
		}
		if calls.Load() != 0 {
			t.Errorf("%d check jobs ran for a restore that named its place", calls.Load())
		}
		if job := e.nomad.Job("whoami_web_restore"); job == nil || job.Meta["backup_key"] != object {
			t.Errorf("restore job = %+v, want it to read %s", job, object)
		}
		if v := e.nomad.Variable("nomad/jobs/whoami_web_restore"); v["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK-offsite" ||
			v["RCLONE_CONFIG_DEST_ENDPOINT"] != "http://10.0.0.5:9000" || v["PGPASSWORD"] != marker {
			t.Errorf("restore variable = %v, want offsite's keys and the login", v)
		}
		if got, _ := e.store.ListRestores(ctx, "whoami", 0); len(got) != 1 || got[0].Destination != "offsite" {
			t.Errorf("restores = %+v", got)
		}
	})

	t.Run("a place the backup isn't in is a field error on from", func(t *testing.T) {
		e, b, _ := newPlacesEnv(t)
		for _, from := range []string{"cold", "Not a name"} {
			_, err := e.svc.Restore(ctx, "whoami", b.ID, from)
			if fieldOf(err) != "from" {
				t.Errorf("Restore from %q = %v, want a field error on from", from, err)
			}
		}
		// The backup's own destination, once retention deleted it there, isn't a place either.
		if err := e.store.ExpireBackups(ctx, "whoami", []string{b.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Restore(ctx, "whoami", b.ID, "main"); fieldOf(err) != "from" {
			t.Errorf("Restore from a destination that lost it = %v, want a field error on from", err)
		}
		if got, _ := e.store.ListRestores(ctx, "whoami", 0); len(got) != 0 {
			t.Errorf("a refused restore left records: %+v", got)
		}
	})

	t.Run("without from, the first place that has it", func(t *testing.T) {
		e, b, _ := newPlacesEnv(t)
		calls, seen := locateHook(e, "caravana-check: found 1\n")
		r, err := e.svc.Restore(ctx, "whoami", b.ID, "")
		if err != nil || r.Destination != "main" || calls.Load() != 1 {
			t.Fatalf("Restore = %+v, %v (%d checks)", r, err, calls.Load())
		}
		v := *seen.Load()
		if v["R1_PATH"] != b.Object || v["R2_PATH"] != "offsite-bucket/caravana/whoami/web/1700000000.dump" ||
			v["RCLONE_CONFIG_R1_SECRET_ACCESS_KEY"] != "SK-"+marker || v["RCLONE_CONFIG_R2_SECRET_ACCESS_KEY"] != "SK-offsite" {
			t.Errorf("check variable = %v, want the backup's places in order, with their keys", v)
		}
		if got, _ := e.store.GetBackup(ctx, "whoami", b.ID); got.Expired || got.Copies[0].Expired {
			t.Errorf("a place that had it was marked missing: %+v", got)
		}
	})

	t.Run("without from, when the first place lost it the copy is used and the first is marked expired", func(t *testing.T) {
		e, b, object := newPlacesEnv(t)
		locateHook(e, "caravana-check: missing 1\ncaravana-check: found 2\n")
		r, err := e.svc.Restore(ctx, "whoami", b.ID, "")
		if err != nil || r.Destination != "offsite" {
			t.Fatalf("Restore = %+v, %v; want it read from offsite", r, err)
		}
		if job := e.nomad.Job("whoami_web_restore"); job == nil || job.Meta["backup_key"] != object {
			t.Errorf("restore job = %+v", job)
		}
		got, err := e.store.GetBackup(ctx, "whoami", b.ID)
		if err != nil || !got.Expired || got.Copies[0].Expired {
			t.Fatalf("backup = %+v, %v; want expired in main only", got, err)
		}
		// It is still listed, and still restorable from the copy.
		live, _, err := e.svc.ListBackups(ctx, "whoami")
		if err != nil || len(live) != 1 || live[0].ID != b.ID {
			t.Errorf("ListBackups = %+v, %v; want the backup kept while its copy holds it", live, err)
		}
	})

	t.Run("in no place any more", func(t *testing.T) {
		e, b, _ := newPlacesEnv(t)
		locateHook(e, "caravana-check: missing 1\ncaravana-check: missing 2\n")
		_, err := e.svc.Restore(ctx, "whoami", b.ID, "")
		if !errors.Is(err, ErrBackupMissing) || !strings.Contains(err.Error(), "isn't in main or offsite any more") {
			t.Fatalf("Restore = %v, want ErrBackupMissing naming both places", err)
		}
		got, _ := e.store.GetBackup(ctx, "whoami", b.ID)
		if !got.Expired || !got.Copies[0].Expired || !got.Gone() {
			t.Errorf("backup = %+v, want it gone from both", got)
		}
		if live, _, err := e.svc.ListBackups(ctx, "whoami"); err != nil || len(live) != 0 {
			t.Errorf("ListBackups = %+v, %v; want the backup gone from the list", live, err)
		}
		if _, err := e.svc.Restore(ctx, "whoami", b.ID, ""); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("a second restore = %v, want ErrNotFound: nothing is left", err)
		}
		if len(e.nomad.JobIDs("whoami_web_restore")) != 0 {
			t.Error("a restore job was started with no object to read")
		}
	})

	t.Run("a place that can't be reached isn't marked missing, and isn't the end of the backup", func(t *testing.T) {
		e, b, _ := newPlacesEnv(t)
		locateHook(e, "caravana-check: unreachable 1\ncaravana-check: missing 2\n")
		_, err := e.svc.Restore(ctx, "whoami", b.ID, "")
		if errors.Is(err, ErrBackupMissing) || !errors.Is(err, deploy.ErrNomadUnavailable) || !strings.Contains(err.Error(), "main couldn't be reached") {
			t.Fatalf("Restore = %v, want the unreachable place named, not ErrBackupMissing", err)
		}
		got, _ := e.store.GetBackup(ctx, "whoami", b.ID)
		if got.Expired || !got.Copies[0].Expired {
			t.Errorf("backup = %+v, want main untouched and the copy marked", got)
		}
	})

	t.Run("a place whose destination was removed isn't asked", func(t *testing.T) {
		e, b, _ := newPlacesEnv(t)
		calls, _ := locateHook(e, "caravana-check: found 1\n")
		// Policy and project use offsite, so the service refuses to remove it: the store does.
		if err := e.store.DeleteBackupDestination(ctx, "offsite"); err != nil {
			t.Fatal(err)
		}
		r, err := e.svc.Restore(ctx, "whoami", b.ID, "")
		if err != nil || r.Destination != "main" || calls.Load() != 0 {
			t.Errorf("Restore = %+v, %v (%d checks); want main without a check job", r, err, calls.Load())
		}
	})

	t.Run("a backup in one place is restored with no check", func(t *testing.T) {
		e := newCopyEnv(t)
		freezeCopies(t, e)
		b := seedBackup(t, e, 0, false)
		calls, _ := locateHook(e, "caravana-check: found 1\n")
		r, err := e.svc.Restore(ctx, "whoami", b.ID, "")
		if err != nil || r.Destination != "main" || calls.Load() != 0 {
			t.Errorf("Restore = %+v, %v (%d checks)", r, err, calls.Load())
		}
	})
}

// The restore clock passes no place: a pinned backup that main lost is restored from its copy.
func TestScheduledRestoreReadsAnyPlace(t *testing.T) {
	ctx := context.Background()
	e, b, object := newPlacesEnv(t)
	if err := e.store.ExpireBackups(ctx, "whoami", []string{b.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.PinBackup(ctx, "whoami", b.ID, "golden", time.Now()); err != nil {
		t.Fatal(err)
	}
	pinned, err := e.store.PinnedBackup(ctx, "whoami", "web", "golden")
	if err != nil || pinned.Gone() || len(pinned.Copies) != 1 {
		t.Fatalf("pinned = %+v, %v; want it not gone, with its copy", pinned, err)
	}
	r, err := e.svc.restoreBackup(ctx, "whoami", pinned, store.RestoreTriggerSchedule, "golden", "")
	if err != nil || r.Destination != "offsite" || r.Pin != "golden" {
		t.Fatalf("restoreBackup = %+v, %v", r, err)
	}
	if job := e.nomad.Job("whoami_web_restore"); job == nil || job.Meta["backup_key"] != object {
		t.Errorf("restore job = %+v", job)
	}
}

// A pin keeps a backup that main lost but a copy holds, and pinning needs a place, not main.
func TestPinsNeedAPlace(t *testing.T) {
	ctx := context.Background()
	e, b, _ := newPlacesEnv(t)
	if err := e.store.ExpireBackups(ctx, "whoami", []string{b.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PinBackup(ctx, "whoami", b.ID, "golden"); err != nil {
		t.Fatalf("PinBackup of a backup only a copy holds = %v", err)
	}
	if stamps, err := e.svc.pinStamps(ctx, "whoami", "web"); err != nil || !slices.Equal(stamps, []string{"1700000000"}) {
		t.Errorf("pinStamps = %v, %v; want the copy's retention told to keep it", stamps, err)
	}
	// With the copy gone too, the pin no longer protects anything.
	if err := e.store.ExpireBackupCopies(ctx, "whoami", "offsite", []string{b.ID}); err != nil {
		t.Fatal(err)
	}
	if stamps, _ := e.svc.pinStamps(ctx, "whoami", "web"); len(stamps) != 0 {
		t.Errorf("pinStamps = %v, want none: no place holds it", stamps)
	}
	if _, err := e.svc.UnpinBackup(ctx, "whoami", b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PinBackup(ctx, "whoami", b.ID, "again"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("PinBackup of a backup no place holds = %v, want ErrNotFound", err)
	}
}

func TestRestorePlacesOrder(t *testing.T) {
	b := store.BackupRecord{ID: "web-1", Object: "main/x", Copies: []store.BackupCopy{
		{Destination: "cold", Status: store.CopySucceeded, Object: "cold/x"},
		{Destination: "offsite", Status: store.CopySucceeded, Object: "offsite/x"},
		{Destination: "failed", Status: store.CopyFailed},
		{Destination: "gone", Status: store.CopySucceeded, Expired: true},
		{Destination: "pending", Status: store.CopyPending},
	}}
	got := restorePlaces(b, "main", []string{"offsite", "cold"})
	want := []restorePlace{{"main", "main/x"}, {"offsite", "offsite/x"}, {"cold", "cold/x"}}
	if !slices.Equal(got, want) {
		t.Errorf("places = %v, want %v: own destination, then the copies in the policy's order", got, want)
	}
	b.Expired = true
	if got := restorePlaces(b, "main", nil); len(got) != 2 || got[0].Destination != "cold" {
		t.Errorf("places without main and without a policy = %v, want the copies in the order they were made", got)
	}
	if namesOf(want) != "main, offsite or cold" || namesOf(want[:2]) != "main or offsite" || namesOf(want[:1]) != "main" {
		t.Errorf("namesOf = %q, %q, %q", namesOf(want), namesOf(want[:2]), namesOf(want[:1]))
	}
	if b.Gone() || (store.BackupRecord{Expired: true}).Gone() != true || (store.BackupRecord{}).Gone() {
		t.Error("Gone isn't 'expired here and in every copy'")
	}
}

func TestTestBackupDestination(t *testing.T) {
	ctx := context.Background()
	check := func(e *env, ok bool, stdout, stderr string) *atomic.Pointer[map[string]string] {
		seen := new(atomic.Pointer[map[string]string])
		failed := ""
		if !ok {
			failed = "check"
		}
		e.nomad.FinishOnRegisterPrefix("_backup-check-", ok, failed, func(job *api.Job) (string, string) {
			v := e.nomad.Variable("nomad/jobs/" + *job.ID)
			seen.Store(&v)
			return stdout, stderr
		})
		return seen
	}

	t.Run("passes", func(t *testing.T) {
		e := newEnv(t)
		saveDestination(t, e, "main", "SK-main")
		seen := check(e, true, "caravana-check: ok\n", "")
		res, err := e.svc.TestBackupDestination(ctx, "main")
		if err != nil || !res.OK || res.Message != "listed, wrote and removed a test object" || res.TestedAt.IsZero() {
			t.Fatalf("TestBackupDestination = %+v, %v", res, err)
		}
		v := *seen.Load()
		if v["R1_PATH"] != "main-bucket/caravana" || v["RCLONE_CONFIG_R1_SECRET_ACCESS_KEY"] != "SK-main" {
			t.Errorf("check variable = %v", v)
		}
		got, _ := e.store.GetBackupDestinationNamed(ctx, "main")
		if got.Test == nil || !got.Test.OK || got.Test.Message != res.Message {
			t.Errorf("stored test = %+v", got.Test)
		}
	})
	t.Run("a bucket that said no is an outcome, with rclone's words and no key", func(t *testing.T) {
		e := newEnv(t)
		saveDestination(t, e, "offsite", "SK-offsite")
		check(e, false, "caravana-check: failed write\n", "ERROR: AccessDenied: SK-offsite can't write\n")
		res, err := e.svc.TestBackupDestination(ctx, "offsite")
		if err != nil || res.OK || !strings.HasPrefix(res.Message, "could not write: ") || !strings.Contains(res.Message, "AccessDenied") ||
			strings.Contains(res.Message, "SK-offsite") {
			t.Fatalf("TestBackupDestination = %+v, %v", res, err)
		}
		if got, _ := e.store.GetBackupDestinationNamed(ctx, "offsite"); got.Test == nil || got.Test.OK {
			t.Errorf("stored test = %+v", got.Test)
		}
	})
	t.Run("a job that never ran says what Nomad said", func(t *testing.T) {
		e := newEnv(t)
		saveDestination(t, e, "main", "SK")
		check(e, false, "", "")
		res, err := e.svc.TestBackupDestination(ctx, "main")
		if err != nil || res.OK || !strings.HasPrefix(res.Message, "could not run the check: ") {
			t.Errorf("TestBackupDestination = %+v, %v", res, err)
		}
	})
	t.Run("a key that is gone leaves the message as rclone said it", func(t *testing.T) {
		e := newEnv(t)
		saveDestination(t, e, "main", "SK")
		e.nomad.SetVariable("caravana/backup-destinations/main", map[string]string{"secretAccessKey": ""})
		check(e, false, "caravana-check: failed list\n", "ERROR: InvalidAccessKeyId\n")
		res, err := e.svc.TestBackupDestination(ctx, "main")
		if err != nil || res.OK || !strings.Contains(res.Message, "InvalidAccessKeyId") || strings.Contains(res.Message, "*") {
			t.Errorf("TestBackupDestination = %+v, %v; want rclone's words untouched", res, err)
		}
	})
	t.Run("an unknown name, and Nomad failing", func(t *testing.T) {
		e := newEnv(t)
		saveDestination(t, e, "main", "SK")
		if _, err := e.svc.TestBackupDestination(ctx, "ghost"); !errors.Is(err, ErrDestinationNotFound) {
			t.Errorf("unknown = %v, want ErrDestinationNotFound", err)
		}
		e.nomad.RejectNext(400, "no")
		if _, err := e.svc.TestBackupDestination(ctx, "main"); err == nil {
			t.Error("a Nomad that refused the check gave no error")
		}
		if got, _ := e.store.GetBackupDestinationNamed(ctx, "main"); got.Test != nil {
			t.Errorf("a check that didn't run stored an outcome: %+v", got.Test)
		}
	})
	t.Run("editing the destination forgets the test", func(t *testing.T) {
		e := newEnv(t)
		saveDestination(t, e, "main", "SK")
		check(e, true, "caravana-check: ok\n", "")
		if _, err := e.svc.TestBackupDestination(ctx, "main"); err != nil {
			t.Fatal(err)
		}
		saveDestination(t, e, "main", "SK2")
		if got, _ := e.store.GetBackupDestinationNamed(ctx, "main"); got.Test != nil {
			t.Errorf("test after a save = %+v, want none", got.Test)
		}
	})
}

// A volume restore reads from the copy: the variable the restore request goes in names that
// destination, and the record says where it read from. The policy's destination is put back
// when it ends.
func TestVolumeRestoreFromACopy(t *testing.T) {
	ctx := context.Background()
	l := newVolumeLab(t)
	saveDestination(t, l.env, "offsite", "SK-offsite")
	backupID := l.saveBackupToRestore()
	b, _ := l.store.GetBackup(ctx, "whoami", backupID)
	copyObject := "offsite-bucket/caravana/whoami/web/1791990000/"
	saveCopy(t, l.env, b, copyObject)

	id := l.id(0)
	l.render(id)
	inRestart, release := l.blockRestart(id, "status=succeeded\nmessage=\n")
	rec, err := l.svc.Restore(ctx, "whoami", backupID, "offsite")
	if err != nil || rec.Destination != "offsite" {
		t.Fatalf("Restore = %+v, %v", rec, err)
	}
	<-inRestart
	v := l.nomad.Variable(labVar)
	if v["OBJECT"] != copyObject || v["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK-offsite" || v["BACKUP_DESTINATION"] != "offsite" {
		t.Errorf("variable during the run = %v, want the copy's object and offsite's keys", v)
	}
	release()
	got := l.settledRestore(rec.ID)
	if got.Status != store.BackupSucceeded || got.Destination != "offsite" {
		t.Errorf("settled = %+v", got)
	}
	waitFor(t, "the policy's destination to be put back", func() bool {
		return l.nomad.Variable(labVar)["BACKUP_DESTINATION"] == "main"
	})
	if v := l.nomad.Variable(labVar); v["RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY"] != "SK-"+marker {
		t.Errorf("variable after the run still holds offsite's keys: %v", v)
	}
	l.released()
}

func TestSQLiteRestoreFromACopy(t *testing.T) {
	ctx := context.Background()
	l := newSQLiteLab(t)
	saveDestination(t, l.env, "offsite", "SK-offsite")
	b := l.saveSQLiteBackup()
	copyObject := "offsite-bucket/caravana/whoami/web/1791990000.sqlite.gz"
	saveCopy(t, l.env, b, copyObject)
	id := l.id(0)
	l.onSQLite(l.sqliteOK(id))
	l.render(id)
	l.nomad.OnRestart(func(copyID string) {
		l.nomad.WriteAllocFile(copyID, "alloc/data/caravana/"+id+".result", "status=succeeded\nmessage=\n")
	})

	rec, err := l.svc.Restore(ctx, "whoami", b.ID, "offsite")
	if err != nil || rec.Destination != "offsite" {
		t.Fatalf("Restore = %+v, %v", rec, err)
	}
	if got := l.settledRestore(rec.ID); got.Status != store.BackupSucceeded || got.Destination != "offsite" {
		t.Errorf("settled = %+v", got)
	}
	found := false
	for _, e := range l.nomad.Execs() {
		if e.Command[2] != nomadjob.SQLiteDownloadScript {
			continue
		}
		found = e.Command[4] == copyObject && strings.Contains(e.Stdin, "SK-offsite") && !strings.Contains(e.Stdin, marker)
	}
	if !found {
		t.Error("the download didn't read the copy's object with offsite's keys")
	}
	l.released()
}
