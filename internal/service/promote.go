package service

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// What promoting and copying an app between environments refuse with, besides
// the errors a past deployment or a deploy already has (ErrDeploymentNotHealthy,
// ErrDefinitionNotRecorded, *app.FieldError, *deploy.ImageError, ErrAppChanged).
var (
	// ErrAppNotInEnvironment is a promotion to an environment that has no app
	// of that name: the app is copied there first (CopyApp).
	ErrAppNotInEnvironment = errors.New("the target environment has no app of this name: copy the app there first")
	// ErrPromotionRemovesService is a promotion whose target has a service the
	// source doesn't: promoting would delete it, and Caravana never drops a
	// service silently (constitution IV).
	ErrPromotionRemovesService = errors.New("promoting would remove a service of the target")
)

// RefusalError is a sentinel refusal with the request field at fault, as
// PlaceError is for a missing project or environment: "to" for the target
// environment, "services.<name>" for a service. The API answers with that
// field. Err is the sentinel, wrapped with the names.
type RefusalError struct {
	Field string
	Err   error
}

func (e *RefusalError) Error() string { return e.Err.Error() }
func (e *RefusalError) Unwrap() error { return e.Err }

// ServiceChange is what a promotion does to one service of the target.
type ServiceChange struct {
	Name string
	// ImageFrom and DigestFrom are what the target runs now: its image
	// reference as written, and the digest its newest healthy deployment
	// pinned. Empty for an added service, and DigestFrom for a target that
	// was never deployed.
	ImageFrom, DigestFrom string
	// ImageTo and DigestTo are what it will run: the source's.
	ImageTo, DigestTo string
	// Changed names the parts of the service's shape the promotion replaces
	// with the source's and that differ: any of "entrypoint", "command", "args", "ports",
	// "health" and "volumes".
	Changed []string
}

// PromotionPreview is what promoting an app would deploy, before anything is.
type PromotionPreview struct {
	// From and To are the app IDs of the source and the target.
	From, To string
	// FromEnvironment and ToEnvironment are the environment names.
	FromEnvironment, ToEnvironment string
	// FromDeployment is the source's newest deployment, which the target
	// would run: the images it pinned and the shape it deployed.
	FromDeployment string
	// Services is every service the target will have, in order.
	Services []ServiceChange
	// Added names the services the target doesn't have yet and gets whole,
	// without domains.
	Added []string
	// Warnings are the ones the deploy will carry.
	Warnings []app.Warning
	// TargetRevision is the target's revision now: sent back as If-Match, it
	// makes the promotion refuse if the target changed after the preview.
	TargetRevision string
}

// MissingSecret is a secret a copied app needs a value for before it deploys.
type MissingSecret struct {
	Service string
	Name    string
}

// promotion is everything a promotion decides before it deploys.
type promotion struct {
	target store.AppRecord
	from   store.DeploymentRecord // the source's newest deployment
	result app.App                // the target as it will be deployed
	view   PromotionPreview
}

// PreviewPromotion answers what Promote(appID, to) would deploy. It saves and
// deploys nothing, and refuses for every reason Promote does (except a stale
// If-Match, which the caller makes with the preview's TargetRevision).
func (s *Service) PreviewPromotion(ctx context.Context, appID, to string) (PromotionPreview, error) {
	p, err := s.planPromotion(ctx, appID, to)
	if err != nil {
		return PromotionPreview{}, err
	}
	return p.view, nil
}

// Promote deploys the app appID, which must be in an environment of its
// project with a healthy newest deployment, into the environment named to,
// where an app of the same name must already exist (CopyApp makes it).
//
// The target runs the images the source's newest deployment pinned (the same
// digests, never the tags again) and its shape: command, arguments, ports,
// health check and volume mounts. It keeps its own env, secrets (and their
// values), domains, copies, resources and backup settings. A service only the
// source has is added whole, without domains; its secrets need values first
// (the deploy's own check refuses it). A service only the target has refuses
// the promotion, naming it (*RefusalError, ErrPromotionRemovesService).
//
// Refusals come before anything changes: an unknown or same environment
// (*app.FieldError or *PlaceError on "to"), a source that isn't healthy or
// recorded no definition (ErrDeploymentNotHealthy, ErrDefinitionNotRecorded,
// ErrNoLiveDeployment), a target that doesn't exist (ErrAppNotInEnvironment),
// an image that is gone from its registry (*deploy.ImageError), and the
// checks of any deploy. cond.IfRevision is the target's revision the caller
// saw; when empty, the revision this promotion was built from is used, so it
// never overwrites an edit made meanwhile (ErrAppChanged).
//
// The new deployment records the source deployment as its source: its
// SourceApp is the source app, which is how history says "promoted from".
func (s *Service) Promote(ctx context.Context, appID, to string, cond Condition, trigger app.Trigger) (store.DeploymentRecord, error) {
	if cond.IfNew {
		return store.DeploymentRecord{}, errors.New("promote: the target already exists, so a condition of IfNew can't hold")
	}
	p, err := s.planPromotion(ctx, appID, to)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	switch cond.IfRevision {
	case "":
		cond.IfRevision = p.target.Revision
	case p.target.Revision:
	default:
		return store.DeploymentRecord{}, fmt.Errorf("app %q is now at revision %s: %w", p.target.App.ID, p.target.Revision, ErrAppChanged)
	}
	return s.saveAndDeploy(ctx, p.result, cond, trigger, made{}, &pinned{digests: p.from.Deployment.Digests, source: p.from.Deployment.ID})
}

