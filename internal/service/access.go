package service

import (
	"context"
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
