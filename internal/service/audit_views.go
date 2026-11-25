package service

import (
	"context"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// What an audit entry says about a resource and its changes is built here (feature 057, plan D6 and
// D8): the views that are diffed hold no secret, and the resources are named the same way everywhere.

// storedSecretMarker stands for the value of a secret that has one, in the views a diff compares: the
// stored app has no values, so a request that sends one must show as "changed", and a request that
// sends none (keep the value) as no change at all. A NUL keeps it from ever equalling typed text.
const storedSecretMarker = "\x00stored"

// appAuditView is the app as a diff reads it: every secret that holds a typed value, here or in the
// store, has the marker for a value, so Diff (which hides any value) tells a replaced secret from a
// kept one without either side ever showing a real one. a is not changed.
func appAuditView(a app.App) app.App {
	a.Services = slices.Clone(a.Services)
	for i := range a.Services {
		a.Services[i].Secrets = slices.Clone(a.Services[i].Secrets)
		for j, sec := range a.Services[i].Secrets {
			if sec.From != nil || sec.Template != "" {
				continue // a reference is compared as it is: provider and path, never a value
			}
			marker := storedSecretMarker
			if sec.Value != nil && *sec.Value != "" {
				// A typed value is told apart from the marker, whatever it says.
				marker = storedSecretMarker + "+"
			}
			a.Services[i].Secrets[j].Value = &marker
		}
	}
	return a
}

// storedAppAuditView is the view of an app read from the store: it holds no value, so each secret has
// the marker.
func storedAppAuditView(a app.App) app.App {
	a.Services = slices.Clone(a.Services)
	for i := range a.Services {
		a.Services[i].Secrets = slices.Clone(a.Services[i].Secrets)
		for j, sec := range a.Services[i].Secrets {
			if sec.From != nil || sec.Template != "" {
				continue
			}
			marker := storedSecretMarker
			a.Services[i].Secrets[j].Value = &marker
		}
	}
	return a
}

// appChanges is what changed between the stored app before (nil for a new app) and after, the app as
// deployed. Secret values never show: Diff reads only the views.
func appChanges(before *app.App, after app.App) []audit.Change {
	var b any
	if before != nil {
		b = storedAppAuditView(*before)
	}
	return audit.Diff(b, appAuditView(after))
}

// appResource names an app in an entry: its ID, its name in its place, and the place.
func appResource(id string) audit.Resource {
	p, err := app.PlaceOf(id)
	if err != nil {
		// An ID from before projects: Default › production, as the store has it.
		return audit.Resource{Kind: audit.KindApp, ID: id, Name: id, Project: app.DefaultProject, Environment: app.DefaultEnvironment}
	}
	return audit.Resource{Kind: audit.KindApp, ID: id, Name: p.Name, Project: p.Project, Environment: p.Environment}
}

// appScoped is the resource of a thing that belongs to an app (its webhook token, its Git access, a
// schedule): the kind, the app's ID as the ID, and the app's place.
func appScoped(kind audit.Kind, appID string) audit.Resource {
	r := appResource(appID)
	r.Kind = kind
	return r
}

// projectResource names a project.
func projectResource(id, name string) audit.Resource {
	if name == "" {
		name = id
	}
	return audit.Resource{Kind: audit.KindProject, ID: id, Name: name, Project: id}
}

// environmentResource names an environment of a project, with the project and environment as its place.
func environmentResource(project, environment string) audit.Resource {
	return audit.Resource{Kind: audit.KindEnvironment, ID: project + "/" + environment, Name: environment, Project: project, Environment: environment}
}

// deploymentRecord links an entry to a deployment.
func deploymentRecord(id string) *audit.Record {
	if id == "" {
		return nil
	}
	return &audit.Record{Kind: audit.RecordDeployment, ID: id}
}

// actionRecord links an entry to an app action (stop, start, restart, run, terminal).
func actionRecord(id string) *audit.Record {
	if id == "" {
		return nil
	}
	return &audit.Record{Kind: audit.RecordAppAction, ID: id}
}

// backupResource names a backup: its ID, the service it backs up as the name, and the app's place.
func backupResource(appID, backupID, service string) audit.Resource {
	r := appResource(appID)
	r.Kind, r.ID, r.Name = audit.KindBackup, backupID, service
	return r
}

// scheduleResource names a schedule: its ID, its name, and its app's place.
func scheduleResource(rec store.ScheduleRecord) audit.Resource {
	r := appResource(rec.AppID)
	r.Kind, r.ID, r.Name = audit.KindSchedule, rec.ID, rec.Name
	return r
}

// scheduleAuditView is what a diff of a schedule reads. The command shows as its program and the number of
// arguments: an argument may be a secret someone typed, and the log keeps none.
func scheduleAuditView(rec store.ScheduleRecord) map[string]any {
	m := map[string]any{
		"name": rec.Name, "service": rec.Service, "cron": rec.Cron,
		"stopAfterSeconds": int(rec.StopAfter.Seconds()), "memoryMB": rec.MemoryMB, "skipIfRunning": rec.SkipIfRunning,
		"target": targetOrFreshWord(rec.Target), "allCopies": rec.AllCopies,
		"arguments": max(len(rec.Command)-1, 0),
	}
	if len(rec.Command) > 0 {
		m["program"] = programOf(rec.Command)
	}
	if rec.PausedAt != nil {
		m["paused"] = true
	}
	return m
}

func targetOrFreshWord(t string) string {
	if t == "" {
		return store.TargetFresh
	}
	return t
}

// deployNoteOverride says what the deploys under a context are noted as, when they are not a plain deploy:
// a template install is "installed", with the template as a change.
type deployNoteOverride struct {
	action  audit.Action
	changes []audit.Change
}

type deployNoteKey struct{}

func withDeployNote(ctx context.Context, action audit.Action, changes ...audit.Change) context.Context {
	return context.WithValue(ctx, deployNoteKey{}, deployNoteOverride{action: action, changes: changes})
}

// noteApp notes a change of an app that is not a deploy: who did what, to which app, with the record
// the change made (nil for none).
func (s *Service) noteApp(ctx context.Context, action audit.Action, appID string, rec *audit.Record, changes ...audit.Change) {
	s.audit(ctx, audit.Entry{Action: action, Resource: appResource(appID), Record: rec, Changes: changes})
}

// noteDeploy notes a deploy of a once its deployment record exists (or, with an empty deploymentID,
// when the record could not be made after the app was stored). m says what the deploy was: its action
// (deployed when none is set) and the stored app it replaced.
func (s *Service) noteDeploy(ctx context.Context, a app.App, m made, deploymentID string) {
	action := m.action
	if action == "" {
		action = audit.ActionDeployed
	}
	var changes []audit.Change
	if m.diff {
		changes = appChanges(m.before, a)
	}
	if o, ok := ctx.Value(deployNoteKey{}).(deployNoteOverride); ok {
		action, changes = o.action, append(changes, o.changes...)
	}
	s.audit(ctx, audit.Entry{Action: action, Resource: appResource(a.ID), Record: deploymentRecord(deploymentID), Changes: changes})
}
