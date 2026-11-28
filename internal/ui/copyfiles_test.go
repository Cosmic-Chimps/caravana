package ui

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
)

const filesSentinelUI = "s3cret-sentinel-2g4z"

// filesEnv is the actions env with a tree of files in copy 1: a task's secrets
// and private folders hold a sentinel that must never show.
func filesEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := actionsEnv(t)
	e.Nomad.WriteAllocFile(actCopy1, "web/local/caravana-files/0", "listen 8080;\n<b>not markup</b>\n")
	e.Nomad.WriteAllocFile(actCopy1, "web/local/dump.bin", "PK\x00\x01binary")
	e.Nomad.WriteAllocFile(actCopy1, "web/local/empty.txt", "")
	e.Nomad.WriteAllocFile(actCopy1, "alloc/data/big.log", strings.Repeat("x", 512<<10+1))
	e.Nomad.WriteAllocFile(actCopy1, "web/secrets/token", filesSentinelUI)
	e.Nomad.WriteAllocFile(actCopy1, "web/private/key", filesSentinelUI)
	return e
}

func filesURL(copyID, path string) string {
	p := "/apps/whoami/copies/" + copyID + "/files"
	if path != "" {
		p += "?path=" + url.QueryEscape(path)
	}
	return p
}

func TestCopyFilesPage(t *testing.T) {
	e := filesEnv(t)

	t.Run("a folder: folders first, with links, sizes and downloads, and no secrets", func(t *testing.T) {
		resp, body := e.get(t, filesURL(actCopy1, "web/local"))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET = %d:\n%s", resp.StatusCode, text(body))
		}
		got := text(body)
		for _, want := range []string{"Files in whoami", "Service web, copy c1111111", "Back to the Running tab",
			"caravana-files/", "dump.bin", "10 B", "folder", "Download"} {
			if !strings.Contains(got, want) {
				t.Errorf("the page lacks %q: %s", want, got)
			}
		}
		if strings.Index(got, "caravana-files/") > strings.Index(got, "dump.bin") {
			t.Errorf("a folder comes after a file: %s", got)
		}
		for _, want := range []string{
			`href="/apps/whoami/copies/` + actCopy1 + `/files?path=web%2Flocal%2Fcaravana-files"`,
			`href="/apps/whoami/copies/` + actCopy1 + `/files/download?path=web%2Flocal%2Fdump.bin"`,
			`href="/apps/whoami/copies/` + actCopy1 + `/files?path=web%2Flocal%2Fdump.bin"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the page lacks %q:\n%s", want, body)
			}
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control = %q", cc)
		}
		for _, tag := range regexp.MustCompile(`<script[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(tag, " src=") {
				t.Errorf("an inline script: %s", tag)
			}
		}
		if strings.Contains(body, "files.js") {
			t.Error("the page loads a script of its own: it should need none")
		}
	})

	t.Run("the root, a task's folder without secrets, and the explanation", func(t *testing.T) {
		_, body := e.get(t, filesURL(actCopy1, ""))
		got := text(body)
		for _, want := range []string{"copy c1111111", "alloc/", "web/",
			"container's own files and its volumes aren't here", "Terminal", "secrets", "never shown", "audit log"} {
			if !strings.Contains(got, want) {
				t.Errorf("the root lacks %q: %s", want, got)
			}
		}
		_, body = e.get(t, filesURL(actCopy1, "web"))
		rows := body[strings.Index(body, "cv-filerows"):strings.Index(body, "</ul>")]
		if strings.Contains(rows, "secrets") || strings.Contains(rows, "private") || !strings.Contains(rows, "local/") {
			t.Errorf("the task's folder lists:\n%s", rows)
		}
	})

	t.Run("a text file: its text, escaped, a download and the crumbs", func(t *testing.T) {
		resp, body := e.get(t, filesURL(actCopy1, "web/local/caravana-files/0"))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET = %d:\n%s", resp.StatusCode, text(body))
		}
		if !strings.Contains(body, "listen 8080;") || !strings.Contains(body, "&lt;b&gt;not markup&lt;/b&gt;") || strings.Contains(body, "<b>not markup") {
			t.Errorf("the text is missing or not escaped:\n%s", body)
		}
		if !strings.Contains(body, `class="cv-output"`) || !strings.Contains(body, "/files/download?path=web%2Flocal%2Fcaravana-files%2F0") {
			t.Errorf("no output block or download link:\n%s", body)
		}
		got := text(body)
		for _, want := range []string{"copy c1111111", "web", "local", "caravana-files", "0", "Download"} {
			if !strings.Contains(got, want) {
				t.Errorf("the page lacks %q: %s", want, got)
			}
		}
	})

	t.Run("an empty file, a large file, a binary file", func(t *testing.T) {
		_, body := e.get(t, filesURL(actCopy1, "web/local/empty.txt"))
		if !strings.Contains(text(body), "The file is empty.") {
			t.Errorf("empty: %s", text(body))
		}
		_, body = e.get(t, filesURL(actCopy1, "alloc/data/big.log"))
		if !strings.Contains(text(body), "It is larger than 512 KB: download it to read it.") || strings.Contains(body, "xxxxxxxx") {
			t.Errorf("large: %s", text(body))
		}
		_, body = e.get(t, filesURL(actCopy1, "web/local/dump.bin"))
		if !strings.Contains(text(body), "It isn't text: download it.") || strings.Contains(body, "binary") {
			t.Errorf("binary: %s", text(body))
		}
	})

	t.Run("the Running tab links to it", func(t *testing.T) {
		_, body := e.get(t, "/apps/whoami")
		if !strings.Contains(body, `href="/apps/whoami/copies/`+actCopy1+`/files"`) || !strings.Contains(text(body), "Files") {
			t.Errorf("no Files link on the Running tab:\n%s", body)
		}
	})

	t.Run("it needs a session", func(t *testing.T) {
		r, _ := e.do(t, request{path: filesURL(actCopy1, "")})
		if r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, r), "/login") {
			t.Errorf("without a session: %d to %q", r.StatusCode, location(t, r))
		}
		r, _ = e.do(t, request{path: "/apps/whoami/copies/" + actCopy1 + "/files/download?path=web/local/dump.bin"})
		if r.StatusCode != http.StatusSeeOther {
			t.Errorf("download without a session: %d", r.StatusCode)
		}
	})
}

