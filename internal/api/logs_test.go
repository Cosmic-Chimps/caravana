package api

import (
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 024: listLogs, getLogsAround and downloadLogs, and getLogs' new
// fields, over the fake Nomad's file system. Every body is checked against the
// contract.

const (
	logsCopyID    = "9a4d13ff-0000-4000-8000-000000000001"
	logsCopyShort = "9a4d13ff"
	logsSentinel  = "s3cret-sentinel-024-api"
)

// logsFixture is app whoami with one running copy that wrote to both streams,
// at times just before now (the service's clock is the real one).
type logsFixture struct {
	e   *testEnv
	now time.Time
}

func (f logsFixture) at(ago time.Duration) time.Time { return f.now.Add(-ago) }

func (f logsFixture) stamp(ago time.Duration) string { return f.at(ago).Format(time.RFC3339) }

func newLogsFixture(t *testing.T) logsFixture {
	t.Helper()
	e := newTestEnv(t, "")
	seedHealthy(t, e, "whoami", 1, 7)
	f := logsFixture{e: e, now: time.Now().UTC().Truncate(time.Second)}
	e.Nomad.SetCopies("whoami", api.AllocationListStub{ID: logsCopyID, TaskGroup: "web", JobVersion: 7, ClientStatus: "running",
		TaskStates: map[string]*api.TaskState{"web": {StartedAt: f.now.Add(-time.Hour)}}})
	e.Nomad.AddLogFile(logsCopyID, "stdout", 0, []string{
		f.stamp(9*time.Minute) + " ERROR first failure " + logsSentinel,
		fmt.Sprintf(`{"level":"error","ts":%d,"msg":"dial tcp: refused"}`, f.at(8*time.Minute).Unix()),
		"    at com.example.Foo.bar(Foo.java:42)",
		f.stamp(7*time.Minute) + " GET /health 200",
		f.stamp(6*time.Minute) + " GET /health 200",
		f.stamp(5*time.Minute) + " GET /health 200",
		"\x1b[31m" + f.stamp(4*time.Minute) + " ERR\x1b[0m the ansi line",
		f.stamp(3*time.Minute) + " DEBUG tracing",
	}, f.at(3*time.Minute-5*time.Second)) // a file's last write is within a minute of its last line, or its times aren't believed
	e.Nomad.AddLogFile(logsCopyID, "stderr", 0, []string{
		f.stamp(2*time.Minute) + " warning: low on memory",
	}, f.at(2*time.Minute-5*time.Second))
	return f
}

// get calls path and returns the status, headers and body.
func (f logsFixture) get(t *testing.T, path string) (int, http.Header, []byte) {
	t.Helper()
	return call(t, http.MethodGet, f.e.URL+path, "", "")
}

// readOK gets path, expects a 200 LogRead and decodes it.
func (f logsFixture) readOK(t *testing.T, path string) logReadBody {
	t.Helper()
	status, header, raw := f.get(t, path)
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, status, raw)
	}
	if header.Get("Content-Type") != "application/json" || header.Get("Cache-Control") != "no-store" {
		t.Errorf("GET %s: Content-Type %q, Cache-Control %q, want application/json and no-store",
			path, header.Get("Content-Type"), header.Get("Cache-Control"))
	}
	checkSchema(t, loadContract(t), raw, "LogRead")
	return decodeAs[logReadBody](t, raw)
}

