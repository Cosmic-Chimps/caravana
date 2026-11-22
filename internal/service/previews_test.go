package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// previewPassword is a preview password the tests search for everywhere it must
// not be.
const previewPassword = "s3cret-sentinel-049-preview"

// previewEnv is a team env with project blog and its staging environment.
func newPreviewEnv(t *testing.T) *teamEnv {
	t.Helper()
	e := newTeamEnv(t)
	ctx := context.Background()
	if _, err := e.svc.CreateProject(ctx, "blog", "Blog"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "staging"); err != nil {
		t.Fatal(err)
	}
	return e
}

// previewWeb is the web app of blog in environment env, with the domain host.
func previewWeb(env, host string) app.App {
	id := app.Place{Project: "blog", Environment: env, Name: "web"}.ID()
	if host == "" {
		return domainApp(id)
	}
	return domainApp(id, app.Domain{Host: host})
}

func TestCreatePreviewEnvironment(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)

	for _, name := range []string{"pr-07", "pr-x", "pr-12-old", "pr-0"} {
		_, err := e.svc.CreateEnvironment(ctx, "blog", name)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "name" || !strings.Contains(fe.Message, "kept for previews") {
			t.Errorf("CreateEnvironment(%q) = %v, want a field error on name about previews", name, err)
		}
	}

	env, err := e.svc.CreateEnvironment(ctx, "blog", "pr-12")
	if err != nil {
		t.Fatalf("CreateEnvironment(pr-12): %v", err)
	}
	if env.Preview == nil || env.Preview.Number != 12 {
		t.Fatalf("Preview = %+v, want number 12", env.Preview)
	}
	if want := env.Preview.CreatedAt.Add(7 * 24 * time.Hour); !env.Preview.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want created + 7 days (%v)", env.Preview.ExpiresAt, want)
	}
	if stg, err := e.svc.CreateEnvironment(ctx, "blog", "qa"); err != nil || stg.Preview != nil {
		t.Errorf("a plain environment = %+v, %v; want no Preview", stg, err)
	}

	// The project shows the preview on its environment.
	p, err := e.svc.Project(ctx, "blog")
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range p.Environments {
		if (en.Name == "pr-12") != (en.Preview != nil) {
			t.Errorf("environment %s has Preview = %+v", en.Name, en.Preview)
		}
	}
}

func TestPreviewLimit(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)
	one := 1
	if _, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{Limit: &one}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "pr-3"); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CreateEnvironment(ctx, "blog", "pr-4")
	var le *PreviewLimitError
	if !errors.Is(err, ErrPreviewLimit) || !errors.As(err, &le) || !slices.Equal(le.Open, []string{"pr-3"}) {
		t.Fatalf("a second preview = %v, want ErrPreviewLimit naming pr-3", err)
	}
	// Staging is not a preview, and the limit is the project's own.
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "qa"); err != nil {
		t.Errorf("a plain environment at the preview limit: %v", err)
	}
	if _, err := e.svc.CreateProject(ctx, "docs", "Docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateEnvironment(ctx, "docs", "pr-3"); err != nil {
		t.Errorf("another project's preview: %v", err)
	}
	// Limit 0 turns previews off.
	zero := 0
	if _, err := e.svc.SetPreviewSettings(ctx, "docs", PreviewSettingsInput{Limit: &zero}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateEnvironment(ctx, "docs", "pr-4"); !errors.Is(err, ErrPreviewLimit) {
		t.Errorf("limit 0 = %v, want ErrPreviewLimit", err)
	}
}

