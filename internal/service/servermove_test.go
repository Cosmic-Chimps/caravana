package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// moveCopyStub is a running copy of the job's group on a node.
func moveCopyStub(id, group, nodeID, nodeName string) api.AllocationListStub {
	return api.AllocationListStub{ID: id, TaskGroup: group, JobVersion: 1, ClientStatus: "running", DesiredStatus: "run",
		NodeID: nodeID, NodeName: nodeName, CreateIndex: 1,
		TaskStates: map[string]*api.TaskState{group: {State: "running"}}}
}

// moveEnv is a cluster of hel-1 (the first server) and two, with the service's move waits short.
type moveEnv struct {
	*clusterEnv
	two store.Server
}

func newMoveEnv(t *testing.T) *moveEnv {
	t.Helper()
	e := &moveEnv{clusterEnv: newClusterEnv(t)}
	e.two = e.addReadyServer(t, "two")
	e.svc.SetMoveTimingForTests(10*time.Millisecond, 3*time.Second, 5*time.Millisecond)
	e.nomad.MoveCopiesTo("node-1", firstServerName)
	return e
}

// saveApp stores app id with these services (name → options) and puts its copies on nodes.
func (e *moveEnv) saveApp(t *testing.T, id string, mutate func(a *app.App), copies map[string][]string) {
	t.Helper()
	a := appWith(id, 2)
	if mutate != nil {
		mutate(&a)
	}
	if _, err := e.store.InsertApp(context.Background(), a, nil); err != nil {
		t.Fatalf("InsertApp(%s): %v", id, err)
	}
	var stubs []api.AllocationListStub
	for group, nodes := range copies {
		for i, n := range nodes {
			stubs = append(stubs, moveCopyStub(fmt.Sprintf("%s-%s-%d", id, group, i), group, "node-"+n, nodeNamed(n)))
		}
	}
	e.nomad.SetCopies(id, stubs...)
}

func nodeNamed(index string) string {
	if index == "1" {
		return firstServerName
	}
	return "two"
}

