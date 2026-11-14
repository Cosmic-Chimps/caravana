package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// refSentinel is a value read from a provider that no record, error, stored
// app or Nomad job may contain.
const refSentinel = "s3cret-sentinel-037-svc"

func ref(provider, path string) *app.SecretRef { return &app.SecretRef{Provider: provider, Path: path} }

// refEnv is a test environment with a Bella provider called "bella-prod" that
// holds DATABASE_URL (the sentinel) and API_KEY.
func refEnv(t *testing.T, allowed ...string) (*env, *secretstest.Source) {
	t.Helper()
	e := newEnv(t)
	if _, err := e.svc.CreateProject(context.Background(), "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	in := bellaInput("bella-prod")
	in.Allowed = allowed
	if _, _, err := e.svc.SaveSecretProvider(context.Background(), in); err != nil {
		t.Fatalf("SaveSecretProvider: %v", err)
	}
	src := e.sources.source("bella-prod")
	src.Set("DATABASE_URL", refSentinel)
	src.Set("API_KEY", "k1")
	return e, src
}

func fromBella(name, path string) app.Secret {
	return app.Secret{Name: name, From: ref("bella-prod", path)}
}

func TestDeployWithReference(t *testing.T) {
	ctx := context.Background()
	e, src := refEnv(t)

	rec := deployHealthy(t, e, withSecrets(fromBella("DATABASE_URL", "DATABASE_URL"), fromBella("KEY", "API_KEY"), set("TYPED", "t")))
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"DATABASE_URL": refSentinel, "KEY": "k1", "TYPED": "t"}}) {
		t.Fatalf("live = %v, want the resolved and the typed values", got)
	}
	if n := src.Reads(); n != 1 {
		t.Errorf("the provider was read %d times, want once for both references", n)
	}

	// The reference is stored as written; the value is nowhere.
	stored, err := e.svc.GetApp(ctx, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	secs := stored.App.Services[0].Secrets
	if secs[0].From == nil || secs[0].From.Provider != "bella-prod" || secs[0].From.Path != "DATABASE_URL" || secs[0].Value != nil {
		t.Errorf("stored secret = %+v, want the reference only", secs[0])
	}
	list, _ := e.store.ListDeployments(ctx, "whoami", 0)
	docs, _ := json.Marshal(list)
	job, _ := json.Marshal(e.nomad.Job("whoami"))
	for what, text := range map[string]string{"stored app": mustJSON(stored), "deployments": string(docs), "Nomad job": string(job), "record error": rec.Error} {
		if strings.Contains(text, refSentinel) {
			t.Errorf("the %s holds the resolved value", what)
		}
	}
	if !strings.Contains(string(docs), `"provider":"bella-prod"`) {
		t.Error("the deployment record lost the reference")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// An unknown provider, or one not allowed for the app's place, is refused
// before anything is saved.
func TestReferenceStaticChecks(t *testing.T) {
	ctx := context.Background()

	t.Run("unknown provider", func(t *testing.T) {
		e, _ := refEnv(t)
		a := withSecrets(app.Secret{Name: "X", From: ref("nope", "X")})
		_, err := e.svc.Deploy(ctx, a, app.TriggerManual)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "services[0].secrets[0].from.provider" || !strings.Contains(fe.Message, `"nope"`) {
			t.Fatalf("Deploy = %v, want a field error on from.provider", err)
		}
		if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp = %v, want the app not saved", err)
		}
	})

	t.Run("not allowed for the app's place", func(t *testing.T) {
		e, src := refEnv(t, "shop")
		_, err := e.svc.Deploy(ctx, withSecrets(fromBella("K", "API_KEY")), app.TriggerManual)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "services[0].secrets[0].from.provider" || !strings.Contains(fe.Message, "default/production") {
			t.Fatalf("Deploy = %v, want a field error on from.provider naming the place", err)
		}
		if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp = %v, want the app not saved", err)
		}
		if src.Reads() != 0 {
			t.Error("the provider was read for an app that may not use it")
		}
	})

	t.Run("allowed by project or by project/environment", func(t *testing.T) {
		for _, allowed := range []string{"default", "default/production"} {
			e, _ := refEnv(t, allowed)
			if _, err := e.svc.Deploy(ctx, withSecrets(fromBella("K", "API_KEY")), app.TriggerManual); err != nil {
				t.Errorf("allowed %q: Deploy = %v", allowed, err)
			}
		}
	})

	t.Run("an app in another environment of an allowed project", func(t *testing.T) {
		e, _ := refEnv(t, "default/production")
		a := withSecrets(fromBella("K", "API_KEY"))
		a.ID = "default--qa--whoami"
		err := e.svc.checkReferences(ctx, a)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "services[0].secrets[0].from.provider" {
			t.Errorf("checkReferences = %v, want a refusal on from.provider", err)
		}
	})
}

