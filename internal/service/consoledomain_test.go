package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const consoleDomain = "caravana.example.com"

// consoleMessage is the refusal's message for a domain written as host.
func consoleMessage(host string) string {
	return host + " is the console's own address (CARAVANA_PUBLIC_URL): " +
		"an app can't use it, or the console would stop answering. Use another domain."
}

// consoleClash is whoami with a second domain, host: a clash is then reported at
// services[0].domains[1].host, not at the first domain.
func consoleClash(host string) app.App {
	a := whoami()
	a.Services[0].Domains = append(a.Services[0].Domains, app.Domain{Host: host, Port: "http"})
	return a
}

// wantConsoleRefusal checks err is the console-domain field error at field.
func wantConsoleRefusal(t *testing.T, err error, field string) {
	t.Helper()
	wantConsoleRefusalFor(t, err, field, consoleDomain)
}

// wantConsoleRefusalFor is wantConsoleRefusal for a domain written as host.
func wantConsoleRefusalFor(t *testing.T, err error, field, host string) {
	t.Helper()
	var fe *app.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("error = %v, want an *app.FieldError", err)
	}
	if want := consoleMessage(host); fe.Field != field || fe.Message != want {
		t.Errorf("error = %q on %q, want %q on %q", fe.Message, fe.Field, want, field)
	}
}

// The ways the console's address may be written in CARAVANA_PUBLIC_URL: the
// host is compared alone, ignoring case, a trailing dot, the port and a path.
var consoleURLs = []string{
	"https://caravana.example.com",
	"https://caravana.example.com/",
	"https://Caravana.Example.COM",
	"https://caravana.example.com.",
	"https://caravana.example.com:8443",
	"https://caravana.example.com:8443/console/",
	"http://caravana.example.com/",
}

func TestCheckConsoleDomain(t *testing.T) {
	tests := []struct {
		name      string
		publicURL string
		host      string
		refused   bool
	}{
		{"the same host", "https://caravana.example.com", "caravana.example.com", true},
		{"mixed case in the setting", "https://Caravana.Example.COM", "caravana.example.com", true},
		{"mixed case in the domain", "https://caravana.example.com", "Caravana.Example.com", true},
		{"trailing dot in the setting", "https://caravana.example.com.", "caravana.example.com", true},
		{"trailing dot in the domain", "https://caravana.example.com", "caravana.example.com.", true},
		{"port and path in the setting", "https://caravana.example.com:8443/console/", "caravana.example.com", true},
		{"no setting, no check", "", "caravana.example.com", false},
		{"another host", "https://caravana.example.com", "shop.example.com", false},
		{"a subdomain of the console's", "https://caravana.example.com", "app.caravana.example.com", false},
		{"the console's parent domain", "https://caravana.example.com", "example.com", false},
		{"a local console", "http://127.0.0.1:7070", "whoami.localhost", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{}
			s.SetPublicURL(tt.publicURL)
			err := s.checkConsoleDomain(consoleClash(tt.host))
			if !tt.refused {
				if err != nil {
					t.Errorf("checkConsoleDomain() = %v, want nil", err)
				}
				return
			}
			wantConsoleRefusalFor(t, err, "services[0].domains[1].host", tt.host)
		})
	}
}

