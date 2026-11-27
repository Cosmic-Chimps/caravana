package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// osSentinel is the cluster's admin password: no record, error, log line, job, export or
// stored app may hold it, and no command may carry it in an argument.
const osSentinel = "os-admin-sentinel-061-$HOME"

// openSearchApp is whoami as a search cluster (feature 061): its snapshot folder on the
// volume "data", the admin password a secret, path.repo in the environment.
func openSearchApp() app.App {
	a := withData()
	svc := &a.Services[0]
	svc.Env = []app.EnvVar{{Name: "path.repo", Value: "/data/snaps"}}
	svc.Secrets = []app.Secret{set("OPENSEARCH_INITIAL_ADMIN_PASSWORD", osSentinel)}
	svc.Backup = &app.Backup{Engine: app.BackupOpenSearch, Repository: "/data/snaps", Keep: 3}
	return a
}

// newOpenSearchLab is the volume lab with openSearchApp deployed: one running copy, "c1",
// whose service task and sidecar the test plays through onOpenSearch.
func newOpenSearchLab(t *testing.T) *volumeLab {
	t.Helper()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, openSearchApp())
	e.nomad.SetCopies("whoami", api.AllocationListStub{ID: labCopy, TaskGroup: "web", ClientStatus: "running", JobVersion: 1})
	e.nomad.SetRestartStop(20 * time.Millisecond)
	clock := e.svc.clock.(*fakeClock)
	return &volumeLab{env: e, t: t, t0: clock.Now(), clock: clock}
}

// openSearchReplies are what the steps' commands do.
type openSearchReplies struct {
	prepare, clean, snapshot, upload, download, restore deploytest.ExecReply
}

func (l *volumeLab) onOpenSearch(r openSearchReplies) {
	l.nomad.OnExec(func(req deploytest.ExecRequest) deploytest.ExecReply {
		script := ""
		if len(req.Command) > 2 {
			script = req.Command[2]
		}
		switch script {
		case nomadjob.OpenSearchPrepareScript:
			return r.prepare
		case nomadjob.OpenSearchCleanScript:
			return r.clean
		case nomadjob.OpenSearchSnapshotScript:
			return r.snapshot
		case nomadjob.OpenSearchUploadScript:
			return r.upload
		case nomadjob.OpenSearchDownloadScript:
			return r.download
		case nomadjob.OpenSearchRestoreScript:
			return r.restore
		}
		return deploytest.ExecReply{ExitCode: 99, Stderr: "unexpected command"}
	})
}

func (l *volumeLab) openSearchOK(id string) openSearchReplies {
	ts := strings.TrimPrefix(id, "web-")
	return openSearchReplies{
		prepare:  deploytest.ExecReply{Stdout: "caravana-opensearch-owner uid=1000 gid=1001\n"},
		snapshot: deploytest.ExecReply{Stdout: "caravana-opensearch-snapshot indices=2\n"},
		upload: deploytest.ExecReply{Stdout: "caravana-backup key=backups/caravana/whoami/web/" + ts + ".opensearch.tar.gz bytes=5000\n" +
			"caravana-expired 1700000000.opensearch.tar.gz\n"},
		download: deploytest.ExecReply{Stdout: "caravana-opensearch-download files=17\n"},
		restore:  deploytest.ExecReply{Stdout: "caravana-opensearch-restore indices=2\n"},
	}
}

// osScripts are the steps the fake was asked to run, in order, with where they ran.
func (l *volumeLab) osScripts() []string {
	var out []string
	for _, e := range l.nomad.Execs() {
		name := map[string]string{
			nomadjob.OpenSearchPrepareScript:  "prepare",
			nomadjob.OpenSearchCleanScript:    "clean",
			nomadjob.OpenSearchSnapshotScript: "snapshot",
			nomadjob.OpenSearchUploadScript:   "upload",
			nomadjob.OpenSearchDownloadScript: "download",
			nomadjob.OpenSearchRestoreScript:  "restore",
		}[e.Command[2]]
		if name != "" {
			out = append(out, name+"@"+e.Task)
		}
	}
	return out
}

