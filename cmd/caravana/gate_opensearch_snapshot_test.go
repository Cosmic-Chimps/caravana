package main

// The opensearch-snapshot gate step (spec 061, D11): an OpenSearch cluster with its security plugin on is
// backed up with the opensearch engine and restored, while it keeps running. It is in its own file only
// because gate_test.go is long; runGate lists it with the other steps.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openSearchAdminSentinel is the cluster's admin password. Only the secret store, the copy's
// environment and the standard input of the backup's commands may ever have it. It is strong enough for
// the image's demo security setup, which refuses a weak one.
const openSearchAdminSentinel = "S3cret-Sentinel-061-gate"

// openSearchSnapshotRepo is the snapshot folder, the mount of the service's volume.
const openSearchSnapshotRepo = "/usr/share/opensearch/snapshots"

// openSearchSnapshotService is the cluster: security on (the demo configuration the image installs, with
// the admin password from a secret), path.repo set by the variable the image reads, the snapshot folder on
// a volume, and the opensearch engine keeping one unpinned backup.
func openSearchSnapshotService() map[string]any {
	return map[string]any{
		"name": "search", "image": openSearchImage,
		"env": []map[string]string{
			{"name": "discovery.type", "value": "single-node"},
			{"name": "path.repo", "value": openSearchSnapshotRepo},
			{"name": "OPENSEARCH_JAVA_OPTS", "value": "-Xms384m -Xmx384m"},
		},
		"secrets": []map[string]any{{"name": "OPENSEARCH_INITIAL_ADMIN_PASSWORD", "value": openSearchAdminSentinel}},
		"ulimits": map[string]any{
			"memlock": map[string]int{"soft": -1, "hard": -1},
			"nofile":  map[string]int{"soft": 65536, "hard": 65536},
		},
		"ports":     []map[string]any{{"name": "http", "containerPort": 9200}},
		"health":    map[string]any{"interval": "10s", "timeout": "5s"},
		"resources": map[string]any{"memoryMB": 768, "memoryMaxMB": 1024},
		"volumes":   []map[string]any{{"name": "snapshots", "path": openSearchSnapshotRepo}},
		"backup":    map[string]any{"engine": "opensearch", "repository": openSearchSnapshotRepo, "keep": 1},
	}
}