func TestSetPreviewSettingsValidation(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)
	short, long := "short", strings.Repeat("x", 129)
	control := "a password\nwith a break"
	neg, big, zeroDays, bigDays := -1, 21, 0, 31
	tests := []struct {
		name  string
		in    PreviewSettingsInput
		field string
	}{
		{"negative limit", PreviewSettingsInput{Limit: &neg}, "limit"},
		{"limit over 20", PreviewSettingsInput{Limit: &big}, "limit"},
		{"zero days", PreviewSettingsInput{ExpireAfterDays: &zeroDays}, "expireAfterDays"},
		{"31 days", PreviewSettingsInput{ExpireAfterDays: &bigDays}, "expireAfterDays"},
		{"short password", PreviewSettingsInput{Password: &short}, "password"},
		{"long password", PreviewSettingsInput{Password: &long}, "password"},
		{"over bcrypt's 72 bytes", PreviewSettingsInput{Password: ptr(strings.Repeat("é", 37))}, "password"},
		{"control character", PreviewSettingsInput{Password: &control}, "password"},
		{"password and remove", PreviewSettingsInput{Password: ptr(previewPassword), RemovePassword: true}, "removePassword"},
	}
	for _, tt := range tests {
		_, err := e.svc.SetPreviewSettings(ctx, "blog", tt.in)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != tt.field {
			t.Errorf("%s = %v, want a field error on %s", tt.name, err, tt.field)
		}
		if err != nil && (strings.Contains(err.Error(), previewPassword) || strings.Contains(err.Error(), short)) {
			t.Errorf("%s: the error holds the password: %v", tt.name, err)
		}
	}
	if _, err := e.svc.SetPreviewSettings(ctx, "nope", PreviewSettingsInput{}); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("unknown project = %v, want ErrProjectNotFound", err)
	}
	// Nothing changed by the refusals; the boundaries work.
	got, err := e.svc.Previews(ctx, "blog")
	if err != nil || got.Settings.Limit != 5 || got.Settings.ExpireAfterDays != 7 || got.Settings.PasswordSet {
		t.Fatalf("settings after refusals = %+v, %v; want the defaults", got.Settings, err)
	}
	for _, in := range []PreviewSettingsInput{
		{Limit: ptr(0)}, {Limit: ptr(20)}, {ExpireAfterDays: ptr(1)}, {ExpireAfterDays: ptr(30)},
		{Password: ptr(strings.Repeat("p", 12))}, {Password: ptr(strings.Repeat("p", 72))}, {Password: ptr(strings.Repeat("é", 36))},
	} {
		if _, err := e.svc.SetPreviewSettings(ctx, "blog", in); err != nil {
			t.Errorf("SetPreviewSettings(%+v) = %v, want it accepted", in, err)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestPreviewsList(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)
	for _, n := range []string{"pr-12", "pr-3"} {
		if _, err := e.svc.CreateEnvironment(ctx, "blog", n); err != nil {
			t.Fatal(err)
		}
	}
	e.deployResult(t, previewWeb("pr-12", "pr-12-blog.example.test"))
	e.deployResult(t, previewWeb("staging", "staging-blog.example.test")) // not a preview

	list, err := e.svc.Previews(ctx, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if list.Settings.Open != 2 || len(list.Previews) != 2 || list.Previews[0].Number != 3 || list.Previews[1].Number != 12 {
		t.Fatalf("previews = %+v, want pr-3 then pr-12", list)
	}
	if apps := list.Previews[0].Apps; len(apps) != 0 {
		t.Errorf("pr-3 apps = %+v, want none", apps)
	}
	pr12 := list.Previews[1]
	if len(pr12.Apps) != 1 {
		t.Fatalf("pr-12 apps = %+v, want one", pr12.Apps)
	}
	a := pr12.Apps[0]
	if a.ID != "blog--pr-12--web" || a.Name != "web" || !slices.Equal(a.Addresses, []string{"pr-12-blog.example.test"}) {
		t.Errorf("app = %+v", a)
	}
	if a.MemoryMB != 64 { // the test deployer's default reservation, one copy
		t.Errorf("MemoryMB = %d, want 64", a.MemoryMB)
	}
	if a.LastDeployment == nil || a.LastDeployment.Status != store.StatusHealthy {
		t.Errorf("LastDeployment = %+v, want a healthy one", a.LastDeployment)
	}
	if !pr12.ExpiresAt.After(list.Previews[0].ExpiresAt) {
		t.Errorf("pr-12 (deployed later) expires %v, pr-3 %v: the deployment should extend it", pr12.ExpiresAt, list.Previews[0].ExpiresAt)
	}
	if _, err := e.svc.Previews(ctx, "nope"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("unknown project = %v, want ErrProjectNotFound", err)
	}
}

func TestClosePreview(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "pr-5"); err != nil {
		t.Fatal(err)
	}
	e.deployResult(t, previewWeb("pr-5", "pr-5-blog.example.test"))
	other := previewWeb("pr-5", "")
	other.ID = "blog--pr-5--api"
	other.Name = "api"
	e.deployResult(t, other)
	e.deployResult(t, previewWeb("staging", "staging-blog.example.test"))

	// An environment that isn't a preview is refused and nothing is deleted.
	if err := e.svc.ClosePreview(ctx, "blog", "staging", CloseByOwner); !errors.Is(err, ErrNotAPreview) {
		t.Errorf("ClosePreview(staging) = %v, want ErrNotAPreview", err)
	}
	if _, err := e.svc.GetApp(ctx, "blog--staging--web"); err != nil {
		t.Errorf("the staging app is gone: %v", err)
	}
	if err := e.svc.ClosePreview(ctx, "blog", "pr-99", CloseByOwner); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Errorf("an unknown environment = %v, want ErrEnvironmentNotFound", err)
	}
	if err := e.svc.ClosePreview(ctx, "nope", "pr-5", CloseByOwner); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("an unknown project = %v, want ErrProjectNotFound", err)
	}

	if err := e.svc.ClosePreview(ctx, "blog", "pr-5", CloseByOwner); err != nil {
		t.Fatalf("ClosePreview: %v", err)
	}
	for _, id := range []string{"blog--pr-5--web", "blog--pr-5--api"} {
		if _, err := e.svc.GetApp(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s after the close = %v, want it gone", id, err)
		}
		if job := e.nomad.Job(id); job != nil {
			t.Errorf("Nomad still has the job of %s", id)
		}
	}
	if ok, _ := e.store.EnvironmentExists(ctx, "blog", "pr-5"); ok {
		t.Error("the environment is still there")
	}
	if _, err := e.svc.GetApp(ctx, "blog--staging--web"); err != nil {
		t.Errorf("closing pr-5 took staging's app: %v", err)
	}
	// An empty preview closes too, and closing twice answers not found.
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "pr-6"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ClosePreview(ctx, "blog", "pr-6", CloseByOwner); err != nil {
		t.Errorf("an empty preview: %v", err)
	}
	if err := e.svc.ClosePreview(ctx, "blog", "pr-6", CloseByOwner); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Errorf("closing twice = %v, want ErrEnvironmentNotFound", err)
	}
}