func TestOpenSearchBackupRun(t *testing.T) {
	ctx := context.Background()

	t.Run("cleans, snapshots in the service, uploads, and never restarts it", func(t *testing.T) {
		l := newOpenSearchLab(t)
		id := l.id(0)
		l.onOpenSearch(l.openSearchOK(id))
		old := store.BackupRecord{AppID: "whoami", ID: "web-1700000000", Service: "web", Engine: app.BackupOpenSearch,
			Status: store.BackupSucceeded, Object: "backups/caravana/whoami/web/1700000000.opensearch.tar.gz", StartedAt: l.t0.Add(-100 * time.Hour)}
		if err := l.store.SaveBackup(ctx, old); err != nil {
			t.Fatal(err)
		}
		rec, err := l.svc.BackUpNow(ctx, "whoami", "web")
		if err != nil {
			t.Fatal(err)
		}
		ts := strings.TrimPrefix(id, "web-")
		if rec.ID != id || rec.Engine != app.BackupOpenSearch || rec.Status != store.BackupRunning ||
			rec.JobID != "whoami_web_opensearch/"+ts || rec.Trigger != store.BackupTriggerOwner || rec.Destination != "main" {
			t.Fatalf("running record = %+v", rec)
		}
		got := l.settled(id)
		if got.Status != store.BackupSucceeded || got.Bytes != 5000 || got.Message != "" || got.Stopped != nil ||
			got.Object != "backups/caravana/whoami/web/"+ts+".opensearch.tar.gz" {
			t.Errorf("record = %+v", got)
		}
		if b, _ := l.store.GetBackup(ctx, "whoami", old.ID); !b.Expired {
			t.Error("the backup retention removed isn't marked expired")
		}
		l.released()
		if rs := l.nomad.Restarts(); len(rs) != 0 {
			t.Errorf("restarts = %v, want none: the service keeps running", rs)
		}
		if got, want := strings.Join(l.osScripts(), " "), "prepare@web clean@caravana-upload snapshot@web upload@caravana-upload"; got != want {
			t.Errorf("commands = %s, want %s", got, want)
		}
		for _, e := range l.nomad.Execs() {
			switch e.Command[2] {
			case nomadjob.OpenSearchSnapshotScript:
				// The login is on the standard input, the folder and port are arguments.
				if e.Stdin != "OPENSEARCH_USER=admin\nOPENSEARCH_PASSWORD="+osSentinel+"\n" ||
					e.Command[4] != "/data/snaps" || e.Command[5] != id || e.Command[7] != "80" {
					t.Errorf("snapshot exec = args %q, stdin %q", e.Command[4:], e.Stdin)
				}
			case nomadjob.OpenSearchUploadScript:
				if !strings.Contains(e.Stdin, "RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY=SK-"+marker) {
					t.Errorf("the upload's standard input lacks the destination's key: %q", e.Stdin)
				}
				if e.Command[4] != "/caravana-volumes/data/snaps/caravana-"+id {
					t.Errorf("the upload's folder = %q", e.Command[4])
				}
			case nomadjob.OpenSearchCleanScript:
				if e.Stdin != "" {
					t.Errorf("clean got a standard input: %q", e.Stdin)
				}
				if owner := e.Command[len(e.Command)-1]; owner != "1000:1001" {
					t.Errorf("clean's owner = %q, want the one prepare reported", owner)
				}
			case nomadjob.OpenSearchPrepareScript:
				if e.Stdin != "" || e.Command[4] != "/data/snaps" {
					t.Errorf("prepare exec = args %q, stdin %q", e.Command[4:], e.Stdin)
				}
			}
			if strings.Contains(strings.Join(e.Command, " "), osSentinel) || strings.Contains(strings.Join(e.Command, " "), marker) {
				t.Errorf("a command's arguments hold a secret (%s)", e.Task)
			}
		}
	})

	t.Run("a pinned backup's timestamp goes to the upload", func(t *testing.T) {
		l := newOpenSearchLab(t)
		id := l.id(0)
		l.onOpenSearch(l.openSearchOK(id))
		old := store.BackupRecord{AppID: "whoami", ID: "web-1700000000", Service: "web", Engine: app.BackupOpenSearch,
			Status: store.BackupSucceeded, Object: "backups/caravana/whoami/web/1700000000.opensearch.tar.gz", StartedAt: l.t0.Add(-100 * time.Hour)}
		if err := l.store.SaveBackup(ctx, old); err != nil {
			t.Fatal(err)
		}
		if _, err := l.svc.store.PinBackup(ctx, "whoami", old.ID, "golden", l.t0); err != nil {
			t.Fatal(err)
		}
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		l.settled(id)
		for _, e := range l.nomad.Execs() {
			if e.Command[2] == nomadjob.OpenSearchUploadScript && e.Command[9] != "1700000000" {
				t.Errorf("pinned = %q, want 1700000000", e.Command[9])
			}
		}
		l.released()
	})

	t.Run("a retention warning is the message of a run that succeeded", func(t *testing.T) {
		l := newOpenSearchLab(t)
		id := l.id(0)
		r := l.openSearchOK(id)
		r.upload.Stdout = "caravana-backup key=b/whoami/web/1.opensearch.tar.gz bytes=10\ncaravana-warning the backup is kept, but old backups were not removed: listing: denied\n"
		l.onOpenSearch(r)
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		got := l.settled(id)
		if got.Status != store.BackupSucceeded || !strings.Contains(got.Message, "old backups were not removed") {
			t.Errorf("record = %+v", got)
		}
		l.released()
	})

	failures := []struct {
		name    string
		replies func(r *openSearchReplies)
		wantMsg string
		steps   string
	}{
		{"the folder is outside path.repo", func(r *openSearchReplies) {
			r.snapshot = deploytest.ExecReply{ExitCode: 1, Stderr: "snapshot: [caravana] location [/data/snaps/caravana-x] doesn't match any of the locations specified by path.repo\n"}
		}, "snapshot: [caravana] location [/data/snaps/caravana-x] doesn't match any of the locations specified by path.repo",
			"prepare@web clean@caravana-upload snapshot@web clean@caravana-upload"},
		{"the login is refused", func(r *openSearchReplies) {
			r.snapshot = deploytest.ExecReply{ExitCode: 1, Stderr: "snapshot: " + nomadjob.OpenSearchLoginRefused + "\n"}
		}, "snapshot: " + nomadjob.OpenSearchLoginRefused, "prepare@web clean@caravana-upload snapshot@web clean@caravana-upload"},
		{"the clean fails", func(r *openSearchReplies) {
			r.clean = deploytest.ExecReply{ExitCode: 1, Stderr: "clean: could not remove /caravana-volumes/data/snaps/caravana-old\n"}
		}, "clean: could not remove /caravana-volumes/data/snaps/caravana-old", "prepare@web clean@caravana-upload"},
		{"the prepare fails", func(r *openSearchReplies) {
			r.prepare = deploytest.ExecReply{ExitCode: 1, Stderr: "prepare: could not make the repository folder /data/snaps\n"}
		}, "prepare: could not make the repository folder /data/snaps", "prepare@web"},
		{"the upload fails", func(r *openSearchReplies) {
			r.upload = deploytest.ExecReply{ExitCode: 1, Stderr: "upload: 403 Forbidden\n"}
		}, "upload: 403 Forbidden", "prepare@web clean@caravana-upload snapshot@web upload@caravana-upload"},
		{"Nomad can't start the command", func(r *openSearchReplies) {
			r.snapshot = deploytest.ExecReply{Fail: "task not running"}
		}, "", "prepare@web clean@caravana-upload snapshot@web clean@caravana-upload"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			l := newOpenSearchLab(t)
			id := l.id(0)
			r := l.openSearchOK(id)
			tt.replies(&r)
			l.onOpenSearch(r)
			if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
				t.Fatal(err)
			}
			got := l.settled(id)
			if got.Status != store.BackupFailed || (tt.wantMsg != "" && got.Message != tt.wantMsg) || got.Message == "" {
				t.Errorf("record = %+v, want failed with %q", got, tt.wantMsg)
			}
			l.released()
			if got := strings.Join(l.osScripts(), " "); got != tt.steps {
				t.Errorf("commands = %s, want %s", got, tt.steps)
			}
		})
	}

	t.Run("a second run while one goes is refused, and the run's limit ends a stuck command", func(t *testing.T) {
		l := newOpenSearchLab(t)
		l.svc.volume.limit, l.svc.volume.grace = 300*time.Millisecond, 0
		id := l.id(0)
		r := l.openSearchOK(id)
		r.snapshot = deploytest.ExecReply{Hold: true}
		l.onOpenSearch(r)
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); !errors.Is(err, ErrBackupRunning) {
			t.Errorf("a second backup = %v, want ErrBackupRunning", err)
		}
		got := l.settled(id)
		if got.Status != store.BackupFailed || !strings.Contains(got.Message, "timed out") {
			t.Errorf("record = %+v, want failed: timed out", got)
		}
		l.released()
	})

	t.Run("no running copy is refused before anything is recorded", func(t *testing.T) {
		l := newOpenSearchLab(t)
		l.nomad.SetCopies("whoami")
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); !errors.Is(err, ErrNoRunningCopy) {
			t.Errorf("BackUpNow = %v, want ErrNoRunningCopy", err)
		}
		if bs, _ := l.store.ListBackups(ctx, "whoami", 0); len(bs) != 0 {
			t.Errorf("records = %+v, want none", bs)
		}
		l.released()
	})

	t.Run("a cluster with its security plugin off logs in with nothing", func(t *testing.T) {
		e := newEnv(t)
		setDestination(t, e)
		a := openSearchApp()
		a.Services[0].Secrets = nil
		deployHealthy(t, e, a)
		e.nomad.SetCopies("whoami", api.AllocationListStub{ID: labCopy, TaskGroup: "web", ClientStatus: "running", JobVersion: 1})
		clock := e.svc.clock.(*fakeClock)
		l := &volumeLab{env: e, t: t, t0: clock.Now(), clock: clock}
		id := l.id(0)
		l.onOpenSearch(l.openSearchOK(id))
		if _, err := l.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
		if got := l.settled(id); got.Status != store.BackupSucceeded {
			t.Fatalf("record = %+v", got)
		}
		for _, e := range l.nomad.Execs() {
			if e.Command[2] == nomadjob.OpenSearchSnapshotScript && e.Stdin != "" {
				t.Errorf("stdin = %q, want none", e.Stdin)
			}
		}
		l.released()
	})

	t.Run("the login comes from the env before the secrets", func(t *testing.T) {
		e := newEnv(t)
		setDestination(t, e)
		a := openSearchApp()
		a.Services[0].Env = append(a.Services[0].Env, app.EnvVar{Name: "INDEXER_PASSWORD", Value: "plain"}, app.EnvVar{Name: "INDEXER_USERNAME", Value: "ops"})
		a.Services[0].Secrets = nil
		deployHealthy(t, e, a)
		login, err := e.svc.openSearchLogin(ctx, a, a.Services[0])
		if err != nil || login.User != "ops" || login.Password != "plain" {
			t.Errorf("login = %+v, %v; want ops/plain from the env", login, err)
		}
	})
}

