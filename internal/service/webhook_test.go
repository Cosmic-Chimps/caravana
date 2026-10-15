package service

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

var tokenRE = regexp.MustCompile(`^cvwh_[A-Za-z0-9_-]{43}$`)

func TestWebhookTokenHelpers(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		tok, err := newWebhookToken()
		if err != nil {
			t.Fatalf("newWebhookToken: %v", err)
		}
		if !tokenRE.MatchString(tok) {
			t.Fatalf("token %.9s… doesn't match %s", tok, tokenRE)
		}
		if seen[tok] {
			t.Fatal("the same token twice")
		}
		seen[tok] = true
	}

	tok, _ := newWebhookToken()
	other, _ := newWebhookToken()
	hash := hashToken(tok)
	// The same token with its last character changed (never to itself).
	last := "x"
	if strings.HasSuffix(tok, "x") {
		last = "y"
	}
	nearly := tok[:len(tok)-1] + last
	if len(hash) != 64 || strings.Contains(hash, tok) || strings.Trim(hash, "0123456789abcdef") != "" {
		t.Errorf("hash = %q, want 64 lowercase hex characters, not the token", hash)
	}

	tests := []struct {
		name              string
		stored, presented string
		want              bool
	}{
		{"same token", hash, tok, true},
		{"another token", hash, other, false},
		{"token with a changed last character", hash, nearly, false},
		{"empty", hash, "", false},
		{"malformed", hash, "not-a-token", false},
		{"the stored hash itself", hash, hash, false},
		{"no stored token", "", tok, false},
		{"no stored token, empty", "", "", false},
		{"the dummy's own input", "", "caravana: no webhook token", false},
		{"garbage stored", "zz", tok, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tokenMatches(tc.stored, tc.presented); got != tc.want {
				t.Errorf("tokenMatches = %v, want %v", got, tc.want)
			}
		})
	}
}

