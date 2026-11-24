package main

// The gate step of feature 052: several backup destinations and a second copy. The bucket job serves two
// buckets (/data/backups and /data/offsite); a Postgres service backs up to the first and has each backup copied
// to the second; the first copy of the backup is deleted, and a restore with no `from` reads the copy.
// It lives in its own file only because gate_test.go is long; runGate lists it after sqlite-backup-restore.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
)

// gateCopyBackup is the part of a Backup (schema Backup) this step reads.
type gateCopyBackup struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	Destination string `json:"destination"`
	Expired     bool   `json:"expired"`
	Copies      []struct {
		Destination string `json:"destination"`
		Status      string `json:"status"`
		Object      string `json:"object"`
		Message     string `json:"message"`
		Expired     bool   `json:"expired"`
	} `json:"copies"`
}

// gateDestTest is the answer of testBackupDestination.
type gateDestTest struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// backupCopiesStep, in the order it checks:
//  1. offsite is added through the API with the gate's endpoint and key, and Test passes for it and for main;
//  2. a destination whose bucket doesn't exist fails Test, the message holds no key, and it is removed;
//  3. a Postgres service with copyTo: ["offsite"] is backed up (Back up now): the backup and its copy succeed and the
//     object is under /data/offsite;
//  4. the data is changed, the object under /data/backups is deleted, and a restore with no `from` reads offsite,
//     says so on the restore, and brings the data back;
//  5. offsite can't be removed while the service copies to it;
//  6. the key is in no answer, Caravana's log, any job the step made (the copy and check jobs are read while they
//     live), the export or the SQLite file.
func (g *gate) backupCopiesStep(t *testing.T) {
	const appID = "gate-copies"
	nomad := g.target.Nomad
	g.send(t, http.MethodDelete, "/v1/apps/"+appID, "") // a leftover from an aborted run, if any
	g.send(t, http.MethodDelete, "/v1/backups/destinations/offsite", "")
	g.send(t, http.MethodDelete, "/v1/backups/destinations/nowhere", "")

	g.startBucket(t)
	secret := "gate-" + g.run // the bucket job's key (startBucket): the sentinel of this step
	var def map[string]any    // the default destination startBucket set: main on a host that had none
	g.getJSON(t, "/v1/backups/destination", &def)
	mainName, _ := def["name"].(string)
	endpoint, _ := def["endpoint"].(string)
	if mainName == "" || endpoint == "" {
		t.Fatalf("the default destination = %v", def)
	}
	t.Cleanup(func() {
		g.send(t, http.MethodDelete, "/v1/apps/"+appID, "")
		g.send(t, http.MethodDelete, "/v1/backups/destinations/offsite", "")
		g.send(t, http.MethodDelete, "/v1/backups/destinations/nowhere", "")
	})

	// The jobs of this step are read while they live: the copy and check jobs are purged when they end.
	var seenMu sync.Mutex
	seen := map[string]string{}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			for _, prefix := range []string{"_backup-check-", appID + "_"} {
				stubs, _, err := nomad.Jobs().List(&api.QueryOptions{Prefix: prefix})
				if err != nil {
					continue
				}
				for _, s := range stubs {
					if job, _, err := nomad.Jobs().Info(s.ID, nil); err == nil {
						b, _ := json.Marshal(job)
						seenMu.Lock()
						seen["the Nomad job "+s.ID] = string(b)
						seenMu.Unlock()
					}
				}
			}
			select {
			case <-stop:
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}()
	stopped := false
	stopPoll := func() {
		if !stopped {
			stopped = true
			close(stop)
			<-done
		}
	}
	defer stopPoll()

	answers := map[string]string{}

	// 1. offsite, and a Test of both.
	put := func(name, bucket string) []byte {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"endpoint": endpoint, "region": "us-east-1", "bucket": bucket, "prefix": "gate",
			"accessKeyId": "GATEKEY", "secretAccessKey": secret})
		return g.sendWant(t, http.StatusCreated, http.MethodPut, "/v1/backups/destinations/"+name, string(body))
	}
	answers["PUT offsite"] = string(put("offsite", "offsite"))
	testDest := func(name string) gateDestTest {
		t.Helper()
		raw := g.sendWant(t, http.StatusOK, http.MethodPost, "/v1/backups/destinations/"+name+"/test", "")
		answers["test "+name] = string(raw)
		var r gateDestTest
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatalf("test %s: %v: %s", name, err, raw)
		}
		return r
	}
	for _, name := range []string{mainName, "offsite"} {
		if r := testDest(name); !r.OK {
			t.Fatalf("Test %s failed: %s", name, r.Message)
		}
	}

	// 2. A destination whose bucket doesn't exist.
	answers["PUT nowhere"] = string(put("nowhere", "no-such-bucket"))
	if r := testDest("nowhere"); r.OK || r.Message == "" || strings.Contains(r.Message, secret) {
		t.Errorf("Test of a missing bucket = %+v, want ok false with a message that has no key", r)
	}
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/backups/destinations/nowhere", "")
	g.refusal(t, http.StatusNotFound, "destination_not_found", "", http.MethodPost, "/v1/backups/destinations/nowhere/test", "")
	var listed []struct {
		Name    string `json:"name"`
		Default bool   `json:"default"`
	}
	raw := g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/backups/destinations", "")
	answers["the list"] = string(raw)
	if err := json.Unmarshal(raw, &listed); err != nil || len(listed) != 2 {
		t.Fatalf("the destinations = %s (%v), want main and offsite", raw, err)
	}

	// 3. The service, backed up with a copy.
	body, _ := json.Marshal(map[string]any{"compose": gateDBCompose,
		"services": map[string]any{"db": map[string]any{"backup": map[string]any{"engine": "postgres", "keep": 2, "copyTo": []string{"offsite"}}}}})
	raw = g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+appID+"/compose", string(body))
	var d gateDeployment
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("deploy %s: %v: %s", appID, err, raw)
	}
	if final := g.waitFinalOf(t, appID, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
	}
	psql := func(sql string) string {
		t.Helper()
		var out string
		var lastErr error
		if !waitUntil(time.Minute, func() bool {
			out, lastErr = g.exec(t, appID, "db", d.ID, "psql", "-U", "postgres", "-tAc", sql)
			return lastErr == nil
		}) {
			t.Fatalf("psql %q: %v", sql, lastErr)
		}
		return strings.TrimSpace(out)
	}
	psql("create table if not exists gate(v text); delete from gate; insert into gate values ('A')")

	type listing struct {
		Backups  []gateCopyBackup `json:"backups"`
		Restores []struct {
			Status      string `json:"status"`
			Message     string `json:"message"`
			Destination string `json:"destination"`
			BackupID    string `json:"backupId"`
		} `json:"restores"`
	}
	list := func() listing {
		t.Helper()
		var l listing
		g.getJSON(t, "/v1/apps/"+appID+"/backups", &l)
		return l
	}
	g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+appID+"/backups", `{"service": "db"}`)
	var backup gateCopyBackup
	waitFor(t, "the backup and its copy to succeed", 5*time.Minute, func() bool {
		for _, b := range list().Backups {
			if b.Status == "failed" {
				t.Fatalf("the backup failed: %s", b.Message)
			}
			for _, c := range b.Copies {
				if c.Status == "failed" {
					t.Fatalf("the copy to %s failed: %s", c.Destination, c.Message)
				}
			}
			if b.Status == "succeeded" && len(b.Copies) == 1 && b.Copies[0].Status == "succeeded" {
				backup = b
				return true
			}
		}
		return false
	})
	if backup.Destination != mainName || backup.Copies[0].Destination != "offsite" || backup.Expired {
		t.Fatalf("the backup = %+v, want it in %s, copied to offsite", backup, mainName)
	}
	// The object is in both buckets. Retention keeps 2, so exactly one backup is there.
	mainDir, copyDir := "/data/backups/gate/"+appID+"/db", "/data/offsite/gate/"+appID+"/db"
	mainObjects, err := g.bucketList(mainDir)
	if err != nil || !strings.HasSuffix(strings.TrimSpace(mainObjects), ".dump") {
		t.Fatalf("%s holds %q (%v), want one .dump", mainDir, mainObjects, err)
	}
	if copyObjects, err := g.bucketList(copyDir); err != nil || strings.TrimSpace(copyObjects) != strings.TrimSpace(mainObjects) {
		t.Fatalf("%s holds %q (%v), want the same object as %s (%q)", copyDir, copyObjects, err, mainDir, mainObjects)
	}

	// 4. Change the data, lose the first copy, restore from the second.
	psql("update gate set v = 'B'")
	// Deleted through the bucket's own S3 API, as retention (or a person) would: rclone serve s3 caches its
	// directory listings, so removing the file under it would leave the check job and the download seeing it.
	object := "backups/gate/" + appID + "/db/" + strings.TrimSpace(mainObjects)
	conn := ":s3,provider=Other,region=us-east-1,force_path_style=true,no_check_bucket=true,endpoint='http://127.0.0.1:9000',access_key_id=GATEKEY,secret_access_key=" + secret
	if _, err := g.bucketExec("rclone", "deletefile", conn+":"+object); err != nil {
		seen, _ := g.bucketExec("rclone", "lsf", "--recursive", conn+":backups")
		t.Fatalf("delete %s through the bucket's S3 API: %v\nthe S3 API lists: %s", object, err, seen)
	}
	if left, _ := g.bucketExec("ls", mainDir); strings.TrimSpace(left) != "" {
		t.Fatalf("%s still holds %q after the delete", mainDir, left)
	}
	g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+appID+"/backups/"+backup.ID+"/restore", "")
	if !waitUntil(5*time.Minute, func() bool {
		r := list().Restores
		if len(r) > 0 && r[0].Status == "failed" {
			mainLeft, _ := g.bucketExec("ls", mainDir)
			copyLeft, _ := g.bucketExec("ls", copyDir)
			t.Fatalf("the restore failed: %s (read from %q; backups = %+v; %s holds %q, %s holds %q)",
				r[0].Message, r[0].Destination, list().Backups, mainDir, mainLeft, copyDir, copyLeft)
		}
		return len(r) > 0 && r[0].Status == "succeeded"
	}) {
		log := ""
		if g.target.ServerLog != nil {
			log = g.target.ServerLog()
			if len(log) > 6000 {
				log = log[len(log)-6000:]
			}
		}
		mainLeft, _ := g.bucketExec("ls", mainDir)
		copyLeft, _ := g.bucketExec("ls", copyDir)
		t.Fatalf("timed out waiting for the restore to succeed; restores = %+v, backups = %+v, %s holds %q, %s holds %q\nend of Caravana's log:\n%s",
			list().Restores, list().Backups, mainDir, mainLeft, copyDir, copyLeft, log)
	}
	l := list()
	if r := l.Restores[0]; r.Destination != "offsite" {
		t.Errorf("the restore read from %q, want offsite", r.Destination)
	}
	if len(l.Backups) != 1 || !l.Backups[0].Expired || len(l.Backups[0].Copies) != 1 || l.Backups[0].Copies[0].Expired {
		t.Errorf("the backup after the restore = %+v, want it gone from %s and kept in offsite", l.Backups, mainName)
	}
	if got := psql("select v from gate"); got != "A" {
		t.Fatalf("after the restore the row is %q, want A", got)
	}

	// 5. offsite is in use.
	msg := g.refusal(t, http.StatusConflict, "destination_in_use", "", http.MethodDelete, "/v1/backups/destinations/offsite", "")
	if !strings.Contains(msg, appID) {
		t.Errorf("the refusal %q doesn't name %s", msg, appID)
	}

	// 6. The key is nowhere. What the later checks read, while the app has its job and its records.
	for _, p := range []string{"/v1/apps/" + appID, "/v1/apps/" + appID + "/deployments", "/v1/apps/" + appID + "/jobspec", "/v1/apps/" + appID + "/backups",
		"/v1/backups/destination", "/v1/backups/destinations", "/v1/attention", "/v1/apps", "/v1/projects/default"} {
		status, b := g.send(t, http.MethodGet, p, "")
		if status != http.StatusOK {
			t.Errorf("GET %s = %d: %s", p, status, b)
		}
		answers[p] = string(b)
	}
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+appID, "")
	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/backups/destinations/offsite", "")
	if left, _, err := nomad.Jobs().List(&api.QueryOptions{Prefix: appID + "_"}); err != nil || len(left) != 0 {
		t.Errorf("jobs left after the delete: %d (%v)", len(left), err)
	}
	stopPoll()
	seenMu.Lock()
	for where, text := range seen {
		answers[where] = text
	}
	seenMu.Unlock()
	var checks, copies int
	for where := range answers {
		switch {
		case strings.HasPrefix(where, "the Nomad job _backup-check-"):
			checks++
		case strings.Contains(where, "_copy-"):
			copies++
		}
	}
	if checks == 0 || copies == 0 {
		t.Errorf("the step read %d check and %d copy jobs, want some of each", checks, copies)
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
		if strings.Contains(text, secret) {
			t.Errorf("%s holds the destination's secret key", where)
		}
	}
}