// logReadBody is a LogRead, decoded; Histogram is nil when the answer has none.
type logReadBody struct {
	Lines []struct {
		ID         string `json:"id"`
		Time       string `json:"time"`
		TimeSource string `json:"timeSource"`
		App        string `json:"app"`
		Service    string `json:"service"`
		Copy       string `json:"copy"`
		Stream     string `json:"stream"`
		Level      string `json:"level"`
		Text       string `json:"text"`
		Raw        string `json:"raw"`
		Repeat     int    `json:"repeat"`
	} `json:"lines"`
	Deploys []struct {
		DeploymentID string `json:"deploymentId"`
		App          string `json:"app"`
		At           string `json:"at"`
		Kind         string `json:"kind"`
		JobVersion   int    `json:"jobVersion"`
		Who          string `json:"who"`
	} `json:"deploys"`
	Histogram *struct {
		From          string `json:"from"`
		To            string `json:"to"`
		BucketSeconds int    `json:"bucketSeconds"`
		Buckets       []struct {
			From      string `json:"from"`
			Count     int    `json:"count"`
			Errors    int    `json:"errors"`
			Estimated int    `json:"estimated"`
			Read      bool   `json:"read"`
		} `json:"buckets"`
	} `json:"histogram"`
	Coverage struct {
		From     string `json:"from"`
		Complete bool   `json:"complete"`
	} `json:"coverage"`
	Copies []struct {
		ID      string `json:"id"`
		Short   string `json:"short"`
		App     string `json:"app"`
		Service string `json:"service"`
		Gone    bool   `json:"gone"`
		Lines   int    `json:"lines"`
	} `json:"copies"`
	Dropped bool   `json:"dropped"`
	Anchor  string `json:"anchor"`
}

func (r logReadBody) texts() []string {
	out := make([]string, len(r.Lines))
	for i, l := range r.Lines {
		out[i] = l.Text
	}
	return out
}