func (l *volumeLab) saveOpenSearchBackup() store.BackupRecord {
	l.t.Helper()
	done := l.t0.Add(-time.Hour)
	rec := store.BackupRecord{AppID: "whoami", ID: "web-1791990000", Service: "web", Engine: app.BackupOpenSearch,
		Status: store.BackupSucceeded, Object: "backups/caravana/whoami/web/1791990000.opensearch.tar.gz",
		StartedAt: done.Add(-time.Minute), FinishedAt: &done}
	if err := l.store.SaveBackup(context.Background(), rec); err != nil {
		l.t.Fatal(err)
	}
	return rec
}

func TestOpenSearchRestoreRun(t *testing.T) {
	ctx := context.Background()

	t.Run("cleans, downloads, restores in the service, cleans again, and stops nothing", func(t *testing.T) {
		l := newOpenSearchLab(t)
		b := l.saveOpenSearchBackup()
		id := l.id(0)
		l.onOpenSearch(l.openSearchOK(id))

		rec, err := l.svc.Restore(ctx, "whoami", b.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if rec.Engine != app.BackupOpenSearch || rec.Status != store.BackupRunning || rec.ID != restoreRecordID("whoami", id) {
			t.Fatalf("running record = %+v", rec)
		}
		got := l.settledRestore(rec.ID)
		if got.Status != store.BackupSucceeded || got.Stopped != nil || got.Destination != "main" || got.Message != "" {
			t.Errorf("record = %+v, want succeeded with no stop time, read from main", got)
		}
		l.released()
		if got, want := strings.Join(l.osScripts(), " "), "prepare@web clean@caravana-upload download@caravana-upload restore@web clean@caravana-upload"; got != want {
			t.Errorf("commands = %s, want %s", got, want)
		}
		if rs := l.nomad.Restarts(); len(rs) != 0 {
			t.Errorf("restarts = %v, want none", rs)
		}
		if v := l.nomad.Variable(labVar); v != nil && v["REQUEST"] != "" {
			t.Errorf("a volume request was written: %v", v)
		}
		for _, e := range l.nomad.Execs() {
			if e.Command[2] == nomadjob.OpenSearchDownloadScript && (e.Command[4] != b.Object || !strings.Contains(e.Stdin, marker)) {
				t.Errorf("the download = %v (stdin has the key: %v)", e.Command[4:], strings.Contains(e.Stdin, marker))
			}
			if e.Command[2] == nomadjob.OpenSearchDownloadScript || e.Command[2] == nomadjob.OpenSearchCleanScript {
				if owner := e.Command[len(e.Command)-1]; owner != "1000:1001" {
					t.Errorf("%s's owner = %q, want the one prepare reported", e.Task, owner)
				}
			}
			if e.Command[2] == nomadjob.OpenSearchRestoreScript && !strings.Contains(e.Stdin, "OPENSEARCH_PASSWORD="+osSentinel) {
				t.Errorf("the restore's standard input lacks the login: %q", e.Stdin)
			}
		}
	})

	for _, tt := range []struct {
		name    string
		replies func(r *openSearchReplies)
		wantMsg string
		steps   string
	}{
		{"a prepare that fails", func(r *openSearchReplies) {
			r.prepare = deploytest.ExecReply{ExitCode: 1, Stderr: "prepare: the repository folder /data/snaps is not writable by the service's user\n"}
		}, "prepare: the repository folder /data/snaps is not writable by the service's user", "prepare@web"},
		{"a download that fails", func(r *openSearchReplies) {
			r.download = deploytest.ExecReply{ExitCode: 1, Stderr: "download: the backup holds no OpenSearch snapshot\n"}
		}, "download: the backup holds no OpenSearch snapshot", "prepare@web clean@caravana-upload download@caravana-upload clean@caravana-upload"},
		{"a restore that fails", func(r *openSearchReplies) {
			r.restore = deploytest.ExecReply{ExitCode: 1, Stderr: "restore: boom; the indices it closed were opened again\n"}
		}, "restore: boom; the indices it closed were opened again", "prepare@web clean@caravana-upload download@caravana-upload restore@web clean@caravana-upload"},
	} {
		t.Run(tt.name+" ends the restore, with the folder cleaned", func(t *testing.T) {
			l := newOpenSearchLab(t)
			b := l.saveOpenSearchBackup()
			r := l.openSearchOK(l.id(0))
			tt.replies(&r)
			l.onOpenSearch(r)
			rec, err := l.svc.Restore(ctx, "whoami", b.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			got := l.settledRestore(rec.ID)
			if got.Status != store.BackupFailed || got.Message != tt.wantMsg {
				t.Errorf("record = %+v, want failed with %q", got, tt.wantMsg)
			}
			l.released()
			if got := strings.Join(l.osScripts(), " "); got != tt.steps {
				t.Errorf("commands = %s, want %s", got, tt.steps)
			}
		})
	}

	t.Run("a backup of another engine is refused", func(t *testing.T) {
		l := newOpenSearchLab(t)
		b := l.saveOpenSearchBackup()
		b.ID, b.Engine = "web-1791990001", app.BackupSQLite
		if err := l.store.SaveBackup(ctx, b); err != nil {
			t.Fatal(err)
		}
		if _, err := l.svc.Restore(ctx, "whoami", b.ID, ""); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Restore = %v, want ErrNotFound", err)
		}
	})
}

// The scheduler runs opensearch backups on their slot, as it does the other in-copy ones.
func TestRunDueOpenSearchBackups(t *testing.T) {
	ctx := context.Background()
	day := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, time.UTC) }
	l := newOpenSearchLab(t)
	last := store.BackupRecord{AppID: "whoami", ID: "web-1", Service: "web", Engine: app.BackupOpenSearch,
		Status: store.BackupSucceeded, StartedAt: day(4, 3, 0).Add(5 * time.Second)}
	if err := l.store.SaveBackup(ctx, last); err != nil {
		t.Fatal(err)
	}
	l.clock.Set(day(5, 3, 20))
	id := fmt.Sprintf("web-%d", day(5, 3, 20).Unix())
	l.onOpenSearch(l.openSearchOK(id))

	l.svc.runDueVolumeBackups(ctx, map[string]string{})
	got := l.settled(id)
	if got.Status != store.BackupSucceeded || got.Engine != app.BackupOpenSearch || got.Trigger != store.BackupTriggerTimer {
		t.Fatalf("the scheduled backup = %+v", got)
	}
	l.released()
}

