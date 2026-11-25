package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Phase B of the audit log (feature 057): every public service method that changes an app, or what hangs
// off it, notes one entry once the change is stored, and a call that failed or changed nothing notes none.

// noted runs f and returns the audit entries it added, oldest first.
func noted(t *testing.T, st *store.Store, f func()) []audit.Event {
	t.Helper()
	before := len(auditRows(t, st))
	f()
	return auditRows(t, st)[before:]
}

// notedOne is noted for a call that must leave exactly one entry, which it returns.
func notedOne(t *testing.T, st *store.Store, f func()) audit.Event {
	t.Helper()
	got := noted(t, st, f)
	if len(got) != 1 {
		t.Fatalf("%d audit entries, want 1: %+v", len(got), got)
	}
	return got[0]
}

// wantEntry checks the entry's action, resource kind and ID, and that it is not a fallback.
func wantEntry(t *testing.T, got audit.Event, action audit.Action, kind audit.Kind, id string) {
	t.Helper()
	if got.Action != action || got.Resource.Kind != kind || got.Resource.ID != id || got.Fallback {
		t.Errorf("entry = %s %s %q (fallback %v), want %s %s %q", got.Action, got.Resource.Kind, got.Resource.ID, got.Fallback, action, kind, id)
	}
}

// wantPlace checks the resource's name, project and environment.
func wantPlace(t *testing.T, got audit.Event, name, project, environment string) {
	t.Helper()
	r := got.Resource
	if r.Name != name || r.Project != project || r.Environment != environment {
		t.Errorf("resource = %+v, want name %q in %s › %s", r, name, project, environment)
	}
}

func wantRecord(t *testing.T, got audit.Event, kind audit.RecordKind, id string) {
	t.Helper()
	if got.Record == nil || got.Record.Kind != kind || got.Record.ID != id {
		t.Errorf("record = %+v, want %s %s", got.Record, kind, id)
	}
}

// change finds the change at path, or nil.
func change(changes []audit.Change, path string) *audit.Change {
	for i := range changes {
		if changes[i].Path == path {
			return &changes[i]
		}
	}
	return nil
}

func changePaths(changes []audit.Change) []string {
	var out []string
	for _, c := range changes {
		out = append(out, string(c.Op)+" "+c.Path)
	}
	return out
}

