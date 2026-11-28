package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/nomad/api"
)

const copyFilesSentinel = "s3cret-sentinel-2g4z"

// US: listCopyFiles and downloadCopyFile answer what the copy's directory holds,
// refuse the paths they must, and never show a task's secret files.
func TestCopyFiles(t *testing.T) {
	e := deployedWithCopies(t)
	doc := loadContract(t)
	e.Nomad.WriteAllocFile("c1", "web/local/caravana-files/0", "listen 8080;\n")
	e.Nomad.WriteAllocFile("c1", "web/local/dump.bin", "PK\x00\x01binary")
	e.Nomad.WriteAllocFile("c1", "web/local/my report \"v2\".txt", "report")
	e.Nomad.WriteAllocFile("c1", "web/tmp/empty", "")
	e.Nomad.WriteAllocFile("c1", "web/secrets/token", copyFilesSentinel)
	e.Nomad.WriteAllocFile("c1", "web/private/key", copyFilesSentinel)
	base := e.URL + "/v1/apps/whoami/copies/c1/files"
	get := func(url string) (int, http.Header, []byte) { return call(t, http.MethodGet, url, "", "") }

	t.Run("a folder: folders first, secrets and private left out, no-store", func(t *testing.T) {
		status, header, raw := get(base + "?path=web")
		if status != http.StatusOK {
			t.Fatalf("list = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "CopyFolder")
		if cc := header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control = %q", cc)
		}
		got := decodeAs[struct {
			Path    string `json:"path"`
			Service string `json:"service"`
			Entries []struct {
				Name, Path, Kind string
			} `json:"entries"`
		}](t, raw)
		var names []string
		for _, en := range got.Entries {
			names = append(names, en.Name+":"+en.Kind)
		}
		if got.Path != "web" || got.Service != "web" || strings.Join(names, " ") != "local:folder tmp:folder" {
			t.Errorf("answer = %s", raw)
		}
	})

	t.Run("the root, and a folder with files", func(t *testing.T) {
		_, _, raw := get(base)
		if !strings.Contains(string(raw), `"name":"alloc"`) || !strings.Contains(string(raw), `"path":""`) {
			t.Errorf("root = %s", raw)
		}
		_, _, raw = get(base + "?path=/web/local/caravana-files/")
		checkSchema(t, doc, raw, "CopyFolder")
		got := decodeAs[struct {
			Entries []struct {
				Name, Path, Kind string
				Size             int64
			} `json:"entries"`
		}](t, raw)
		if len(got.Entries) != 1 || got.Entries[0].Path != "web/local/caravana-files/0" || got.Entries[0].Kind != "file" || got.Entries[0].Size != 13 {
			t.Errorf("answer = %s", raw)
		}
	})

	t.Run("a download: the bytes and the headers", func(t *testing.T) {
		status, header, raw := get(base + "/download?path=web/local/dump.bin")
		if status != http.StatusOK || string(raw) != "PK\x00\x01binary" {
			t.Fatalf("download = %d %q", status, raw)
		}
		for k, want := range map[string]string{
			"Content-Type": "application/octet-stream", "Content-Length": "10", "X-Content-Type-Options": "nosniff",
			"Cache-Control": "no-store", "Content-Disposition": "attachment; filename=dump.bin",
		} {
			if header.Get(k) != want {
				t.Errorf("%s = %q, want %q", k, header.Get(k), want)
			}
		}
		// A name with a space and a quote is quoted, not sent raw.
		_, header, _ = get(base + "/download?path=" + "web/local/my%20report%20%22v2%22.txt")
		if cd := header.Get("Content-Disposition"); cd != `attachment; filename="my report \"v2\".txt"` {
			t.Errorf("Content-Disposition = %q", cd)
		}
		// An empty file is a download of nothing.
		status, header, raw = get(base + "/download?path=web/tmp/empty")
		if status != http.StatusOK || len(raw) != 0 || header.Get("Content-Length") != "0" {
			t.Errorf("empty = %d %q %v", status, raw, header)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		tests := []struct {
			name, url   string
			status      int
			code, field string
		}{
			{"a secrets folder", base + "?path=web/secrets", 422, "invalid_field", "path"},
			{"a secret file", base + "/download?path=web/secrets/token", 422, "invalid_field", "path"},
			{"a private file", base + "/download?path=web/private/key", 422, "invalid_field", "path"},
			{"dot dot into secrets", base + "/download?path=web/local/../secrets/token", 422, "invalid_field", "path"},
			{"dot dot", base + "?path=..", 422, "invalid_field", "path"},
			{"a backslash", base + "?path=web%5Clocal", 422, "invalid_field", "path"},
			{"a NUL byte", base + "?path=web%00", 422, "invalid_field", "path"},
			{"a file listed", base + "?path=web/local/dump.bin", 422, "invalid_field", "path"},
			{"a folder downloaded", base + "/download?path=web/local", 422, "invalid_field", "path"},
			{"the root downloaded", base + "/download", 422, "invalid_field", "path"},
			{"a missing folder", base + "?path=nope", 404, "file_not_found", "path"},
			{"a missing file", base + "/download?path=web/local/nope", 404, "file_not_found", "path"},
			{"not the app's copy", e.URL + "/v1/apps/whoami/copies/other/files", 404, "not_found", ""},
			{"an unknown app", e.URL + "/v1/apps/ghost/copies/c1/files", 404, "not_found", ""},
		}
		for _, tt := range tests {
			status, header, raw := get(tt.url)
			wantError(t, tt.name, status, raw, tt.status, tt.code, tt.field)
			checkSchema(t, doc, raw, "Error")
			if header.Get("Cache-Control") != "no-store" {
				t.Errorf("%s: Cache-Control = %q", tt.name, header.Get("Cache-Control"))
			}
		}
	})

	t.Run("an ended copy, a stopped app, a token without read-fs", func(t *testing.T) {
		e.Nomad.FailFS(403)
		status, _, raw := get(base + "?path=web")
		e.Nomad.FailFS(0)
		if got := decodeAs[errorBody](t, raw); status != http.StatusBadGateway || got.Error.Code != "nomad_unavailable" || !strings.Contains(got.Error.Message, "read-fs") {
			t.Errorf("403 from Nomad = %d %s", status, raw)
		}
		e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "complete", JobVersion: 0})
		status, _, raw = get(base)
		wantError(t, "an ended copy", status, raw, http.StatusConflict, "copy_not_running", "")
		e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: "c1", TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
		if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/apps/whoami/stop", "", ""); status != http.StatusOK {
			t.Fatalf("stop = %d %s", status, raw)
		}
		status, _, raw = get(base)
		wantError(t, "a stopped app", status, raw, http.StatusConflict, "app_stopped", "")
	})

	t.Run("the secret is in no answer and no log line", func(t *testing.T) {
		var seen []string
		for _, u := range []string{base + "?path=web/secrets", base + "/download?path=web/secrets/token",
			base + "/download?path=web%2Fx%2F..%2Fsecrets%2Ftoken", base + "?path=web", e.URL + "/v1/audit"} {
			_, _, raw := get(u)
			seen = append(seen, u+" "+string(raw))
		}
		seen = append(seen, "the log: "+e.Logs.String())
		for _, s := range seen {
			if strings.Contains(s, copyFilesSentinel) {
				head, _, _ := strings.Cut(s, " ")
				t.Errorf("%s holds the secret file's content", head)
			}
		}
	})
}

