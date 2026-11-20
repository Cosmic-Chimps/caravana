package main

// The gate steps of feature 046: the dump engines mysql and mongodb. Each is a
// backed-up database service whose root password is a secret holding a
// sentinel, backed up on the policy's own timer, backed up again while a writer
// inserts, changed and restored, and then searched for the sentinel everywhere
// Caravana could have put it. They are in their own file only because
// gate_test.go is long; runGate lists them after sqlite-backup-restore.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

const mysqlGateCompose = `services:
  db:
    image: mariadb:11.4
    ports: ["3306"]
    environment:
      MARIADB_DATABASE: gate046
    mem_reservation: 256m
    mem_limit: 640m
    volumes: ["db-data:/var/lib/mysql"]
volumes:
  db-data: {}
`

// The wired tiger cache is cut to a quarter of a gigabyte: the lab VM has 2 GiB.
const mongoGateCompose = `services:
  db:
    image: mongo:8.0
    ports: ["27017"]
    command: ["mongod", "--wiredTigerCacheSizeGB", "0.25"]
    environment:
      MONGO_INITDB_ROOT_USERNAME: root
    mem_reservation: 256m
    mem_limit: 768m
    volumes: ["db-data:/data/db"]
volumes:
  db-data: {}
`

// dumpGate is what differs between the two engines' steps. The statements run
// in the service's own task with the login from the task's environment (the
// secret's value reaches the copy as MARIADB_ROOT_PASSWORD or
// MONGO_INITDB_ROOT_PASSWORD), so the gate's code never holds the sentinel
// but in the overlay it sends. Each prints a word, so Nomad's exec, which can
// report an exit before the output arrives, never makes a statement run twice.
type dumpGate struct {
	engine, appID, compose, sentinel string
	ext                              string // the object's extension in the bucket
	passwordName                     string // the secret's name in the overlay
	command                          func(statement string) []string
	prepare                          string // the database, the marker A and an empty writer table; prints "ready"
	insert                           func(i int) string
	change                           string // the marker A to B; prints "changed"
	readMark, countRows              string
}

func (g *gate) mysqlBackupRestoreStep(t *testing.T) {
	g.dumpBackupRestoreStep(t, dumpGate{
		engine: "mysql", appID: "lab046-mysql", compose: mysqlGateCompose, sentinel: "s3cret-sentinel-046-mysql",
		ext: ".sql.gz", passwordName: "MARIADB_ROOT_PASSWORD",
		command: func(statement string) []string {
			// The statement is the script's $1, so no quoting of it is needed.
			return []string{"sh", "-c", `exec mariadb --user=root --password="$MARIADB_ROOT_PASSWORD" -N -B -e "$1"`, "sh", statement}
		},
		prepare: "CREATE DATABASE IF NOT EXISTS gate046; CREATE TABLE IF NOT EXISTS gate046.marker(v VARCHAR(8)); " +
			"DELETE FROM gate046.marker; INSERT INTO gate046.marker VALUES ('A'); " +
			"CREATE TABLE IF NOT EXISTS gate046.w(i INT); DELETE FROM gate046.w; SELECT 'ready'",
		insert:    func(i int) string { return fmt.Sprintf("INSERT INTO gate046.w VALUES (%d); SELECT 'ok'", i) },
		change:    "UPDATE gate046.marker SET v = 'B'; SELECT 'changed'",
		readMark:  "SELECT v FROM gate046.marker",
		countRows: "SELECT count(*) FROM gate046.w",
	})
}

