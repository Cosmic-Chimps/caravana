package main

// The copy-files gate step (feature 2g4z): browse and download the files Nomad keeps for a copy, with the
// token Caravana really runs with (so a policy without read-fs fails here). It is in its own file only
// because gate_test.go is long; runGate lists it with the other steps.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// copyFilesStep:
//
//  1. a busybox app with a content file and a secret file (both through `files`) and a file it writes to
//     /alloc/data deploys healthy;
//  2. the root lists `alloc` and the service's task folder; the task folder lists `local` and `tmp` and
//     neither `secrets` nor `private`; `alloc` lists `data`, `logs` and `tmp`;
//  3. the content file's download has its bytes and the download headers; so does the file the app wrote;
//  4. a path in the task's secrets folder is 422 whichever way it is written (also through `..`), a missing
//     path is 404 file_not_found, a folder can't be downloaded;
//  5. the audit log has the downloads (copy, path, as download) and no entry for a listing;
//  6. the secret's value is in no answer, no log line and no byte of the data directory.
func (g *gate) copyFilesStep(t *testing.T) {
	const id = "lab2g4z"
	secret := "s3cret-sentinel-2g4z-lab-" + g.run
	content := "listen 8080;\n# gate 2g4z " + g.run + "\n"
	const reportText = "report from the copy\n"

	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // leftovers from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
	t.Cleanup(remove)

	// 1. Deploy.
	web := actionsWeb("", 1)
	// The app also makes links into its secrets folder, which Nomad would follow
	// (lab, 2026-11-28): Caravana follows none.
	web["args"] = []string{"-c", "mkdir -p /www /alloc/data/export; echo ok > /www/index.html; printf 'report from the copy\\n' > /alloc/data/export/report.txt; " +
		"ln -sf ../secrets/caravana-files/1 /local/peek; ln -sfn ../secrets /local/sdir; exec httpd -f -p 8080 -h /www"}
	web["secrets"] = []map[string]any{{"name": "GATE_KEY", "value": secret}}
	web["files"] = []map[string]any{
		{"path": "/etc/gate2g4z/app.conf", "content": content},
		{"path": "/etc/gate2g4z/key", "secret": "GATE_KEY", "mode": "0400"},
	}
	raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(web))
	var d gateDeployment
	if err := json.Unmarshal(raw, &d); err != nil || d.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	var copies []string
	waitFor(t, "a running copy of "+id, time.Minute, func() bool {
		copies = g.runningCopies(t, id)
		return len(copies) == 1
	})
	base := "/v1/apps/" + id + "/copies/" + copies[0] + "/files"

	type entry struct{ Name, Path, Kind string }
	var seen []string // every answer, searched for the secret at the end
	list := func(path string) []entry {
		t.Helper()
		body := g.sendWant(t, http.StatusOK, http.MethodGet, base+"?path="+url.QueryEscape(path), "")
		seen = append(seen, string(body))
		var out struct {
			Path    string
			Service string
			Entries []entry
		}
		if err := json.Unmarshal(body, &out); err != nil || out.Path != path || out.Service != "web" {
			t.Fatalf("list %q = %s (%v)", path, body, err)
		}
		return out.Entries
	}
	names := func(es []entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Name)
		}
		return out
	}

	// 2. Listings.
	if root := names(list("")); !slices.Contains(root, "alloc") || !slices.Contains(root, "web") {
		t.Errorf("the root lists %v, want alloc and web", root)
	}
	task := names(list("web"))
	if !slices.Contains(task, "local") || !slices.Contains(task, "tmp") || slices.Contains(task, "secrets") || slices.Contains(task, "private") {
		t.Errorf("the task folder lists %v, want local and tmp, and no secrets or private", task)
	}
	if alloc := names(list("alloc")); !slices.Contains(alloc, "data") || !slices.Contains(alloc, "logs") {
		t.Errorf("alloc lists %v, want data and logs", alloc)
	}
	if files := names(list("web/local/caravana-files")); !slices.Contains(files, "0") {
		t.Errorf("web/local/caravana-files lists %v, want the content file 0", files)
	}

	// 3. Downloads.
	download := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := g.apiRequest(http.MethodGet, base+"/download?path="+url.QueryEscape(path), "", "")
		if err != nil {
			t.Fatalf("download %q: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, string(body)
	}
	var report string
	waitFor(t, "the file the app wrote to /alloc/data", 30*time.Second, func() bool {
		resp, body := download("alloc/data/export/report.txt")
		report = body
		return resp.StatusCode == http.StatusOK
	})
	if report != reportText {
		t.Errorf("the app's file = %q, want %q", report, reportText)
	}
	resp, body := download("web/local/caravana-files/0")
	if resp.StatusCode != http.StatusOK || body != content {
		t.Fatalf("download of the content file = %d %q, want %q", resp.StatusCode, body, content)
	}
	for k, want := range map[string]string{
		"Content-Type": "application/octet-stream", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff",
		"Content-Disposition": "attachment; filename=0",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s of a download = %q, want %q", k, got, want)
		}
	}

	// 4. Refusals.
	for _, path := range []string{"web/secrets", "web/secrets/caravana-files/1", "web/private", "web/local/../secrets/caravana-files/1", "web/SECRETS"} {
		g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "path", http.MethodGet, base+"?path="+url.QueryEscape(path), "")
		g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "path", http.MethodGet, base+"/download?path="+url.QueryEscape(path), "")
	}
	// The links are listed as links and never followed.
	kinds := map[string]string{}
	for _, e := range list("web/local") {
		kinds[e.Name] = e.Kind
	}
	if kinds["peek"] != "link" || kinds["sdir"] != "link" {
		t.Errorf("web/local's links are listed as %q and %q, want link", kinds["peek"], kinds["sdir"])
	}
	for _, path := range []string{"web/local/peek", "web/local/sdir", "web/local/sdir/caravana-files/1"} {
		g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "path", http.MethodGet, base+"?path="+url.QueryEscape(path), "")
		g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "path", http.MethodGet, base+"/download?path="+url.QueryEscape(path), "")
	}
	g.refusal(t, http.StatusNotFound, "file_not_found", "path", http.MethodGet, base+"?path=nope", "")
	g.refusal(t, http.StatusNotFound, "file_not_found", "path", http.MethodGet, base+"/download?path=web/local/nope", "")
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "path", http.MethodGet, base+"/download?path=web/local", "")
	g.refusal(t, http.StatusUnprocessableEntity, "invalid_field", "path", http.MethodGet, base+"?path=web/local/caravana-files/0", "")

	// 5. The audit log has the downloads, with the path and never the content.
	rawAudit := g.sendWant(t, http.StatusOK, http.MethodGet, "/v1/audit?action="+url.QueryEscape("read a file"), "")
	seen = append(seen, string(rawAudit))
	var log struct {
		Events []struct {
			Action   string
			Resource struct{ ID string }
			Changes  []struct{ Path, To string }
		}
	}
	if err := json.Unmarshal(rawAudit, &log); err != nil {
		t.Fatalf("audit answer: %s (%v)", rawAudit, err)
	}
	var paths []string
	for _, e := range log.Events {
		if e.Resource.ID != id || e.Action != "read a file" {
			continue
		}
		to := map[string]string{}
		for _, c := range e.Changes {
			to[c.Path] = c.To
		}
		if to["as"] == "download" && to["copy"] == copies[0] {
			paths = append(paths, to["path"])
		}
	}
	for _, want := range []string{"web/local/caravana-files/0", "alloc/data/export/report.txt"} {
		if !slices.Contains(paths, want) {
			t.Errorf("the audit log has no download of %s (it has %v)", want, paths)
		}
	}
	if slices.Contains(paths, "") || slices.ContainsFunc(paths, func(p string) bool { return strings.Contains(p, "secrets") }) {
		t.Errorf("the audit log has an entry for a listing or a secret path: %v", paths)
	}

	// 6. The secret is in no answer, log or data file.
	for _, p := range []string{"/v1/apps/" + id, "/v1/apps/" + id + "/deployments", "/v1/apps/" + id + "/copies", "/v1/apps/" + id + "/actions",
		"/v1/apps/" + id + "/jobspec", "/v1/attention"} {
		seen = append(seen, string(g.sendWant(t, http.StatusOK, http.MethodGet, p, "")))
	}
	if g.target.ServerLog != nil {
		seen = append(seen, g.target.ServerLog())
	}
	if g.target.DataDir != "" {
		files, _ := filepath.Glob(filepath.Join(g.target.DataDir, "*"))
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil {
				seen = append(seen, string(b))
			}
		}
	}
	for i, text := range seen {
		if strings.Contains(text, secret) {
			t.Fatalf("the secret file's value is in an answer, the log or the data (#%d)", i)
		}
	}

	g.sendWant(t, http.StatusNoContent, http.MethodDelete, "/v1/apps/"+id, "")
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
}
