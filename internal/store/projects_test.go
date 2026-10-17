package store

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/app/apptest"
)

// storedTimeRE is the stored time format: UTC with nine fraction digits.
var storedTimeRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$`)

func TestDefaultProject(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	projects, err := s.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("ListProjects = %+v, %v; want only Default", projects, err)
	}
	if p := projects[0]; p.ID != "default" || p.DisplayName != "Default" {
		t.Errorf("project = %+v, want default / Default", p)
	}
	envs, err := s.ListEnvironments(ctx, "default")
	if err != nil || len(envs) != 1 || envs[0].Name != "production" || envs[0].Position != 0 {
		t.Fatalf("ListEnvironments(default) = %+v, %v; want production at 0", envs, err)
	}

	// The migration writes the times in SQL; they must look like the ones Go
	// writes, or they would sort wrongly as text.
	var created, updated, envCreated string
	if err := s.db.QueryRowContext(ctx, "SELECT created_at, updated_at FROM projects WHERE id = 'default'").Scan(&created, &updated); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT created_at FROM environments WHERE project_id = 'default'").Scan(&envCreated); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{created, updated, envCreated} {
		if !storedTimeRE.MatchString(v) {
			t.Errorf("migration wrote time %q, want the stored format (nine fraction digits, Z)", v)
		}
		if _, err := time.Parse(storedTime, v); err != nil {
			t.Errorf("time %q does not parse with storedTime: %v", v, err)
		}
	}
}

func TestProjects(t *testing.T) {
	ctx := context.Background()

	t.Run("insert creates production", func(t *testing.T) {
		s := openTestStore(t)
		p, err := s.InsertProject(ctx, "shop", "The Shop")
		if err != nil {
			t.Fatalf("InsertProject: %v", err)
		}
		if p.ID != "shop" || p.DisplayName != "The Shop" || !p.CreatedAt.Equal(p.UpdatedAt) {
			t.Errorf("project = %+v", p)
		}
		envs, err := s.ListEnvironments(ctx, "shop")
		if err != nil || len(envs) != 1 || envs[0].Name != "production" || envs[0].Position != 0 || envs[0].Apps != 0 {
			t.Errorf("ListEnvironments = %+v, %v; want production at 0, no apps", envs, err)
		}
		if _, err := s.InsertProject(ctx, "shop", "again"); !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("second InsertProject = %v, want ErrAlreadyExists", err)
		}
		if _, err := s.InsertProject(ctx, "default", "x"); !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("InsertProject(default) = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("list puts Default first then by creation", func(t *testing.T) {
		s := openTestStore(t)
		// The fake clock starts in 2026-10-02, before the migration's real
		// "now": Default must still come first.
		for _, id := range []string{"zeta", "alpha", "mid"} {
			if _, err := s.InsertProject(ctx, id, id); err != nil {
				t.Fatal(err)
			}
		}
		list, err := s.ListProjects(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, p := range list {
			ids = append(ids, p.ID)
		}
		if want := []string{"default", "zeta", "alpha", "mid"}; !slices.Equal(ids, want) {
			t.Errorf("order = %v, want %v", ids, want)
		}
	})

	t.Run("get and rename", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.GetProject(ctx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetProject(nope) = %v, want ErrNotFound", err)
		}
		first, err := s.InsertProject(ctx, "shop", "Shop")
		if err != nil {
			t.Fatal(err)
		}
		renamed, err := s.SetProjectDisplayName(ctx, "shop", "Shop 2")
		if err != nil {
			t.Fatalf("SetProjectDisplayName: %v", err)
		}
		if renamed.DisplayName != "Shop 2" || !renamed.UpdatedAt.After(first.UpdatedAt) || !renamed.CreatedAt.Equal(first.CreatedAt) {
			t.Errorf("renamed = %+v, first = %+v", renamed, first)
		}
		if _, err := s.SetProjectDisplayName(ctx, "nope", "x"); !errors.Is(err, ErrNotFound) {
			t.Errorf("rename of an unknown project = %v, want ErrNotFound", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.InsertProject(ctx, "shop", "Shop"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.InsertEnvironment(ctx, "shop", "staging"); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteProject(ctx, "shop"); err != nil {
			t.Fatalf("DeleteProject: %v", err)
		}
		if _, err := s.GetProject(ctx, "shop"); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetProject after delete = %v, want ErrNotFound", err)
		}
		if n := countRows(t, s, "SELECT count(*) FROM environments WHERE project_id = 'shop'"); n != 0 {
			t.Errorf("%d environments left after deleting the project, want 0", n)
		}
		if err := s.DeleteProject(ctx, "shop"); !errors.Is(err, ErrNotFound) {
			t.Errorf("second DeleteProject = %v, want ErrNotFound", err)
		}
	})

	t.Run("delete refused while apps remain", func(t *testing.T) {
		s := openTestStore(t)
		if _, err := s.InsertProject(ctx, "shop", "Shop"); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"shop--production--web", "shop--production--api"} {
			saveAppWithID(t, s, id)
		}
		err := s.DeleteProject(ctx, "shop")
		if !errors.Is(err, ErrNotEmpty) {
			t.Fatalf("DeleteProject = %v, want ErrNotEmpty", err)
		}
		var ne *NotEmptyError
		if !errors.As(err, &ne) || !slices.Equal(ne.AppIDs, []string{"shop--production--api", "shop--production--web"}) {
			t.Errorf("error = %+v, want the two app IDs sorted", ne)
		}
		if _, err := s.GetProject(ctx, "shop"); err != nil {
			t.Errorf("project gone after a refused delete: %v", err)
		}
	})
}

func TestEnvironments(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.InsertProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}

	staging, err := s.InsertEnvironment(ctx, "shop", "staging")
	if err != nil {
		t.Fatalf("InsertEnvironment: %v", err)
	}
	if staging.Project != "shop" || staging.Name != "staging" || staging.Position != 1 {
		t.Errorf("staging = %+v, want position 1 after production", staging)
	}
	qa, err := s.InsertEnvironment(ctx, "shop", "qa")
	if err != nil || qa.Position != 2 {
		t.Fatalf("qa = %+v, %v; want position 2", qa, err)
	}
	if _, err := s.InsertEnvironment(ctx, "shop", "qa"); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("duplicate = %v, want ErrAlreadyExists", err)
	}
	if _, err := s.InsertEnvironment(ctx, "nope", "qa"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project = %v, want ErrNotFound", err)
	}
	// The same name in another project is fine.
	if _, err := s.InsertEnvironment(ctx, "default", "qa"); err != nil {
		t.Errorf("qa in default: %v", err)
	}

	list, err := s.ListEnvironments(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name)
	}
	if want := []string{"production", "staging", "qa"}; !slices.Equal(names, want) {
		t.Errorf("environments = %v, want %v", names, want)
	}
	if _, err := s.ListEnvironments(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ListEnvironments(nope) = %v, want ErrNotFound", err)
	}

	for env, want := range map[string]bool{"staging": true, "production": true, "nope": false} {
		if got, err := s.EnvironmentExists(ctx, "shop", env); err != nil || got != want {
			t.Errorf("EnvironmentExists(shop, %s) = %v, %v; want %v", env, got, err, want)
		}
	}
	if got, err := s.EnvironmentExists(ctx, "nope", "production"); err != nil || got {
		t.Errorf("EnvironmentExists(nope, production) = %v, %v; want false", got, err)
	}

	// Counts follow the apps, and a delete is refused while there are any.
	saveAppWithID(t, s, "shop--staging--web")
	list, err = s.ListEnvironments(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if list[1].Name != "staging" || list[1].Apps != 1 || list[0].Apps != 0 {
		t.Errorf("counts = %+v, want staging 1 and production 0", list)
	}
	err = s.DeleteEnvironment(ctx, "shop", "staging")
	var ne *NotEmptyError
	if !errors.Is(err, ErrNotEmpty) || !errors.As(err, &ne) || !slices.Equal(ne.AppIDs, []string{"shop--staging--web"}) {
		t.Fatalf("DeleteEnvironment with an app = %v, want ErrNotEmpty naming shop--staging--web", err)
	}
	if err := s.DeleteApp(ctx, "shop--staging--web"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
	if err := s.DeleteEnvironment(ctx, "shop", "staging"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second DeleteEnvironment = %v, want ErrNotFound", err)
	}
	// Deleting an environment leaves the next one's position alone; a new one goes last.
	again, err := s.InsertEnvironment(ctx, "shop", "staging")
	if err != nil || again.Position != 3 {
		t.Errorf("staging again = %+v, %v; want position 3", again, err)
	}
}

// countRows runs a "SELECT count(*) ..." query.
func countRows(t *testing.T, s *Store, query string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// saveAppWithID stores the whoami app under another ID.
func saveAppWithID(t *testing.T, s *Store, id string) {
	t.Helper()
	a := apptest.Whoami().App
	a.ID = id
	if _, err := s.SaveApp(context.Background(), a, nil); err != nil {
		t.Fatalf("SaveApp %s: %v", id, err)
	}
}

func TestAppPlace(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.InsertProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertEnvironment(ctx, "shop", "staging"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		id   string
		want app.Place
	}{
		{"whoami", app.Place{Project: "default", Environment: "production", Name: "whoami"}},
		{"shop--staging--web", app.Place{Project: "shop", Environment: "staging", Name: "web"}},
		{"shop--production--api", app.Place{Project: "shop", Environment: "production", Name: "api"}},
		// An ID from before projects: PlaceOf refuses it, so it stays in Default.
		{"my--app", app.Place{Project: "default", Environment: "production", Name: "my--app"}},
		{"a--b--c--d", app.Place{Project: "default", Environment: "production", Name: "a--b--c--d"}},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			a := apptest.Whoami().App
			a.ID = tt.id
			rec, err := s.SaveApp(ctx, a, nil)
			if err != nil {
				t.Fatalf("SaveApp: %v", err)
			}
			if rec.Place != tt.want {
				t.Errorf("SaveApp place = %+v, want %+v", rec.Place, tt.want)
			}
			if got, err := s.GetApp(ctx, tt.id); err != nil || got.Place != tt.want {
				t.Errorf("GetApp place = %+v, %v; want %+v", got.Place, err, tt.want)
			}
		})
	}

	t.Run("InsertApp places the same way", func(t *testing.T) {
		a := apptest.Whoami().App
		a.ID = "shop--staging--worker"
		rec, err := s.InsertApp(ctx, a, nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := (app.Place{Project: "shop", Environment: "staging", Name: "worker"}); rec.Place != want {
			t.Errorf("place = %+v, want %+v", rec.Place, want)
		}
	})

	t.Run("an update never moves the app", func(t *testing.T) {
		before, err := s.GetApp(ctx, "shop--staging--web")
		if err != nil {
			t.Fatal(err)
		}
		// Scramble the stored place by hand; SaveApp and SaveAppIf must leave it alone.
		if _, err := s.db.ExecContext(ctx,
			"UPDATE apps SET project = 'default', environment = 'production', app_name = 'moved' WHERE id = ?", before.App.ID); err != nil {
			t.Fatal(err)
		}
		want := app.Place{Project: "default", Environment: "production", Name: "moved"}

		changed := before.App
		changed.Name = "second"
		rec, err := s.SaveApp(ctx, changed, nil)
		if err != nil || rec.Place != want {
			t.Fatalf("SaveApp place = %+v, %v; want %+v", rec.Place, err, want)
		}
		changed.Name = "third"
		rec, err = s.SaveAppIf(ctx, changed, nil, rec.Revision)
		if err != nil || rec.Place != want {
			t.Fatalf("SaveAppIf place = %+v, %v; want %+v", rec.Place, err, want)
		}
	})

	t.Run("a computed ID that is taken is refused", func(t *testing.T) {
		// "my--app" is stored as default/production/"my--app"; the same ID again is a conflict.
		a := apptest.Whoami().App
		a.ID = "my--app"
		if _, err := s.InsertApp(ctx, a, nil); !errors.Is(err, ErrAlreadyExists) {
			t.Errorf("InsertApp of a taken ID = %v, want ErrAlreadyExists", err)
		}
	})
}

func TestListAppsIn(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, p := range []string{"shop", "blog"} {
		if _, err := s.InsertProject(ctx, p, p); err != nil {
			t.Fatal(err)
		}
		if _, err := s.InsertEnvironment(ctx, p, "staging"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"whoami", "shop--production--web", "shop--staging--web", "shop--staging--api", "blog--staging--web"} {
		saveAppWithID(t, s, id)
	}

	tests := []struct {
		name, project, env string
		want               []string
	}{
		{"all", "", "", []string{"blog--staging--web", "shop--production--web", "shop--staging--api", "shop--staging--web", "whoami"}},
		{"project", "shop", "", []string{"shop--production--web", "shop--staging--api", "shop--staging--web"}},
		{"project and environment", "shop", "staging", []string{"shop--staging--api", "shop--staging--web"}},
		{"environment alone", "", "staging", []string{"blog--staging--web", "shop--staging--api", "shop--staging--web"}},
		{"default production", "default", "production", []string{"whoami"}},
		{"nothing there", "blog", "production", []string{}},
		{"unknown project", "nope", "", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs, err := s.ListAppsIn(ctx, tt.project, tt.env)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, r := range recs {
				got = append(got, r.App.ID)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("ListAppsIn(%q, %q) = %v, want %v", tt.project, tt.env, got, tt.want)
			}
		})
	}
	if all, err := s.ListApps(ctx); err != nil || len(all) != 5 {
		t.Errorf("ListApps = %d apps, %v; want 5", len(all), err)
	}
}

func TestDeploymentSourceApp(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	saveAppWithID(t, s, "shop--staging--web")
	saveAppWithID(t, s, "shop--production--web")

	create := func(appID, id, source string) DeploymentRecord {
		t.Helper()
		rec, err := s.CreateDeployment(ctx, appID, newDeployment(id), nil, nil, source)
		if err != nil {
			t.Fatalf("CreateDeployment %s: %v", id, err)
		}
		return rec
	}
	plain := create("shop--staging--web", "dep-plain", "")
	redeploy := create("shop--staging--web", "dep-redeploy", "dep-plain")
	promotion := create("shop--production--web", "dep-promoted", "dep-plain")
	create("shop--production--web", "dep-dangling", "dep-gone") // the source was deleted

	if plain.SourceApp != "" || redeploy.SourceApp != "" {
		t.Errorf("SourceApp of a plain deploy %q and of a redeploy %q, want both empty", plain.SourceApp, redeploy.SourceApp)
	}
	if redeploy.Source != "dep-plain" {
		t.Errorf("Source = %q, want dep-plain", redeploy.Source)
	}
	if promotion.SourceApp != "shop--staging--web" || promotion.Source != "dep-plain" {
		t.Errorf("promotion Source/SourceApp = %q/%q, want dep-plain/shop--staging--web", promotion.Source, promotion.SourceApp)
	}

	// The record names where its app is, and where the promotion came from.
	if want := (app.Place{Project: "shop", Environment: "production", Name: "web"}); promotion.Place != want || promotion.SourceEnvironment != "staging" {
		t.Errorf("promotion place %+v from %q, want %+v from staging", promotion.Place, promotion.SourceEnvironment, want)
	}
	if want := (app.Place{Project: "shop", Environment: "staging", Name: "web"}); plain.Place != want || plain.SourceEnvironment != "" || redeploy.SourceEnvironment != "" {
		t.Errorf("plain place %+v, source environments %q/%q", plain.Place, plain.SourceEnvironment, redeploy.SourceEnvironment)
	}

	// Every read path carries it.
	got, err := s.GetDeployment(ctx, "dep-promoted")
	if err != nil || got.SourceApp != "shop--staging--web" {
		t.Errorf("GetDeployment SourceApp = %q, %v", got.SourceApp, err)
	}
	list, err := s.ListDeployments(ctx, "shop--production--web", 0)
	if err != nil || len(list) != 2 || list[0].Deployment.ID != "dep-dangling" || list[1].SourceApp != "shop--staging--web" || list[0].SourceApp != "" {
		t.Errorf("ListDeployments = %v, %v", deploymentIDs(list), err)
	}
	latest, err := s.LatestDeployments(ctx)
	if err != nil || latest["shop--production--web"].Deployment.ID != "dep-dangling" {
		t.Errorf("LatestDeployments = %v, %v", latest, err)
	}
	recent, err := s.ListRecentDeployments(ctx, 10)
	if err != nil || len(recent) != 4 {
		t.Fatalf("ListRecentDeployments = %d, %v", len(recent), err)
	}
	if i := slices.IndexFunc(recent, func(r DeploymentRecord) bool { return r.Deployment.ID == "dep-promoted" }); i < 0 || recent[i].SourceApp != "shop--staging--web" || recent[i].SourceEnvironment != "staging" || recent[i].Place.Environment != "production" {
		t.Errorf("recent deployments lose SourceApp: %+v", recent)
	}
	before, err := s.ListDeploymentsBefore(ctx, "shop--production--web", time.Now().Add(time.Hour), 10)
	if err != nil || len(before) != 2 {
		t.Errorf("ListDeploymentsBefore = %d, %v", len(before), err)
	}
	for _, id := range []string{"dep-promoted", "dep-dangling"} {
		if _, err := s.UpdateDeployment(ctx, id, DeploymentUpdate{Status: StatusRunning}); err != nil {
			t.Fatal(err)
		}
	}
	unfinished, err := s.ListUnfinishedDeployments(ctx)
	if err != nil || len(unfinished) != 4 {
		t.Errorf("ListUnfinishedDeployments = %d, %v; want 4", len(unfinished), err)
	}
	healthy, err := s.UpdateDeployment(ctx, "dep-promoted", DeploymentUpdate{Status: StatusHealthy})
	if err != nil || healthy.SourceApp != "shop--staging--web" {
		t.Fatalf("UpdateDeployment SourceApp = %q, %v", healthy.SourceApp, err)
	}
	if h, err := s.LatestHealthyDeployment(ctx, "shop--production--web"); err != nil || h.SourceApp != "shop--staging--web" {
		t.Errorf("LatestHealthyDeployment SourceApp = %q, %v", h.SourceApp, err)
	}
}

// migrationsUpTo returns the embedded migrations with a version of at most n,
// so a test can build a store as an older Caravana left it.
func migrationsUpTo(t *testing.T, n int) fs.FS {
	t.Helper()
	all := testMigrations(t)
	known, err := readMigrations(all)
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, m := range known {
		if m.version > n {
			continue
		}
		body, err := fs.ReadFile(all, m.file)
		if err != nil {
			t.Fatal(err)
		}
		out[m.file] = &fstest.MapFile{Data: body}
	}
	return out
}

// TestUpgradeTo0011 migrates a store holding data as feature 018 left it:
// nothing may change except that every app now sits in default/production.
func TestUpgradeTo0011(t *testing.T) {
	ctx := context.Background()
	db := testDB(t, t.TempDir())
	if n, err := migrate(ctx, db, migrationsUpTo(t, 10)); err != nil || n != 10 {
		t.Fatalf("migrate to 0010 = %d, %v; want 10", n, err)
	}

	// Two apps, one with an ID projects would refuse, written by hand: the
	// store's own writes know the new columns, which don't exist yet.
	ids := []string{"whoami", "blog--v2"}
	docs := map[string]string{}
	for _, id := range ids {
		a := apptest.Whoami().App
		a.ID = id
		doc, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		docs[id] = string(doc)
		if _, err := db.ExecContext(ctx,
			"INSERT INTO apps (id, schema_version, document, created_at, updated_at) VALUES (?, 1, ?, '2026-10-01T10:00:00.000000000Z', '2026-10-01T10:00:00.000000000Z')",
			id, string(doc)); err != nil {
			t.Fatal(err)
		}
	}
	revisionBefore := map[string]string{}
	for id, doc := range docs {
		revisionBefore[id] = revisionOf(doc, "", false)
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	mustExec("INSERT INTO webhook_tokens (app_id, token_hash, created_at) VALUES ('whoami', 'abc123', '2026-10-01T10:01:00.000000000Z')")
	mustExec(`INSERT INTO backups (app_id, id, service, job_id, status, started_at)
		VALUES ('whoami', 'db-1', 'db', 'whoami_db_backup/periodic-1', 'succeeded', '2026-10-01T10:02:00.000000000Z')`)
	mustExec(`INSERT INTO deployments (id, app_id, trigger, digests, status, created_at, updated_at)
		VALUES ('dep-1', 'whoami', 'api', '{"web":"traefik/whoami@sha256:abc"}', 'healthy', '2026-10-01T10:03:00.000000000Z', '2026-10-01T10:03:00.000000000Z')`)
	mustExec(`INSERT INTO deployments (id, app_id, trigger, digests, status, source_deployment, created_at, updated_at)
		VALUES ('dep-2', 'whoami', 'api', '{}', 'healthy', 'dep-1', '2026-10-01T10:04:00.000000000Z', '2026-10-01T10:04:00.000000000Z')`)

	if n, err := migrate(ctx, db, testMigrations(t)); err != nil || n != len(embeddedVersions(t))-10 {
		t.Fatalf("migrate on = %d, %v", n, err)
	}
	s := &Store{db: db, now: time.Now}

	for _, id := range ids {
		rec, err := s.GetApp(ctx, id)
		if err != nil {
			t.Fatalf("GetApp %s: %v", id, err)
		}
		if rec.App.ID != id {
			t.Errorf("ID = %q, want %q", rec.App.ID, id)
		}
		if rec.Revision != revisionBefore[id] {
			t.Errorf("%s: revision %s after the upgrade, was %s", id, rec.Revision, revisionBefore[id])
		}
		want := app.Place{Project: "default", Environment: "production", Name: id}
		if rec.Place != want {
			t.Errorf("%s: place = %+v, want %+v", id, rec.Place, want)
		}
		if !rec.CreatedAt.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) {
			t.Errorf("%s: CreatedAt = %v, changed by the upgrade", id, rec.CreatedAt)
		}
	}
	if recs, err := s.ListAppsIn(ctx, "default", "production"); err != nil || len(recs) != 2 {
		t.Errorf("ListAppsIn(default, production) = %d, %v; want both apps", len(recs), err)
	}
	if envs, err := s.ListEnvironments(ctx, "default"); err != nil || len(envs) != 1 || envs[0].Apps != 2 {
		t.Errorf("ListEnvironments(default) = %+v, %v; want production with 2 apps", envs, err)
	}

	if tok, err := s.GetWebhookToken(ctx, "whoami"); err != nil || tok.Hash != "abc123" {
		t.Errorf("webhook token = %+v, %v; want the same hash", tok, err)
	}
	if backups, err := s.ListBackups(ctx, "whoami", 10); err != nil || len(backups) != 1 || backups[0].ID != "db-1" {
		t.Errorf("backups = %+v, %v; want db-1", backups, err)
	}
	deps, err := s.ListDeployments(ctx, "whoami", 0)
	if err != nil || len(deps) != 2 {
		t.Fatalf("deployments = %d, %v; want 2", len(deps), err)
	}
	if deps[0].Deployment.ID != "dep-2" || deps[0].Source != "dep-1" || deps[0].SourceApp != "" {
		t.Errorf("newest deployment = %+v, want dep-2 redeploying dep-1 (same app, no SourceApp)", deps[0])
	}
	if want := map[string]string{"web": "traefik/whoami@sha256:abc"}; !reflect.DeepEqual(deps[1].Deployment.Digests, want) {
		t.Errorf("digests = %v, want %v", deps[1].Deployment.Digests, want)
	}

	// The migration is recorded once and a second run does nothing.
	if n, err := migrate(ctx, db, testMigrations(t)); err != nil || n != 0 {
		t.Errorf("second migrate = %d, %v; want 0", n, err)
	}
}