func TestListLogs(t *testing.T) {
	f := newLogsFixture(t)

	t.Run("the default read: shape, order, levels, folding and stripping", func(t *testing.T) {
		got := f.readOK(t, "/v1/logs")
		// Newest first; DEBUG is not in the default levels.
		want := []string{
			f.stamp(2*time.Minute) + " warning: low on memory",
			f.stamp(4*time.Minute) + " ERR the ansi line",
			f.stamp(5*time.Minute) + " GET /health 200", // three folded into one row
			"    at com.example.Foo.bar(Foo.java:42)",
			"dial tcp: refused",
			f.stamp(9*time.Minute) + " ERROR first failure " + logsSentinel,
		}
		if g := got.texts(); strings.Join(g, "\n") != strings.Join(want, "\n") {
			t.Fatalf("texts =\n%s\nwant\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
		}

		// One line, field by field.
		l := got.Lines[1]
		if l.App != "whoami" || l.Service != "web" || l.Copy != logsCopyShort || l.Stream != "stdout" || l.Level != "error" ||
			l.TimeSource != "line" || l.Time != f.at(4*time.Minute).Format(time.RFC3339) || l.Raw != "" || l.Repeat != 0 {
			t.Errorf("the ansi line = %+v", l)
		}
		if !strings.HasPrefix(l.ID, logsCopyShort+".stdout.0.") {
			t.Errorf("id = %q, want %s.stdout.0.<offset>", l.ID, logsCopyShort)
		}
		if strings.ContainsRune(l.Text, 0x1b) {
			t.Errorf("text %q still has a control sequence", l.Text)
		}

		// A JSON line: the message, the raw object, its level.
		j := got.Lines[4]
		if j.Level != "error" || !strings.HasPrefix(j.Raw, `{"level":"error"`) || j.Time != f.at(8*time.Minute).Format(time.RFC3339) {
			t.Errorf("json line = %+v", j)
		}
		// A continuation line: the level above it, an estimated time.
		c := got.Lines[3]
		if c.Level != "error" || c.TimeSource != "estimated" {
			t.Errorf("continuation line = %+v, want error and estimated", c)
		}
		// Three health checks fold into one row of repeat 2, at the newest's time.
		if r := got.Lines[2]; r.Repeat != 2 || r.Level != "info" {
			t.Errorf("folded row = %+v, want repeat 2 and info", r)
		}
		if s := got.Lines[0]; s.Stream != "stderr" || s.Level != "warn" {
			t.Errorf("stderr line = %+v, want a warn on stderr", s)
		}
	})

	t.Run("histogram, coverage, copies", func(t *testing.T) {
		got := f.readOK(t, "/v1/logs?levels=error,warn,info,debug")
		h := got.Histogram
		if h == nil || len(h.Buckets) != 30 || h.BucketSeconds != 30 {
			t.Fatalf("histogram = %+v, want 30 buckets of 30 s", h)
		}
		total, errors := 0, 0
		for _, b := range h.Buckets {
			total += b.Count
			errors += b.Errors
			if !b.Read {
				t.Errorf("bucket %s not read, want a complete read", b.From)
			}
		}
		// 9 lines in the files, none folded in the histogram: 4 errors (the
		// text one, the JSON one, its continuation, the ANSI one).
		if total != 9 || errors != 4 {
			t.Errorf("histogram counts %d lines and %d errors, want 9 and 4", total, errors)
		}
		if !got.Coverage.Complete || got.Coverage.From == "" {
			t.Errorf("coverage = %+v, want complete", got.Coverage)
		}
		if got.Dropped {
			t.Error("dropped = true, want false")
		}
		if len(got.Copies) != 1 {
			t.Fatalf("copies = %+v", got.Copies)
		}
		c := got.Copies[0]
		if c.ID != logsCopyID || c.Short != logsCopyShort || c.App != "whoami" || c.Service != "web" || c.Gone || c.Lines != 9 {
			t.Errorf("copy = %+v, want %s whoami/web, 9 lines, not gone", c, logsCopyShort)
		}
	})

	t.Run("deploys of the range", func(t *testing.T) {
		got := f.readOK(t, "/v1/logs")
		kinds := map[string]bool{}
		for _, d := range got.Deploys {
			kinds[d.Kind] = true
			if d.DeploymentID != "whoami-d1" || d.App != "whoami" || d.JobVersion != 7 || d.Who != "through the API" {
				t.Errorf("deploy = %+v", d)
			}
		}
		if !kinds["started"] || !kinds["live"] || len(got.Deploys) != 2 {
			t.Errorf("deploys = %+v, want a started row and a live one", got.Deploys)
		}
		// Not narrowed by level or text.
		got = f.readOK(t, "/v1/logs?levels=debug&q=nothing-matches-this")
		if len(got.Lines) != 0 || len(got.Deploys) != 2 {
			t.Errorf("lines %v deploys %d, want no lines and both deploys", got.texts(), len(got.Deploys))
		}
		// Another app's: none.
		seedHealthy(t, f.e, "other", 1, 1)
		got = f.readOK(t, "/v1/logs?app=whoami")
		for _, d := range got.Deploys {
			if d.App != "whoami" {
				t.Errorf("app=whoami answered a deploy of %s", d.App)
			}
		}
		got = f.readOK(t, "/v1/logs?service=whoami/web")
		for _, d := range got.Deploys {
			if d.App != "whoami" {
				t.Errorf("service=whoami/web answered a deploy of %s", d.App)
			}
		}
	})

	t.Run("filters", func(t *testing.T) {
		tests := []struct {
			query string
			want  int
		}{
			{"?levels=error", 4},
			{"?levels=debug", 1},
			{"?levels=ERROR,Warn", 5},
			{"?stream=stderr", 1},
			{"?stream=stdout&levels=error", 4},
			{"?q=HEALTH&levels=info", 1},
			{"?q=whoami/web&levels=debug", 1},
			{"?app=whoami&service=whoami/web", 6},
			{"?copy=" + logsCopyShort, 6},
			{"?copy=" + logsCopyID, 6},
			{"?lines=2", 2},
			{"?range=1h", 6},
			{"?project=default&environment=production", 6},
			{"?from=" + url.QueryEscape(f.at(5*time.Minute+30*time.Second).Format(time.RFC3339)), 3},
			{"?before=" + url.QueryEscape(f.at(6*time.Minute+30*time.Second).Format(time.RFC3339)), 4},
		}
		for _, tc := range tests {
			if got := f.readOK(t, "/v1/logs"+tc.query); len(got.Lines) != tc.want {
				t.Errorf("logs%s = %d rows %q, want %d", tc.query, len(got.Lines), got.texts(), tc.want)
			}
		}
	})

	t.Run("no output is an empty read, not null", func(t *testing.T) {
		status, _, raw := f.get(t, "/v1/logs?levels=debug&q=zzz")
		if status != http.StatusOK || strings.Contains(string(raw), "null") {
			t.Errorf("status %d body %s, want 200 and no null", status, raw)
		}
	})
}

func TestListLogsRefusals(t *testing.T) {
	f := newLogsFixture(t)
	now := f.now.Format(time.RFC3339)
	earlier := f.at(time.Hour).Format(time.RFC3339)
	tests := []struct {
		name, query string
		status      int
		code, field string
	}{
		{"unknown level", "?levels=loud", 422, codeInvalidField, "levels"},
		{"empty level in the list", "?levels=error,", 422, codeInvalidField, "levels"},
		{"q of 201 bytes", "?q=" + strings.Repeat("a", 201), 422, codeInvalidField, "q"},
		{"range not a preset", "?range=2h", 422, codeInvalidField, "range"},
		{"range in days not a preset", "?range=1d", 422, codeInvalidField, "range"},
		{"range with from", "?range=1h&from=" + url.QueryEscape(earlier), 422, codeInvalidField, "range"},
		{"range with to", "?range=1h&to=" + url.QueryEscape(now), 422, codeInvalidField, "range"},
		{"from after to", "?from=" + url.QueryEscape(now) + "&to=" + url.QueryEscape(earlier), 422, codeInvalidField, "from"},
		{"from not a time", "?from=yesterday", 422, codeInvalidField, "from"},
		{"to not a time", "?to=2026-10-25", 422, codeInvalidField, "to"},
		{"before not a time", "?before=soon", 422, codeInvalidField, "before"},
		{"lines zero", "?lines=0", 422, codeInvalidField, "lines"},
		{"lines too many", "?lines=1001", 422, codeInvalidField, "lines"},
		{"lines not a number", "?lines=many", 422, codeInvalidField, "lines"},
		{"lines empty", "?lines=", 422, codeInvalidField, "lines"},
		{"stream unknown", "?stream=both,stdout", 422, codeInvalidField, "stream"},
		{"service without its app", "?service=web", 422, codeInvalidField, "service"},
		{"service of no app", "?service=ghost/web", 422, codeInvalidField, "service"},
		{"service that isn't one", "?service=whoami/nope", 422, codeInvalidField, "service"},
		{"service of another app than app", "?app=whoami&service=other/web", 422, codeInvalidField, "service"},
		{"project not found", "?project=nope", 404, codeProjectNotFound, "project"},
		{"environment not found", "?project=default&environment=nope", 404, codeEnvironmentNotFound, "environment"},
		{"app not found", "?app=ghost", 404, codeAppNotFound, "app"},
		{"copy not found", "?copy=deadbeef", 404, codeNotFound, "copy"},
	}
	for _, path := range []string{"/v1/logs", "/v1/logs/download"} {
		for _, tc := range tests {
			t.Run(path+" "+tc.name, func(t *testing.T) {
				status, header, raw := f.get(t, path+tc.query)
				wantError(t, tc.query, status, raw, tc.status, tc.code, tc.field)
				if header.Get("Cache-Control") != "no-store" {
					t.Errorf("Cache-Control = %q on an error, want no-store", header.Get("Cache-Control"))
				}
				if strings.Contains(string(raw), logsSentinel) {
					t.Errorf("the error answer holds the app's text: %s", raw)
				}
			})
		}
	}

	t.Run("before the start of the range is 422", func(t *testing.T) {
		status, _, raw := f.get(t, "/v1/logs?before="+url.QueryEscape(f.at(time.Hour).Format(time.RFC3339)))
		wantError(t, "before", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "before")
	})
	t.Run("Nomad down is 502", func(t *testing.T) {
		f.e.Nomad.Down()
		defer f.e.Nomad.Up()
		status, _, raw := f.get(t, "/v1/logs")
		wantError(t, "Nomad down", status, raw, http.StatusBadGateway, codeNomadUnavailable, "")
	})

	if strings.Contains(f.e.Logs.String(), logsSentinel) {
		t.Errorf("the server log holds the app's text")
	}
}

func TestGetLogsAround(t *testing.T) {
	f := newLogsFixture(t)
	list := f.readOK(t, "/v1/logs?levels=error,warn,info,debug")
	var anchorID string
	for _, l := range list.Lines {
		if strings.Contains(l.Text, "GET /health") {
			anchorID = l.ID // the newest of the three, at 5 minutes ago... the row carries its group's id
		}
	}
	if anchorID == "" {
		t.Fatalf("no health line in %q", list.texts())
	}

	t.Run("by id, every level, both streams, with the anchor and no histogram", func(t *testing.T) {
		got := f.readOK(t, "/v1/logs/around?copy="+logsCopyShort+"&at="+anchorID+"&lines=3")
		if got.Anchor != anchorID {
			t.Errorf("anchor = %q, want %q", got.Anchor, anchorID)
		}
		if got.Histogram != nil {
			t.Errorf("histogram = %+v, want none", got.Histogram)
		}
		if len(got.Copies) != 1 || got.Copies[0].Short != logsCopyShort || got.Copies[0].App != "whoami" || !got.Coverage.Complete {
			t.Errorf("copies %+v coverage %+v", got.Copies, got.Coverage)
		}
		hasAnchor, levels := false, map[string]bool{}
		for _, l := range got.Lines {
			hasAnchor = hasAnchor || l.ID == anchorID
			levels[l.Level] = true
			if l.Repeat != 0 {
				t.Errorf("line %q has repeat %d, want none folded", l.Text, l.Repeat)
			}
		}
		if !hasAnchor || len(got.Lines) < 3 || len(got.Lines) > 7+1 {
			t.Errorf("lines = %q, want the anchor among up to 7 around it", got.texts())
		}
		if !levels["debug"] && !levels["info"] {
			t.Errorf("levels %v, want every level", levels)
		}
	})
	t.Run("by time and stream", func(t *testing.T) {
		got := f.readOK(t, "/v1/logs/around?copy="+logsCopyShort+"&around="+url.QueryEscape(f.at(3*time.Minute).Format(time.RFC3339))+"&stream=stdout&lines=1")
		if !strings.HasPrefix(got.Anchor, logsCopyShort+".stdout.0.") || len(got.Lines) == 0 {
			t.Errorf("anchor %q lines %q", got.Anchor, got.texts())
		}
	})
	t.Run("a copy that is gone is 404 not_found, a purged file 404 log_rotated", func(t *testing.T) {
		status, _, raw := f.get(t, "/v1/logs/around?copy=deadbeef&at=deadbeef.stdout.0.0")
		wantError(t, "unknown copy", status, raw, http.StatusNotFound, codeNotFound, "copy")

		f.e.Nomad.AddLogFile(logsCopyID, "stdout", 1, []string{f.stamp(time.Minute) + " newer file"}, f.at(time.Minute-5*time.Second))
		f.e.Nomad.PurgeLogFile(logsCopyID, "stdout", 0)
		status, header, raw := f.get(t, "/v1/logs/around?copy="+logsCopyShort+"&at="+anchorID)
		wantError(t, "purged file", status, raw, http.StatusNotFound, codeLogRotated, "")
		if header.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q on log_rotated", header.Get("Cache-Control"))
		}
		if strings.Contains(string(raw), logsSentinel) {
			t.Errorf("the answer holds the app's text: %s", raw)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		g := newLogsFixture(t)
		tests := []struct {
			name, query string
			status      int
			code, field string
		}{
			{"no copy", "?at=" + anchorID, 422, codeInvalidField, "copy"},
			{"neither at nor around", "?copy=" + logsCopyShort, 422, codeInvalidField, "at"},
			{"both at and around", "?copy=" + logsCopyShort + "&at=" + anchorID + "&around=" + url.QueryEscape(g.now.Format(time.RFC3339)), 422, codeInvalidField, "at"},
			{"a bad at", "?copy=" + logsCopyShort + "&at=nonsense", 422, codeInvalidField, "at"},
			{"a bad stream in at", "?copy=" + logsCopyShort + "&at=9a4d13ff.stdlog.0.0", 422, codeInvalidField, "at"},
			{"a bad around", "?copy=" + logsCopyShort + "&around=noon", 422, codeInvalidField, "around"},
			{"a bad stream", "?copy=" + logsCopyShort + "&around=" + url.QueryEscape(g.now.Format(time.RFC3339)) + "&stream=both", 422, codeInvalidField, "stream"},
			{"lines zero", "?copy=" + logsCopyShort + "&at=" + anchorID + "&lines=0", 422, codeInvalidField, "lines"},
			{"lines over 200", "?copy=" + logsCopyShort + "&at=" + anchorID + "&lines=201", 422, codeInvalidField, "lines"},
			{"an offset past the end", "?copy=" + logsCopyShort + "&at=" + logsCopyShort + ".stdout.0.99999999", 422, codeInvalidField, "at"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				status, header, raw := g.get(t, "/v1/logs/around"+tc.query)
				wantError(t, tc.query, status, raw, tc.status, tc.code, tc.field)
				if header.Get("Cache-Control") != "no-store" || strings.Contains(string(raw), logsSentinel) {
					t.Errorf("Cache-Control %q, body %s", header.Get("Cache-Control"), raw)
				}
			})
		}
	})
}

