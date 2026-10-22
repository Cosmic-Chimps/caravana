package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const (
	uiSentinel    = "s3cret-sentinel-0205-ui"
	uiArgSentinel = "s3cret-arg-0205-ui"
	actCopy1      = "c1111111-0000-4000-8000-000000000001"
	actCopy2      = "c2222222-0000-4000-8000-000000000002"
)

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
		err  string // a fragment of the error; "" for none
	}{
		{"one word", "ls", []string{"ls"}, ""},
		{"words", "/app/Umbral reset-credential", []string{"/app/Umbral", "reset-credential"}, ""},
		{"extra spaces and tabs", "  echo \t a   b  ", []string{"echo", "a", "b"}, ""},
		{"single quotes keep everything", `sh -c 'echo "a b" | wc -l'`, []string{"sh", "-c", `echo "a b" | wc -l`}, ""},
		{"a backslash in single quotes is a backslash", `echo 'a\nb'`, []string{"echo", `a\nb`}, ""},
		{"double quotes group", `echo "a b" c`, []string{"echo", "a b", "c"}, ""},
		{"escaped quote and backslash in double quotes", `echo "say \"hi\" \\ done"`, []string{"echo", `say "hi" \ done`}, ""},
		{"another escape in double quotes stays as typed", `echo "a\nb"`, []string{"echo", `a\nb`}, ""},
		{"a variable is plain text", `echo "$HOME" $PATH`, []string{"echo", "$HOME", "$PATH"}, ""},
		{"a backslash outside quotes escapes", `echo a\ b \"c\"`, []string{"echo", "a b", `"c"`}, ""},
		{"pieces next to each other are one word", `echo a"b c"'d e'f`, []string{"echo", "ab cd ef"}, ""},
		{"an empty pair is an empty word", `echo "" ''`, []string{"echo", "", ""}, ""},
		{"non-ASCII", "echo héllo 'wörld ü'", []string{"echo", "héllo", "wörld ü"}, ""},
		{"empty", "", nil, "type a command"},
		{"blank", "  \t ", nil, "type a command"},
		{"an unclosed single quote", "echo 'abc", nil, "' quote isn't closed"},
		{"an unclosed double quote", `echo "abc`, nil, `" quote isn't closed`},
		{"an escaped quote doesn't close", `echo "abc\"`, nil, `" quote isn't closed`},
		{"a trailing backslash", `echo abc\`, nil, "backslash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitCommand(tt.line)
			switch {
			case tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)):
				t.Errorf("splitCommand(%q) = %q, %v; want an error with %q", tt.line, got, err, tt.err)
			case tt.err == "" && err != nil:
				t.Errorf("splitCommand(%q) error: %v", tt.line, err)
			case tt.err == "" && !reflect.DeepEqual(got, tt.want):
				t.Errorf("splitCommand(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

func TestCapitalize(t *testing.T) {
	for in, want := range map[string]string{"a quote": "A quote", "": "", "Éa": "Éa", "éa": "Éa"} {
		if got := capitalize(in); got != want {
			t.Errorf("capitalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// actionsEnv has whoami deployed through the service (healthy) with two running copies, and a
// command that prints the sentinel and exits 0.
func actionsEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	rec, err := e.Svc.Deploy(context.Background(), whoamiApp(), app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	e.waitDeploy(t, rec.Deployment.ID)
	e.Nomad.SetCopies("whoami",
		api.AllocationListStub{ID: actCopy1, TaskGroup: "web", ClientStatus: "running", JobVersion: 0},
		api.AllocationListStub{ID: actCopy2, TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
	e.Nomad.SetRestartStop(0)
	e.Nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
		return deploytest.ExecReply{Stdout: uiSentinel + "\n", Stderr: "a warning\n"}
	})
	return e
}

// postAction posts a stop, start or restart. The deploy that actionsEnv waits
// for holds the app's lock a moment longer than its record says it ended (it
// still sends its notices), and these actions refuse a held lock with 409
// "busy" rather than wait. So a busy answer is tried again for up to 5 s;
// every other answer is returned as it came.
func (e *consoleEnv) postAction(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	for range 100 {
		resp, body := e.postForm(t, path, url.Values{})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "Wait for it to finish") {
			return resp, body
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("POST %s stayed busy for 5 s", path)
	return nil, ""
}

func (e *consoleEnv) runIn(t *testing.T, copyID, line string) (*http.Response, string) {
	t.Helper()
	return e.postForm(t, "/apps/whoami/copies/"+copyID+"/run", url.Values{"command": {line}})
}

// waitActions waits until the app has n actions and none is running.
func (e *consoleEnv) waitActions(t *testing.T, id string, n int) []store.ActionRecord {
	t.Helper()
	for range 500 {
		acts, err := e.Svc.ListActions(context.Background(), id, 20)
		if err != nil {
			t.Fatal(err)
		}
		done := len(acts) == n
		for _, a := range acts {
			done = done && a.Status != store.ActionRunning
		}
		if done {
			return acts
		}
		sleepMs(10)
	}
	t.Fatalf("%s didn't reach %d finished actions", id, n)
	return nil
}

// US1: the Running tab has a form per live copy; the answer to a post shows the output once.
func TestRunCommandInTheConsole(t *testing.T) {
	e := actionsEnv(t)

	t.Run("the Running tab offers a form per live copy and explains the line", func(t *testing.T) {
		_, body := e.get(t, "/apps/whoami")
		for _, id := range []string{actCopy1, actCopy2} {
			if !strings.Contains(body, `action="/apps/whoami/copies/`+id+`/run"`) {
				t.Errorf("no run form for copy %s:\n%s", id, body)
			}
		}
		got := text(body)
		for _, want := range []string{"Run a command", "Quote like a shell", "sh -c '...'", "isn't kept"} {
			if !strings.Contains(got, want) {
				t.Errorf("the tab lacks %q: %s", want, got)
			}
		}
		if strings.Contains(got, "This output is shown once") {
			t.Error("the tab shows a result before a command ran")
		}
		if !strings.Contains(got, "Nothing yet.") {
			t.Errorf("the recent actions say something before any action: %s", got)
		}
	})

	t.Run("the answer shows the output, the exit code and the way it ended, once, and is not cached", func(t *testing.T) {
		resp, body := e.runIn(t, actCopy1, `echo --password=`+uiArgSentinel)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("run = %d:\n%s", resp.StatusCode, body)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", cc)
		}
		got := text(body)
		for _, want := range []string{uiSentinel, "a warning", "exit code 0", "This output is shown once; it isn't kept.", "in copy c1111111", "of web"} {
			if !strings.Contains(got, want) {
				t.Errorf("the answer lacks %q: %s", want, got)
			}
		}
		if !strings.Contains(body, `<pre class="cv-output"`) {
			t.Errorf("the output isn't in a <pre>:\n%s", body)
		}
		// The forms are there again, empty.
		if !strings.Contains(body, `action="/apps/whoami/copies/`+actCopy2+`/run"`) {
			t.Error("the answer has no run form")
		}
		execs := e.Nomad.Execs()
		if len(execs) != 1 || execs[0].Copy != actCopy1 || execs[0].Task != "web" ||
			!reflect.DeepEqual(execs[0].Command, []string{"echo", "--password=" + uiArgSentinel}) {
			t.Errorf("exec requests = %+v", execs)
		}
	})

	t.Run("nothing of the output or the arguments is anywhere afterwards", func(t *testing.T) {
		paths := []string{"/apps/whoami", "/apps/whoami/deployments", "/apps/whoami/configure", "/apps/whoami/backups",
			"/apps/whoami/logs", "/apps", "/settings", "/parts/apps", "/parts/activity", "/parts/attention",
			"/parts/apps/whoami/header", "/parts/apps/whoami/copies", "/parts/apps/whoami/deployments",
			"/parts/apps/whoami/actions", "/apps/whoami/stop"}
		for _, p := range paths {
			if _, body := e.get(t, p); strings.Contains(body, uiSentinel) || strings.Contains(body, uiArgSentinel) {
				t.Errorf("GET %s has the sentinel", p)
			}
		}
		if logs := e.Logs.String(); strings.Contains(logs, uiSentinel) || strings.Contains(logs, uiArgSentinel) {
			t.Errorf("the log has the sentinel:\n%s", logs)
		}
		acts, err := e.Svc.ListActions(context.Background(), "whoami", 10)
		if err != nil || len(acts) != 1 || acts[0].Program != "echo" || fmt.Sprintf("%+v", acts[0]) == "" ||
			strings.Contains(fmt.Sprintf("%+v", acts), uiSentinel) || strings.Contains(fmt.Sprintf("%+v", acts), uiArgSentinel) {
			t.Errorf("actions = %+v, %v", acts, err)
		}
	})

	t.Run("the recent actions list the program and the exit code, never the arguments", func(t *testing.T) {
		_, body := e.get(t, "/apps/whoami")
		got := text(body)
		for _, want := range []string{"Recent actions", "Run echo", "exit 0", "in copy c1111111", "from the console"} {
			if !strings.Contains(got, want) {
				t.Errorf("the list lacks %q: %s", want, got)
			}
		}
	})

	t.Run("a non-zero exit is said, with the warning mark", func(t *testing.T) {
		e.Nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{ExitCode: 3}
		})
		resp, body := e.runIn(t, actCopy2, `sh -c 'exit 3'`)
		got := text(body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(got, "exit code 3") || !strings.Contains(body, "cv-pill-warning") {
			t.Errorf("run = %d: %s", resp.StatusCode, got)
		}
		if got := text(body); strings.Count(got, "Nothing.") != 2 {
			t.Errorf("empty streams aren't both said to be empty: %s", got)
		}
	})

	t.Run("a long output says it was cut", func(t *testing.T) {
		e.Nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Stdout: strings.Repeat("x", 70<<10)}
		})
		_, body := e.runIn(t, actCopy1, "big")
		if got := text(body); !strings.Contains(got, "The output was cut") {
			t.Errorf("no note about the cut: %.300s", got)
		}
	})

	t.Run("a program the image lacks is said plainly", func(t *testing.T) {
		e.Nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
			return deploytest.ExecReply{Fail: `exec: "nope": executable file not found in $PATH`}
		})
		resp, body := e.runIn(t, actCopy1, "nope")
		got := text(body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(got, "It couldn't be started") || !strings.Contains(got, "executable file not found") ||
			!strings.Contains(got, "not started") {
			t.Errorf("run = %d: %s", resp.StatusCode, got)
		}
	})
}

// Refusals draw the tab again with the reason and keep what was typed; none is a redirect.
func TestRunCommandRefusals(t *testing.T) {
	e := actionsEnv(t)
	tests := []struct {
		name   string
		copy   string
		line   string
		status int
		want   string
	}{
		{"an unclosed quote", actCopy1, `echo "abc`, 422, `A " quote isn't closed`},
		{"nothing typed", actCopy1, "   ", 422, "Type a command"},
		{"a NUL byte", actCopy1, "echo a\x00b", 422, "Can't run that: item 1 has a NUL byte."},
		{"a copy that isn't the app's", "ffffffff-0000-4000-8000-000000000009", "true", 404, "That copy isn't part of the app any more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := e.runIn(t, tt.copy, tt.line)
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d:\n%s", resp.StatusCode, tt.status, text(body))
			}
			if !strings.Contains(text(body), tt.want) {
				t.Errorf("the page lacks %q: %s", tt.want, text(body))
			}
			if resp.Header.Get("Location") != "" {
				t.Errorf("a refusal redirects to %q", resp.Header.Get("Location"))
			}
		})
	}
	if n := len(e.Nomad.Execs()); n != 0 {
		t.Errorf("%d commands reached Nomad, want none", n)
	}

	t.Run("what was typed stays in that copy's form", func(t *testing.T) {
		_, body := e.runIn(t, actCopy2, `echo 'abc`)
		form := body[strings.Index(body, `id="run-c2222222"`):]
		form = form[:strings.Index(form, "</form>")]
		if !strings.Contains(form, `value="echo &#39;abc"`) || !strings.Contains(form, `aria-invalid="true"`) {
			t.Errorf("the second copy's form is:\n%s", form)
		}
		first := body[strings.Index(body, `id="run-c1111111"`):]
		first = first[:strings.Index(first, "</form>")]
		if strings.Contains(first, "aria-invalid") {
			t.Errorf("the first copy's form is marked too:\n%s", first)
		}
	})

	t.Run("a copy that ended", func(t *testing.T) {
		e.Nomad.SetCopies("whoami",
			api.AllocationListStub{ID: actCopy1, TaskGroup: "web", ClientStatus: "complete", JobVersion: 0},
			api.AllocationListStub{ID: actCopy2, TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
		resp, body := e.runIn(t, actCopy1, "true")
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "isn't running any more") {
			t.Errorf("run = %d: %s", resp.StatusCode, text(body))
		}
		if strings.Contains(body, `copies/`+actCopy1+`/run`) {
			t.Error("the ended copy still has a form")
		}
	})

	t.Run("Nomad refusing alloc-exec says how to fix it", func(t *testing.T) {
		e.Nomad.FailExec(http.StatusForbidden)
		resp, body := e.runIn(t, actCopy2, "true")
		if resp.StatusCode != http.StatusBadGateway || !strings.Contains(text(body), "Run the installer again") {
			t.Errorf("run = %d: %s", resp.StatusCode, text(body))
		}
		e.Nomad.FailExec(0)
	})

	t.Run("an unknown app is a 404 page", func(t *testing.T) {
		resp, body := e.postForm(t, "/apps/ghost/copies/x/run", url.Values{"command": {"true"}})
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "There's no app called ghost.") {
			t.Errorf("run in an unknown app = %d: %s", resp.StatusCode, text(body))
		}
	})
}

