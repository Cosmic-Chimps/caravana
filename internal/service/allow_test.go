package service

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// shopMember is the member of the task's story: view on production, deploy on staging, in shop.
func shopMember() Principal {
	return Principal{PersonID: "mia", Email: "mia@example.com", OrgRole: OrgMember, Roles: []ProjectRole{
		{Project: "shop", Role: RoleView, Environments: map[string]Role{"staging": RoleDeploy}},
	}}
}

func asPerson(p Principal) context.Context { return WithPrincipal(context.Background(), p) }

func TestPrincipalAllow(t *testing.T) {
	e := newEnv(t)
	now := e.svc.clock.Now()
	owner := Principal{OrgRole: OrgOwner}
	admin := Principal{OrgRole: OrgAdmin}
	member := shopMember()

	tests := []struct {
		name   string
		p      Principal
		need   Need
		target Target
		want   string // the refusal's words, "" for allowed
	}{
		{"anyone signed in", member, NeedSignedIn(), Target{}, ""},
		{"admin work for an owner", owner, NeedAdmin(), Target{}, ""},
		{"admin work for an admin", admin, NeedAdmin(), Target{}, ""},
		{"admin work for a member", member, NeedAdmin(), Target{}, "this needs an owner or an admin"},
		{"owner work for an owner", owner, NeedOwner(), Target{}, ""},
		{"owner work for an admin", admin, NeedOwner(), Target{}, "this needs an owner"},
		{"owner work for a member", member, NeedOwner(), Target{}, "this needs an owner"},

		{"deploy on staging with deploy", member, NeedApp(LevelDeploy), Target{AppID: "shop--staging--web"}, ""},
		{"view on staging with deploy", member, NeedApp(LevelView), Target{AppID: "shop--staging--web"}, ""},
		{"maintain on staging with deploy", member, NeedApp(LevelMaintain), Target{AppID: "shop--staging--web"},
			"your role in shop › staging is deploy; this needs maintain"},
		{"deploy on production with view", member, NeedApp(LevelDeploy), Target{AppID: "shop--production--web"},
			"your role in shop › production is view; this needs deploy"},
		{"view on production with view", member, NeedApp(LevelView), Target{AppID: "shop--production--web"}, ""},
		{"an app of another project", member, NeedApp(LevelView), Target{AppID: "blog--production--web"},
			"your role in blog › production is none; this needs view"},
		{"an app that doesn't exist is judged by its ID", member, NeedApp(LevelDeploy), Target{AppID: "shop--staging--nothing-yet"}, ""},
		{"an app that doesn't exist, in a place with no role", member, NeedApp(LevelView), Target{AppID: "blog--staging--nothing-yet"},
			"your role in blog › staging is none; this needs view"},
		{"a bare name is default › production", member, NeedApp(LevelView), Target{AppID: "whoami"},
			"your role in default › production is none; this needs view"},
		{"an ID PlaceOf refuses is default › production too", member, NeedApp(LevelView), Target{AppID: "old--style--id--x"},
			"your role in default › production is none; this needs view"},
		{"an owner anywhere", owner, NeedApp(LevelMaintain), Target{AppID: "blog--production--web"}, ""},
		{"an admin anywhere", admin, NeedApp(LevelMaintain), Target{AppID: "anything"}, ""},

		{"a place", member, NeedPlace(LevelDeploy), Target{Project: "shop", Environment: "staging"}, ""},
		{"a place too high", member, NeedPlace(LevelMaintain), Target{Project: "shop", Environment: "production"},
			"your role in shop › production is view; this needs maintain"},
		{"the project's own role is the project's", member, NeedProject(LevelView), Target{Project: "shop"}, ""},
		{"the project's own role, too low", member, NeedProject(LevelDeploy), Target{Project: "shop"},
			"your role in shop is view; this needs deploy"},
		{"a project with no role", member, NeedProject(LevelView), Target{Project: "blog"},
			"your role in blog is none; this needs view"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := e.svc.Allow(asPerson(tt.p), tt.need, tt.target)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Allow = %v, want allowed", err)
				}
				return
			}
			var na *NotAllowedError
			if !errors.As(err, &na) || !errors.Is(err, ErrNotAllowed) {
				t.Fatalf("Allow = %v, want a *NotAllowedError wrapping ErrNotAllowed", err)
			}
			if err.Error() != tt.want {
				t.Errorf("message = %q, want %q", err.Error(), tt.want)
			}
			// The pure function says the same.
			if err2 := tt.p.Allow(tt.need, tt.target, now); err2 == nil || err2.Error() != tt.want {
				t.Errorf("Principal.Allow = %v, want the same refusal", err2)
			}
		})
	}

	t.Run("no principal is Caravana itself", func(t *testing.T) {
		for _, need := range []Need{NeedAdmin(), NeedOwner(), NeedApp(LevelMaintain), NeedPlace(LevelMaintain), NeedProject(LevelMaintain)} {
			if err := e.svc.Allow(context.Background(), need, Target{AppID: "x--y--z", Project: "x", Environment: "y"}); err != nil {
				t.Errorf("Allow without a principal = %v, want allowed", err)
			}
		}
	})

	t.Run("the refusal says the place, the levels and the field", func(t *testing.T) {
		err := e.svc.Allow(asPerson(member), NeedApp(LevelMaintain), Target{AppID: "shop--staging--web"})
		var na *NotAllowedError
		if !errors.As(err, &na) || na.Have != LevelDeploy || na.Need != LevelMaintain || na.Project != "shop" || na.Environment != "staging" || na.Field != "" {
			t.Errorf("refusal = %+v", na)
		}
		with := &NotAllowedError{Have: LevelDeploy, Need: LevelMaintain, Project: "shop", Environment: "staging", Field: "services[0].env"}
		if got, want := with.Error(), "your role in shop › staging is deploy; this needs maintain to change services[0].env"; got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	})

	t.Run("a role past its end is none", func(t *testing.T) {
		ended := shopMember()
		past := now.Add(-1)
		ended.Roles[0].EndsAt = &past
		err := e.svc.Allow(asPerson(ended), NeedApp(LevelView), Target{AppID: "shop--staging--web"})
		if err == nil || err.Error() != "your role in shop › staging is none; this needs view" {
			t.Errorf("Allow with an ended role = %v", err)
		}
	})
}

