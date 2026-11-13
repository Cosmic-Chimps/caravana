package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sqliteGateApp is a worker that keeps a SQLite database in WAL mode on its
// volume and inserts a row about five times a second, backed up with the
// sqlite engine (feature 029). It makes the database once, owned by 1000:1000
// with mode 640, so the restore's keeping of owner and mode shows. It has no
// port, so no traffic and no health check. The image is the one the
// caravana-sqlite sidecar uses; any image would do for the app.
const sqliteGateApp = `{
  "source": {"kind": "image"},
  "services": [{
    "name": "db",
    "image": "alpine/sqlite:3.53.4",
    "entrypoint": ["/bin/sh", "-c"],
    "args": ["if [ ! -f /data/app.db ]; then sqlite3 /data/app.db 'PRAGMA journal_mode=WAL' 'CREATE TABLE t(v INTEGER)' > /dev/null; chown 1000:1000 /data/app.db; chmod 640 /data/app.db; fi; trap 'exit 0' TERM; i=0; while :; do i=$((i+1)); sqlite3 -cmd '.timeout 5000' /data/app.db \"INSERT INTO t VALUES ($i)\"; sleep 0.2 & wait $!; done"],
    "resources": {"memoryMB": 64},
    "volumes": [{"name": "data", "path": "/data"}],
    "backup": {"engine": "sqlite", "file": "/data/app.db", "keep": 2}
  }]
}`

