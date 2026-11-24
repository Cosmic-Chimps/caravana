package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// previewRefsEnv is project shop with a production web that references DATABASE_URL (a from) and
// API_KEY (inside a template) of the provider bella-prod, plus an open preview pr-1.
func previewRefsEnv(t *testing.T) *env {
	t.Helper()
	e, src := refEnv(t)
	src.Set("OTHER", "o1")
	base := withSecrets(fromBella("DB", "DATABASE_URL"),
		templated("CONN", "key=${k}", map[string]string{"k": "API_KEY"}))
	base.ID = productionID
	base.Services[0].Domains = nil
	deployHealthy(t, e, base)
	if _, err := e.svc.CreateEnvironment(context.Background(), "shop", "pr-1"); err != nil {
		t.Fatal(err)
	}
	return e
}

// previewApp is the preview's copy of the base app with secrets in place of its own.
func previewApp(list ...app.Secret) app.App {
	a := withSecrets(list...)
	a.ID = "shop--pr-1--web"
	a.Services[0].Domains = nil
	return a
}

// A deploy-level person, a deploy token and a maintainer define apps in a preview (feature 054,
// plan D12): below maintain, only with the secret references the base app already has.
func TestPreviewSecretReferences(t *testing.T) {
	e := previewRefsEnv(t)
	deployer := Principal{PersonID: "ci", Email: "ci@example.com", OrgRole: OrgMember,
		Roles: []ProjectRole{{Project: "shop", Role: RoleDeploy}}}
	maintainer := Principal{PersonID: "mo", Email: "mo@example.com", OrgRole: OrgMember,
		Roles: []ProjectRole{{Project: "shop", Role: RoleMaintain}}}
	// CI's token: made by a person (here an owner), limited to shop's web.
	token := Principal{PersonID: "own", Email: "own@example.com", OrgRole: OrgOwner, TokenName: "ci",
		Scope: &TokenScope{Project: "shop", App: "web"}}
	deployToken := Principal{PersonID: "ci", Email: "ci@example.com", OrgRole: OrgMember, TokenName: "ci",
		Roles: []ProjectRole{{Project: "shop", Role: RoleDeploy}}, Scope: &TokenScope{Project: "shop", App: "web"}}
	owner := Principal{PersonID: "own", Email: "own@example.com", OrgRole: OrgOwner}

	same := previewApp(fromBella("DB", "DATABASE_URL"), templated("CONN", "key=${k}", map[string]string{"k": "API_KEY"}))
	subset := previewApp(fromBella("DB", "DATABASE_URL"))
	typedOnly := previewApp(set("TYPED", "t"))
	none := previewApp()
	newFrom := previewApp(fromBella("DB", "DATABASE_URL"), fromBella("EXTRA", "OTHER"))
	samePathNewName := previewApp(fromBella("DB2", "DATABASE_URL"))
	newTemplatePath := previewApp(templated("CONN", "key=${k}", map[string]string{"k": "OTHER"}))
	otherProvider := previewApp(app.Secret{Name: "DB", From: ref("bella-staging", "DATABASE_URL")})

	cases := []struct {
		name      string
		who       *Principal
		a         app.App
		wantField string // "" means allowed
	}{
		{"references equal to the base app's", &deployer, same, ""},
		{"a subset of them", &deployer, subset, ""},
		{"no references, typed values only", &deployer, typedOnly, ""},
		{"no secrets at all", &deployer, none, ""},
		{"the same path under another secret name", &deployer, samePathNewName, ""},
		{"a new from", &deployer, newFrom, "services[0].secrets[1].from"},
		{"a template ref to a new path", &deployer, newTemplatePath, "services[0].secrets[0].refs.k"},
		{"the provider's other path through a template", &deployer, previewApp(
			fromBella("DB", "DATABASE_URL"), templated("CONN", "${pw}-${k}", map[string]string{"pw": "OTHER", "k": "API_KEY"})),
			"services[0].secrets[1].refs.pw"},
		{"the same path of another provider", &deployer, otherProvider, "services[0].secrets[0].from"},
		{"a maintainer may add a reference", &maintainer, newFrom, ""},
		{"an owner may add a reference", &owner, newFrom, ""},
		{"a deploy token's normal copy of the base app", &deployToken, same, ""},
		{"a deploy token with a new from", &deployToken, newFrom, "services[0].secrets[1].from"},
		{"an owner's deploy token with a new from", &token, newFrom, "services[0].secrets[1].from"},
		{"an owner's deploy token with a template ref to a new path", &token, newTemplatePath, "services[0].secrets[0].refs.k"},
		{"Caravana itself", nil, newFrom, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.who != nil {
				ctx = WithPrincipal(ctx, *c.who)
			}
			err := e.svc.allowPreviewReferences(ctx, c.a)
			if c.wantField == "" {
				if err != nil {
					t.Fatalf("allowPreviewReferences = %v, want allowed", err)
				}
				return
			}
			var na *NotAllowedError
			if !errors.As(err, &na) || !errors.Is(err, ErrNotAllowed) {
				t.Fatalf("allowPreviewReferences = %v, want a *NotAllowedError", err)
			}
			if na.Field != c.wantField || na.Need != LevelMaintain || na.Have != LevelDeploy || na.Project != "shop" || na.Environment != "pr-1" {
				t.Errorf("refusal = %+v, want deploy→maintain at shop › pr-1 on %s", *na, c.wantField)
			}
			if !strings.Contains(na.Error(), "secret references its base app already has") || !strings.Contains(na.Error(), c.wantField) {
				t.Errorf("message = %q, want the sentence and the field", na.Error())
			}
		})
	}

	t.Run("outside a preview the rule is not asked", func(t *testing.T) {
		a := newFrom
		a.ID = "shop--staging--web"
		if err := e.svc.allowPreviewReferences(WithPrincipal(context.Background(), deployer), a); err != nil {
			t.Errorf("allowPreviewReferences in staging = %v, want nil (the level rules decide there)", err)
		}
	})

	t.Run("an app with no base app has no references to use", func(t *testing.T) {
		a := previewApp(fromBella("DB", "DATABASE_URL"))
		a.ID = "shop--pr-1--worker"
		err := e.svc.allowPreviewReferences(WithPrincipal(context.Background(), deployer), a)
		if !errors.Is(err, ErrNotAllowed) {
			t.Errorf("allowPreviewReferences for an app with no base = %v, want not allowed", err)
		}
		a.Services[0].Secrets = nil
		if err := e.svc.allowPreviewReferences(WithPrincipal(context.Background(), deployer), a); err != nil {
			t.Errorf("without references = %v, want allowed", err)
		}
	})
}

