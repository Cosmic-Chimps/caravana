package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// domainFile is a kept Compose file with one service on one port.
const domainFile = "# the shop\nservices:\n  web:\n    image: traefik/whoami:latest  # pinned later\n    ports: [\"8080:80\"]\n"

func TestSetDomainsIfDefinition(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, domainApp("shop", app.Domain{Host: "shop.example.com"}))
	before, _ := e.svc.GetApp(ctx, "shop")

	domains := []app.Domain{
		{Host: "Shop.Example.com", Port: "http"},
		{Host: "admin.example.com", Port: "http", TeamOnly: true, Path: "/admin"},
	}
	res, err := e.svc.SetDomainsIf(ctx, "shop", "web", domains, Condition{IfRevision: before.Revision}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("SetDomainsIf: %v", err)
	}
	waitFinal(t, e.store, res.Deployment.ID)
	if res.TeamPasswords["admin.example.com"] == "" || len(res.TeamPasswords) != 1 {
		t.Errorf("TeamPasswords = %v, want admin.example.com's", res.TeamPasswords)
	}
	after, _ := e.svc.GetApp(ctx, "shop")
	want := []app.Domain{{Host: "shop.example.com", Port: "http"}, {Host: "admin.example.com", Port: "http", TeamOnly: true, Path: "/admin"}}
	if got := after.App.Services[0].Domains; !reflect.DeepEqual(got, want) {
		t.Errorf("domains = %+v, want %+v", got, want)
	}

	// A stale revision, an unknown service, a stopped app.
	if _, err := e.svc.SetDomainsIf(ctx, "shop", "web", nil, Condition{IfRevision: before.Revision}, app.TriggerConsole); !errors.Is(err, ErrAppChanged) {
		t.Errorf("stale revision = %v, want ErrAppChanged", err)
	}
	var fe *app.FieldError
	if _, err := e.svc.SetDomainsIf(ctx, "shop", "api", nil, Condition{}, app.TriggerConsole); !errors.As(err, &fe) || fe.Field != "service" {
		t.Errorf("unknown service = %v, want a field error on service", err)
	}
	if _, err := e.svc.SetDomainsIf(ctx, "nope", "web", nil, Condition{}, app.TriggerConsole); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown app = %v, want store.ErrNotFound", err)
	}
	stopDomainApp(t, e, "shop")
	if _, err := e.svc.SetDomainsIf(ctx, "shop", "web", nil, Condition{}, app.TriggerConsole); !errors.Is(err, ErrAppStopped) {
		t.Errorf("stopped app = %v, want ErrAppStopped", err)
	}
}

func TestSetDomainsIfKeptFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	req := compose.Request{Compose: domainFile, Services: map[string]compose.Overlay{
		"web": {
			Domains: []compose.Domain{{Host: "shop.example.com"}},
			Secrets: []app.Secret{{Name: "TOKEN", Value: new(keptSentinel)}},
		},
	}}
	rec, err := e.svc.DeployComposeIf(ctx, "shop", req, Condition{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployComposeIf: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	before, _ := e.svc.GetApp(ctx, "shop")
	port := before.App.Services[0].Ports[0].Name

	domains := []app.Domain{
		{Host: "example.com", Port: port, WWWRedirect: true},
		{Host: "shop.example.com", Port: port, TeamOnly: true},
	}
	res, err := e.svc.SetDomainsIf(ctx, "shop", "web", domains, Condition{IfRevision: before.Revision}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("SetDomainsIf: %v", err)
	}
	final := waitFinal(t, e.store, res.Deployment.ID)
	if final.Status != store.StatusHealthy {
		t.Fatalf("deployment = %s (%s)", final.Status, final.Error)
	}
	if droppedFile(final) != 0 {
		t.Errorf("warnings = %+v: the file was dropped", final.Warnings)
	}
	if res.TeamPasswords["shop.example.com"] == "" {
		t.Errorf("TeamPasswords = %v, want shop.example.com's", res.TeamPasswords)
	}

	after, _ := e.svc.GetApp(ctx, "shop")
	if after.Compose == nil || after.Compose.Compose != domainFile {
		t.Fatalf("kept file = %+v, want the same text", after.Compose)
	}
	wantOverlay := []compose.Domain{{Host: "example.com", WWWRedirect: true}, {Host: "shop.example.com", TeamOnly: true}}
	if got := after.Compose.Services["web"].Domains; !reflect.DeepEqual(got, wantOverlay) {
		t.Errorf("overlay domains = %+v, want %+v", got, wantOverlay)
	}
	if got := after.Compose.Services["web"].Secrets; !reflect.DeepEqual(got, []app.Secret{{Name: "TOKEN"}}) {
		t.Errorf("overlay secrets = %+v, want the name TOKEN only", got)
	}
	if !reflect.DeepEqual(after.App.Services[0].Domains, domains) {
		t.Errorf("app domains = %+v, want %+v", after.App.Services[0].Domains, domains)
	}
	// The secret's value stays in the secret store and nowhere else.
	if v := e.secrets.Live("shop")["web"]["TOKEN"]; v != keptSentinel {
		t.Errorf("TOKEN = %q, want its value kept", v)
	}
	dep, _ := e.svc.GetDeployment(ctx, "shop", res.Deployment.ID)
	for what, v := range map[string]any{"the app": after, "the deployment": dep, "the answer": res} {
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), keptSentinel) {
			t.Errorf("%s holds the secret value", what)
		}
	}

	// A port the service doesn't have names the domain's port.
	_, err = e.svc.SetDomainsIf(ctx, "shop", "web", []app.Domain{{Host: "x.example.com", Port: "nope"}}, Condition{}, app.TriggerConsole)
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "domains[0].port" {
		t.Errorf("unknown port = %v, want a field error on domains[0].port", err)
	}
}

func TestWithDomains(t *testing.T) {
	one := []app.Port{{Name: "http", ContainerPort: 80}}
	two := []app.Port{{Name: "http", ContainerPort: 80}, {Name: "admin", ContainerPort: 9000}}
	file := compose.Request{Compose: "x", Services: map[string]compose.Overlay{
		"web": {Domains: []compose.Domain{{Host: "a.example.com", Port: 80}}, Secrets: []app.Secret{{Name: "K", Value: new("v")}}},
	}}

	got, err := withDomains(file, "web", []app.Domain{{Host: "a.example.com", Port: "http"}, {Host: "b.example.com", Port: "http", Path: "/b"}}, one)
	if err != nil {
		t.Fatal(err)
	}
	want := []compose.Domain{{Host: "a.example.com", Port: 80}, {Host: "b.example.com", Path: "/b"}}
	if !reflect.DeepEqual(got.Services["web"].Domains, want) {
		t.Errorf("one port: %+v, want %+v (a named port kept, a new one short)", got.Services["web"].Domains, want)
	}
	if got.Services["web"].Secrets[0].Value != nil {
		t.Errorf("the copy holds a secret value")
	}
	if file.Services["web"].Domains[0].Host != "a.example.com" || len(file.Services["web"].Domains) != 1 || file.Services["web"].Secrets[0].Value == nil {
		t.Errorf("the request it started from changed: %+v", file)
	}

	got, err = withDomains(file, "api", []app.Domain{{Host: "api.example.com", Port: "admin"}}, two)
	if err != nil {
		t.Fatal(err)
	}
	if d := got.Services["api"].Domains; len(d) != 1 || d[0].Port != 9000 {
		t.Errorf("two ports: %+v, want port 9000", d)
	}

	got, err = withDomains(compose.Request{Compose: "x", Services: map[string]compose.Overlay{"web": {Domains: []compose.Domain{{Host: "a.example.com"}}}}}, "web", nil, one)
	if err != nil || got.Services != nil {
		t.Errorf("no domains left = %+v (%v), want no overlay at all", got.Services, err)
	}
}