func TestVisibility(t *testing.T) {
	e := newEnv(t)
	v := e.svc.visible(asPerson(shopMember()))
	for _, tt := range []struct {
		project, env string
		want         bool
	}{{"shop", "staging", true}, {"shop", "production", true}, {"shop", "qa", true}, {"blog", "production", false}} {
		if got := v.place(tt.project, tt.env); got != tt.want {
			t.Errorf("place(%s, %s) = %v, want %v", tt.project, tt.env, got, tt.want)
		}
	}
	if !v.app("shop--staging--web") || v.app("blog--production--web") || v.app("bare-name") {
		t.Error("app() doesn't follow the ID's place")
	}
	if v.installation() {
		t.Error("a member sees the installation's items")
	}
	for _, p := range []Principal{{OrgRole: OrgOwner}, {OrgRole: OrgAdmin}} {
		v := e.svc.visible(asPerson(p))
		if !v.installation() || !v.place("anything", "at-all") || !v.app("whatever") {
			t.Errorf("%s doesn't see everything", p.OrgRole)
		}
	}
	if v := e.svc.visible(context.Background()); !v.installation() || !v.place("a", "b") {
		t.Error("Caravana itself doesn't see everything")
	}
}

func TestAttentionItemVisibility(t *testing.T) {
	e := newEnv(t)
	v := e.svc.visible(asPerson(shopMember()))
	items := []struct {
		name string
		item AttentionItem
		want bool
	}{
		{"an app of the member's place", AttentionItem{Kind: AttentionDeployFailed, AppID: "shop--staging--web", Project: "shop", Environment: "staging"}, true},
		{"an app of another project", AttentionItem{Kind: AttentionDeployFailed, AppID: "blog--production--web", Project: "blog", Environment: "production"}, false},
		{"an item with an ID only", AttentionItem{Kind: AttentionUnhealthy, AppID: "blog--production--web"}, false},
		{"an item with an ID only, the member's", AttentionItem{Kind: AttentionUnhealthy, AppID: "shop--production--web"}, true},
		{"no backup destination", AttentionItem{Kind: AttentionNoBackupDestination}, false},
		{"a disk", AttentionItem{Kind: AttentionDiskNearlyFull, Mount: "/"}, false},
		{"a server", AttentionItem{Kind: AttentionServerDown, Server: "two"}, false},
		{"the host's updates", AttentionItem{Kind: AttentionOSSecurityUpdates}, false},
		{"Caravana's own update", AttentionItem{Kind: AttentionCaravanaUpdateFailed}, false},
	}
	admin := e.svc.visible(asPerson(Principal{OrgRole: OrgAdmin}))
	for _, tt := range items {
		if got := v.attentionItem(tt.item); got != tt.want {
			t.Errorf("%s: member sees = %v, want %v", tt.name, got, tt.want)
		}
		if !admin.attentionItem(tt.item) {
			t.Errorf("%s: an admin doesn't see it", tt.name)
		}
	}
}

// ids is the app IDs of summaries, sorted.
func summaryIDs(sums []AppSummary) []string {
	var out []string
	for _, s := range sums {
		out = append(out, s.App.ID)
	}
	slices.Sort(out)
	return out
}
