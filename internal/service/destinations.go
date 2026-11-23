package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Backup destinations (feature 052): the installation keeps several named
// S3-compatible destinations, one of them the default. A backup policy names
// where its backups go (or leaves it to its project's default, then the
// installation's), and may name others to copy each backup to. The secret key
// of a destination lives only in the Nomad Variable its row names.

// Errors the API maps to their own codes.
var (
	ErrDestinationNotFound  = errors.New("backup destination not found")
	ErrDestinationIsDefault = errors.New("is the default backup destination: make another the default first")
)

// DestinationInput is a backup destination's settings. A nil SecretAccessKey
// keeps the one set before. Default asks to make the destination the default
// (nil leaves it as it is; the first destination is the default whatever it
// says).
type DestinationInput struct {
	Endpoint, Region, Bucket, Prefix, AccessKeyID string
	SecretAccessKey                               *string
	Default                                       *bool
}

// DestinationUse says what uses a destination: a service's policy, as its
// destination or as a copy (Role), or a project's default (Project).
type DestinationUse struct {
	App, Service string
	// Role is "destination" or "copy" for a service; empty for a project.
	Role string
	// Project is set when the use is a project's default.
	Project string
}

// Roles of a service that uses a destination.
const (
	UseDestination = "destination"
	UseCopy        = "copy"
)

// String is the use as words an error message can list.
func (u DestinationUse) String() string {
	switch {
	case u.Project != "":
		return "project " + u.Project
	case u.Role == UseCopy:
		return u.App + "/" + u.Service + " (copy)"
	}
	return u.App + "/" + u.Service
}

// DestinationView is a destination with what uses it. It never has the secret
// key.
type DestinationView struct {
	store.BackupDestination
	UsedBy []DestinationUse
}

// DestinationInUseError is the refusal of removing a destination something still
// uses. Users names them, as DestinationUse.String says.
type DestinationInUseError struct {
	Name  string
	Users []string
}

func (e *DestinationInUseError) Error() string {
	shown := e.Users
	more := ""
	if len(shown) > maxUsersNamed {
		shown, more = shown[:maxUsersNamed], fmt.Sprintf(" and %d more", len(e.Users)-maxUsersNamed)
	}
	return fmt.Sprintf("backup destination %q is used by %s%s", e.Name, strings.Join(shown, ", "), more)
}

// maxUsersNamed is how many users a refusal lists before "and N more".
const maxUsersNamed = 5

var (
	bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	prefixRE = regexp.MustCompile(`^[A-Za-z0-9._/-]*$`)
)

// GetBackupDestination returns the default destination without its secret key,
// or store.ErrNotFound when none is set.
func (s *Service) GetBackupDestination(ctx context.Context) (store.BackupDestination, error) {
	return s.store.GetBackupDestination(ctx)
}

// SetBackupDestination saves the default destination, or makes "main" when
// there is none: the call of the single-destination days (GET and PUT
// /v1/backups/destination), which reads and writes the default.
func (s *Service) SetBackupDestination(ctx context.Context, in DestinationInput) (store.BackupDestination, error) {
	name := "main"
	switch d, err := s.store.GetBackupDestination(ctx); {
	case err == nil:
		name = d.Name
	case !errors.Is(err, store.ErrNotFound):
		return store.BackupDestination{}, err
	}
	return s.SaveBackupDestination(ctx, name, in)
}

// ListBackupDestinations returns every destination, ordered by name, with what
// uses each.
func (s *Service) ListBackupDestinations(ctx context.Context) ([]DestinationView, error) {
	list, err := s.store.ListBackupDestinations(ctx)
	if err != nil {
		return nil, err
	}
	uses, err := s.destinationUses(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DestinationView, 0, len(list))
	for _, d := range list {
		out = append(out, DestinationView{BackupDestination: d, UsedBy: uses[d.Name]})
	}
	return out, nil
}

