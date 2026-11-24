package service

import (
	"context"
	"testing"
	"time"
)

func TestLevelString(t *testing.T) {
	for l, want := range map[Level]string{LevelNone: "none", LevelView: "view", LevelDeploy: "deploy", LevelMaintain: "maintain", Level(99): "none"} {
		if got := l.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", l, got, want)
		}
	}
	if !(LevelNone < LevelView && LevelView < LevelDeploy && LevelDeploy < LevelMaintain) {
		t.Error("the levels are not in order")
	}
	for r, want := range map[Role]Level{RoleNone: LevelNone, RoleView: LevelView, RoleDeploy: LevelDeploy, RoleMaintain: LevelMaintain, "boss": LevelNone, "": LevelNone} {
		if got := r.Level(); got != want {
			t.Errorf("Role(%q).Level() = %v, want %v", r, got, want)
		}
	}
}

func TestPrincipalLevels(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)

	member := func(roles ...ProjectRole) Principal {
		return Principal{PersonID: "p", Email: "m@example.com", OrgRole: OrgMember, Roles: roles}
	}
	// The member of the task's story: deploy on staging, view on production, in shop.
	shop := ProjectRole{Project: "shop", Role: RoleView, Environments: map[string]Role{"staging": RoleDeploy}}

	tests := []struct {
		name         string
		p            Principal
		project      string
		env          string
		at           Level // LevelAt
		projectLevel Level // ProjectLevel
	}{
		{"owner anywhere", Principal{OrgRole: OrgOwner}, "shop", "production", LevelMaintain, LevelMaintain},
		{"admin anywhere", Principal{OrgRole: OrgAdmin}, "nope", "nope", LevelMaintain, LevelMaintain},
		{"owner ignores roles it has", Principal{OrgRole: OrgOwner, Roles: []ProjectRole{{Project: "shop", Role: RoleView}}}, "shop", "production", LevelMaintain, LevelMaintain},
		{"member without a role in the project", member(shop), "blog", "production", LevelNone, LevelNone},
		{"member with no roles at all", member(), "shop", "production", LevelNone, LevelNone},
		{"project role in an environment without an override", member(shop), "shop", "production", LevelView, LevelView},
		{"an override raises it", member(shop), "shop", "staging", LevelDeploy, LevelView},
		{"an override lowers it", member(ProjectRole{Project: "shop", Role: RoleMaintain, Environments: map[string]Role{"production": RoleView}}),
			"shop", "production", LevelView, LevelMaintain},
		{"none hides an environment", member(ProjectRole{Project: "shop", Role: RoleDeploy, Environments: map[string]Role{"production": RoleNone}}),
			"shop", "production", LevelNone, LevelDeploy},
		{"a role past its end counts as none", member(ProjectRole{Project: "shop", Role: RoleMaintain, EndsAt: &past}),
			"shop", "production", LevelNone, LevelNone},
		{"a role that ends later still counts", member(ProjectRole{Project: "shop", Role: RoleMaintain, EndsAt: &future}),
			"shop", "production", LevelMaintain, LevelMaintain},
		{"a role ending exactly now has ended", member(ProjectRole{Project: "shop", Role: RoleMaintain, EndsAt: &now}),
			"shop", "production", LevelNone, LevelNone},
		{"an ended role takes its overrides with it", member(ProjectRole{Project: "shop", Role: RoleView, EndsAt: &past, Environments: map[string]Role{"staging": RoleMaintain}}),
			"shop", "staging", LevelNone, LevelNone},
		{"a preview takes the project role, not an override", member(ProjectRole{Project: "shop", Role: RoleDeploy, Environments: map[string]Role{"pr-12": RoleNone}}),
			"shop", "pr-12", LevelDeploy, LevelDeploy},
		{"a name that only looks like a preview is an environment", member(ProjectRole{Project: "shop", Role: RoleDeploy, Environments: map[string]Role{"pr-012": RoleView}}),
			"shop", "pr-012", LevelView, LevelDeploy},
		{"roles of two projects don't mix", member(shop, ProjectRole{Project: "blog", Role: RoleMaintain}), "blog", "staging", LevelMaintain, LevelMaintain},
		{"an unknown role gives nothing", member(ProjectRole{Project: "shop", Role: "boss"}), "shop", "production", LevelNone, LevelNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.LevelAt(tt.project, tt.env, now); got != tt.at {
				t.Errorf("LevelAt(%q, %q) = %v, want %v", tt.project, tt.env, got, tt.at)
			}
			if got := tt.p.ProjectLevel(tt.project, now); got != tt.projectLevel {
				t.Errorf("ProjectLevel(%q) = %v, want %v", tt.project, got, tt.projectLevel)
			}
		})
	}
}

func TestPrincipalRoles(t *testing.T) {
	for role, want := range map[OrgRole][2]bool{OrgOwner: {true, true}, OrgAdmin: {false, true}, OrgMember: {false, false}, "": {false, false}} {
		p := Principal{OrgRole: role}
		if p.Owner() != want[0] || p.Admin() != want[1] {
			t.Errorf("%q: Owner() = %v, Admin() = %v; want %v", role, p.Owner(), p.Admin(), want)
		}
	}
}

func TestPrincipalInContext(t *testing.T) {
	if _, ok := PrincipalFrom(context.Background()); ok {
		t.Error("a context with no principal has one")
	}
	want := Principal{PersonID: "p1", Email: "a@example.com", OrgRole: OrgAdmin, TokenName: "ci"}
	got, ok := PrincipalFrom(WithPrincipal(context.Background(), want))
	if !ok || got.PersonID != want.PersonID || got.Email != want.Email || got.OrgRole != want.OrgRole || got.TokenName != want.TokenName {
		t.Errorf("PrincipalFrom = %+v, %v; want %+v", got, ok, want)
	}
}