func TestDownloadLogs(t *testing.T) {
	f := newLogsFixture(t)

	t.Run("headers and the first lines", func(t *testing.T) {
		status, header, raw := f.get(t, "/v1/logs/download?levels=error,warn,info,debug")
		if status != http.StatusOK {
			t.Fatalf("download = %d %s", status, raw)
		}
		if ct := header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("Content-Type = %q", ct)
		}
		if cc := header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", cc)
		}
		disp, params, err := mime.ParseMediaType(header.Get("Content-Disposition"))
		if err != nil || disp != "attachment" || !strings.HasPrefix(params["filename"], "caravana-logs-default-production-") ||
			!strings.HasSuffix(params["filename"], "Z.txt") || strings.Contains(params["filename"], ":") {
			t.Errorf("Content-Disposition = %q (%v)", header.Get("Content-Disposition"), err)
		}
		lines := strings.Split(string(raw), "\n")
		if lines[0] != "# newest last" || !strings.HasPrefix(lines[1], "# caravana logs default/production ") {
			t.Errorf("first lines = %q, %q", lines[0], lines[1])
		}
		// Oldest first: the first row is the error 9 minutes ago.
		var rows []string
		for _, l := range lines {
			if l != "" && !strings.HasPrefix(l, "#") {
				rows = append(rows, l)
			}
		}
		if len(rows) != 9 {
			t.Fatalf("%d rows %q, want 9 (folded rows written as many times as they stand for)", len(rows), rows)
		}
		fields := strings.Split(rows[0], "\t")
		if len(fields) != 6 || fields[1] != "whoami/web" || fields[2] != logsCopyShort || fields[3] != "stdout" || fields[4] != "error" ||
			fields[5] != f.stamp(9*time.Minute)+" ERROR first failure "+logsSentinel {
			t.Errorf("first row = %q", rows[0])
		}
		if !strings.Contains(string(raw), "# deploy ") {
			t.Errorf("no deploy line in the file:\n%s", raw)
		}
	})
	t.Run("a range and a place name the file", func(t *testing.T) {
		from, to := f.at(time.Hour), f.at(time.Minute)
		_, header, _ := f.get(t, "/v1/logs/download?from="+url.QueryEscape(from.Format(time.RFC3339))+"&to="+url.QueryEscape(to.Format(time.RFC3339)))
		_, params, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
		want := "caravana-logs-default-production-" + from.Format("20060102T150405Z") + "-" + to.Format("20060102T150405Z") + ".txt"
		if params["filename"] != want {
			t.Errorf("filename = %q, want %q", params["filename"], want)
		}
	})
	t.Run("an empty download still has its headers", func(t *testing.T) {
		status, header, raw := f.get(t, "/v1/logs/download?q=zzz")
		if status != http.StatusOK || header.Get("Content-Disposition") == "" || !strings.HasPrefix(string(raw), "# newest last") {
			t.Errorf("status %d header %v body %s", status, header, raw)
		}
	})
	t.Run("a second download while one runs is 409 download_busy", func(t *testing.T) {
		g := newLogsFixture(t)
		wrote, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			done <- g.e.Service.DownloadLogs(t.Context(), service.EnvLogQuery{}, &blockingWriter{wrote: wrote, release: release})
		}()
		<-wrote // the first download has its streams read and is writing
		status, header, raw := g.get(t, "/v1/logs/download")
		wantError(t, "second download", status, raw, http.StatusConflict, codeDownloadBusy, "")
		if header.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q on download_busy", header.Get("Cache-Control"))
		}
		close(release)
		if err := <-done; err != nil {
			t.Errorf("the first download: %v", err)
		}
		if status, _, raw := g.get(t, "/v1/logs/download"); status != http.StatusOK {
			t.Errorf("a download after it ended = %d %s", status, raw)
		}
	})
}