// healthyWhoami deploys whoami through the API path and waits for healthy.
func healthyWhoami(t *testing.T, e *env) store.DeploymentRecord {
	t.Helper()
	rec, err := e.svc.Deploy(context.Background(), whoami(), app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	return waitFinal(t, e.store, rec.Deployment.ID)
}

func deploymentCount(t *testing.T, e *env) int {
	t.Helper()
	list, err := e.store.ListDeployments(context.Background(), "whoami", 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

// US1: create a token, redeploy when something changed, skip when not.
func TestWebhook(t *testing.T) {
	ctx := context.Background()

	t.Run("create", func(t *testing.T) {
		e := newEnv(t)
		healthyWhoami(t, e)
		tok, err := e.svc.CreateWebhookToken(ctx, "whoami")
		if err != nil {
			t.Fatalf("CreateWebhookToken: %v", err)
		}
		if !tokenRE.MatchString(tok.Token) || tok.Path != "/v1/webhooks/whoami?token="+tok.Token || tok.CreatedAt.IsZero() {
			t.Errorf("token %.9s…, path %q, created %v", tok.Token, strings.Replace(tok.Path, tok.Token, "<token>", 1), tok.CreatedAt)
		}
		if rec, _ := e.store.GetWebhookToken(ctx, "whoami"); rec.Hash != hashToken(tok.Token) {
			t.Error("the store doesn't hold the token's hash")
		}
		if _, err := e.svc.CreateWebhookToken(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("CreateWebhookToken(ghost) = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("unchanged push deploys nothing", func(t *testing.T) {
		e := newEnv(t)
		live := healthyWhoami(t, e)
		tok, _ := e.svc.CreateWebhookToken(ctx, "whoami")
		before, regs := deploymentCount(t, e), e.nomad.Registrations()

		res, err := e.svc.Webhook(ctx, "whoami", tok.Token)
		if err != nil {
			t.Fatalf("Webhook: %v", err)
		}
		if res.Changed || res.Deployment.Deployment.ID != live.Deployment.ID {
			t.Errorf("result = changed %v, deployment %s; want unchanged, the live %s", res.Changed, res.Deployment.Deployment.ID, live.Deployment.ID)
		}
		if deploymentCount(t, e) != before || e.nomad.Registrations() != regs {
			t.Error("an unchanged push created a deployment or a job version")
		}
	})

	t.Run("moved tag redeploys", func(t *testing.T) {
		e := newEnv(t)
		healthyWhoami(t, e)
		tok, _ := e.svc.CreateWebhookToken(ctx, "whoami")
		e.registry.AddIndex("traefik/whoami", "latest", movedDigest)

		res, err := e.svc.Webhook(ctx, "whoami", tok.Token)
		if err != nil {
			t.Fatalf("Webhook: %v", err)
		}
		d := res.Deployment
		if !res.Changed || d.Deployment.Trigger != app.TriggerWebhook || d.Deployment.Digests["web"] != "traefik/whoami@"+movedDigest {
			t.Errorf("result = changed %v, trigger %s, digest %s; want a webhook deploy of the moved digest",
				res.Changed, d.Deployment.Trigger, d.Deployment.Digests["web"])
		}
		if final := waitFinal(t, e.store, d.Deployment.ID); final.Status != store.StatusHealthy {
			t.Errorf("final = %s, want healthy", final.Status)
		}
		if stored, _ := e.svc.GetApp(ctx, "whoami"); stored.App.Services[0].Image != "traefik/whoami:latest" {
			t.Errorf("stored image = %q, want the tag as written", stored.App.Services[0].Image)
		}
	})

	t.Run("no healthy deployment yet redeploys", func(t *testing.T) {
		e := newEnv(t)
		e.nomad.Default() // rollouts never finish
		if _, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		tok, _ := e.svc.CreateWebhookToken(ctx, "whoami")
		res, err := e.svc.Webhook(ctx, "whoami", tok.Token)
		if err != nil || !res.Changed {
			t.Errorf("Webhook = changed %v, %v; want a redeploy", res.Changed, err)
		}
	})

	t.Run("app edited since the healthy deployment redeploys", func(t *testing.T) {
		e := newEnv(t)
		healthyWhoami(t, e)
		tok, _ := e.svc.CreateWebhookToken(ctx, "whoami")
		edited := whoami()
		edited.Services[0].Replicas = new(3)
		if _, err := e.store.SaveApp(ctx, edited, nil); err != nil {
			t.Fatal(err)
		}
		res, err := e.svc.Webhook(ctx, "whoami", tok.Token)
		if err != nil || !res.Changed {
			t.Errorf("Webhook = changed %v, %v; want a redeploy with the same digests", res.Changed, err)
		}
	})
}

// US2: every authorization failure is ErrUnauthorized and deploys nothing.
func TestWebhookUnauthorized(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	healthyWhoami(t, e)
	tok, _ := e.svc.CreateWebhookToken(ctx, "whoami")

	other := whoami()
	other.ID, other.Name = "other", "other"
	other.Services[0].Domains[0].Host = "other.localhost"
	if _, err := e.store.SaveApp(ctx, other, nil); err != nil {
		t.Fatal(err)
	}
	otherTok, _ := e.svc.CreateWebhookToken(ctx, "other")
	stored, _ := e.store.GetWebhookToken(ctx, "whoami")
	noToken := whoami()
	noToken.ID, noToken.Name = "plain", "plain"
	noToken.Services[0].Domains[0].Host = "plain.localhost"
	if _, err := e.store.SaveApp(ctx, noToken, nil); err != nil {
		t.Fatal(err)
	}

	before, regs := deploymentCount(t, e), e.nomad.Registrations()
	for name, call := range map[string][2]string{
		"no token":           {"whoami", ""},
		"wrong token":        {"whoami", "cvwh_" + strings.Repeat("A", 43)},
		"another app's":      {"whoami", otherTok.Token},
		"wrong prefix":       {"whoami", "xxxx_" + tok.Token[5:]},
		"too short":          {"whoami", tok.Token[:20]},
		"not base64":         {"whoami", "cvwh_" + strings.Repeat("!", 43)},
		"the stored hash":    {"whoami", stored.Hash},
		"app without token":  {"plain", tok.Token},
		"unknown app":        {"ghost", tok.Token},
		"unknown app, empty": {"ghost", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := e.svc.Webhook(ctx, call[0], call[1]); err != ErrUnauthorized {
				t.Errorf("Webhook = %v, want exactly ErrUnauthorized", err)
			}
		})
	}
	if deploymentCount(t, e) != before || e.nomad.Registrations() != regs {
		t.Error("a refused call created a deployment or a job version")
	}
}

// US3: rotate, status, delete.
func TestWebhookTokenLifecycle(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	healthyWhoami(t, e)

	if st, err := e.svc.WebhookStatus(ctx, "whoami"); err != nil || st.Enabled {
		t.Errorf("status before = %+v, %v; want disabled", st, err)
	}
	first, _ := e.svc.CreateWebhookToken(ctx, "whoami")
	second, _ := e.svc.CreateWebhookToken(ctx, "whoami")
	if err := e.svc.CheckWebhookToken(ctx, "whoami", first.Token); err != ErrUnauthorized {
		t.Errorf("old token after rotation = %v, want ErrUnauthorized", err)
	}
	if err := e.svc.CheckWebhookToken(ctx, "whoami", second.Token); err != nil {
		t.Errorf("new token = %v, want nil", err)
	}
	if st, err := e.svc.WebhookStatus(ctx, "whoami"); err != nil || !st.Enabled || !st.CreatedAt.Equal(second.CreatedAt) {
		t.Errorf("status = %+v, %v; want enabled at %v", st, err, second.CreatedAt)
	}

	if err := e.svc.DeleteWebhookToken(ctx, "whoami"); err != nil {
		t.Fatalf("DeleteWebhookToken: %v", err)
	}
	if err := e.svc.CheckWebhookToken(ctx, "whoami", second.Token); err != ErrUnauthorized {
		t.Errorf("token after delete = %v, want ErrUnauthorized", err)
	}
	if st, _ := e.svc.WebhookStatus(ctx, "whoami"); st.Enabled {
		t.Error("status after delete is enabled")
	}
	if err := e.svc.DeleteWebhookToken(ctx, "whoami"); err != nil {
		t.Errorf("second delete = %v, want nil", err)
	}
	if _, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual); err != nil {
		t.Errorf("Deploy after delete = %v, want it to keep working", err)
	}

	for name, f := range map[string]func() error{
		"status": func() error { _, err := e.svc.WebhookStatus(ctx, "ghost"); return err },
		"delete": func() error { return e.svc.DeleteWebhookToken(ctx, "ghost") },
		"create": func() error { _, err := e.svc.CreateWebhookToken(ctx, "ghost"); return err },
	} {
		if err := f(); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s(ghost) = %v, want store.ErrNotFound", name, err)
		}
	}
}

// US4: webhook deploys keep every feature 003 rule.
func TestWebhookDeployOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("newest wins", func(t *testing.T) {
		e := newEnv(t)
		healthyWhoami(t, e)
		tok, _ := e.svc.CreateWebhookToken(ctx, "whoami")
		e.registry.AddIndex("traefik/whoami", "latest", movedDigest)
		// New rollouts stay running until both calls are in, so the older one
		// can't finish first; Nomad cancels it when the newer one registers.
		e.nomad.Default(api.Deployment{Status: api.DeploymentStatusRunning})
		release := e.registry.Hold("traefik/whoami", "latest")

		type result struct {
			res WebhookResult
			err error
		}
		first := make(chan result, 1)
		go func() { r, err := e.svc.Webhook(ctx, "whoami", tok.Token); first <- result{r, err} }()
		waitFor(t, "the first call to reach the registry", func() bool { return e.registry.Requests() >= 2 })
		second := make(chan result, 1)
		go func() { r, err := e.svc.Webhook(ctx, "whoami", tok.Token); second <- result{r, err} }()
		time.Sleep(50 * time.Millisecond) // the second call is now waiting on the registry too
		release()

		a, b := <-first, <-second
		if a.err != nil || b.err != nil {
			t.Fatalf("errors: %v, %v", a.err, b.err)
		}
		// Let the version Nomad runs now (the newest) finish its rollout.
		e.nomad.Script("whoami", *e.nomad.Job("whoami").Version, api.Deployment{Status: api.DeploymentStatusSuccessful})
		statuses := map[store.Status]int{}
		for _, r := range []result{a, b} {
			statuses[waitFinal(t, e.store, r.res.Deployment.Deployment.ID).Status]++
		}
		if statuses[store.StatusHealthy] != 1 || statuses[store.StatusSuperseded] != 1 {
			t.Errorf("final statuses = %v, want one healthy and one superseded", statuses)
		}
	})

	tests := []struct {
		name  string
		setup func(e *env)
		want  error
	}{
		{"missing tag", func(e *env) {
			a := whoami()
			a.Services[0].Image = "traefik/whoami:nope"
			if _, err := e.store.SaveApp(context.Background(), a, nil); err != nil {
				t.Fatal(err)
			}
		}, deploy.ErrImageNotFound},
		{"registry down", func(e *env) { e.registry.Fail("traefik/whoami", "latest", http.StatusBadGateway) }, deploy.ErrRegistryUnavailable},
		{"nomad down", func(e *env) {
			e.registry.AddIndex("traefik/whoami", "latest", movedDigest)
			e.nomad.Down()
		}, deploy.ErrNomadUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			healthyWhoami(t, e)
			tok, _ := e.svc.CreateWebhookToken(ctx, "whoami")
			tc.setup(e)
			_, err := e.svc.Webhook(ctx, "whoami", tok.Token)
			var de *DeployError
			if !errors.As(err, &de) || !errors.Is(err, tc.want) {
				t.Fatalf("Webhook = %v, want a *DeployError matching %v", err, tc.want)
			}
			rec, _ := e.store.GetDeployment(ctx, de.DeploymentID)
			if rec.Status != store.StatusFailed || rec.Deployment.Trigger != app.TriggerWebhook {
				t.Errorf("record = %s, trigger %s; want failed, webhook", rec.Status, rec.Deployment.Trigger)
			}
		})
	}
}