func (g *gate) mongodbBackupRestoreStep(t *testing.T) {
	g.dumpBackupRestoreStep(t, dumpGate{
		engine: "mongodb", appID: "lab046-mongodb", compose: mongoGateCompose, sentinel: "s3cret-sentinel-046-mongodb",
		ext: ".archive.gz", passwordName: "MONGO_INITDB_ROOT_PASSWORD",
		command: func(js string) []string {
			return []string{"sh", "-c", `exec mongosh --quiet --username "$MONGO_INITDB_ROOT_USERNAME" --password "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin --eval "$1"`, "sh", js}
		},
		prepare: `const d = db.getSiblingDB("gate046"); d.marker.deleteMany({}); d.marker.insertOne({v: "A"}); d.w.deleteMany({}); print("ready")`,
		insert: func(i int) string {
			return fmt.Sprintf(`db.getSiblingDB("gate046").w.insertOne({i: %d}); print("ok")`, i)
		},
		change:    `db.getSiblingDB("gate046").marker.updateOne({}, {$set: {v: "B"}}); print("changed")`,
		readMark:  `print(db.getSiblingDB("gate046").marker.findOne().v)`,
		countRows: `print(db.getSiblingDB("gate046").w.countDocuments({}))`,
	})
}

// allocOf is the running copy of the deployment's job version, found as exec
// finds it but without failing the test, so a goroutine may call it.
func (g *gate) allocOf(appID, service, deploymentID string) (*api.Allocation, error) {
	nomad := g.target.Nomad
	job, _, err := nomad.Jobs().Info(appID, nil)
	if err != nil {
		return nil, err
	}
	if job.Meta["caravana.deployment"] != deploymentID {
		return nil, fmt.Errorf("the live job is not deployment %s", deploymentID)
	}
	stubs, _, err := nomad.Jobs().Allocations(appID, false, nil)
	if err != nil {
		return nil, err
	}
	for _, s := range stubs {
		if s.ClientStatus == "running" && s.JobVersion == *job.Version && s.TaskGroup == service {
			a, _, err := nomad.Allocations().Info(s.ID, nil)
			return a, err
		}
	}
	return nil, fmt.Errorf("no running copy of %s/%s", appID, service)
}

// execAlloc runs one command in the task of alloc: its trimmed output, or an
// error holding the exit code and stderr. It never calls the test.
func (g *gate) execAlloc(alloc *api.Allocation, task string, cmd []string) (string, error) {
	var out, errOut strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, err := g.target.Nomad.Allocations().Exec(ctx, alloc, task, false, cmd, strings.NewReader(""), &out, &errOut, nil, nil)
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d: %s", code, strings.TrimSpace(errOut.String()))
	}
	return strings.TrimSpace(out.String()), err
}