func TestCopyFileDownload(t *testing.T) {
	e := filesEnv(t)
	resp, body := e.get(t, "/apps/whoami/copies/"+actCopy1+"/files/download?path=web/local/dump.bin")
	if resp.StatusCode != http.StatusOK || body != "PK\x00\x01binary" {
		t.Fatalf("download = %d %q", resp.StatusCode, body)
	}
	for k, want := range map[string]string{
		"Content-Type": "application/octet-stream", "Content-Length": "10", "X-Content-Type-Options": "nosniff",
		"Cache-Control": "no-store", "Content-Disposition": "attachment; filename=dump.bin",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// A big file comes whole.
	resp, body = e.get(t, "/apps/whoami/copies/"+actCopy1+"/files/download?path=alloc/data/big.log")
	if resp.StatusCode != http.StatusOK || len(body) != 512<<10+1 {
		t.Errorf("big download = %d, %d bytes", resp.StatusCode, len(body))
	}
}

func TestCopyFilesRefusals(t *testing.T) {
	e := filesEnv(t)
	tests := []struct {
		name, path string
		status     int
		want       string
	}{
		{"an unknown app", "/apps/ghost/copies/x/files", 404, "There's no app called ghost."},
		{"a copy that isn't the app's", "/apps/whoami/copies/ffffffff-0000-4000-8000-000000000009/files", 404, "That copy isn't part of the app."},
		{"a missing path", filesURL(actCopy1, "nope"), 404, "There is nothing at that path."},
		{"a secrets folder", filesURL(actCopy1, "web/secrets"), 422, "which are never shown"},
		{"a secret file, through dot dot", filesURL(actCopy1, "web/local/../secrets/token"), 422, "That path can't be shown."},
		{"a secret file, downloaded", "/apps/whoami/copies/" + actCopy1 + "/files/download?path=web/private/key", 422, "which are never shown"},
		{"a folder downloaded", "/apps/whoami/copies/" + actCopy1 + "/files/download?path=web/local", 422, "is a folder, not a file"},
		{"a missing file downloaded", "/apps/whoami/copies/" + actCopy1 + "/files/download?path=web/local/nope", 404, "There is nothing at that path."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := e.get(t, tt.path)
			if resp.StatusCode != tt.status || !strings.Contains(text(body), tt.want) {
				t.Errorf("%d: %s, want %d with %q", resp.StatusCode, text(body), tt.status, tt.want)
			}
			// A refusal that knows the app and the copy is drawn in the files page, with the copy's root to
			// start again from; an unknown app or a copy that isn't the app's is the plain page.
			inPage := strings.Contains(tt.path, actCopy1)
			root := `href="/apps/whoami/copies/` + actCopy1 + `/files"`
			if got := strings.Contains(body, root) && strings.Contains(text(body), "Files in whoami"); got != inPage {
				t.Errorf("drawn in the files page = %v, want %v:\n%s", got, inPage, body)
			}
		})
	}

	t.Run("a copy that ended, a stopped app", func(t *testing.T) {
		e.Nomad.SetCopies("whoami",
			api.AllocationListStub{ID: actCopy1, TaskGroup: "web", ClientStatus: "complete", JobVersion: 0},
			api.AllocationListStub{ID: actCopy2, TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
		resp, body := e.get(t, filesURL(actCopy1, ""))
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "That copy isn't running.") {
			t.Errorf("ended copy = %d: %s", resp.StatusCode, text(body))
		}
		if resp, body := e.postAction(t, "/apps/whoami/stop"); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("stop = %d: %s", resp.StatusCode, text(body))
		}
		resp, body = e.get(t, filesURL(actCopy2, ""))
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "The app is stopped.") {
			t.Errorf("stopped app = %d: %s", resp.StatusCode, text(body))
		}
	})

	t.Run("Nomad refuses the token", func(t *testing.T) {
		e2 := filesEnv(t)
		e2.Nomad.FailFS(403)
		resp, body := e2.get(t, filesURL(actCopy1, ""))
		if resp.StatusCode != http.StatusBadGateway || !strings.Contains(text(body), "read-fs") {
			t.Errorf("403 = %d: %s", resp.StatusCode, text(body))
		}
	})
}

