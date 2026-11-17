package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func TestPlacementItemsOf(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	stopped := now
	placed := func(id string, stoppedAt *time.Time) store.AppRecord {
		a := appWith(id, 2, 1)
		a.Services[0].Placement = &app.Placement{Require: map[string]string{"ssd": "true"}}
		return store.AppRecord{App: a, StoppedAt: stoppedAt, Place: app.Place{Project: "default", Environment: "production", Name: id}}
	}
	plain := store.AppRecord{App: appWith("plain", 1)}

	t.Run("one item per service with rules and queued copies, in words", func(t *testing.T) {
		items := placementItemsOf(now, []store.AppRecord{placed("blog", nil), plain},
			map[string]map[string]int{"blog": {"web": 2, "worker": 1}, "plain": {"web": 3}})
		if len(items) != 1 {
			t.Fatalf("items = %+v, want one: worker has no rules and plain is only short of room", items)
		}
		it := items[0]
		if it.Kind != AttentionCopiesUnplaced || it.AppID != "blog" || it.Service != "web" || it.Queued != 2 || it.Level != "failed" {
			t.Errorf("item = %+v", it)
		}
		if want := "blog/web: 2 copies can't be placed: no server that accepts work matches its placement rules, or none has room."; it.Detail != want {
			t.Errorf("detail = %q, want %q", it.Detail, want)
		}
		if key := problemKey(it); key != "copies_unplaced|blog|web|" {
			t.Errorf("key = %q", key)
		}
		if got := problemPath(it.Kind, it.AppID, problemSubject(it)); got != "/apps/blog" {
			t.Errorf("path = %q", got)
		}
	})

	t.Run("one copy", func(t *testing.T) {
		items := placementItemsOf(now, []store.AppRecord{placed("blog", nil)}, map[string]map[string]int{"blog": {"web": 1}})
		if len(items) != 1 || !strings.Contains(items[0].Detail, ": 1 copy can't be placed:") {
			t.Errorf("items = %+v", items)
		}
	})

	t.Run("a stopped app wants no copies", func(t *testing.T) {
		if items := placementItemsOf(now, []store.AppRecord{placed("blog", &stopped)}, map[string]map[string]int{"blog": {"web": 2}}); len(items) != 0 {
			t.Errorf("items = %+v", items)
		}
	})

	t.Run("nothing queued, nothing said", func(t *testing.T) {
		if items := placementItemsOf(now, []store.AppRecord{placed("blog", nil)}, nil); len(items) != 0 {
			t.Errorf("items = %+v", items)
		}
	})
}

