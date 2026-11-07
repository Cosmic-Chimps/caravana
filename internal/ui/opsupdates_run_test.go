package ui

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 030, the console part: updating caravana from the Updates page. The page is checked on the
// 027 environment with a fake updater directory (the marker file the installer writes), the fake release
// server (v0.16.0, or the tag a test sets) and update runs written straight into the store, so that each
// state of a run is drawn without a root updater to play it.

const (
	runID1 = "3f6c0d2a9b7e41c58d0f2e6a1b9c7d40"
	runLog = "/var/lib/caravana-update/logs/3f6c0d2a9b7e41c58d0f2e6a1b9c7d40.log"
)

// putRun stores an update run from 0.15.0 to 0.16.0, asked five minutes ago, in the state.
func putRun(t *testing.T, e *consoleEnv, state string, edit func(*store.UpdateRun)) store.UpdateRun {
	t.Helper()
	now := time.Now().UTC()
	r := store.UpdateRun{ID: runID1, Version: "0.16.0", FromVersion: "0.15.0", Trigger: store.UpdateByConsole,
		RequestedAt: now.Add(-5 * time.Minute), State: state}
	if state != store.UpdateRequested {
		r.StartedAt = now.Add(-4 * time.Minute)
	}
	if store.UpdateRunFinal(state) {
		r.FinishedAt = r.StartedAt.Add(100 * time.Second)
	}
	if edit != nil {
		edit(&r)
	}
	if _, err := e.Store.InsertUpdateRun(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

func dropRun(t *testing.T, e *consoleEnv, id string) {
	t.Helper()
	if err := e.Store.DeleteUpdateRun(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

// runPanel is the text of the page's run panel, "" when it has none.
func runPanel(t *testing.T, body string) (html, words string) {
	t.Helper()
	i := strings.Index(body, `<section id="run"`)
	if i < 0 {
		return "", ""
	}
	j := strings.Index(body[i:], "</section>")
	if j < 0 {
		t.Fatalf("the run panel isn't closed:\n%s", body[i:])
	}
	html = body[i : i+j]
	return html, text(html)
}

func TestUpdateRunPanelStates(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true})
	releaseFound(t, e)

	reason := "caravana 0.16.0 didn't answer its health check within 60 s; 0.15.0 runs again, with its data as it was"
	logSentence := "The whole log is on the server: sudo cat " + runLog
	cases := []struct {
		state string
		edit  func(*store.UpdateRun)
		mark  string
		want  []string
	}{
		{store.UpdateRequested, nil, "rolling", []string{
			"Waiting for the server's updater to start the update to 0.16.0. It looks for a request every minute."}},
		{store.UpdateRunning, nil, "rolling", []string{
			"Updating caravana 0.15.0 → 0.16.0. The console stops for about a minute while the new version starts. This page comes back by itself; your apps keep running."}},
		{store.UpdateUpdated, nil, "healthy", []string{"caravana 0.16.0 is running. Updated from 0.15.0 in 1 min 40 s."}},
		{store.UpdateRolledBack, func(r *store.UpdateRun) { r.Reason, r.Log = reason, runLog }, "warning", []string{
			"caravana 0.16.0 didn't start, so 0.15.0 runs again with its data as it was. caravana 0.16.0 didn't answer its health check within 60 s. " + logSentence}},
		{store.UpdateFailed, func(r *store.UpdateRun) { r.Reason, r.Log = "The installer exited with status 1.", runLog }, "failed", []string{
			"The update to caravana 0.16.0 failed. The installer exited with status 1. " + logSentence}},
		{store.UpdateRefused, func(r *store.UpdateRun) { r.Reason = "0.16.0 isn't a release version." }, "warning", []string{
			"The server's updater didn't start the update to 0.16.0. 0.16.0 isn't a release version."}},
		{store.UpdateLost, func(r *store.UpdateRun) { r.Reason = "The updater stopped without saying how it went." }, "warning", []string{
			"The update to caravana 0.16.0 didn't finish. The updater stopped without saying how it went."}},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			putRun(t, e, c.state, c.edit)
			defer dropRun(t, e, runID1)
			resp, body := e.get(t, "/ops/updates")
			if resp.StatusCode != 200 {
				t.Fatalf("GET /ops/updates = %d", resp.StatusCode)
			}
			panel, words := runPanel(t, body)
			if panel == "" {
				t.Fatalf("no run panel:\n%s", text(body))
			}
			for _, want := range c.want {
				if !strings.Contains(words, want) {
					t.Errorf("the panel lacks %q:\n%s", want, words)
				}
			}
			if !strings.Contains(panel, `cv-mark cv-mark-lg cv-mark-`+c.mark) {
				t.Errorf("the panel's mark isn't %s:\n%s", c.mark, panel)
			}
			// The panel is the banners' row: a section of its own, between the head and Available.
			if !strings.Contains(panel, `class="cv-panel cv-dmbanner cv-uprun"`) {
				t.Errorf("the panel isn't a banner row:\n%s", panel)
			}
			if i, j := strings.Index(body, `<section id="run"`), strings.Index(body, `<section id="available"`); i < 0 || j < i {
				t.Errorf("the run panel isn't above Available (%d, %d)", i, j)
			}
			// The meta: who and when, in the deployments' words, with the time through up-time.
			if !regexp.MustCompile(`from the console · asked \d\d:\d\d UTC`).MatchString(words) &&
				!regexp.MustCompile(`from the console · asked [A-Z][a-z]{2} \d+, \d\d:\d\d UTC`).MatchString(words) {
				t.Errorf("the panel's meta:\n%s", words)
			}
			if !strings.Contains(panel, `data-ops-time`) {
				t.Error("the panel's time isn't an up-time")
			}
			// Live only while it is open, and a page without script reloads itself then.
			open := c.state == store.UpdateRequested || c.state == store.UpdateRunning
			poll := `hx-get="/parts/ops/updates/run" hx-trigger="every 5s" hx-swap="outerHTML"`
			noscript := `<noscript><meta http-equiv="refresh" content="15"></noscript>`
			if got := strings.Contains(panel, poll); got != open {
				t.Errorf("polls = %v, want %v", got, open)
			}
			if got := strings.Contains(body, noscript); got != open {
				t.Errorf("the noscript refresh = %v, want %v", got, open)
			}
			if i := strings.Index(body, noscript); open && (i < 0 || i > strings.Index(body, "</head>")) {
				t.Error("the noscript refresh isn't in the head")
			}
		})
	}
}

