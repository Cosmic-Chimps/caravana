package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Previews (feature 049). A preview is an environment named pr-<n> and the apps
// in it; CI opens and closes one with a deploy token, Caravana closes the ones
// nobody did, and an optional project password keeps them private. Nothing here
// is a new kind of object: a preview's apps are ordinary apps (spec 049, plan
// D1), so its deploys, logs and backups are the usual ones.

var (
	// ErrNotAPreview is ClosePreview for an environment that isn't named pr-<n>.
	ErrNotAPreview = errors.New("is not a preview environment")
	// ErrPreviewsOpen is a refusal to remove the preview password while the
	// project has open previews (a *PreviewsOpenError, which names them).
	ErrPreviewsOpen = errors.New("the project has open previews")
	// ErrPreviewPassword is RotateTeamPassword for a domain that uses its
	// project's preview password (a *PreviewPasswordError).
	ErrPreviewPassword = errors.New("the domain uses the project's preview password")
	// ErrPreviewLimit is a preview beyond the project's limit; the error behind
	// it is a *PreviewLimitError.
	ErrPreviewLimit = store.ErrPreviewLimit
)

// PreviewLimitError is the store's error, re-exported so the API and the
// console never import the store for it.
type PreviewLimitError = store.PreviewLimitError

// PreviewsOpenError names the open previews that stop the password from being
// removed.
type PreviewsOpenError struct {
	Project string
	Open    []string
}

func (e *PreviewsOpenError) Error() string {
	return fmt.Sprintf("project %s has open previews (%s): their domains use the preview password; close them first",
		e.Project, strings.Join(e.Open, ", "))
}

// Unwrap makes errors.Is(err, ErrPreviewsOpen) true.
func (e *PreviewsOpenError) Unwrap() error { return ErrPreviewsOpen }

// PreviewPasswordError is the refusal to give a preview domain a password of
// its own.
type PreviewPasswordError struct {
	Project string
}

func (e *PreviewPasswordError) Error() string {
	return fmt.Sprintf("this domain uses project %s's preview password; change it in the project's preview settings", e.Project)
}

// Unwrap makes errors.Is(err, ErrPreviewPassword) true.
func (e *PreviewPasswordError) Unwrap() error { return ErrPreviewPassword }

// The limits of the settings.
const (
	maxPreviewLimit      = 20
	minPreviewExpireDays = 1
	maxPreviewExpireDays = 30
	minPreviewPassword   = 12
	maxPreviewPassword   = 128
	// maxPreviewPasswordBytes is bcrypt's own limit (it refuses a longer one): the hash Traefik checks
	// is bcrypt, so a password the owner types at a preview can't be longer.
	maxPreviewPasswordBytes = 72
	defaultPreviewReapGap   = 5 * time.Minute
)

// SetPreviewReapGap sets how often the reaper looks for expired previews (5
// minutes by default); the lab shortens it. Call it before Start; a value that
// isn't positive keeps the default.
func (s *Service) SetPreviewReapGap(d time.Duration) {
	if d > 0 {
		s.previewGap = d
	}
}

// SetPreviewExpiryForTests makes d the time every preview may sit idle, in
// place of each project's days, so the lab can see the reaper close one in
// seconds. Zero gives the projects' own back. Safe to call at any time.
func (s *Service) SetPreviewExpiryForTests(d time.Duration) {
	s.previewExpiry.Store(int64(max(d, 0)))
}

// expireAfter is how long a preview of the project may sit idle.
func (s *Service) expireAfter(rec store.ProjectRecord) time.Duration {
	if d := time.Duration(s.previewExpiry.Load()); d > 0 {
		return d
	}
	return time.Duration(rec.PreviewExpireDays) * 24 * time.Hour
}

// PreviewInfo is what a preview environment adds to an Environment.
type PreviewInfo struct {
	Number    int
	CreatedAt time.Time
	// ExpiresAt is the later of the preview's creation and its newest
	// deployment, plus the project's days (plan D2). It is computed on every
	// read, so changing the days applies to the previews open.
	ExpiresAt time.Time
}

