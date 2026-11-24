package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Secret templates (feature 053): a value built from text and references.

// tplSentinel is a value a provider gives that no record, error, stored app,
// log line or Nomad job may contain.
const tplSentinel = "s3cret-sentinel-053"

// templated is a secret whose value is tpl with refs filled from the "bella-prod"
// provider; refs maps a name in the template to a path.
func templated(name, tpl string, refs map[string]string) app.Secret {
	out := map[string]app.SecretRef{}
	for n, p := range refs {
		out[n] = app.SecretRef{Provider: "bella-prod", Path: p}
	}
	return app.Secret{Name: name, Template: tpl, Refs: out}
}

func connString() app.Secret {
	return templated("ConnectionStrings__postgres", "Host=postgres;Username=banana;Password=${pw};Note=$$5",
		map[string]string{"pw": "DB_PASSWORD"})
}

func TestDeployWithTemplate(t *testing.T) {
	ctx := context.Background()
	e, src := refEnv(t)
	src.Set("DB_PASSWORD", tplSentinel+"$x${y}")
	logs := &lockedBuffer{}
	e.svc = startServiceLogging(t, e.store, e.svc.deployer, e.secrets, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	e.svc.SetSecretSources(e.sources.factory)

	rec := deployHealthy(t, e, withSecrets(connString(), fromBella("KEY", "API_KEY"), set("TYPED", "t")))
	want := "Host=postgres;Username=banana;Password=" + tplSentinel + "$x${y};Note=$5"
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"ConnectionStrings__postgres": want, "KEY": "k1", "TYPED": "t"}}) {
		t.Fatalf("live = %v, want the rendered, the read and the typed values (the value inserted as it is)", got)
	}
	if n := src.Reads(); n != 1 {
		t.Errorf("the provider was read %d times, want once for the template's ref and the `from`", n)
	}

	// The template and its refs are stored as written; no value is anywhere.
	stored, err := e.svc.GetApp(ctx, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	sec := stored.App.Services[0].Secrets[0]
	if sec.Template != connString().Template || sec.Refs["pw"].Path != "DB_PASSWORD" || sec.Value != nil || sec.From != nil {
		t.Errorf("stored secret = %+v, want the template and refs only", sec)
	}
	list, _ := e.store.ListDeployments(ctx, "whoami", 0)
	docs, _ := json.Marshal(list)
	job, _ := json.Marshal(e.nomad.Job("whoami"))
	for what, text := range map[string]string{"stored app": mustJSON(stored), "deployments": string(docs), "Nomad job": string(job),
		"record error": rec.Error, "log": logs.String(), "SQLite file and every row in it": rawStore(t, e.dir)} {
		if strings.Contains(text, tplSentinel) {
			t.Errorf("the %s holds a value", what)
		}
	}
	if !strings.Contains(string(docs), `"template":"Host=postgres`) {
		t.Error("the deployment record lost the template")
	}
	if HasSecretReferences(withSecrets(set("A", "b"))) || !HasSecretReferences(withSecrets(connString())) {
		t.Error("HasSecretReferences is wrong for a template")
	}
}

// A template with two refs of two providers reads each provider once.
func TestTemplateTwoProviders(t *testing.T) {
	ctx := context.Background()
	e, src := refEnv(t)
	src.Set("DB_USER", "banana")
	src.Set("DB_PASSWORD", "pw1")
	in := bellaInput("bella-two")
	if _, _, err := e.svc.SaveSecretProvider(ctx, in); err != nil {
		t.Fatal(err)
	}
	e.sources.source("bella-two").Set("HOST", "db.internal")

	sec := app.Secret{Name: "URL", Template: "postgres://${u}:${p}@${h}/app", Refs: map[string]app.SecretRef{
		"u": {Provider: "bella-prod", Path: "DB_USER"}, "p": {Provider: "bella-prod", Path: "DB_PASSWORD"}, "h": {Provider: "bella-two", Path: "HOST"}}}
	deployHealthy(t, e, withSecrets(sec))
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"URL": "postgres://banana:pw1@db.internal/app"}}) {
		t.Errorf("live = %v", got)
	}
	if src.Reads() != 1 || e.sources.source("bella-two").Reads() != 1 {
		t.Errorf("reads = %d and %d, want one each", src.Reads(), e.sources.source("bella-two").Reads())
	}
}

