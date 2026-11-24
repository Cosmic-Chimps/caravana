package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// People and roles (feature 053). This file is the pure part: who a request acts as (a
// Principal) and what that person may do in a place (a Level). Nothing here reads the store;
// Auth loads the Principal and the rules built on it come next to it.

// OrgRole is a person's role in the installation.
type OrgRole string

// The organization roles. An owner does everything and is the only role that makes, changes or
// removes owners and admins; an admin does everything else; a member has only the projects they
// are given.
const (
	OrgOwner  OrgRole = "owner"
	OrgAdmin  OrgRole = "admin"
	OrgMember OrgRole = "member"
)

// Role is a role in a project, or in one of its environments. RoleNone only exists as an
// environment override: it takes away what the project role gives.
type Role string

// The roles a member may hold in a project or an environment.
const (
	RoleNone     Role = "none"
	RoleView     Role = "view"
	RoleDeploy   Role = "deploy"
	RoleMaintain Role = "maintain"
)

// Level is what a person may do in a place, in order: each level includes the ones before it.
type Level int

// The levels. The zero value is no access.
const (
	LevelNone Level = iota
	LevelView
	LevelDeploy
	LevelMaintain
)

// String is the level's name as the console and the API say it: none, view, deploy, maintain.
func (l Level) String() string {
	switch l {
	case LevelView:
		return string(RoleView)
	case LevelDeploy:
		return string(RoleDeploy)
	case LevelMaintain:
		return string(RoleMaintain)
	default:
		return string(RoleNone)
	}
}

// Level is the level a role gives; a role we don't know gives none.
func (r Role) Level() Level {
	switch r {
	case RoleView:
		return LevelView
	case RoleDeploy:
		return LevelDeploy
	case RoleMaintain:
		return LevelMaintain
	default:
		return LevelNone
	}
}

// ProjectRole is a member's role in one project, with the overrides for some of its
// environments. EndsAt, when set, is when the whole role (overrides too) stops counting.
type ProjectRole struct {
	Project      string
	Role         Role
	EndsAt       *time.Time
	Environments map[string]Role
}

// current reports whether the role still counts at now.
func (r ProjectRole) current(now time.Time) bool {
	return r.EndsAt == nil || now.Before(*r.EndsAt)
}

// Principal is who a request acts as: a signed-in person, or the person an API token belongs to
// (then Scope and TokenName say which token and what it is limited to). Auth loads it fresh from
// the store on every request, so a changed role or a removal applies to the next one.
type Principal struct {
	PersonID string
	Email    string
	OrgRole  OrgRole
	// Roles are a member's project roles; an owner and an admin have none and need none.
	Roles []ProjectRole
	// Scope is set for a deploy token (feature 020): it limits the person further. TokenName is
	// the token's name, "" for a session.
	Scope     *TokenScope
	TokenName string
}

// Owner reports whether the person is an owner.
func (p Principal) Owner() bool { return p.OrgRole == OrgOwner }

// Admin reports whether the person is an owner or an admin: they run the installation and
// have maintain everywhere.
func (p Principal) Admin() bool { return p.OrgRole == OrgOwner || p.OrgRole == OrgAdmin }

// projectRole finds the member's role in a project.
func (p Principal) projectRole(project string) (ProjectRole, bool) {
	for _, r := range p.Roles {
		if r.Project == project {
			return r, true
		}
	}
	return ProjectRole{}, false
}

// LevelAt is what the person may do in one environment of a project at now. An owner and an admin
// may do everything. A member has the environment's override when one is set, else the project
// role; a role past its end date counts as none. A preview environment (pr-<n>) takes the
// project role: nobody sets an override on a place that is made and removed by CI.
func (p Principal) LevelAt(project, environment string, now time.Time) Level {
	if p.Admin() {
		return LevelMaintain
	}
	r, ok := p.projectRole(project)
	if !ok || !r.current(now) {
		return LevelNone
	}
	if override, ok := r.Environments[environment]; ok && !app.IsPreviewEnvironment(environment) {
		return override.Level()
	}
	return r.Role.Level()
}

// ProjectLevel is what the person may do about the project itself (its settings, its
// environments) at now: the project role, whatever the overrides say.
func (p Principal) ProjectLevel(project string, now time.Time) Level {
	if p.Admin() {
		return LevelMaintain
	}
	r, ok := p.projectRole(project)
	if !ok || !r.current(now) {
		return LevelNone
	}
	return r.Role.Level()
}

type principalKey struct{}

