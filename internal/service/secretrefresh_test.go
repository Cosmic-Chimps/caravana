package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func TestRefreshSecretsChanged(t *testing.T) {
	ctx := context.Background()
	e, src := refEnv(t)
	live := deployHealthy(t, e, withSecrets(fromBella("DATABASE_URL", "DATABASE_URL"), set("TYPED", "t")))

	src.Set("DATABASE_URL", "rotated")
	got, err := e.svc.RefreshSecrets(ctx, "whoami", app.TriggerManual)
	if err != nil || !got.Changed || got.Deployment == nil {
		t.Fatalf("RefreshSecrets = %+v, %v; want a changed refresh with a deployment", got, err)
	}
	rec := *got.Deployment
	if !rec.Deployment.Refresh || rec.Source != live.Deployment.ID || rec.Deployment.Trigger != app.TriggerManual {
		t.Errorf("record: refresh %v, source %q, trigger %s; want a refresh of %s", rec.Deployment.Refresh, rec.Source, rec.Deployment.Trigger, live.Deployment.ID)
	}
	if rec.Deployment.Digests["web"] != live.Deployment.Digests["web"] {
		t.Errorf("digest = %q, want the live one %q", rec.Deployment.Digests["web"], live.Deployment.Digests["web"])
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy || !final.Deployment.Refresh {
		t.Fatalf("final = %s (%s), refresh %v; want healthy", final.Status, final.Error, final.Deployment.Refresh)
	}
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"DATABASE_URL": "rotated", "TYPED": "t"}}) {
		t.Errorf("live = %v, want the new value and the typed one kept", got)
	}
	waitFor(t, "the secrets to be committed", func() bool { owner, _ := e.secrets.Kept("whoami"); return owner == "" })
	waitFor(t, "the deploy to let go of the lock", func() bool {
		st := e.svc.stateOf("whoami")
		if !st.mu.TryLock() {
			return false
		}
		st.mu.Unlock()
		return true
	})

	// Now nothing changed.
	regs, deploys := e.nomad.Registrations(), deployCount(e, "whoami")
	again, err := e.svc.RefreshSecrets(ctx, "whoami", app.TriggerManual)
	if err != nil || again.Changed || again.Deployment != nil {
		t.Fatalf("second RefreshSecrets = %+v, %v; want unchanged", again, err)
	}
	if e.nomad.Registrations() != regs || deployCount(e, "whoami") != deploys {
		t.Error("an unchanged refresh deployed or recorded something")
	}
}

func TestRefreshSecretsRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("no references", func(t *testing.T) {
		e, _ := refEnv(t)
		deployHealthy(t, e, withSecrets(set("TYPED", "t")))
		if _, err := e.svc.RefreshSecrets(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrNoSecretReferences) {
			t.Errorf("RefreshSecrets = %v, want ErrNoSecretReferences", err)
		}
	})

	t.Run("no live version", func(t *testing.T) {
		e, _ := refEnv(t)
		a := withSecrets(fromBella("K", "API_KEY"))
		if _, err := e.store.InsertApp(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.RefreshSecrets(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrNoLiveDeployment) {
			t.Errorf("RefreshSecrets = %v, want ErrNoLiveDeployment", err)
		}
	})

	t.Run("not defined", func(t *testing.T) {
		e, _ := refEnv(t)
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "empty", Source: app.Source{Kind: app.SourceImage}}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.RefreshSecrets(ctx, "empty", app.TriggerManual); !errors.Is(err, ErrAppNotDefined) {
			t.Errorf("RefreshSecrets = %v, want ErrAppNotDefined", err)
		}
	})

	t.Run("unknown app", func(t *testing.T) {
		e, _ := refEnv(t)
		if _, err := e.svc.RefreshSecrets(ctx, "nope", app.TriggerManual); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RefreshSecrets = %v, want ErrNotFound", err)
		}
	})

	t.Run("stopped", func(t *testing.T) {
		e, _ := refEnv(t)
		deployHealthy(t, e, withSecrets(fromBella("K", "API_KEY")))
		if _, err := e.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.RefreshSecrets(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrAppStopped) {
			t.Errorf("RefreshSecrets = %v, want ErrAppStopped", err)
		}
	})

	t.Run("a provider that is down or refuses deploys nothing", func(t *testing.T) {
		for name, fail := range map[string]struct {
			set  func(*testing.T, *env)
			want error
		}{
			"down":      {func(t *testing.T, e *env) { e.sources.source("bella-prod").SetDown(true) }, secrets.ErrSourceUnavailable},
			"refused":   {func(t *testing.T, e *env) { e.sources.source("bella-prod").SetRefused(true) }, secrets.ErrSourceRefused},
			"path gone": {func(t *testing.T, e *env) { e.sources.source("bella-prod").Delete("API_KEY") }, ErrSecretNotFound},
		} {
			e, _ := refEnv(t)
			deployHealthy(t, e, withSecrets(fromBella("K", "API_KEY")))
			regs, deploys := e.nomad.Registrations(), deployCount(e, "whoami")
			fail.set(t, e)
			_, err := e.svc.RefreshSecrets(ctx, "whoami", app.TriggerManual)
			var ref *ReferenceError
			if !errors.As(err, &ref) || !errors.Is(err, fail.want) {
				t.Errorf("%s: RefreshSecrets = %v, want a ReferenceError matching %v", name, err, fail.want)
			}
			if e.nomad.Registrations() != regs || deployCount(e, "whoami") != deploys {
				t.Errorf("%s: something was deployed or recorded", name)
			}
			if strings.Contains(errString(err), refSentinel) {
				t.Errorf("%s: the error holds a value", name)
			}
		}
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