// openSearchSnapshotStep (spec 061, D11):
//
//  1. a repository outside every volume, and one with another engine, are refused naming the field;
//  2. the cluster deploys healthy with its security plugin on;
//  3. Back up now succeeds with the service running: same copy, no restart, same job version, the object
//     in the bucket, and no run's folder left in the repository;
//  4. after a change (a document rewritten, an index added), restoring the backup brings the document
//     back and leaves the new index, still with no restart;
//  5. a pinned backup outlives retention (keep 1);
//  6. the admin password is in no answer, log line, job, export or byte of the data directory.
func (g *gate) openSearchSnapshotStep(t *testing.T) {
	const appID = "lab061"
	nomad := g.target.Nomad
	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+appID, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // a leftover from an aborted run, if any
	waitFor(t, appID+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := nomad.Jobs().Info(appID, nil)
		return err != nil
	})
	t.Cleanup(remove)

	g.startBucket(t)

	// 1. Refusals name the repository field.
	outside := openSearchSnapshotService()
	outside["backup"] = map[string]any{"engine": "opensearch", "repository": "/tmp/snapshots"}
	g.refusal(t, http.StatusBadRequest, "invalid_field", "services[0].backup.repository", http.MethodPost,
		"/v1/apps/"+appID+"/deployments", labApp(outside))
	otherEngine := openSearchSnapshotService()
	otherEngine["backup"] = map[string]any{"engine": "volume", "repository": openSearchSnapshotRepo}
	g.refusal(t, http.StatusBadRequest, "invalid_field", "services[0].backup.repository", http.MethodPost,
		"/v1/apps/"+appID+"/deployments", labApp(otherEngine))
	if _, _, err := nomad.Jobs().Info(appID, nil); err == nil {
		t.Fatalf("a refused deploy registered %s", appID)
	}

	// 2. Deploy.
	raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+appID+"/deployments", labApp(openSearchSnapshotService()))
	var d gateDeployment
	if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
		t.Fatalf("deploy %s: %s", appID, raw)
	}
	if final := g.waitFinalOf(t, appID, d.ID, 8*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
	}

	// api calls the cluster from inside its own task, logged in with the password the task's
	// environment holds (so the test never puts it in an argument either).
	api := func(method, path, body string) string {
		t.Helper()
		script := `curl -sk -u "admin:$OPENSEARCH_INITIAL_ADMIN_PASSWORD" -X "$1" -H 'Content-Type: application/json' "https://127.0.0.1:9200$2"`
		if body != "" {
			script += ` --data-binary "$3"`
		}
		out, err := g.exec(t, appID, "search", d.ID, "sh", "-c", script, "sh", method, path, body)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return out
	}
	waitFor(t, "the cluster to answer with its security plugin on", 3*time.Minute, func() bool {
		return strings.Contains(api(http.MethodGet, "/_cluster/health", ""), `"status"`)
	})
	if out := api(http.MethodPut, "/docs/_doc/1?refresh=true", `{"v":"A"}`); !strings.Contains(out, `"result"`) {
		t.Fatalf("index a document: %s", out)
	}
	doc := func() string {
		t.Helper()
		var v struct {
			Source struct {
				V string `json:"v"`
			} `json:"_source"`
		}
		out := api(http.MethodGet, "/docs/_doc/1", "")
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("read the document: %v: %s", err, out)
		}
		return v.Source.V
	}

	versionOf := func() uint64 {
		t.Helper()
		job, _, err := nomad.Jobs().Info(appID, nil)
		if err != nil || job.Version == nil {
			t.Fatalf("read job %s: %v", appID, err)
		}
		return *job.Version
	}
	copyOf := func() (string, uint64) {
		t.Helper()
		stubs, _, err := nomad.Jobs().Allocations(appID, false, nil)
		if err != nil {
			t.Fatalf("list the copies of %s: %v", appID, err)
		}
		for _, s := range stubs {
			if s.DesiredStatus == "run" && s.ClientStatus == "running" {
				if st := s.TaskStates["search"]; st != nil {
					return s.ID, st.Restarts
				}
				return s.ID, 0
			}
		}
		t.Fatalf("%s has no running copy", appID)
		return "", 0
	}
	untouched := func(after string, version uint64, copyID string, restarts uint64) {
		t.Helper()
		if id, r := copyOf(); id != copyID || r != restarts {
			t.Errorf("after the %s the copy is %s with %d restarts, was %s with %d: the engine must not stop the cluster",
				after, id, r, copyID, restarts)
		}
		if got := versionOf(); got != version {
			t.Errorf("the job is at version %d after the %s, was %d", got, after, version)
		}
		if out, err := g.exec(t, appID, "search", d.ID, "sh", "-c", "ls -A "+openSearchSnapshotRepo+"; echo end"); err != nil || strings.Contains(out, "caravana-") {
			t.Errorf("the repository after the %s holds %q (%v), want no run's folder", after, out, err)
		}
	}
	version := versionOf()
	copyID, restarts := copyOf()

	backUp := func() gateVolumeRun {
		t.Helper()
		raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+appID+"/backups", `{"service": "search"}`)
		var started struct{ ID string }
		if json.Unmarshal(raw, &started) != nil || started.ID == "" {
			t.Fatalf("back up now: %s", raw)
		}
		var run gateVolumeRun
		waitFor(t, "the opensearch backup to succeed", 4*time.Minute, func() bool {
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
					t.Fatalf("the opensearch backup failed: %s", b.Message)
				case "succeeded":
					run = b
					return true
				}
			}
			return false
		})
		return run
	}

	// 3. Back up now, with the cluster running.
	golden := backUp()
	if golden.Engine != "opensearch" || golden.Bytes <= 0 || golden.StoppedSeconds != nil {
		t.Errorf("the backup = %+v, want opensearch, a size and no stop time", golden)
	}
	goldenTS := strings.TrimPrefix(golden.ID, "search-")
	bucketDir := "/data/backups/gate/" + appID + "/search/"
	if listed, err := g.bucketList(bucketDir); err != nil || !strings.Contains(listed, goldenTS+".opensearch.tar.gz") {
		t.Errorf("the bucket's %s lists %q (%v), want %s.opensearch.tar.gz", bucketDir, listed, err, goldenTS)
	}
	untouched("backup", version, copyID, restarts)
	g.pinBackup(t, appID, golden.ID, "golden")

	// 4. A change the restore must undo, and an index it must leave.
	api(http.MethodPut, "/docs/_doc/1?refresh=true", `{"v":"B"}`)
	api(http.MethodPut, "/later/_doc/1?refresh=true", `{"v":"later"}`)
	if got := doc(); got != "B" {
		t.Fatalf("the document = %q after the change, want B", got)
	}
	second := backUp() // unpinned: retention takes it once a third one is made

	raw = g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+appID+"/backups/"+golden.ID+"/restore", "")
	var restoring struct{ ID string }
	if json.Unmarshal(raw, &restoring) != nil || restoring.ID == "" {
		t.Fatalf("restore %s: %s", golden.ID, raw)
	}
	var restore gateVolumeRestore
	waitFor(t, "the opensearch restore to succeed", 4*time.Minute, func() bool {
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
				t.Fatalf("the opensearch restore failed: %s", r.Message)
			case "succeeded":
				restore = r
				return true
			}
		}
		return false
	})
	if restore.Engine != "opensearch" || restore.BackupID != golden.ID || restore.StoppedSeconds != nil {
		t.Errorf("restore = %+v, want opensearch, the backup %s and no stop time", restore, golden.ID)
	}
	if got := doc(); got != "A" {
		t.Errorf("the document = %q after the restore, want A: the restore brings the snapshot's indices back", got)
	}
	if out := api(http.MethodGet, "/later/_doc/1", ""); !strings.Contains(out, `"found":true`) {
		t.Errorf("the index made after the backup is gone after the restore: %s", out)
	}
	untouched("restore", version, copyID, restarts)

	// 5. Retention keeps one unpinned backup beside the pinned one.
	third := backUp()
	listed, err := g.bucketList(bucketDir)
	if err != nil {
		t.Fatalf("list the bucket: %v", err)
	}
	secondTS, thirdTS := strings.TrimPrefix(second.ID, "search-"), strings.TrimPrefix(third.ID, "search-")
	if !strings.Contains(listed, goldenTS+".opensearch.tar.gz") || !strings.Contains(listed, thirdTS+".opensearch.tar.gz") ||
		strings.Contains(listed, secondTS+".opensearch.tar.gz") {
		t.Errorf("the bucket lists %q, want the pinned %s and the newest %s, not %s", listed, goldenTS, thirdTS, secondTS)
	}

	// 6. The admin password is nowhere it shouldn't be.
	job, _, err := nomad.Jobs().Info(appID, nil)
	if err != nil {
		t.Fatalf("read the job %s: %v", appID, err)
	}
	jobJSON, _ := json.Marshal(job)
	if strings.Contains(string(jobJSON), openSearchAdminSentinel) {
		t.Error("the Nomad job holds the admin password")
	}
	g.exportMatchesLive(t, appID, openSearchAdminSentinel)
	var later []string
	for _, p := range []string{"/v1/apps/" + appID, "/v1/apps/" + appID + "/deployments", "/v1/apps/" + appID + "/backups",
		"/v1/apps/" + appID + "/actions", "/v1/apps", "/v1/attention", "/v1/activity"} {
		resp, err := g.apiRequest(http.MethodGet, p, "", "")
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		later = append(later, string(b))
	}
	if g.target.ServerLog != nil {
		later = append(later, g.target.ServerLog())
	}
	if g.target.DataDir != "" {
		files, _ := filepath.Glob(filepath.Join(g.target.DataDir, "*"))
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil {
				later = append(later, string(b))
			}
		}
	}
	for i, text := range later {
		if strings.Contains(text, openSearchAdminSentinel) {
			t.Fatalf("the admin password is in Caravana's answer, log or data (#%d)", i)
		}
	}
}