// previewInfo is the info of one preview from its activity.
func (s *Service) previewInfo(rec store.ProjectRecord, a store.PreviewActivity) PreviewInfo {
	return PreviewInfo{Number: a.Number, CreatedAt: a.CreatedAt, ExpiresAt: a.Idle().Add(s.expireAfter(rec))}
}

// withPreviews fills Environment.Preview of the preview environments of p.
func (s *Service) withPreviews(ctx context.Context, rec store.ProjectRecord, p *Project) error {
	if !slices.ContainsFunc(p.Environments, func(e Environment) bool { return app.IsPreviewEnvironment(e.Name) }) {
		return nil
	}
	acts, err := s.store.PreviewActivity(ctx, rec.ID)
	if err != nil {
		return fmt.Errorf("project %q: %w", rec.ID, err)
	}
	for i := range p.Environments {
		for _, a := range acts {
			if a.Environment == p.Environments[i].Name {
				info := s.previewInfo(rec, a)
				p.Environments[i].Preview = &info
			}
		}
	}
	return nil
}

// PreviewSettings are a project's preview settings as every answer shows them.
// The password is never here: only whether there is one.
type PreviewSettings struct {
	Limit           int
	ExpireAfterDays int
	PasswordSet     bool
	Open            int // preview environments open now
}

// PreviewApp is one app of a preview.
type PreviewApp struct {
	ID, Name string
	State    AppState
	// Addresses are the hosts of the app's domains.
	Addresses []string
	// MemoryMB is what the app reserves: each service's reservation, with the
	// default applied, times the copies it wants.
	MemoryMB       int
	LastDeployment *store.DeploymentRecord // nil when the app was never deployed
}

// Preview is one open preview with its apps.
type Preview struct {
	Environment string
	Number      int
	CreatedAt   time.Time
	ExpiresAt   time.Time
	Apps        []PreviewApp // by ID
}

// PreviewList is the project's settings and its open previews, by pull request
// number.
type PreviewList struct {
	Project  string
	Settings PreviewSettings
	Previews []Preview
}

func previewSettingsOf(rec store.ProjectRecord, open int) PreviewSettings {
	return PreviewSettings{Limit: rec.PreviewLimit, ExpireAfterDays: rec.PreviewExpireDays,
		PasswordSet: rec.PreviewPasswordHash != "", Open: open}
}

// Previews lists a project's settings and its open previews. An unknown project
// is ErrProjectNotFound.
func (s *Service) Previews(ctx context.Context, project string) (PreviewList, error) {
	rec, err := s.store.GetProject(ctx, project)
	if errors.Is(err, store.ErrNotFound) {
		return PreviewList{}, fmt.Errorf("%w: %q", ErrProjectNotFound, project)
	}
	if err != nil {
		return PreviewList{}, err
	}
	acts, err := s.store.PreviewActivity(ctx, project)
	if err != nil {
		return PreviewList{}, err
	}
	out := PreviewList{Project: project, Settings: previewSettingsOf(rec, len(acts)), Previews: []Preview{}}
	if len(acts) == 0 {
		return out, nil
	}
	sums, err := s.ListAppsIn(ctx, project, "")
	if err != nil {
		return PreviewList{}, err
	}
	byEnv := map[string][]AppSummary{}
	for _, sum := range sums {
		byEnv[sum.Place.Environment] = append(byEnv[sum.Place.Environment], sum)
	}
	for _, a := range acts {
		info := s.previewInfo(rec, a)
		p := Preview{Environment: a.Environment, Number: a.Number, CreatedAt: info.CreatedAt, ExpiresAt: info.ExpiresAt,
			Apps: make([]PreviewApp, 0, len(byEnv[a.Environment]))}
		for _, sum := range byEnv[a.Environment] { // ListAppsIn sorts by ID
			p.Apps = append(p.Apps, s.previewApp(sum))
		}
		out.Previews = append(out.Previews, p)
	}
	return out, nil
}