// Every path that saves or deploys domains refuses the console's own, with the
// field error, and saves, records and registers nothing.
func TestConsoleDomainRefusedOnEveryPath(t *testing.T) {
	ctx := context.Background()
	composeReq := func(host string) compose.Request {
		return compose.Request{
			Compose: "services:\n  web:\n    image: traefik/whoami:latest\n    ports: [\"80\"]\n",
			Services: map[string]compose.Overlay{"web": {Domains: []compose.Domain{
				{Host: "blog.example.com"}, {Host: host}}}},
		}
	}
	paths := []struct {
		name  string
		field string
		run   func(e *env) error
	}{
		{"Deploy", "services[0].domains[1].host", func(e *env) error {
			_, err := e.svc.Deploy(ctx, consoleClash(consoleDomain), app.TriggerManual)
			return err
		}},
		{"DeployIf", "services[0].domains[1].host", func(e *env) error {
			_, err := e.svc.DeployIf(ctx, consoleClash(consoleDomain), Condition{IfNew: true}, app.TriggerManual)
			return err
		}},
		{"CreateApp", "services[0].domains[1].host", func(e *env) error {
			_, err := e.svc.CreateApp(ctx, consoleClash(consoleDomain), app.TriggerManual)
			return err
		}},
		{"CreateAppOnly", "services[0].domains[1].host", func(e *env) error {
			_, err := e.svc.CreateAppOnly(ctx, consoleClash(consoleDomain))
			return err
		}},
		{"DeployCompose", "services.web.domains[1].host", func(e *env) error {
			_, err := e.svc.DeployCompose(ctx, "whoami", composeReq(consoleDomain), app.TriggerManual)
			return err
		}},
		{"PreviewCompose", "services.web.domains[1].host", func(e *env) error {
			_, _, err := e.svc.PreviewCompose(ctx, "whoami", composeReq(consoleDomain))
			return err
		}},
	}
	for _, p := range paths {
		for _, publicURL := range consoleURLs {
			t.Run(p.name+" "+publicURL, func(t *testing.T) {
				e := newEnv(t)
				e.svc.SetPublicURL(publicURL)
				wantConsoleRefusal(t, p.run(e), p.field)
				if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("GetApp = %v, want store.ErrNotFound: nothing may be saved", err)
				}
				if n := e.nomad.Registrations(); n != 0 {
					t.Errorf("Nomad registrations = %d, want none", n)
				}
			})
		}
	}

	t.Run("a stored edit is refused and the stored app is kept", func(t *testing.T) {
		e := newEnv(t)
		healthyWhoami(t, e)
		e.svc.SetPublicURL("https://" + consoleDomain)
		stored, err := e.svc.GetApp(ctx, "whoami")
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.svc.DeployIf(ctx, consoleClash(consoleDomain), Condition{IfRevision: stored.Revision}, app.TriggerConsole)
		wantConsoleRefusal(t, err, "services[0].domains[1].host")
		untouched(t, e, "whoami", stored.Revision, 1)
	})
}

// Domains that are fine are not refused, whatever the setting.
func TestConsoleDomainAllowed(t *testing.T) {
	ctx := context.Background()
	for _, host := range []string{"app.caravana.example.com", "caravana.example.org", "example.com"} {
		t.Run(host, func(t *testing.T) {
			e := newEnv(t)
			e.svc.SetPublicURL("https://" + consoleDomain)
			if _, err := e.svc.CreateAppOnly(ctx, consoleClash(host)); err != nil {
				t.Fatalf("CreateAppOnly() = %v, want it saved", err)
			}
			if _, err := e.svc.Deploy(ctx, consoleClash(host), app.TriggerManual); err != nil {
				t.Fatalf("Deploy() = %v, want it deployed", err)
			}
		})
	}
	t.Run("no setting", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Deploy(ctx, consoleClash(consoleDomain), app.TriggerManual); err != nil {
			t.Fatalf("Deploy() = %v, want no check without CARAVANA_PUBLIC_URL", err)
		}
	})
}