func TestPreviewReaper(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)
	clock := e.svc.clock.(*fakeClock)
	e.store.SetClock(clock.Now)
	t.Cleanup(func() { e.store.SetClock(time.Now) })
	start := clock.Now()

	for _, n := range []string{"pr-1", "pr-2", "pr-3"} {
		if _, err := e.svc.CreateEnvironment(ctx, "blog", n); err != nil {
			t.Fatal(err)
		}
	}
	e.deployResult(t, previewWeb("pr-1", "pr-1-blog.example.test"))

	// pr-2 is deployed three days in: its clock restarts there.
	clock.Set(start.Add(3 * 24 * time.Hour))
	pr2 := previewWeb("pr-2", "")
	e.deployResult(t, pr2)

	// Day 7 and a minute: pr-1 and pr-3 (never deployed) are past 7 days, pr-2 is not.
	clock.Set(start.Add(7*24*time.Hour + time.Minute))
	e.svc.reapPreviews(ctx)
	for env, wantOpen := range map[string]bool{"pr-1": false, "pr-3": false, "pr-2": true} {
		if ok, _ := e.store.EnvironmentExists(ctx, "blog", env); ok != wantOpen {
			t.Errorf("after day 7: %s open = %v, want %v", env, ok, wantOpen)
		}
	}
	if job := e.nomad.Job("blog--pr-1--web"); job != nil {
		t.Error("pr-1's job is still in Nomad")
	}
	if _, err := e.svc.GetApp(ctx, "blog--pr-2--web"); err != nil {
		t.Errorf("pr-2's app = %v, want it kept", err)
	}

	// Changing the days applies to the open ones at once.
	days := 2
	if _, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{ExpireAfterDays: &days}); err != nil {
		t.Fatal(err)
	}
	e.svc.reapPreviews(ctx)
	if ok, _ := e.store.EnvironmentExists(ctx, "blog", "pr-2"); ok {
		t.Error("pr-2 is still open past its 2 days")
	}

	// The test hook replaces the days.
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "pr-4"); err != nil {
		t.Fatal(err)
	}
	e.svc.reapPreviews(ctx)
	if ok, _ := e.store.EnvironmentExists(ctx, "blog", "pr-4"); !ok {
		t.Fatal("a new preview was reaped")
	}
	e.svc.SetPreviewExpiryForTests(30 * time.Second)
	clock.Set(clock.Now().Add(31 * time.Second))
	e.svc.reapPreviews(ctx)
	if ok, _ := e.store.EnvironmentExists(ctx, "blog", "pr-4"); ok {
		t.Error("pr-4 is still open past the test expiry")
	}
	// Staging is never reaped.
	if ok, _ := e.store.EnvironmentExists(ctx, "blog", "staging"); !ok {
		t.Error("the reaper closed staging")
	}
}