func TestUpdateRunPanelAbsentWithoutRun(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true})
	releaseFound(t, e)
	_, body := e.get(t, "/ops/updates")
	if panel, _ := runPanel(t, body); panel != "" {
		t.Errorf("a run panel without a run:\n%s", panel)
	}
	if strings.Contains(body, "http-equiv=\"refresh\"") || strings.Contains(body, "/parts/ops/updates/run") {
		t.Error("the page polls or refreshes without an open run")
	}
	// An update that finished more than a day ago isn't shown any more.
	putRun(t, e, store.UpdateUpdated, func(r *store.UpdateRun) {
		r.RequestedAt = time.Now().Add(-30 * time.Hour)
		r.StartedAt = r.RequestedAt
		r.FinishedAt = r.RequestedAt.Add(time.Minute)
	})
	_, body = e.get(t, "/ops/updates")
	if panel, _ := runPanel(t, body); panel != "" {
		t.Errorf("a run panel for a run of 30 hours ago:\n%s", panel)
	}
}

func TestUpdateRunMeta(t *testing.T) {
	now := time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC)
	view := func(r store.UpdateRun) service.UpdatesView {
		var v service.UpdatesView
		v.Caravana.Current, v.Caravana.Latest = "0.16.0", "0.17.0"
		v.Caravana.Run = &r
		return v
	}
	base := store.UpdateRun{Version: "0.17.0", FromVersion: "0.16.0", State: store.UpdateRequested,
		RequestedAt: time.Date(2026, 11, 6, 14, 2, 41, 0, time.UTC)}
	for name, c := range map[string]struct {
		edit func(*store.UpdateRun)
		want string
	}{
		"console":   {func(r *store.UpdateRun) { r.Trigger = store.UpdateByConsole }, "from the console · asked 14:02 UTC"},
		"api":       {func(r *store.UpdateRun) { r.Trigger, r.TokenName = store.UpdateByAPI, "ci-token" }, "through the API by ci-token · asked 14:02 UTC"},
		"automatic": {func(r *store.UpdateRun) { r.Trigger = store.UpdateByAutomatic }, "automatic, at 14:02 UTC"},
		"yesterday": {func(r *store.UpdateRun) {
			r.Trigger = store.UpdateByConsole
			r.RequestedAt = r.RequestedAt.Add(-24 * time.Hour)
		},
			"from the console · asked Nov 5, 14:02 UTC"},
	} {
		r := base
		c.edit(&r)
		run := upRunOf(now, view(r))
		if got := run.Meta.String() + run.Asked.Text; got != c.want {
			t.Errorf("%s: meta = %q, want %q", name, got, c.want)
		}
		if !run.Meta[len(run.Meta)-1].Nowrap && name == "api" && !run.Meta[1].Mono {
			t.Errorf("the token's name isn't a code span")
		}
	}
}

