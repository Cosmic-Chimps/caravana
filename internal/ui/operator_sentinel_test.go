package ui

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
)

// Feature 027, T033 (SC-006), the console's part: the three Operator pages and their parts never
// carry a secret's value, a command's output, text of pro's answer or of apt's history, and the
// server log never carries them or the release's notes. The service's surfaces and every
// notification are read in internal/service/operator_sentinel_test.go, the API's answers in
// internal/api/operator_sentinel_test.go.
const (
	uiOpsSecret = "s3cret-sentinel-027-secret"
	uiOpsOutput = "s3cret-sentinel-027-output"
	uiOpsPro    = "s3cret-sentinel-027-pro"
	uiOpsApt    = "s3cret-sentinel-027-apt"
	uiOpsNotes  = "s3cret-sentinel-027-notes"
)

func TestOperatorPagesKeepSecretsAndOutputOut(t *testing.T) {
	ctx := context.Background()

	// A host whose apt history carries a sentinel in a command line, pro that does in its answer.
	history := "\nStart-Date: 2026-10-26  09:14:02\nCommandline: apt install --password=" + uiOpsApt + " htop\n" +
		"Install: htop:amd64 (3.3.0-4build1)\nEnd-Date: 2026-10-26  09:14:20\n"
	withHistory := func(root string) {
		path := filepath.Join(root, "var/log/apt/history.log")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(history), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pro := uiPro{ok: true, out: []byte(`{"packages":[{"package":"openssl","service_name":"standard-security","status":"upgrade_available",` +
		`"description":"` + uiOpsPro + `"}],"summary":{"num_standard_security_updates":1,"note":"` + uiOpsPro + `"}}`)}
	e, gh := updEnv(t, updOpts{fixture: "ubuntu-kernel-reboot", edit: withHistory, pro: pro})

	// The release's notes carry a sentinel: public text, shown on purpose.
	gh.mu.Lock()
	gh.body = "Highlights\n\n- One thing, " + uiOpsNotes + ".\n"
	gh.mu.Unlock()
	releaseFound(t, e)

	// The package names, asked for through the page's own button.
	if resp, body := e.postForm(t, "/ops/updates/check", url.Values{"what": {"packages"}}); resp.StatusCode != 303 {
		t.Fatalf("POST check packages = %d:\n%s", resp.StatusCode, text(body))
	}
	srvWait(t, "the package names", func() bool {
		v, err := e.Svc.Updates(ctx)
		return err == nil && !v.Checking.Packages && !v.OS.Updates.PackagesCheckedAt.IsZero()
	})

	// A secret deployed through the service (its value goes to Nomad Variables only), a copy to run in,
	// and a command that prints a sentinel.
	a := whoamiApp()
	a.Services[0].Secrets = []app.Secret{{Name: "API_KEY", Value: new(uiOpsSecret)}}
	rec, err := e.Svc.Deploy(ctx, a, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	e.waitDeploy(t, rec.Deployment.ID)
	e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
	e.Nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
		return deploytest.ExecReply{Stdout: uiOpsOutput + "\n", Stderr: uiOpsOutput + "-err\n"}
	})
	res, err := e.Svc.RunCommand(ctx, "whoami", "c1", []string{"echo", uiOpsOutput}, app.TriggerConsole)
	if err != nil || !strings.Contains(res.Stdout, uiOpsOutput) {
		t.Fatalf("RunCommand = %+v, %v: the output wasn't planted", res, err)
	}

	// Every page and part of the Operator view, as the HTML a browser gets.
	paths := []string{
		"/ops", "/parts/ops/server", "/parts/ops/apps", "/parts/ops/activity", "/parts/attention",
		"/ops/servers/lab", "/parts/ops/servers/lab",
		"/ops/updates", "/parts/ops/updates",
	}
	pages := map[string]string{}
	for _, p := range paths {
		resp, body := e.get(t, p)
		if resp.StatusCode != 200 {
			t.Errorf("GET %s = %d:\n%s", p, resp.StatusCode, text(body))
			continue
		}
		pages[p] = body
	}

	// The planted data is there: the page shows the release's notes and a package name on purpose, and
	// the activity names the command, so "absent" below isn't for lack of data.
	updates := text(pages["/ops/updates"])
	for _, want := range []string{uiOpsNotes, "openssl"} {
		if !strings.Contains(updates, want) {
			t.Errorf("/ops/updates lacks %q:\n%s", want, updates)
		}
	}
	if !strings.Contains(text(pages["/ops"]), "echo ran in whoami from the console") {
		t.Errorf("/ops lacks the command's line in the activity:\n%s", text(pages["/ops"]))
	}

	for path, body := range pages {
		for _, sentinel := range []string{uiOpsSecret, uiOpsOutput, uiOpsPro, uiOpsApt} {
			if strings.Contains(body, sentinel) {
				t.Errorf("%s holds %q", path, sentinel)
			}
		}
	}

	// The server log: none of them, and not the release's notes either.
	logs := e.Logs.String()
	if logs == "" {
		t.Fatal("the log is empty: the check below proves nothing")
	}
	for _, sentinel := range []string{uiOpsSecret, uiOpsOutput, uiOpsPro, uiOpsApt, uiOpsNotes} {
		if strings.Contains(logs, sentinel) {
			t.Errorf("the log holds %q", sentinel)
		}
	}
}