// dumpBackupRestoreStep is the whole step; see the file's comment.
func (g *gate) dumpBackupRestoreStep(t *testing.T, d dumpGate) {
	appID, nomad := d.appID, g.target.Nomad
	del := func() {
		resp := g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "")
		resp.Body.Close()
	}
	del() // a leftover from an aborted run, if any
	g.startBucket(t)

	// The overlay: the policy runs every minute, and the root password is a secret.
	overlay, _ := json.Marshal(map[string]any{"compose": d.compose,
		"services": map[string]any{"db": map[string]any{
			"backup":  map[string]any{"engine": d.engine, "schedule": "* * * * *", "keep": 20},
			"secrets": []map[string]any{{"name": d.passwordName, "value": d.sentinel}},
		}}})
	resp := g.api(t, http.MethodPost, "/v1/apps/"+appID+"/compose", "application/json", string(overlay))
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var dep gateDeployment
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &dep) != nil {
		t.Fatalf("deploy %s: %d %s", appID, resp.StatusCode, raw)
	}
	t.Cleanup(del)
	if final := g.waitFinalOf(t, appID, dep.ID, 5*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
	}

	// Statements run in the service's task. The database may still be starting
	// (MongoDB makes its root user on a first start), so the first is asked
	// again until it answers.
	query := func(statement, want string) string {
		t.Helper()
		var out string
		var lastErr error
		if !waitUntil(3*time.Minute, func() bool {
			out, lastErr = g.exec(t, appID, "db", dep.ID, d.command(statement)...)
			return lastErr == nil && (want == "" || out == want)
		}) {
			t.Fatalf("%s: got %q (%v)", statement, out, lastErr)
		}
		return out
	}
	query(d.prepare, "ready")
	if got := query(d.readMark, ""); got != "A" {
		t.Fatalf("the marker is %q after preparing, want A", got)
	}

	type run struct {
		ID, Engine, Status, Trigger, Message string
		Bytes                                int64
	}
	type restoreRun struct{ ID, Status, Message string }
	type listing struct {
		Backups  []run        `json:"backups"`
		Restores []restoreRun `json:"restores"`
	}
	list := func() listing {
		t.Helper()
		var l listing
		g.getJSON(t, "/v1/apps/"+appID+"/backups", &l)
		return l
	}

	// 1. The policy's own timer: a run with trigger timer succeeds. Runs that
	// fail before the database is ready are expected and only logged.
	var timerRun run
	seenFailed := map[string]bool{}
	waitFor(t, "a timer backup to succeed", 6*time.Minute, func() bool {
		for _, b := range list().Backups {
			if b.Trigger != "timer" {
				continue
			}
			switch b.Status {
			case "succeeded":
				timerRun = b
				return true
			case "failed":
				if !seenFailed[b.ID] {
					seenFailed[b.ID] = true
					t.Logf("timer backup %s failed (the database may not have been ready): %s", b.ID, b.Message)
				}
			}
		}
		return false
	})
	if timerRun.Engine != d.engine || timerRun.Bytes <= 0 {
		t.Errorf("the timer backup = %+v, want engine %s and a size", timerRun, d.engine)
	}
	t.Logf("timer backup %s succeeded (%d bytes)", timerRun.ID, timerRun.Bytes)

	// 2. Back up now while a writer inserts a row every ~200 ms.
	alloc, err := g.allocOf(appID, "db", dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu               sync.Mutex
		inserted, failed int
		failures         []string
	)
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			out, err := g.execAlloc(alloc, "db", d.command(d.insert(i)))
			mu.Lock()
			if err != nil || out != "ok" {
				failed++
				if len(failures) < 5 {
					failures = append(failures, fmt.Sprintf("insert %d: %q (%v)", i, out, err))
				}
			} else {
				inserted++
			}
			mu.Unlock()
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}()
	stopWriter := sync.OnceFunc(func() { close(stop); writer.Wait() })
	t.Cleanup(stopWriter)
	waitFor(t, "the writer's first rows", time.Minute, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return inserted >= 5
	})

	// retry409 calls the API until it stops answering 409 (a timer run or the
	// other action is going), for up to three minutes.
	retry409 := func(path, requestBody string) []byte {
		t.Helper()
		var status int
		var body []byte
		if !waitUntil(3*time.Minute, func() bool {
			resp := g.api(t, http.MethodPost, path, "application/json", requestBody)
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			status = resp.StatusCode
			return status != http.StatusConflict
		}) {
			t.Fatalf("POST %s stayed 409: %s", path, body)
		}
		if status != http.StatusAccepted {
			t.Fatalf("POST %s = %d %s", path, status, body)
		}
		return body
	}
	var started struct{ ID string }
	if err := json.Unmarshal(retry409("/v1/apps/"+appID+"/backups", `{"service": "db"}`), &started); err != nil || started.ID == "" {
		t.Fatalf("back up now answered no run ID: %v", err)
	}
	var manual run
	waitFor(t, "Back up now to succeed", 5*time.Minute, func() bool {
		for _, b := range list().Backups {
			if b.ID != started.ID {
				continue
			}
			switch b.Status {
			case "failed":
				t.Fatalf("the backup made under writes failed: %s", b.Message)
			case "succeeded":
				manual = b
				return true
			}
		}
		return false
	})
	waitFor(t, "more rows after the backup", time.Minute, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return inserted >= 15
	})
	stopWriter()
	mu.Lock()
	t.Logf("the writer made %d inserts while the backup ran and after it, %d failed", inserted, failed)
	if failed > 0 {
		t.Errorf("%d inserts failed while Back up now ran: %v", failed, failures)
	}
	total := inserted
	mu.Unlock()
	if manual.Engine != d.engine || manual.Bytes <= 0 || manual.Trigger != "owner" {
		t.Errorf("Back up now's run = %+v, want engine %s, a size and trigger owner", manual, d.engine)
	}
	ts := strings.TrimPrefix(manual.ID, "db-")
	if listed, err := g.bucketList("/data/backups/gate/" + appID + "/db/"); err != nil || !strings.Contains(listed, ts+d.ext) {
		t.Errorf("the bucket's backups/gate/%s/db/ lists %q (%v), want %s%s", appID, listed, err, ts, d.ext)
	}

	// 3. A is backed up; B is written; the restore brings A back.
	query(d.change, "changed")
	if got := query(d.readMark, ""); got != "B" {
		t.Fatalf("the marker is %q after the change, want B", got)
	}
	var restoring struct{ ID string }
	if err := json.Unmarshal(retry409("/v1/apps/"+appID+"/backups/"+manual.ID+"/restore", ""), &restoring); err != nil || restoring.ID == "" {
		t.Fatalf("restore answered no ID: %v", err)
	}
	waitFor(t, "the restore to succeed", 5*time.Minute, func() bool {
		for _, r := range list().Restores {
			if r.ID != restoring.ID {
				continue
			}
			if r.Status == "failed" {
				t.Fatalf("the restore failed: %s", r.Message)
			}
			return r.Status == "succeeded"
		}
		return false
	})
	if got := query(d.readMark, ""); got != "A" {
		t.Errorf("after the restore the marker is %q, want A", got)
	}
	// The rows the writer made before the backup came back with it; none made
	// after can be more than it inserted.
	var rows int
	if _, err := fmt.Sscan(query(d.countRows, ""), &rows); err != nil || rows < 5 || rows > total {
		t.Errorf("after the restore the writer's table has %d rows (%v), want between 5 and %d", rows, err, total)
	} else {
		t.Logf("after the restore the writer's table has %d of the %d rows written", rows, total)
	}

	// 4. The sentinel is nowhere: the jobs, the export, every answer read, the log.
	answers := map[string]string{}
	for _, p := range []string{
		"/v1/apps/" + appID, "/v1/apps/" + appID + "/backups", "/v1/apps/" + appID + "/deployments",
		"/v1/apps/" + appID + "/deployments/" + dep.ID, "/v1/apps/" + appID + "/jobspec", "/v1/apps/" + appID + "/compose",
		"/v1/attention", "/v1/apps",
	} {
		status, body := g.send(t, http.MethodGet, p, "")
		if status != http.StatusOK {
			t.Errorf("GET %s = %d: %s", p, status, body)
		}
		answers[p] = string(body)
	}
	for _, id := range []string{appID + "_db_backup", appID + "_db_restore", appID} {
		job, _, err := nomad.Jobs().Info(id, nil)
		if err != nil {
			// A one-off restore job may be gone already: the other two must be there.
			if id != appID+"_db_restore" {
				t.Errorf("read the job %s: %v", id, err)
			} else {
				t.Logf("the job %s is gone (%v)", id, err)
			}
			continue
		}
		answers["the Nomad job "+id] = string(mustJSON(t, job))
	}
	if g.target.ServerLog != nil {
		answers["Caravana's log"] = g.target.ServerLog()
	}
	if dir := g.target.DataDir; dir != "" {
		for _, f := range []string{"caravana.db", "caravana.db-wal"} {
			b, _ := os.ReadFile(filepath.Join(dir, f))
			answers["the SQLite file "+f] = string(b)
		}
	}
	for where, text := range answers {
		if strings.Contains(text, d.sentinel) {
			t.Errorf("%s holds the root password's sentinel", where)
		}
	}

	// 5. Deleting the app leaves no job of its own.
	del()
	waitFor(t, "the app's jobs to go", time.Minute, func() bool {
		left, _, err := nomad.Jobs().List(&api.QueryOptions{Prefix: appID})
		return err == nil && len(left) == 0
	})
}