// A scheduled restore (feature 036) of a pinned opensearch backup goes through the same flow.
func TestRestoreScheduleRestoresAnOpenSearchBackup(t *testing.T) {
	ctx := context.Background()
	l := newOpenSearchLab(t)
	b := l.saveOpenSearchBackup()
	if _, err := l.svc.store.PinBackup(ctx, "whoami", b.ID, "golden", l.t0); err != nil {
		t.Fatal(err)
	}
	l.onOpenSearch(l.openSearchOK(l.id(0)))
	rec, err := l.svc.restoreBackup(ctx, "whoami", b, store.RestoreTriggerSchedule, "golden", "")
	if err != nil {
		t.Fatal(err)
	}
	got := l.settledRestore(rec.ID)
	if got.Status != store.BackupSucceeded || got.Trigger != store.RestoreTriggerSchedule || got.Pin != "golden" {
		t.Errorf("record = %+v", got)
	}
	l.released()
}

// At start, an opensearch backup or restore left running is failed at once, with nothing
// asked of Nomad (no exec, no request), and the restore says what it may have left closed.
func TestResumeOpenSearchRuns(t *testing.T) {
	ctx := context.Background()
	l := newOpenSearchLab(t)
	backup := store.BackupRecord{AppID: "whoami", ID: l.id(0), Service: "web", JobID: "whoami_web_opensearch/1", Engine: app.BackupOpenSearch,
		Status: store.BackupRunning, StartedAt: time.Now().UTC()}
	if err := l.store.SaveBackup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	restore := store.RestoreRecord{ID: restoreRecordID("whoami", l.id(1)), AppID: "whoami", Service: "web", BackupID: "web-1",
		Engine: app.BackupOpenSearch, Status: store.BackupRunning, CreatedAt: time.Now().UTC()}
	if err := l.store.SaveRestore(ctx, restore); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	startService(t, l.store, l.registry, l.nomad, l.secrets)
	gotB := l.settled(backup.ID)
	gotR := l.settledRestore(restore.ID)
	if gotB.Status != store.BackupFailed || gotB.Message != restartedMessage {
		t.Errorf("backup = %+v", gotB)
	}
	if gotR.Status != store.BackupFailed || gotR.Message != restartedMessage+"; the indices it closed may still be closed" {
		t.Errorf("restore = %+v", gotR)
	}
	if len(l.nomad.Execs()) != 0 {
		t.Errorf("execs = %d, want none", len(l.nomad.Execs()))
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("settling took %s", time.Since(start))
	}
	l.released()
}