func TestRunCommandNeedsASession(t *testing.T) {
	e := actionsEnv(t)
	resp, _ := e.do(t, request{method: http.MethodPost, path: "/apps/whoami/copies/" + actCopy1 + "/run",
		form: url.Values{"command": {"true"}}, headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login") {
		t.Errorf("a post without a session = %d to %q, want a redirect to /login", resp.StatusCode, location(t, resp))
	}
	resp, _ = e.do(t, request{method: http.MethodPost, path: "/apps/whoami/copies/" + actCopy1 + "/run",
		form: url.Values{"command": {"true"}}, cookies: []*http.Cookie{e.Cookie}, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a cross-site post = %d, want 403", resp.StatusCode)
	}
	if n := len(e.Nomad.Execs()); n != 0 {
		t.Errorf("%d commands ran", n)
	}
}

// US2: the header offers Restart and Stop; Stop asks first; a stopped app shows ○ stopped and Start.
func TestStopAndStartInTheConsole(t *testing.T) {
	e := actionsEnv(t)

	t.Run("a deployed app's header offers Restart and Stop, not Start", func(t *testing.T) {
		_, body := e.get(t, "/apps/whoami")
		for _, want := range []string{`action="/apps/whoami/restart"`, `href="/apps/whoami/stop"`} {
			if !strings.Contains(body, want) {
				t.Errorf("the header lacks %q", want)
			}
		}
		if strings.Contains(body, `action="/apps/whoami/start"`) {
			t.Error("a running app offers Start")
		}
	})

	t.Run("the confirmation says what stops and what waits", func(t *testing.T) {
		resp, body := e.get(t, "/apps/whoami/stop")
		got := text(body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET stop = %d: %s", resp.StatusCode, got)
		}
		for _, want := range []string{"Stop whoami?", "Every copy stops (2 copies now) and the app stops answering.", "Deploys and the webhook wait until you start it", "Nothing is deleted", "Stop whoami", "Cancel"} {
			if !strings.Contains(got, want) {
				t.Errorf("the page lacks %q: %s", want, got)
			}
		}
		if strings.Contains(got, "scheduled backups") {
			t.Errorf("an app without backups talks about them: %s", got)
		}
		if !strings.Contains(body, `action="/apps/whoami/stop"`) {
			t.Error("the page has no stop form")
		}
		if n := e.Nomad.Stops("whoami"); n != 0 {
			t.Errorf("GET stopped the app")
		}
	})

	t.Run("posting stops it, and the page says so", func(t *testing.T) {
		resp, body := e.postAction(t, "/apps/whoami/stop")
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/whoami?did=stop" {
			t.Fatalf("stop = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
		e.Nomad.SetCopies("whoami") // Nomad stops the copies
		_, page := e.get(t, location(t, resp))
		got := text(page)
		for _, want := range []string{"Stopped. Nothing was deleted: Start runs it again exactly as it was.", "stopped", "Start it to run what ran before.",
			"The app is stopped. Start it to run its copies again.", "Stop", "from the console"} {
			if !strings.Contains(got, want) {
				t.Errorf("the page lacks %q: %s", want, got)
			}
		}
		if !strings.Contains(page, `action="/apps/whoami/start"`) || strings.Contains(page, `action="/apps/whoami/restart"`) || strings.Contains(page, `href="/apps/whoami/stop"`) {
			t.Errorf("a stopped app's header isn't just Start:\n%s", page)
		}
		if !strings.Contains(page, `class="cv-pill cv-pill-idle"><span class="cv-mark cv-mark-idle" aria-hidden="true"></span>stopped`) {
			t.Errorf("no idle stopped pill:\n%s", page)
		}
		if strings.Contains(page, `/run"`) {
			t.Error("a stopped app offers Run a command")
		}
		if _, plainPage := e.get(t, "/apps/whoami"); strings.Contains(text(plainPage), "Stopped. Nothing was deleted") {
			t.Error("the notice shows without ?did=stop")
		}
	})

	t.Run("the list shows the app as stopped and does not call it trouble", func(t *testing.T) {
		_, body := e.get(t, "/apps")
		got := text(body)
		if !strings.Contains(got, "stopped") || !strings.Contains(got, "1 app stopped.") || strings.Contains(got, "app needs you") || strings.Contains(got, "apps need you") {
			t.Errorf("the list: %s", got)
		}
	})

	t.Run("every way of deploying says the app is stopped", func(t *testing.T) {
		const want = "The app is stopped: start it first."
		resp, body := e.postForm(t, "/apps/whoami/deploy", url.Values{})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), want) {
			t.Errorf("deploy = %d: %s", resp.StatusCode, text(body))
		}
		_, vars := e.get(t, "/apps/whoami/variables")
		resp, body = e.submit(t, formWith(t, vars, "/apps/whoami/variables"), url.Values{"service": {"web"}, "name": {"K"}, "value": {"v"}, "secret": {"1"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), want) {
			t.Errorf("set a secret = %d: %s", resp.StatusCode, text(body))
		}
		for _, tab := range []string{"/apps/whoami/deployments", "/apps/whoami/configure", "/apps/whoami/variables", "/apps/whoami/domains"} {
			if _, body := e.get(t, tab); !strings.Contains(text(body), want) {
				t.Errorf("%s doesn't say it before anyone tries: %s", tab, text(body))
			}
		}
		recs := e.deployments(t, "whoami")
		if len(recs) != 1 {
			t.Errorf("%d deployments, want the one from before the stop", len(recs))
		}
	})

	t.Run("a second Stop is refused, and the Stop page offers Start", func(t *testing.T) {
		resp, body := e.postAction(t, "/apps/whoami/stop")
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "The app is stopped already.") || resp.Header.Get("Location") != "" {
			t.Errorf("stop again = %d: %s", resp.StatusCode, text(body))
		}
		resp, body = e.get(t, "/apps/whoami/stop")
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This app is stopped already.") || !strings.Contains(body, `action="/apps/whoami/start"`) {
			t.Errorf("GET stop = %d: %s", resp.StatusCode, text(body))
		}
	})

	t.Run("Restart on a stopped app is refused", func(t *testing.T) {
		resp, body := e.postAction(t, "/apps/whoami/restart")
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "The app is stopped: start it first.") {
			t.Errorf("restart = %d: %s", resp.StatusCode, text(body))
		}
	})

	t.Run("posting Start runs it again, and a second Start is refused", func(t *testing.T) {
		resp, body := e.postAction(t, "/apps/whoami/start")
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/whoami?did=start" {
			t.Fatalf("start = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
		}
		e.Nomad.SetCopies("whoami",
			api.AllocationListStub{ID: actCopy1, TaskGroup: "web", ClientStatus: "running", JobVersion: 0},
			api.AllocationListStub{ID: actCopy2, TaskGroup: "web", ClientStatus: "running", JobVersion: 0})
		_, page := e.get(t, location(t, resp))
		got := text(page)
		if !strings.Contains(got, "Started. It runs the same images and settings as before it stopped.") || strings.Contains(got, "Start it to run what ran before.") {
			t.Errorf("the page after Start: %s", got)
		}
		if !strings.Contains(page, `action="/apps/whoami/restart"`) || strings.Contains(page, `action="/apps/whoami/start"`) {
			t.Error("the header after Start isn't Restart and Stop")
		}
		resp, body = e.postAction(t, "/apps/whoami/start")
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "isn't stopped") {
			t.Errorf("start again = %d: %s", resp.StatusCode, text(body))
		}
		if recs := e.deployments(t, "whoami"); len(recs) != 1 {
			t.Errorf("%d deployments after stop and start, want 1: neither is a deploy", len(recs))
		}
	})

	t.Run("the recent actions list the stop and the start", func(t *testing.T) {
		acts := e.waitActions(t, "whoami", 2)
		if acts[0].Action != store.ActionStart || acts[1].Action != store.ActionStop {
			t.Fatalf("actions = %+v", acts)
		}
		_, body := e.get(t, "/parts/apps/whoami/actions")
		got := text(body)
		if !strings.Contains(got, "Start") || !strings.Contains(got, "Stop") || !strings.Contains(got, "done") {
			t.Errorf("the list: %s", got)
		}
	})
}

