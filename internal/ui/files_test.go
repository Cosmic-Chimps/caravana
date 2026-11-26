package ui

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// Spec 060: the Compose form has no input for files, so a deploy from it keeps the overlay's, and no page
// draws a secret file's value.

const sentinel060 = "s3cret-sentinel-060-key"

var shopFiles = []app.File{
	{Path: "/etc/shop/shop.yml", Content: "public: true\n", Mode: "0640", UID: new(1000)},
	{Path: "/certs/api.key", Secret: "API_KEY", Mode: "0400"},
}

func fileShopRequest() compose.Request {
	req := shopRequest()
	req.Compose = keptShopFile
	ov := req.Services["web"]
	ov.Secrets = []app.Secret{{Name: "API_KEY", Value: new(sentinel060)}}
	ov.Files = shopFiles
	req.Services["web"] = ov
	return req
}

func TestKeptFileDeployKeepsFiles(t *testing.T) {
	e, _ := deployShop(t, fileShopRequest())
	_, page := e.get(t, "/apps/shop/configure/source")
	resp, body := e.postForm(t, "/apps/shop/compose", shopFormOf(t, page, url.Values{"compose": {"# edited\n" + keptShopFile}}))
	if resp.StatusCode != 303 {
		t.Fatalf("deploy = %d:\n%s", resp.StatusCode, text(body))
	}
	e.waitDeploy(t, e.deployments(t, "shop")[0].Deployment.ID)

	rec, err := e.Store.GetApp(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Compose.Services["web"].Files; !reflect.DeepEqual(got, shopFiles) {
		t.Errorf("kept overlay files = %+v, want %+v", got, shopFiles)
	}
	if got := webService(t, rec.App).Files; !reflect.DeepEqual(got, shopFiles) {
		t.Errorf("app files = %+v, want %+v", got, shopFiles)
	}
	// The secret's value is nowhere in what the store keeps of the app.
	stored, _ := json.Marshal(rec)
	if strings.Contains(string(stored), sentinel060) {
		t.Errorf("the stored app holds the secret file's value:\n%s", stored)
	}
}

// An app whose file isn't kept keeps its files through the form as well, read from the stored app.
func TestNotKeptFileDeployKeepsFiles(t *testing.T) {
	e, _ := deployShop(t, fileShopRequest())
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
	if got := webService(t, e.storedApp(t, "shop")).Files; !reflect.DeepEqual(got, shopFiles) {
		t.Errorf("app files = %+v, want %+v", got, shopFiles)
	}
}

// The draft sends the files it keeps, even for a service with no other overlay, and a new app's draft sends none.
func TestComposeDraftFiles(t *testing.T) {
	d, ok := parseComposeDraft(url.Values{"compose": {shopCompose}, "svc.worker.overlay": {"1"}})
	if !ok {
		t.Fatal("the draft was refused")
	}
	if _, has := d.request().Services["worker"]; has {
		t.Error("a new app's draft sends an overlay for a service with nothing set")
	}
	d.keptFiles = map[string][]app.File{"worker": shopFiles}
	if got := d.request().Services["worker"].Files; !reflect.DeepEqual(got, shopFiles) {
		t.Errorf("an existing app's files = %+v, want the kept ones", got)
	}
}

// No page draws a secret file's value, and the Variables page says where the secret is written.
func TestNoPageDrawsASecretFile(t *testing.T) {
	e, _ := deployShop(t, fileShopRequest())
	for _, path := range []string{"/apps/shop", "/apps/shop/configure", "/apps/shop/configure/source", "/apps/shop/configure/services",
		"/apps/shop/variables", "/apps/shop/domains", "/apps/shop/configure/integrations", "/apps/shop/deployments", "/apps"} {
		resp, page := e.get(t, path)
		if resp.StatusCode != 200 {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
		if strings.Contains(page, sentinel060) {
			t.Errorf("%s draws the secret file's value", path)
		}
	}
	_, vars := e.get(t, "/apps/shop/variables")
	if !strings.Contains(text(vars), "Written to /certs/api.key, not to the environment.") {
		t.Errorf("Variables doesn't say where API_KEY is written:\n%s", text(vars))
	}
	_, services := e.get(t, "/apps/shop/configure/services")
	for _, want := range []string{"/etc/shop/shop.yml · mode 0640 · 13 B", "/certs/api.key · mode 0400 · from secret API_KEY"} {
		if n := strings.Count(text(services), want); n != 1 {
			t.Errorf("Services has %q %d times, want once:\n%s", want, n, text(services))
		}
	}
}