// Static checks and failures name the ref's field.
func TestTemplateChecksAndFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("unknown provider", func(t *testing.T) {
		e, _ := refEnv(t)
		sec := app.Secret{Name: "X", Template: "${a}", Refs: map[string]app.SecretRef{"a": {Provider: "nope", Path: "X"}}}
		_, err := e.svc.Deploy(ctx, withSecrets(sec), app.TriggerManual)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "services[0].secrets[0].refs.a.provider" || !strings.Contains(fe.Message, `"nope"`) {
			t.Fatalf("Deploy = %v, want a field error on refs.a.provider", err)
		}
		if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp = %v, want the app not saved", err)
		}
	})

	t.Run("not allowed for the app's place", func(t *testing.T) {
		e, src := refEnv(t, "shop")
		_, err := e.svc.Deploy(ctx, withSecrets(connString()), app.TriggerManual)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "services[0].secrets[0].refs.pw.provider" || !strings.Contains(fe.Message, "default/production") {
			t.Fatalf("Deploy = %v, want a field error on refs.pw.provider naming the place", err)
		}
		if src.Reads() != 0 {
			t.Error("the provider was read for an app that may not use it")
		}
	})

	tests := []struct {
		name      string
		change    func(*secretstest.Source)
		wantIs    error
		wantField string
	}{
		{"a path the provider doesn't have", func(s *secretstest.Source) { s.Delete("API_KEY") },
			ErrSecretNotFound, "services[0].secrets[1].refs.k.path"},
		{"a provider that refuses", func(s *secretstest.Source) { s.SetRefused(true) },
			secrets.ErrSourceRefused, "services[0].secrets[0].refs.pw"},
		{"a provider that is down", func(s *secretstest.Source) { s.SetDown(true) },
			secrets.ErrSourceUnavailable, "services[0].secrets[0].refs.pw"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, src := refEnv(t)
			src.Set("DB_PASSWORD", tplSentinel)
			a := withSecrets(connString(), templated("K2", "key=${k}", map[string]string{"k": "API_KEY"}))
			deployHealthy(t, e, a)
			live := e.secrets.Live("whoami")
			replaces, regs := e.secrets.Replaces(), e.nomad.Registrations()

			tt.change(src)
			rec, err := e.svc.Deploy(ctx, a, app.TriggerManual)
			var re *ReferenceError
			var de *DeployError
			if !errors.As(err, &re) || !errors.As(err, &de) || !errors.Is(err, tt.wantIs) {
				t.Fatalf("Deploy = %v, want a DeployError with a ReferenceError matching %v", err, tt.wantIs)
			}
			// The failing ref is the first one for a provider-wide failure and the
			// second secret's (K2) for the missing path.
			if re.Provider != "bella-prod" || re.Field != tt.wantField {
				t.Errorf("ReferenceError = %+v, want field %s", re, tt.wantField)
			}
			if got := e.secrets.Live("whoami"); !got.Equal(live) {
				t.Errorf("live = %v, want the running values %v", got, live)
			}
			if e.secrets.Replaces() != replaces || e.nomad.Registrations() != regs {
				t.Error("a secret was replaced or a job registered although the provider failed")
			}
			if strings.Contains(err.Error(), tplSentinel) || strings.Contains(rec.Error, tplSentinel) {
				t.Error("an error holds a value")
			}
		})
	}
}

