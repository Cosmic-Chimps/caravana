package mcpserver

import (
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// TestImports: the server is an API client only (constitution V). It may not
// import the service, the store, the deploy engine, the translator or anything
// of Nomad's. The one Caravana package it may use is internal/app.
func TestImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	const module = "github.com/cosmic-chimps/caravana/"
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, "nomad") {
				t.Errorf("%s imports %s: Nomad is reached through the API only", name, path)
			}
			if rest, ok := strings.CutPrefix(path, module); ok && rest != "internal/app" {
				t.Errorf("%s imports %s: this package is an API client only", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no source files to check")
	}
}

// TestTokenNeverAppears: a full session with a token that is easy to search
// for, against an API that does its worst (echoes the Authorization header in
// its answers and errors), leaves the token out of every answer, every error
// and every log line, and out of every URL.
func TestTokenNeverAppears(t *testing.T) {
	const answerSentinel = "answer-sentinel-0265"

	f := newFakeAPI(t)
	echo := func(status int, format string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			// The answer says what it was sent, token included.
			io.WriteString(w, strings.ReplaceAll(format, "AUTH", r.Header.Get("Authorization")))
		}
	}
	f.on("GET /v1/apps", echo(200, `{"apps":["`+answerSentinel+` AUTH"]}`))
	f.on("GET /v1/apps/web", echo(422, `{"error":{"code":"invalid_field","message":"`+answerSentinel+` AUTH","field":"x"}}`))
	f.on("GET /v1/apps/old", echo(401, `{"error":{"code":"unauthorized","message":"AUTH"}}`))
	f.on("GET /v1/apps/gw", echo(502, `<html>AUTH `+answerSentinel+`</html>`))
	f.on("GET /v1/apps/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.invalid/?t="+r.Header.Get("Authorization"), http.StatusFound)
	})

	log, logs := newTestLog()
	cs := f.session(t, true, nil, fastTuning, log)

	var texts []string
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"list_apps", nil},
		{"get_app", map[string]any{"app": "web"}},
		{"get_app", map[string]any{"app": "old"}},
		{"get_app", map[string]any{"app": "gw"}},
		{"get_app", map[string]any{"app": "moved"}},
		{"get_app", map[string]any{"app": "Bad ID"}}, // refused before the API
		{"stop_app", map[string]any{"app": "missing"}},
		{"get_version", nil},
	} {
		res, err := callTool(t, cs, c.tool, c.args)
		if err != nil {
			texts = append(texts, err.Error())
			continue
		}
		texts = append(texts, text(res))
	}

	f.srv.Close()
	res, err := callTool(t, cs, "list_apps", nil) // unreachable
	if err != nil {
		t.Fatal(err)
	}
	texts = append(texts, text(res))

	for i, got := range texts {
		if strings.Contains(got, sentinelToken) {
			t.Errorf("answer %d holds the token: %q", i, got)
		}
	}
	if !strings.Contains(strings.Join(texts, "\n"), "[redacted]") {
		t.Error("the echoed token was not replaced: the test did not exercise the redaction")
	}
	logged := logs.String()
	if logged == "" {
		t.Fatal("nothing was logged: the test did not exercise the log")
	}
	if strings.Contains(logged, sentinelToken) {
		t.Errorf("the log holds the token: %s", logged)
	}
	if strings.Contains(logged, answerSentinel) {
		t.Errorf("the log holds an answer: %s", logged)
	}
	for _, r := range f.requests() {
		if strings.Contains(r.Path+"?"+r.Query, sentinelToken) {
			t.Errorf("the token is in a URL: %s", r.Path)
		}
	}
}

// TestLogHasOneLinePerCall: the log has the tool, the outcome and the API's
// code, and nothing of the input.
func TestLogHasOneLinePerCall(t *testing.T) {
	f := newFakeAPI(t)
	f.reply("GET /v1/apps", 200, `{"apps":[]}`)
	f.reply("GET /v1/apps/web", 409, `{"error":{"code":"app_stopped","message":"stopped"}}`)
	log, logs := newTestLog()
	cs := f.session(t, false, nil, fastTuning, log)

	mustCall(t, cs, "list_apps", nil)
	mustFail(t, cs, "get_app", map[string]any{"app": "web"})

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d log lines, want 2: %s", len(lines), logs.String())
	}
	for _, want := range []string{`"tool":"list_apps"`, `"outcome":"ok"`} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("line 1 lacks %s: %s", want, lines[0])
		}
	}
	for _, want := range []string{`"tool":"get_app"`, `"outcome":"error"`, `"code":"app_stopped"`, `"status":409`} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("line 2 lacks %s: %s", want, lines[1])
		}
	}
	if strings.Contains(logs.String(), `"web"`) {
		t.Errorf("the log holds an input (the app): %s", logs.String())
	}
}
