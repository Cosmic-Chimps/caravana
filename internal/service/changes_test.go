package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
)

// drain returns the notices waiting on ch, without blocking.
func drain(ch <-chan Change) []Change {
	var got []Change
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, c)
		default:
			return got
		}
	}
}

func TestSubscribeFiltering(t *testing.T) {
	notices := []Change{
		{AppID: "blog", What: ChangeApp},
		{AppID: "whoami", What: ChangeDeployments},
		{AppID: "", What: ChangeApps},
		{AppID: "blog", What: ChangeCopies},
	}
	tests := []struct {
		name  string
		appID string
		want  []Change
	}{
		{"one app gets only its notices", "blog", []Change{notices[0], notices[3]}},
		{"another app", "whoami", []Change{notices[1]}},
		{"an app without notices", "shop", nil},
		{"the empty ID gets every notice", "", notices},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newEnv(t).svc
			ch, unsubscribe := s.Subscribe(tc.appID)
			defer unsubscribe()
			for _, n := range notices {
				s.publish(n)
			}
			got := drain(ch)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("notice %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestSubscriberBufferIsSixteenAndFullDoesNotBlockOthers(t *testing.T) {
	s := newEnv(t).svc
	slow, unsubSlow := s.Subscribe("blog")
	defer unsubSlow()
	fast, unsubFast := s.Subscribe("blog")
	defer unsubFast()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 40 { // far more than the buffer, and nobody reads slow
			s.publish(Change{AppID: "blog", What: ChangeCopies})
			if got := drain(fast); len(got) != 1 {
				t.Errorf("the fast subscriber got %d notices, want 1", len(got))
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a subscriber that isn't reading")
	}
	if got := len(drain(slow)); got != 16 {
		t.Errorf("the slow subscriber kept %d notices, want its buffer of 16 (the rest dropped)", got)
	}
}

func TestUnsubscribe(t *testing.T) {
	s := newEnv(t).svc
	ch, unsubscribe := s.Subscribe("blog")
	other, unsubOther := s.Subscribe("blog")
	defer unsubOther()

	unsubscribe()
	unsubscribe() // twice is fine: it must not close the channel twice

	if _, open := <-ch; open {
		t.Error("the channel is still open after unsubscribe")
	}
	s.publish(Change{AppID: "blog", What: ChangeApp}) // must not panic on the closed channel
	if got := drain(other); len(got) != 1 {
		t.Errorf("the other subscriber got %d notices, want 1", len(got))
	}
}

// Run with -race: publishers, subscribers and unsubscribers at once.
func TestChangesConcurrent(t *testing.T) {
	s := newEnv(t).svc
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 200 {
				s.publish(Change{AppID: "blog", What: ChangeApp})
			}
		}()
		go func() {
			defer wg.Done()
			id := ""
			if i%2 == 0 {
				id = "blog"
			}
			for range 50 {
				ch, unsubscribe := s.Subscribe(id)
				drain(ch)
				unsubscribe()
			}
		}()
	}
	wg.Wait()
}

// collector gathers every notice a subscription delivers, so a test can look
// at them after the deploy has run (a deploy publishes more than the buffer).
type collector struct {
	mu   sync.Mutex
	list []Change
}

func collect(ch <-chan Change) *collector {
	c := &collector{}
	go func() {
		for n := range ch {
			c.mu.Lock()
			c.list = append(c.list, n)
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *collector) count(app string, what ChangeKind) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, x := range c.list {
		if x.AppID == app && x.What == what {
			n++
		}
	}
	return n
}

// Data model, "Who publishes what": a deploy publishes at each step of its
// record, which is pending, running and healthy for a scripted rollout.
func TestDeployPublishesNotices(t *testing.T) {
	ctx := context.Background()

	t.Run("a healthy rollout", func(t *testing.T) {
		e := newEnv(t)
		oneApp, unsubscribeApp := e.svc.Subscribe("whoami")
		defer unsubscribeApp()
		everyApp, unsubscribeAll := e.svc.Subscribe("")
		defer unsubscribeAll()
		forApp, forAll := collect(oneApp), collect(everyApp)

		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)

		// SaveApp: app + apps. Then pending, running (twice: the deploy and
		// its watcher both record it) and healthy: at least three.
		waitFor(t, "three deployments notices for the app", func() bool {
			return forApp.count("whoami", ChangeDeployments) >= 3
		})
		if n := forApp.count("whoami", ChangeApp); n < 4 { // SaveApp + the deployment records
			t.Errorf("app notices = %d, want at least 4", n)
		}
		for _, what := range []ChangeKind{ChangeApps, ChangeActivity} {
			if forApp.count("", what) != 0 {
				t.Errorf("a per-app subscriber got an all-apps %q notice", what)
			}
		}
		waitFor(t, "apps and activity notices for all", func() bool {
			return forAll.count("", ChangeApps) >= 4 && forAll.count("", ChangeActivity) >= 3
		})
	})

	t.Run("a failed deploy publishes pending and failed", func(t *testing.T) {
		e := newEnv(t)
		sub, unsubscribe := e.svc.Subscribe("whoami")
		defer unsubscribe()
		got := collect(sub)

		a := whoami()
		a.Services[0].Image = "traefik/whoami:nope"
		if _, err := e.svc.Deploy(ctx, a, app.TriggerManual); err == nil {
			t.Fatal("Deploy of a missing image succeeded")
		}
		waitFor(t, "two deployments notices", func() bool { return got.count("whoami", ChangeDeployments) >= 2 })
	})

	t.Run("a deploy that fails validation publishes nothing", func(t *testing.T) {
		e := newEnv(t)
		sub, unsubscribe := e.svc.Subscribe("")
		defer unsubscribe()
		a := whoami()
		a.Services[0].Name = "Web"
		if _, err := e.svc.Deploy(ctx, a, app.TriggerManual); err == nil {
			t.Fatal("Deploy of an invalid app succeeded")
		}
		if got := drain(sub); len(got) != 0 {
			t.Errorf("notices = %v, want none", got)
		}
	})
}

// connected waits until Caravana follows Nomad's events: it makes a node event
// until a notice comes through (a Resync's or the event's own, either one means
// the stream is up and its first Resync has been handled).
func connected(t *testing.T, e *env) {
	t.Helper()
	ch, done := e.svc.Subscribe("")
	defer done()
	got := collect(ch)
	waitFor(t, "the event stream to be followed", func() bool {
		e.nomad.SetNodeStaticMeta("probe", nil) // an unknown node: only the event matters
		time.Sleep(5 * time.Millisecond)
		return got.count("", ChangeServers) >= 1
	})
	time.Sleep(50 * time.Millisecond) // the notices of the probes still on their way come through now, not in the test's own subscription
}

// Nomad's events drive the copies notices: no subscriber starts a watcher any more.
func TestCopiesEvents(t *testing.T) {
	running := func(id string) api.AllocationListStub {
		return api.AllocationListStub{ID: id, TaskGroup: "web", ClientStatus: "running"}
	}

	t.Run("a subscriber hears copies changes", func(t *testing.T) {
		e := newEnv(t)
		connected(t, e)
		oneApp, unsubscribeApp := e.svc.Subscribe("blog")
		defer unsubscribeApp()
		other, unsubscribeOther := e.svc.Subscribe("whoami")
		defer unsubscribeOther()
		all, unsubscribeAll := e.svc.Subscribe("")
		defer unsubscribeAll()
		forApp, forOther, forAll := collect(oneApp), collect(other), collect(all)

		e.nomad.SetCopies("blog", running("b1"))

		waitFor(t, "a copies notice", func() bool { return forApp.count("blog", ChangeCopies) >= 1 })
		waitFor(t, "an app notice", func() bool { return forApp.count("blog", ChangeApp) >= 1 })
		waitFor(t, "an apps notice for all", func() bool { return forAll.count("", ChangeApps) >= 1 })
		if forAll.count("blog", ChangeCopies) < 1 {
			t.Error("the all-apps subscriber missed the copies notice")
		}
		if forOther.count("blog", ChangeCopies) != 0 || forOther.count("", ChangeApps) != 0 {
			t.Error("another app's subscriber heard about blog")
		}
	})

	t.Run("no subscriber means no query over every allocation", func(t *testing.T) {
		e := newEnv(t)
		connected(t, e)
		e.nomad.SetCopies("blog", running("b1"))
		time.Sleep(50 * time.Millisecond)
		if n := e.nomad.AllocationRequests(); n != 0 {
			t.Errorf("Nomad was asked for every allocation %d times; the event stream replaces that", n)
		}
	})

	t.Run("a deployment event is the history's progress notice", func(t *testing.T) {
		e := newEnv(t)
		connected(t, e)
		ch, done := e.svc.Subscribe("blog")
		defer done()
		got := collect(ch)
		e.nomad.Script("blog", 0, api.Deployment{Status: api.DeploymentStatusRunning})
		waitFor(t, "a deployments notice", func() bool { return got.count("blog", ChangeDeployments) >= 1 })
	})

	t.Run("a node event is a servers notice", func(t *testing.T) {
		e := newEnv(t)
		connected(t, e)
		ch, done := e.svc.Subscribe("")
		defer done()
		got := collect(ch)
		e.nomad.AddNode("n2", "two", "ready", "10.99.0.2")
		waitFor(t, "a servers notice", func() bool { return got.count("", ChangeServers) >= 1 })
	})

	t.Run("a Resync refreshes every stored app's copies and deployments", func(t *testing.T) {
		e := newEnv(t)
		connected(t, e)
		if _, err := e.svc.Deploy(context.Background(), whoami(), app.TriggerManual); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		ch, done := e.svc.Subscribe("whoami")
		defer done()
		got := collect(ch)
		e.svc.onEvents(deploy.EventBatch{Resync: true})
		waitFor(t, "the notices a reload needs", func() bool {
			return got.count("whoami", ChangeCopies) >= 1 && got.count("whoami", ChangeDeployments) >= 1
		})
	})

	t.Run("a stream that was lost and comes back is logged once each way", func(t *testing.T) {
		e := newEnv(t)
		var logs syncBuffer
		svc := New(e.store, newDeployer(t, e.registry, e.nomad), e.secrets, slog.New(slog.NewTextHandler(&logs, nil))) // not started: only the log is looked at
		svc.eventsFailed(errors.New("down"))
		svc.eventsFailed(errors.New("still down"))
		svc.onEvents(deploy.EventBatch{Resync: true})
		svc.onEvents(deploy.EventBatch{Resync: true}) // a second Resync is no news
		out := logs.String()
		if strings.Count(out, "lost Nomad's event stream") != 1 || strings.Count(out, "following Nomad's events again") != 1 {
			t.Errorf("log = %q, want one line each way", out)
		}
	})
}
