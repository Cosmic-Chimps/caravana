package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 042, the console: server labels, moving work off a server and Accept work again. The cluster is
// threeServers (hel-1 the first, fsn-1 and fsn-2); the move's waits are shortened as the service's tests do.

// runningOn is a running copy of the job's group on a node.
func runningOn(id, group, nodeID, nodeName string) api.AllocationListStub {
	return api.AllocationListStub{ID: id, TaskGroup: group, JobVersion: 1, ClientStatus: "running", DesiredStatus: "run",
		NodeID: nodeID, NodeName: nodeName, CreateIndex: 1, TaskStates: map[string]*api.TaskState{group: {State: "running"}}}
}

// movableCluster is threeServers with something on fsn-1: two copies of blog's web (it can move), a service with
// a volume and Traefik (which stay).
func movableCluster(t *testing.T) *consoleEnv {
	t.Helper()
	e := threeServers(t)
	e.Svc.SetMoveTimingForTests(10*time.Millisecond, 3*time.Second, 5*time.Millisecond)
	e.Nomad.MoveCopiesTo("node-3", "fsn-2")
	blog := blogApp()
	e.seed(t, blog, seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.Nomad.SetCopies("blog", runningOn("blog-web-0", "web", "node-2", "fsn-1"), runningOn("blog-web-1", "web", "node-2", "fsn-1"),
		runningOn("blog-worker-0", "worker", "node-1", "hel-1"))
	db := app.App{ID: "db", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{Name: "pg", Image: "postgres:17",
		Volumes: []app.Volume{{Name: "data", Path: "/var/lib/postgresql/data"}}}}}
	e.seed(t, db, seedDeploy{ID: "d2", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	e.Nomad.SetCopies("db", runningOn("db-pg-0", "pg", "node-2", "fsn-1"))
	e.Nomad.SetTraefikJob("traefik:v3")
	e.Nomad.SetCopies("traefik", runningOn("tf-0", "traefik", "node-2", "fsn-1"))
	return e
}

// waitServerMove waits until the server's move is in this state.
func (e *consoleEnv) waitServerMove(t *testing.T, name, state string) service.ServerView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		v, err := e.Svc.Server(context.Background(), name)
		if err != nil {
			t.Fatalf("Server(%s): %v", name, err)
		}
		if v.Move != nil && v.Move.State == state {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("the move of %s is %+v, want %s", name, v.Move, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestParsePairs(t *testing.T) {
	for _, c := range []struct {
		in   string
		want map[string]string
		err  string
	}{
		{"", map[string]string{}, ""},
		{"ssd=true, region=eu", map[string]string{"ssd": "true", "region": "eu"}, ""},
		{"ssd", map[string]string{"ssd": "true"}, ""},
		{" ssd = true ,\nregion=eu\n", map[string]string{"ssd": "true", "region": "eu"}, ""},
		{"gpu=a=b", map[string]string{"gpu": "a=b"}, ""},
		{"=true", nil, "no name"},
		{"ssd, ssd=false", nil, "ssd is written twice"},
	} {
		got, err := parsePairs(c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("parsePairs(%q) error = %v, want one with %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || len(got) != len(c.want) {
			t.Errorf("parsePairs(%q) = %v, %v", c.in, got, err)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("parsePairs(%q)[%s] = %q, want %q", c.in, k, got[k], v)
			}
		}
	}
}

func TestServerPageLabels(t *testing.T) {
	e := threeServers(t)
	_, body := e.get(t, "/ops/servers/fsn-1")
	f := formWith(t, body, "/ops/servers/fsn-1/labels")
	if got := f.Values.Get("labels"); got != "" {
		t.Errorf("the input of a server with no labels holds %q", got)
	}
	if !strings.Contains(text(body), "labels none") {
		t.Errorf("the Labels row of a server with none:\n%s", text(body))
	}
	if !strings.Contains(body, `id="srv-labels-form"`) || !strings.Contains(body, `hx-preserve="true"`) {
		t.Error("the labels form isn't kept across the body's refresh")
	}

	// A bare word is =true; the answer is the page again (303).
	resp, out := e.submit(t, f, url.Values{"labels": {"region=eu, ssd"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/servers/fsn-1" {
		t.Fatalf("POST = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(out))
	}
	v, err := e.Svc.Server(context.Background(), "fsn-1")
	if err != nil || v.Labels["ssd"] != "true" || v.Labels["region"] != "eu" || len(v.Labels) != 2 {
		t.Fatalf("labels = %v, %v", v.Labels, err)
	}
	_, body = e.get(t, "/ops/servers/fsn-1")
	for _, want := range []string{`<li class="cv-slrole">region=eu</li>`, `<li class="cv-slrole">ssd=true</li>`, `value="region=eu, ssd=true"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q:\n%s", want, body)
		}
	}
	// The part draws the same row.
	_, part := e.get(t, "/parts/ops/servers/fsn-1")
	if !strings.Contains(part, `<li class="cv-slrole">ssd=true</li>`) {
		t.Errorf("the part lacks the label:\n%s", part)
	}

	// An empty input takes every label off.
	if resp, _ := e.submit(t, formWith(t, body, "/ops/servers/fsn-1/labels"), url.Values{"labels": {""}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("clearing = %d", resp.StatusCode)
	}
	if v, _ := e.Svc.Server(context.Background(), "fsn-1"); len(v.Labels) != 0 {
		t.Errorf("labels after clearing = %v", v.Labels)
	}
}

func TestServerLabelsRefusalsAreDrawnAtTheForm(t *testing.T) {
	e := threeServers(t)
	for _, c := range []struct {
		name, typed, want string
		status            int
	}{
		{"an upper-case name", "SSD=true", "Label name", http.StatusUnprocessableEntity},
		{"a bad value", "ssd=no way", "ssd: ", http.StatusUnprocessableEntity},
		{"no name", "=true", "has no name", http.StatusUnprocessableEntity},
		{"a name twice", "ssd, ssd=false", "Ssd is written twice", http.StatusUnprocessableEntity},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp, body := e.postForm(t, "/ops/servers/fsn-1/labels", url.Values{"labels": {c.typed}})
			if resp.StatusCode != c.status {
				t.Fatalf("POST = %d, want %d:\n%s", resp.StatusCode, c.status, text(body))
			}
			if !strings.Contains(text(body), c.want) || !strings.Contains(body, `id="srv-labels-error"`) || !strings.Contains(body, `aria-invalid="true"`) {
				t.Errorf("no sentence at the form:\n%s", text(body))
			}
			if !strings.Contains(body, `value="`+c.typed+`"`) {
				t.Errorf("what was typed was lost:\n%s", body)
			}
			if v, _ := e.Svc.Server(context.Background(), "fsn-1"); len(v.Labels) != 0 {
				t.Errorf("a refused post set labels %v", v.Labels)
			}
		})
	}
	if resp, _ := e.postForm(t, "/ops/servers/nobody/labels", url.Values{"labels": {"ssd"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown server = %d, want 404", resp.StatusCode)
	}
}

// A server whose Nomad node hasn't shown the labels says so quietly.
func TestServerLabelsNoteWhileNomadHasntTakenThem(t *testing.T) {
	l := labelsFormOf(service.ServerView{Name: "fsn-1", State: "ready", Labels: map[string]string{"ssd": "true"}}, nil, nil)
	if len(l.Note) == 0 || !strings.Contains(plainOf(sentenceParts(l.Note)), "Nomad hasn't taken them yet") {
		t.Errorf("note = %v", l.Note)
	}
	l = labelsFormOf(service.ServerView{Name: "fsn-1", State: "ready", LabelsApplied: true}, nil, nil)
	if len(l.Note) != 0 {
		t.Errorf("applied labels have a note: %v", l.Note)
	}
	l = labelsFormOf(service.ServerView{Name: "fsn-1", State: "down"}, nil, nil)
	if len(l.Note) != 0 {
		t.Errorf("a down server has a note: %v", l.Note)
	}
	typed := "x"
	if l = labelsFormOf(service.ServerView{Name: "fsn-1", LabelsApplied: true, Labels: map[string]string{"a": "b"}}, &typed, nil); l.Text != "x" {
		t.Errorf("typed text = %q", l.Text)
	}
}

func sentenceParts(s sentence) []service.TextPart {
	out := make([]service.TextPart, len(s))
	for i, p := range s {
		out[i] = service.TextPart{Text: p.Text, Mono: p.Mono}
	}
	return out
}

func TestServerHeadActions(t *testing.T) {
	e := threeServers(t)
	_, body := e.get(t, "/ops/servers/fsn-1")
	if !strings.Contains(body, `href="/ops/servers/fsn-1/move"`) || !strings.Contains(text(body), "Move work off…") || strings.Contains(text(body), "Accept work again") {
		t.Errorf("a server that takes work:\n%s", text(body))
	}
	// A server that is down has neither, and keeps Remove.
	e.Nomad.SetNodeStatusOf("node-3", "down")
	_, body = e.get(t, "/ops/servers/fsn-2")
	if strings.Contains(text(body), "Move work off") || strings.Contains(text(body), "Accept work again") || !strings.Contains(text(body), "Remove server…") {
		t.Errorf("a down server's head:\n%s", text(body))
	}
	// A ready server that takes no work offers to accept it again.
	e.Nomad.SetNodeEligibilityOf("node-2", "ineligible", false)
	_, body = e.get(t, "/ops/servers/fsn-1")
	f := formWith(t, body, "/ops/servers/fsn-1/accept")
	if !strings.Contains(text(body), "Accept work again") || strings.Contains(text(body), "Move work off") {
		t.Errorf("an ineligible server:\n%s", text(body))
	}
	resp, _ := e.submit(t, f, nil)
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/servers/fsn-1" {
		t.Errorf("accept = %d to %q", resp.StatusCode, location(t, resp))
	}
	if v, _ := e.Svc.Server(context.Background(), "fsn-1"); !v.AcceptsWork {
		t.Error("the server takes no work after Accept work again")
	}
}

// The server Caravana runs on has the same actions.
func TestFirstServerHeadActions(t *testing.T) {
	e := sampledServersEnv(t)
	e.readyServer(t, "fsn-1")
	_, body := e.get(t, "/ops/servers/hel-1")
	if !strings.Contains(body, `href="/ops/servers/hel-1/move"`) || !strings.Contains(body, `action="/ops/servers/hel-1/labels"`) {
		t.Errorf("the first server lacks Move work off or labels:\n%s", text(body))
	}
}

func TestMoveConfirmation(t *testing.T) {
	e := movableCluster(t)
	resp, body := e.get(t, "/ops/servers/fsn-1/move")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d:\n%s", resp.StatusCode, text(body))
	}
	words := text(body)
	for _, want := range []string{"Move work off fsn-1?", "What moves", "blog/web 2 copies", "A service with one copy is unreachable while its new copy starts",
		"What stays", "db/pg its data is on this server", "traefik the proxy runs here", "Give up after", "Move work off fsn-1"} {
		if !strings.Contains(words, want) {
			t.Errorf("the confirmation lacks %q:\n%s", want, words)
		}
	}
	if !strings.Contains(body, `<option value="15" selected>15 minutes</option>`) {
		t.Errorf("15 minutes isn't selected:\n%s", body)
	}
	for _, m := range []string{"5", "30", "60"} {
		if !strings.Contains(body, `<option value="`+m+`">`) {
			t.Errorf("the select lacks %s:\n%s", m, body)
		}
	}
	formWith(t, body, "/ops/servers/fsn-1/move")
	// The server's own breadcrumb leads back.
	if !strings.Contains(body, `href="/ops/servers/fsn-1"`) {
		t.Error("no way back")
	}
}

func TestMoveConfirmationRefusals(t *testing.T) {
	// One server: nowhere to move to.
	e := serversEnv(t)
	resp, body := e.get(t, "/ops/servers/hel-1/move")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "No other server accepts work: there is nowhere to move to.") || strings.Contains(body, `action="/ops/servers/hel-1/move"`) {
		t.Errorf("one server = %d:\n%s", resp.StatusCode, text(body))
	}
	if resp, _ := e.get(t, "/ops/servers/nobody/move"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown server = %d, want 404", resp.StatusCode)
	}
	// A server that is down isn't ready.
	e = threeServers(t)
	e.Nomad.SetNodeStatusOf("node-3", "down")
	resp, body = e.get(t, "/ops/servers/fsn-2/move")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This server isn't ready") {
		t.Errorf("a down server = %d:\n%s", resp.StatusCode, text(body))
	}
	// A refused post draws the confirmation again with the reason.
	resp, body = e.postForm(t, "/ops/servers/fsn-2/move", url.Values{"deadline": {"15"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This server isn't ready") {
		t.Errorf("post to a down server = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestMovePostRejectsADeadlineTheSelectCantSend(t *testing.T) {
	e := movableCluster(t)
	resp, body := e.postForm(t, "/ops/servers/fsn-1/move", url.Values{"deadline": {"7"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "Choose a deadline from the list.") ||
		!strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, `action="/ops/servers/fsn-1/move"`) {
		t.Errorf("POST = %d:\n%s", resp.StatusCode, text(body))
	}
	if v, _ := e.Svc.Server(context.Background(), "fsn-1"); v.Move != nil || !v.AcceptsWork {
		t.Errorf("a refused post started something: %+v", v.Move)
	}
}

// A move from the console, to its end: the panel follows it, and the server stays out of work until it is
// told to accept it again.
func TestMoveWorkOffFromTheConsole(t *testing.T) {
	e := movableCluster(t)
	resp, body := e.postForm(t, "/ops/servers/fsn-1/move", url.Values{"deadline": {"15"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/ops/servers/fsn-1" {
		t.Fatalf("POST = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	e.waitServerMove(t, "fsn-1", store.MoveDone)

	_, page := e.get(t, "/ops/servers/fsn-1")
	words := text(page)
	for _, want := range []string{"Moved 2 copies in", "db/pg its data is on this server", "traefik the proxy runs here",
		"This server takes no new work.", "Accept work again"} {
		if !strings.Contains(words, want) {
			t.Errorf("the page after the move lacks %q:\n%s", want, words)
		}
	}
	// The panel is the body's first panel, so the stream's refresh redraws it.
	if i, j := strings.Index(page, `id="srv-move-title"`), strings.Index(page, `class="cv-srvgrid"`); i < 0 || j < 0 || i > j {
		t.Errorf("the move panel isn't before the body's grid (%d, %d)", i, j)
	}
	_, part := e.get(t, "/parts/ops/servers/fsn-1")
	if !strings.Contains(part, `id="srv-move-title"`) {
		t.Error("the part lacks the move panel")
	}
	if strings.Contains(words, "Move work off…") {
		t.Error("a server that doesn't take work offers to move work off")
	}

	// Accept work again: the server takes work, and the head offers the move again.
	resp, _ = e.submit(t, formWith(t, page, "/ops/servers/fsn-1/accept"), nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("accept = %d", resp.StatusCode)
	}
	_, page = e.get(t, "/ops/servers/fsn-1")
	if strings.Contains(text(page), "Accept work again") || !strings.Contains(text(page), "Move work off…") {
		t.Errorf("after accepting:\n%s", text(page))
	}
}

// While a move goes the state says so, the panel has the progress and Cancel, and the head has no action;
// Cancel is the accept route and the panel says it was cancelled.
func TestMoveInProgressAndCancel(t *testing.T) {
	e := movableCluster(t)
	e.Nomad.FailAllocStops(http.StatusInternalServerError) // Nomad won't move them: the move goes on
	if resp, _ := e.postForm(t, "/ops/servers/fsn-1/move", url.Values{"deadline": {"30"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST = %d", resp.StatusCode)
	}
	_, page := e.get(t, "/ops/servers/fsn-1")
	words := text(page)
	for _, want := range []string{"moving work off", "Moving work off · 0 of 2 moved · until", "UTC", "Cancel"} {
		if !strings.Contains(words, want) {
			t.Errorf("the page during the move lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(words, "Move work off…") || strings.Contains(words, "Accept work again") {
		t.Errorf("the head has an action during the move:\n%s", words)
	}
	f := formWith(t, page, "/ops/servers/fsn-1/accept")
	resp, _ := e.submit(t, f, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("cancel = %d", resp.StatusCode)
	}
	e.waitServerMove(t, "fsn-1", store.MoveCancelled)
	_, page = e.get(t, "/ops/servers/fsn-1")
	if !strings.Contains(text(page), "The move was cancelled") || strings.Contains(text(page), "Accept work again") {
		t.Errorf("after the cancel:\n%s", text(page))
	}
}

func TestMoveIncompleteSaysWhatIsLeft(t *testing.T) {
	e := movableCluster(t)
	e.Nomad.FailAllocStops(http.StatusInternalServerError)
	if _, err := e.Svc.MoveWorkOff(context.Background(), "fsn-1", 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	e.waitServerMove(t, "fsn-1", store.MoveIncomplete)
	_, page := e.get(t, "/ops/servers/fsn-1")
	words := text(page)
	for _, want := range []string{"The move didn't finish", "2 copies still run here", "had nowhere to go", "Accept work again"} {
		if !strings.Contains(words, want) {
			t.Errorf("the incomplete move lacks %q:\n%s", want, words)
		}
	}
	// The needs-you list has the item with its link to the server.
	att, err := e.Svc.Attention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range att.Items {
		if it.Kind == service.AttentionServerMoveIncomplete {
			row := e.Server.attentionRow(time.Now(), it, places{}, nil)
			if row.Href != "/ops/servers/fsn-1" || row.Action != "Open server" || row.Mark != "warning" {
				t.Errorf("row = %+v", row)
			}
			return
		}
	}
	t.Errorf("no server_move_incomplete item: %+v", att.Items)
}

func TestMovePanelIsHistoryAnHourAfterAnAcceptedMove(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	v := service.ServerView{Name: "fsn-1", Managed: true, State: "ready", AcceptsWork: true, Move: &service.MoveView{
		State: store.MoveDone, StartedAt: now.Add(-3 * time.Hour), EndedAt: now.Add(-3 * time.Hour).Add(time.Minute), Moved: 2}}
	if moveOf(now, v, places{}) != nil {
		t.Error("an old move of a server that takes work is still drawn")
	}
	v.Move.EndedAt = now.Add(-10 * time.Minute)
	if m := moveOf(now, v, places{}); m == nil || m.Accept != "" || m.Cancel != "" {
		t.Errorf("a recent move that was accepted: %+v", m)
	}
	v.AcceptsWork = false
	v.Move.EndedAt = now.Add(-5 * time.Hour)
	if m := moveOf(now, v, places{}); m == nil || m.Accept != "/ops/servers/fsn-1/accept" {
		t.Errorf("an old move of a server that takes no work has no way back: %+v", m)
	}
	if got := elapsedWords(2*time.Minute + 20*time.Second); got != "2 minutes" {
		t.Errorf("elapsedWords = %q", got)
	}
	if got := elapsedWords(40 * time.Second); got != "40 seconds" {
		t.Errorf("elapsedWords = %q", got)
	}
}

func TestServersListShowsLabelsAndMovingWork(t *testing.T) {
	e := movableCluster(t)
	if _, err := e.Svc.SetServerLabels(context.Background(), "fsn-1", map[string]string{"ssd": "true"}); err != nil {
		t.Fatal(err)
	}
	_, body := e.get(t, "/ops/servers")
	if r := rowOfServer(t, body, "fsn-1"); !strings.Contains(r, `<span class="cv-slrole">ssd=true</span>`) {
		t.Errorf("the row lacks the label:\n%s", r)
	}
	if r := rowOfServer(t, body, "fsn-2"); strings.Contains(r, "cv-sllabels") {
		t.Errorf("a server with no label has a labels line:\n%s", r)
	}
	e.Nomad.FailAllocStops(http.StatusInternalServerError)
	if _, err := e.Svc.MoveWorkOff(context.Background(), "fsn-1", 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	_, body = e.get(t, "/ops/servers")
	if r := rowOfServer(t, body, "fsn-1"); !strings.Contains(text(r), "moving work off") || !strings.Contains(r, "cv-pill-rolling") {
		t.Errorf("the row of a server being moved off:\n%s", r)
	}
	_, page := e.get(t, "/ops/servers/fsn-1")
	if !strings.Contains(page, `cv-pill cv-pill-rolling`) || !strings.Contains(text(page), "moving work off") {
		t.Errorf("the head's state:\n%s", text(page))
	}
}

func TestAcceptRefusals(t *testing.T) {
	e := threeServers(t)
	if resp, _ := e.postForm(t, "/ops/servers/nobody/accept", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown server = %d, want 404", resp.StatusCode)
	}
	e.Nomad.SetNodeStatusOf("node-3", "down")
	resp, body := e.postForm(t, "/ops/servers/fsn-2/accept", nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "This server isn't ready") {
		t.Errorf("a down server = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestAttentionRowsOfPlacementKinds(t *testing.T) {
	s := newConsoleEnv(t).Server
	now := time.Now()
	row := s.attentionRow(now, service.AttentionItem{Kind: service.AttentionCopiesUnplaced, AppID: "blog", Service: "web", Queued: 2, When: now}, places{}, nil)
	if row.Action != "Open placement" || row.Href != "/apps/blog/configure/services#placement-web" || row.Mark != "failed" {
		t.Errorf("copies_unplaced row = %+v", row)
	}
	if got := headlineWords(service.AttentionItem{Kind: service.AttentionCopiesUnplaced}); strings.Contains(got, "needs you (") {
		t.Errorf("headline = %q", got)
	}
}
