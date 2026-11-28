package ui

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Task 2xg2: the console says where a service answers by name, and what stops a name from working.

// addressesEnv is configureEnv (shop: web with a port and worker without, so shop claims shop-web and
// shop) beside an app that claims shop-web too, and a one-service Postgres that nothing collides with.
func addressesEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := configureEnv(t)
	rival := app.App{ID: "shop-web", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{
		{Name: "api", Image: "ghcr.io/example/api:1", Ports: []app.Port{{Name: "http", ContainerPort: 9000}}}}}
	pg := app.App{ID: "postgres", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{
		{Name: "postgres", Image: "postgres:17", Ports: []app.Port{{Name: "db", ContainerPort: 5432}}}}}
	for _, a := range []app.App{rival, pg} {
		e.seed(t, a, seedDeploy{ID: "d-" + a.ID, Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	}
	return e
}

func TestConfigureOverviewSaysWhereServicesAnswer(t *testing.T) {
	e := addressesEnv(t)

	t.Run("a multi-service app with a conflict", func(t *testing.T) {
		resp, body := e.get(t, "/apps/shop/configure")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET = %d", resp.StatusCode)
		}
		page := text(body)
		for _, want := range []string{
			"Other apps in this environment reach it at shop:8080.",
			"Its own services reach it at web:8080.",
			"shop-web answers to nothing: app shop-web claims it too.",
		} {
			if !strings.Contains(page, want) {
				t.Errorf("Overview lacks %q:\n%s", want, page)
			}
		}
		if !strings.Contains(body, `<code class="cv-inline-mono cv-addr-name">shop:8080</code>`) ||
			!strings.Contains(body, `<a href="/apps/shop-web">shop-web</a>`) {
			t.Errorf("names aren't mono values or the other app isn't linked:\n%s", body)
		}
		// The worker has no port, so no address and no service label (one row only).
		if strings.Contains(page, "worker reach") || strings.Contains(page, "Other apps in this environment reach it at shop:8080, ") {
			t.Errorf("an address for a service without ports:\n%s", page)
		}
	})
	t.Run("the rival's names and the conflict from its side", func(t *testing.T) {
		_, body := e.get(t, "/apps/shop-web/configure")
		page := text(body)
		// shop-web's api claims shop-web-api and shop-web; shop's web claims shop-web: the lone name left is shop-web-api.
		if !strings.Contains(page, "reach it at shop-web-api:9000.") || !strings.Contains(page, "shop-web answers to nothing: app shop claims it too.") {
			t.Errorf("rival's Overview:\n%s", page)
		}
		if strings.Contains(page, "Its own services") {
			t.Errorf("a one-service app has no own-name line:\n%s", page)
		}
	})
	t.Run("a one-service app answers to its name", func(t *testing.T) {
		_, body := e.get(t, "/apps/postgres/configure")
		if page := text(body); !strings.Contains(page, "reach it at postgres-postgres:5432, postgres:5432.") ||
			strings.Contains(page, "answers to nothing") {
			t.Errorf("postgres Overview:\n%s", page)
		}
	})
}

// Every name colliding says so in words: a two-service app's "mix-web" and "mix-api" are both taken
// by one-service apps called mix-web and mix-api (an app's own bare name is claimed only by a lone
// service, so only this shape can lose every name).
func TestOverviewWhenEveryNameCollides(t *testing.T) {
	e := newConsoleEnv(t)
	port := []app.Port{{Name: "http", ContainerPort: 80}}
	for _, a := range []app.App{
		{ID: "mix", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{
			{Name: "web", Image: "ghcr.io/example/w:1", Ports: port}, {Name: "api", Image: "ghcr.io/example/a:1", Ports: port}}},
		{ID: "mix-web", Source: app.Source{Kind: app.SourceImage}, Services: []app.Service{{Name: "z", Image: "ghcr.io/example/z:1", Ports: port}}},
	} {
		e.seed(t, a, seedDeploy{ID: "d-" + a.ID, Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	}
	_, body := e.get(t, "/apps/mix/configure")
	page := text(body)
	for _, want := range []string{
		"web No other app can reach it by name until one of the apps below is renamed.",
		"web Its own services reach it at web:80.",
		"mix-web answers to nothing: app mix-web claims it too.",
		"api Other apps in this environment reach it at mix-api:80.",
		"api Its own services reach it at api:80.",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("Overview lacks %q:\n%s", want, page)
		}
	}
}

func TestRunningTabSaysWhereServicesAnswer(t *testing.T) {
	e := addressesEnv(t)
	for _, path := range []string{"/apps/shop", "/parts/apps/shop/copies"} {
		resp, body := e.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		page := text(body)
		for _, want := range []string{"Other apps in this environment reach it at shop:8080.", "shop-web answers to nothing: app shop-web claims it too."} {
			if !strings.Contains(page, want) {
				t.Errorf("%s lacks %q:\n%s", path, want, page)
			}
		}
	}
}

func TestAddrRowsFrom(t *testing.T) {
	rows := addrRowsFrom([]app.Address{
		{Service: "web", Ports: []int{8080, 9090}, Names: []string{"shop-web", "shop"}, Own: "web"},
		{Service: "db", Ports: []int{5432}, Names: []string{"shop-db"}, Own: "db"},
		{Service: "worker"}, // no ports: left out
	}, "Shop › production")
	if len(rows) != 2 || !rows[0].Many || rows[0].Own != "web:8080" || rows[0].Names[0] != "shop-web:8080" || rows[1].Names[0] != "shop-db:5432" {
		t.Errorf("rows = %+v", rows)
	}
	if addrRowsFrom(nil, "x") != nil {
		t.Error("no addresses should give no rows")
	}
}
