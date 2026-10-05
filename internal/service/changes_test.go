package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
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

// US1/R6: the copies watcher runs for subscribers only.
func TestCopiesWatcher(t *testing.T) {
	running := func(id string) api.AllocationListStub {
		return api.AllocationListStub{ID: id, TaskGroup: "web", ClientStatus: "running"}
	}
	// firstAnswer waits for the watcher's starting point: changes made before
	// it are part of it, not news.
	firstAnswer := func(t *testing.T, e *env) {
		t.Helper()
		waitFor(t, "the watcher's first query", func() bool { return e.nomad.AllocationRequests() >= 1 })
	}

	t.Run("a subscriber hears copies changes", func(t *testing.T) {
		e := newEnv(t)
		oneApp, unsubscribeApp := e.svc.Subscribe("blog")
		defer unsubscribeApp()
		other, unsubscribeOther := e.svc.Subscribe("whoami")
		defer unsubscribeOther()
		all, unsubscribeAll := e.svc.Subscribe("")
		defer unsubscribeAll()
		forApp, forOther, forAll := collect(oneApp), collect(other), collect(all)
		firstAnswer(t, e)

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

	t.Run("it stops after the last subscriber leaves", func(t *testing.T) {
		e := newEnv(t)
		_, unsubscribeA := e.svc.Subscribe("blog")
		_, unsubscribeB := e.svc.Subscribe("")
		firstAnswer(t, e)

		unsubscribeA()
		time.Sleep(50 * time.Millisecond) // five times the idle delay
		e.nomad.SetCopies("blog", running("b1"))
		waitFor(t, "the watcher to ask again while one subscriber is left", func() bool {
			return e.nomad.AllocationRequests() >= 2
		})

		unsubscribeB()
		time.Sleep(100 * time.Millisecond) // the idle delay passes and the watcher stops
		asked := e.nomad.AllocationRequests()
		e.nomad.SetCopies("blog", running("b1"), running("b2"))
		time.Sleep(100 * time.Millisecond)
		if got := e.nomad.AllocationRequests(); got != asked {
			t.Errorf("Nomad was asked %d more times after the watcher should have stopped", got-asked)
		}
	})

	t.Run("a subscriber who returns within the delay keeps the watcher", func(t *testing.T) {
		e := newEnv(t)
		e.svc.watchIdle = 200 * time.Millisecond
		_, unsubscribe := e.svc.Subscribe("blog")
		firstAnswer(t, e)
		unsubscribe()

		ch, unsubscribeAgain := e.svc.Subscribe("blog") // the reload
		defer unsubscribeAgain()
		got := collect(ch)
		time.Sleep(300 * time.Millisecond) // the first delay has passed
		e.nomad.SetCopies("blog", running("b1"))
		waitFor(t, "a copies notice from the same watcher", func() bool { return got.count("blog", ChangeCopies) >= 1 })
	})

	t.Run("it starts again for a later subscriber", func(t *testing.T) {
		e := newEnv(t)
		_, unsubscribe := e.svc.Subscribe("blog")
		firstAnswer(t, e)
		unsubscribe()
		time.Sleep(100 * time.Millisecond)

		before := e.nomad.AllocationRequests()
		ch, unsubscribeAgain := e.svc.Subscribe("blog")
		defer unsubscribeAgain()
		got := collect(ch)
		waitFor(t, "the new watcher's first query", func() bool { return e.nomad.AllocationRequests() > before })
		e.nomad.SetCopies("blog", running("b1"))
		waitFor(t, "a copies notice", func() bool { return got.count("blog", ChangeCopies) >= 1 })
	})
}