// Feature 015: a webhook redeploy of a Compose app keeps its file: the app
// still has it, the new deployment records it, and there is no drop warning.
func TestWebhookKeepsFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	composeHealthy(t, e, compose.Request{Compose: keptFile})
	tok, _ := e.svc.CreateWebhookToken(ctx, "whoami")
	e.registry.AddIndex("traefik/whoami", "latest", movedDigest)

	res, err := e.svc.Webhook(ctx, "whoami", tok.Token)
	if err != nil || !res.Changed {
		t.Fatalf("Webhook = changed %v, %v; want a redeploy", res.Changed, err)
	}
	waitFinal(t, e.store, res.Deployment.Deployment.ID)
	if n := droppedFile(res.Deployment); n != 0 {
		t.Errorf("warnings = %+v, want no drop warning", res.Deployment.Warnings)
	}
	if dep, _ := e.svc.GetDeployment(ctx, "whoami", res.Deployment.Deployment.ID); dep.Compose == nil || dep.Compose.Compose != keptFile {
		t.Errorf("the webhook deployment records %+v, want the kept file", dep.Compose)
	}
	if stored, _ := e.svc.GetApp(ctx, "whoami"); stored.Compose == nil || stored.Compose.Compose != keptFile {
		t.Errorf("kept file after the webhook = %+v, want it unchanged", stored.Compose)
	}
}
