package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 030, T020: what the update runs carry through Caravana. The updater's one-line REASON is
// shown (it is the updater's own sentence, by design), but the installer's log, a line of the status
// file that isn't one of its keys, the request file's bytes and the API token that asked never reach
// an answer or the server log because of this feature.
const (
	updSentinelReason = "s3cret-sentinel-030-reason"
	updSentinelLog    = "s3cret-sentinel-030-log"
	updSentinelStatus = "s3cret-sentinel-030-status"
)

func TestUpdateRunsKeepTheLogAndTheRequestOut(t *testing.T) {
	ctx := context.Background()
	u := &updaterEnv{dir: t.TempDir()}
	u.request = filepath.Join(t.TempDir(), "update-request")
	u.testEnv = newTestEnvWith(t, "", func(s *service.Service) {
		s.SetMetricsSampleGap(time.Hour)
		s.SetVersion("0.16.0")
		s.SetUpdaterDir(u.dir)
		s.SetUpdateRequestPath(u.request)
		s.SetUpdateWatchGap(20 * time.Millisecond) // the watcher reads the updater's status on its own
	})
	if err := os.WriteFile(filepath.Join(u.dir, "updater"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, _ := u.Store.ReleaseCheck(ctx)
	rc.LatestTag = "v0.17.0"
	if err := u.Store.SaveReleaseResult(ctx, rc, nil); err != nil {
		t.Fatal(err)
	}

	status, raw := u.post(t, `{"version":"0.17.0"}`)
	if status != http.StatusAccepted {
		t.Fatalf("POST = %d %s", status, raw)
	}
	id := decodeAs[map[string]any](t, raw)["id"].(string)
	requestBytes, err := os.ReadFile(u.request)
	if err != nil {
		t.Fatal(err)
	}

	// The updater, played by the test: its log holds a sentinel (the installer's output) and its
	// status says the update was rolled back, with a sentinel in the reason and one on a line that
	// is no key of the contract.
	if err := os.MkdirAll(filepath.Join(u.dir, "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(u.dir, "logs", id+".log")
	if err := os.WriteFile(logPath, []byte("caravana install: "+updSentinelLog+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	statusFile := "ID=" + id + "\nVERSION=0.17.0\nFROM=0.16.0\nSTATE=rolled_back\nSTARTED=" + now.Add(-time.Minute).Format(time.RFC3339) +
		"\nFINISHED=" + now.Format(time.RFC3339) + "\nREASON=" + updSentinelReason + "\nNOTE=" + updSentinelStatus + "\n"
	if err := os.WriteFile(filepath.Join(u.dir, "status"), []byte(statusFile), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, "the watcher to record the rolled-back run", func() bool {
		return strings.Contains(string(mustGet(t, u.testEnv, "/v1/updates/runs")), `"rolled_back"`)
	})

	var seen []string
	for _, p := range []string{"/v1/updates", "/v1/updates/runs", "/v1/attention", "/v1/activity", "/v1/activity?server=true"} {
		seen = append(seen, p+" "+string(mustGet(t, u.testEnv, p)))
	}
	// The reason is shown: it is the updater's sentence.
	if !strings.Contains(seen[1], updSentinelReason) {
		t.Errorf("listUpdateRuns doesn't show the reason: %s", seen[1])
	}
	// The log's path is told (where to read it as root), never what is in it.
	if !strings.Contains(seen[1], logPath) {
		t.Errorf("listUpdateRuns doesn't tell where the log is: %s", seen[1])
	}
	for _, s := range seen {
		head, _, _ := strings.Cut(s, " ")
		for name, secret := range map[string]string{
			"the log's content": updSentinelLog, "a line of the status that is no key": updSentinelStatus,
			"the request file's bytes": strings.TrimSpace(string(requestBytes)), "the API token": u.Token,
			"the request's ID line": "ID=" + id,
		} {
			if strings.Contains(s, secret) {
				t.Errorf("%s holds %s", head, name)
			}
		}
	}
	logs := u.Logs.String()
	if logs == "" {
		t.Fatal("the server log is empty: the check below proves nothing")
	}
	for name, secret := range map[string]string{
		"the log's content": updSentinelLog, "a line of the status that is no key": updSentinelStatus,
		"the reason": updSentinelReason, "the API token": u.Token, "the request file's bytes": "VERSION=0.17.0",
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("the server log holds %s", name)
		}
	}
}