// previewApp is an app summary as a preview shows it.
func (s *Service) previewApp(sum AppSummary) PreviewApp {
	pa := PreviewApp{ID: sum.App.ID, Name: sum.Place.Name, State: sum.Status.State, Addresses: []string{},
		LastDeployment: sum.LastDeployment}
	for i, svc := range sum.App.Services {
		for _, d := range svc.Domains {
			if !slices.Contains(pa.Addresses, d.Host) {
				pa.Addresses = append(pa.Addresses, d.Host)
			}
		}
		copies := svc.CountFor(nil)
		if len(sum.Status.Services) == len(sum.App.Services) {
			copies = sum.Status.Services[i].Wanted
		}
		reserved := svc.Resources.MemoryMB
		if s.deployer != nil {
			_, reserved, _ = s.deployer.Reserved(svc.Resources)
		}
		pa.MemoryMB += reserved * copies
	}
	return pa
}

// PreviewSettingsInput is a change of the settings: a nil field keeps what is
// there.
type PreviewSettingsInput struct {
	Limit           *int
	ExpireAfterDays *int
	Password        *string
	RemovePassword  bool
}

// PreviewSettingsResult is the settings after a change, and what the owner
// should know about it.
type PreviewSettingsResult struct {
	Settings PreviewSettings
	Warnings []app.Warning
}

// validPreviewPassword checks a preview password: 12 to 128 printable
// characters (no control characters) and at most 72 bytes, since the hash is
// bcrypt's, which takes no more (a deviation from the contract's 128, which
// bcrypt can't honor).
func validPreviewPassword(p string) error {
	n := utf8.RuneCountInString(p)
	switch {
	case !utf8.ValidString(p) || strings.ContainsFunc(p, unicode.IsControl):
		return app.NewFieldError("password", "must be printable text, without control characters")
	case n < minPreviewPassword || n > maxPreviewPassword:
		return app.NewFieldError("password", "is %d characters; it must be %d to %d", n, minPreviewPassword, maxPreviewPassword)
	case len(p) > maxPreviewPasswordBytes:
		return app.NewFieldError("password", "is %d bytes; at most %d (the limit of the hash that protects the previews)", len(p), maxPreviewPasswordBytes)
	}
	return nil
}

