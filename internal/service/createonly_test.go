package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// noJobFor fails the test if the app has a deployment or Nomad saw a job.
func noJobFor(t *testing.T, e *env, appID string) {
	t.Helper()
	if n := deployCount(e, appID); n != 0 {
		t.Errorf("%d deployments of %q, want none", n, appID)
	}
	if n := e.nomad.Registrations(); n != 0 {
		t.Errorf("Nomad registrations = %d, want none", n)
	}
}

func TestAppDefined(t *testing.T) {
	if (app.App{}).Defined() || (app.App{Services: []app.Service{}}).Defined() {
		t.Error("an app with no services is Defined")
	}
	if !whoami().Defined() {
		t.Error("whoami is not Defined")
	}
}

func TestCreateAppOnly(t *testing.T) {
	ctx := context.Background()

	t.Run("a definition is saved and nothing is deployed", func(t *testing.T) {
		e := newEnv(t)
		rec, err := e.svc.CreateAppOnly(ctx, whoami())
		if err != nil {
			t.Fatalf("CreateAppOnly() = %v", err)
		}
		if !rec.App.Defined() || rec.Revision == "" {
			t.Errorf("record = %+v, want a defined app with a revision", rec)
		}
		stored, err := e.svc.GetApp(ctx, "whoami")
		if err != nil || !reflect.DeepEqual(stored.App, whoami()) {
			t.Errorf("stored = %+v, %v; want the app as sent", stored.App, err)
		}
		noJobFor(t, e, "whoami")
		sum, err := e.svc.AppSummary(ctx, "whoami")
		if err != nil || sum.LastDeployment != nil {
			t.Errorf("summary = %+v, %v; want no last deployment", sum, err)
		}
	})

	t.Run("a name only is saved with no services", func(t *testing.T) {
		e := newEnv(t)
		rec, err := e.svc.CreateAppOnly(ctx, app.App{ID: "shop", Name: "Shop"})
		if err != nil {
			t.Fatalf("CreateAppOnly() = %v", err)
		}
		if rec.App.Defined() || rec.App.Services == nil || rec.App.Source.Kind != app.SourceImage || rec.App.Name != "Shop" {
			t.Errorf("record app = %+v, want a named image app with an empty, non-nil service list", rec.App)
		}
		noJobFor(t, e, "shop")
		sum, err := e.svc.AppSummary(ctx, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if sum.App.Defined() || sum.Status.State == StateHealthy {
			t.Errorf("summary = %+v, want an undefined app that is not healthy", sum)
		}
		list, err := e.svc.ListApps(ctx)
		if err != nil || len(list) != 1 || list[0].App.ID != "shop" {
			t.Errorf("ListApps = %+v, %v; want the app listed", list, err)
		}
	})

	t.Run("the ID is placed in an existing environment", func(t *testing.T) {
		e := shopEnv(t)
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: stagingID}); err != nil {
			t.Errorf("CreateAppOnly(%s) = %v", stagingID, err)
		}
		var placeErr *PlaceError
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "ghost--staging--web"}); !errors.As(err, &placeErr) {
			t.Errorf("CreateAppOnly(unknown project) = %v, want a *PlaceError", err)
		}
		var fieldErr *app.FieldError
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "Bad_ID"}); !errors.As(err, &fieldErr) || fieldErr.Field != "id" {
			t.Errorf("CreateAppOnly(bad ID) = %v, want a FieldError on id", err)
		}
	})

	t.Run("an existing ID is refused and nothing changes", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.CreateAppOnly(ctx, whoami()); err != nil {
			t.Fatal(err)
		}
		before, _ := e.svc.GetApp(ctx, "whoami")
		other := whoami()
		other.Services[0].Replicas = new(4)
		if _, err := e.svc.CreateAppOnly(ctx, other); !errors.Is(err, ErrAppExists) {
			t.Fatalf("second CreateAppOnly() = %v, want ErrAppExists", err)
		}
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "whoami"}); !errors.Is(err, ErrAppExists) {
			t.Fatalf("name-only over an app = %v, want ErrAppExists", err)
		}
		if after, _ := e.svc.GetApp(ctx, "whoami"); !reflect.DeepEqual(before, after) {
			t.Errorf("the stored app changed: %+v -> %+v", before, after)
		}
	})

	t.Run("an invalid definition is a field error and nothing is saved", func(t *testing.T) {
		e := newEnv(t)
		bad := whoami()
		bad.Services[0].Name = "Web"
		var fieldErr *app.FieldError
		if _, err := e.svc.CreateAppOnly(ctx, bad); !errors.As(err, &fieldErr) || fieldErr.Field != "services[0].name" {
			t.Fatalf("CreateAppOnly() = %v, want a FieldError on services[0].name", err)
		}
		if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp = %v, want the app not saved", err)
		}
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "shop", Name: "a\x00b"}); !errors.As(err, &fieldErr) || fieldErr.Field != "name" {
			t.Errorf("CreateAppOnly(control character) = %v, want a FieldError on name", err)
		}
	})

	t.Run("a secret value is refused naming the field, a name is kept", func(t *testing.T) {
		e := newEnv(t)
		a := whoami()
		a.Services[0].Secrets = []app.Secret{keep("API_KEY"), set("DB_URL", "s3cret-sentinel-020")}
		var fieldErr *app.FieldError
		_, err := e.svc.CreateAppOnly(ctx, a)
		if !errors.As(err, &fieldErr) || fieldErr.Field != "services[0].secrets[1].value" {
			t.Fatalf("CreateAppOnly() = %v, want a FieldError on services[0].secrets[1].value", err)
		}
		if strings.Contains(err.Error(), "s3cret-sentinel-020") {
			t.Error("the error holds the secret value")
		}
		if _, err := e.svc.GetApp(ctx, "whoami"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp = %v, want the app not saved", err)
		}

		a.Services[0].Secrets = []app.Secret{keep("API_KEY")}
		rec, err := e.svc.CreateAppOnly(ctx, a)
		if err != nil {
			t.Fatalf("CreateAppOnly(names only) = %v", err)
		}
		if got := rec.App.Services[0].Secrets; len(got) != 1 || got[0].Name != "API_KEY" || got[0].Value != nil {
			t.Errorf("secrets = %+v, want the name only", got)
		}
	})
}