// WithPrincipal returns a context that carries p. The API's authenticate and the console's guard
// put it there; the service reads it with PrincipalFrom.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom is the principal WithPrincipal put in ctx. ok is false for a context without one:
// that is Caravana itself (the watchers, the notifier, the host CLI), which is not filtered and
// not refused.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// ErrNotAllowed is the refusal of something the person's role doesn't permit. Callers match it
// with errors.Is; the error is a *NotAllowedError that says what was held and what was needed.
var ErrNotAllowed = errors.New("not allowed")

// NotAllowedError says why a person may not do something, in words that name the role they have
// and the one it needs: "your role in shop › production is view; this needs deploy". It wraps
// ErrNotAllowed.
type NotAllowedError struct {
	// Have is the person's level in the place, Need the level the action needs. Both are unused
	// for a refusal that needs an organization role (Org).
	Have, Need Level
	// Org is "admin" or "owner" when the action needs an organization role rather than a role in
	// a place: "this needs an owner or an admin" or "this needs an owner".
	Org string
	// Project and Environment are the place; Environment is "" for a project's own settings.
	Project, Environment string
	// Field is the field of the request that needs Need, when one does ("services[0].env"): a
	// deploy-level person may change only images.
	Field string
}

func (e *NotAllowedError) Unwrap() error { return ErrNotAllowed }

func (e *NotAllowedError) Error() string {
	switch e.Org {
	case "owner":
		return "this needs an owner"
	case "admin":
		return "this needs an owner or an admin"
	}
	where := e.Project
	if e.Environment != "" {
		where += " › " + e.Environment
	}
	msg := fmt.Sprintf("your role in %s is %s; this needs %s", where, e.Have, e.Need)
	if e.Field != "" {
		msg += " to change " + e.Field
	}
	return msg
}

// needKind says what a Need is about.
type needKind int

const (
	needSignedIn needKind = iota
	needAdmin
	needOwner
	needApp
	needPlace
	needProject
)

// Need is what an action needs of the person who asks. Build one with the constructors; the API's
// and the console's route tables and the service's own checks all use them, so one function
// (Principal.Allow, Service.Allow) decides everything.
type Need struct {
	kind  needKind
	level Level
}

// NeedSignedIn is any person: their own account, and the lists the service filters for them.
func NeedSignedIn() Need { return Need{kind: needSignedIn} }

// NeedAdmin is an owner or an admin.
func NeedAdmin() Need { return Need{kind: needAdmin} }

// NeedOwner is an owner: making, changing or removing owners and admins.
func NeedOwner() Need { return Need{kind: needOwner} }

// NeedApp is at least level in the place of the app whose ID the target gives. An app that doesn't
// exist is judged by its ID alone, so a person with no role gets the same answer whether or not
// the app exists.
func NeedApp(level Level) Need { return Need{kind: needApp, level: level} }

// NeedPlace is at least level in the project and environment the target gives.
func NeedPlace(level Level) Need { return Need{kind: needPlace, level: level} }

// NeedProject is at least level in the project's own role (its settings, its environments).
func NeedProject(level Level) Need { return Need{kind: needProject, level: level} }

// Target says where a Need applies: the app's ID, or a project and environment (Environment is ""
// for NeedProject).
type Target struct {
	AppID                string
	Project, Environment string
}

// placeOfAppID is the project and environment an app ID belongs to. An ID that app.PlaceOf refuses
// (one from before projects, with a "--" in it) is in Default › production, as feature 019 has it.
func placeOfAppID(id string) (project, environment string) {
	if p, err := app.PlaceOf(id); err == nil {
		return p.Project, p.Environment
	}
	return app.DefaultProject, app.DefaultEnvironment
}

// Allow judges whether the person may do what need says at target, as of now. It is nil, or a
// *NotAllowedError. It judges the person only: a deploy token's scope is the API's to hold.
func (p Principal) Allow(need Need, t Target, now time.Time) error {
	switch need.kind {
	case needSignedIn:
		return nil
	case needAdmin:
		if p.Admin() {
			return nil
		}
		return &NotAllowedError{Org: "admin"}
	case needOwner:
		if p.Owner() {
			return nil
		}
		return &NotAllowedError{Org: "owner"}
	case needProject:
		if have := p.ProjectLevel(t.Project, now); have < need.level {
			return &NotAllowedError{Have: have, Need: need.level, Project: t.Project}
		}
		return nil
	}
	project, environment := t.Project, t.Environment
	if need.kind == needApp {
		project, environment = placeOfAppID(t.AppID)
	}
	if have := p.LevelAt(project, environment, now); have < need.level {
		return &NotAllowedError{Have: have, Need: need.level, Project: project, Environment: environment}
	}
	return nil
}
