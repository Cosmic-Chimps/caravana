package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// whoamiAs is the whoami app under another ID.
func whoamiAs(id string) app.App {
	a := whoami()
	a.ID = id
	return a
}

// envNames lists a project's environment names in order.
func envNames(p Project) []string {
	var out []string
	for _, e := range p.Environments {
		out = append(out, e.Name)
	}
	return out
}

func TestProjectsCRUD(t *testing.T) {
	ctx := context.Background()

	t.Run("the installation starts with Default and its production", func(t *testing.T) {
		e := newEnv(t)
		list, err := e.svc.Projects(ctx)
		if err != nil || len(list) != 1 {
			t.Fatalf("Projects = %+v, %v; want only Default", list, err)
		}
		if p := list[0]; p.ID != "default" || p.DisplayName != "Default" || !slices.Equal(envNames(p), []string{"production"}) {
			t.Errorf("project = %+v", p)
		}
	})

	t.Run("create, read, rename", func(t *testing.T) {
		e := newEnv(t)
		p, err := e.svc.CreateProject(ctx, "shop", "The Shop")
		if err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
		if p.ID != "shop" || p.DisplayName != "The Shop" || !slices.Equal(envNames(p), []string{"production"}) {
			t.Errorf("created = %+v, want a project with production", p)
		}
		blank, err := e.svc.CreateProject(ctx, "blog", "")
		if err != nil || blank.DisplayName != "blog" {
			t.Errorf("CreateProject without a display name = %+v, %v; want it to default to the name", blank, err)
		}
		got, err := e.svc.Project(ctx, "shop")
		if err != nil || got.DisplayName != "The Shop" {
			t.Errorf("Project = %+v, %v", got, err)
		}
		renamed, err := e.svc.SetProjectDisplayName(ctx, "shop", "Shop (EU)")
		if err != nil || renamed.DisplayName != "Shop (EU)" || renamed.ID != "shop" {
			t.Errorf("SetProjectDisplayName = %+v, %v", renamed, err)
		}
		if def, err := e.svc.SetProjectDisplayName(ctx, "default", "Everything"); err != nil || def.DisplayName != "Everything" {
			t.Errorf("Default can be renamed for display: %+v, %v", def, err)
		}
		list, _ := e.svc.Projects(ctx)
		var ids []string
		for _, p := range list {
			ids = append(ids, p.ID)
		}
		if !slices.Equal(ids, []string{"default", "shop", "blog"}) {
			t.Errorf("projects = %v, want Default first then by creation", ids)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.CreateProject(ctx, "shop", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.CreateProject(ctx, "shop", ""); !errors.Is(err, ErrProjectExists) {
			t.Errorf("duplicate = %v, want ErrProjectExists", err)
		}
		if _, err := e.svc.CreateProject(ctx, "default", ""); !errors.Is(err, ErrProjectExists) {
			t.Errorf("default again = %v, want ErrProjectExists", err)
		}
		for _, name := range []string{"", "Shop", "my_shop", "a--b", "-a", strings.Repeat("a", 64)} {
			var fe *app.FieldError
			if _, err := e.svc.CreateProject(ctx, name, ""); !errors.As(err, &fe) || fe.Field != "name" {
				t.Errorf("CreateProject(%q) = %v, want a field error on name", name, err)
			}
		}
		for _, display := range []string{"   ", "two\nlines", strings.Repeat("x", 101)} {
			var fe *app.FieldError
			if _, err := e.svc.CreateProject(ctx, "ok", display); !errors.As(err, &fe) || fe.Field != "displayName" {
				t.Errorf("CreateProject display %q = %v, want a field error on displayName", display, err)
			}
		}
		// Anything printable is fine, including 100 characters of it.
		if _, err := e.svc.CreateProject(ctx, "ok", strings.Repeat("é", 100)); err != nil {
			t.Errorf("a 100 character display name: %v", err)
		}
		if _, err := e.svc.Project(ctx, "nope"); !errors.Is(err, ErrProjectNotFound) {
			t.Errorf("Project(nope) = %v, want ErrProjectNotFound", err)
		}
		if _, err := e.svc.SetProjectDisplayName(ctx, "nope", "x"); !errors.Is(err, ErrProjectNotFound) {
			t.Errorf("rename of nope = %v, want ErrProjectNotFound", err)
		}
	})

	t.Run("environments", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.CreateProject(ctx, "shop", ""); err != nil {
			t.Fatal(err)
		}
		staging, err := e.svc.CreateEnvironment(ctx, "shop", "staging")
		if err != nil || staging.Project != "shop" || staging.Name != "staging" || staging.Position != 1 || staging.Apps != 0 {
			t.Fatalf("CreateEnvironment = %+v, %v", staging, err)
		}
		if _, err := e.svc.CreateEnvironment(ctx, "shop", "staging"); !errors.Is(err, ErrEnvironmentExists) {
			t.Errorf("duplicate = %v, want ErrEnvironmentExists", err)
		}
		if _, err := e.svc.CreateEnvironment(ctx, "nope", "staging"); !errors.Is(err, ErrProjectNotFound) {
			t.Errorf("in an unknown project = %v, want ErrProjectNotFound", err)
		}
		var fe *app.FieldError
		if _, err := e.svc.CreateEnvironment(ctx, "shop", "pre--prod"); !errors.As(err, &fe) || fe.Field != "name" {
			t.Errorf("a name with -- = %v, want a field error on name", err)
		}
		// The same name in another project is fine.
		if _, err := e.svc.CreateEnvironment(ctx, "default", "staging"); err != nil {
			t.Errorf("staging in default: %v", err)
		}
		p, _ := e.svc.Project(ctx, "shop")
		if !slices.Equal(envNames(p), []string{"production", "staging"}) {
			t.Errorf("environments = %v", envNames(p))
		}
		if err := e.svc.DeleteEnvironment(ctx, "shop", "staging"); err != nil {
			t.Fatalf("DeleteEnvironment: %v", err)
		}
		if err := e.svc.DeleteEnvironment(ctx, "shop", "staging"); !errors.Is(err, ErrEnvironmentNotFound) {
			t.Errorf("second delete = %v, want ErrEnvironmentNotFound", err)
		}
		if err := e.svc.DeleteEnvironment(ctx, "nope", "staging"); !errors.Is(err, ErrProjectNotFound) {
			t.Errorf("delete in an unknown project = %v, want ErrProjectNotFound", err)
		}
		// production of another project is not protected; Default's is.
		if err := e.svc.DeleteEnvironment(ctx, "shop", "production"); err != nil {
			t.Errorf("shop's production: %v", err)
		}
	})
}

// FR-003 and the edge cases: protected, and refused while apps remain.
func TestProjectDeletes(t *testing.T) {
	ctx := context.Background()

	t.Run("Default and its production are protected", func(t *testing.T) {
		e := newEnv(t)
		if err := e.svc.DeleteProject(ctx, "default"); !errors.Is(err, ErrProtected) {
			t.Errorf("DeleteProject(default) = %v, want ErrProtected", err)
		}
		if err := e.svc.DeleteEnvironment(ctx, "default", "production"); !errors.Is(err, ErrProtected) {
			t.Errorf("DeleteEnvironment(default, production) = %v, want ErrProtected", err)
		}
		if p, err := e.svc.Project(ctx, "default"); err != nil || !slices.Equal(envNames(p), []string{"production"}) {
			t.Errorf("Default after the refusals = %+v, %v", p, err)
		}
		// Other environments of Default can go.
		if _, err := e.svc.CreateEnvironment(ctx, "default", "staging"); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.DeleteEnvironment(ctx, "default", "staging"); err != nil {
			t.Errorf("default's staging: %v", err)
		}
	})

	t.Run("refused while apps remain, naming them", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.CreateProject(ctx, "shop", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"shop--staging--web", "shop--staging--api"} {
			rec, err := e.svc.CreateApp(ctx, whoamiAs(id), app.TriggerManual)
			if err != nil {
				t.Fatalf("CreateApp %s: %v", id, err)
			}
			waitFinal(t, e.store, rec.Deployment.ID)
		}

		err := e.svc.DeleteEnvironment(ctx, "shop", "staging")
		var ne *NotEmptyError
		if !errors.Is(err, ErrNotEmpty) || !errors.As(err, &ne) || !slices.Equal(ne.AppIDs, []string{"shop--staging--api", "shop--staging--web"}) {
			t.Fatalf("DeleteEnvironment = %v, want ErrNotEmpty naming both apps", err)
		}
		if !strings.Contains(err.Error(), "shop--staging--web") {
			t.Errorf("message %q does not name the apps", err)
		}
		err = e.svc.DeleteProject(ctx, "shop")
		if !errors.Is(err, ErrNotEmpty) || !errors.As(err, &ne) || len(ne.AppIDs) != 2 {
			t.Fatalf("DeleteProject = %v, want ErrNotEmpty naming both apps", err)
		}
		p, err := e.svc.Project(ctx, "shop")
		if err != nil || !slices.Equal(envNames(p), []string{"production", "staging"}) || p.Environments[1].Apps != 2 {
			t.Fatalf("project after the refusals = %+v, %v; want it whole, staging with 2 apps", p, err)
		}

		// With the apps deleted, both deletes go through.
		for _, id := range []string{"shop--staging--web", "shop--staging--api"} {
			if err := e.svc.DeleteApp(ctx, id); err != nil {
				t.Fatalf("DeleteApp %s: %v", id, err)
			}
		}
		if err := e.svc.DeleteEnvironment(ctx, "shop", "staging"); err != nil {
			t.Fatalf("DeleteEnvironment: %v", err)
		}
		if err := e.svc.DeleteProject(ctx, "shop"); err != nil {
			t.Fatalf("DeleteProject: %v", err)
		}
		if err := e.svc.DeleteProject(ctx, "shop"); !errors.Is(err, ErrProjectNotFound) {
			t.Errorf("second DeleteProject = %v, want ErrProjectNotFound", err)
		}
	})
}

