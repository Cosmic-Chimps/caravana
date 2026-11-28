package ui

import (
	"net/http"
	"regexp"
	"slices"
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

// rowText is a row's sentences as text, one per line, the way the page reads them.
func rowText(r addrRow) []string {
	var out []string
	for _, line := range r.Lines {
		var b strings.Builder
		for _, p := range line {
			b.WriteString(p.Text)
		}
		out = append(out, b.String())
	}
	return out
}

func TestConfigureOverviewSaysWhereServicesAnswer(t *testing.T) {
	e := addressesEnv(t)

	t.Run("a multi-service app with a conflict: the sentences sit in the service's own row", func(t *testing.T) {
		resp, body := e.get(t, "/apps/shop/configure")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET = %d", resp.StatusCode)
		}
		rows := regexp.MustCompile(`(?s)<li class="cv-ovsvc">.*?</li>`).FindAllString(body, -1)
		if len(rows) != 2 {
			t.Fatalf("%d service rows, want 2:\n%s", len(rows), body)
		}
		web, worker := text(rows[0]), text(rows[1])
		for _, want := range []string{
			"Other apps in this environment reach it at shop:8080.",
			"The other services of this app reach it at web:8080.",
			"shop-web reaches neither app: it is also the name of the app shop-web.",
		} {
			if !strings.Contains(web, want) {
				t.Errorf("the web row lacks %q:\n%s", want, web)
			}
		}
		if strings.Contains(worker, "reach it") || strings.Contains(rows[1], "cv-ovsvc-addr") {
			t.Errorf("a service without ports has names:\n%s", worker)
		}
		// The row names the service, so the sentences carry no name of their own, and nothing is drawn outside the rows.
		if strings.Contains(rows[0], "cv-addr-svc") || strings.Count(body, `class="cv-addr"`) != 1 {
			t.Errorf("a service named twice, or a block outside its row:\n%s", body)
		}
		if !strings.Contains(rows[0], `<code class="cv-inline-mono cv-addr-name">shop:8080</code>`) ||
			!strings.Contains(rows[0], `<a href="/apps/shop-web">shop-web</a>`) ||
			!strings.Contains(rows[0], `<code class="cv-inline-mono">shop-web</code>`) {
			t.Errorf("a working name isn't selectable, a colliding one is, or the app isn't linked:\n%s", rows[0])
		}
	})
	t.Run("the other claimant is a service of another app", func(t *testing.T) {
		_, body := e.get(t, "/apps/shop-web/configure")
		page := text(body)
		if !strings.Contains(page, "Other apps in this environment reach it at shop-web-api:9000.") ||
			!strings.Contains(page, "shop-web reaches neither app: it is also the name of web in shop.") {
			t.Errorf("rival's Overview:\n%s", page)
		}
		if strings.Contains(page, "The other services") {
			t.Errorf("a one-service app has no own-name line:\n%s", page)
		}
	})
	t.Run("a one-service app answers to its name, shortest first", func(t *testing.T) {
		_, body := e.get(t, "/apps/postgres/configure")
		if page := text(body); !strings.Contains(page, "reach it at postgres:5432 or postgres-postgres:5432.") ||
			strings.Contains(page, "reaches neither") {
			t.Errorf("postgres Overview:\n%s", page)
		}
	})
}

// Every name colliding is one sentence: a two-service app's "mix-web" and "mix-api" are both taken by
// one-service apps called mix-web and mix-api (an app's own bare name is claimed only by a lone
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
	rows := regexp.MustCompile(`(?s)<li class="cv-ovsvc">.*?</li>`).FindAllString(body, -1)
	if len(rows) != 2 {
		t.Fatalf("%d service rows:\n%s", len(rows), body)
	}
	web, api := text(rows[0]), text(rows[1])
	for _, want := range []string{
		"Other apps in this environment can't reach it by name: mix-web is also the name of the app mix-web. Rename one of the two apps to free it.",
		"The other services of this app reach it at web:80.",
	} {
		if !strings.Contains(web, want) {
			t.Errorf("the web row lacks %q:\n%s", want, web)
		}
	}
	if strings.Contains(web, "below") || strings.Contains(web, "reaches neither") {
		t.Errorf("the web row says it twice:\n%s", web)
	}
	for _, want := range []string{"Other apps in this environment reach it at mix-api:80.", "The other services of this app reach it at api:80."} {
		if !strings.Contains(api, want) {
			t.Errorf("the api row lacks %q:\n%s", want, api)
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
		for _, want := range []string{"Other apps in this environment reach it at shop:8080.",
			"shop-web reaches neither app: it is also the name of the app shop-web."} {
			if !strings.Contains(page, want) {
				t.Errorf("%s lacks %q:\n%s", path, want, page)
			}
		}
		// The block follows the table, in the live part, and shop has one service with ports: no name line.
		if strings.Index(body, `class="cv-copies-addr"`) < strings.Index(body, `aria-label="Copies"`) || strings.Contains(body, "cv-addr-svc") {
			t.Errorf("%s: the block isn't below the table, or names the only service:\n%s", path, body)
		}
	}
}