// targetPlace is the place appID's counterpart has in environment to of the
// same project, after checking to: a name, not the app's own environment, and
// an environment that exists (a *app.FieldError or *PlaceError on "to"). An
// app from before projects whose name holds "--" has no counterpart: its ID
// couldn't be read back.
func (s *Service) targetPlace(ctx context.Context, src store.AppRecord, to string) (app.Place, error) {
	if err := app.ValidName("to", to); err != nil {
		return app.Place{}, err
	}
	if to == src.Place.Environment {
		return app.Place{}, app.NewFieldError("to", "is the environment %q the app is in already", to)
	}
	if err := s.requireEnvironment(ctx, src.Place.Project, to); err != nil {
		return app.Place{}, &PlaceError{Field: "to", Err: err}
	}
	place := app.Place{Project: src.Place.Project, Environment: to, Name: src.Place.Name}
	if _, err := app.PlaceOf(place.ID()); err != nil {
		return app.Place{}, app.NewFieldError("id", "app %q can't be placed in another environment: its name must not contain \"--\"", src.App.ID)
	}
	return place, nil
}

// planPromotion makes every decision of a promotion and every check that can
// refuse it, in an order that names the cause the owner can fix first.
func (s *Service) planPromotion(ctx context.Context, appID, to string) (promotion, error) {
	src, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return promotion{}, err
	}
	place, err := s.targetPlace(ctx, src, to)
	if err != nil {
		return promotion{}, err
	}

	// What the target will run is the source's newest deployment, and only a
	// healthy one is known to work.
	latest, err := s.store.ListDeployments(ctx, appID, 1)
	if err != nil {
		return promotion{}, err
	}
	if len(latest) == 0 {
		return promotion{}, fmt.Errorf("app %q: %w", appID, ErrNoLiveDeployment)
	}
	from := latest[0]
	if from.Status != store.StatusHealthy {
		return promotion{}, fmt.Errorf("deployment %q is %s: %w", from.Deployment.ID, from.Status, ErrDeploymentNotHealthy)
	}
	if from.Definition == nil {
		return promotion{}, fmt.Errorf("deployment %q: %w", from.Deployment.ID, ErrDefinitionNotRecorded)
	}

	targetID := place.ID()
	target, err := s.store.GetApp(ctx, targetID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && target.Place != place) {
		// (A different place under that ID is an app from before projects
		// whose ID happens to spell this one: not the counterpart.)
		return promotion{}, &RefusalError{Field: "to", Err: fmt.Errorf("%w: %q has no app %q", ErrAppNotInEnvironment, to, place.Name)}
	}
	if err != nil {
		return promotion{}, err
	}
	if target.StoppedAt != nil {
		return promotion{}, &RefusalError{Field: "to", Err: fmt.Errorf("%q: %w", targetID, ErrAppStopped)}
	}

	result, changes, added, err := promoted(target.App, *from.Definition)
	if err != nil {
		return promotion{}, err
	}
	if err := s.deployer.Validate(result); err != nil {
		return promotion{}, err
	}
	// A promotion keeps the target's own domains and adds services without any,
	// so it can't bring the console's address in. The target may already have it
	// (saved before this check), and the preview should say so before Promote does.
	if err := s.checkConsoleDomain(result); err != nil {
		return promotion{}, err
	}
	if err := s.checkSecrets(ctx, result); err != nil {
		return promotion{}, err
	}
	if err := s.checkBackups(ctx, result); err != nil {
		return promotion{}, err
	}
	digests := from.Deployment.Digests
	if err := s.deployer.CheckDigests(ctx, result, digests); err != nil {
		return promotion{}, err
	}

	// DigestFrom is what the target runs now, from its newest healthy deployment.
	running := map[string]string{}
	if live, err := s.store.LatestHealthyDeployment(ctx, targetID); err == nil {
		running = live.Deployment.Digests
	} else if !errors.Is(err, store.ErrNotFound) {
		return promotion{}, err
	}
	for i := range changes {
		changes[i].DigestFrom = running[changes[i].Name]
		changes[i].DigestTo = digests[changes[i].Name]
		if slices.Contains(added, changes[i].Name) {
			changes[i].DigestFrom = ""
		}
	}

	// The warnings the deploy will record for this change (see saveAndDeploy).
	warnings := unmountedVolumes(target.App, result)
	if target.Compose != nil {
		warnings = append(warnings, app.Warning{Message: droppedFileWarning})
	}
	if warnings == nil {
		warnings = []app.Warning{}
	}
	return promotion{
		target: target,
		from:   from,
		result: result,
		view: PromotionPreview{
			From: appID, To: targetID,
			FromEnvironment: src.Place.Environment, ToEnvironment: to,
			FromDeployment: from.Deployment.ID,
			Services:       changes, Added: added, Warnings: warnings,
			TargetRevision: target.Revision,
		},
	}, nil
}

