package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 027, T033 (SC-006), the API's part: listActivity, getUpdates and getHost (and the other
// Operator reads: the needs-you list and the host's copies) never carry a secret's value, a
// command's output, text of pro's answer or of apt's history, and the server log never carries
// them or the release's notes. The service's own surfaces and every notification are read in
// internal/service/operator_sentinel_test.go, the pages in internal/ui/operator_sentinel_test.go.
const (
	apiOpsSecret = "s3cret-sentinel-027-secret"
	apiOpsOutput = "s3cret-sentinel-027-output"
	apiOpsPro    = "s3cret-sentinel-027-pro"
	apiOpsApt    = "s3cret-sentinel-027-apt"
	apiOpsNotes  = "s3cret-sentinel-027-notes"
)

func TestOperatorAnswersKeepSecretsAndOutputOut(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	// A release server whose notes hold a sentinel (public text, shown on purpose by getUpdates).
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_, _ = w.Write([]byte(`{"tag_name":"v0.16.0","html_url":"https://github.com/Cosmic-Chimps/caravana/releases/tag/v0.16.0",` +
				`"published_at":"2026-11-02T10:00:00Z","body":"- One, ` + apiOpsNotes + `.\n","draft":false,"prerelease":false}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(gh.Close)

	// A host whose apt history carries a sentinel in a command line, and pro that does in its answer.
	files := ubuntuFiles(now.Add(-time.Hour))
	history := "\nStart-Date: 2026-10-26  09:14:02\nCommandline: apt install --password=" + apiOpsApt + " htop\n" +
		"Install: htop:amd64 (3.3.0-4build1)\nEnd-Date: 2026-10-26  09:14:20\n"
	files["var/log/apt/history.log"] = &fstest.MapFile{Data: []byte(history)}
	pro := proFake{out: `{"packages":[{"package":"openssl","service_name":"standard-security","status":"upgrade_available",` +
		`"description":"` + apiOpsPro + `"}],"summary":{"num_standard_security_updates":1,"note":"` + apiOpsPro + `"}}`}

	e := newTestEnvWith(t, "", func(s *service.Service) {
		s.SetMetricsSampleGap(500 * time.Millisecond)
		s.SetVersion("0.15.0")
		s.SetReleaseURL(gh.URL + "/repos/x/releases/latest")
		s.SetReleasePinURLs(gh.URL+"/download", gh.URL+"/raw")
		s.SetHostRoot(files)
		s.SetProRunner(pro)
	})
	e.Nomad.SetNode("node-1", "lab", "ready", deploytest.LabNodeResources(), nil)
	e.Nomad.SetHostStats(deploytest.LabHostStats())
	e.Nomad.SetNodeFacts(deploytest.LabNodeAttributes(), "default", []*api.NodeEvent{
		{Subsystem: "Cluster", Message: "Node registered", Timestamp: now.Add(-time.Hour)},
	})

	// A secret deployed through the API (its value goes to Nomad Variables only), a copy to run in,
	// and a command that prints a sentinel.
	body := strings.Replace(whoamiBody, `"health":`, `"secrets": [{"name": "API_KEY", "value": "`+apiOpsSecret+`"}], "health":`, 1)
	d := deployOK(t, e, "whoami", body)
	waitHealthy(t, e, "whoami", d.ID)
	e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
	e.Nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
		return deploytest.ExecReply{Stdout: apiOpsOutput + "\n", Stderr: apiOpsOutput + "-err\n"}
	})
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/copies/c1/run", "application/json",
		`{"command": ["echo", "`+apiOpsOutput+`"]}`)
	if status != http.StatusOK || !strings.Contains(string(raw), apiOpsOutput) {
		t.Fatalf("run = %d %s: the output wasn't planted", status, raw)
	}

	// pro's package names (the owner asks for them), and the release check.
	if err := e.Service.CheckPackages(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Service.CheckRelease(ctx); err != nil {
		t.Fatal(err)
	}
	var updates []byte
	waitForCondition(t, "the package names, the release and the sampler's first reading", func() bool {
		_, _, updates = call(t, http.MethodGet, e.URL+"/v1/updates", "", "")
		_, _, host := call(t, http.MethodGet, e.URL+"/v1/host", "", "")
		s := string(updates)
		return strings.Contains(s, `"openssl"`) && strings.Contains(s, "0.16.0") && strings.Contains(string(host), `"node":{`)
	})

	// The planted data is there: the notes are shown (on purpose), a package name is shown.
	if !strings.Contains(string(updates), apiOpsNotes) {
		t.Errorf("getUpdates doesn't show the release's notes: %s", updates)
	}

	// Every Operator read, as the bytes a client gets.
	var seen []string
	for _, p := range []string{"/v1/activity", "/v1/activity?server=true", "/v1/updates", "/v1/host", "/v1/attention", "/v1/apps/whoami/copies",
		"/v1/apps", "/v1/apps/whoami/actions"} {
		status, _, raw := call(t, http.MethodGet, e.URL+p, "", "")
		if status != http.StatusOK {
			t.Errorf("GET %s = %d %s", p, status, raw)
		}
		seen = append(seen, p+" "+string(raw))
	}
	_, _, raw = call(t, http.MethodGet, e.URL+"/v1/activity", "", "")
	if !strings.Contains(string(raw), "echo ran in whoami") {
		t.Errorf("the activity lacks the command: %s", raw)
	}
	for _, s := range seen {
		head, _, _ := strings.Cut(s, " ")
		for _, sentinel := range []string{apiOpsSecret, apiOpsOutput, apiOpsPro, apiOpsApt} {
			if strings.Contains(s, sentinel) {
				t.Errorf("%s holds %q", head, sentinel)
			}
		}
	}

	// The server log: none of them, and not the release's notes either.
	logs := e.Logs.String()
	if logs == "" {
		t.Fatal("the log is empty: the check below proves nothing")
	}
	for _, sentinel := range []string{apiOpsSecret, apiOpsOutput, apiOpsPro, apiOpsApt, apiOpsNotes} {
		if strings.Contains(logs, sentinel) {
			t.Errorf("the log holds %q", sentinel)
		}
	}
}

// waitForCondition polls cond for up to 15 seconds.
func waitForCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(errors.New("timed out waiting for " + what))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