// A failure is logged and the pass goes on to the next preview; the next pass
// tries the first again.
func TestPreviewReaperGoesOnAfterAFailure(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)
	clock := e.svc.clock.(*fakeClock)
	e.store.SetClock(clock.Now)
	t.Cleanup(func() { e.store.SetClock(time.Now) })
	for _, n := range []string{"pr-1", "pr-2"} {
		if _, err := e.svc.CreateEnvironment(ctx, "blog", n); err != nil {
			t.Fatal(err)
		}
	}
	e.deployResult(t, previewWeb("pr-1", ""))
	clock.Set(clock.Now().Add(8 * 24 * time.Hour))

	// Nomad refuses the volumes calls the delete makes: pr-1 stays, pr-2 (no apps) closes.
	e.nomad.FailVariables(500)
	e.svc.reapPreviews(ctx)
	e.nomad.FailVariables(0)
	if ok, _ := e.store.EnvironmentExists(ctx, "blog", "pr-2"); ok {
		t.Error("the pass stopped at the failure: pr-2 is still open")
	}
	if !strings.Contains(e.logs.String(), "close an expired preview") {
		t.Errorf("the failure wasn't logged: %s", e.logs.String())
	}
	e.svc.reapPreviews(ctx)
	if ok, _ := e.store.EnvironmentExists(ctx, "blog", "pr-1"); ok {
		t.Error("the next pass didn't close pr-1")
	}
}

func TestPreviewTeamOnly(t *testing.T) {
	a := domainApp("blog--pr-1--web",
		app.Domain{Host: "a.example.test"}, app.Domain{Host: "b.example.test", TeamOnly: true}, app.Domain{Host: "c.example.test"})
	out, field := previewTeamOnly(a)
	if field != "services[0].domains[0].teamOnly" {
		t.Errorf("field = %q, want the first changed", field)
	}
	for i, d := range out.Services[0].Domains {
		if !d.TeamOnly {
			t.Errorf("domain %d is not Team only", i)
		}
	}
	if a.Services[0].Domains[0].TeamOnly || a.Services[0].Domains[2].TeamOnly {
		t.Error("the input app was changed")
	}
	// When every domain already is, or there is none, nothing changed.
	for _, same := range []app.App{domainApp("x"), domainApp("x", app.Domain{Host: "t.example.test", TeamOnly: true})} {
		if _, field := previewTeamOnly(same); field != "" {
			t.Errorf("field = %q for an app with nothing to change", field)
		}
	}
	// The first changed field is the first one that wasn't Team only.
	second := domainApp("x", app.Domain{Host: "t.example.test", TeamOnly: true}, app.Domain{Host: "u.example.test"})
	if _, field := previewTeamOnly(second); field != "services[0].domains[1].teamOnly" {
		t.Errorf("field = %q, want domains[1]", field)
	}
}

