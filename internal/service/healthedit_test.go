package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// An app with a definition has its health check set on the service, keeping the port it named.
func TestSetHealthIfDefinition(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := whoami()
	deployHealthy(t, e, a)
	before, _ := e.svc.GetApp(ctx, "whoami")
	port := ""
	if h := before.App.Services[0].Health; h != nil {
		port = h.Port
	}

	rec, err := e.svc.SetHealthIf(ctx, "whoami", "web", "/ready", 5*time.Second, time.Second, Condition{IfRevision: before.Revision}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("SetHealthIf: %v", err)
	}
	if rec.Deployment.Trigger != app.TriggerConsole {
		t.Errorf("trigger = %s, want console", rec.Deployment.Trigger)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	after, _ := e.svc.GetApp(ctx, "whoami")
	if h := after.App.Services[0].Health; h == nil || h.Path != "/ready" || h.Interval != 5*time.Second || h.Timeout != time.Second || h.Port != port {
		t.Errorf("health = %+v, want /ready every 5s with timeout 1s and port %q", h, port)
	}

	// Empty is the default check: a TCP connection.
	rec, err = e.svc.SetHealthIf(ctx, "whoami", "web", "", 0, 0, Condition{}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("SetHealthIf empty: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	after, _ = e.svc.GetApp(ctx, "whoami")
	if h := after.App.Services[0].Health; h == nil || h.Path != "" || h.Interval != 0 || h.Timeout != 0 {
		t.Errorf("health = %+v, want the TCP default", h)
	}
}

// An app that keeps its Compose file keeps it: the overlay's health follows, with its port, and the
// text, the domain, the other overlay fields and the secret's name stay, with no secret value.
func TestSetHealthIfKeptFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	req := compose.Request{Compose: domainFile, Services: map[string]compose.Overlay{"web": {
		Domains: []compose.Domain{{Host: "shop.example.com"}},
		Secrets: []app.Secret{{Name: "API_KEY", Value: new(keptSentinel)}},
		Health:  &compose.Health{Path: "/old", Port: 80},
		Backup:  nil,
	}}}
	composeHealthy(t, e, req)
	before, _ := e.svc.GetApp(ctx, "whoami")

	rec, err := e.svc.SetHealthIf(ctx, "whoami", "web", "/ready", 5*time.Second, time.Second, Condition{IfRevision: before.Revision}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("SetHealthIf: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if n := droppedFile(rec); n != 0 {
		t.Errorf("warnings = %+v, want no drop warning", rec.Warnings)
	}
	after, _ := e.svc.GetApp(ctx, "whoami")
	if after.Compose == nil || after.Compose.Compose != domainFile {
		t.Fatalf("kept file = %+v, want the same text", after.Compose)
	}
	web := after.Compose.Services["web"]
	want := compose.Health{Path: "/ready", Port: 80, Interval: 5 * time.Second, Timeout: time.Second}
	if web.Health == nil || *web.Health != want {
		t.Errorf("overlay health = %+v, want %+v", web.Health, want)
	}
	if len(web.Domains) != 1 || web.Domains[0].Host != "shop.example.com" || len(web.Secrets) != 1 || web.Secrets[0].Name != "API_KEY" || web.Secrets[0].Value != nil {
		t.Errorf("overlay = %+v, want the domain and the secret's name kept, with no value", web)
	}
	if h := after.App.Services[0].Health; h == nil || h.Path != "/ready" {
		t.Errorf("app health = %+v, want /ready", h)
	}
	data, _ := json.Marshal(after.Compose)
	if strings.Contains(string(data), keptSentinel) {
		t.Error("the stored file holds a secret value")
	}

	t.Run("a stale revision", func(t *testing.T) {
		n := len(mustDeployments(t, e))
		if _, err := e.svc.SetHealthIf(ctx, "whoami", "web", "/x", 0, 0, Condition{IfRevision: before.Revision}, app.TriggerConsole); !errors.Is(err, ErrAppChanged) {
			t.Errorf("a stale revision: %v, want ErrAppChanged", err)
		}
		if len(mustDeployments(t, e)) != n {
			t.Error("a refused change deployed")
		}
	})

	t.Run("a refusal names the file's path", func(t *testing.T) {
		var fe *app.FieldError
		_, err := e.svc.SetHealthIf(ctx, "whoami", "web", "/x", time.Second, 5*time.Second, Condition{}, app.TriggerConsole)
		if !errors.As(err, &fe) || !strings.HasPrefix(fe.Field, "services.web.health") {
			t.Errorf("timeout over interval: %v, want a field error on services.web.health…", err)
		}
		still, _ := e.svc.GetApp(ctx, "whoami")
		if still.Revision != after.Revision {
			t.Error("a refused change saved the app")
		}
	})

	t.Run("empty keeps only the port", func(t *testing.T) {
		rec, err := e.svc.SetHealthIf(ctx, "whoami", "web", "", 0, 0, Condition{}, app.TriggerConsole)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		got, _ := e.svc.GetApp(ctx, "whoami")
		if h := got.Compose.Services["web"].Health; h == nil || *h != (compose.Health{Port: 80}) {
			t.Errorf("overlay health = %+v, want only the port", h)
		}
	})
}

// With no port in the overlay, emptying the check leaves no health at all, and an overlay left
// with nothing has no entry; what else the overlay set is never dropped with it.
func TestWithHealth(t *testing.T) {
	file := compose.Request{Compose: domainFile, Services: map[string]compose.Overlay{
		"web": {Health: &compose.Health{Path: "/old"}},
		"api": {Identities: []app.Identity{{Name: "bella", Audience: []string{"a"}}}, Health: &compose.Health{Path: "/x"}},
	}}
	got := withHealth(file, "web", "", 0, 0)
	if _, ok := got.Services["web"]; ok {
		t.Errorf("web = %+v, want no entry", got.Services["web"])
	}
	got = withHealth(file, "api", "", 0, 0)
	if o := got.Services["api"]; o.Health != nil || len(o.Identities) != 1 {
		t.Errorf("api = %+v, want its identities and no health", o)
	}
	if file.Services["web"].Health == nil {
		t.Error("withHealth changed its argument")
	}
}

func TestSetHealthIfRefusals(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, whoami())
	n := len(mustDeployments(t, e))
	var fe *app.FieldError
	if _, err := e.svc.SetHealthIf(ctx, "whoami", "nope", "/", 0, 0, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "service" {
		t.Errorf("an unknown service: %v", err)
	}
	if _, err := e.svc.SetHealthIf(ctx, "ghost", "web", "/", 0, 0, Condition{}, app.TriggerConsole); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown app: %v", err)
	}
	stopDomainApp(t, e, "whoami")
	if _, err := e.svc.SetHealthIf(ctx, "whoami", "web", "/", 0, 0, Condition{}, app.TriggerConsole); !errors.Is(err, ErrAppStopped) {
		t.Errorf("a stopped app: %v, want ErrAppStopped", err)
	}
	if len(mustDeployments(t, e)) != n {
		t.Error("a refused change deployed")
	}

	t.Run("a service with no port", func(t *testing.T) {
		e := newEnv(t)
		a := kuma(true)
		a.Services[0].Backup = nil
		deployHealthy(t, e, a)
		if _, err := e.svc.SetHealthIf(ctx, "whoami", "web", "/", 0, 0, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "service" {
			t.Errorf("no port: %v, want a field error on service", err)
		}
	})
}
