package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// What the project and environment calls refuse with. They are sentinels, as
// ErrAppExists is: callers match with errors.Is, and the wrapped text names
// the project or environment.
var (
	ErrProjectNotFound     = errors.New("project not found")
	ErrEnvironmentNotFound = errors.New("environment not found")
	ErrProjectExists       = errors.New("a project with this name already exists")
	ErrEnvironmentExists   = errors.New("this project already has an environment with this name")
	// ErrProtected is the refusal of deleting what existing apps depend on:
	// project default and its production environment (their IDs are bare
	// names, so apps from before projects live there).
	ErrProtected = errors.New("is protected: apps that existed before projects live there, and their IDs depend on it")
)

// ErrNotEmpty is the store's: a project or environment can't be deleted while
// it has apps. The error behind it is a *NotEmptyError that names them
// (errors.As).
var ErrNotEmpty = store.ErrNotEmpty

// NotEmptyError is the store's error, re-exported so the API and the console
// never import the store for it.
type NotEmptyError = store.NotEmptyError

// PlaceError is ErrProjectNotFound or ErrEnvironmentNotFound with the request
// field that named the missing place: "id" when an app ID did ("shop--qa--web"
// in a deploy), "to" for a promotion's target, and so on. The API answers with
// that field. Err is the sentinel, wrapped with the name.
type PlaceError struct {
	Field string
	Err   error
}

func (e *PlaceError) Error() string { return e.Err.Error() }
func (e *PlaceError) Unwrap() error { return e.Err }

// Environment is one stage of a project, with how many apps are in it.
type Environment struct {
	Project  string
	Name     string
	Position int
	Apps     int
	// Preview is set for a preview environment (pr-<n>, feature 049).
	Preview *PreviewInfo
}

// Project is a group of environments, which hold apps. ID is its name: the
// first part of its apps' IDs.
type Project struct {
	ID           string
	DisplayName  string
	Environments []Environment // in the order shown
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// BackupDestination is the name of the backup destination the project's
	// backups go to when a policy names none (feature 052); "" means the
	// installation's default.
	BackupDestination string
}

// maxDisplayName is the longest project display name, in characters.
const maxDisplayName = 100

func toEnvironment(r store.EnvironmentRecord) Environment {
	return Environment{Project: r.Project, Name: r.Name, Position: r.Position, Apps: r.Apps}
}

// projectOf joins a stored project with its environments.
func (s *Service) projectOf(ctx context.Context, rec store.ProjectRecord) (Project, error) {
	envs, err := s.store.ListEnvironments(ctx, rec.ID)
	if err != nil {
		return Project{}, fmt.Errorf("project %q: %w", rec.ID, err)
	}
	p := Project{ID: rec.ID, DisplayName: rec.DisplayName, CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
		BackupDestination: rec.BackupDestination, Environments: make([]Environment, 0, len(envs))}
	for _, e := range envs {
		p.Environments = append(p.Environments, toEnvironment(e))
	}
	if err := s.withPreviews(ctx, rec, &p); err != nil {
		return Project{}, err
	}
	return p, nil
}

// Projects lists every project with its environments: Default first, then in
// the order they were made.
func (s *Service) Projects(ctx context.Context) ([]Project, error) {
	recs, err := s.store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(recs))
	for _, rec := range recs {
		p, err := s.projectOf(ctx, rec)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Project returns one project, or ErrProjectNotFound.
func (s *Service) Project(ctx context.Context, id string) (Project, error) {
	rec, err := s.store.GetProject(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Project{}, fmt.Errorf("%w: %q", ErrProjectNotFound, id)
	}
	if err != nil {
		return Project{}, err
	}
	return s.projectOf(ctx, rec)
}

// validDisplayName checks a project's display name: 1 to 100 printable
// characters (no control characters), not only spaces.
func validDisplayName(s string) error {
	switch {
	case strings.TrimSpace(s) == "":
		return app.NewFieldError("displayName", "must not be empty")
	case !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl):
		return app.NewFieldError("displayName", "must be printable text, without control characters")
	case utf8.RuneCountInString(s) > maxDisplayName:
		return app.NewFieldError("displayName", "is %d characters; at most %d", utf8.RuneCountInString(s), maxDisplayName)
	}
	return nil
}