func TestStopPageOfAnAppWithBackups(t *testing.T) {
	e := backupsEnv(t, true)
	_, body := e.get(t, "/apps/duo/stop")
	if got := text(body); !strings.Contains(got, "Deploys, the webhook and scheduled backups wait until you start it.") {
		t.Errorf("the page: %s", got)
	}
}

// A stopped app refuses a backup with the same words.
func TestBackUpNowOfAStoppedApp(t *testing.T) {
	e := backupsEnv(t, true)
	// The setup's deploy may still hold the app's lock a moment after its
	// record ends, and Stop refuses a held lock: try again while it is busy.
	for i := 0; ; i++ {
		_, err := e.Svc.StopApp(context.Background(), "duo", app.TriggerManual)
		if err == nil {
			break
		}
		if !errors.Is(err, service.ErrAppBusy) || i == 100 {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, body := e.postForm(t, "/apps/duo/backups", url.Values{"service": {"web"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "The app is stopped: start it first.") {
		t.Errorf("Back up now = %d: %s", resp.StatusCode, text(body))
	}
}

// US3: Restart goes on in the background; the page says so, and the action ends done.
func TestRestartInTheConsole(t *testing.T) {
	e := actionsEnv(t)
	resp, body := e.postAction(t, "/apps/whoami/restart")
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/whoami?did=restart" {
		t.Fatalf("restart = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	_, page := e.get(t, "/apps/whoami?did=restart")
	if got := text(page); !strings.Contains(got, "Restarting: its copies restart one at a time") {
		t.Errorf("the page lacks the notice: %s", got)
	}
	acts := e.waitActions(t, "whoami", 1)
	if acts[0].Action != store.ActionRestart || acts[0].Status != store.ActionSucceeded {
		t.Fatalf("actions = %+v", acts)
	}
	if rs := e.Nomad.Restarts(); len(rs) != 2 {
		t.Errorf("restarts = %+v, want both copies", rs)
	}
	_, list := e.get(t, "/parts/apps/whoami/actions")
	if got := text(list); !strings.Contains(got, "Restart") || !strings.Contains(got, "done") || !strings.Contains(got, "from the console") {
		t.Errorf("the list: %s", got)
	}

	t.Run("with no running copy it is refused", func(t *testing.T) {
		e.Nomad.SetCopies("whoami")
		resp, body := e.postAction(t, "/apps/whoami/restart")
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "No copy of the app is running") {
			t.Errorf("restart = %d: %s", resp.StatusCode, text(body))
		}
	})
}

func TestActionsOfAnAppNothingIsDeployedOf(t *testing.T) {
	e := newConsoleEnv(t)
	if _, err := e.Svc.CreateAppOnly(context.Background(), app.App{ID: "shop"}); err != nil {
		t.Fatal(err)
	}
	_, body := e.get(t, "/apps/shop")
	if strings.Contains(body, "/apps/shop/restart") || strings.Contains(body, "/apps/shop/stop") || strings.Contains(body, "/apps/shop/start") {
		t.Errorf("an app with nothing deployed offers actions:\n%s", body)
	}
	for _, op := range []string{"stop", "start", "restart"} {
		resp, page := e.postForm(t, "/apps/shop/"+op, url.Values{})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "Nothing is deployed yet") {
			t.Errorf("%s = %d: %s", op, resp.StatusCode, text(page))
		}
	}
	resp, page := e.get(t, "/apps/shop/stop")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(page), "nothing to stop") {
		t.Errorf("GET stop = %d: %s", resp.StatusCode, text(page))
	}
}

func TestActionRoutesOfAnUnknownApp(t *testing.T) {
	e := newConsoleEnv(t)
	if resp, body := e.get(t, "/apps/ghost/stop"); resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "There's no app called ghost.") {
		t.Errorf("GET stop = %d: %s", resp.StatusCode, text(body))
	}
	for _, op := range []string{"stop", "start", "restart"} {
		if resp, body := e.postForm(t, "/apps/ghost/"+op, url.Values{}); resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "There's no app called ghost.") {
			t.Errorf("%s = %d: %s", op, resp.StatusCode, text(body))
		}
	}
	resp, body := e.get(t, "/parts/apps/ghost/actions")
	if resp.StatusCode != http.StatusOK || !strings.Contains(text(body), "ghost was deleted.") {
		t.Errorf("actions part = %d: %s", resp.StatusCode, text(body))
	}
}