// Each failure of the provider fails that deploy, names the field and the
// provider, leaves the values in the store as they were, and holds no value.
func TestReferenceFailures(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		change    func(*secretstest.Source)
		wantIs    error
		wantField string
		wantText  string
	}{
		{"a path the provider doesn't have", func(s *secretstest.Source) { s.Delete("API_KEY") },
			ErrSecretNotFound, "services[0].secrets[1].from.path", `provider "bella-prod" has no secret "API_KEY"`},
		{"a provider that refuses", func(s *secretstest.Source) { s.SetRefused(true) },
			secrets.ErrSourceRefused, "services[0].secrets[0].from", `secret provider "bella-prod"`},
		{"a provider that is down", func(s *secretstest.Source) { s.SetDown(true) },
			secrets.ErrSourceUnavailable, "services[0].secrets[0].from", `secret provider "bella-prod"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, src := refEnv(t)
			a := withSecrets(fromBella("DATABASE_URL", "DATABASE_URL"), fromBella("KEY", "API_KEY"))
			deployHealthy(t, e, a)
			live := e.secrets.Live("whoami")
			replaces, regs, deploys := e.secrets.Replaces(), e.nomad.Registrations(), deployCount(e, "whoami")

			tt.change(src)
			rec, err := e.svc.Deploy(ctx, a, app.TriggerManual)
			var ref *ReferenceError
			var de *DeployError
			if !errors.As(err, &ref) || !errors.As(err, &de) || !errors.Is(err, tt.wantIs) {
				t.Fatalf("Deploy = %v, want a DeployError with a ReferenceError matching %v", err, tt.wantIs)
			}
			if ref.Field != tt.wantField || ref.Provider != "bella-prod" {
				t.Errorf("ReferenceError = %+v, want field %s", ref, tt.wantField)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("error = %q, want it to say %q", err, tt.wantText)
			}
			if rec.Status != store.StatusFailed || rec.Error != err.Error() {
				t.Errorf("record = %s %q, want failed with the error", rec.Status, rec.Error)
			}
			if deployCount(e, "whoami") != deploys+1 {
				t.Error("the failed deploy was not recorded")
			}
			if got := e.secrets.Live("whoami"); !got.Equal(live) {
				t.Errorf("live = %v, want the running values %v", got, live)
			}
			if e.secrets.Replaces() != replaces || e.nomad.Registrations() != regs {
				t.Error("a secret was replaced or a job registered although the provider failed")
			}
			if strings.Contains(err.Error(), refSentinel) || strings.Contains(rec.Error, refSentinel) {
				t.Error("an error holds a value")
			}
		})
	}
}

// With the provider down, a new deploy fails but a copy restarted meanwhile
// starts with the values it had: the Nomad Variable is never touched.
func TestProviderDownLeavesRunningValues(t *testing.T) {
	ctx := context.Background()
	e, src := refEnv(t)
	a := withSecrets(fromBella("DATABASE_URL", "DATABASE_URL"))
	deployHealthy(t, e, a)

	src.SetDown(true)
	if _, err := e.svc.Deploy(ctx, a, app.TriggerManual); !errors.Is(err, secrets.ErrSourceUnavailable) {
		t.Fatalf("Deploy = %v, want unavailable", err)
	}
	// A redeploy fails the same way, and the stored values stay.
	if _, err := e.svc.Redeploy(ctx, "whoami", app.TriggerWebhook); !errors.Is(err, secrets.ErrSourceUnavailable) {
		t.Fatalf("Redeploy = %v, want unavailable", err)
	}
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"DATABASE_URL": refSentinel}}) {
		t.Errorf("live = %v, want the value it had", got)
	}
	// Back up: the next deploy works.
	src.SetDown(false)
	src.Set("DATABASE_URL", "new")
	deployHealthy(t, e, a)
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"DATABASE_URL": "new"}}) {
		t.Errorf("live = %v, want the new value", got)
	}
}

// Going back to an old version reads the provider now, not as it was.
func TestRollbackResolvesNow(t *testing.T) {
	ctx := context.Background()
	e, src := refEnv(t)
	first := deployHealthy(t, e, withEnv(withSecrets(fromBella("K", "API_KEY")), "a"))
	deployHealthy(t, e, withEnv(withSecrets(fromBella("K", "API_KEY")), "b"))

	src.Set("API_KEY", "rotated")
	rec, err := e.svc.RedeployVersion(ctx, "whoami", first.Deployment.ID, app.TriggerConsole)
	if err != nil {
		t.Fatalf("RedeployVersion = %v", err)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("final = %s (%s)", final.Status, final.Error)
	}
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"K": "rotated"}}) {
		t.Errorf("live = %v, want today's value", got)
	}
}

// A reference may replace a typed secret of the same name: the deploy's value
// wins over the stored one.
func TestReferenceReplacesStoredValue(t *testing.T) {
	e, _ := refEnv(t)
	deployHealthy(t, e, withSecrets(set("KEY", "typed")))
	deployHealthy(t, e, withSecrets(fromBella("KEY", "API_KEY")))
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"KEY": "k1"}}) {
		t.Errorf("live = %v, want the provider's value", got)
	}
}

func TestCopyAppSkipsReferencesInMissingSecrets(t *testing.T) {
	ctx := context.Background()
	e, _ := refEnv(t)
	if _, err := e.svc.CreateEnvironment(ctx, "default", "staging"); err != nil {
		t.Fatal(err)
	}
	deployHealthy(t, e, withSecrets(fromBella("DATABASE_URL", "DATABASE_URL"), set("TYPED_KEPT", "y")))
	_, missing, err := e.svc.CopyApp(ctx, "whoami", "staging")
	if err != nil {
		t.Fatalf("CopyApp = %v", err)
	}
	if len(missing) != 1 || missing[0].Name != "TYPED_KEPT" {
		t.Errorf("missing secrets = %+v, want only the typed one", missing)
	}
}