// Each deploy re-renders, and Refresh secrets re-renders and redeploys only
// when the rendered value changed.
func TestRefreshSecretsTemplate(t *testing.T) {
	ctx := context.Background()
	e, src := refEnv(t)
	src.Set("DB_PASSWORD", "one")
	live := deployHealthy(t, e, withSecrets(connString()))

	// Nothing changed: no deployment.
	regs, deploys := e.nomad.Registrations(), deployCount(e, "whoami")
	got, err := e.svc.RefreshSecrets(ctx, "whoami", app.TriggerManual)
	if err != nil || got.Changed {
		t.Fatalf("RefreshSecrets = %+v, %v; want unchanged", got, err)
	}
	if e.nomad.Registrations() != regs || deployCount(e, "whoami") != deploys {
		t.Error("an unchanged refresh deployed something")
	}

	src.Set("DB_PASSWORD", "two")
	got, err = e.svc.RefreshSecrets(ctx, "whoami", app.TriggerManual)
	if err != nil || !got.Changed || got.Deployment == nil {
		t.Fatalf("RefreshSecrets = %+v, %v; want a changed refresh", got, err)
	}
	rec := *got.Deployment
	if !rec.Deployment.Refresh || rec.Source != live.Deployment.ID {
		t.Errorf("record: refresh %v, source %q; want a refresh of %s", rec.Deployment.Refresh, rec.Source, live.Deployment.ID)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("final = %s (%s)", final.Status, final.Error)
	}
	want := "Host=postgres;Username=banana;Password=two;Note=$5"
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"ConnectionStrings__postgres": want}}) {
		t.Errorf("live = %v, want the re-rendered value", got)
	}
}

// A typed value or a plain reference replaces a template of the same name, and a
// template replaces a value.
func TestTemplateReplacesAndIsReplaced(t *testing.T) {
	ctx := context.Background()
	e, src := refEnv(t)
	src.Set("DB_PASSWORD", "pw")
	deployHealthy(t, e, withSecrets(set("ConnectionStrings__postgres", "typed")))
	deployHealthy(t, e, withSecrets(connString()))
	if got := e.secrets.Live("whoami")["web"]["ConnectionStrings__postgres"]; !strings.Contains(got, "Password=pw") {
		t.Errorf("live = %q, want the rendered template", got)
	}

	// Setting the value in the console path drops the template.
	rec, err := e.svc.SetSecretIf(ctx, "whoami", "web", "ConnectionStrings__postgres", "typed2", Condition{}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("SetSecretIf = %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	stored, _ := e.svc.GetApp(ctx, "whoami")
	sec := stored.App.Services[0].Secrets[0]
	if sec.Template != "" || sec.Refs != nil {
		t.Errorf("stored secret = %+v, want the template gone", sec)
	}
	if got := e.secrets.Live("whoami")["web"]["ConnectionStrings__postgres"]; got != "typed2" {
		t.Errorf("live = %q, want the typed value", got)
	}
}

// A template secret is read at the deploy, so it is not a missing secret of a copy
// and a provider with an app using it can't be removed.
func TestTemplateCopyAndProviderUse(t *testing.T) {
	ctx := context.Background()
	e, src := refEnv(t)
	src.Set("DB_PASSWORD", "pw")
	if _, err := e.svc.CreateEnvironment(ctx, "default", "staging"); err != nil {
		t.Fatal(err)
	}
	deployHealthy(t, e, withSecrets(connString(), set("TYPED_KEPT", "y")))
	copied, missing, err := e.svc.CopyApp(ctx, "whoami", "staging")
	if err != nil {
		t.Fatalf("CopyApp = %v", err)
	}
	if len(missing) != 1 || missing[0].Name != "TYPED_KEPT" {
		t.Errorf("missing secrets = %+v, want only the typed one", missing)
	}
	if got := copied.App.Services[0].Secrets[0]; got.Template == "" || len(got.Refs) != 1 {
		t.Errorf("the copy's secret = %+v, want the template kept", got)
	}

	providers, err := e.svc.SecretProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range providers {
		if p.Name == "bella-prod" {
			found = len(p.UsedBy) == 2 // the app and its copy
		}
	}
	if !found {
		t.Errorf("providers = %+v, want bella-prod used by the two apps", providers)
	}
	if err := e.svc.DeleteSecretProvider(ctx, "bella-prod"); err == nil {
		t.Error("a provider only a template uses was removed")
	}
}