// validateDestination checks a destination's name and settings. The errors are
// *app.FieldError naming the input field.
func validateDestination(name string, in DestinationInput) error {
	u, err := url.Parse(in.Endpoint)
	switch {
	case !app.ValidLabel(name):
		return app.NewFieldError("name", "%q must be 1 to 32 lowercase letters, digits or hyphens, starting and ending with a letter or digit", name)
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return app.NewFieldError("endpoint", "%q must be an http or https URL", in.Endpoint)
	case strings.TrimSpace(in.Region) == "":
		return app.NewFieldError("region", "is required (us-east-1 when the provider doesn't care)")
	case !bucketRE.MatchString(in.Bucket):
		return app.NewFieldError("bucket", "%q must be a bucket name: lowercase letters, digits, '.' and '-'", in.Bucket)
	case !prefixRE.MatchString(in.Prefix):
		return app.NewFieldError("prefix", "may hold letters, digits, '.', '_', '-' and '/'")
	case strings.TrimSpace(in.AccessKeyID) == "":
		return app.NewFieldError("accessKeyId", "is required")
	}
	return nil
}

// SaveBackupDestination creates or updates the destination called name and
// points every backup that goes there at the new settings from its next run (no
// deploy). The first destination is the default; Default true makes this one
// the default, and Default false on the default is a field error on "default".
// Errors are *app.FieldError naming the input field, or Nomad's. When the
// destination is saved but the backups couldn't be repointed, the error says so
// and the destination is returned with it.
func (s *Service) SaveBackupDestination(ctx context.Context, name string, in DestinationInput) (store.BackupDestination, error) {
	if err := validateDestination(name, in); err != nil {
		return store.BackupDestination{}, err
	}
	existing, err := s.store.GetBackupDestinationNamed(ctx, name)
	isNew := errors.Is(err, store.ErrNotFound)
	if err != nil && !isNew {
		return store.BackupDestination{}, err
	}
	if in.Default != nil && !*in.Default && !isNew && existing.IsDefault {
		return store.BackupDestination{}, app.NewFieldError("default", "can't be false on the default destination: make another destination the default instead")
	}
	makeDefault := in.Default != nil && *in.Default
	if makeDefault && (isNew || !existing.IsDefault) {
		if err := s.checkDefaultChange(ctx, name); err != nil {
			return store.BackupDestination{}, err
		}
	}

	path := existing.SecretPath
	if isNew {
		path = deploy.DestinationSecretPath(name)
	}
	secret := ""
	if in.SecretAccessKey != nil {
		if secret = *in.SecretAccessKey; secret == "" {
			return store.BackupDestination{}, app.NewFieldError("secretAccessKey", "is empty; leave it out to keep the one set before")
		}
		if err := s.deployer.SaveBackupSecret(ctx, path, secret); err != nil {
			return store.BackupDestination{}, err
		}
	} else if isNew {
		return store.BackupDestination{}, app.NewFieldError("secretAccessKey", "is required the first time")
	} else if secret, err = s.deployer.BackupSecret(ctx, path); err != nil {
		return store.BackupDestination{}, err
	} else if secret == "" {
		return store.BackupDestination{}, app.NewFieldError("secretAccessKey", "is required: the one set before is gone")
	}

	d := store.BackupDestination{Name: name, Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket,
		Prefix: strings.Trim(in.Prefix, "/"), AccessKeyID: in.AccessKeyID, SecretPath: path, UpdatedAt: time.Now().UTC()}
	if err := s.store.SaveBackupDestination(ctx, d); err != nil {
		return store.BackupDestination{}, err
	}
	if makeDefault {
		if err := s.store.SetDefaultBackupDestination(ctx, name); err != nil {
			return store.BackupDestination{}, err
		}
	}
	saved, err := s.store.GetBackupDestinationNamed(ctx, name)
	if err != nil {
		return store.BackupDestination{}, err
	}
	s.publish(Change{What: ChangeAttention}) // "no backup destination" may be resolved
	if err := s.repointBackups(ctx); err != nil {
		return saved, fmt.Errorf("saved, but the backup jobs still point at the old destination: %w", err)
	}
	return saved, nil
}