func TestAddrRowsFrom(t *testing.T) {
	place := "Shop › production"
	t.Run("a multi-service app", func(t *testing.T) {
		rows := addrRowsFrom([]app.Address{
			{Service: "web", Ports: []int{8080, 9090}, Names: []string{"shop-web", "shop"}, Own: "web"},
			{Service: "db", Ports: []int{5432}, Names: []string{"shop-db"}, Own: "db"},
			{Service: "worker"}, // no ports: left out
		}, place)
		if len(rows) != 2 || !rows[0].Many || !rows[1].Many {
			t.Fatalf("rows = %+v", rows)
		}
		got := rowText(rows[0])
		want := []string{"Other apps in Shop › production reach it at shop:8080 or shop-web:8080.", "The other services of this app reach it at web:8080."}
		if !slices.Equal(got, want) {
			t.Errorf("web = %q, want %q", got, want)
		}
	})
	t.Run("no addresses", func(t *testing.T) {
		if addrRowsFrom(nil, "x") != nil {
			t.Error("no addresses should give no rows")
		}
	})
	t.Run("a name another service of the app's own name", func(t *testing.T) {
		rows := addrRowsFrom([]app.Address{{Service: "postgres", Ports: []int{5432}, Names: []string{"postgres"},
			Conflicts: []app.NameConflict{{Name: "postgres-postgres", App: "postgres-postgres", Service: "x"}}}}, place)
		want := []string{"Other apps in Shop › production reach it at postgres:5432.",
			"postgres-postgres reaches neither app: it is also the name of the app postgres-postgres."}
		if got := rowText(rows[0]); !slices.Equal(got, want) {
			t.Errorf("= %q, want %q", got, want)
		}
	})
	t.Run("a name that is a service of another app", func(t *testing.T) {
		rows := addrRowsFrom([]app.Address{{Service: "postgres", Ports: []int{5432}, Names: []string{"postgres"},
			Conflicts: []app.NameConflict{{Name: "postgres-postgres", App: "hollo", Service: "postgres"}}}}, place)
		want := "postgres-postgres reaches neither app: it is also the name of postgres in hollo."
		if got := rowText(rows[0]); got[1] != want {
			t.Errorf("= %q, want %q", got[1], want)
		}
		if l := rows[0].Lines[1]; l[len(l)-2].Href != "/apps/hollo" {
			t.Errorf("the app isn't linked: %+v", l)
		}
	})
	t.Run("every name collides, one pair", func(t *testing.T) {
		rows := addrRowsFrom([]app.Address{{Service: "web", Ports: []int{80}, Own: "web",
			Conflicts: []app.NameConflict{{Name: "hollo-web", App: "hollo-web", Service: "z"}}}}, place)
		want := []string{"Other apps in Shop › production can't reach it by name: hollo-web is also the name of the app hollo-web. Rename one of the two apps to free it.",
			"The other services of this app reach it at web:80."}
		if got := rowText(rows[0]); !slices.Equal(got, want) {
			t.Errorf("= %q, want %q", got, want)
		}
	})
	t.Run("every name collides, two names", func(t *testing.T) {
		rows := addrRowsFrom([]app.Address{{Service: "web", Ports: []int{80},
			Conflicts: []app.NameConflict{{Name: "a-web", App: "a-web", Service: "z"}, {Name: "a-web2", App: "a", Service: "web2"}}}}, place)
		want := "Other apps in Shop › production can't reach it by name: a-web is also the name of the app a-web; a-web2 is also the name of web2 in a. Renaming one app of each pair frees that name."
		if got := rowText(rows[0]); len(got) != 1 || got[0] != want {
			t.Errorf("= %q, want %q", got, want)
		}
	})
	t.Run("three claimants of one name", func(t *testing.T) {
		rows := addrRowsFrom([]app.Address{{Service: "postgres", Ports: []int{5432}, Names: []string{"postgres"},
			Conflicts: []app.NameConflict{{Name: "postgres-postgres", App: "postgres-postgres", Service: "x"}, {Name: "postgres-postgres", App: "hollo", Service: "postgres"}}}}, place)
		want := "postgres-postgres reaches none of them: it is also the name of the app postgres-postgres and postgres in hollo."
		if got := rowText(rows[0]); got[1] != want {
			t.Errorf("= %q, want %q", got[1], want)
		}
	})
	t.Run("every name collides with several apps", func(t *testing.T) {
		rows := addrRowsFrom([]app.Address{{Service: "web", Ports: []int{80},
			Conflicts: []app.NameConflict{{Name: "a-web", App: "a-web", Service: "z"}, {Name: "a-web", App: "b", Service: "web"}}}}, place)
		want := "Other apps in Shop › production can't reach it by name: a-web is also the name of the app a-web and web in b. Rename all but one of these apps to free it."
		if got := rowText(rows[0]); got[0] != want {
			t.Errorf("= %q, want %q", got[0], want)
		}
	})
}