// TestCopyFilesNeverDrawSecrets: whichever way a task's secret file is asked for,
// the sentinel is on no page, no answer and not in the log.
func TestCopyFilesNeverDrawSecrets(t *testing.T) {
	e := filesEnv(t)
	var seen strings.Builder
	for _, p := range []string{"web/secrets", "web/secrets/token", "web/private", "web/private/key", "web/SECRETS/token",
		"web/local/../secrets/token", "names/secrets", "web", ""} {
		for _, kind := range []string{"", "/download"} {
			u := "/apps/whoami/copies/" + actCopy1 + "/files" + kind + "?path=" + url.QueryEscape(p)
			resp, body := e.get(t, u)
			seen.WriteString(resp.Status + " " + body + "\n")
		}
	}
	_, body := e.get(t, "/apps/whoami")
	seen.WriteString(body)
	if strings.Contains(seen.String(), filesSentinelUI) {
		t.Error("a page or answer holds the secret file's content")
	}
	if strings.Contains(e.Logs.String(), filesSentinelUI) {
		t.Error("the log holds the secret file's content")
	}
	for _, r := range e.Nomad.FileRequests() {
		if strings.Contains(r.Path, "secrets") || strings.Contains(strings.ToLower(r.Path), "private") {
			t.Errorf("Nomad was asked for %s %q", r.Op, r.Path)
		}
	}
}

