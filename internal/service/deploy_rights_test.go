package service

import (
	"context"
	"errors"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

const v2Digest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

// refusedWithRole checks err is a *NotAllowedError for that place, levels and field.
func refusedWithRole(t *testing.T, what string, err error, have, need Level, project, environment, field string) {
	t.Helper()
	var na *NotAllowedError
	if !errors.As(err, &na) || !errors.Is(err, ErrNotAllowed) {
		t.Errorf("%s = %v, want a *NotAllowedError", what, err)
		return
	}
	if na.Have != have || na.Need != need || na.Project != project || na.Environment != environment || na.Field != field {
		t.Errorf("%s: refused as %+v; want have %s, need %s at %s › %s, field %q", what, *na, have, need, project, environment, field)
	}
}

// untouchedBy checks nothing was saved or deployed for an app.
func untouchedBy(t *testing.T, e *env, id, revision string, deployments int) {
	t.Helper()
	untouched(t, e, id, revision, deployments)
}

func TestDeployRights(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	both(t, e)
	e.registry.AddIndex("traefik/whoami", "v2", v2Digest)
	member := asPerson(shopMember()) // view on production, deploy on staging
	stored := func(id string) store.AppRecord {
		rec, err := e.svc.GetApp(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	deployments := func(id string) int {
		recs, _ := e.svc.ListDeployments(ctx, id, 0)
		return len(recs)
	}

	t.Run("a new image on the environment they may deploy to", func(t *testing.T) {
		next := stored(stagingID).App
		next.Services[0].Image = "traefik/whoami:v2"
		rec, err := e.svc.Deploy(member, next, app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy with a new image = %v, want allowed", err)
		}
		if rec.Deployment.Actor != "mia@example.com" {
			t.Errorf("the deployment's actor = %q, want mia@example.com", rec.Deployment.Actor)
		}
		if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
			t.Fatalf("final = %s (%s)", final.Status, final.Error)
		}
		if got := stored(stagingID).App.Services[0].Image; got != "traefik/whoami:v2" {
			t.Errorf("stored image = %q, want the new one", got)
		}
	})

	t.Run("the stored app as it is, again", func(t *testing.T) {
		rec, err := e.svc.DeployIf(member, stored(stagingID).App, Condition{IfRevision: stored(stagingID).Revision}, app.TriggerManual)
		if err != nil {
			t.Fatalf("DeployIf of the stored app = %v, want allowed", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
	})

	t.Run("anything but an image needs maintain, naming the field", func(t *testing.T) {
		before := stored(stagingID)
		n := deployments(stagingID)
		next := before.App
		next.Services = append([]app.Service(nil), next.Services...)
		next.Services[0].Env = append([]app.EnvVar{{Name: "EXTRA", Value: "1"}}, next.Services[0].Env...)
		_, err := e.svc.Deploy(member, next, app.TriggerManual)
		refusedWithRole(t, "Deploy with a changed env", err, LevelDeploy, LevelMaintain, "shop", "staging", "services[0].env")
		untouchedBy(t, e, stagingID, before.Revision, n)

		next = before.App
		next.Services = append([]app.Service(nil), next.Services...)
		next.Services[0].Secrets = []app.Secret{set("DB_URL", "member-typed")}
		_, err = e.svc.DeployIf(member, next, Condition{}, app.TriggerManual)
		refusedWithRole(t, "DeployIf with a secret value", err, LevelDeploy, LevelMaintain, "shop", "staging", "services[0].secrets")
		untouchedBy(t, e, stagingID, before.Revision, n)

		next = before.App
		next.Services = append(append([]app.Service(nil), next.Services...), app.Service{Name: "db", Image: "postgres:17"})
		_, err = e.svc.Deploy(member, next, app.TriggerManual)
		refusedWithRole(t, "Deploy with another service", err, LevelDeploy, LevelMaintain, "shop", "staging", "services")
	})

	t.Run("view is not enough for an image", func(t *testing.T) {
		before := stored(productionID)
		n := deployments(productionID)
		next := before.App
		next.Services = append([]app.Service(nil), next.Services...)
		next.Services[0].Image = "traefik/whoami:v2"
		_, err := e.svc.Deploy(member, next, app.TriggerManual)
		refusedWithRole(t, "Deploy of production", err, LevelView, LevelDeploy, "shop", "production", "")
		untouchedBy(t, e, productionID, before.Revision, n)

		next.Services[0].Env = nil
		_, err = e.svc.Deploy(member, next, app.TriggerManual)
		refusedWithRole(t, "Deploy of production with more than an image", err, LevelView, LevelMaintain, "shop", "production", "services[0].env")
	})

	t.Run("creating an app needs maintain", func(t *testing.T) {
		_, err := e.svc.Deploy(member, whoamiAs("shop--staging--api"), app.TriggerManual)
		refusedWithRole(t, "Deploy of a new app", err, LevelDeploy, LevelMaintain, "shop", "staging", "")
		_, err = e.svc.CreateApp(member, whoamiAs("shop--staging--api"), app.TriggerManual)
		refusedWithRole(t, "CreateApp", err, LevelDeploy, LevelMaintain, "shop", "staging", "")
		_, err = e.svc.CreateAppOnly(member, app.App{ID: "shop--staging--api"})
		refusedWithRole(t, "CreateAppOnly", err, LevelDeploy, LevelMaintain, "shop", "staging", "")
		if _, err := e.svc.GetApp(ctx, "shop--staging--api"); err == nil {
			t.Error("a refused create left an app behind")
		}
		// A person with no role anywhere near it is told the same, whether or not the app exists.
		stranger := asPerson(Principal{PersonID: "x", Email: "x@example.com", OrgRole: OrgMember})
		_, err = e.svc.CreateApp(stranger, whoamiAs("shop--staging--api"), app.TriggerManual)
		refusedWithRole(t, "CreateApp by a stranger", err, LevelNone, LevelMaintain, "shop", "staging", "")
	})

	t.Run("a Compose file needs maintain", func(t *testing.T) {
		req := compose.Request{Compose: "services:\n  web:\n    image: traefik/whoami:latest\n"}
		_, err := e.svc.DeployCompose(member, stagingID, req, app.TriggerManual)
		refusedWithRole(t, "DeployCompose", err, LevelDeploy, LevelMaintain, "shop", "staging", "")
		_, err = e.svc.DeployComposeIf(member, "shop--staging--fresh", req, Condition{IfNew: true}, app.TriggerManual)
		refusedWithRole(t, "DeployComposeIf of a new app", err, LevelDeploy, LevelMaintain, "shop", "staging", "")
	})

	t.Run("a redeploy needs deploy", func(t *testing.T) {
		_, err := e.svc.Redeploy(member, productionID, app.TriggerManual)
		refusedWithRole(t, "Redeploy of production", err, LevelView, LevelDeploy, "shop", "production", "")
		rec, err := e.svc.Redeploy(member, stagingID, app.TriggerManual)
		if err != nil {
			t.Fatalf("Redeploy of staging = %v, want allowed", err)
		}
		if rec.Deployment.Actor != "mia@example.com" {
			t.Errorf("actor = %q", rec.Deployment.Actor)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		// A past version too: a viewer is refused, and nothing is recorded.
		past := stored(stagingID)
		n := deployments(stagingID)
		_, err = e.svc.RedeployVersion(member, productionID, "d-any", app.TriggerManual)
		refusedWithRole(t, "RedeployVersion of production", err, LevelView, LevelDeploy, "shop", "production", "")
		_, err = e.svc.RefreshSecrets(member, productionID, app.TriggerManual)
		refusedWithRole(t, "RefreshSecrets of production", err, LevelView, LevelDeploy, "shop", "production", "")
		untouchedBy(t, e, stagingID, past.Revision, n)
		// The webhook and the host (no principal) redeploy as before, and leave no actor.
		rec, err = e.svc.Redeploy(ctx, productionID, app.TriggerWebhook)
		if err != nil || rec.Deployment.Actor != "" {
			t.Errorf("Redeploy with no principal = %q, %v; want allowed, no actor", rec.Deployment.Actor, err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
	})

	t.Run("maintain does it all", func(t *testing.T) {
		maintainer := asPerson(Principal{PersonID: "max", Email: "max@example.com", OrgRole: OrgMember,
			Roles: []ProjectRole{{Project: "shop", Role: RoleMaintain}}})
		next := stored(stagingID).App
		next.Services = append([]app.Service(nil), next.Services...)
		next.Services[0].Env = []app.EnvVar{{Name: "MODE", Value: "tested"}}
		rec, err := e.svc.Deploy(maintainer, next, app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy with a changed env as a maintainer = %v", err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
		if _, err := e.svc.CreateAppOnly(maintainer, app.App{ID: "shop--staging--api"}); err != nil {
			t.Errorf("CreateAppOnly as a maintainer = %v", err)
		}
	})

	t.Run("owners, admins and Caravana itself are not held to any of it", func(t *testing.T) {
		for name, c := range seesAll() {
			next := stored(stagingID).App
			next.Services = append([]app.Service(nil), next.Services...)
			next.Services[0].Env = []app.EnvVar{{Name: "BY", Value: name}}
			rec, err := e.svc.Deploy(c, next, app.TriggerManual)
			if err != nil {
				t.Errorf("%s: Deploy = %v", name, err)
				continue
			}
			waitFinal(t, e.store, rec.Deployment.ID)
		}
	})
}

// A preview (pr-<n>) is opened by CI with a deploy token: the deploy role is what may define its apps.
func TestDeployRightsInAPreview(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	both(t, e)
	if _, err := e.svc.CreateEnvironment(ctx, "shop", "pr-9"); err != nil {
		t.Fatal(err)
	}
	deployer := asPerson(Principal{PersonID: "ci", Email: "ci@example.com", OrgRole: OrgMember,
		Roles: []ProjectRole{{Project: "shop", Role: RoleDeploy}}})

	// The whole definition, in a preview.
	a := whoamiAs("shop--pr-9--web")
	a.Services[0].Env = []app.EnvVar{{Name: "PREVIEW", Value: "9"}}
	a.Services[0].Secrets = []app.Secret{set("DB_URL", "preview-db")}
	rec, err := e.svc.Deploy(deployer, a, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy of a new app in a preview with deploy = %v, want allowed", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	a.Services[0].Env = []app.EnvVar{{Name: "PREVIEW", Value: "9b"}}
	if rec, err = e.svc.Deploy(deployer, a, app.TriggerManual); err != nil {
		t.Fatalf("a changed env in a preview with deploy = %v, want allowed", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	if _, err := e.svc.CreateAppOnly(deployer, app.App{ID: "shop--pr-9--worker"}); err != nil {
		t.Errorf("CreateAppOnly in a preview with deploy = %v, want allowed", err)
	}

	// The same person is held to images in a real environment.
	rec2, err := e.svc.GetApp(ctx, stagingID)
	if err != nil {
		t.Fatal(err)
	}
	next := rec2.App
	next.Services = append([]app.Service(nil), next.Services...)
	next.Services[0].Env = []app.EnvVar{{Name: "X", Value: "1"}}
	_, err = e.svc.Deploy(deployer, next, app.TriggerManual)
	refusedWithRole(t, "Deploy to staging with a changed env", err, LevelDeploy, LevelMaintain, "shop", "staging", "services[0].env")
	_, err = e.svc.CreateAppOnly(deployer, app.App{ID: "shop--staging--worker"})
	refusedWithRole(t, "CreateAppOnly in staging", err, LevelDeploy, LevelMaintain, "shop", "staging", "")

	// And view in a preview isn't enough.
	viewer := asPerson(Principal{PersonID: "v", Email: "v@example.com", OrgRole: OrgMember, Roles: []ProjectRole{{Project: "shop", Role: RoleView}}})
	_, err = e.svc.Deploy(viewer, whoamiAs("shop--pr-9--other"), app.TriggerManual)
	refusedWithRole(t, "Deploy to a preview with view", err, LevelView, LevelDeploy, "shop", "pr-9", "")
}

func TestPromoteAndCopyRights(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	both(t, e)
	member := asPerson(shopMember()) // view on production, deploy on staging

	// Into staging, from production: view on the source and deploy on the target.
	if _, err := e.svc.PreviewPromotion(member, productionID, "staging"); err != nil {
		t.Errorf("PreviewPromotion from production = %v, want allowed (view on the source)", err)
	}
	rec, err := e.svc.Promote(member, productionID, "staging", Condition{}, app.TriggerConsole)
	if err != nil {
		t.Fatalf("Promote into staging = %v, want allowed", err)
	}
	if rec.Deployment.Actor != "mia@example.com" {
		t.Errorf("promotion's actor = %q", rec.Deployment.Actor)
	}
	waitFinal(t, e.store, rec.Deployment.ID)

	// Into production: only view on the target.
	_, err = e.svc.Promote(member, stagingID, "production", Condition{}, app.TriggerConsole)
	refusedWithRole(t, "Promote into production", err, LevelView, LevelDeploy, "shop", "production", "")
	// A preview of it is a read of the source, which they may do.
	if _, err := e.svc.PreviewPromotion(member, stagingID, "production"); err != nil {
		t.Errorf("PreviewPromotion from staging = %v, want allowed", err)
	}

	// No view on the source: refused before anything is read.
	if _, err := e.svc.CreateProject(ctx, "blog", ""); err != nil {
		t.Fatal(err)
	}
	deployHealthy(t, e, blogApp())
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "staging"); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.PreviewPromotion(member, blogID, "staging")
	refusedWithRole(t, "PreviewPromotion of blog", err, LevelNone, LevelView, "blog", "production", "")
	_, err = e.svc.Promote(member, blogID, "staging", Condition{}, app.TriggerConsole)
	refusedWithRole(t, "Promote of blog", err, LevelNone, LevelView, "blog", "production", "")
	_, _, err = e.svc.CopyApp(member, blogID, "staging")
	refusedWithRole(t, "CopyApp of blog", err, LevelNone, LevelView, "blog", "production", "")

	// A copy creates an app in the target: that needs maintain there.
	if _, err := e.svc.CreateEnvironment(ctx, "shop", "qa"); err != nil {
		t.Fatal(err)
	}
	_, _, err = e.svc.CopyApp(member, stagingID, "qa")
	refusedWithRole(t, "CopyApp into qa with no role there beyond the project's view", err, LevelView, LevelMaintain, "shop", "qa", "")
	qaMaintainer := asPerson(Principal{PersonID: "q", Email: "q@example.com", OrgRole: OrgMember, Roles: []ProjectRole{
		{Project: "shop", Role: RoleView, Environments: map[string]Role{"qa": RoleMaintain}}}})
	if _, _, err := e.svc.CopyApp(qaMaintainer, stagingID, "qa"); err != nil {
		t.Errorf("CopyApp into qa as its maintainer = %v, want allowed", err)
	}
	if _, err := e.svc.GetApp(ctx, "shop--qa--web"); err != nil {
		t.Errorf("the copy wasn't made: %v", err)
	}
}
