package ui

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const (
	terminalPath = "/apps/whoami/copies/" + actCopy1 + "/terminal"

	// What the terminal page's policy is, word for word: the console's, with styles allowed inline
	// (xterm.js makes <style> elements) and the websocket's own origin named.
	wantTerminalCSP = "default-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'"
	wantConsoleCSP  = "default-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'"
)

func TestTerminalPage(t *testing.T) {
	e := actionsEnv(t)
	resp, body := e.get(t, terminalPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET terminal = %d:\n%s", resp.StatusCode, text(body))
	}

	t.Run("it names the app, the service and the copy, and links back", func(t *testing.T) {
		got := text(body)
		for _, want := range []string{"Terminal in whoami", "Service web", "copy c1111111", "Back to the Running tab"} {
			if !strings.Contains(got, want) {
				t.Errorf("the page lacks %q: %s", want, got)
			}
		}
		if !strings.Contains(body, `title="`+actCopy1+`"`) || !strings.Contains(body, `href="/apps/whoami"`) {
			t.Errorf("no full copy id or link back:\n%s", body)
		}
	})

	t.Run("it hands the script what it needs in data attributes", func(t *testing.T) {
		for _, want := range []string{
			`data-socket="/v1/apps/whoami/copies/` + actCopy1 + `/terminal"`,
			`data-terminal-screen`, `data-terminal-status`, `data-terminal-new`, `data-terminal-close`,
			`<option value="auto" selected>Automatic</option>`, `<option value="bash">bash</option>`, `<option value="sh">sh</option>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the page lacks %q:\n%s", want, body)
			}
		}
		got := text(body)
		for _, want := range []string{"Connecting…", "New session", "Close"} {
			if !strings.Contains(got, want) {
				t.Errorf("the page lacks %q: %s", want, got)
			}
		}
	})

	t.Run("it loads xterm, its addon and its own script, and has no inline script", func(t *testing.T) {
		for _, want := range []string{"xterm.css", "xterm.js", "addon-fit.js", "terminal.js"} {
			if !regexp.MustCompile(`(href|src)="/static/js/` + regexp.QuoteMeta(want) + `\?v=[0-9a-f]{12}"`).MatchString(body) {
				t.Errorf("the page doesn't load %s:\n%s", want, body)
			}
		}
		for _, tag := range regexp.MustCompile(`<script[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(tag, " src=") {
				t.Errorf("an inline script: %s", tag)
			}
		}
		for _, bad := range []string{" onclick=", " onchange=", "javascript:"} {
			if strings.Contains(body, bad) {
				t.Errorf("the page has %q", bad)
			}
		}
	})

	t.Run("without JavaScript it says what works instead", func(t *testing.T) {
		if !strings.Contains(body, "<noscript>") ||
			!strings.Contains(text(body), "The terminal needs JavaScript. Run a command (on the Running tab) works without it.") {
			t.Errorf("no noscript text:\n%s", body)
		}
	})

	t.Run("the page has its own policy, exactly, and no other page does", func(t *testing.T) {
		if got := resp.Header.Get("Content-Security-Policy"); got != wantTerminalCSP {
			t.Errorf("Content-Security-Policy = %q, want %q", got, wantTerminalCSP)
		}
		for _, path := range []string{"/apps/whoami", "/apps", "/settings", "/apps/whoami/configure"} {
			r, _ := e.get(t, path)
			if got := r.Header.Get("Content-Security-Policy"); got != wantConsoleCSP {
				t.Errorf("Content-Security-Policy of %s = %q, want %q", path, got, wantConsoleCSP)
			}
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
	})

	t.Run("the shell asked for is the one selected", func(t *testing.T) {
		_, body := e.get(t, terminalPath+"?shell=sh")
		if !strings.Contains(body, `<option value="sh" selected>sh</option>`) || strings.Contains(body, `value="auto" selected`) {
			t.Errorf("?shell=sh didn't select sh:\n%s", body)
		}
		_, body = e.get(t, terminalPath+"?shell=rm")
		if !strings.Contains(body, `<option value="auto" selected>`) {
			t.Errorf("an unknown shell wasn't ignored:\n%s", body)
		}
	})

	t.Run("it needs a session", func(t *testing.T) {
		r, _ := e.do(t, request{path: terminalPath})
		if r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, r), "/login") {
			t.Errorf("without a session: %d to %q", r.StatusCode, location(t, r))
		}
	})
}