func TestUpdateRunTryAgain(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true})
	releaseFound(t, e)
	href := `<a href="/ops/updates/caravana?version=0.16.0" class="cv-btn cv-btn-secondary cv-btn-sm">Try again…</a>`
	for state, want := range map[string]bool{store.UpdateRolledBack: true, store.UpdateFailed: true, store.UpdateRefused: true,
		store.UpdateLost: true, store.UpdateUpdated: false, store.UpdateRunning: false, store.UpdateRequested: false} {
		putRun(t, e, state, nil)
		_, body := e.get(t, "/ops/updates")
		panel, _ := runPanel(t, body)
		if got := strings.Contains(panel, href); got != want {
			t.Errorf("%s: Try again = %v, want %v\n%s", state, got, want, panel)
		}
		dropRun(t, e, runID1)
	}
}

// The run's own part: the panel alone while the run is open; once it has finished, the panel without its
// poll and, by id, the main region, Past updates, the settings' lines and the sidebar's mark.
func TestUpdateRunPart(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true})
	releaseFound(t, e)

	putRun(t, e, store.UpdateRunning, nil)
	_, body := e.get(t, "/parts/ops/updates/run")
	if !strings.HasPrefix(strings.TrimSpace(body), `<section id="run"`) || strings.Contains(body, "hx-swap-oob") {
		t.Errorf("an open run's part isn't the panel alone:\n%s", body)
	}
	if !strings.Contains(body, `hx-trigger="every 5s"`) {
		t.Error("an open run's part doesn't poll again")
	}
	dropRun(t, e, runID1)

	putRun(t, e, store.UpdateUpdated, nil)
	_, body = e.get(t, "/parts/ops/updates/run")
	if !strings.HasPrefix(strings.TrimSpace(body), `<section id="run"`) || strings.Contains(body, "every 5s") {
		t.Errorf("a finished run's panel polls or isn't first:\n%s", body)
	}
	for _, want := range []string{`hx-swap-oob="innerHTML:#up-top"`, `id="available"`, `hx-swap-oob="innerHTML:#up-past"`,
		`hx-swap-oob="innerHTML:#nav-updates-mark"`, `hx-swap-oob="innerHTML:#up-banners"`, `hx-swap-oob="innerHTML:#up-autoline"`,
		"caravana 0.16.0 is running."} {
		if !strings.Contains(body, want) && !strings.Contains(text(body), want) {
			t.Errorf("a finished run's part lacks %q", want)
		}
	}
	dropRun(t, e, runID1)

	// No run: nothing, so that the swap removes the panel.
	resp, body := e.get(t, "/parts/ops/updates/run")
	if resp.StatusCode != 200 || strings.TrimSpace(body) != "" {
		t.Errorf("no run: %d %q", resp.StatusCode, body)
	}
	// A part needs a session.
	if resp, _ := e.do(t, request{path: "/parts/ops/updates/run"}); resp.StatusCode == 200 {
		t.Errorf("GET /parts/ops/updates/run without a session = %d", resp.StatusCode)
	}
}