// The rule is asked wherever a deploy-level person defines a preview app.
func TestPreviewSecretReferencesThroughTheEntryPoints(t *testing.T) {
	ctx := context.Background()
	e := previewRefsEnv(t)
	deployer := WithPrincipal(ctx, Principal{PersonID: "ci", Email: "ci@example.com", OrgRole: OrgMember,
		Roles: []ProjectRole{{Project: "shop", Role: RoleDeploy}}})
	bad := previewApp(fromBella("DB", "DATABASE_URL"), fromBella("EXTRA", "OTHER"))

	if _, err := e.svc.Deploy(deployer, bad, app.TriggerManual); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("Deploy of a new preview app with a new reference = %v, want not allowed", err)
	}
	if _, err := e.svc.CreateApp(deployer, bad, app.TriggerManual); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("CreateApp = %v, want not allowed", err)
	}
	if _, err := e.svc.CreateAppOnly(deployer, bad); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("CreateAppOnly = %v, want not allowed", err)
	}
	if _, err := e.svc.GetApp(ctx, bad.ID); err == nil {
		t.Error("a refused preview app was stored")
	}

	// CI's normal deploy: a copy of the base app. Then a changed definition that adds a reference.
	good := previewApp(fromBella("DB", "DATABASE_URL"), templated("CONN", "key=${k}", map[string]string{"k": "API_KEY"}))
	rec, err := e.svc.Deploy(deployer, good, app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy of CI's copy of the base app = %v, want allowed", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	before, err := e.svc.GetApp(ctx, good.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Deploy(deployer, bad, app.TriggerManual); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("Deploy that adds a reference to an existing preview app = %v, want not allowed", err)
	}
	if after, _ := e.svc.GetApp(ctx, good.ID); after.Revision != before.Revision {
		t.Error("the refused deploy changed the stored preview app")
	}

	// A copy of the base app into another preview keeps its references, which are the base's.
	if _, err := e.svc.CreateEnvironment(ctx, "shop", "pr-2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.CopyApp(deployer, productionID, "pr-2"); err != nil {
		t.Errorf("CopyApp of the base app into a preview = %v, want allowed", err)
	}
}