func TestAttentionHasCopiesUnplaced(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := appWith("blog", 2)
	a.Services[0].Placement = &app.Placement{Require: map[string]string{"gpu": "true"}}
	if _, err := e.store.InsertApp(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	e.nomad.AddForeignJob(&api.Job{ID: new("blog"), Name: new("blog")})

	att, err := e.svc.Attention(ctx)
	if err != nil || len(att.Items) != 0 || att.CopiesUnknown {
		t.Fatalf("with nothing queued: %+v, %v", att, err)
	}
	e.nomad.SetQueued("blog", "web", 2)
	att, err = e.svc.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(att.Items) != 1 || att.Items[0].Kind != AttentionCopiesUnplaced || att.Items[0].Queued != 2 {
		t.Fatalf("items = %+v, want copies_unplaced with 2 queued", att.Items)
	}
	// Nomad can't be read: the kind is not judged, and the list says so.
	e.nomad.Down()
	if items, known := e.svc.placementItems(ctx, []store.AppRecord{{App: a}}, time.Now()); known || len(items) != 0 {
		t.Errorf("with Nomad down: %+v, known %v, want none and unknown", items, known)
	}
	e.nomad.Up()
}

// An app with no placement never makes the list ask Nomad for queued copies.
func TestPlacementItemsAskNothingWithoutRules(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.nomad.Down() // any call would fail
	if items, known := e.svc.placementItems(ctx, []store.AppRecord{{App: appWith("plain", 1)}}, time.Now()); !known || len(items) != 0 {
		t.Errorf("= %+v, known %v, want none and known", items, known)
	}
}

func TestServerMoveIncompleteItem(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	row := store.Server{Index: 2, Name: "two", State: store.ServerReady, SeenAt: now}
	node := func(eligible bool, status string) []deploy.Node {
		return []deploy.Node{{ID: "n2", Status: status, Eligible: eligible, AppAddress: "10.99.0.2"}}
	}
	move := func(state string, left int) map[int]store.ServerMove {
		return map[int]store.ServerMove{2: {State: state, Left: left, EndedAt: now.Add(-time.Minute)}}
	}
	for _, c := range []struct {
		name  string
		nodes []deploy.Node
		nomad bool
		moves map[int]store.ServerMove
		want  int
	}{
		{"incomplete and still not taking work", node(false, "ready"), true, move(store.MoveIncomplete, 2), 1},
		{"the server takes work again", node(true, "ready"), true, move(store.MoveIncomplete, 2), 0},
		{"a done move", node(false, "ready"), true, move(store.MoveDone, 0), 0},
		{"a cancelled move", node(false, "ready"), true, move(store.MoveCancelled, 1), 0},
		{"a move still going", node(false, "ready"), true, move(store.MoveMoving, 1), 0},
		{"nothing left to say", node(false, "ready"), true, move(store.MoveIncomplete, 0), 0},
		{"Nomad can't be read: not judged", nil, false, move(store.MoveIncomplete, 2), 0},
		{"the node is down: that is its own item", node(false, "down"), true, move(store.MoveIncomplete, 2), 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			n := 0
			for _, it := range serverItemsOf(now, []store.Server{row}, c.nodes, c.nomad, c.moves) {
				if it.Kind != AttentionServerMoveIncomplete {
					continue
				}
				n++
				if it.Server != "two" || it.Left != 2 || it.Level != "warning" {
					t.Errorf("item = %+v", it)
				}
				if want := "two: the move-off passed its deadline; 2 copies still run there."; it.Detail != want {
					t.Errorf("detail = %q, want %q", it.Detail, want)
				}
				if key := problemKey(it); key != "server_move_incomplete|||two" {
					t.Errorf("key = %q", key)
				}
				if got := problemPath(it.Kind, it.AppID, problemSubject(it)); got != "/ops/servers/two" {
					t.Errorf("path = %q", got)
				}
			}
			if n != c.want {
				t.Errorf("%d move items, want %d", n, c.want)
			}
		})
	}
	// One copy left reads in the singular.
	items := serverItemsOf(now, []store.Server{row}, node(false, "ready"), true, move(store.MoveIncomplete, 1))
	if len(items) != 1 || items[0].Detail != "two: the move-off passed its deadline; 1 copy still runs there." {
		t.Errorf("items = %+v", items)
	}
}

// Both kinds are judged with what they come from: copies_unplaced with the copy kinds, the move item with
// the servers; and each has a title and a link in the notification.
func TestPlacementKindsAreJudgedAndNotified(t *testing.T) {
	if !notJudged(AttentionCopiesUnplaced, judged{Servers: true}) || notJudged(AttentionCopiesUnplaced, judged{Copies: true}) {
		t.Error("copies_unplaced must follow the copy kinds")
	}
	if !notJudged(AttentionServerMoveIncomplete, judged{Copies: true}) || notJudged(AttentionServerMoveIncomplete, judged{Servers: true}) {
		t.Error("server_move_incomplete must follow the servers")
	}
	it := AttentionItem{Kind: AttentionCopiesUnplaced, AppID: "blog", Service: "web", Queued: 2, Detail: "blog/web: 2 copies can't be placed."}
	ev := problemEvent(it, "https://c.test", time.Now())
	if ev.Title != "blog/web: copies can't be placed" || ev.Text != it.Detail || ev.URL != "https://c.test/apps/blog" {
		t.Errorf("copies_unplaced event = %+v", ev)
	}
	mv := AttentionItem{Kind: AttentionServerMoveIncomplete, Server: "two", Left: 2, Detail: "two: the move-off passed its deadline; 2 copies still run there."}
	ev = problemEvent(mv, "https://c.test", time.Now())
	if ev.Title != "Move-off incomplete: two" || ev.Text != mv.Detail || ev.URL != "https://c.test/ops/servers/two" || ev.Server != "two" {
		t.Errorf("server_move_incomplete event = %+v", ev)
	}
	// The activity feed files the move item under its server.
	if got := problemServerOf(problemKey(mv)); got != "two" {
		t.Errorf("problemServerOf = %q, want two", got)
	}
}