func TestCaravanaRowAction(t *testing.T) {
	update := `<a href="/ops/updates/caravana?version=0.16.0" class="cv-btn cv-btn-primary cv-btn-sm">Update to 0.16.0…</a>`

	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true})
	releaseFound(t, e)
	_, body := e.get(t, "/ops/updates")
	row := regionBetween(t, body, `data-up="release"`, `id="apply"`)
	if !strings.Contains(row, update) {
		t.Errorf("the caravana row has no Update action:\n%s", row)
	}
	// After the notes and the release link.
	if i, j := strings.Index(row, "Release notes on GitHub"), strings.Index(row, update); i < 0 || j < i {
		t.Error("the action isn't after the release notes' link")
	}
	// While a run is open the action is left out.
	putRun(t, e, store.UpdateRunning, nil)
	_, body = e.get(t, "/ops/updates")
	if strings.Contains(body, update) || strings.Contains(body, "/ops/updates/caravana?version=") {
		t.Error("the Update action stays while a run is open")
	}
	if apply := text(regionBetween(t, body, `id="apply"`, `id="how"`)); !strings.Contains(apply, "Run the installer of 0.16.0 on the server:") ||
		strings.Contains(apply, "caravana row above") {
		t.Errorf("How to apply's caravana step while a run is open:\n%s", apply)
	}
	dropRun(t, e, runID1)
	// A rolled-back run of this version: the run panel's Try again… is the one way to ask again.
	putRun(t, e, store.UpdateRolledBack, nil)
	_, body = e.get(t, "/ops/updates")
	if strings.Contains(regionBetween(t, body, `data-up="release"`, `id="apply"`), "/ops/updates/caravana?version=") ||
		!strings.Contains(body, "Try again…") {
		t.Error("after a rolled-back run the row still has its Update action, or the panel has no Try again")
	}
	dropRun(t, e, runID1)
	// A finished run leaves it in place.
	putRun(t, e, store.UpdateUpdated, nil)
	_, body = e.get(t, "/ops/updates")
	if !strings.Contains(body, update) {
		t.Error("the Update action is gone after a finished run")
	}

	// How to apply with the updater: the console is the first way, the installer the other.
	apply := text(regionBetween(t, body, `id="apply"`, `id="how"`))
	if !strings.Contains(apply, "Update from the caravana row above, or run the installer of 0.16.0 on the server: curl -fsSL") ||
		strings.Contains(apply, "Running it once also adds the updater") {
		t.Errorf("How to apply's caravana step with the updater:\n%s", apply)
	}

	// Without the updater: the sentence, and no button.
	e2, _ := updEnv(t, updOpts{fixture: "ubuntu-clean"})
	releaseFound(t, e2)
	_, body = e2.get(t, "/ops/updates")
	row = text(regionBetween(t, body, `data-up="release"`, `id="apply"`))
	if !strings.Contains(row, "To update from here, run the installer of 0.16.0 once on the server (How to apply). It adds the updater.") ||
		strings.Contains(body, "/ops/updates/caravana?version=") {
		t.Errorf("the caravana row without the updater:\n%s", row)
	}
	if !strings.Contains(body, `(<a href="#apply" class="cv-upreleaselink">How to apply</a>)`) {
		t.Error("How to apply isn't a link to #apply in the row's sentence")
	}

	// A development build: nothing in its place.
	if a := updateAction(service.CaravanaUpdate{Current: "dev", Latest: "0.16.0"}); a != nil {
		t.Errorf("a development build gets %+v", a)
	}
}