// The audit log has one entry for a download, with the copy and the path and
// never the content, and none for a listing.
func TestCopyFilesAreAudited(t *testing.T) {
	e := deployedWithCopies(t)
	e.Nomad.WriteAllocFile("c1", "web/local/notes.txt", "a private thought "+copyFilesSentinel)
	base := e.URL + "/v1/apps/whoami/copies/c1/files"
	call(t, http.MethodGet, base+"?path=web/local", "", "")
	call(t, http.MethodGet, base+"/download?path=web/local/notes.txt", "", "")

	_, _, raw := call(t, http.MethodGet, e.URL+"/v1/audit?action=read%20a%20file", "", "")
	got := decodeAs[struct {
		Events []struct {
			Action   string `json:"action"`
			Resource struct{ ID string }
			Changes  []struct{ Path, To string }
		} `json:"events"`
	}](t, raw)
	if len(got.Events) != 1 || got.Events[0].Action != "read a file" || got.Events[0].Resource.ID != "whoami" {
		t.Fatalf("audit = %s", raw)
	}
	to := map[string]string{}
	for _, c := range got.Events[0].Changes {
		to[c.Path] = c.To
	}
	if to["copy"] != "c1" || to["path"] != "web/local/notes.txt" || to["as"] != "download" {
		t.Errorf("changes = %v", to)
	}
	if strings.Contains(string(raw), copyFilesSentinel) {
		t.Error("the audit log holds the file's content")
	}
}
