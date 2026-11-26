package service

import (
	"context"
	"maps"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// SetBackupPolicy deploys appID with policy as the backup of service: a Deploy of
// the stored app with that one change, on the condition that the app still has
// the revision the caller read (ErrAppChanged otherwise; an empty Condition
// saves whatever is there). The API reaches the same result by deploying the
// app with the service's backup set. An unknown app is store.ErrNotFound; an
// unknown service, or one that already has a policy, is an *app.FieldError on
// "service".
//
// An app that keeps its Compose file keeps it: the overlay's backup follows
// the app, as the overlay's secrets do (editSecrets), and the file's text stays
// as it was.
func (s *Service) SetBackupPolicy(ctx context.Context, appID, service string, policy app.Backup, cond Condition, trigger app.Trigger) (store.DeploymentRecord, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	a := rec.App
	i := slices.IndexFunc(a.Services, func(svc app.Service) bool { return svc.Name == service })
	if i < 0 {
		return store.DeploymentRecord{}, app.NewFieldError("service", "app %q has no service %q", appID, service)
	}
	if a.Services[i].Backup != nil {
		return store.DeploymentRecord{}, app.NewFieldError("service", "service %q already has a backup policy", service)
	}
	services := slices.Clone(a.Services)
	services[i].Backup = &policy
	a.Services = services

	m := made{action: audit.ActionUpdated}
	if rec.Compose != nil {
		file := withBackup(*rec.Compose, service, policy)
		m.file = &file
	}
	return recordOnly(s.saveAndDeploy(ctx, a, cond, trigger, m, nil))
}

// ChangeBackupPolicy deploys appID with the engine, database file, snapshot
// folder (feature 061), schedule and number to keep of service's backup policy replaced (feature 059): a Deploy of
// the stored app with that one change, on the condition that the app still has
// the revision the caller read (ErrAppChanged otherwise; an empty Condition
// saves whatever is there), as SetBackupPolicy does for a service with none. The
// rest of the policy stays as it was: its restore schedule, destination, copies
// and how many each copy keeps. An empty schedule is the default, daily at
// 03:00 UTC, and "never" takes no automatic backups; keep 0 is the default
// count. An unknown service, or one with no policy, is an *app.FieldError on
// "service"; what the model refuses is its *app.FieldError on
// services[i].backup.*, as a deploy says it. An app that keeps its Compose file
// keeps it, with the overlay's backup following the app.
func (s *Service) ChangeBackupPolicy(ctx context.Context, appID, service, engine, file, repository, schedule string, keep int, cond Condition, trigger app.Trigger) (store.DeploymentRecord, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	a := rec.App
	i := slices.IndexFunc(a.Services, func(svc app.Service) bool { return svc.Name == service })
	if i < 0 {
		return store.DeploymentRecord{}, app.NewFieldError("service", "app %q has no service %q", appID, service)
	}
	if a.Services[i].Backup == nil {
		return store.DeploymentRecord{}, app.NewFieldError("service", "service %q has no backup policy", service)
	}
	policy := *a.Services[i].Backup
	policy.Engine, policy.File, policy.Repository, policy.Schedule, policy.Keep = engine, file, repository, schedule, keep
	services := slices.Clone(a.Services)
	services[i].Backup = &policy
	a.Services = services

	m := made{action: audit.ActionUpdated}
	if rec.Compose != nil {
		file := withBackup(*rec.Compose, service, policy)
		m.file = &file
	}
	return recordOnly(s.saveAndDeploy(ctx, a, cond, trigger, m, nil))
}

// SetRestoreSchedule sets, or with restore nil removes, the restore schedule of
// a service that already has a backup policy, and deploys, as SetBackupPolicy
// does for the policy itself (feature 036): the same condition, trigger and
// kept Compose file. The rest of the policy is as it was. A service with no
// policy, or an unknown one, is an *app.FieldError on "service"; a schedule or
// label the model refuses is its *app.FieldError, as a deploy says it.
func (s *Service) SetRestoreSchedule(ctx context.Context, appID, service string, restore *app.RestoreSchedule, cond Condition, trigger app.Trigger) (store.DeploymentRecord, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	a := rec.App
	i := slices.IndexFunc(a.Services, func(svc app.Service) bool { return svc.Name == service })
	if i < 0 {
		return store.DeploymentRecord{}, app.NewFieldError("service", "app %q has no service %q", appID, service)
	}
	if a.Services[i].Backup == nil {
		return store.DeploymentRecord{}, app.NewFieldError("service", "service %q has no backup policy", service)
	}
	policy := *a.Services[i].Backup
	policy.Restore = restore
	services := slices.Clone(a.Services)
	services[i].Backup = &policy
	a.Services = services

	m := made{action: audit.ActionUpdated}
	if rec.Compose != nil {
		file := withBackup(*rec.Compose, service, policy)
		m.file = &file
	}
	return recordOnly(s.saveAndDeploy(ctx, a, cond, trigger, m, nil))
}

// SetBackupPlaces sets where a service's backups go and are copied (feature 052)
// and deploys, as SetRestoreSchedule does for the restore schedule: the same
// condition, trigger and kept Compose file, and the rest of the policy as it was.
// destination "" means the project's, else the installation's default; copyTo
// and copyKeep are the policy's fields of those names. A service with no policy,
// or an unknown one, is an *app.FieldError on "service"; a name the model refuses
// is its *app.FieldError, and one that names no destination is the deploy's
// *app.FieldError on services[i].backup.destination or .copyTo[k] (checkBackups).
func (s *Service) SetBackupPlaces(ctx context.Context, appID, service, destination string, copyTo []string, copyKeep int, cond Condition, trigger app.Trigger) (store.DeploymentRecord, error) {
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	a := rec.App
	i := slices.IndexFunc(a.Services, func(svc app.Service) bool { return svc.Name == service })
	if i < 0 {
		return store.DeploymentRecord{}, app.NewFieldError("service", "app %q has no service %q", appID, service)
	}
	if a.Services[i].Backup == nil {
		return store.DeploymentRecord{}, app.NewFieldError("service", "service %q has no backup policy", service)
	}
	policy := *a.Services[i].Backup
	policy.Destination = destination
	policy.CopyTo = slices.Clone(copyTo)
	policy.CopyKeep = copyKeep
	services := slices.Clone(a.Services)
	services[i].Backup = &policy
	a.Services = services

	m := made{action: audit.ActionUpdated}
	if rec.Compose != nil {
		file := withBackup(*rec.Compose, service, policy)
		m.file = &file
	}
	return recordOnly(s.saveAndDeploy(ctx, a, cond, trigger, m, nil))
}

// withBackup returns a copy of file whose overlay for service sets policy as
// the backup. The rest of the overlay stays. file is not changed.
func withBackup(file compose.Request, service string, policy app.Backup) compose.Request {
	file = file.WithoutSecretValues() // its own Services map and Secrets slices
	services := make(map[string]compose.Overlay, len(file.Services)+1)
	maps.Copy(services, file.Services)
	overlay := services[service]
	overlay.Backup = &policy
	services[service] = overlay
	file.Services = services
	return file
}