// An app saved before the check, with the console's domain, is refused on its
// next redeploy (so on its webhook's too) with the same error, and nothing is
// deployed.
func TestConsoleDomainRefusedOnRedeploy(t *testing.T) {
	ctx := context.Background()

	t.Run("Redeploy", func(t *testing.T) {
		e := newEnv(t)
		deployHealthy(t, e, consoleClash(consoleDomain)) // saved with no setting, as before the fix
		e.svc.SetPublicURL("https://" + consoleDomain)
		before := e.nomad.Registrations()
		_, err := e.svc.Redeploy(ctx, "whoami", app.TriggerManual)
		wantConsoleRefusal(t, err, "services[0].domains[1].host")
		untouched(t, e, "whoami", mustRevision(t, e, "whoami"), 1)
		if n := e.nomad.Registrations(); n != before {
			t.Errorf("Nomad registrations = %d, want %d", n, before)
		}
	})

	t.Run("a webhook that finds a new image", func(t *testing.T) {
		e := newEnv(t)
		deployHealthy(t, e, consoleClash(consoleDomain))
		e.svc.SetPublicURL("https://" + consoleDomain)
		e.registry.AddIndex("traefik/whoami", "latest", movedDigest)
		_, err := e.svc.WebhookRedeploy(ctx, "whoami")
		wantConsoleRefusal(t, err, "services[0].domains[1].host")
		untouched(t, e, "whoami", mustRevision(t, e, "whoami"), 1)
	})

	t.Run("RedeployVersion", func(t *testing.T) {
		e := newEnv(t)
		rec := deployHealthy(t, e, consoleClash(consoleDomain))
		e.svc.SetPublicURL("https://" + consoleDomain)
		_, err := e.svc.RedeployVersion(ctx, "whoami", rec.Deployment.ID, app.TriggerManual)
		wantConsoleRefusal(t, err, "services[0].domains[1].host")
	})
}

// A promotion keeps the target's domains, so it can't bring the console's in;
// a target that already has it (saved before the check) is refused by the
// preview and by Promote, and nothing is deployed.
func TestConsoleDomainPromote(t *testing.T) {
	ctx := context.Background()

	t.Run("a target with the console's domain", func(t *testing.T) {
		e := shopEnv(t)
		prod := production()
		prod.Services[0].Domains = []app.Domain{{Host: consoleDomain, Port: "http"}}
		setDestination(t, e)
		deployHealthy(t, e, prod) // before the setting
		deployHealthy(t, e, staging())
		e.svc.SetPublicURL("https://" + consoleDomain)
		rev := mustRevision(t, e, productionID)

		_, err := e.svc.PreviewPromotion(ctx, stagingID, "production")
		wantConsoleRefusal(t, err, "services[0].domains[0].host")
		_, err = e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerConsole)
		wantConsoleRefusal(t, err, "services[0].domains[0].host")
		untouched(t, e, productionID, rev, 1)
	})

	t.Run("staging's own domain is not carried over", func(t *testing.T) {
		e := shopEnv(t)
		stage := staging()
		stage.Services[0].Domains = []app.Domain{{Host: consoleDomain, Port: "http"}}
		setDestination(t, e)
		deployHealthy(t, e, production())
		deployHealthy(t, e, stage) // before the setting
		e.svc.SetPublicURL("https://" + consoleDomain)

		rec, err := e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerConsole)
		if err != nil {
			t.Fatalf("Promote() = %v, want production to keep its own domain", err)
		}
		if got := storedService(t, e, productionID).Domains; len(got) != 1 || got[0].Host != "shop.example.com" {
			t.Errorf("production's domains = %+v, want its own", got)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
	})
}

// CopyApp removes domains, so the copy can't carry the console's.
func TestConsoleDomainCopyApp(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	prod := production()
	prod.Services[0].Domains = []app.Domain{{Host: consoleDomain, Port: "http"}}
	setDestination(t, e)
	deployHealthy(t, e, prod)
	e.svc.SetPublicURL("https://" + consoleDomain)
	rec, _, err := e.svc.CopyApp(ctx, productionID, "staging")
	if err != nil {
		t.Fatalf("CopyApp() = %v", err)
	}
	for _, svc := range rec.App.Services {
		if len(svc.Domains) != 0 || strings.Contains(svc.Name, consoleDomain) {
			t.Errorf("copy kept domains %+v, want none", svc.Domains)
		}
	}
}

// mustRevision is the stored app's revision.
func mustRevision(t *testing.T, e *env, id string) string {
	t.Helper()
	rec, err := e.svc.GetApp(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Revision
}
