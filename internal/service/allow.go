package service

import (
	"context"
	"errors"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Allow judges whether the person the request acts as may do what need says at target. It reads
// the principal from ctx; a context without one is Caravana itself (the watchers, the notifier,
// the host's CLI) and is allowed everything. The answer is nil or a *NotAllowedError.
//
// It judges the person only. A deploy token is held to its scope by the API before any handler
// runs, and that stays there.
func (s *Service) Allow(ctx context.Context, need Need, target Target) error {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil
	}
	return p.Allow(need, target, s.clock.Now())
}

// AllowPrincipal is Allow for a principal the caller already holds, not the one in a context: the
// API's streams ask again, now and then, whether the person still may do what opened them.
func (s *Service) AllowPrincipal(p Principal, need Need, target Target) error {
	return p.Allow(need, target, s.clock.Now())
}

// visibility says which places the person behind a request may see: every place for Caravana
// itself, an owner and an admin, and for a member the environments where their level is at least
// view. Lists that span places filter with it (feature 053, D6).
type visibility struct {
	who Principal
	all bool // no principal, an owner or an admin: nothing is filtered
	now time.Time
}

// visible is the visibility of the request in ctx.
func (s *Service) visible(ctx context.Context) visibility {
	p, ok := PrincipalFrom(ctx)
	return visibility{who: p, all: !ok || p.Admin(), now: s.clock.Now()}
}

// installation says the request may see what belongs to no place: the servers, the disks, the
// host's updates, Caravana's own update, the backup destination, the certificates.
func (v visibility) installation() bool { return v.all }

// place says the person may see the environment of the project.
func (v visibility) place(project, environment string) bool {
	return v.all || v.who.LevelAt(project, environment, v.now) >= LevelView
}

// app says the person may see the app, which is judged by its ID's place alone (as Allow does).
func (v visibility) app(id string) bool {
	if v.all {
		return true
	}
	project, environment := placeOfAppID(id)
	return v.place(project, environment)
}

// project is p with only the environments the person may see; ok is false when none is left, and
// the project is then invisible to them.
func (v visibility) project(p Project) (Project, bool) {
	if v.all {
		return p, true
	}
	envs := make([]Environment, 0, len(p.Environments))
	for _, e := range p.Environments {
		if v.place(p.ID, e.Name) {
			envs = append(envs, e)
		}
	}
	p.Environments = envs
	return p, len(envs) > 0
}

// attentionItem says the person may see a needs-you item: one with an app is judged by its place
// (the stored place the item carries, else the app ID's), one without is the installation's.
func (v visibility) attentionItem(it AttentionItem) bool {
	switch {
	case v.all:
		return true
	case it.AppID == "":
		return v.installation()
	case it.Project != "" && it.Environment != "":
		return v.place(it.Project, it.Environment)
	default:
		return v.app(it.AppID)
	}
}

// ConfigureLevel is the level that changes an app's definition in an environment: maintain, and
// deploy in a preview environment (pr-<n>), which CI opens and closes with a deploy token: the
// project's deploy role is what opens a preview, so it is what may define the apps in it
// (feature 053, plan D7).
func ConfigureLevel(environment string) Level {
	if app.IsPreviewEnvironment(environment) {
		return LevelDeploy
	}
	return LevelMaintain
}

// allowCreate refuses a person who may not create the app with that ID: it needs the level that
// configures its place. It reads the principal from ctx, and Caravana itself is allowed.
func (s *Service) allowCreate(ctx context.Context, id string) error {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil
	}
	project, environment := placeOfAppID(id)
	need := ConfigureLevel(environment)
	if have := p.LevelAt(project, environment, s.clock.Now()); have < need {
		return &NotAllowedError{Have: have, Need: need, Project: project, Environment: environment}
	}
	return nil
}

