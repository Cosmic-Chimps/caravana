package main

// The stop-timeout gate step: how long a service gets to stop before Nomad
// kills it, seen through a volume backup, which stops the service. It is in
// its own file only because gate_test.go is long. It starts its own bucket and
// backup destination (startBucket), so it needs no earlier step.

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// stopTimeoutApp is a service whose shutdown takes ten seconds: on SIGTERM
// it waits, then writes /data/clean and exits. Killed earlier, the file is
// never written. stopTimeout is left out of the body when it is 0.
func stopTimeoutApp(stopTimeout int) string {
	svc := map[string]any{
		"name": "web", "image": "busybox:1.37", "command": "sh",
		"args":      []string{"-c", `trap "sleep 10; touch /data/clean; exit 0" TERM; while :; do sleep 1; done`},
		"resources": labResources,
		"volumes":   []map[string]any{{"name": "data", "path": "/data"}},
		"backup":    map[string]any{"engine": "volume", "keep": 2},
	}
	if stopTimeout > 0 {
		svc["stopTimeout"] = stopTimeout
	}
	body, _ := json.Marshal(map[string]any{"source": map[string]any{"kind": "image"}, "services": []any{svc}})
	return string(body)
}

// stopTimeoutStep: a service with a volume and a slow shutdown is backed up
// twice, with the token Caravana really runs with.
//
//  1. stopTimeout 2: the backup stops the service, Nomad kills it after 2 s,
//     before its shutdown writes /data/clean, so the file isn't there;
//  2. a deploy without stopTimeout: a service with a volume gets 30 s, its
//     shutdown finishes, and /data/clean is there when the backup ends.
//
// Both backups must succeed.
func (g *gate) stopTimeoutStep(t *testing.T) {
	const appID = "stopt"
	resp := g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "") // a leftover from an aborted run, if any
	resp.Body.Close()
	t.Cleanup(func() {
		resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+appID, "", "")
		if err == nil {
			resp.Body.Close()
		}
	})

	g.startBucket(t)

	// deploy sends the app and returns the deployment's ID once it is healthy.
	deploy := func(body string) string {
		t.Helper()
		resp := g.api(t, http.MethodPost, "/v1/apps/"+appID+"/deployments", "application/json", body)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var d gateDeployment
		if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &d) != nil {
			t.Fatalf("deploy %s: %d %s", appID, resp.StatusCode, raw)
		}
		if final := g.waitFinalOf(t, appID, d.ID, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("deploy %s ended %s: %s", appID, final.Status, final.Error)
		}
		return d.ID
	}
	// exitCode runs `test -f /data/clean` in the copy of the deployment and
	// returns its exit code. The shell prints the code, because Nomad's exec
	// can report the exit before the output has arrived: an empty answer is
	// asked again, and the copy may still be starting after the backup.
	exitCode := func(deploymentID string) string {
		t.Helper()
		var got string
		var lastErr error
		if !waitUntil(time.Minute, func() bool {
			got, lastErr = g.execOnce(t, appID, "web", deploymentID, "sh", "-c", "test -f /data/clean; echo $?")
			return lastErr == nil && got != ""
		}) {
			t.Fatalf("test -f /data/clean: %q (%v)", got, lastErr)
		}
		return got
	}
	// backUp backs the service up now, waits for the run and returns it.
	backUp := func() gateVolumeRun {
		t.Helper()
		resp := g.api(t, http.MethodPost, "/v1/apps/"+appID+"/backups", "application/json", `{"service": "web"}`)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var started struct{ ID string }
		if resp.StatusCode != http.StatusAccepted || json.Unmarshal(raw, &started) != nil || started.ID == "" {
			t.Fatalf("back up now: %d %s", resp.StatusCode, raw)
		}
		var run gateVolumeRun
		waitFor(t, "the volume backup to end", 5*time.Minute, func() bool {
			var l struct {
				Backups []gateVolumeRun `json:"backups"`
			}
			g.getJSON(t, "/v1/apps/"+appID+"/backups", &l)
			for _, b := range l.Backups {
				if b.ID == started.ID && (b.Status == "failed" || b.Status == "succeeded") {
					run = b
					return true
				}
			}
			return false
		})
		if run.Status != "succeeded" {
			t.Fatalf("the volume backup %s: %s", run.Status, run.Message)
		}
		return run
	}
	// stopped says how long the volume task measured the service stopped, for
	// the log: it doesn't include the time the service took to shut down.
	stopped := func(run gateVolumeRun) float64 {
		if run.StoppedSeconds == nil {
			return 0
		}
		return *run.StoppedSeconds
	}

	// A: a 2 s stop timeout kills the service before its shutdown writes the file.
	id := deploy(stopTimeoutApp(2))
	if out, err := g.exec(t, appID, "web", id, "sh", "-c", "rm -f /data/clean; echo removed"); err != nil || out != "removed" {
		t.Fatalf("remove /data/clean: %q (%v)", out, err)
	}
	run := backUp()
	code := exitCode(id)
	if code != "1" {
		t.Errorf("stopTimeout 2: test -f /data/clean exits %s, want 1: the service should be killed before its shutdown finishes", code)
	}
	t.Logf("stopTimeout 2: backup %s, service stopped %.1f s, test -f /data/clean exit %s", run.Status, stopped(run), code)

	// B: without stopTimeout, a service with a volume gets 30 s, enough to finish.
	id = deploy(stopTimeoutApp(0))
	if out, err := g.exec(t, appID, "web", id, "sh", "-c", "rm -f /data/clean; echo removed"); err != nil || out != "removed" {
		t.Fatalf("remove /data/clean: %q (%v)", out, err)
	}
	run = backUp()
	code = exitCode(id)
	if code != "0" {
		t.Errorf("no stopTimeout: test -f /data/clean exits %s, want 0: the volume default should let the shutdown finish", code)
	}
	t.Logf("no stopTimeout: backup %s, service stopped %.1f s, test -f /data/clean exit %s", run.Status, stopped(run), code)

	resp = g.api(t, http.MethodDelete, "/v1/apps/"+appID, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete %s: %d", appID, resp.StatusCode)
	}
}
