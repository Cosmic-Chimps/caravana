package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// publishingApp is whoami with a TCP+UDP port published as number.
func publishingApp(id string, number int) app.App {
	a := whoamiAs(id)
	a.Services[0].Replicas = nil
	a.Services[0].Domains = nil
	a.Services[0].Health = &app.HealthCheck{Port: "http", Path: "/"}
	a.Services[0].Ports = append(a.Services[0].Ports, app.Port{Name: "turn", ContainerPort: 3478, Protocol: app.ProtocolTCPUDP, Published: number})
	return a
}

// withPublicNode gives the fake Nomad a server that offers published ports.
func withPublicNode(e *env) {
	e.nomad.AddNode("n1", "one", "ready", "10.99.0.1")
	e.nomad.SetPublicNetwork("n1", true)
}

func TestPortTaken(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	withPublicNode(e)
	deployHealthy(t, e, publishingApp("calls", 3478))

	t.Run("another app's port", func(t *testing.T) {
		_, err := e.svc.Deploy(ctx, publishingApp("game", 3478), app.TriggerManual)
		var taken *PortTakenError
		if !errors.As(err, &taken) || !errors.Is(err, ErrPortTaken) {
			t.Fatalf("Deploy = %v, want a *PortTakenError", err)
		}
		if taken.Field != "services[0].ports[1].published" || taken.App != "calls" || taken.Port != 3478 {
			t.Errorf("error = %+v", taken)
		}
		if _, err := e.store.GetApp(ctx, "game"); !errors.Is(err, store.ErrNotFound) {
			t.Error("the refused app was saved")
		}
	})

	t.Run("a stopped app still holds its port", func(t *testing.T) {
		stopDomainApp(t, e, "calls")
		_, err := e.svc.Deploy(ctx, publishingApp("game", 3478), app.TriggerManual)
		if !errors.Is(err, ErrPortTaken) {
			t.Errorf("Deploy = %v, want ErrPortTaken", err)
		}
		if err := e.store.SetAppStopped(ctx, "calls", nil); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("another number is free", func(t *testing.T) {
		deployHealthy(t, e, publishingApp("game", 3479))
	})

	t.Run("the same app redeploys with its port", func(t *testing.T) {
		deployHealthy(t, e, publishingApp("calls", 3478))
	})

	t.Run("an app that publishes nothing is not asked", func(t *testing.T) {
		deployHealthy(t, e, whoamiAs("plain"))
	})
}

// A deploy that publishes a port when no server offers the network ends in an
// error that matches deploy.ErrNoPublicNetwork, naming the port's field.
func TestNoPublicNetworkPassesThrough(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.Deploy(context.Background(), publishingApp("calls", 3478), app.TriggerManual)
	var pe *deploy.PublicNetworkError
	if !errors.As(err, &pe) || !errors.Is(err, deploy.ErrNoPublicNetwork) {
		t.Fatalf("Deploy = %v, want a *deploy.PublicNetworkError", err)
	}
	if pe.Field != "services[0].ports[1].published" {
		t.Errorf("field = %q", pe.Field)
	}
}

// A promotion keeps the target's published numbers by port name, and a port the
// target doesn't have arrives unpublished (spec 041 D7).
func TestPromotedKeepsThePublishedPorts(t *testing.T) {
	source, target := publishingApp("shop--staging--web", 4000), publishingApp("shop--production--web", 3478)
	source.Services[0].Ports[1].Protocol = app.ProtocolUDP

	got, changes, _, err := promoted(target, source)
	if err != nil {
		t.Fatal(err)
	}
	ports := got.Services[0].Ports
	if ports[1].Published != 3478 || ports[1].Protocol != app.ProtocolUDP {
		t.Errorf("turn = %+v, want the target's 3478 and the source's udp", ports[1])
	}
	if !slices.Contains(changes[0].Changed, "ports") {
		t.Errorf("changes = %+v, want ports listed (the protocol changed)", changes)
	}

	// Only the published number differs: nothing is reported.
	source.Services[0].Ports[1].Protocol = app.ProtocolTCPUDP
	_, changes, _, _ = promoted(target, source)
	if slices.Contains(changes[0].Changed, "ports") {
		t.Errorf("changes = %+v, a different published number is the environment's own", changes)
	}

	// The target doesn't publish: the source's number doesn't arrive.
	target.Services[0].Ports[1].Published = 0
	got, _, _, _ = promoted(target, source)
	if got.Services[0].Ports[1].Published != 0 {
		t.Errorf("ports = %+v, want turn unpublished", got.Services[0].Ports)
	}

	// A service new to the target arrives unpublished.
	source.Services = append(source.Services, publishingApp("x", 5000).Services[0])
	source.Services[1].Name = "extra"
	got, _, added, _ := promoted(target, source)
	if !slices.Equal(added, []string{"extra"}) || got.Services[1].Ports[1].Published != 0 {
		t.Errorf("added = %v, service = %+v, want it unpublished", added, got.Services[1])
	}
	if source.Services[1].Ports[1].Published != 5000 {
		t.Error("the source's port was changed")
	}
}

// Copy to drops every published number, as it drops domains.
func TestCopyAppDropsPublishedPorts(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	withPublicNode(e)
	deployHealthy(t, e, publishingApp(productionID, 3478))
	rec, _, err := e.svc.CopyApp(ctx, productionID, "staging")
	if err != nil {
		t.Fatalf("CopyApp() = %v", err)
	}
	for _, p := range rec.App.Services[0].Ports {
		if p.Published != 0 {
			t.Errorf("copy has %+v published", p)
		}
	}
	if got := storedService(t, e, productionID).Ports[1].Published; got != 3478 {
		t.Errorf("the source lost its port: %d", got)
	}
}

const turnCompose = "services:\n  turn:\n    image: traefik/whoami:latest\n    ports: [\"3478:3478/udp\", \"3478:3478/tcp\", \"80\"]\n"

func turnRequest(publish ...compose.Publish) compose.Request {
	return compose.Request{Compose: turnCompose, Services: map[string]compose.Overlay{
		"turn": {Publish: publish, Health: &compose.Health{Port: 80}},
	}}
}

// A Compose deploy names a refused port by its overlay entry, and Copy to drops
// the kept overlay's publish list with the app's numbers.
func TestComposePublishedPorts(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	withPublicNode(e)

	rec, err := e.svc.DeployCompose(ctx, productionID, turnRequest(compose.Publish{Port: 3478}), app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployCompose: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if got := storedService(t, e, productionID).Ports[0]; got.Published != 3478 || got.Protocol != app.ProtocolTCPUDP {
		t.Errorf("port = %+v, want 3478 tcp+udp", got)
	}

	_, err = e.svc.DeployCompose(ctx, "game", turnRequest(compose.Publish{Port: 3478}), app.TriggerManual)
	var taken *PortTakenError
	if !errors.As(err, &taken) || taken.Field != "services.turn.publish[0].published" || taken.App != productionID {
		t.Errorf("DeployCompose of another app = %v, want a *PortTakenError on services.turn.publish[0].published", err)
	}

	cp, _, err := e.svc.CopyApp(ctx, productionID, "staging")
	if err != nil {
		t.Fatalf("CopyApp: %v", err)
	}
	if got := cp.Compose.Services["turn"].Publish; got != nil {
		t.Errorf("the copy's overlay publishes %+v", got)
	}
	if got := cp.App.Services[0].Ports[0].Published; got != 0 {
		t.Errorf("the copy publishes %d", got)
	}
	src, _ := e.svc.GetApp(ctx, productionID)
	if len(src.Compose.Services["turn"].Publish) != 1 {
		t.Error("the source's overlay lost its publish list")
	}
}