func TestTerminalPageRefusals(t *testing.T) {
	e := actionsEnv(t)

	t.Run("an unknown app", func(t *testing.T) {
		resp, body := e.get(t, "/apps/ghost/copies/x/terminal")
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "There's no app called ghost.") {
			t.Errorf("unknown app = %d: %s", resp.StatusCode, text(body))
		}
	})
	t.Run("a copy that isn't the app's", func(t *testing.T) {
		resp, body := e.get(t, "/apps/whoami/copies/ffffffff-0000-4000-8000-000000000009/terminal")
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "That copy isn't part of the app.") {
			t.Errorf("unknown copy = %d: %s", resp.StatusCode, text(body))
		}
		if resp.Header.Get("Content-Security-Policy") != wantConsoleCSP {
			t.Errorf("a refusal has the terminal's policy: %q", resp.Header.Get("Content-Security-Policy"))
		}
	})
	t.Run("a copy Nomad is stopping", func(t *testing.T) {
		e.Nomad.SetCopies("whoami",
			api.AllocationListStub{ID: actCopy1, TaskGroup: "web", ClientStatus: "running", DesiredStatus: "stop", JobVersion: 0},
			api.AllocationListStub{ID: actCopy2, TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
		resp, body := e.get(t, terminalPath)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "That copy isn't running.") {
			t.Errorf("stopping copy = %d: %s", resp.StatusCode, text(body))
		}
		// The Running tab offers the terminal of the other copy only.
		_, tab := e.get(t, "/apps/whoami")
		if strings.Contains(tab, terminalPath) || !strings.Contains(tab, "/copies/"+actCopy2+"/terminal") {
			t.Errorf("the Running tab's terminal links are wrong:\n%s", tab)
		}
	})
	t.Run("a copy that ended", func(t *testing.T) {
		e.Nomad.SetCopies("whoami",
			api.AllocationListStub{ID: actCopy1, TaskGroup: "web", ClientStatus: "complete", JobVersion: 0},
			api.AllocationListStub{ID: actCopy2, TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
		resp, body := e.get(t, terminalPath)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "That copy isn't running.") {
			t.Errorf("ended copy = %d: %s", resp.StatusCode, text(body))
		}
	})
	t.Run("a stopped app", func(t *testing.T) {
		e.Nomad.SetCopies("whoami",
			api.AllocationListStub{ID: actCopy1, TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
		resp, body := e.postAction(t, "/apps/whoami/stop")
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("stop = %d: %s", resp.StatusCode, text(body))
		}
		resp, body = e.get(t, terminalPath)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "The app is stopped: start it first.") {
			t.Errorf("stopped app = %d: %s", resp.StatusCode, text(body))
		}
	})
}

func TestRunningTabOffersATerminalPerCopy(t *testing.T) {
	e := actionsEnv(t)
	_, body := e.get(t, "/apps/whoami")
	for _, id := range []string{actCopy1, actCopy2} {
		link := `<a href="/apps/whoami/copies/` + id + `/terminal"`
		i := strings.Index(body, link)
		if i < 0 {
			t.Fatalf("no Terminal link for copy %s:\n%s", id, body)
		}
		// It sits in that copy's own form, next to Run.
		form := body[strings.LastIndex(body[:i], "<form"):]
		form = form[:strings.Index(form, "</form>")]
		if !strings.Contains(form, `action="/apps/whoami/copies/`+id+`/run"`) || !strings.Contains(form, ">Terminal</a>") {
			t.Errorf("the link isn't in copy %s's run form:\n%s", id, form)
		}
	}
}

func TestTerminalRowsInRecentActions(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	started := now.Add(-4 * time.Minute)
	finished := started.Add(4 * time.Minute)
	zero, three := 0, 3
	tests := []struct {
		name string
		rec  store.ActionRecord
		want string // "<label> <program> <duration> <status word> | <detail>"
	}{
		{"a shell that exited 0", store.ActionRecord{Program: "sh", Status: store.ActionSucceeded, ExitCode: &zero, FinishedAt: &finished}, "Terminal sh 4 min exit 0 | "},
		{"a shell that exited 3", store.ActionRecord{Program: "bash", Status: store.ActionSucceeded, ExitCode: &three, FinishedAt: &finished}, "Terminal bash 4 min exit 3 | "},
		{"an idle one", store.ActionRecord{Program: "auto", Status: store.ActionSucceeded, Error: "idle for 15 minutes", FinishedAt: &finished}, "Terminal auto 4 min ended | idle for 15 minutes"},
		{"one that was lost", store.ActionRecord{Program: "sh", Status: store.ActionFailed, Error: "connection lost", FinishedAt: &finished}, "Terminal sh 4 min failed | connection lost"},
		{"one still open", store.ActionRecord{Program: "sh", Status: store.ActionRunning}, "Terminal sh  running | "},
	}
	for _, tt := range tests {
		tt.rec.Action, tt.rec.Trigger, tt.rec.StartedAt, tt.rec.CopyID = store.ActionTerminal, "console", started, actCopy1
		row := actionRowOf(now, tt.rec)
		if got := row.Label + " " + row.Program + " " + row.Duration + " " + row.Status.Word + " | " + row.Error; got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
		if row.Trigger != "from the console" || row.Copy != "c1111111" {
			t.Errorf("%s: trigger %q, copy %q", tt.name, row.Trigger, row.Copy)
		}
	}
}

// A stored terminal action shows on the Running tab in the words of the spec, and its record has no
// input or output to show.
func TestStoredTerminalActionOnTheRunningTab(t *testing.T) {
	e := actionsEnv(t)
	zero := 0
	started := time.Now().UTC().Add(-4 * time.Minute)
	finished := started.Add(4 * time.Minute)
	rec := store.ActionRecord{ID: "act_terminal0001", AppID: "whoami", Action: store.ActionTerminal, Trigger: string(app.TriggerConsole),
		Status: store.ActionSucceeded, CopyID: actCopy1, Service: "web", Program: "sh", ExitCode: &zero,
		StartedAt: started, FinishedAt: &finished}
	if err := e.Store.InsertAction(context.Background(), rec); err != nil {
		t.Fatalf("InsertAction: %v", err)
	}
	_, body := e.get(t, "/apps/whoami")
	got := text(body)
	for _, want := range []string{"exit 0 Terminal sh in copy c1111111 4 min from the console"} {
		if !strings.Contains(got, want) {
			t.Errorf("Recent actions lack %q: %s", want, got)
		}
	}
	_, part := e.get(t, "/parts/apps/whoami/actions")
	if !strings.Contains(text(part), "Terminal sh") {
		t.Errorf("the actions part lacks the session: %s", text(part))
	}
}