func TestRedeployWithoutDefinition(t *testing.T) {
	ctx := context.Background()

	t.Run("an app with no services refuses a redeploy and records nothing", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "shop"}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Redeploy(ctx, "shop", app.TriggerManual); !errors.Is(err, ErrAppNotDefined) {
			t.Fatalf("Redeploy() = %v, want ErrAppNotDefined", err)
		}
		if _, err := e.svc.RedeployVersion(ctx, "shop", "no-such-deployment", app.TriggerManual); !errors.Is(err, ErrAppNotDefined) {
			t.Fatalf("RedeployVersion() = %v, want ErrAppNotDefined before anything else", err)
		}
		noJobFor(t, e, "shop")
	})

	t.Run("an unknown app is still not found", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.Redeploy(ctx, "ghost", app.TriggerManual); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Redeploy(ghost) = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("its webhook is refused and records nothing", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "shop"}); err != nil {
			t.Fatal(err)
		}
		tok, err := e.svc.CreateWebhookToken(ctx, "shop")
		if err != nil {
			t.Fatalf("CreateWebhookToken: %v", err)
		}
		if _, err := e.svc.Webhook(ctx, "shop", tok.Token); !errors.Is(err, ErrAppNotDefined) {
			t.Fatalf("Webhook() = %v, want ErrAppNotDefined", err)
		}
		noJobFor(t, e, "shop")
	})

	t.Run("a deploy with a definition defines it and deploys", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "whoami"}); err != nil {
			t.Fatal(err)
		}
		rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy() = %v", err)
		}
		if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
			t.Errorf("final = %s, want healthy", final.Status)
		}
		if stored, _ := e.svc.GetApp(ctx, "whoami"); !stored.App.Defined() {
			t.Error("the app is still undefined after a deploy with a definition")
		}
	})

	t.Run("a deploy with the revision defines it", func(t *testing.T) {
		e := newEnv(t)
		created, err := e.svc.CreateAppOnly(ctx, app.App{ID: "whoami"})
		if err != nil {
			t.Fatal(err)
		}
		rec, err := e.svc.DeployIf(ctx, whoami(), Condition{IfRevision: created.Revision}, app.TriggerConsole)
		if err != nil {
			t.Fatalf("DeployIf() = %v", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
	})
}

func TestFirstDeployOfADefinedApp(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.svc.CreateAppOnly(ctx, whoami()); err != nil {
		t.Fatal(err)
	}
	tok, err := e.svc.CreateWebhookToken(ctx, "whoami")
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Webhook(ctx, "whoami", tok.Token)
	if err != nil {
		t.Fatalf("Webhook() = %v", err)
	}
	if !res.Changed || res.Deployment.Deployment.Trigger != app.TriggerWebhook {
		t.Errorf("result = %+v, want a changed webhook deployment", res)
	}
	if final := waitFinal(t, e.store, res.Deployment.Deployment.ID); final.Status != store.StatusHealthy {
		t.Errorf("final = %s, want healthy", final.Status)
	}
	if n := deployCount(e, "whoami"); n != 1 {
		t.Errorf("deployments = %d, want 1", n)
	}
}

func TestCreateAppOnlyThenManage(t *testing.T) {
	ctx := context.Background()

	t.Run("delete works for an app that was never deployed", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "shop"}); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.DeleteApp(ctx, "shop"); err != nil {
			t.Fatalf("DeleteApp() = %v", err)
		}
		if _, err := e.svc.GetApp(ctx, "shop"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetApp = %v, want store.ErrNotFound", err)
		}
	})

	t.Run("copying an app with no definition copies the name only", func(t *testing.T) {
		e := shopEnv(t)
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: productionID, Name: "Web"}); err != nil {
			t.Fatal(err)
		}
		rec, missing, err := e.svc.CopyApp(ctx, productionID, "staging")
		if err != nil {
			t.Fatalf("CopyApp() = %v", err)
		}
		if rec.App.ID != stagingID || rec.App.Name != "Web" || rec.App.Defined() || len(missing) != 0 {
			t.Errorf("copy = %+v, missing %v; want an undefined %s named Web", rec.App, missing, stagingID)
		}
		noJobFor(t, e, stagingID)
	})

	t.Run("promoting from an app that was never deployed is refused", func(t *testing.T) {
		e := shopEnv(t)
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: productionID}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.svc.CopyApp(ctx, productionID, "staging"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.Promote(ctx, productionID, "staging", Condition{}, app.TriggerManual); !errors.Is(err, ErrNoLiveDeployment) {
			t.Errorf("Promote() = %v, want ErrNoLiveDeployment", err)
		}
		noJobFor(t, e, stagingID)
	})

	t.Run("promoting into an app with no definition adds every service", func(t *testing.T) {
		e := shopEnv(t)
		if healthy := deployHealthy(t, e, whoamiAs(productionID)); healthy.Status != store.StatusHealthy {
			t.Fatalf("production = %s, want healthy", healthy.Status)
		}
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: stagingID}); err != nil {
			t.Fatal(err)
		}
		prev, err := e.svc.PreviewPromotion(ctx, productionID, "staging")
		if err != nil {
			t.Fatalf("PreviewPromotion() = %v", err)
		}
		if len(prev.Added) != 1 || prev.Added[0] != "web" {
			t.Errorf("Added = %v, want every service of the source", prev.Added)
		}
	})
}

func TestAttentionOfAnAppNeverDeployed(t *testing.T) {
	apps := []store.AppRecord{
		{App: app.App{ID: "shop", Services: []app.Service{}}},
		{App: whoami()},
	}
	got := attentionOf(attNow, apps, map[string]store.DeploymentRecord{}, nil, nil, nil, nil, true, scheduleFacts{}, nil)
	if len(got.Items) != 0 {
		t.Errorf("items = %+v, want none for apps that were never deployed", got.Items)
	}
}