func TestPreviewPasswordProtectsDeploys(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)
	changes, cancel := e.svc.Subscribe("")
	defer cancel()

	// Without a password a preview is public: no Team only, no warning.
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "pr-7"); err != nil {
		t.Fatal(err)
	}
	pub := e.deployResult(t, previewWeb("pr-7", "pr-7-blog.example.test"))
	if len(pub.Warnings) != 0 || pub.TeamPasswords != nil {
		t.Errorf("a public preview deploy = %+v warnings, %v passwords", pub.Warnings, pub.TeamPasswords)
	}
	if rows, _ := e.store.TeamPasswords(ctx); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}

	// Setting it the first time with a preview open says so, and protects at its next deploy.
	res, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{Password: ptr(previewPassword)})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Settings.PasswordSet || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Message, "open previews pr-7 get the password at their next deploy") {
		t.Errorf("result = %+v, want the first-time warning naming pr-7", res)
	}
	rec, _ := e.store.GetProject(ctx, "blog")
	if !deploy.CheckTeamPassword(rec.PreviewPasswordHash, previewPassword) {
		t.Fatal("the stored hash doesn't verify the password")
	}

	protected := e.deployResult(t, previewWeb("pr-7", "pr-7-blog.example.test"))
	if protected.TeamPasswords != nil {
		t.Errorf("TeamPasswords = %v, want none: the project's password is used", protected.TeamPasswords)
	}
	var warned bool
	for _, w := range protected.Warnings {
		if w.Field == "services[0].domains[0].teamOnly" &&
			w.Message == "domains of preview environments are Team only with project blog's preview password" {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %+v, want the Team-only warning on the first changed field", protected.Warnings)
	}
	stored, _ := e.svc.GetApp(ctx, "blog--pr-7--web")
	if !stored.App.Services[0].Domains[0].TeamOnly {
		t.Error("the stored app doesn't say Team only")
	}
	row, err := e.store.TeamPassword(ctx, "pr-7-blog.example.test")
	if err != nil || row.Hash != rec.PreviewPasswordHash || row.AppID != "blog--pr-7--web" {
		t.Fatalf("row = %+v, %v; want the project's hash", row, err)
	}
	router := nomadjob.DomainRouterName("blog--pr-7--web", "web", "pr-7-blog.example.test")
	if got := e.teamVariable()[nomadjob.TeamAuthMiddleware(router)]; got != "team:"+rec.PreviewPasswordHash {
		t.Errorf("variable's item = %q, want the project's hash", got)
	}
	// A client that sends no teamOnly is told again; a redeploy of the stored app, which says it, is not.
	again := e.deployResult(t, previewWeb("pr-7", "pr-7-blog.example.test"))
	if len(again.Warnings) != 1 {
		t.Errorf("a deploy without teamOnly = %+v warnings, want the one", again.Warnings)
	}
	redone, err := e.svc.Redeploy(ctx, "blog--pr-7--web", app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if final := waitFinal(t, e.store, redone.Deployment.ID); len(final.Warnings) != 0 {
		t.Errorf("a redeploy warns: %+v", final.Warnings)
	}
	// A domain added by a later deploy is protected too, and a domain added by a later deploy is protected too.
	two := domainApp("blog--pr-7--web", app.Domain{Host: "pr-7-blog.example.test"}, app.Domain{Host: "pr-7-extra.example.test"})
	e.deployResult(t, two)
	if r, err := e.store.TeamPassword(ctx, "pr-7-extra.example.test"); err != nil || r.Hash != rec.PreviewPasswordHash {
		t.Errorf("the added domain's row = %+v, %v", r, err)
	}

	// Rotating one of these domains is refused: the project's password is changed in its settings.
	_, err = e.svc.RotateTeamPassword(ctx, "blog--pr-7--web", "pr-7-blog.example.test")
	var pe *PreviewPasswordError
	if !errors.Is(err, ErrPreviewPassword) || !errors.As(err, &pe) || pe.Project != "blog" ||
		!strings.Contains(err.Error(), "change it in the project's preview settings") {
		t.Errorf("RotateTeamPassword = %v, want ErrPreviewPassword naming blog", err)
	}
	// A staging app of the same project has no preview password.
	stg := e.deployResult(t, previewWeb("staging", "staging-blog.example.test"))
	if len(stg.Warnings) != 0 || stg.TeamPasswords != nil {
		t.Errorf("a staging deploy = %+v / %v, want it untouched", stg.Warnings, stg.TeamPasswords)
	}

	// Where the password may not be.
	texts := map[string]string{
		"the log":       e.logs.String(),
		"the result":    fmt.Sprintf("%+v", res),
		"the deploy":    fmt.Sprintf("%+v", protected),
		"the stored":    fmt.Sprintf("%+v", stored),
		"the notices":   drainNotices(changes),
		"the variables": otherVariables(e.nomad) + fmt.Sprint(e.teamVariable()),
	}
	list, _ := e.svc.Previews(ctx, "blog")
	texts["the list"] = fmt.Sprintf("%+v", list)
	for what, text := range texts {
		if strings.Contains(text, previewPassword) {
			t.Errorf("%s holds the password", what)
		}
	}
	for _, line := range storeRows(t, e.dir) {
		if strings.Contains(line, previewPassword) {
			t.Errorf("a stored row holds the password: %s", line)
		}
		if strings.Contains(line, rec.PreviewPasswordHash) &&
			!strings.HasPrefix(line, "team_passwords.hash=") && !strings.HasPrefix(line, "projects.preview_password_hash=") {
			t.Errorf("a row outside the two hash columns holds the hash: %s", line)
		}
	}
}

func TestPreviewPasswordChangeAppliesAtOnce(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)
	if _, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{Password: ptr(previewPassword)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "pr-7"); err != nil {
		t.Fatal(err)
	}
	e.deployResult(t, previewWeb("pr-7", "pr-7-blog.example.test"))
	// A Team-only domain of staging has its own password and must not change.
	own := domainApp("blog--staging--web", app.Domain{Host: "admin-blog.example.test", TeamOnly: true})
	ownRes := e.deployResult(t, own)
	ownRow, _ := e.store.TeamPassword(ctx, "admin-blog.example.test")
	if ownRes.TeamPasswords["admin-blog.example.test"] == "" {
		t.Fatal("staging's own Team-only domain got no password")
	}

	deployments := func() int {
		recs, _ := e.store.ListDeployments(ctx, "blog--pr-7--web", 50)
		return len(recs)
	}
	before := deployments()
	next := "another-preview-password"
	if _, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{Password: &next}); err != nil {
		t.Fatal(err)
	}
	if deployments() != before {
		t.Error("changing the password made a deploy")
	}
	proj, _ := e.store.GetProject(ctx, "blog")
	row, _ := e.store.TeamPassword(ctx, "pr-7-blog.example.test")
	if row.Hash != proj.PreviewPasswordHash || !deploy.CheckTeamPassword(row.Hash, next) || deploy.CheckTeamPassword(row.Hash, previewPassword) {
		t.Errorf("the preview's row doesn't verify the new password only")
	}
	router := nomadjob.DomainRouterName("blog--pr-7--web", "web", "pr-7-blog.example.test")
	if got := e.teamVariable()[nomadjob.TeamAuthMiddleware(router)]; got != "team:"+proj.PreviewPasswordHash {
		t.Errorf("variable's item = %q, want the new hash", got)
	}
	if after, _ := e.store.TeamPassword(ctx, "admin-blog.example.test"); after.Hash != ownRow.Hash {
		t.Error("the change touched a domain outside the previews")
	}

	// A variable that can't be written keeps the old hash everywhere.
	e.nomad.FailVariables(500)
	third := "a-third-preview-password"
	if _, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{Password: &third}); err == nil {
		t.Fatal("the change succeeded with Nomad failing")
	}
	e.nomad.FailVariables(0)
	proj2, _ := e.store.GetProject(ctx, "blog")
	row2, _ := e.store.TeamPassword(ctx, "pr-7-blog.example.test")
	if proj2.PreviewPasswordHash != proj.PreviewPasswordHash || row2.Hash != proj.PreviewPasswordHash {
		t.Error("a failed change left a new hash somewhere")
	}
	if got := e.teamVariable()[nomadjob.TeamAuthMiddleware(router)]; got != "team:"+proj.PreviewPasswordHash {
		t.Errorf("variable's item after a failed change = %q, want the old hash", got)
	}
	if strings.Contains(e.logs.String(), next) || strings.Contains(e.logs.String(), third) {
		t.Error("the log holds a password")
	}
}