// TestCopyFilesLinks: a link an app made into its secrets folder is listed as a
// link, with nothing to open or download, and opening it by its path is refused
// without the secret ever coming back.
func TestCopyFilesLinks(t *testing.T) {
	e := filesEnv(t)
	e.Nomad.WriteAllocLink(actCopy1, "web/local/peek", "../secrets/token")
	e.Nomad.WriteAllocLink(actCopy1, "web/local/sdir", "../secrets")

	resp, body := e.get(t, filesURL(actCopy1, "web/local"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d:\n%s", resp.StatusCode, text(body))
	}
	if got := text(body); !strings.Contains(got, "peek") || !strings.Contains(got, "link, not followed") {
		t.Errorf("the link isn't listed as one: %s", got)
	}
	if strings.Contains(body, "path=web%2Flocal%2Fpeek") || strings.Contains(body, "path=web%2Flocal%2Fsdir") {
		t.Errorf("the page links to a link:\n%s", body)
	}
	for _, p := range []string{"web/local/peek", "web/local/sdir", "web/local/sdir/token"} {
		for _, u := range []string{filesURL(actCopy1, p), "/apps/whoami/copies/" + actCopy1 + "/files/download?path=" + url.QueryEscape(p)} {
			resp, body := e.get(t, u)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Errorf("GET %s = %d, want 422", u, resp.StatusCode)
			}
			if strings.Contains(body, filesSentinelUI) {
				t.Errorf("GET %s showed the secret", u)
			}
		}
	}
}

// TestCopyFilesAuditDoor: a file shown or downloaded in the console is noted with the console's door,
// the client's address and the route; a listing leaves nothing.
func TestCopyFilesAuditDoor(t *testing.T) {
	e := filesEnv(t)
	e.get(t, filesURL(actCopy1, "web/local"))
	e.get(t, filesURL(actCopy1, "web/local/caravana-files/0"))
	e.get(t, "/apps/whoami/copies/"+actCopy1+"/files/download?path=web/local/dump.bin")
	var got []string
	for _, ev := range e.auditEntries(t) {
		if ev.Action != audit.ActionReadFile {
			continue
		}
		as := ""
		for _, c := range ev.Changes {
			if c.Path == "as" {
				as = c.To
			}
		}
		got = append(got, as)
		if ev.Door != audit.DoorConsole || ev.Address == "" || !strings.HasPrefix(ev.Operation, "GET /apps/{id}/copies/{copyId}/files") {
			t.Errorf("entry = door %q, address %q, operation %q", ev.Door, ev.Address, ev.Operation)
		}
	}
	if strings.Join(got, ",") != "view,download" && strings.Join(got, ",") != "download,view" {
		t.Errorf("read entries = %v, want one view and one download", got)
	}
}

// TestAppTitleNamesThePlace: the files and terminal pages name an app by its
// place label: its name alone while Default › production is the only place, and
// with its project and environment once there are more.
func TestAppTitleNamesThePlace(t *testing.T) {
	e := filesEnv(t)
	ctx := t.Context()
	words := func(sn sentence) string {
		var b strings.Builder
		for _, w := range sn {
			b.WriteString(w.Text)
		}
		return b.String()
	}
	if got := words(e.Server.appTitle(ctx, "whoami", app.Place{Project: "default", Environment: "production", Name: "whoami"})); got != "whoami" {
		t.Errorf("a home app = %q, want whoami", got)
	}
	if _, err := e.Svc.CreateProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	got := words(e.Server.appTitle(ctx, "shop--staging--web", app.Place{Project: "shop", Environment: "staging", Name: "web"}))
	if got != "Shop › staging › web" {
		t.Errorf("an app in shop › staging = %q", got)
	}
}