// SetDefaultBackupDestination makes the destination called name the default and
// points every backup that goes to the default at it from its next run. An
// unknown name is ErrDestinationNotFound. A change that would leave a policy
// copying to the destination its backups go to is refused, naming the service.
func (s *Service) SetDefaultBackupDestination(ctx context.Context, name string) error {
	if _, err := s.store.GetBackupDestinationNamed(ctx, name); errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %q", ErrDestinationNotFound, name)
	} else if err != nil {
		return err
	}
	if err := s.checkDefaultChange(ctx, name); err != nil {
		return err
	}
	if err := s.store.SetDefaultBackupDestination(ctx, name); err != nil {
		return err
	}
	s.publish(Change{What: ChangeAttention})
	if err := s.repointBackups(ctx); err != nil {
		return fmt.Errorf("saved, but the backup jobs still point at the old destination: %w", err)
	}
	return nil
}

// DeleteBackupDestination removes the destination called name and its secret
// key. An unknown name is ErrDestinationNotFound, the default is
// ErrDestinationIsDefault and one that something uses is a
// *DestinationInUseError naming its users. Backups already kept there stay in
// the bucket and their records stay.
func (s *Service) DeleteBackupDestination(ctx context.Context, name string) error {
	d, err := s.store.GetBackupDestinationNamed(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %q", ErrDestinationNotFound, name)
	}
	if err != nil {
		return err
	}
	if d.IsDefault {
		return fmt.Errorf("%q %w", name, ErrDestinationIsDefault)
	}
	uses, err := s.destinationUses(ctx)
	if err != nil {
		return err
	}
	if used := uses[name]; len(used) > 0 {
		users := make([]string, len(used))
		for i, u := range used {
			users[i] = u.String()
		}
		return &DestinationInUseError{Name: name, Users: users}
	}
	if err := s.store.DeleteBackupDestination(ctx, name); err != nil {
		return err
	}
	s.publish(Change{What: ChangeAttention})
	if err := s.deployer.DeleteBackupSecret(ctx, d.SecretPath); err != nil {
		return fmt.Errorf("removed, but its secret key is still stored: %w", err)
	}
	return nil
}

func toDeployDestination(d store.BackupDestination, secret string) deploy.BackupDestination {
	return deploy.BackupDestination{Name: d.Name, Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, Prefix: d.Prefix,
		AccessKeyID: d.AccessKeyID, SecretAccessKey: secret}
}

// withSecret is d with its secret key, read from the Nomad Variable its row names.
func (s *Service) withSecret(ctx context.Context, d store.BackupDestination) (deploy.BackupDestination, error) {
	secret, err := s.deployer.BackupSecret(ctx, d.SecretPath)
	if err != nil {
		return deploy.BackupDestination{}, err
	}
	return toDeployDestination(d, secret), nil
}

// backupDestination is the installation's default destination, secret
// included, for jobs. ok is false when none is set. What a service's backups use
// is destinationFor.
func (s *Service) backupDestination(ctx context.Context) (deploy.BackupDestination, bool, error) {
	d, err := s.store.GetBackupDestination(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return deploy.BackupDestination{}, false, nil
	}
	if err != nil {
		return deploy.BackupDestination{}, false, err
	}
	dest, err := s.withSecret(ctx, d)
	if err != nil {
		return deploy.BackupDestination{}, false, err
	}
	return dest, true, nil
}

// destinationNamed is the destination called name, secret included. ok is false
// when there is none by that name.
func (s *Service) destinationNamed(ctx context.Context, name string) (deploy.BackupDestination, bool, error) {
	d, err := s.store.GetBackupDestinationNamed(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return deploy.BackupDestination{}, false, nil
	}
	if err != nil {
		return deploy.BackupDestination{}, false, err
	}
	dest, err := s.withSecret(ctx, d)
	if err != nil {
		return deploy.BackupDestination{}, false, err
	}
	return dest, true, nil
}