func TestUpdateConfirmPage(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true})
	releaseFound(t, e)
	resp, body := e.get(t, "/ops/updates/caravana?version=0.16.0")
	if resp.StatusCode != 200 {
		t.Fatalf("GET confirm = %d:\n%s", resp.StatusCode, text(body))
	}
	words := text(body)
	for _, want := range []string{
		"Update caravana to 0.16.0",
		"caravana 0.15.0 → 0.16.0. The server's updater starts it within a minute, as root, from the installer: the console only asks.",
		"The console stops for about a minute while the new version starts. Your apps keep running and keep answering.",
		"Nomad and Traefik stay as they are, unless the installer changes their settings.",
		"If 0.16.0 doesn't answer its health check within a minute, 0.15.0 comes back with its data as it was, and this page says why.",
		"OS updates and the reboot aren't part of it: they stay on the server.",
		"Update to 0.16.0 Cancel",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("the page lacks %q:\n%s", want, words)
		}
	}
	for _, want := range []string{
		`<form method="post" action="/ops/updates/caravana" class="cv-actions">`, `<input type="hidden" name="version" value="0.16.0">`,
		`class="cv-btn cv-btn-primary">Update to 0.16.0</button>`,
		`<a href="/ops/updates" class="cv-btn cv-btn-secondary">Cancel</a>`,
		`<span class="cv-inline-mono text-fg-1 cv-nowrap">0.15.0 → 0.16.0</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(body, `role="alert"`) {
		t.Error("the page opens with a refusal")
	}
	if !strings.Contains(body, `<li><a href="/ops/updates" class="cv-crumb">Updates</a></li>`) || !strings.Contains(body, `aria-current="page">Update caravana</span>`) {
		t.Error("the crumb isn't Updates / Update caravana")
	}

	// A version that isn't offered is refused at once, with the service's sentence and no button.
	resp, body = e.get(t, "/ops/updates/caravana?version=0.16.1")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "0.16.1 isn't the newest release caravana has seen (0.16.0). Check for releases first.") ||
		strings.Contains(body, `action="/ops/updates/caravana"`) {
		t.Errorf("GET confirm for 0.16.1 = %d:\n%s", resp.StatusCode, text(body))
	}
	// Needs a session.
	if resp, _ := e.do(t, request{path: "/ops/updates/caravana?version=0.16.0"}); resp.StatusCode == 200 {
		t.Errorf("GET confirm without a session = %d", resp.StatusCode)
	}
}

// The panel's sentences for each pin the release changes, one each; none says they stay.
func TestUpdateConfirmPins(t *testing.T) {
	c := service.CaravanaUpdate{Current: "0.16.0", Latest: "0.17.0", Pins: []service.VersionPin{
		{Name: "Nomad", From: "2.0.7", To: "2.1.0"}, {Name: "Traefik", From: "3.7.13", To: "3.8.0"}}}
	var got []string
	for _, p := range upConfirmOf(c, "0.17.0").Paras {
		got = append(got, p.String())
	}
	want := []string{
		"caravana 0.16.0 → 0.17.0. The server's updater starts it within a minute, as root, from the installer: the console only asks.",
		"The console stops for about a minute while the new version starts. Your apps keep running and keep answering.",
		"Nomad 2.0.7 → 2.1.0 is installed in the same run. Nomad restarts; apps keep running.",
		"Traefik 3.7.13 → 3.8.0 is installed in the same run. Traefik restarts, so every app's address stops answering for a few seconds.",
		"If 0.17.0 doesn't answer its health check within a minute, 0.16.0 comes back with its data as it was, and this page says why.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("paragraphs =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	c.Pins = c.Pins[:1] // only Nomad
	for _, p := range upConfirmOf(c, "0.17.0").Paras {
		if strings.Contains(p.String(), "Traefik") || strings.Contains(p.String(), "stay as they are") {
			t.Errorf("a paragraph for a pin the release doesn't change: %s", p.String())
		}
	}
}

func TestUpdateConfirmPost(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true})
	releaseFound(t, e)

	// A version that isn't the newest, then one that isn't a version.
	resp, body := e.postForm(t, "/ops/updates/caravana", url.Values{"version": {"0.16.1"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "0.16.1 isn't the newest release caravana has seen (0.16.0). Check for releases first.") {
		t.Errorf("POST 0.16.1 = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.postForm(t, "/ops/updates/caravana", url.Values{"version": {"0.15.0"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "caravana 0.15.0 already runs here.") {
		t.Errorf("POST 0.15.0 = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.postForm(t, "/ops/updates/caravana", url.Values{"version": {"soon"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Must be a release version such as 0.17.0.") {
		t.Errorf("POST soon = %d:\n%s", resp.StatusCode, text(body))
	}
	// A refusal is the page again: the sentence in an alert, only the way back, never a redirect.
	main := regionBetween(t, body, `<section class="cv-main">`, "</section>")
	if !strings.Contains(main, `role="alert" class="cv-error"`) || !strings.Contains(main, `>Back to Updates</a>`) ||
		strings.Contains(main, `type="submit"`) || strings.Contains(main, "Cancel") || resp.Header.Get("Location") != "" {
		t.Errorf("a refusal's page:\n%s", main)
	}

	// The request: 303 to the run panel, a run in the store and the file the updater looks for.
	resp, body = e.postForm(t, "/ops/updates/caravana", url.Values{"version": {"0.16.0"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/updates#run" {
		t.Fatalf("POST 0.16.0 = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	runs, err := e.Store.UpdateRuns(context.Background(), 5)
	if err != nil || len(runs) != 1 || runs[0].State != store.UpdateRequested || runs[0].Trigger != store.UpdateByConsole ||
		runs[0].Version != "0.16.0" || runs[0].FromVersion != "0.15.0" || runs[0].TokenName != "" {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	// The page now says it is waiting, polls, and the confirmation page refuses a second one.
	_, page := e.get(t, "/ops/updates")
	panel, words := runPanel(t, page)
	if !strings.Contains(words, "Waiting for the server's updater to start the update to 0.16.0.") || !strings.Contains(panel, "every 5s") {
		t.Errorf("the page after the request:\n%s", words)
	}
	resp, body = e.postForm(t, "/ops/updates/caravana", url.Values{"version": {"0.16.0"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "An update to 0.16.0 is already under way.") {
		t.Errorf("a second POST = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, body = e.get(t, "/ops/updates/caravana?version=0.16.0")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "An update to 0.16.0 is already under way.") {
		t.Errorf("GET while a run is open = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestUpdateConfirmRefusalsWithoutUpdaterOrOnDevBuild(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean"})
	releaseFound(t, e)
	resp, body := e.postForm(t, "/ops/updates/caravana", url.Values{"version": {"0.16.0"}})
	want := "This server's updater isn't installed. Run the installer of 0.16.0 once on the server; it adds the updater, and later updates can start here."
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), want) {
		t.Errorf("POST without the updater = %d:\n%s", resp.StatusCode, text(body))
	}
	if runs, _ := e.Store.UpdateRuns(context.Background(), 5); len(runs) != 0 {
		t.Errorf("a refused request left runs: %+v", runs)
	}

	dev, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true, version: "dev"})
	resp, body = dev.postForm(t, "/ops/updates/caravana", url.Values{"version": {"0.16.0"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This is a development build, which has no release to compare with.") {
		t.Errorf("POST on a development build = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestAutomaticCaravanaUpdatesRow(t *testing.T) {
	hour3 := `<option value="3" selected>03:00 UTC</option>`

	// On, with a patch release waiting: the next request's time.
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true, version: "0.16.0", tag: "v0.16.1"})
	releaseFound(t, e)
	_, body := e.get(t, "/ops/updates")
	row := regionBetween(t, body, "Automatic caravana updates", `aria-labelledby="up-past-title"`)
	words := text(row)
	for _, want := range []string{
		"Automatic caravana updates Only patch releases (0.16.x), which fix without changing how things work. A newer minor release waits for you, and a version that failed is never tried again by itself.",
		"Install patch releases by themselves at",
		"off",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("the row lacks %q:\n%s", want, words)
		}
	}
	if !strings.Contains(row, hour3) || strings.Count(row, "<option") != 24 || !strings.Contains(row, `<option value="23">23:00 UTC</option>`) ||
		!strings.Contains(row, `<option value="0">00:00 UTC</option>`) {
		t.Errorf("the hour select:\n%s", row)
	}
	if strings.Contains(row, "disabled") || !strings.Contains(body, `<input type="hidden" name="autoForm" value="1">`) {
		t.Errorf("an installed updater's controls are disabled, or the form has no marker")
	}
	if !strings.Contains(row, `class="cv-select"`) || !strings.Contains(row, `name="autoUpdate" value="1" form="up-settings"`) {
		t.Errorf("the controls:\n%s", row)
	}

	// Saving turns it on at 07:00 UTC; one Save for both settings, the same 303.
	resp, _ := e.postForm(t, "/ops/updates/settings", url.Values{"checkEnabled": {"1"}, "autoForm": {"1"}, "autoUpdate": {"1"}, "autoUpdateHour": {"7"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/updates?saved=1#how" {
		t.Fatalf("POST settings = %d to %q", resp.StatusCode, location(t, resp))
	}
	v, _ := e.Svc.Updates(context.Background())
	if !v.Caravana.Auto.Enabled || v.Caravana.Auto.Hour != 7 || !v.Caravana.CheckEnabled {
		t.Fatalf("auto = %+v, check = %v", v.Caravana.Auto, v.Caravana.CheckEnabled)
	}
	_, body = e.get(t, "/ops/updates")
	row = regionBetween(t, body, "Automatic caravana updates", `aria-labelledby="up-past-title"`)
	if !strings.Contains(row, `<option value="7" selected>07:00 UTC</option>`) || !strings.Contains(row, `name="autoUpdate" value="1" form="up-settings" checked`) {
		t.Errorf("the saved row:\n%s", row)
	}
	if !regexp.MustCompile(`next: <time[^>]*>[A-Z][a-z]{2} \d+, 07:00 UTC</time>`).MatchString(row) {
		t.Errorf("the line under the row has no next time:\n%s", row)
	}

	// The form's marker: an unchecked box is off; a post without the marker leaves the setting alone; a
	// bad hour is a 422 with a sentence and nothing is stored.
	e.postForm(t, "/ops/updates/settings", url.Values{"checkEnabled": {"1"}, "autoForm": {"1"}, "autoUpdateHour": {"7"}})
	if v, _ := e.Svc.Updates(context.Background()); v.Caravana.Auto.Enabled {
		t.Error("an unchecked box didn't turn the setting off")
	}
	e.postForm(t, "/ops/updates/settings", url.Values{"checkEnabled": {"1"}, "autoForm": {"1"}, "autoUpdate": {"1"}, "autoUpdateHour": {"7"}})
	e.postForm(t, "/ops/updates/settings", url.Values{"checkEnabled": {"1"}})
	if v, _ := e.Svc.Updates(context.Background()); !v.Caravana.Auto.Enabled || v.Caravana.Auto.Hour != 7 {
		t.Errorf("a post without the marker changed the automatic setting: %+v", v.Caravana.Auto)
	}
	resp, body = e.postForm(t, "/ops/updates/settings", url.Values{"checkEnabled": {"1"}, "autoForm": {"1"}, "autoUpdate": {"1"}, "autoUpdateHour": {"25"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Choose an hour from 00:00 UTC to 23:00 UTC.") {
		t.Errorf("POST hour 25 = %d:\n%s", resp.StatusCode, text(body))
	}
	if v, _ := e.Svc.Updates(context.Background()); v.Caravana.Auto.Hour != 7 {
		t.Errorf("a bad hour was stored: %+v", v.Caravana.Auto)
	}

	// Off with the check off: the line says what it needs.
	e.postForm(t, "/ops/updates/settings", url.Values{"autoForm": {"1"}, "autoUpdate": {"1"}, "autoUpdateHour": {"3"}})
	_, body = e.get(t, "/ops/updates")
	row = regionBetween(t, body, "Automatic caravana updates", `aria-labelledby="up-past-title"`)
	if !strings.Contains(text(row), "It needs the release check: turn on Check GitHub for new releases.") {
		t.Errorf("the check off:\n%s", text(row))
	}

	// Without the updater: both controls are disabled, no marker is posted, and the line says why.
	e2, _ := updEnv(t, updOpts{fixture: "ubuntu-clean"})
	_, body = e2.get(t, "/ops/updates")
	row = regionBetween(t, body, "Automatic caravana updates", `aria-labelledby="up-past-title"`)
	if strings.Count(row, " disabled") != 2 || strings.Contains(body, `name="autoForm"`) ||
		!strings.Contains(text(row), "It needs the server's updater: run the installer once on the server.") {
		t.Errorf("without the updater:\n%s", row)
	}
	e2.postForm(t, "/ops/updates/settings", url.Values{"checkEnabled": {"1"}}) // what a disabled form posts
	if v, _ := e2.Svc.Updates(context.Background()); v.Caravana.Auto.Enabled {
		t.Error("a form with disabled controls turned the setting on")
	}

	// A development build says so.
	dev, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true, version: "dev"})
	_, body = dev.get(t, "/ops/updates")
	row = regionBetween(t, body, "Automatic caravana updates", `aria-labelledby="up-past-title"`)
	if !strings.Contains(text(row), "This is a development build.") {
		t.Errorf("a development build:\n%s", text(row))
	}
}

func TestPastUpdatesDrawsRuns(t *testing.T) {
	now := time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC)
	for state, mark := range map[string]string{store.UpdateUpdated: "healthy", store.UpdateFailed: "failed",
		store.UpdateRolledBack: "warning", store.UpdateRefused: "warning", store.UpdateLost: "warning"} {
		rows := upPastOf(now, []service.UpdateEvent{{At: now.Add(-time.Hour), Kind: "update", State: state,
			Text: "caravana 0.16.0 → 0.17.0", Detail: "from the console · 1 min 40 s"}})
		if len(rows) != 1 || rows[0].Mark != mark || rows[0].Text.String() != "caravana 0.16.0 → 0.17.0" {
			t.Errorf("%s: rows = %+v, want mark %s", state, rows, mark)
		}
	}

	// On the page, the rows are the service's: the detail words of the contract.
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true})
	releaseFound(t, e)
	putRun(t, e, store.UpdateRolledBack, func(r *store.UpdateRun) {
		r.Reason = "caravana 0.16.0 didn't answer its health check within 60 s; 0.15.0 runs again"
		r.RequestedAt = time.Now().Add(-48 * time.Hour)
		r.StartedAt = r.RequestedAt.Add(time.Minute)
		r.FinishedAt = r.StartedAt.Add(2 * time.Minute)
	})
	_, body := e.get(t, "/ops/updates")
	past := regionBetween(t, body, `id="up-past"`, "</section>")
	if !strings.Contains(text(past), "caravana 0.15.0 → 0.16.0 rolled back · caravana 0.16.0 didn't answer its health check within 60 s…") ||
		!strings.Contains(past, `cv-mark cv-mark-warning`) {
		t.Errorf("Past updates:\n%s", text(past))
	}
	if !strings.Contains(past, `<span class="cv-inline-mono text-fg-1 cv-nowrap">0.15.0 → 0.16.0</span>`) {
		t.Error("the versions of a run's row aren't one token")
	}
}

// The run says why it ended; the page doesn't draw the needs-you banner for it as well. On /ops and the
// Developer home it shows like every item.
func TestUpdatesPageHasNoBannerForAFailedUpdate(t *testing.T) {
	e, _ := updEnv(t, updOpts{fixture: "ubuntu-clean", updater: true})
	releaseFound(t, e)
	putRun(t, e, store.UpdateRolledBack, func(r *store.UpdateRun) { r.Reason, r.Log = "It didn't answer.", runLog })
	att, err := e.Svc.Attention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range att.Items {
		found = found || it.Kind == service.AttentionCaravanaUpdateFailed
	}
	if !found {
		t.Fatal("the rolled-back run has no needs-you item, so this test checks nothing")
	}
	_, body := e.get(t, "/ops/updates")
	if got := bannersOf(body); len(got) != 0 {
		t.Errorf("the Updates page drew banners for the failed update: %q", got)
	}
	if n := strings.Count(text(body), "didn't start, so 0.15.0 runs again"); n != 1 {
		t.Errorf("the failed update is said %d times, want once (the run panel)", n)
	}
	_, ops := e.get(t, "/ops")
	if !strings.Contains(text(ops), "caravana 0.16.0 didn't start, so 0.15.0 runs again.") {
		t.Errorf("/ops doesn't list the needs-you item:\n%s", text(ops))
	}
}