// The cluster's password is in no record, error, log line, stored app, job or export.
func TestOpenSearchPasswordNeverLeaks(t *testing.T) {
	ctx := context.Background()
	logs := &lockedBuffer{}
	e := newEnv(t)
	e.svc = startServiceLogging(t, e.store, e.svc.deployer, e.secrets, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	setDestination(t, e)
	deployHealthy(t, e, openSearchApp())
	e.nomad.SetCopies("whoami", api.AllocationListStub{ID: labCopy, TaskGroup: "web", ClientStatus: "running", JobVersion: 1})
	e.nomad.SetRestartStop(20 * time.Millisecond)
	clock := e.svc.clock.(*fakeClock)
	l := &volumeLab{env: e, t: t, t0: clock.Now(), clock: clock}

	// A tool that echoes the password in its failure, as a broken proxy might.
	id := l.id(0)
	r := l.openSearchOK(id)
	r.snapshot = deploytest.ExecReply{ExitCode: 1, Stderr: "snapshot: bad login " + osSentinel + "\n"}
	l.onOpenSearch(r)
	if _, err := e.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
		t.Fatal(err)
	}
	failed := l.settled(id)
	// And a good one, then a restore, so every record and notice has had its chance.
	good := l.id(5)
	l.clock.Set(l.t0.Add(5 * time.Second))
	l.onOpenSearch(l.openSearchOK(good))
	var ok store.BackupRecord
	waitFor(t, "the first run to give the app back", func() bool {
		var err error
		ok, err = e.svc.BackUpNow(ctx, "whoami", "web")
		if err != nil && !errors.Is(err, ErrBackupRunning) {
			t.Fatal(err)
		}
		return err == nil
	})
	l.settled(ok.ID)
	// A restore whose tool echoes the password too.
	var rr store.RestoreRecord
	l.clock.Set(l.t0.Add(10 * time.Second))
	rep := l.openSearchOK(l.id(10))
	rep.restore = deploytest.ExecReply{ExitCode: 1, Stderr: "restore: User [password=" + osSentinel + "] has no permission\n"}
	l.onOpenSearch(rep)
	waitFor(t, "the backup to give the app back", func() bool {
		var err error
		rr, err = e.svc.Restore(ctx, "whoami", ok.ID, "")
		if err != nil && !errors.Is(err, ErrBackupRunning) {
			t.Fatal(err)
		}
		return err == nil
	})
	if got := l.settledRestore(rr.ID); got.Status != store.BackupFailed || !strings.Contains(got.Message, "***") {
		t.Errorf("restore = %+v, want failed with the password replaced", got)
	}

	stored, err := e.svc.GetApp(ctx, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	backups, restores, err := e.svc.ListBackups(ctx, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	_, hcl, err := e.svc.Export(ctx, "whoami", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(failed.Message, osSentinel) {
		t.Errorf("a record's message holds the password: %q", failed.Message)
	}
	for what, text := range map[string]string{
		"stored app": mustJSON(stored), "backups": mustJSON(backups), "restores": mustJSON(restores), "Nomad job": mustJSON(e.nomad.Job("whoami")),
		"export": string(hcl), "log": logs.String(), "SQLite file and every row in it": rawStore(t, e.dir),
	} {
		if strings.Contains(text, osSentinel) {
			t.Errorf("the %s holds the password", what)
		}
	}
}