// CreateProject makes a project with its production environment. name is a
// lowercase label without "--" (a *app.FieldError on "name" otherwise);
// displayName defaults to the name. A taken name is ErrProjectExists.
func (s *Service) CreateProject(ctx context.Context, name, displayName string) (Project, error) {
	if err := app.ValidName("name", name); err != nil {
		return Project{}, err
	}
	if displayName == "" {
		displayName = name
	}
	if err := validDisplayName(displayName); err != nil {
		return Project{}, err
	}
	rec, err := s.store.InsertProject(ctx, name, displayName)
	if errors.Is(err, store.ErrAlreadyExists) {
		return Project{}, fmt.Errorf("%w: %q", ErrProjectExists, name)
	}
	if err != nil {
		return Project{}, err
	}
	s.publish(Change{What: ChangeApps})
	return s.projectOf(ctx, rec)
}

// SetProjectDisplayName changes what the console calls a project. The name
// itself never changes: it is part of each app's ID. Default may be renamed.
func (s *Service) SetProjectDisplayName(ctx context.Context, id, displayName string) (Project, error) {
	if err := validDisplayName(displayName); err != nil {
		return Project{}, err
	}
	rec, err := s.store.SetProjectDisplayName(ctx, id, displayName)
	if errors.Is(err, store.ErrNotFound) {
		return Project{}, fmt.Errorf("%w: %q", ErrProjectNotFound, id)
	}
	if err != nil {
		return Project{}, err
	}
	s.publish(Change{What: ChangeApps})
	return s.projectOf(ctx, rec)
}

// SetProjectBackupDestination sets the backup destination the project's backups
// go to when a policy names none: name, or "" for the installation's default
// (feature 052). An unknown project is ErrProjectNotFound. A name that isn't a
// destination, or that would leave a policy of the project copying to where its
// backups already go, is a *app.FieldError on "backupDestination". It points the
// project's backup jobs at the new destination from their next run (no deploy).
func (s *Service) SetProjectBackupDestination(ctx context.Context, id, name string) (Project, error) {
	if _, err := s.store.GetProject(ctx, id); errors.Is(err, store.ErrNotFound) {
		return Project{}, fmt.Errorf("%w: %q", ErrProjectNotFound, id)
	} else if err != nil {
		return Project{}, err
	}
	if name != "" {
		if !app.ValidLabel(name) {
			return Project{}, app.NewFieldError("backupDestination", "%q must be a destination's name: 1 to 32 lowercase letters, digits or hyphens", name)
		}
		if _, err := s.store.GetBackupDestinationNamed(ctx, name); errors.Is(err, store.ErrNotFound) {
			return Project{}, app.NewFieldError("backupDestination", "no backup destination is named %q; add it in Settings › Backup storage", name)
		} else if err != nil {
			return Project{}, err
		}
	}
	apps, projects, installDefault, err := s.backupWorld(ctx)
	if err != nil {
		return Project{}, err
	}
	projects[id] = name
	if a, svc, dest, found := copyConflict(apps, projects, installDefault); found {
		return Project{}, app.NewFieldError("backupDestination", "%s/%s copies its backups to %q, where they would already go; change that policy first", a, svc, dest)
	}
	if err := s.store.SetProjectBackupDestination(ctx, id, name); err != nil {
		return Project{}, err
	}
	rec, err := s.store.GetProject(ctx, id)
	if err != nil {
		return Project{}, err
	}
	p, err := s.projectOf(ctx, rec)
	if err != nil {
		return Project{}, err
	}
	s.publish(Change{What: ChangeApps})
	s.publish(Change{What: ChangeAttention})
	if err := s.repointBackups(ctx); err != nil {
		return p, fmt.Errorf("saved, but the backup jobs still point at the old destination: %w", err)
	}
	return p, nil
}

// DeleteProject deletes a project and its environments. Default is
// ErrProtected, an unknown project ErrProjectNotFound, and a project that
// still has apps ErrNotEmpty, naming them.
func (s *Service) DeleteProject(ctx context.Context, id string) error {
	if id == app.DefaultProject {
		return fmt.Errorf("project %q %w", id, ErrProtected)
	}
	err := s.store.DeleteProject(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %q", ErrProjectNotFound, id)
	}
	if err != nil {
		return err
	}
	s.publish(Change{What: ChangeApps})
	return nil
}