func TestActionRefusalWords(t *testing.T) {
	tests := []struct {
		err    error
		verb   string
		status int
		want   string
	}{
		{service.ErrAppStopped, "restart", 409, "The app is stopped: start it first."},
		{service.ErrAppStopped, "stop", 409, "The app is stopped already."},
		{service.ErrAppNotStopped, "start", 409, "isn't stopped"},
		{fmt.Errorf("app x: %w", service.ErrAppBusy), "stop", 409, "Wait for it to finish"},
		{service.ErrNoLiveDeployment, "restart", 409, "nothing to restart"},
		{service.ErrAppNotDefined, "stop", 409, "nothing to stop"},
		{service.ErrNoRunningCopy, "restart", 409, "No copy of the app is running"},
		{deploy.ErrNomadUnavailable, "stop", 502, "Nomad couldn't be reached"},
		{deploy.ErrNomadRejected, "start", 502, "refused it"},
	}
	for _, tt := range tests {
		status, got, ok := actionRefusal(tt.err, tt.verb)
		if !ok || status != tt.status || !strings.Contains(got.String(), tt.want) {
			t.Errorf("actionRefusal(%v, %s) = %d %q %v; want %d with %q", tt.err, tt.verb, status, got, ok, tt.status, tt.want)
		}
	}
	if _, _, ok := actionRefusal(errors.New("disk on fire"), "stop"); ok {
		t.Error("an unknown error has words")
	}
}