// SetPreviewSettings changes a project's preview settings. Validation errors are
// *app.FieldError (limit 0 to 20, days 1 to 30, a password of 12 to 128
// printable characters, a password together with removePassword), an unknown
// project is ErrProjectNotFound, and removing the password while previews are
// open is ErrPreviewsOpen. A new password applies to every open preview at once:
// the Team-only rows of their domains take the new hash and Traefik's variable
// is written again, with no deploy; when that can't be written, the old hash
// stays everywhere. The password is hashed here and never stored, logged or
// returned.
func (s *Service) SetPreviewSettings(ctx context.Context, project string, in PreviewSettingsInput) (PreviewSettingsResult, error) {
	if in.Limit != nil && (*in.Limit < 0 || *in.Limit > maxPreviewLimit) {
		return PreviewSettingsResult{}, app.NewFieldError("limit", "is %d; it must be 0 to %d", *in.Limit, maxPreviewLimit)
	}
	if in.ExpireAfterDays != nil && (*in.ExpireAfterDays < minPreviewExpireDays || *in.ExpireAfterDays > maxPreviewExpireDays) {
		return PreviewSettingsResult{}, app.NewFieldError("expireAfterDays", "is %d; it must be %d to %d",
			*in.ExpireAfterDays, minPreviewExpireDays, maxPreviewExpireDays)
	}
	if in.Password != nil {
		if err := validPreviewPassword(*in.Password); err != nil {
			return PreviewSettingsResult{}, err
		}
		if in.RemovePassword {
			return PreviewSettingsResult{}, app.NewFieldError("removePassword", "can't be combined with a new password")
		}
	}

	// The Team-only rows and the project's hash change together, so no deploy
	// reads one without the other (ensureTeamPasswords takes the same lock).
	s.teamMu.Lock()
	defer s.teamMu.Unlock()

	rec, err := s.store.GetProject(ctx, project)
	if errors.Is(err, store.ErrNotFound) {
		return PreviewSettingsResult{}, fmt.Errorf("%w: %q", ErrProjectNotFound, project)
	}
	if err != nil {
		return PreviewSettingsResult{}, err
	}
	acts, err := s.store.PreviewActivity(ctx, project)
	if err != nil {
		return PreviewSettingsResult{}, err
	}
	open := make([]string, 0, len(acts))
	for _, a := range acts {
		open = append(open, a.Environment)
	}

	set := store.PreviewSettings{Limit: in.Limit, ExpireDays: in.ExpireAfterDays}
	var undo []store.TeamPasswordRecord
	var warnings []app.Warning
	switch {
	case in.RemovePassword:
		if rec.PreviewPasswordHash != "" && len(open) > 0 {
			return PreviewSettingsResult{}, &PreviewsOpenError{Project: project, Open: open}
		}
		set.ClearPassword = true
	case in.Password != nil:
		hash, err := deploy.TeamPasswordHash(*in.Password)
		if err != nil {
			return PreviewSettingsResult{}, err
		}
		set.PasswordHash = &hash
		if undo, err = s.store.SetPreviewTeamHash(ctx, project, hash, s.clock.Now().UTC()); err != nil {
			return PreviewSettingsResult{}, err
		}
		if len(undo) > 0 {
			if err := s.syncPreviewRowsLocked(ctx); err != nil {
				s.restoreTeamRows(ctx, undo)
				return PreviewSettingsResult{}, err
			}
		}
		if rec.PreviewPasswordHash == "" && len(open) > 0 {
			warnings = append(warnings, app.Warning{Field: "password", Message: fmt.Sprintf(
				"open previews %s get the password at their next deploy", strings.Join(open, ", "))})
		}
	}

	rec, err = s.store.SetPreviewSettings(ctx, project, set)
	if err != nil {
		if len(undo) > 0 {
			s.restoreTeamRows(ctx, undo)
			if syncErr := s.syncPreviewRowsLocked(ctx); syncErr != nil {
				s.log.Warn("write the Team-only passwords for Traefik after a failed preview settings change", "error", syncErr)
			}
		}
		if errors.Is(err, store.ErrNotFound) {
			return PreviewSettingsResult{}, fmt.Errorf("%w: %q", ErrProjectNotFound, project)
		}
		return PreviewSettingsResult{}, err
	}
	s.publish(Change{What: ChangeApps})
	return PreviewSettingsResult{Settings: previewSettingsOf(rec, len(open)), Warnings: warnings}, nil
}

// syncPreviewRowsLocked writes Traefik's variable from the table and the stored
// apps. The caller holds teamMu.
func (s *Service) syncPreviewRowsLocked(ctx context.Context) error {
	apps, err := s.storedAppsByID(ctx, nil)
	if err != nil {
		return err
	}
	return s.syncTeamPasswordsLocked(ctx, apps)
}

// restoreTeamRows puts back rows SetPreviewTeamHash changed. Failures are logged.
func (s *Service) restoreTeamRows(ctx context.Context, rows []store.TeamPasswordRecord) {
	for _, r := range rows {
		if err := s.store.SetTeamPassword(ctx, r.Host, r.AppID, r.Hash, r.CreatedAt); err != nil {
			s.log.Warn("put back a preview Team-only password", "domain", r.Host, "error", err)
		}
	}
}

// previewProject is the project of an app in a preview environment, or false for
// an app anywhere else.
func previewProject(appID string) (project string, ok bool) {
	place, err := app.PlaceOf(appID)
	if err != nil || !app.IsPreviewEnvironment(place.Environment) {
		return "", false
	}
	return place.Project, true
}

// previewHash is the preview password's hash of the project the app's preview
// belongs to: "" for an app that is no preview's, or whose project has no
// password. A project that can't be read counts as none.
func (s *Service) previewHash(ctx context.Context, appID string) (project, hash string) {
	project, ok := previewProject(appID)
	if !ok {
		return "", ""
	}
	rec, err := s.store.GetProject(ctx, project)
	if err != nil {
		return "", ""
	}
	return project, rec.PreviewPasswordHash
}

