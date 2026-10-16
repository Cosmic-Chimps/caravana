package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// deployNotifyEnv is an env with one channel for each way of wanting deploy
// outcomes, and its sender parked so the queue can be read.
type deployNotifyEnv struct {
	*env
	succeeded store.NotificationChannel // deploy_succeeded
	failed    store.NotificationChannel // deploy_failed
	both      store.NotificationChannel // both
	problems  store.NotificationChannel // problems only: never gets a deploy outcome
	paused    store.NotificationChannel // both, paused
}

func newDeployNotifyEnv(t *testing.T) *deployNotifyEnv {
	t.Helper()
	ctx := context.Background()
	e := &deployNotifyEnv{env: newEnv(t)}
	parkSender(e.svc)
	e.svc.publicURL = "https://caravana.example.com"
	add := func(name string, events []string, paused bool) store.NotificationChannel {
		in := channelInput(notify.KindNtfy, "https://ntfy.example.com/"+name)
		in.Name, in.Events, in.Paused = name, events, paused
		got, err := e.svc.CreateNotificationChannel(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return got.NotificationChannel.NotificationChannel
	}
	e.succeeded = add("succeeded", []string{EventDeploySucceeded}, false)
	e.failed = add("failed", []string{EventDeployFailed}, false)
	e.both = add("both", []string{EventDeployFailed, EventDeploySucceeded}, false)
	e.problems = add("problems", []string{EventProblems}, false)
	e.paused = add("paused", []string{EventDeployFailed, EventDeploySucceeded}, true)
	return e
}

// settle waits until no rollout watcher is running, so every outcome that was
// coming has been recorded and queued.
func (e *deployNotifyEnv) settle() { e.svc.watchers.Wait() }

// events returns the events queued for a channel, oldest first.
func (e *deployNotifyEnv) events(t *testing.T, ch store.NotificationChannel) []notify.Event {
	t.Helper()
	var out []notify.Event
	for _, d := range (&notifyEnv{env: e.env}).deliveriesOf(t, ch) {
		if d.Status != store.DeliveryPending || d.ChannelID != ch.ID {
			t.Errorf("delivery %+v, want pending for %s", d, ch.ID)
		}
		out = append(out, payloadOf(t, d))
	}
	// The fake clock stamps every delivery alike, so the queue order is not
	// readable; the events carry the real time their deployments ended.
	slices.SortFunc(out, func(a, b notify.Event) int { return a.At.Compare(b.At) })
	return out
}

// wantQueued checks which channels were told: those named got exactly one event
// of type typ, the others none.
func (e *deployNotifyEnv) wantQueued(t *testing.T, typ string, told ...store.NotificationChannel) notify.Event {
	t.Helper()
	var first notify.Event
	for _, ch := range []store.NotificationChannel{e.succeeded, e.failed, e.both, e.problems, e.paused} {
		got := e.events(t, ch)
		wanted := false
		for _, c := range told {
			wanted = wanted || c.ID == ch.ID
		}
		switch {
		case wanted && (len(got) != 1 || got[0].Type != typ):
			t.Errorf("channel %s got %+v, want one %s", ch.ID, got, typ)
		case wanted:
			first = got[0]
		case !wanted && len(got) != 0:
			t.Errorf("channel %s got %+v, want nothing", ch.ID, got)
		}
	}
	return first
}

// Each way a deployment can end is queued once, to the channels that want it,
// with the version, outcome, trigger and reason.
func TestNotifyDeployOutcomes(t *testing.T) {
	ctx := context.Background()
	revert := "Failed due to unhealthy allocations - rolling back to job version 0"

	t.Run("healthy from the console", func(t *testing.T) {
		e := newDeployNotifyEnv(t)
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerConsole)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		e.settle()
		ev := e.wantQueued(t, notify.EventDeploySucceeded, e.succeeded, e.both)
		if ev.Outcome != "healthy" || ev.Trigger != "console" || ev.AppID != "whoami" || ev.Deployment != rec.Deployment.ID ||
			ev.Version == nil || *ev.Version != 0 || ev.Detail != "" ||
			ev.URL != "https://caravana.example.com/apps/whoami/deployments#deployment-"+rec.Deployment.ID {
			t.Errorf("event = %+v", ev)
		}
	})

	t.Run("failed rollout started by the API", func(t *testing.T) {
		e := newDeployNotifyEnv(t)
		desc := "Failed due to progress deadline"
		e.nomad.Script("whoami", 0, api.Deployment{Status: api.DeploymentStatusFailed, StatusDescription: desc})
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		e.settle()
		ev := e.wantQueued(t, notify.EventDeployFailed, e.failed, e.both)
		if ev.Outcome != "failed" || ev.Trigger != "manual" || ev.Detail != desc || ev.Version == nil {
			t.Errorf("event = %+v", ev)
		}
	})

	t.Run("reverted, started by a webhook", func(t *testing.T) {
		e := newDeployNotifyEnv(t)
		deployHealthy(t, e.env, whoami())
		e.settle()
		// The first deploy told the channels that want successes. Look only at what comes next.
		e.nomad.Script("whoami", 1, api.Deployment{Status: api.DeploymentStatusFailed, StatusDescription: revert})
		rec, err := e.svc.Redeploy(ctx, "whoami", app.TriggerWebhook)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		e.settle()

		for _, ch := range []store.NotificationChannel{e.failed, e.both} {
			got := e.events(t, ch)
			last := got[len(got)-1]
			if last.Type != notify.EventDeployFailed || last.Outcome != "reverted" || last.Trigger != "webhook" || last.Detail != revert ||
				last.Deployment != rec.Deployment.ID {
				t.Errorf("channel %s last event = %+v", ch.ID, last)
			}
		}
		if got := e.events(t, e.failed); len(got) != 1 {
			t.Errorf("the failures-only channel got %d events, want 1 (the revert)", len(got))
		}
		if got := e.events(t, e.succeeded); len(got) != 1 || got[0].Type != notify.EventDeploySucceeded {
			t.Errorf("the successes-only channel got %+v, want only the first deploy's", got)
		}
	})

	t.Run("failed before Nomad: the image is not found", func(t *testing.T) {
		e := newDeployNotifyEnv(t)
		a := whoami()
		a.Services[0].Image = "traefik/whoami:nope"
		if _, err := e.svc.Deploy(ctx, a, app.TriggerManual); !errors.Is(err, deploy.ErrImageNotFound) {
			t.Fatalf("Deploy() = %v, want ErrImageNotFound", err)
		}
		ev := e.wantQueued(t, notify.EventDeployFailed, e.failed, e.both)
		if ev.Outcome != "failed" || ev.Version != nil || ev.Trigger != "manual" || ev.Detail == "" {
			t.Errorf("event = %+v, want failed, no version, with the reason", ev)
		}

		// A redeploy from a webhook that fails the same way is a second event, with its own trigger.
		if _, err := e.svc.Redeploy(ctx, "whoami", app.TriggerWebhook); err == nil {
			t.Fatal("Redeploy of a missing image succeeded")
		}
		got := e.events(t, e.both)
		if len(got) != 2 || got[1].Trigger != "webhook" || got[1].Deployment == got[0].Deployment {
			t.Errorf("events after the redeploy = %+v", got)
		}
	})

	t.Run("replaced by a newer deploy before it registered", func(t *testing.T) {
		e := newDeployNotifyEnv(t)
		release := e.registry.Hold("traefik/whoami", "latest")
		aDone := make(chan error, 1)
		go func() {
			_, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
			aDone <- err
		}()
		waitFor(t, "A to reach the registry", func() bool { return e.registry.Requests() >= 1 })
		list, _ := e.store.ListDeployments(ctx, "whoami", 0)
		aID := list[0].Deployment.ID
		bDone := make(chan store.DeploymentRecord, 1)
		go func() {
			rec, _ := e.svc.Deploy(ctx, whoami(), app.TriggerWebhook)
			bDone <- rec
		}()
		waitFor(t, "B to be the latest request", func() bool { return e.svc.latestOf("whoami") != aID })
		release()
		if err := <-aDone; err != nil {
			t.Fatal(err)
		}
		b := <-bDone
		waitFinal(t, e.store, b.Deployment.ID)
		e.settle()

		got := e.events(t, e.both)
		if len(got) != 2 {
			t.Fatalf("channel both got %+v, want A's replacement and B's success", got)
		}
		byDeployment := map[string]notify.Event{got[0].Deployment: got[0], got[1].Deployment: got[1]}
		if a := byDeployment[aID]; a.Type != notify.EventDeployFailed || a.Outcome != "superseded" || a.Version != nil || a.Detail == "" {
			t.Errorf("A's event = %+v, want a failed deploy, superseded, never registered", a)
		}
		if b := byDeployment[b.Deployment.ID]; b.Type != notify.EventDeploySucceeded || b.Trigger != "webhook" {
			t.Errorf("B's event = %+v, want a success started by the webhook", b)
		}
	})
}

// Nothing is queued, and nothing breaks, when no channel wants the outcome.
func TestNotifyDeployWithoutChannels(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Errorf("deployment = %s", final.Status)
	}
}

// A deployment that was already final is not told again: a resumed rollout that
// ends twice, or any second write, queues nothing.
func TestNotifyDeployOnce(t *testing.T) {
	ctx := context.Background()
	e := newDeployNotifyEnv(t)
	rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	e.settle()
	e.wantQueued(t, notify.EventDeploySucceeded, e.succeeded, e.both)

	e.svc.finish("whoami", rec.Deployment.ID, store.StatusFailed, "a second, late outcome")
	e.wantQueued(t, notify.EventDeploySucceeded, e.succeeded, e.both)
}