// destinationFor is the destination a service's backups go to, secret included.
// ok is false when none resolves or the one named doesn't exist.
func (s *Service) destinationFor(ctx context.Context, appID string, b app.Backup) (deploy.BackupDestination, bool, error) {
	primary, _, err := s.backupPlaces(ctx, appID, b)
	if errors.Is(err, ErrNoBackupDestination) {
		return deploy.BackupDestination{}, false, nil
	}
	if err != nil {
		return deploy.BackupDestination{}, false, err
	}
	return s.destinationNamed(ctx, primary)
}

// runDestination is the destination a volume or sqlite run uses, secret included:
// the one named from when it is not empty (a restore reading from a place), else
// the one the service's policy sends backups to. ok is false when there is none.
func (s *Service) runDestination(ctx context.Context, appID string, b app.Backup, from string) (deploy.BackupDestination, bool, error) {
	if from != "" {
		return s.destinationNamed(ctx, from)
	}
	return s.destinationFor(ctx, appID, b)
}

// resolveBackupPlaces is the rule of where a policy's backups go: its own
// destination, else its project's default, else the installation's ("" when none
// of the three exists). Copies are the policy's, as it says them.
func resolveBackupPlaces(b app.Backup, projectDefault, installDefault string) (primary string, copies []string) {
	switch {
	case b.Destination != "":
		primary = b.Destination
	case projectDefault != "":
		primary = projectDefault
	default:
		primary = installDefault
	}
	return primary, b.CopyTo
}

// backupPlaces resolves where the backups of the app's policy b go: the name of
// its destination and of each copy. It doesn't check that the names exist
// (checkBackups does). With no destination at all it is ErrNoBackupDestination.
func (s *Service) backupPlaces(ctx context.Context, appID string, b app.Backup) (primary string, copies []string, err error) {
	projectDefault, err := s.projectBackupDestination(ctx, appID)
	if err != nil {
		return "", nil, err
	}
	installDefault := ""
	switch d, err := s.store.GetBackupDestination(ctx); {
	case err == nil:
		installDefault = d.Name
	case !errors.Is(err, store.ErrNotFound):
		return "", nil, err
	}
	primary, copies = resolveBackupPlaces(b, projectDefault, installDefault)
	if primary == "" {
		return "", nil, ErrNoBackupDestination
	}
	return primary, copies, nil
}

// projectBackupDestination is the default destination of the project the app
// lives in, "" when it has none. An ID that reads as no place lives in Default.
func (s *Service) projectBackupDestination(ctx context.Context, appID string) (string, error) {
	project := app.DefaultProject
	if p, err := app.PlaceOf(appID); err == nil {
		project = p.Project
	}
	rec, err := s.store.GetProject(ctx, project)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return rec.BackupDestination, nil
}

// destinationUses says, for each destination name, which services' policies
// resolve to it (as destination or as a copy) and which projects have it as
// their default.
func (s *Service) destinationUses(ctx context.Context) (map[string][]DestinationUse, error) {
	apps, projects, installDefault, err := s.backupWorld(ctx)
	if err != nil {
		return nil, err
	}
	uses := map[string][]DestinationUse{}
	for _, rec := range apps {
		for _, svc := range rec.App.Services {
			if svc.Backup == nil {
				continue
			}
			primary, copies := resolveBackupPlaces(*svc.Backup, projects[projectOfApp(rec.App.ID)], installDefault)
			if primary != "" {
				uses[primary] = append(uses[primary], DestinationUse{App: rec.App.ID, Service: svc.Name, Role: UseDestination})
			}
			for _, c := range copies {
				uses[c] = append(uses[c], DestinationUse{App: rec.App.ID, Service: svc.Name, Role: UseCopy})
			}
		}
	}
	ids := make([]string, 0, len(projects))
	for id := range projects {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if name := projects[id]; name != "" {
			uses[name] = append(uses[name], DestinationUse{Project: id})
		}
	}
	return uses, nil
}