// waitMove waits until the server's move is in this state.
func (e *moveEnv) waitMove(t *testing.T, state string) *MoveView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		v, err := e.svc.Server(context.Background(), "two")
		if err != nil {
			t.Fatalf("Server: %v", err)
		}
		if v.Move != nil && v.Move.State == state {
			return v.Move
		}
		if time.Now().After(deadline) {
			t.Fatalf("the move is %+v, want state %s", v.Move, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMoveWorkOffMovesWhatCanMove(t *testing.T) {
	ctx := context.Background()
	e := newMoveEnv(t)
	e.saveApp(t, "blog", nil, map[string][]string{"web": {"2", "2"}})
	e.saveApp(t, "db", func(a *app.App) {
		a.Services[0].Volumes = []app.Volume{{Name: "data", Path: "/data"}}
		a.Services[0].Replicas = nil
	}, map[string][]string{"web": {"2"}})
	e.saveApp(t, "turn", func(a *app.App) {
		a.Services[0].Replicas = nil
		a.Services[0].Ports = []app.Port{{Name: "turn", ContainerPort: 3478, Published: 3478}}
	}, map[string][]string{"web": {"2"}})
	e.nomad.AddForeignJob(&api.Job{ID: new("odd"), Name: new("odd")})
	e.nomad.SetCopies("odd", moveCopyStub("odd-0", "x", "node-2", "two"))
	e.nomad.SetTraefikJob("traefik:v3")
	e.nomad.SetCopies("traefik", moveCopyStub("tf-0", "traefik", "node-2", "two"))
	e.nomad.SetCopies("blog_web_backup/periodic-1", moveCopyStub("bk-0", "backup", "node-2", "two"))

	v, err := e.svc.MoveWorkOff(ctx, "two", 5*time.Second)
	if err != nil {
		t.Fatalf("MoveWorkOff: %v", err)
	}
	if v.AcceptsWork || v.Move == nil || v.Move.ID == "" {
		t.Errorf("the answer = accepts %v, move %+v", v.AcceptsWork, v.Move)
	}
	m := e.waitMove(t, store.MoveDone)
	if m.Moved != 2 || m.Left != 0 || m.EndedAt.IsZero() || m.Error != "" {
		t.Errorf("move = %+v, want 2 moved, none left", m)
	}
	// Two copies of blog were stopped, and nothing else.
	stopped := e.nomad.StoppedCopies()
	slices.Sort(stopped)
	if !slices.Equal(stopped, []string{"blog-web-0", "blog-web-1"}) {
		t.Errorf("stopped copies = %v", stopped)
	}
	reasons := map[string]string{}
	for _, st := range m.Stays {
		reasons[st.App+"/"+st.Service] = st.Reason
	}
	for key, reason := range map[string]string{
		"db/web":   "its data is on this server",
		"turn/web": "its port 3478 is on this server's address",
		"traefik/": "the proxy runs here",
		"blog/":    "a run that ends on its own",
		"odd/":     "not deployed by Caravana",
	} {
		if reasons[key] != reason {
			t.Errorf("stays[%s] = %q, want %q (all: %v)", key, reasons[key], reason, reasons)
		}
	}
	// The server keeps taking no new work, and the attention list has nothing for a done move.
	nodes, _ := e.svc.deployer.Nodes(ctx)
	if nodeOf(nodes, 2).Eligible {
		t.Error("the node accepts work after the move")
	}
	att, err := e.svc.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range att.Items {
		if it.Kind == AttentionServerMoveIncomplete {
			t.Errorf("a done move is on the needs-you list: %+v", it)
		}
	}
}

func TestMoveWorkOffRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("an unknown name, an unmanaged node and a server that hasn't joined", func(t *testing.T) {
		e := newMoveEnv(t)
		e.nomad.AddNode("node-9", "stray", "ready", "")
		if _, err := e.svc.MoveWorkOff(ctx, "nope", 0); !errors.Is(err, ErrServerNotFound) {
			t.Errorf("unknown: %v", err)
		}
		if _, err := e.svc.MoveWorkOff(ctx, "stray", 0); !errors.Is(err, ErrServerNotFound) {
			t.Errorf("unmanaged: %v", err)
		}
		j := e.newJoin(t, "later")
		if _, err := e.svc.MoveWorkOff(ctx, j.Server.Name, 0); !errors.Is(err, ErrServerNotReady) {
			t.Errorf("waiting: %v, want ErrServerNotReady", err)
		}
	})

	t.Run("a node Nomad says is down", func(t *testing.T) {
		e := newMoveEnv(t)
		e.nomad.SetNodeStatusOf("node-2", "down")
		if _, err := e.svc.MoveWorkOff(ctx, "two", 0); !errors.Is(err, ErrServerNotReady) {
			t.Errorf("= %v, want ErrServerNotReady", err)
		}
	})

	t.Run("no other server that accepts work", func(t *testing.T) {
		e := newMoveEnv(t)
		e.nomad.SetNodeEligibilityOf("node-1", "ineligible", false)
		_, err := e.svc.MoveWorkOff(ctx, "two", 0)
		if !errors.Is(err, ErrNoOtherServer) {
			t.Fatalf("= %v, want ErrNoOtherServer", err)
		}
		if got := err.Error(); got != "No other server accepts work: there is nowhere to move to." {
			t.Errorf("sentence = %q", got)
		}
		if len(e.nomad.EligibilityRequests()) != 0 {
			t.Error("the node was made ineligible")
		}
	})

	t.Run("a deadline outside 1 minute to 2 hours", func(t *testing.T) {
		e := newMoveEnv(t)
		e.svc.SetMoveTimingForTests(time.Minute, 0, 0)
		for _, d := range []time.Duration{time.Second, 3 * time.Hour} {
			_, err := e.svc.MoveWorkOff(ctx, "two", d)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != "deadlineSeconds" {
				t.Errorf("deadline %s: %v, want a field error on deadlineSeconds", d, err)
			}
		}
	})

	t.Run("a move already going", func(t *testing.T) {
		e := newMoveEnv(t)
		// A copy that can't be placed keeps the move open: its app is busy.
		e.saveApp(t, "blog", nil, map[string][]string{"web": {"2"}})
		state, err := e.svc.tryLockApp("blog")
		if err != nil {
			t.Fatal(err)
		}
		defer state.mu.Unlock()
		if _, err := e.svc.MoveWorkOff(ctx, "two", time.Minute); err != nil {
			t.Fatalf("first: %v", err)
		}
		if _, err := e.svc.MoveWorkOff(ctx, "two", time.Minute); !errors.Is(err, ErrServerMoving) {
			t.Errorf("second = %v, want ErrServerMoving", err)
		}
		if _, err := e.svc.AcceptWork(ctx, "two"); err != nil { // so the goroutine ends before the test does
			t.Fatal(err)
		}
	})

	t.Run("Nomad refuses to make the node ineligible", func(t *testing.T) {
		e := newMoveEnv(t)
		e.nomad.FailNodeWrites(403)
		_, err := e.svc.MoveWorkOff(ctx, "two", time.Minute)
		if err == nil || !strings.Contains(err.Error(), "node:write") {
			t.Errorf("= %v, want the capability error", err)
		}
		if v, _ := e.svc.Server(ctx, "two"); v.Move != nil {
			t.Errorf("a move was recorded: %+v", v.Move)
		}
	})
}

func TestMoveSkipsABusyAppUntilItIsFree(t *testing.T) {
	ctx := context.Background()
	e := newMoveEnv(t)
	e.saveApp(t, "blog", nil, map[string][]string{"web": {"2"}})
	state, err := e.svc.tryLockApp("blog") // a deploy holds it
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.MoveWorkOff(ctx, "two", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond) // several rounds
	if len(e.nomad.StoppedCopies()) != 0 {
		t.Fatalf("a copy of a busy app was moved: %v", e.nomad.StoppedCopies())
	}
	if v, _ := e.svc.Server(ctx, "two"); v.Move.State != store.MoveMoving || v.Move.Left != 1 {
		t.Errorf("move = %+v, want moving with 1 left", v.Move)
	}
	state.mu.Unlock()
	if m := e.waitMove(t, store.MoveDone); m.Moved != 1 {
		t.Errorf("moved = %d, want 1", m.Moved)
	}
}

func TestMoveEndsIncompleteAtTheDeadline(t *testing.T) {
	ctx := context.Background()
	e := newMoveEnv(t)
	e.svc.SetMoveTimingForTests(10*time.Millisecond, 0, 0)
	e.saveApp(t, "blog", nil, map[string][]string{"web": {"2", "2"}})
	e.nomad.FailAllocStops(500) // Nomad won't move them
	if _, err := e.svc.MoveWorkOff(ctx, "two", 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	m := e.waitMove(t, store.MoveIncomplete)
	if m.Left != 2 || m.Moved != 0 || m.Error == "" || m.EndedAt.IsZero() {
		t.Errorf("move = %+v, want incomplete with 2 left and a sentence", m)
	}
	if strings.Contains(m.Error, "blog") {
		t.Errorf("the sentence names an app: %q", m.Error)
	}
	// The server still takes no work, so the needs-you list has the item, in its words.
	att, err := e.svc.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var item *AttentionItem
	for i := range att.Items {
		if att.Items[i].Kind == AttentionServerMoveIncomplete {
			item = &att.Items[i]
		}
	}
	if item == nil || item.Server != "two" || item.Left != 2 {
		t.Fatalf("attention = %+v, want server_move_incomplete for two with 2 left", att.Items)
	}
	if want := "two: the move-off passed its deadline; 2 copies still run there."; item.Detail != want {
		t.Errorf("detail = %q, want %q", item.Detail, want)
	}
	// Accepting work again takes the item away.
	if _, err := e.svc.AcceptWork(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	att, _ = e.svc.Attention(ctx)
	for _, it := range att.Items {
		if it.Kind == AttentionServerMoveIncomplete {
			t.Errorf("the item is still there after Accept work again: %+v", it)
		}
	}
}

func TestAcceptWorkCancelsAMoveAndMakesTheServerEligible(t *testing.T) {
	ctx := context.Background()
	e := newMoveEnv(t)
	e.saveApp(t, "blog", nil, map[string][]string{"web": {"2", "2"}})
	e.nomad.SetStartDelay(time.Hour) // the first replacement never runs: the move is stuck waiting for it
	if _, err := e.svc.MoveWorkOff(ctx, "two", time.Minute); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(e.nomad.StoppedCopies()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no copy was stopped")
		}
		time.Sleep(5 * time.Millisecond)
	}
	v, err := e.svc.AcceptWork(ctx, "two")
	if err != nil {
		t.Fatalf("AcceptWork: %v", err)
	}
	if !v.AcceptsWork || v.Move == nil || v.Move.State != store.MoveCancelled || v.Move.EndedAt.IsZero() {
		t.Errorf("view = accepts %v, move %+v", v.AcceptsWork, v.Move)
	}
	if got := len(e.nomad.StoppedCopies()); got != 1 {
		t.Errorf("stopped copies = %d, want only the one already moving", got)
	}
	// With no move going it only toggles eligibility.
	if _, err := e.svc.AcceptWork(ctx, "two"); err != nil {
		t.Errorf("second AcceptWork: %v", err)
	}
	if _, err := e.svc.AcceptWork(ctx, "nope"); !errors.Is(err, ErrServerNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

func TestAcceptWorkOnAServerMadeIneligibleOutside(t *testing.T) {
	ctx := context.Background()
	e := newMoveEnv(t)
	e.nomad.SetNodeEligibilityOf("node-2", "ineligible", false)
	v, err := e.svc.AcceptWork(ctx, "two")
	if err != nil || !v.AcceptsWork {
		t.Fatalf("AcceptWork = %v, %v", v.AcceptsWork, err)
	}
}

// A process that ended during a move leaves the row open: the next start goes on, or settles it when its
// deadline has passed.
func TestResumeMoves(t *testing.T) {
	ctx := context.Background()

	t.Run("goes on from the copies still there", func(t *testing.T) {
		e := newMoveEnv(t)
		e.saveApp(t, "blog", nil, map[string][]string{"web": {"2"}})
		e.nomad.SetNodeEligibilityOf("node-2", "ineligible", false)
		now := e.svc.clock.Now()
		if _, err := e.store.InsertServerMove(ctx, store.ServerMove{ID: "mv_old", ServerIdx: e.two.Index,
			StartedAt: now, DeadlineAt: now.Add(time.Minute), Moved: 3, Left: 1}); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.resumeMoves(ctx); err != nil {
			t.Fatal(err)
		}
		m := e.waitMove(t, store.MoveDone)
		if m.ID != "mv_old" || m.Moved != 4 {
			t.Errorf("move = %+v, want mv_old done with the earlier 3 and this one counted", m)
		}
	})

	t.Run("a deadline that has passed is incomplete", func(t *testing.T) {
		e := newMoveEnv(t)
		now := e.svc.clock.Now()
		if _, err := e.store.InsertServerMove(ctx, store.ServerMove{ID: "mv_old", ServerIdx: e.two.Index,
			StartedAt: now.Add(-time.Hour), DeadlineAt: now.Add(-time.Minute), Moved: 1, Left: 2}); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.resumeMoves(ctx); err != nil {
			t.Fatal(err)
		}
		v, _ := e.svc.Server(ctx, "two")
		if v.Move.State != store.MoveIncomplete || v.Move.Error != "Caravana restarted during it." || v.Move.Left != 2 {
			t.Errorf("move = %+v", v.Move)
		}
	})
}

func TestClassifyCopies(t *testing.T) {
	svc := func(name string, mutate func(*app.Service)) app.Service {
		s := app.Service{Name: name, Image: "x/y:1"}
		if mutate != nil {
			mutate(&s)
		}
		return s
	}
	rec := func(id string, services ...app.Service) store.AppRecord {
		return store.AppRecord{App: app.App{ID: id, Services: services}}
	}
	apps := []store.AppRecord{
		rec("blog", svc("web", nil), svc("worker", nil)),
		rec("db", svc("pg", func(s *app.Service) { s.Volumes = []app.Volume{{Name: "d", Path: "/d"}} })),
		rec("turn", svc("srv", func(s *app.Service) { s.Ports = []app.Port{{Name: "t", ContainerPort: 1, Published: 3478}} })),
		rec("ssd", svc("web", func(s *app.Service) { s.Placement = &app.Placement{Require: map[string]string{"ssd": "true"}} })),
		rec("avoid", svc("web", func(s *app.Service) { s.Placement = &app.Placement{Avoid: []string{"one"}} })),
	}
	cp := func(id, service, node string) deploy.Copy { return deploy.Copy{ID: id, Service: service, Node: node} }
	byJob := map[string][]deploy.Copy{
		"blog": {cp("b1", "web", "two"), cp("b2", "web", "one"), cp("b3", "worker", "two"),
			{ID: "b4", Service: "web", Node: "two", Terminal: true}, {ID: "b5", Service: "web", Node: "two", Stopping: true}},
		"db":                          {cp("d1", "pg", "two")},
		"turn":                        {cp("t1", "srv", "two")},
		"ssd":                         {cp("s1", "web", "two")},
		"avoid":                       {cp("a1", "web", "two")},
		"traefik":                     {cp("tf", "traefik", "two")},
		"blog_web_backup/periodic-17": {cp("r1", "backup", "two")},
		"blog_run-abc":                {cp("r2", "run", "two"), cp("r3", "run", "two")},
		"foreign":                     {cp("f1", "g", "two")},
		"gone":                        {{ID: "g1", Service: "x", Node: "elsewhere"}},
	}
	others := []moveTarget{{Name: "one", Managed: true, Labels: map[string]string{}}}

	movables, stays := classifyCopies(apps, byJob, "two", others)
	var got []string
	for _, m := range movables {
		got = append(got, m.AppID+"/"+m.Service+":"+m.CopyID)
	}
	// Job order, then the copies' own; a placement that "avoid"s the only other server can't move.
	if want := []string{"blog/web:b1", "blog/worker:b3"}; !slices.Equal(got, want) {
		t.Errorf("movable = %v, want %v", got, want)
	}
	wantStays := []store.MoveStay{
		{App: "avoid", Service: "web", Reason: "its placement rules allow no other server"},
		{App: "blog", Reason: "a run that ends on its own"}, // both helpers: one entry
		{App: "db", Service: "pg", Reason: "its data is on this server"},
		{App: "foreign", Reason: "not deployed by Caravana"},
		{App: "ssd", Service: "web", Reason: "its placement rules allow no other server"},
		{App: "traefik", Reason: "the proxy runs here"},
		{App: "turn", Service: "srv", Reason: "its port 3478 is on this server's address"},
	}
	if !slices.Equal(stays, wantStays) {
		t.Errorf("stays:\n got %v\nwant %v", stays, wantStays)
	}

	// With another server that has the label, the "ssd" service can move; and an unmanaged server
	// satisfies "avoid" (its name isn't in the metadata, research R2).
	others = []moveTarget{{Name: "one", Managed: true, Labels: map[string]string{"ssd": "true"}}, {Name: "stray", Labels: map[string]string{}}}
	movables, _ = classifyCopies(apps, byJob, "two", others)
	got = nil
	for _, m := range movables {
		got = append(got, m.AppID)
	}
	if !slices.Contains(got, "ssd") || !slices.Contains(got, "avoid") {
		t.Errorf("movable apps = %v, want ssd and avoid among them", got)
	}
}

func TestAllowedOn(t *testing.T) {
	two := moveTarget{Name: "two", Managed: true, Labels: map[string]string{"ssd": "true", "region": "eu"}}
	for _, tc := range []struct {
		name string
		p    *app.Placement
		on   moveTarget
		want bool
	}{
		{"no rules", nil, two, true},
		{"prefer never forbids", &app.Placement{Prefer: []app.Preference{{Label: "gpu", Value: "x"}}}, two, true},
		{"every required label", &app.Placement{Require: map[string]string{"ssd": "true", "region": "eu"}}, two, true},
		{"one required label is missing", &app.Placement{Require: map[string]string{"ssd": "true", "gpu": "a"}}, two, false},
		{"a different value", &app.Placement{Require: map[string]string{"region": "us"}}, two, false},
		{"avoided by name", &app.Placement{Avoid: []string{"two"}}, two, false},
		{"another server avoided", &app.Placement{Avoid: []string{"one"}}, two, true},
		{"an unmanaged node is never avoided", &app.Placement{Avoid: []string{"stray"}}, moveTarget{Name: "stray"}, true},
		{"an unmanaged node has no labels", &app.Placement{Require: map[string]string{"ssd": "true"}}, moveTarget{Name: "stray"}, false},
	} {
		if got := allowedOn(tc.p, tc.on); got != tc.want {
			t.Errorf("%s: allowedOn = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// What a move records and says holds no output of an app and no secret: counts, IDs of apps and
// Caravana's own sentences.
func TestMoveRecordsOnlyCountsAndOurWords(t *testing.T) {
	ctx := context.Background()
	e := newMoveEnv(t)
	e.saveApp(t, "blog", nil, map[string][]string{"web": {"2"}})
	if _, err := e.svc.MoveWorkOff(ctx, "two", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	m := e.waitMove(t, store.MoveDone)
	text := fmt.Sprintf("%+v", m)
	for _, bad := range []string{"alloc", "blog-web-0", "10.99.0", "http"} {
		if strings.Contains(text, bad) {
			t.Errorf("the move %q holds %q", text, bad)
		}
	}
}