// What the status says for a stopped app.
func TestStoppedAppStatusWords(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	stoppedAt := now.Add(-3 * time.Hour)
	whoami := whoamiApp()
	sum := service.AppSummary{
		AppRecord: store.AppRecord{App: whoami, StoppedAt: &stoppedAt},
		Status:    service.AppStatus{State: service.StateStopped, WantedCopies: 2},
	}
	if got := appStatusView(sum); got != (statusView{"idle", "stopped"}) {
		t.Errorf("appStatusView = %+v, want an idle stopped", got)
	}
	if got := summaryOf(sum, now).String(); got != "Stopped 3 hours ago. Start it to run what ran before. Never deployed." {
		t.Errorf("summaryOf = %q", got)
	}
	if got := headlineOf([]service.AppSummary{sum}, nil).String(); got != "1 app stopped." {
		t.Errorf("headlineOf = %q", got)
	}
	healthy := service.AppSummary{AppRecord: store.AppRecord{App: apiApp()}, Status: service.AppStatus{State: service.StateHealthy, WantedCopies: 2, HealthyCopies: 2}}
	if got := headlineOf([]service.AppSummary{sum, healthy}, nil).String(); got != "1 app healthy, 1 app stopped." {
		t.Errorf("headlineOf = %q", got)
	}
}

