package ui

import (
	"context"
	"net/url"
	"reflect"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// Spec 038: the Compose form has no input for workload identities, so a deploy
// from it keeps the ones the app has, in the kept overlay and on the app.

var shopIdentities = []app.Identity{
	{Name: "bella", Audience: []string{"bella-spiffe/env-1"}},
	{Name: "billing", Audience: []string{"billing", "audit"}, TTLSeconds: 120},
}

func identityShopRequest() compose.Request {
	req := shopRequest()
	req.Compose = keptShopFile
	ov := req.Services["web"]
	ov.Identities = shopIdentities
	req.Services["web"] = ov
	return req
}

func webService(t *testing.T, a app.App) app.Service {
	t.Helper()
	for _, svc := range a.Services {
		if svc.Name == "web" {
			return svc
		}
	}
	t.Fatal("no web service")
	return app.Service{}
}

func TestKeptFileDeployKeepsIdentities(t *testing.T) {
	e, _ := deployShop(t, identityShopRequest())
	_, page := e.get(t, "/apps/shop/configure")
	resp, body := e.postForm(t, "/apps/shop/compose", shopFormOf(t, page, url.Values{"compose": {"# edited\n" + keptShopFile}}))
	if resp.StatusCode != 303 {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)

	rec, err := e.Store.GetApp(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Compose.Services["web"].Identities; !reflect.DeepEqual(got, shopIdentities) {
		t.Errorf("kept overlay identities = %+v, want %+v", got, shopIdentities)
	}
	if got := webService(t, rec.App).Identities; !reflect.DeepEqual(got, shopIdentities) {
		t.Errorf("app identities = %+v, want %+v", got, shopIdentities)
	}
}

// An app whose file isn't kept keeps its identities through the form as well,
// read from the stored app.
func TestNotKeptFileDeployKeepsIdentities(t *testing.T) {
	e, _ := deployShop(t, identityShopRequest())
	ctx := context.Background()
	stored, err := e.Store.GetApp(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := e.Store.SaveApp(ctx, stored.App, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, body := e.postForm(t, "/apps/shop/compose", url.Values{"revision": {legacy.Revision}, "compose": {shopCompose},
		"svc.web.overlay": {"1"}, "svc.web.healthPath": {"/health"}, "svc.db.overlay": {"1"}})
	if resp.StatusCode != 303 {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)
	if got := webService(t, e.storedApp(t, "shop")).Identities; !reflect.DeepEqual(got, shopIdentities) {
		t.Errorf("app identities = %+v, want %+v", got, shopIdentities)
	}
}

// The draft sends the identities it keeps, even for a service with no other
// overlay, and a new app's draft sends none.
func TestComposeDraftIdentities(t *testing.T) {
	d, ok := parseComposeDraft(url.Values{"compose": {shopCompose}, "svc.worker.overlay": {"1"}})
	if !ok {
		t.Fatal("the draft was refused")
	}
	if _, has := d.request().Services["worker"]; has {
		t.Error("a new app's draft sends an overlay for a service with nothing set")
	}
	d.keptIdentities = map[string][]app.Identity{"worker": shopIdentities}
	if got := d.request().Services["worker"].Identities; !reflect.DeepEqual(got, shopIdentities) {
		t.Errorf("an existing app's identities = %+v, want the kept ones", got)
	}
}