// backupWorld reads what resolving backup places needs: every stored app, each
// project's default destination (by project ID, "" for none) and the name of
// the installation's default destination ("" for none).
func (s *Service) backupWorld(ctx context.Context) (apps []store.AppRecord, projects map[string]string, installDefault string, err error) {
	if apps, err = s.store.ListApps(ctx); err != nil {
		return nil, nil, "", err
	}
	recs, err := s.store.ListProjects(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	projects = make(map[string]string, len(recs))
	for _, p := range recs {
		projects[p.ID] = p.BackupDestination
	}
	switch d, err := s.store.GetBackupDestination(ctx); {
	case err == nil:
		installDefault = d.Name
	case !errors.Is(err, store.ErrNotFound):
		return nil, nil, "", err
	}
	return apps, projects, installDefault, nil
}

// projectOfApp is the project an app lives in: an ID that reads as no place
// lives in Default.
func projectOfApp(appID string) string {
	if p, err := app.PlaceOf(appID); err == nil {
		return p.Project
	}
	return app.DefaultProject
}

// copyConflict finds a policy that, with the given defaults (the projects' by
// project ID and the installation's, as they would be after a change), would
// copy its backups to the destination they already go to.
func copyConflict(apps []store.AppRecord, projects map[string]string, installDefault string) (appID, service, dest string, found bool) {
	for _, rec := range apps {
		for _, svc := range rec.App.Services {
			if svc.Backup == nil {
				continue
			}
			primary, copies := resolveBackupPlaces(*svc.Backup, projects[projectOfApp(rec.App.ID)], installDefault)
			if primary != "" && slices.Contains(copies, primary) {
				return rec.App.ID, svc.Name, primary, true
			}
		}
	}
	return "", "", "", false
}

// checkDefaultChange refuses making name the installation's default when a
// policy would then copy to where its backups go.
func (s *Service) checkDefaultChange(ctx context.Context, name string) error {
	apps, projects, _, err := s.backupWorld(ctx)
	if err != nil {
		return err
	}
	if a, svc, dest, found := copyConflict(apps, projects, name); found {
		return app.NewFieldError("name", "making %q the default would make %s/%s copy its backups to %q, where they would already go; change that policy first",
			name, a, svc, dest)
	}
	return nil
}

// repointService points the backup jobs of one service at the destination its
// policy resolves to now. A restore that read from another place leaves that
// place's keys in the service's volume variable; this puts the policy's back. A
// service that no longer has a policy, or whose destination is gone, is left as it is.
func (s *Service) repointService(ctx context.Context, appID, service string) {
	_, svc, err := s.backedUpService(ctx, appID, service)
	if err != nil {
		return
	}
	dest, ok, err := s.destinationFor(ctx, appID, *svc.Backup)
	if err == nil && ok {
		err = s.deployer.PointBackups(ctx, appID, service, dest)
	}
	if err != nil {
		s.log.Warn("point a service's backups at its destination", "app", appID, "service", service, "error", err)
	}
}

// repointBackups points the backup jobs of every stored service that has a
// backup policy at the destination its policy resolves to now, without a deploy.
// It runs after a destination is saved, the default changes or a project's
// default changes. A service whose destination can't be resolved is left as it
// is; the other services are still pointed, and the errors are joined.
func (s *Service) repointBackups(ctx context.Context) error {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, rec := range apps {
		for _, svc := range rec.App.Services {
			if svc.Backup == nil {
				continue
			}
			dest, ok, err := s.destinationFor(ctx, rec.App.ID, *svc.Backup)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !ok {
				continue
			}
			if err := s.deployer.PointBackups(ctx, rec.App.ID, svc.Name, dest); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