func TestPreviewPasswordRemoval(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)
	if _, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{Password: ptr(previewPassword)}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"pr-3", "pr-9"} {
		if _, err := e.svc.CreateEnvironment(ctx, "blog", n); err != nil {
			t.Fatal(err)
		}
	}
	_, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{RemovePassword: true})
	var oe *PreviewsOpenError
	if !errors.Is(err, ErrPreviewsOpen) || !errors.As(err, &oe) || !slices.Equal(oe.Open, []string{"pr-3", "pr-9"}) {
		t.Fatalf("removal with previews open = %v, want ErrPreviewsOpen naming pr-3 and pr-9", err)
	}
	if got, _ := e.svc.Previews(ctx, "blog"); !got.Settings.PasswordSet {
		t.Error("the refused removal removed the password")
	}
	for _, n := range []string{"pr-3", "pr-9"} {
		if err := e.svc.ClosePreview(ctx, "blog", n, CloseByOwner); err != nil {
			t.Fatal(err)
		}
	}
	res, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{RemovePassword: true})
	if err != nil || res.Settings.PasswordSet {
		t.Errorf("removal with none open = %+v, %v; want it removed", res, err)
	}
	// With none set, removing is a no-op even while previews are open.
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "pr-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{RemovePassword: true}); err != nil {
		t.Errorf("removing a password that isn't set = %v", err)
	}
}

