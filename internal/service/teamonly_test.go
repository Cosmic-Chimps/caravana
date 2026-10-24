package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
	"github.com/cosmic-chimps/caravana/internal/secrets/secretstest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// teamEnv is a service whose store directory is known and whose log is kept,
// for the searches of the password and its hash.
type teamEnv struct {
	*env
	dir  string
	logs *syncBuffer
}

func newTeamEnv(t *testing.T) *teamEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{store: st, registry: registrytest.New(t), nomad: deploytest.New(t), secrets: secretstest.New()}
	e.registry.AddIndex("traefik/whoami", "latest", whoamiDigest)
	e.nomad.Succeed()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.svc = startServiceLogging(t, st, newDeployer(t, e.registry, e.nomad), e.secrets, log)
	return &teamEnv{env: e, dir: dir, logs: logs}
}

// deployResult deploys a with DeployIfResult and waits for it to be healthy.
func (e *teamEnv) deployResult(t *testing.T, a app.App) DeployResult {
	t.Helper()
	res, err := e.svc.DeployIfResult(context.Background(), a, Condition{}, app.TriggerManual)
	if err != nil {
		t.Fatalf("DeployIfResult(%s): %v", a.ID, err)
	}
	if final := waitFinal(t, e.store, res.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("deployment = %s (%s), want healthy", final.Status, final.Error)
	}
	return res
}

// teamVariable is the variable Traefik's job reads.
func (e *teamEnv) teamVariable() map[string]string {
	return e.nomad.Variable(deploy.TraefikVariablePath)
}

// storeRows is every value of every table, one "table.column=value" line each.
func storeRows(t *testing.T, dir string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "caravana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	names, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for names.Next() {
		var n string
		if err := names.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	names.Close()
	var out []string
	for _, table := range tables {
		rows, err := db.Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]sql.NullString, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan %s: %v", table, err)
			}
			for i, v := range vals {
				out = append(out, fmt.Sprintf("%s.%s=%s", table, cols[i], v.String))
			}
		}
		rows.Close()
	}
	return out
}

func teamApp(id string, domains ...app.Domain) app.App {
	return domainApp(id, domains...)
}