// allowSave judges the person behind a deploy that saves the app a (feature 053, D5 and D7). A
// definition that creates the app, a Compose file (m.file) and a body that changes anything but the
// services' images need the level that configures the place (maintain); a body that changes only
// images, which is what CI sends (the stored app with a new image), needs deploy. A person below the
// level the deploy needs is refused with the field that needs more, when there is one.
func (s *Service) allowSave(ctx context.Context, a app.App, m made) error {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil
	}
	project, environment := placeOfAppID(a.ID)
	have := p.LevelAt(project, environment, s.clock.Now())
	configure := ConfigureLevel(environment)
	if have >= configure {
		// Nothing a deploy does needs more, except a preview's secret references (below).
		return s.allowPreviewReferences(ctx, a)
	}
	refuse := func(need Level, field string) error {
		return &NotAllowedError{Have: have, Need: need, Project: project, Environment: environment, Field: field}
	}
	if m.file != nil {
		return refuse(configure, "")
	}
	stored, err := s.store.GetApp(ctx, a.ID)
	if errors.Is(err, store.ErrNotFound) {
		return refuse(configure, "") // it would be created
	} else if err != nil {
		return err
	}
	// The same normalization the deploy makes, so a body that only differs by it is no change.
	next := withBuildDefaults(a.WithLowercaseHosts())
	if field, same := app.ImagesOnlyChange(stored.App, next); !same {
		return refuse(configure, field)
	}
	if have < LevelDeploy {
		return refuse(LevelDeploy, "")
	}
	return nil
}

// previewReferencesReason is what a refusal of a preview's new secret reference says.
const previewReferencesReason = "a preview may only use the secret references its base app already has; adding one needs maintain on the project"

// allowPreviewReferences is the rule that keeps a preview's lower level from reaching a
// maintainer's secrets (feature 054, plan D12). In a preview environment (pr-<n>) a deploy-level
// person defines apps (ConfigureLevel), and a secret provider that the project may use is open to
// every app in the project's places, previews included. So a person below maintain, or a deploy
// token (CI's), may define or change a preview app only if every secret reference in it (each
// `from`, each template ref: provider and path) is one the app of the same name already has in a
// base environment of the project, any environment that isn't a preview. A reference that isn't
// is a *NotAllowedError whose Field names it. Maintain and above, and Caravana itself, are not
// held to it. A deploy token is held to it whoever made it, since CI's token is what a pull
// request's workflow holds.
func (s *Service) allowPreviewReferences(ctx context.Context, a app.App) error {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil
	}
	place := placeOfApp(a)
	if !app.IsPreviewEnvironment(place.Environment) {
		return nil
	}
	have := p.LevelAt(place.Project, place.Environment, s.clock.Now())
	if have >= LevelMaintain && p.Scope == nil {
		return nil
	}
	refs := referencesOf(a)
	if len(refs) == 0 {
		return nil
	}
	base, err := s.baseReferences(ctx, place)
	if err != nil {
		return err
	}
	for _, r := range refs {
		if base[r.ref] {
			continue
		}
		return &NotAllowedError{Have: min(have, LevelDeploy), Need: LevelMaintain, Project: place.Project,
			Environment: place.Environment, Field: r.field, Reason: previewReferencesReason}
	}
	return nil
}

// baseReferences is every reference (provider and path) of the apps named like place's app in the
// project's environments that aren't previews: the apps a preview is a copy of. The store doesn't
// record which environment a preview was opened from (CI chooses it), so every base environment
// counts.
func (s *Service) baseReferences(ctx context.Context, place app.Place) (map[app.SecretRef]bool, error) {
	recs, err := s.store.ListAppsIn(ctx, place.Project, "")
	if err != nil {
		return nil, err
	}
	out := map[app.SecretRef]bool{}
	for _, rec := range recs {
		if rec.Place.Name != place.Name || app.IsPreviewEnvironment(rec.Place.Environment) {
			continue
		}
		for _, r := range referencesOf(rec.App) {
			out[r.ref] = true
		}
	}
	return out, nil
}