// everyEntry is every audit entry as one JSON text, for the sentinel tests.
func everyEntry(t *testing.T, st *store.Store) string {
	t.Helper()
	raw, err := json.Marshal(auditRows(t, st))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAuditNotesTheDeployPaths(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)

	a := whoamiAs(productionID)
	a.Services[0].Domains = []app.Domain{{Host: "old.example.com", Port: "http"}}
	a.Services[0].Secrets = []app.Secret{set("DB_URL", "v1")}
	a.Services[0].Env = []app.EnvVar{{Name: "MODE", Value: "one"}}

	// A new app deployed: created, with its deployment, its place and the fields it has.
	var first store.DeploymentRecord
	got := notedOne(t, e.store, func() { first = deployHealthy(t, e, a) })
	wantEntry(t, got, audit.ActionCreated, audit.KindApp, productionID)
	wantPlace(t, got, "web", "shop", "production")
	wantRecord(t, got, audit.RecordDeployment, first.Deployment.ID)
	if len(got.Changes) == 0 {
		t.Error("a new app has no changes in its entry")
	}

	// A deploy that moves the domain, sets the secret again and changes an env value.
	b := a
	b.Services = append([]app.Service(nil), a.Services...)
	b.Services[0].Domains = []app.Domain{{Host: "new.example.com", Port: "http"}}
	b.Services[0].Secrets = []app.Secret{set("DB_URL", "v2")}
	b.Services[0].Env = []app.EnvVar{{Name: "MODE", Value: "two"}}
	var second store.DeploymentRecord
	got = notedOne(t, e.store, func() { second = deployHealthy(t, e, b) })
	wantEntry(t, got, audit.ActionDeployed, audit.KindApp, productionID)
	wantRecord(t, got, audit.RecordDeployment, second.Deployment.ID)
	if c := change(got.Changes, "services[web].domains[old.example.com]"); c == nil || c.Op != audit.OpRemoved {
		t.Errorf("the old host is not shown removed: %v", changePaths(got.Changes))
	}
	if c := change(got.Changes, "services[web].domains[new.example.com]"); c == nil || c.Op != audit.OpAdded {
		t.Errorf("the new host is not shown added: %v", changePaths(got.Changes))
	}
	// A secret replaced is "changed" with no value; so is an env value.
	for _, path := range []string{"services[web].secrets[DB_URL].value", "services[web].env[MODE].value"} {
		if c := change(got.Changes, path); c == nil || c.Op != audit.OpChanged || c.From != "" || c.To != "" {
			t.Errorf("%s = %+v, want changed with no from or to", path, c)
		}
	}

	// A deploy that sends no value for the secret (keep it) leaves it out of the changes.
	c := b
	c.Services = append([]app.Service(nil), b.Services...)
	c.Services[0].Secrets = []app.Secret{keep("DB_URL")}
	got = notedOne(t, e.store, func() { deployHealthy(t, e, c) })
	if change(got.Changes, "services[web].secrets[DB_URL].value") != nil {
		t.Errorf("a kept secret shows as changed: %v", changePaths(got.Changes))
	}

	// Redeploy, the webhook's call included: redeployed, no definition changed.
	var re store.DeploymentRecord
	got = notedOne(t, e.store, func() {
		var err error
		if re, err = e.svc.Redeploy(ctx, productionID, app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, re.Deployment.ID)
	})
	wantEntry(t, got, audit.ActionRedeployed, audit.KindApp, productionID)
	wantRecord(t, got, audit.RecordDeployment, re.Deployment.ID)
	if len(got.Changes) != 0 {
		t.Errorf("a redeploy lists changes: %v", changePaths(got.Changes))
	}

	// A rollback to the first version.
	var rb store.DeploymentRecord
	got = notedOne(t, e.store, func() {
		var err error
		if rb, err = e.svc.RedeployVersion(ctx, productionID, first.Deployment.ID, app.TriggerManual); err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, rb.Deployment.ID)
	})
	wantEntry(t, got, audit.ActionRolledBack, audit.KindApp, productionID)
	wantRecord(t, got, audit.RecordDeployment, rb.Deployment.ID)
	if c := change(got.Changes, "services[web].domains[old.example.com]"); c == nil || c.Op != audit.OpAdded {
		t.Errorf("the rollback does not show the old host back: %v", changePaths(got.Changes))
	}

	// Edits through Configure's own methods: updated, with the record of their deployment.
	var sec store.DeploymentRecord
	got = notedOne(t, e.store, func() {
		var err error
		if sec, err = e.svc.SetSecret(ctx, productionID, "web", "DB_URL", "v3", app.TriggerConsole); err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, sec.Deployment.ID)
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindApp, productionID)
	wantRecord(t, got, audit.RecordDeployment, sec.Deployment.ID)
	if c := change(got.Changes, "services[web].secrets[DB_URL].value"); c == nil || c.Op != audit.OpChanged || c.From != "" || c.To != "" {
		t.Errorf("a secret set on an existing name = %+v, want changed with no from or to", c)
	}
	got = notedOne(t, e.store, func() {
		res, err := e.svc.SetDomainsIf(ctx, productionID, "web", []app.Domain{{Host: "third.example.com", Port: "http"}}, Condition{}, app.TriggerConsole)
		if err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, res.Deployment.ID)
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindApp, productionID)
	if c := change(got.Changes, "services[web].domains[third.example.com]"); c == nil || c.Op != audit.OpAdded {
		t.Errorf("the domain edit shows %v", changePaths(got.Changes))
	}

	// Delete.
	got = notedOne(t, e.store, func() {
		if err := e.svc.DeleteApp(ctx, productionID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindApp, productionID)
	wantPlace(t, got, "web", "shop", "production")
}

func TestAuditNotesCreateCopyAndPromote(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)

	// Created without a deploy: a name, then a definition.
	got := notedOne(t, e.store, func() {
		if _, err := e.svc.CreateAppOnly(ctx, app.App{ID: "shop--staging--empty", Name: "empty"}); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindApp, "shop--staging--empty")
	wantPlace(t, got, "empty", "shop", "staging")
	if got.Record != nil {
		t.Errorf("an app created without a deploy links %+v", got.Record)
	}

	// Created with a deploy.
	var rec store.DeploymentRecord
	got = notedOne(t, e.store, func() {
		var err error
		if rec, err = e.svc.CreateApp(ctx, whoamiAs("shop--staging--made"), app.TriggerConsole); err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, rec.Deployment.ID)
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindApp, "shop--staging--made")
	wantRecord(t, got, audit.RecordDeployment, rec.Deployment.ID)

	// Copied into another environment: the new app is the resource.
	if _, err := e.svc.CreateEnvironment(ctx, "shop", "qa"); err != nil {
		t.Fatal(err)
	}
	got = notedOne(t, e.store, func() {
		if _, _, err := e.svc.CopyApp(ctx, "shop--staging--made", "qa"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCopied, audit.KindApp, "shop--qa--made")
	if c := change(got.Changes, "copiedFrom"); c == nil || c.To != "shop--staging--made" {
		t.Errorf("copiedFrom = %+v", c)
	}

	// A promotion into production.
	prod, stage := both(t, e)
	_ = prod
	var promoted store.DeploymentRecord
	got = notedOne(t, e.store, func() {
		var err error
		if promoted, err = e.svc.Promote(ctx, stagingID, "production", Condition{}, app.TriggerConsole); err != nil {
			t.Fatal(err)
		}
		waitFinal(t, e.store, promoted.Deployment.ID)
	})
	wantEntry(t, got, audit.ActionPromoted, audit.KindApp, productionID)
	wantRecord(t, got, audit.RecordDeployment, promoted.Deployment.ID)
	_ = stage
}

func TestAuditNotesTheActions(t *testing.T) {
	ctx := context.Background()
	l := newActionLab(t)
	l.nomad.OnExec(func(deploytest.ExecRequest) deploytest.ExecReply {
		return deploytest.ExecReply{Stdout: "out-" + sentinel, Stderr: "err-" + sentinel}
	})

	actionID := func() string {
		acts, err := l.svc.ListActions(ctx, "whoami", 1)
		if err != nil || len(acts) == 0 {
			t.Fatalf("ListActions = %v, %v", acts, err)
		}
		return acts[0].ID
	}

	// A command: its program only, never an argument or the output.
	got := notedOne(t, l.store, func() {
		if _, err := l.svc.RunCommand(ctx, "whoami", "c1", []string{"echo", argSentinel}, app.TriggerManual); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRanCommand, audit.KindApp, "whoami")
	wantRecord(t, got, audit.RecordAppAction, actionID())
	if c := change(got.Changes, "program"); c == nil || c.To != "echo" {
		t.Errorf("program = %+v", c)
	}

	got = notedOne(t, l.store, func() {
		if _, err := l.svc.StopApp(ctx, "whoami", app.TriggerManual); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionStopped, audit.KindApp, "whoami")
	wantRecord(t, got, audit.RecordAppAction, actionID())

	got = notedOne(t, l.store, func() {
		if _, err := l.svc.StartApp(ctx, "whoami", app.TriggerManual); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionStarted, audit.KindApp, "whoami")
	wantRecord(t, got, audit.RecordAppAction, actionID())
	l.copies()

	var restart store.ActionRecord
	got = notedOne(t, l.store, func() {
		var err error
		if restart, err = l.svc.RestartApp(ctx, "whoami", app.TriggerManual); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRestarted, audit.KindApp, "whoami")
	wantRecord(t, got, audit.RecordAppAction, restart.ID)
	waitFor(t, "the restart to end", func() bool {
		acts, _ := l.svc.ListActions(ctx, "whoami", 5)
		for _, a := range acts {
			if a.ID == restart.ID {
				return a.Status != store.ActionRunning
			}
		}
		return false
	})

	// A terminal is a GET: noted at once, with the record of its session.
	var sess *TerminalSession
	got = notedOne(t, l.store, func() {
		var err error
		if sess, err = l.svc.OpenTerminal(ctx, "whoami", "c1", "sh", TerminalSize{Cols: 80, Rows: 24}, app.TriggerConsole); err != nil {
			t.Fatal(err)
		}
	})
	t.Cleanup(func() { sess.Close(EndClosed) })
	wantEntry(t, got, audit.ActionOpenedTerminal, audit.KindApp, "whoami")
	wantRecord(t, got, audit.RecordAppAction, sess.actionID)

	// What failed before it changed anything notes nothing.
	if n := len(noted(t, l.store, func() {
		var fe *app.FieldError
		if _, err := l.svc.RunCommand(ctx, "whoami", "c1", nil, app.TriggerManual); !errors.As(err, &fe) {
			t.Errorf("RunCommand with no command = %v", err)
		}
		if _, err := l.svc.StartApp(ctx, "whoami", app.TriggerManual); !errors.Is(err, ErrAppNotStopped) {
			t.Errorf("StartApp of a running app = %v", err)
		}
		if _, err := l.svc.StopApp(ctx, "ghost", app.TriggerManual); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("StopApp of an unknown app = %v", err)
		}
	})); n != 0 {
		t.Errorf("refused calls left %d entries", n)
	}
}

func TestAuditNotesNothingForARefusedCall(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	deployHealthy(t, e, whoamiAs(productionID))
	member := WithPrincipal(ctx, Principal{PersonID: "m1", Email: "mia@example.com", OrgRole: OrgMember})

	if n := len(noted(t, e.store, func() {
		// A field error: nothing was saved.
		bad := whoamiAs(productionID)
		bad.Services[0].Image = ""
		var fe *app.FieldError
		if _, err := e.svc.Deploy(ctx, bad, app.TriggerManual); !errors.As(err, &fe) {
			t.Errorf("Deploy of an app with no image = %v, want a field error", err)
		}
		// A role refusal.
		var na *NotAllowedError
		if _, err := e.svc.Redeploy(member, productionID, app.TriggerManual); !errors.As(err, &na) {
			t.Errorf("Redeploy by a member with no role = %v, want a refusal", err)
		}
		if _, err := e.svc.Deploy(member, whoamiAs(productionID), app.TriggerManual); !errors.As(err, &na) {
			t.Errorf("Deploy by a member with no role = %v, want a refusal", err)
		}
		// A secret with no value, a stale revision, an unknown service.
		if _, err := e.svc.SetDomainsIf(ctx, productionID, "api", nil, Condition{}, app.TriggerConsole); err == nil {
			t.Error("SetDomainsIf on an unknown service worked")
		}
		if _, err := e.svc.SetSecretIf(ctx, productionID, "web", "X", "v", Condition{IfRevision: "0000000000000000"}, app.TriggerConsole); !errors.Is(err, ErrAppChanged) {
			t.Errorf("SetSecretIf with a stale revision = %v", err)
		}
		if err := e.svc.DeleteApp(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeleteApp of an unknown app = %v", err)
		}
		if _, err := e.svc.CreateProject(ctx, "Bad Name", ""); err == nil {
			t.Error("CreateProject with a bad name worked")
		}
		if _, err := e.svc.CreateEnvironment(ctx, "ghost", "x"); err == nil {
			t.Error("CreateEnvironment in an unknown project worked")
		}
	})); n != 0 {
		t.Errorf("refused calls left %d entries", n)
	}
}

func TestAuditOneRequestOneEntry(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	deployHealthy(t, e, whoamiAs(productionID))
	user := WithPrincipal(ctx, Principal{PersonID: "p1", Email: "ana@example.com", OrgRole: OrgOwner})

	// One request that deploys with a new domain and a secret: its notes are one entry.
	reqCtx, sc := e.svc.BeginAudit(user, audit.DoorAPI, "203.0.113.4", "POST /v1/apps/{id}/deployments")
	a := whoamiAs(productionID)
	a.Services[0].Domains = []app.Domain{{Host: "one.example.com", Port: "http"}}
	a.Services[0].Secrets = []app.Secret{set("KEY", "typed-"+sentinel)}
	rec, err := e.svc.Deploy(reqCtx, a, app.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	got := notedOne(t, e.store, func() { sc.Finish(200, audit.Resource{}) })
	wantEntry(t, got, audit.ActionDeployed, audit.KindApp, productionID)
	if got.Actor.Email != "ana@example.com" || got.Door != audit.DoorAPI || got.Address != "203.0.113.4" {
		t.Errorf("who = %+v door %s from %s", got.Actor, got.Door, got.Address)
	}
	if strings.Contains(everyEntry(t, e.store), sentinel) {
		t.Error("a secret value is in an entry")
	}

	// A request that changed nothing (a webhook whose images are the live ones) says so: no fallback.
	reqCtx, sc = e.svc.BeginAudit(ctx, audit.DoorWebhook, "203.0.113.5", "POST /v1/webhooks/{id}")
	res, err := e.svc.WebhookRedeploy(reqCtx, productionID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Skip("the webhook found a change; the fake registry moved") // the first one deployed what is live
	}
	if n := len(noted(t, e.store, func() { sc.Finish(200, audit.Resource{ID: productionID, Kind: audit.KindApp}) })); n != 0 {
		t.Errorf("a webhook that changed nothing left %d entries", n)
	}
}

func TestAuditWebhookIsItsAppsActor(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	deployHealthy(t, e, whoamiAs(productionID))
	e.registry.AddIndex("traefik/whoami", "latest", movedDigest) // the tag moved: the webhook redeploys

	reqCtx, sc := e.svc.BeginAudit(ctx, audit.DoorWebhook, "203.0.113.5", "POST /v1/webhooks/{id}")
	res, err := e.svc.WebhookRedeploy(reqCtx, productionID)
	if err != nil || !res.Changed {
		t.Fatalf("WebhookRedeploy = %+v, %v; want a change", res, err)
	}
	waitFinal(t, e.store, res.Deployment.Deployment.ID)
	got := notedOne(t, e.store, func() { sc.Finish(200, audit.Resource{}) })
	wantEntry(t, got, audit.ActionRedeployed, audit.KindApp, productionID)
	wantRecord(t, got, audit.RecordDeployment, res.Deployment.Deployment.ID)
	if got.Actor.Kind != audit.ActorWebhook || got.Actor.Token != productionID || got.Door != audit.DoorWebhook {
		t.Errorf("actor = %+v door %s, want the app's webhook", got.Actor, got.Door)
	}
}

func TestAuditNotesWebhookTokens(t *testing.T) {
	ctx := context.Background()
	e := shopEnv(t)
	deployHealthy(t, e, whoamiAs(productionID))

	got := notedOne(t, e.store, func() {
		if _, err := e.svc.CreateWebhookToken(ctx, productionID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindWebhookToken, productionID)
	wantPlace(t, got, "web", "shop", "production")

	var token WebhookToken
	got = notedOne(t, e.store, func() {
		var err error
		if token, err = e.svc.CreateWebhookToken(ctx, productionID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRotated, audit.KindWebhookToken, productionID)
	if strings.Contains(everyEntry(t, e.store), token.Token) {
		t.Error("the webhook token is in an entry")
	}

	got = notedOne(t, e.store, func() {
		if err := e.svc.DeleteWebhookToken(ctx, productionID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindWebhookToken, productionID)

	// Deleting a token there is none of changes nothing.
	if n := len(noted(t, e.store, func() {
		if err := e.svc.DeleteWebhookToken(ctx, productionID); err != nil {
			t.Fatal(err)
		}
	})); n != 0 {
		t.Errorf("deleting no token left %d entries", n)
	}
}

func TestAuditNotesProjectsAndEnvironments(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	got := notedOne(t, e.store, func() {
		if _, err := e.svc.CreateProject(ctx, "blog", "The blog"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindProject, "blog")
	if c := change(got.Changes, "displayName"); c == nil || c.To != "The blog" {
		t.Errorf("displayName = %+v", c)
	}

	got = notedOne(t, e.store, func() {
		if _, err := e.svc.SetProjectDisplayName(ctx, "blog", "Blog"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindProject, "blog")
	if c := change(got.Changes, "displayName"); c == nil || c.From != "The blog" || c.To != "Blog" {
		t.Errorf("displayName = %+v", c)
	}

	name := "Journal"
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.UpdateProject(ctx, "blog", ProjectChanges{DisplayName: &name}); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindProject, "blog")

	got = notedOne(t, e.store, func() {
		if _, err := e.svc.CreateEnvironment(ctx, "blog", "staging"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindEnvironment, "blog/staging")
	wantPlace(t, got, "staging", "blog", "staging")

	got = notedOne(t, e.store, func() {
		if err := e.svc.DeleteEnvironment(ctx, "blog", "staging"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindEnvironment, "blog/staging")

	got = notedOne(t, e.store, func() {
		if err := e.svc.DeleteProject(ctx, "blog"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindProject, "blog")

	// A protected project is refused and noted nowhere.
	if n := len(noted(t, e.store, func() {
		if err := e.svc.DeleteProject(ctx, app.DefaultProject); !errors.Is(err, ErrProtected) {
			t.Errorf("DeleteProject(default) = %v", err)
		}
	})); n != 0 {
		t.Errorf("a refused delete left %d entries", n)
	}
}

func TestAuditNotesPreviews(t *testing.T) {
	ctx := context.Background()
	e := newPreviewEnv(t)
	clock := e.svc.clock.(*fakeClock)
	e.store.SetClock(clock.Now)
	t.Cleanup(func() { e.store.SetClock(time.Now) })

	limit, days, password := 3, 5, "a long preview password"
	var got audit.Event
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{Limit: &limit, ExpireAfterDays: &days, Password: &password}); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindProject, "blog")
	if c := change(got.Changes, "limit"); c == nil || c.To != "3" {
		t.Errorf("limit = %+v in %v", c, changePaths(got.Changes))
	}
	if c := change(got.Changes, "password"); c == nil || c.Op != audit.OpAdded || c.From != "" || c.To != "" {
		t.Errorf("password = %+v, want set with no value", c)
	}
	if strings.Contains(everyEntry(t, e.store), password) {
		t.Error("the preview password is in an entry")
	}

	// A refused value notes nothing.
	bad := 99
	if n := len(noted(t, e.store, func() {
		if _, err := e.svc.SetPreviewSettings(ctx, "blog", PreviewSettingsInput{Limit: &bad}); err == nil {
			t.Error("a limit of 99 was accepted")
		}
	})); n != 0 {
		t.Errorf("a refused setting left %d entries", n)
	}

	// Closing by the owner: one entry for the environment, listing its apps.
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "pr-1"); err != nil {
		t.Fatal(err)
	}
	e.deployResult(t, previewWeb("pr-1", "pr-1-blog.example.test"))
	got = notedOne(t, e.store, func() {
		if err := e.svc.ClosePreview(ctx, "blog", "pr-1", CloseByOwner); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindEnvironment, "blog/pr-1")
	if len(got.Related) != 1 || got.Related[0].Kind != audit.KindApp || got.Related[0].ID != "blog--pr-1--web" || got.Related[0].Action != audit.ActionDeleted {
		t.Errorf("related = %+v, want the app deleted with it", got.Related)
	}

	// The reaper closing an expired preview is Caravana's own entry.
	if _, err := e.svc.CreateEnvironment(ctx, "blog", "pr-2"); err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.Now().Add(6 * 24 * time.Hour))
	got = notedOne(t, e.store, func() { e.svc.reapPreviews(ctx) })
	wantEntry(t, got, audit.ActionDeleted, audit.KindEnvironment, "blog/pr-2")
	if got.Actor.Kind != audit.ActorCaravana || got.Door != audit.DoorCaravana {
		t.Errorf("the reaper's entry has actor %+v door %s, want Caravana", got.Actor, got.Door)
	}
}

func TestAuditNotesSchedules(t *testing.T) {
	ctx := context.Background()
	l := newSchedLab(t)

	var sc Schedule
	got := notedOne(t, l.store, func() { sc = l.create("nightly", "0 3 * * *", "echo", argSentinel) })
	wantEntry(t, got, audit.ActionCreated, audit.KindSchedule, sc.ID)
	wantPlace(t, got, "nightly", "default", "production")
	if c := change(got.Changes, "program"); c == nil || c.To != "echo" {
		t.Errorf("program = %+v", c)
	}
	if strings.Contains(everyEntry(t, l.store), argSentinel) {
		t.Error("an argument of the command is in an entry")
	}

	got = notedOne(t, l.store, func() {
		if _, err := l.svc.UpdateSchedule(ctx, sc.ID, NewSchedule{App: "whoami", Name: "nightly", Command: []string{"echo", "x"}, Cron: "0 4 * * *"}); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSchedule, sc.ID)
	if c := change(got.Changes, "cron"); c == nil || c.From != "0 3 * * *" || c.To != "0 4 * * *" {
		t.Errorf("cron = %+v", c)
	}

	got = notedOne(t, l.store, func() {
		if _, err := l.svc.PauseSchedule(ctx, sc.ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSchedule, sc.ID)
	if n := len(noted(t, l.store, func() {
		if _, err := l.svc.PauseSchedule(ctx, sc.ID); err != nil { // already paused
			t.Fatal(err)
		}
	})); n != 0 {
		t.Errorf("pausing a paused schedule left %d entries", n)
	}
	got = notedOne(t, l.store, func() {
		if _, err := l.svc.ResumeSchedule(ctx, sc.ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSchedule, sc.ID)

	var run ScheduleRun
	got = notedOne(t, l.store, func() { run = l.runNow(sc.ID) })
	wantEntry(t, got, audit.ActionRanCommand, audit.KindSchedule, sc.ID)
	wantRecord(t, got, audit.RecordScheduleRun, run.ID)

	got = notedOne(t, l.store, func() {
		if _, err := l.svc.StopScheduleRun(ctx, sc.ID, run.ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionStopped, audit.KindSchedule, sc.ID)
	wantRecord(t, got, audit.RecordScheduleRun, run.ID)

	got = notedOne(t, l.store, func() {
		if err := l.svc.DeleteSchedule(ctx, sc.ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindSchedule, sc.ID)

	// The clock's own runs and a refusal leave nothing.
	if n := len(noted(t, l.store, func() {
		if _, err := l.svc.CreateSchedule(ctx, NewSchedule{App: "ghost", Name: "x", Command: []string{"true"}, Cron: "0 3 * * *"}); err == nil {
			t.Error("a schedule in an unknown app was created")
		}
	})); n != 0 {
		t.Errorf("a refused schedule left %d entries", n)
	}
}

func TestAuditNotesBackups(t *testing.T) {
	ctx := context.Background()
	l := newVolumeLab(t)
	b1 := saveRun(t, l.store, "whoami", "web", 1791990000, "", false)
	b2 := saveRun(t, l.store, "whoami", "web", 1791990100, "", false)

	got := notedOne(t, l.store, func() {
		if _, err := l.svc.PinBackup(ctx, "whoami", b1.ID, "golden"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionPinned, audit.KindBackup, b1.ID)
	wantPlace(t, got, "web", "default", "production")
	if c := change(got.Changes, "pin"); c == nil || c.Op != audit.OpAdded || c.To != "golden" {
		t.Errorf("pin = %+v", c)
	}

	// The label moves: the backup that held it is listed as unpinned.
	got = notedOne(t, l.store, func() {
		if _, err := l.svc.PinBackup(ctx, "whoami", b2.ID, "golden"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionPinned, audit.KindBackup, b2.ID)
	if len(got.Related) != 1 || got.Related[0].ID != b1.ID || got.Related[0].Action != audit.ActionUnpinned {
		t.Errorf("related = %+v, want %s unpinned", got.Related, b1.ID)
	}

	got = notedOne(t, l.store, func() {
		if _, err := l.svc.UnpinBackup(ctx, "whoami", b2.ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUnpinned, audit.KindBackup, b2.ID)
	if n := len(noted(t, l.store, func() { // nothing to unpin
		if _, err := l.svc.UnpinBackup(ctx, "whoami", b2.ID); err != nil {
			t.Fatal(err)
		}
	})); n != 0 {
		t.Errorf("unpinning an unpinned backup left %d entries", n)
	}

	// A refusal notes nothing.
	if n := len(noted(t, l.store, func() {
		if _, err := l.svc.PinBackup(ctx, "whoami", b1.ID, "Bad Label"); err == nil {
			t.Error("a bad label was accepted")
		}
	})); n != 0 {
		t.Errorf("a refused pin left %d entries", n)
	}
}

func TestAuditNotesBackUpNowAndRestore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	setDestination(t, e)
	deployHealthy(t, e, db())

	var b store.BackupRecord
	got := notedOne(t, e.store, func() {
		var err error
		if b, err = e.svc.BackUpNow(ctx, "whoami", "web"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionBackedUp, audit.KindBackup, b.ID)
	wantRecord(t, got, audit.RecordBackup, b.ID)

	// Refused while the backup runs: nothing noted.
	if n := len(noted(t, e.store, func() {
		if _, err := e.svc.BackUpNow(ctx, "whoami", "web"); !errors.Is(err, ErrBackupRunning) {
			t.Errorf("second BackUpNow = %v", err)
		}
	})); n != 0 {
		t.Errorf("a refused backup left %d entries", n)
	}

	// A backup that is done can be restored: one entry with the restore's record.
	e.nomad.FinishRun(b.JobID, true, "", "caravana-backup key=backups/caravana/whoami/web/1.dump bytes=1499\n", "")
	waitFor(t, "the backup to be recorded", func() bool {
		got, err := e.store.GetBackup(ctx, "whoami", b.ID)
		return err == nil && got.Status == store.BackupSucceeded
	})
	var r store.RestoreRecord
	got = notedOne(t, e.store, func() {
		var err error
		if r, err = e.svc.Restore(ctx, "whoami", b.ID, ""); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRestored, audit.KindBackup, b.ID)
	wantRecord(t, got, audit.RecordRestore, r.ID)
}

func TestAuditNotesTheTeamPasswordAndCertificateSettings(t *testing.T) {
	ctx := context.Background()
	e := newTeamEnv(t)
	e.deployResult(t, teamApp("shop", app.Domain{Host: "admin.example.com", TeamOnly: true}))

	var password string
	got := notedOne(t, e.store, func() {
		var err error
		if password, err = e.svc.RotateTeamPassword(ctx, "shop", "admin.example.com"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionRotated, audit.KindDomain, "admin.example.com")
	wantPlace(t, got, "admin.example.com", "default", "production")
	if strings.Contains(everyEntry(t, e.store), password) {
		t.Error("the new password is in an entry")
	}
	// A refused rotation notes nothing.
	if n := len(noted(t, e.store, func() {
		if _, err := e.svc.RotateTeamPassword(ctx, "shop", "nothing.example.com"); err == nil {
			t.Error("a rotation of an unknown domain worked")
		}
	})); n != 0 {
		t.Errorf("a refused rotation left %d entries", n)
	}

	got = notedOne(t, e.store, func() {
		if err := e.svc.SetWarnBeforeDays(ctx, 30); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSettings, "certificates")
	if c := change(got.Changes, "warnBeforeDays"); c == nil || c.To != "30" {
		t.Errorf("warnBeforeDays = %+v", c)
	}
	if n := len(noted(t, e.store, func() {
		if err := e.svc.SetWarnBeforeDays(ctx, 5); err == nil {
			t.Error("5 days was accepted")
		}
	})); n != 0 {
		t.Errorf("a refused setting left %d entries", n)
	}
}

func TestAuditNotesGitAccessAndNeverTheSecrets(t *testing.T) {
	ctx := context.Background()
	l := newBuildLab(t, nil)
	if _, err := l.svc.CreateAppOnly(ctx, builtApp()); err != nil {
		t.Fatal(err)
	}

	const token = "ghp_s3cret-sentinel-057-token"
	got := notedOne(t, l.store, func() {
		if _, err := l.svc.SetGitToken(ctx, "shop", "web", "ana", token); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindGitAccess, "shop/web")
	wantPlace(t, got, "web", "default", "production")
	if c := change(got.Changes, "token"); c == nil || c.From != "" || c.To != "" {
		t.Errorf("token = %+v, want set with no value", c)
	}

	// A deploy key replaces the token: updated, and neither half of the key is in the entry.
	got = notedOne(t, l.store, func() {
		if _, err := l.svc.NewDeployKey(ctx, "shop", "web"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindGitAccess, "shop/web")
	g, found, _ := l.svc.deployer.ReadGitAccess(ctx, "shop", "web")
	if !found {
		t.Fatal("no key was stored")
	}
	body := strings.Split(g.PrivateKey, "\n")[1]

	got = notedOne(t, l.store, func() {
		if err := l.svc.RemoveGitAccess(ctx, "shop", "web"); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindGitAccess, "shop/web")

	// Removing what isn't there is refused and noted nowhere.
	if n := len(noted(t, l.store, func() {
		if err := l.svc.RemoveGitAccess(ctx, "shop", "web"); !errors.Is(err, ErrGitAccessNotFound) {
			t.Errorf("RemoveGitAccess of none = %v", err)
		}
	})); n != 0 {
		t.Errorf("a refused removal left %d entries", n)
	}
	all := everyEntry(t, l.store)
	if strings.Contains(all, token) || strings.Contains(all, body) {
		t.Error("a Git token or the private key is in an entry")
	}
	if raw := rawStore(t, l.dir); strings.Contains(raw, token) || strings.Contains(raw, body) {
		t.Error("a Git token or the private key is in the SQLite file")
	}
}

func TestAuditNotesDeployTokens(t *testing.T) {
	ctx := context.Background()
	e := newAuthEnvWithAdmin(t)
	if _, err := e.store.InsertProject(ctx, "shop", "Shop"); err != nil {
		t.Fatal(err)
	}
	var created CreatedAPIToken
	got := notedOne(t, e.store, func() {
		var err error
		if created, err = e.auth.CreateScopedAPIToken(ctx, e.owner, "shop-web-ci", nil, &TokenScope{Project: "shop", App: "web"}); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionCreated, audit.KindToken, created.Info.ID)
	if got.Resource.Name != "shop-web-ci" || got.Resource.Project != "shop" {
		t.Errorf("resource = %+v, want the token's name, placed in shop", got.Resource)
	}
	if c := change(got.Changes, "scope"); c == nil || c.To != "shop/web" {
		t.Errorf("scope = %+v", c)
	}
	if strings.Contains(everyEntry(t, e.store), created.Token) {
		t.Error("the token itself is in an entry")
	}
	// A refused token notes nothing.
	if n := len(noted(t, e.store, func() {
		if _, err := e.auth.CreateScopedAPIToken(ctx, e.owner, "shop-web-ci", nil, &TokenScope{Project: "shop", App: "web"}); !errors.Is(err, ErrTokenNameTaken) {
			t.Errorf("a second token with the name = %v", err)
		}
	})); n != 0 {
		t.Errorf("a refused token left %d entries", n)
	}
}

// The sentinel: a secret value, an env value, a Git token and a deploy key typed into a request never
// reach an audit row, and so never the SQLite file through one.
func TestAuditNeverHoldsASecret(t *testing.T) {
	ctx := context.Background()
	l, _ := newLoggedActionLab(t)
	const (
		secretS = "s3cret-sentinel-057-secret"
		envS    = "s3cret-sentinel-057-env"
	)
	a := whoami()
	a.Services[0].Secrets = []app.Secret{set("API_KEY", secretS)}
	a.Services[0].Env = []app.EnvVar{{Name: "PLAIN", Value: envS}}
	deployHealthy(t, l.env, a)
	if _, err := l.svc.SetSecret(ctx, "whoami", "web", "API_KEY", secretS+"-again", app.TriggerConsole); err != nil {
		t.Fatal(err)
	}
	if _, err := l.svc.SetSecret(ctx, "whoami", "web", "NEW_KEY", secretS+"-new", app.TriggerConsole); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the deploys to end", func() bool {
		deps, _ := l.store.ListDeployments(ctx, "whoami", 0)
		for _, d := range deps {
			if d.Status == store.StatusPending || d.Status == store.StatusRunning {
				return false
			}
		}
		return true
	})

	// No entry holds a value, a plain one included: a change of an env value shows as changed.
	if all := everyEntry(t, l.store); strings.Contains(all, "s3cret-sentinel-057") {
		t.Errorf("a secret or env value is in the audit log: %s", all)
	}
	// A secret value is kept in Nomad's variables only, so nothing of it may be in the SQLite file.
	if raw := rawStore(t, l.dir); strings.Contains(raw, secretS) {
		t.Error("a secret value is in the SQLite file")
	}
	var shown bool
	for _, ev := range auditRows(t, l.store) {
		for _, c := range ev.Changes {
			if strings.HasSuffix(c.Path, "secrets[API_KEY].value") && c.Op == audit.OpChanged {
				shown = true
			}
		}
	}
	if !shown {
		t.Error("the replaced secret does not show as changed in any entry")
	}
}

// A restore on the owner's schedule is Caravana's own entry (plan D14); a backup the clock starts is no
// entry at all.
func TestAuditScheduledRestoreIsCaravanas(t *testing.T) {
	ctx := context.Background()
	day := func(d, h, m, s int) time.Time { return time.Date(2026, 10, d, h, m, s, 0, time.UTC) }
	l := newVolumeLab(t)
	b := l.pinGolden()
	l.setRestoreSchedule("0 4 * * *", "golden")
	l.seedScheduled(day(14, 4, 0, 5))
	l.clock.Set(day(15, 4, 0, 20))
	id := fmt.Sprintf("web-%d", day(15, 4, 0, 20).Unix())
	l.render(id)
	l.answer(id, "status=succeeded\nmessage=\n")

	got := notedOne(t, l.store, func() {
		l.svc.runDueRestores(ctx, map[string]string{})
		l.settledRestore(restoreRecordID("whoami", id))
	})
	wantEntry(t, got, audit.ActionRestored, audit.KindBackup, b.ID)
	wantRecord(t, got, audit.RecordRestore, restoreRecordID("whoami", id))
	if got.Actor.Kind != audit.ActorCaravana || got.Door != audit.DoorCaravana {
		t.Errorf("actor = %+v door %s, want Caravana", got.Actor, got.Door)
	}
	l.waitReleased()

	// A backup the clock starts writes its record and no entry.
	if n := len(noted(t, l.store, func() {
		if _, err := l.svc.backUpNow(ctx, "whoami", "web", store.BackupTriggerTimer); err != nil {
			t.Fatalf("backUpNow: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	})); n != 0 {
		t.Errorf("a backup by the clock left %d entries", n)
	}
}