// blockingWriter signals its first write and then waits to be released.
type blockingWriter struct {
	wrote, release chan struct{}
	once           bool
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	if !b.once {
		b.once = true
		close(b.wrote)
		<-b.release
	}
	return len(p), nil
}

// TestGetLogsNewFields: getLogs (per app) answers each line's id, level and
// timeSource, its text stripped, in the same order as before.
func TestGetLogsNewFields(t *testing.T) {
	f := newLogsFixture(t)
	status, _, raw := f.get(t, "/v1/apps/whoami/logs?stream=stdout")
	if status != http.StatusOK {
		t.Fatalf("getLogs = %d %s", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "LogLines")
	lines := decodeAs[struct {
		Lines []struct {
			Copy       string  `json:"copy"`
			Text       string  `json:"text"`
			Time       *string `json:"time"`
			ID         string  `json:"id"`
			Level      string  `json:"level"`
			TimeSource string  `json:"timeSource"`
		} `json:"lines"`
	}](t, raw).Lines
	if len(lines) == 0 {
		t.Fatalf("no lines in %s", raw)
	}
	// Oldest first, as before.
	if !strings.HasSuffix(lines[0].Text, "ERROR first failure "+logsSentinel) {
		t.Errorf("first line = %q, want the oldest", lines[0].Text)
	}
	first := lines[0]
	if first.Level != "error" || first.TimeSource != "line" || first.Time == nil || *first.Time != f.at(9*time.Minute).Format(time.RFC3339) ||
		!strings.HasPrefix(first.ID, logsCopyShort+".stdout.0.") {
		t.Errorf("first line = %+v", first)
	}
	for _, l := range lines {
		if strings.ContainsRune(l.Text, 0x1b) {
			t.Errorf("line %q has a control sequence", l.Text)
		}
		if l.Level == "" || l.TimeSource == "" || l.ID == "" {
			t.Errorf("line %+v lacks level, timeSource or id", l)
		}
	}
}

// A deploy token may use none of the three routes.
func TestLogsRoutesRefuseDeployTokens(t *testing.T) {
	f := newLogsFixture(t)
	shopProject(t, f.e)
	auth := scopedToken(t, f.e, "shop", "web")
	for _, path := range []string{
		"/v1/logs", "/v1/logs?app=shop--staging--web", "/v1/logs/around?copy=" + logsCopyShort + "&at=" + logsCopyShort + ".stdout.0.0",
		"/v1/logs/download",
	} {
		status, _, raw := callWith(t, auth, http.MethodGet, f.e.RawURL+path, "", "")
		wantError(t, path+" with a deploy token", status, raw, http.StatusForbidden, codeTokenScope, "")
		if strings.Contains(string(raw), logsSentinel) {
			t.Errorf("%s: the answer holds the app's text: %s", path, raw)
		}
	}
	// And a request without credentials is the uniform 401.
	status, _, raw := callWith(t, "", http.MethodGet, f.e.RawURL+"/v1/logs", "", "")
	wantError(t, "no credentials", status, raw, http.StatusUnauthorized, codeUnauthorized, "")
}

// No answer, error or log line holds the app's text but a 200 body of the reads.
func TestLogsNeverLeakIntoLogs(t *testing.T) {
	f := newLogsFixture(t)
	f.readOK(t, "/v1/logs?levels=error,warn,info,debug")
	f.get(t, "/v1/logs/download?levels=error,warn,info,debug")
	f.get(t, "/v1/logs/around?copy="+logsCopyShort+"&at="+logsCopyShort+".stdout.0.99999999")
	f.get(t, "/v1/logs?copy=deadbeef")
	if log := f.e.Logs.String(); strings.Contains(log, logsSentinel) {
		t.Errorf("the server log holds the app's text:\n%s", log)
	}
}