// CreateEnvironment adds an empty environment after the project's others.
// name is a label without "--" that doesn't start with the reserved "pr-"
// unless it is a preview name, pr-<n> (a *app.FieldError on "name"); an unknown
// project is ErrProjectNotFound and a taken name ErrEnvironmentExists. A
// preview is counted against the project's limit, with any token, in the same
// transaction that makes it (a *PreviewLimitError, ErrPreviewLimit).
func (s *Service) CreateEnvironment(ctx context.Context, project, name string) (Environment, error) {
	if err := app.ValidEnvironmentName("name", name); err != nil {
		return Environment{}, err
	}
	insert := s.store.InsertEnvironment
	if app.IsPreviewEnvironment(name) {
		insert = s.store.InsertPreviewEnvironment
	}
	rec, err := insert(ctx, project, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return Environment{}, fmt.Errorf("%w: %q", ErrProjectNotFound, project)
	case errors.Is(err, store.ErrAlreadyExists):
		return Environment{}, fmt.Errorf("%w: %q in project %q", ErrEnvironmentExists, name, project)
	case err != nil:
		return Environment{}, err
	}
	s.publish(Change{What: ChangeApps})
	env := toEnvironment(rec)
	if n, ok := app.PreviewNumber(name); ok {
		// Made just now, so it has no deployment: its clock is its creation.
		if proj, err := s.store.GetProject(ctx, project); err == nil {
			info := s.previewInfo(proj, store.PreviewActivity{Environment: name, Number: n, CreatedAt: rec.CreatedAt})
			env.Preview = &info
		}
	}
	return env, nil
}

// DeleteEnvironment deletes an environment. Default's production is
// ErrProtected; an unknown project or environment is ErrProjectNotFound or
// ErrEnvironmentNotFound; one that still has apps is ErrNotEmpty, naming them.
func (s *Service) DeleteEnvironment(ctx context.Context, project, name string) error {
	if project == app.DefaultProject && name == app.DefaultEnvironment {
		return fmt.Errorf("environment %q of project %q %w", name, project, ErrProtected)
	}
	if err := s.requireProject(ctx, project); err != nil {
		return err
	}
	err := s.store.DeleteEnvironment(ctx, project, name)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %q in project %q", ErrEnvironmentNotFound, name, project)
	}
	if err != nil {
		return err
	}
	s.publish(Change{What: ChangeApps})
	return nil
}

// requireProject returns ErrProjectNotFound unless the project exists.
func (s *Service) requireProject(ctx context.Context, project string) error {
	if _, err := s.store.GetProject(ctx, project); errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %q", ErrProjectNotFound, project)
	} else if err != nil {
		return err
	}
	return nil
}

// requireEnvironment returns ErrProjectNotFound or ErrEnvironmentNotFound
// unless the project has the environment.
func (s *Service) requireEnvironment(ctx context.Context, project, environment string) error {
	if err := s.requireProject(ctx, project); err != nil {
		return err
	}
	if ok, err := s.store.EnvironmentExists(ctx, project, environment); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %q in project %q", ErrEnvironmentNotFound, environment, project)
	}
	return nil
}

// ResolvePlace checks that the project and its environment exist and that
// name is a name (*app.FieldError on "name"), and returns the place whose ID
// the app has. The routes that name an app by project, environment and name
// (/v1/projects/{project}/environments/{environment}/apps/{name}) use it.
func (s *Service) ResolvePlace(ctx context.Context, project, environment, name string) (app.Place, error) {
	if err := s.requireEnvironment(ctx, project, environment); err != nil {
		return app.Place{}, err
	}
	if err := app.ValidName("name", name); err != nil {
		return app.Place{}, err
	}
	return app.Place{Project: project, Environment: environment, Name: name}, nil
}

// checkPlace is run before an app that doesn't exist yet is stored, on every
// path that can create one (CreateApp, the first Deploy or DeployCompose of an
// ID). It refuses, naming the field "id":
//   - an ID app.PlaceOf can't read (a "--" that isn't <project>--<environment>--<name>),
//     as a *app.FieldError;
//   - a project or environment that doesn't exist, as a *PlaceError, so a typo
//     in an ID never creates an app nobody can find under a project.
//
// An app that exists is not checked: one from before projects, whose ID has a
// "--" PlaceOf refuses, keeps deploying. The store, not this check, decides
// who owns an ID, so a create that loses a race is still ErrAppExists. (An
// environment deleted between this check and the insert leaves its new app in
// a place nobody can list; the window is a few milliseconds and the app can be
// deleted by its ID.)
func (s *Service) checkPlace(ctx context.Context, id string) error {
	if _, err := s.store.GetApp(ctx, id); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	place, err := app.PlaceOf(id)
	if err != nil {
		return err
	}
	if err := s.requireEnvironment(ctx, place.Project, place.Environment); err != nil {
		return &PlaceError{Field: "id", Err: err}
	}
	return nil
}