// promoted builds the target as a promotion leaves it, from the target as
// stored and the source as its newest deployment deployed it (research R4):
// for each service both have, the source's image, entrypoint, command, arguments, ports,
// health check and volume mounts, and the target's everything else. A service
// only the source has is added whole, without domains. A service only the
// target has is a *RefusalError. It is pure.
func promoted(target, source app.App) (result app.App, changes []ServiceChange, added []string, err error) {
	byName := map[string]app.Service{}
	for _, svc := range source.Services {
		byName[svc.Name] = svc
	}
	for _, t := range target.Services {
		if _, ok := byName[t.Name]; !ok {
			return app.App{}, nil, nil, &RefusalError{Field: "services." + t.Name,
				Err: fmt.Errorf("%w: service %q is in the target but not in the source", ErrPromotionRemovesService, t.Name)}
		}
	}

	result = target
	result.Services = nil
	kept := map[string]bool{}
	for _, t := range target.Services {
		s := byName[t.Name]
		kept[t.Name] = true
		change := ServiceChange{Name: t.Name, ImageFrom: t.Image, ImageTo: s.Image}
		if !slices.Equal(t.Entrypoint, s.Entrypoint) {
			change.Changed = append(change.Changed, "entrypoint")
		}
		if t.Command != s.Command {
			change.Changed = append(change.Changed, "command")
		}
		if !slices.Equal(t.Args, s.Args) {
			change.Changed = append(change.Changed, "args")
		}
		if !slices.Equal(t.Ports, s.Ports) {
			change.Changed = append(change.Changed, "ports")
		}
		if !sameHealth(t.Health, s.Health) {
			change.Changed = append(change.Changed, "health")
		}
		if !slices.Equal(t.Volumes, s.Volumes) {
			change.Changed = append(change.Changed, "volumes")
		}
		changes = append(changes, change)

		t.Image, t.Command = s.Image, s.Command
		t.Entrypoint = slices.Clone(s.Entrypoint)
		t.Args, t.Ports, t.Volumes = slices.Clone(s.Args), slices.Clone(s.Ports), slices.Clone(s.Volumes)
		t.Health = cloneHealth(s.Health)
		result.Services = append(result.Services, t)
	}
	for _, s := range source.Services {
		if kept[s.Name] {
			continue
		}
		added = append(added, s.Name)
		changes = append(changes, ServiceChange{Name: s.Name, ImageTo: s.Image})
		s.Domains = nil
		result.Services = append(result.Services, s)
	}
	return result, changes, added, nil
}

func sameHealth(a, b *app.HealthCheck) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func cloneHealth(h *app.HealthCheck) *app.HealthCheck {
	if h == nil {
		return nil
	}
	c := *h
	return &c
}

// CopyApp creates the app appID in another environment of its project, named
// by to, without deploying it (research R5): the stored app as it is, with no
// domains (the target's are its own to choose), and the Compose file it keeps,
// if any, likewise. Secret values were never stored, so the copy has none; the
// answer lists every secret it needs one for, since a deploy is refused until
// they are set. Webhook tokens, backups and the contents of volumes are not
// copied.
//
// An environment that has an app of that name is *RefusalError on "to" with
// ErrAppExists (promotion is how it is updated); an unknown or same
// environment is as for Promote.
func (s *Service) CopyApp(ctx context.Context, appID, to string) (store.AppRecord, []MissingSecret, error) {
	src, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return store.AppRecord{}, nil, err
	}
	place, err := s.targetPlace(ctx, src, to)
	if err != nil {
		return store.AppRecord{}, nil, err
	}

	a := src.App.WithoutSecretValues() // already names only; a copy of the services
	a.ID = place.ID()
	for i := range a.Services {
		a.Services[i].Domains = nil
	}
	var file *compose.Request
	if src.Compose != nil {
		kept := src.Compose.WithoutSecretValues() // its own Services map
		for name, overlay := range kept.Services {
			overlay.Domains = nil
			kept.Services[name] = overlay
		}
		file = &kept
	}
	// The ID may be too long with the place in front of it, and the settings
	// may have changed since the source was saved: refuse now, not at its first
	// deploy. An app with no definition yet is only a name: nothing to check.
	if a.Defined() {
		if err := s.deployer.Validate(a); err != nil {
			return store.AppRecord{}, nil, err
		}
	}

	rec, err := s.store.InsertApp(ctx, a, file)
	if errors.Is(err, store.ErrAlreadyExists) {
		return store.AppRecord{}, nil, &RefusalError{Field: "to",
			Err: fmt.Errorf("%w: %q already has app %q", ErrAppExists, to, place.Name)}
	}
	if err != nil {
		return store.AppRecord{}, nil, err
	}
	s.appChanged(a.ID)

	var missing []MissingSecret
	for _, svc := range a.Services {
		for _, sec := range svc.Secrets {
			missing = append(missing, MissingSecret{Service: svc.Name, Name: sec.Name})
		}
	}
	return rec, missing, nil
}