func TestTeamOnlyPasswordShownOnce(t *testing.T) {
	ctx := context.Background()
	e := newTeamEnv(t)
	changes, cancel := e.svc.Subscribe("")
	defer cancel()

	a := teamApp("shop", app.Domain{Host: "shop.example.com"}, app.Domain{Host: "admin.example.com", TeamOnly: true})
	first := e.deployResult(t, a)
	password := first.TeamPasswords["admin.example.com"]
	if len(first.TeamPasswords) != 1 || len(password) != 32 {
		t.Fatalf("TeamPasswords = %v, want one 32-character password for admin.example.com", first.TeamPasswords)
	}

	row, err := e.store.TeamPassword(ctx, "admin.example.com")
	if err != nil {
		t.Fatalf("TeamPassword: %v", err)
	}
	if row.AppID != "shop" || !deploy.CheckTeamPassword(row.Hash, password) {
		t.Fatalf("row = %+v: not shop's, or its hash doesn't verify the password", row)
	}
	router := nomadjob.DomainRouterName("shop", "web", "admin.example.com")
	want := map[string]string{nomadjob.TeamAuthMiddleware(router): "team:" + row.Hash}
	if got := e.teamVariable(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("variable = %v, want %v", got, want)
	}

	// The same app again: nothing new to show, the hash stays.
	second := e.deployResult(t, a)
	if second.TeamPasswords != nil {
		t.Errorf("second deploy's TeamPasswords = %v, want nil", second.TeamPasswords)
	}
	if again, _ := e.store.TeamPassword(ctx, "admin.example.com"); again.Hash != row.Hash {
		t.Errorf("the hash changed on a deploy that kept the option")
	}
	// Nor does Deploy, Redeploy or the record-only answers.
	rec, err := e.svc.Redeploy(ctx, "shop", app.TriggerManual)
	if err != nil {
		t.Fatalf("Redeploy: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)

	// Where the password and the hash may and may not be.
	job, _ := json.Marshal(e.nomad.Job("shop"))
	_, export, err := e.svc.Export(ctx, "shop", "")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	stored, _ := e.svc.GetApp(ctx, "shop")
	views, _ := e.svc.Domains(ctx, DomainFilter{})
	deps, _ := e.store.ListDeployments(ctx, "shop", 10)
	later := map[string]string{
		"the job":                      string(job),
		"the export":                   string(export),
		"the log":                      e.logs.String(),
		"the stored app":               fmt.Sprintf("%+v", stored),
		"the domains list":             fmt.Sprintf("%+v", views),
		"the deployments":              fmt.Sprintf("%+v", deps),
		"the second answer":            fmt.Sprintf("%+v", second),
		"the redeploy's answer":        fmt.Sprintf("%+v", rec),
		"the notices":                  drainNotices(changes),
		"every variable but Traefik's": otherVariables(e.nomad),
	}
	for what, text := range later {
		if strings.Contains(text, password) {
			t.Errorf("%s holds the password", what)
		}
		if strings.Contains(text, row.Hash) {
			t.Errorf("%s holds the hash", what)
		}
	}
	for _, line := range storeRows(t, e.dir) {
		if strings.Contains(line, password) {
			t.Errorf("a stored row holds the password: %s", line)
		}
		if strings.Contains(line, row.Hash) && !strings.HasPrefix(line, "team_passwords.hash=") {
			t.Errorf("a row outside team_passwords holds the hash: %s", line)
		}
	}
	if !strings.Contains(string(job), nomadjob.TeamAuthMiddleware(router)+"@file") {
		t.Errorf("the job's router doesn't name the middleware %s@file", nomadjob.TeamAuthMiddleware(router))
	}
}

// otherVariables is every variable's items except Traefik's, as text.
func otherVariables(n *deploytest.Nomad) string {
	var out strings.Builder
	for _, p := range n.VariablePaths() {
		if p != deploy.TraefikVariablePath {
			fmt.Fprintf(&out, "%s=%v\n", p, n.Variable(p))
		}
	}
	return out.String()
}

func TestTeamOnlyRemoved(t *testing.T) {
	ctx := context.Background()
	e := newTeamEnv(t)
	on := teamApp("shop", app.Domain{Host: "admin.example.com", TeamOnly: true}, app.Domain{Host: "staff.example.com", TeamOnly: true})
	other := teamApp("blog", app.Domain{Host: "blog.example.com", TeamOnly: true})
	e.deployResult(t, on)
	e.deployResult(t, other)
	if got := len(e.teamVariable()); got != 3 {
		t.Fatalf("variable has %d items, want 3", got)
	}

	hosts := func() []string {
		rows, err := e.store.TeamPasswords(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.Host)
		}
		return out
	}

	// The option turned off on one domain, the other domain removed.
	off := teamApp("shop", app.Domain{Host: "admin.example.com"})
	e.deployResult(t, off)
	if got := hosts(); fmt.Sprint(got) != "[blog.example.com]" {
		t.Fatalf("rows after the option went = %v, want only blog's", got)
	}
	if got := e.teamVariable(); len(got) != 1 || got[nomadjob.TeamAuthMiddleware(nomadjob.DomainRouterName("blog", "web", "blog.example.com"))] == "" {
		t.Fatalf("variable = %v, want blog's middleware only", got)
	}

	// Turned on again: a new password, shown again.
	res := e.deployResult(t, teamApp("shop", app.Domain{Host: "admin.example.com", TeamOnly: true}))
	if res.TeamPasswords["admin.example.com"] == "" {
		t.Errorf("turning the option on again showed no password")
	}

	// The app deleted: its rows and its middleware go.
	if err := e.svc.DeleteApp(ctx, "shop"); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if got := hosts(); fmt.Sprint(got) != "[blog.example.com]" {
		t.Fatalf("rows after the delete = %v, want only blog's", got)
	}
	if got := e.teamVariable(); len(got) != 1 {
		t.Fatalf("variable after the delete = %v, want blog's middleware only", got)
	}
	if err := e.svc.DeleteApp(ctx, "blog"); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if got := e.teamVariable(); len(got) != 1 || got["CARAVANA_TEAM"] == "" {
		t.Fatalf("variable with no Team-only domain = %v, want the marker only", got)
	}
}

func TestTeamOnlyRegisterFailsForgetsPassword(t *testing.T) {
	ctx := context.Background()
	e := newTeamEnv(t)
	e.nomad.RejectNext(500, "no")
	_, err := e.svc.DeployIfResult(ctx, teamApp("shop", app.Domain{Host: "admin.example.com", TeamOnly: true}), Condition{}, app.TriggerManual)
	if err == nil {
		t.Fatal("deploy succeeded, want Nomad's refusal")
	}
	if rows, _ := e.store.TeamPasswords(ctx); len(rows) != 0 {
		t.Fatalf("rows after a failed register = %+v, want none (the password was never shown)", rows)
	}
	res := e.deployResult(t, teamApp("shop", app.Domain{Host: "admin.example.com", TeamOnly: true}))
	if res.TeamPasswords["admin.example.com"] == "" {
		t.Errorf("the next deploy showed no password")
	}
}

func TestRotateTeamPassword(t *testing.T) {
	ctx := context.Background()
	e := newTeamEnv(t)
	first := e.deployResult(t, teamApp("shop", app.Domain{Host: "admin.example.com", TeamOnly: true}, app.Domain{Host: "shop.example.com"}))
	old := first.TeamPasswords["admin.example.com"]

	password, err := e.svc.RotateTeamPassword(ctx, "shop", "Admin.Example.com")
	if err != nil {
		t.Fatalf("RotateTeamPassword: %v", err)
	}
	row, _ := e.store.TeamPassword(ctx, "admin.example.com")
	if password == old || !deploy.CheckTeamPassword(row.Hash, password) || deploy.CheckTeamPassword(row.Hash, old) {
		t.Fatalf("after rotation the hash doesn't verify the new password only")
	}
	middleware := nomadjob.TeamAuthMiddleware(nomadjob.DomainRouterName("shop", "web", "admin.example.com"))
	if got := e.teamVariable()[middleware]; got != "team:"+row.Hash {
		t.Errorf("variable's item = %q, want the new hash", got)
	}
	if strings.Contains(e.logs.String(), password) {
		t.Errorf("the log holds the rotated password")
	}

	// Refusals.
	for _, tc := range []struct {
		app, host string
		want      error
	}{
		{"shop", "shop.example.com", ErrNotTeamOnly},
		{"shop", "nope.example.com", ErrDomainNotFound},
		{"nope", "admin.example.com", store.ErrNotFound},
	} {
		if _, err := e.svc.RotateTeamPassword(ctx, tc.app, tc.host); !errors.Is(err, tc.want) {
			t.Errorf("RotateTeamPassword(%s, %s) = %v, want %v", tc.app, tc.host, err, tc.want)
		}
	}

	// A variable that can't be written keeps the old password.
	e.nomad.FailVariables(500)
	if _, err := e.svc.RotateTeamPassword(ctx, "shop", "admin.example.com"); err == nil {
		t.Errorf("rotation with Nomad failing succeeded")
	}
	e.nomad.FailVariables(0)
	if after, _ := e.store.TeamPassword(ctx, "admin.example.com"); after.Hash != row.Hash {
		t.Errorf("a failed rotation changed the stored hash")
	}

	stopDomainApp(t, e.env, "shop")
	if _, err := e.svc.RotateTeamPassword(ctx, "shop", "admin.example.com"); !errors.Is(err, ErrAppStopped) {
		t.Errorf("rotation of a stopped app = %v, want ErrAppStopped", err)
	}
}

func TestDomainTaken(t *testing.T) {
	ctx := context.Background()
	e := newTeamEnv(t)
	e.deployResult(t, teamApp("blog", app.Domain{Host: "blog.example.com"}, app.Domain{Host: "example.org", WWWRedirect: true}))

	_, err := e.svc.Deploy(ctx, teamApp("shop", app.Domain{Host: "shop.example.com"}, app.Domain{Host: "blog.example.com"}), app.TriggerManual)
	var taken *DomainTakenError
	if !errors.As(err, &taken) || !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("Deploy = %v, want a *DomainTakenError", err)
	}
	if taken.Field != "services[0].domains[1].host" || taken.App != "blog" {
		t.Errorf("error = %+v, want the field services[0].domains[1].host and the app blog", taken)
	}
	if _, err := e.store.GetApp(ctx, "shop"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the refused app was saved")
	}

	// The pair another app redirects is taken too.
	if _, err := e.svc.Deploy(ctx, teamApp("shop", app.Domain{Host: "www.example.org"}), app.TriggerManual); !errors.As(err, &taken) {
		t.Errorf("Deploy of a redirected pair = %v, want a *DomainTakenError", err)
	}
	// A wwwRedirect whose pair is another app's domain names the option.
	e.deployResult(t, teamApp("www", app.Domain{Host: "www.example.net"}))
	_, err = e.svc.Deploy(ctx, teamApp("shop", app.Domain{Host: "example.net", WWWRedirect: true}), app.TriggerManual)
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "services[0].domains[0].wwwRedirect" {
		t.Errorf("Deploy with another app's pair = %v, want a field error on wwwRedirect", err)
	}

	// An app redeploying a host it already has is never refused, even when a
	// second app claims it (stored before this check existed).
	if _, err := e.store.InsertApp(ctx, teamApp("old", app.Domain{Host: "blog.example.com"}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Deploy(ctx, teamApp("old", app.Domain{Host: "blog.example.com"}), app.TriggerManual); err != nil {
		t.Errorf("redeploy of a host the app had = %v, want it accepted", err)
	}
}

func TestTeamEntries(t *testing.T) {
	apps := map[string]app.App{
		"shop": teamApp("shop", app.Domain{Host: "a.example.com", TeamOnly: true}, app.Domain{Host: "b.example.com"}),
	}
	rows := []store.TeamPasswordRecord{
		{Host: "a.example.com", AppID: "shop", Hash: "h1"},
		{Host: "b.example.com", AppID: "shop", Hash: "h2"}, // option off: left out
		{Host: "c.example.com", AppID: "gone", Hash: "h3"}, // app gone: left out
	}
	got := teamEntries(rows, apps)
	want := map[string]string{nomadjob.TeamAuthMiddleware(nomadjob.DomainRouterName("shop", "web", "a.example.com")): "team:h1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("teamEntries = %v, want %v", got, want)
	}
}