// A preview deployed before its project had a password becomes Team only at its
// next deploy of any kind, a redeploy of the stored app included.
func TestPreviewRedeployAfterPasswordIsProtected(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "pr-7"); err != nil {
		t.Fatal(err)
	}
	e.deployResult(t, previewWeb("pr-7", "pr-7-blog.example.test"))
	if _, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{Password: ptr(previewPassword)}); err != nil {
		t.Fatal(err)
	}
	rec, err := e.svc.Redeploy(ctx, "blog--pr-7--web", app.TriggerWebhook)
	if err != nil {
		t.Fatal(err)
	}
	final := waitFinal(t, e.store, rec.Deployment.ID)
	if len(final.Warnings) != 1 || final.Warnings[0].Field != "services[0].domains[0].teamOnly" {
		t.Errorf("warnings = %+v, want the Team-only one", final.Warnings)
	}
	stored, _ := e.svc.GetApp(ctx, "blog--pr-7--web")
	if !stored.App.Services[0].Domains[0].TeamOnly {
		t.Error("the stored app isn't Team only after the redeploy")
	}
	proj, _ := e.store.GetProject(ctx, "blog")
	if row, err := e.store.TeamPassword(ctx, "pr-7-blog.example.test"); err != nil || row.Hash != proj.PreviewPasswordHash {
		t.Errorf("row = %+v, %v; want the project's hash", row, err)
	}
}