// nothingDeployed fails unless a refused deploy left nothing behind: no app, no
// registered job, no deployment record.
func nothingDeployed(t *testing.T, e *env, id string) {
	t.Helper()
	if _, err := e.svc.GetApp(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetApp(%s) = %v, want ErrNotFound: nothing may be saved", id, err)
	}
	if n := e.nomad.Registrations(); n != 0 {
		t.Errorf("Nomad registered %d job(s), want 0", n)
	}
	if recent, _ := e.svc.RecentDeployments(context.Background(), 10); len(recent) != 0 {
		t.Errorf("%d deployment record(s), want none", len(recent))
	}
}

// Research R7: the place comes from the ID on every create path.
func TestCheckPlace(t *testing.T) {
	ctx := context.Background()

	type attempt struct {
		name string
		do   func(e *env, id string) error
	}
	attempts := []attempt{
		{"CreateApp", func(e *env, id string) error {
			_, err := e.svc.CreateApp(ctx, whoamiAs(id), app.TriggerManual)
			return err
		}},
		{"Deploy", func(e *env, id string) error {
			_, err := e.svc.Deploy(ctx, whoamiAs(id), app.TriggerManual)
			return err
		}},
		{"DeployIf IfNew", func(e *env, id string) error {
			_, err := e.svc.DeployIf(ctx, whoamiAs(id), Condition{IfNew: true}, app.TriggerManual)
			return err
		}},
		{"DeployCompose", func(e *env, id string) error {
			_, err := e.svc.DeployCompose(ctx, id, compose.Request{Compose: twoServices}, app.TriggerManual)
			return err
		}},
	}

	for _, at := range attempts {
		t.Run(at.name+" in a project that doesn't exist", func(t *testing.T) {
			e := newEnv(t)
			err := at.do(e, "shop--staging--web")
			var pe *PlaceError
			if !errors.Is(err, ErrProjectNotFound) || !errors.As(err, &pe) || pe.Field != "id" {
				t.Fatalf("err = %v, want ErrProjectNotFound on field id", err)
			}
			nothingDeployed(t, e, "shop--staging--web")
		})

		t.Run(at.name+" in an environment that doesn't exist", func(t *testing.T) {
			e := newEnv(t)
			if _, err := e.svc.CreateProject(ctx, "shop", ""); err != nil {
				t.Fatal(err)
			}
			err := at.do(e, "shop--staging--web")
			var pe *PlaceError
			if !errors.Is(err, ErrEnvironmentNotFound) || !errors.As(err, &pe) || pe.Field != "id" {
				t.Fatalf("err = %v, want ErrEnvironmentNotFound on field id", err)
			}
			if !strings.Contains(err.Error(), "staging") || !strings.Contains(err.Error(), "shop") {
				t.Errorf("message %q should name the environment and the project", err)
			}
			nothingDeployed(t, e, "shop--staging--web")
		})

		t.Run(at.name+" with an ID that isn't a place", func(t *testing.T) {
			e := newEnv(t)
			for _, id := range []string{"a--b", "a--b--c--d", "a---b--c", "default--production--web"} {
				var fe *app.FieldError
				if err := at.do(e, id); !errors.As(err, &fe) || fe.Field != "id" {
					t.Errorf("%s: err = %v, want a field error on id", id, err)
				}
				nothingDeployed(t, e, id)
			}
		})

		t.Run(at.name+" into an environment that exists", func(t *testing.T) {
			e := newEnv(t)
			if _, err := e.svc.CreateProject(ctx, "shop", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
				t.Fatal(err)
			}
			if err := at.do(e, "shop--staging--web"); err != nil {
				t.Fatalf("err = %v", err)
			}
			rec, err := e.svc.GetApp(ctx, "shop--staging--web")
			if err != nil {
				t.Fatal(err)
			}
			if want := (app.Place{Project: "shop", Environment: "staging", Name: "web"}); rec.Place != want {
				t.Errorf("place = %+v, want %+v", rec.Place, want)
			}
		})
	}

	t.Run("a bare name is Default's production, which always exists", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.CreateApp(ctx, whoami(), app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		rec, _ := e.svc.GetApp(ctx, "whoami")
		if want := (app.Place{Project: "default", Environment: "production", Name: "whoami"}); rec.Place != want {
			t.Errorf("place = %+v, want %+v", rec.Place, want)
		}
	})

	t.Run("an app that exists keeps deploying, even with an ID projects would refuse", func(t *testing.T) {
		e := newEnv(t)
		// Stored by an older Caravana: any label was allowed.
		if _, err := e.store.SaveApp(ctx, whoamiAs("my--app"), nil); err != nil {
			t.Fatal(err)
		}
		rec, err := e.svc.Deploy(ctx, whoamiAs("my--app"), app.TriggerManual)
		if err != nil {
			t.Fatalf("Deploy of an existing app with -- in its ID: %v", err)
		}
		if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
			t.Errorf("deploy = %s (%s)", final.Status, final.Error)
		}
		if _, err := e.svc.Redeploy(ctx, "my--app", app.TriggerWebhook); err != nil {
			t.Errorf("Redeploy: %v", err)
		}
		// Creating it again is the existing refusal, not a place error.
		if _, err := e.svc.CreateApp(ctx, whoamiAs("my--app"), app.TriggerManual); !errors.Is(err, ErrAppExists) {
			t.Errorf("CreateApp of an existing ID = %v, want ErrAppExists", err)
		}
		rec2, _ := e.svc.GetApp(ctx, "my--app")
		if want := (app.Place{Project: "default", Environment: "production", Name: "my--app"}); rec2.Place != want {
			t.Errorf("place = %+v, want %+v", rec2.Place, want)
		}
	})

	t.Run("a computed ID that is taken is app_exists", func(t *testing.T) {
		e := newEnv(t)
		// From before projects: ID "x--y--z" sits in Default, as the migration left it.
		if _, err := e.store.SaveApp(ctx, whoamiAs("x--y--z"), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.CreateProject(ctx, "x", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.CreateEnvironment(ctx, "x", "y"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.CreateApp(ctx, whoamiAs("x--y--z"), app.TriggerManual); !errors.Is(err, ErrAppExists) {
			t.Errorf("CreateApp = %v, want ErrAppExists", err)
		}
	})

	t.Run("a stale revision on an unknown ID is still refused for its place first", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.svc.DeployIf(ctx, whoamiAs("shop--staging--web"), Condition{IfRevision: "abc"}, app.TriggerManual)
		if !errors.Is(err, ErrProjectNotFound) {
			t.Errorf("err = %v, want ErrProjectNotFound", err)
		}
	})
}

func TestListAppsIn(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	for _, p := range []string{"shop", "blog"} {
		if _, err := e.svc.CreateProject(ctx, p, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.CreateEnvironment(ctx, p, "staging"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"whoami", "shop--production--web", "shop--staging--web", "blog--staging--web"} {
		rec, err := e.svc.CreateApp(ctx, whoamiAs(id), app.TriggerManual)
		if err != nil {
			t.Fatalf("CreateApp %s: %v", id, err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
	}

	ids := func(project, environment string) []string {
		t.Helper()
		sums, err := e.svc.ListAppsIn(ctx, project, environment)
		if err != nil {
			t.Fatalf("ListAppsIn(%q, %q): %v", project, environment, err)
		}
		out := []string{}
		for _, s := range sums {
			out = append(out, s.App.ID)
		}
		return out
	}
	if got := ids("", ""); len(got) != 4 {
		t.Errorf("all = %v", got)
	}
	if got := ids("shop", ""); !slices.Equal(got, []string{"shop--production--web", "shop--staging--web"}) {
		t.Errorf("shop = %v", got)
	}
	if got := ids("shop", "staging"); !slices.Equal(got, []string{"shop--staging--web"}) {
		t.Errorf("shop staging = %v", got)
	}
	if got := ids("", "staging"); !slices.Equal(got, []string{"blog--staging--web", "shop--staging--web"}) {
		t.Errorf("any staging = %v", got)
	}
	if got := ids("default", "production"); !slices.Equal(got, []string{"whoami"}) {
		t.Errorf("default production = %v", got)
	}
	if got := ids("blog", "production"); len(got) != 0 {
		t.Errorf("blog production = %v, want none", got)
	}
	if _, err := e.svc.ListAppsIn(ctx, "nope", ""); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("unknown project = %v, want ErrProjectNotFound", err)
	}
	if _, err := e.svc.ListAppsIn(ctx, "shop", "qa"); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Errorf("unknown environment = %v, want ErrEnvironmentNotFound", err)
	}
	// The summary carries the place.
	sums, _ := e.svc.ListAppsIn(ctx, "shop", "staging")
	if want := (app.Place{Project: "shop", Environment: "staging", Name: "web"}); sums[0].Place != want {
		t.Errorf("summary place = %+v, want %+v", sums[0].Place, want)
	}
	if sum, err := e.svc.AppSummary(ctx, "shop--staging--web"); err != nil || sum.Place.Name != "web" {
		t.Errorf("AppSummary place = %+v, %v", sum.Place, err)
	}
}

func TestResolvePlace(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.svc.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}
	p, err := e.svc.ResolvePlace(ctx, "shop", "production", "web")
	if err != nil || p.ID() != "shop--production--web" {
		t.Errorf("ResolvePlace = %+v, %v", p, err)
	}
	if p, err := e.svc.ResolvePlace(ctx, "default", "production", "web"); err != nil || p.ID() != "web" {
		t.Errorf("ResolvePlace(default, production) = %+v, %v; want the bare name", p, err)
	}
	if _, err := e.svc.ResolvePlace(ctx, "nope", "production", "web"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("unknown project = %v", err)
	}
	if _, err := e.svc.ResolvePlace(ctx, "shop", "qa", "web"); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Errorf("unknown environment = %v", err)
	}
	var fe *app.FieldError
	if _, err := e.svc.ResolvePlace(ctx, "shop", "production", "a--b"); !errors.As(err, &fe) || fe.Field != "name" {
		t.Errorf("bad name = %v, want a field error on name", err)
	}
}

func TestProjectChangesArePublished(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	ch, stop := e.svc.Subscribe("")
	defer stop()

	want := func(what string) {
		t.Helper()
		for {
			select {
			case c := <-ch:
				if c.What == ChangeApps {
					return
				}
			default:
				t.Fatalf("no apps notice after %s", what)
			}
		}
	}
	if _, err := e.svc.CreateProject(ctx, "shop", ""); err != nil {
		t.Fatal(err)
	}
	want("CreateProject")
	if _, err := e.svc.CreateEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	want("CreateEnvironment")
	if err := e.svc.DeleteEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}
	want("DeleteEnvironment")
	if err := e.svc.DeleteProject(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	want("DeleteProject")
}