// sqliteBackupRestoreStep: a sqlite backup and its restore (feature 029), with
// the token Caravana really runs with. The backup runs beside the service,
// which keeps writing: no restart, same copy, same job version. The restore
// stops it only to put the file back: a row written after the backup is gone,
// and the file keeps its owner and mode. Retention keeps 2.
func (g *gate) sqliteBackupRestoreStep(t *testing.T) {
	const appID = "lab029"
	nomad := g.target.Nomad
	resp := g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "") // a leftover from an aborted run, if any
	resp.Body.Close()

	g.startBucket(t)

	// Refusals name the file field.
	for _, bad := range []struct{ name, from, to string }{
		{"a file outside every volume", `"file": "/data/app.db"`, `"file": "/tmp/app.db"`},
		{"the WAL file", `"file": "/data/app.db"`, `"file": "/data/app.db-wal"`},
		{"a file with the volume engine", `"engine": "sqlite"`, `"engine": "volume"`},
	} {
		resp := g.api(t, http.MethodPost, "/v1/apps/"+appID+"/deployments", "application/json", strings.Replace(sqliteGateApp, bad.from, bad.to, 1))
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), `"field":"services[0].backup.file"`) {
			t.Errorf("%s: deploy = %d %s, want 400 on services[0].backup.file", bad.name, resp.StatusCode, raw)
		}
	}

	resp = g.api(t, http.MethodPost, "/v1/apps/"+appID+"/deployments", "application/json", sqliteGateApp)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var d gateDeployment
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &d) != nil {
		t.Fatalf("deploy %s: %d %s", appID, resp.StatusCode, raw)
	}
	t.Cleanup(func() {
		resp := g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "")
		resp.Body.Close()
	})
	if final := g.waitFinalOf(t, appID, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
	}

	// query runs one SQL statement in the service's own task and returns its
	// output, waiting while the service starts.
	query := func(sql string) string {
		t.Helper()
		var got string
		var lastErr error
		if !waitUntil(time.Minute, func() bool {
			got, lastErr = g.exec(t, appID, "db", d.ID, "sqlite3", "-cmd", ".timeout 5000", "/data/app.db", sql)
			return lastErr == nil && got != ""
		}) {
			t.Fatalf("%s: %q (%v)", sql, got, lastErr)
		}
		return strings.TrimSpace(got)
	}
	count := func() int {
		t.Helper()
		n, err := strconv.Atoi(query("SELECT count(*) FROM t"))
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	waitFor(t, "the app to write some rows", time.Minute, func() bool { return count() >= 10 })

	versionOf := func() uint64 {
		t.Helper()
		job, _, err := nomad.Jobs().Info(appID, nil)
		if err != nil || job.Version == nil {
			t.Fatalf("read job %s: %v", appID, err)
		}
		return *job.Version
	}
	// copyOf is the one running copy and how often its service task restarted.
	copyOf := func() (string, uint64) {
		t.Helper()
		stubs, _, err := nomad.Jobs().Allocations(appID, false, nil)
		if err != nil {
			t.Fatalf("list the copies of %s: %v", appID, err)
		}
		for _, s := range stubs {
			if s.DesiredStatus == "run" && s.ClientStatus == "running" {
				if st := s.TaskStates["db"]; st != nil {
					return s.ID, st.Restarts
				}
				return s.ID, 0
			}
		}
		t.Fatalf("%s has no running copy", appID)
		return "", 0
	}
	versionBefore := versionOf()
	copyBefore, restartsBefore := copyOf()

	// backUp runs Back up now and waits for the run to succeed.
	backUp := func() gateVolumeRun {
		t.Helper()
		resp := g.api(t, http.MethodPost, "/v1/apps/"+appID+"/backups", "application/json", `{"service": "db"}`)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var started struct{ ID string }
		if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &started) != nil || started.ID == "" {
			t.Fatalf("back up now: %d %s", resp.StatusCode, raw)
		}
		var run gateVolumeRun
		waitFor(t, "the sqlite backup to succeed", 3*time.Minute, func() bool {
			var l struct {
				Backups []gateVolumeRun `json:"backups"`
			}
			g.getJSON(t, "/v1/apps/"+appID+"/backups", &l)
			for _, b := range l.Backups {
				if b.ID != started.ID {
					continue
				}
				switch b.Status {
				case "failed":
					t.Fatalf("the sqlite backup failed: %s", b.Message)
				case "succeeded":
					run = b
					return true
				}
			}
			return false
		})
		return run
	}

	rowsBefore := count()
	run := backUp()
	if run.Engine != "sqlite" || run.Bytes <= 0 || run.StoppedSeconds != nil || len(run.Volumes) != 0 {
		t.Errorf("the backup = %+v, want sqlite, a size, and no stop time or volumes", run)
	}
	ts := strings.TrimPrefix(run.ID, "db-")
	if listed, err := g.bucketExec("ls", "/data/backups/gate/"+appID+"/db/"); err != nil || !strings.Contains(listed, ts+".sqlite.gz") {
		t.Errorf("the bucket's backups/gate/%s/db/ lists %q (%v), want %s.sqlite.gz", appID, listed, err, ts)
	}
	// The service never stopped: same copy, no restart, same job, and it kept writing.
	if id, restarts := copyOf(); id != copyBefore || restarts != restartsBefore {
		t.Errorf("after the backup the copy is %s with %d restarts, was %s with %d: a sqlite backup must not stop the service",
			id, restarts, copyBefore, restartsBefore)
	}
	if got := versionOf(); got != versionBefore {
		t.Errorf("the job is at version %d after the backup, was %d", got, versionBefore)
	}
	waitFor(t, "the app to go on writing", 30*time.Second, func() bool { return count() > rowsBefore })

	// A change after the backup, which the restore must undo.
	// It prints a word: query asks again when a statement prints nothing, and
	// the table can only be made once.
	query("CREATE TABLE IF NOT EXISTS marks(v); SELECT 'made'")
	if got := query("SELECT count(*) FROM sqlite_master WHERE name = 'marks'"); got != "1" {
		t.Fatalf("the marks table = %q after making it, want 1", got)
	}

	resp = g.api(t, http.MethodPost, "/v1/apps/"+appID+"/backups/"+run.ID+"/restore", "", "")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	var restoring struct{ ID string }
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &restoring) != nil || restoring.ID == "" {
		t.Fatalf("restore %s: %d %s", run.ID, resp.StatusCode, raw)
	}
	var restore gateVolumeRestore
	waitFor(t, "the sqlite restore to succeed", 3*time.Minute, func() bool {
		var l struct {
			Restores []gateVolumeRestore `json:"restores"`
		}
		g.getJSON(t, "/v1/apps/"+appID+"/backups", &l)
		for _, r := range l.Restores {
			if r.ID != restoring.ID {
				continue
			}
			switch r.Status {
			case "failed":
				t.Fatalf("the sqlite restore failed: %s", r.Message)
			case "succeeded":
				restore = r
				return true
			}
		}
		return false
	})
	if restore.Engine != "sqlite" || restore.BackupID != run.ID || restore.StoppedSeconds == nil || *restore.StoppedSeconds <= 0 {
		t.Errorf("restore = %+v, want sqlite, the backup %s and a stop time", restore, run.ID)
	} else {
		t.Logf("the service was stopped %.1f s for the restore", *restore.StoppedSeconds)
	}
	if got := query("SELECT count(*) FROM sqlite_master WHERE name = 'marks'"); got != "0" {
		t.Errorf("the marks table = %q after the restore, want 0: the restore brings the backup's database back", got)
	}
	if got := query("PRAGMA quick_check"); got != "ok" {
		t.Errorf("quick_check after the restore = %q", got)
	}
	if out, err := g.exec(t, appID, "db", d.ID, "stat", "-c", "%u:%g %a", "/data/app.db"); err != nil || strings.TrimSpace(out) != "1000:1000 640" {
		t.Errorf("/data/app.db after the restore is %q (%v), want 1000:1000 640: the old file's owner and mode", out, err)
	}
	if out, err := g.exec(t, appID, "db", d.ID, "ls", "-A", "/data"); err != nil || strings.Contains(out, ".caravana-restore") {
		t.Errorf("/data after the restore holds %q (%v), want no staging directory", out, err)
	}
	if got := versionOf(); got != versionBefore {
		t.Errorf("the job is at version %d after the restore, was %d", got, versionBefore)
	}

	// Retention keeps 2: after two more backups, the first is gone from the bucket.
	backUp()
	backUp()
	var l struct {
		Backups []gateVolumeRun `json:"backups"`
	}
	g.getJSON(t, "/v1/apps/"+appID+"/backups", &l)
	for _, b := range l.Backups {
		if b.ID == run.ID {
			t.Errorf("the first backup %s is still listed after two more with keep 2", run.ID)
		}
	}
	if listed, err := g.bucketExec("ls", "/data/backups/gate/"+appID+"/db/"); err != nil || strings.Contains(listed, ts+".sqlite.gz") ||
		strings.Count(listed, ".sqlite.gz") != 2 {
		t.Errorf("the bucket lists %q (%v), want two backups and not %s.sqlite.gz", listed, err, ts)
	}

	// A pinned backup outlives retention (feature 036): pinned as golden, then
	// five more backups with keep 2. Its record and its object stay, and two
	// unpinned ones are kept beside it.
	golden := backUp()
	g.pinBackup(t, appID, golden.ID, "golden")
	goldenTS := strings.TrimPrefix(golden.ID, "db-")
	// A change after the pin, which the scheduled restore must undo.
	query("CREATE TABLE IF NOT EXISTS later(v); SELECT 'made'")
	for range 5 {
		backUp()
	}
	var pinned struct {
		Backups []struct {
			ID  string `json:"id"`
			Pin string `json:"pin"`
		} `json:"backups"`
	}
	g.getJSON(t, "/v1/apps/"+appID+"/backups", &pinned)
	unpinned, kept := 0, false
	for _, b := range pinned.Backups {
		if b.ID == golden.ID {
			kept = b.Pin == "golden"
		} else {
			unpinned++
		}
	}
	if !kept || unpinned != 2 {
		t.Errorf("after five more backups the list has the golden one %v and %d others, want it pinned and 2 others: %+v", kept, unpinned, pinned.Backups)
	}
	if listed, err := g.bucketExec("ls", "/data/backups/gate/"+appID+"/db/"); err != nil || !strings.Contains(listed, goldenTS+".sqlite.gz") ||
		strings.Count(listed, ".sqlite.gz") != 3 {
		t.Errorf("the bucket lists %q (%v), want the pinned %s.sqlite.gz and two others", listed, err, goldenTS)
	}

	// A restore schedule whose slot is two minutes ahead restores the golden
	// backup: the table made after the pin is gone. The redeploy keeps the volume.
	d, slot := g.deployRestoreSchedule(t, appID, sqliteGateApp, "golden")
	if got := query("SELECT count(*) FROM sqlite_master WHERE name = 'later'"); got != "1" {
		t.Fatalf("the later table = %q after deploying the schedule, want 1: the restore waits for its slot", got)
	}
	g.waitScheduledRestore(t, appID, golden.ID, "golden", slot)
	if got := query("SELECT count(*) FROM sqlite_master WHERE name = 'later'"); got != "0" {
		t.Errorf("the later table = %q after the scheduled restore, want 0: the golden backup's database is back", got)
	}
	if got := query("PRAGMA quick_check"); got != "ok" {
		t.Errorf("quick_check after the scheduled restore = %q", got)
	}

	// The bucket's secret key is in no answer and not in the job.
	secret := "gate-" + g.run
	resp = g.api(t, http.MethodGet, "/v1/apps/"+appID+"/backups", "", "")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	job, _, err := nomad.Jobs().Info(appID, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobJSON, _ := json.Marshal(job)
	if strings.Contains(string(raw), secret) || strings.Contains(string(jobJSON), secret) {
		t.Error("the destination's secret key is in the backups' answer or the job")
	}
}