func TestActionRowWords(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	started := now.Add(-2 * time.Minute)
	zero, three := 0, 3
	tests := []struct {
		name string
		rec  store.ActionRecord
		want string // "<label> <program> <status word>"
	}{
		{"a run that exited 0", store.ActionRecord{Action: store.ActionRun, Program: "echo", Status: store.ActionSucceeded, ExitCode: &zero}, "Run echo exit 0"},
		{"a run that exited 3", store.ActionRecord{Action: store.ActionRun, Program: "sh", Status: store.ActionSucceeded, ExitCode: &three}, "Run sh exit 3"},
		{"a run that timed out", store.ActionRecord{Action: store.ActionRun, Program: "sleep", Status: store.ActionFailed, Error: "timed out after 1m0s"}, "Run sleep failed"},
		{"a restart under way", store.ActionRecord{Action: store.ActionRestart, Status: store.ActionRunning}, "Restart  running"},
		{"a stop", store.ActionRecord{Action: store.ActionStop, Status: store.ActionSucceeded}, "Stop  done"},
		{"a start", store.ActionRecord{Action: store.ActionStart, Status: store.ActionSucceeded}, "Start  done"},
	}
	for _, tt := range tests {
		tt.rec.Trigger, tt.rec.StartedAt = "manual", started
		row := actionRowOf(now, tt.rec)
		if got := row.Label + " " + row.Program + " " + row.Status.Word; got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
		if row.Trigger != "through the API" || row.When.Text != "2 minutes ago" {
			t.Errorf("%s: trigger %q, when %q", tt.name, row.Trigger, row.When.Text)
		}
	}
}

// A command or an action tells the open page "actions" and nothing else: the notice has no content.
func TestLiveAppStreamSendsActionsNotices(t *testing.T) {
	e := actionsEnv(t)
	ls := openStream(t, e.URL, "/live/apps/whoami?tab=running", e.Cookie.Value)
	ls.expect("sync")
	if resp, body := e.runIn(t, actCopy1, "echo hi"); resp.StatusCode != http.StatusOK {
		t.Fatalf("run = %d: %s", resp.StatusCode, text(body))
	}
	for seen := false; !seen; {
		switch name := ls.next(); name {
		case "actions":
			seen = true
		case "app", "deployments", "copies":
		default:
			t.Fatalf("event %q, want actions", name)
		}
	}
	// The reader keeps only "event:" and comment lines, and an event is a bare name:
	// there is no data line that could carry output.
}
