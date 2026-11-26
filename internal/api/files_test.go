package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const filesSentinel = "s3cret-sentinel-060-key"

// filesApp is whoami with two secrets and two files: one holds a config, the other a secret.
func filesApp(files string) string {
	body := withSecrets(`[{"name": "TLS_KEY", "value": "` + filesSentinel + `"}, {"name": "TYPED", "value": "t"}]`)
	return strings.Replace(body, `"secrets":`, `"files": `+files+`, "secrets":`, 1)
}

const goodFiles = `[{"path": "/etc/app/app.yml", "content": "public: true\n", "mode": "0640", "uid": 1000},
	{"path": "/certs/node.key", "secret": "TLS_KEY", "mode": "0400"}]`

// Files (feature 060) deploy, are read back as written, are refused with the field named, and the value of a
// secret file is in the service's Nomad Variable and in no answer, record, job, export or log.
func TestDeployWithFilesEndpoint(t *testing.T) {
	e := newTestEnv(t, "")
	d := deployOK(t, e, "whoami", filesApp(goodFiles))
	waitHealthy(t, e, "whoami", d.ID)
	if got := e.Nomad.Variable("nomad/jobs/whoami/web"); got["TLS_KEY"] != filesSentinel || got["TYPED"] != "t" {
		t.Fatalf("variable = %v, want the file's secret and the typed one", got)
	}

	status, _, raw := call(t, http.MethodGet, e.URL+"/v1/apps/whoami", "", "")
	if status != http.StatusOK {
		t.Fatalf("GET app: %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "AppRecord")
	for _, want := range []string{`"path":"/etc/app/app.yml"`, `"secret":"TLS_KEY"`, `"mode":"0400"`, `"uid":1000`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("GET app lacks %s: %s", want, raw)
		}
	}
	seen := []string{string(raw)}
	for _, path := range []string{"/v1/apps", "/v1/apps/whoami/deployments", "/v1/apps/whoami/deployments/" + d.ID, "/v1/attention", "/v1/apps/whoami/jobspec"} {
		_, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
		seen = append(seen, string(raw))
	}
	if !strings.Contains(seen[len(seen)-1], `mount {`) {
		t.Errorf("the export has no mount:\n%s", seen[len(seen)-1])
	}

	failures := []struct {
		name, files, field string
	}{
		{"relative path", `[{"path": "etc/a", "content": "x"}]`, "services[0].files[0].path"},
		{"path in /proc", `[{"path": "/proc/a", "content": "x"}]`, "services[0].files[0].path"},
		{"neither", `[{"path": "/a"}]`, "services[0].files[0]"},
		{"both", `[{"path": "/a", "content": "x", "secret": "TLS_KEY"}]`, "services[0].files[0]"},
		{"unknown secret", `[{"path": "/a", "secret": "NOPE"}]`, "services[0].files[0].secret"},
		{"bad mode", `[{"path": "/a", "content": "x", "mode": "rw"}]`, "services[0].files[0].mode"},
		{"uid out of range", `[{"path": "/a", "content": "x", "uid": 70000}]`, "services[0].files[0].uid"},
		{"same path twice", `[{"path": "/a", "content": "x"}, {"path": "/a", "content": "y"}]`, "services[0].files[1].path"},
	}
	for _, f := range failures {
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/deployments", "application/json", filesApp(f.files))
		wantError(t, f.name, status, raw, 400, codeInvalidField, f.field)
		seen = append(seen, string(raw))
	}

	rec, err := e.Store.GetApp(context.Background(), "whoami")
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, string(mustJSON(t, rec)), string(mustJSON(t, e.Nomad.Job("whoami"))), e.Logs.String())
	for _, out := range seen {
		if strings.Contains(out, filesSentinel) {
			t.Errorf("the secret file's value leaked into: %.300s", out)
		}
	}
}