// previewTeamOnly turns Team only on for every domain of a, which is an app of
// a preview of a project with a password (D6). It returns a copy, and the field
// of the first domain it changed ("" when none was: every domain already was
// Team only, or there is none). a is not modified.
func previewTeamOnly(a app.App) (app.App, string) {
	out := a
	out.Services = slices.Clone(a.Services)
	first := ""
	for i := range out.Services {
		domains := slices.Clone(out.Services[i].Domains)
		for j := range domains {
			if domains[j].TeamOnly {
				continue
			}
			domains[j].TeamOnly = true
			if first == "" {
				first = fmt.Sprintf("services[%d].domains[%d].teamOnly", i, j)
			}
		}
		out.Services[i].Domains = domains
	}
	return out, first
}

// applyPreviewTeamOnly is previewTeamOnly for an app whose project may have a
// password: a and nil for any other app, else the changed app and the warning
// that says why (one, on the first changed field).
func (s *Service) applyPreviewTeamOnly(ctx context.Context, a app.App) (app.App, []app.Warning) {
	project, hash := s.previewHash(ctx, a.ID)
	if hash == "" {
		return a, nil
	}
	out, field := previewTeamOnly(a)
	if field == "" {
		return a, nil
	}
	return out, []app.Warning{{Field: field, Message: fmt.Sprintf(
		"domains of preview environments are Team only with project %s's preview password", project)}}
}

// CloseTrigger says who closes a preview: for the log, not stored.
type CloseTrigger string

// The ways a preview is closed.
const (
	CloseByOwner CloseTrigger = "owner"  // the API (a full token) or the console
	CloseExpired CloseTrigger = "expiry" // the reaper
)

// ClosePreview deletes every app of a preview, as DeleteApp does, then the
// environment. An unknown project or environment is ErrProjectNotFound or
// ErrEnvironmentNotFound, an environment that isn't named pr-<n> is
// ErrNotAPreview (nothing is deleted), and an app that can't be deleted is
// DeleteApp's error with the preview left as it is: the apps already deleted
// stay deleted, and the same call finishes the job. An app created while this
// runs makes the final delete answer ErrNotEmpty; the next call (or the
// reaper's next pass) closes it.
func (s *Service) ClosePreview(ctx context.Context, project, environment string, trigger CloseTrigger) error {
	if err := s.requireEnvironment(ctx, project, environment); err != nil {
		return err
	}
	if !app.IsPreviewEnvironment(environment) {
		return fmt.Errorf("environment %q of project %q %w", environment, project, ErrNotAPreview)
	}
	recs, err := s.store.ListAppsIn(ctx, project, environment)
	if err != nil {
		return fmt.Errorf("close preview %s/%s: %w", project, environment, err)
	}
	ids := make([]string, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, rec.App.ID)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := s.DeleteApp(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("close preview %s/%s: %w", project, environment, err)
		}
	}
	if err := s.DeleteEnvironment(ctx, project, environment); err != nil {
		return err
	}
	s.log.Info("closed a preview", "project", project, "environment", environment, "apps", len(ids), "by", string(trigger))
	return nil
}

// runPreviewReaper closes the expired previews every gap, the first pass one gap
// after start, until ctx ends.
func (s *Service) runPreviewReaper(ctx context.Context, gap time.Duration) {
	t := time.NewTicker(gap)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.reapPreviews(ctx)
	}
}

// reapPreviews is one pass of the reaper: every preview whose time is up is
// closed. A failure is logged with what failed and the pass goes on; the next
// pass tries again.
func (s *Service) reapPreviews(ctx context.Context) {
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		s.log.Warn("preview reaper: list the projects", "error", err)
		return
	}
	now := s.clock.Now()
	for _, rec := range projects {
		acts, err := s.store.PreviewActivity(ctx, rec.ID)
		if err != nil {
			s.log.Warn("preview reaper: read a project's previews", "project", rec.ID, "error", err)
			continue
		}
		for _, a := range acts {
			if ctx.Err() != nil {
				return
			}
			if !now.After(s.previewInfo(rec, a).ExpiresAt) {
				continue
			}
			if err := s.ClosePreview(ctx, rec.ID, a.Environment, CloseExpired); err != nil {
				s.log.Warn("preview reaper: close an expired preview; the next pass tries again",
					"project", rec.ID, "environment", a.Environment, "error", err)
			}
		}
	}
}
