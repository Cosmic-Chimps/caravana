package ui

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Feature 023: the Options form of a domain's row (Team only, the www redirect, Path), Remove… and
// New password, on /domains and on the app's page.

// optionsEnv has app "shop" with three domains on one service (a www host, its apex and a
// subdomain) and app "other", which has "taken.example.org".
func optionsEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnv(t)
	e.deploy(t, domainsApp("shop", "www.example.com", "example.net", "app.example.com"))
	e.deploy(t, domainsApp("other", "taken.example.org"))
	return e
}

// optionsOf is a post to a row's Options with what its form would carry.
func optionsOf(e *consoleEnv, t *testing.T, app, service string, extra url.Values) url.Values {
	t.Helper()
	form := url.Values{"app": {app}, "service": {service}, "revision": {e.revisionOf(t, app)}}
	for k, v := range extra {
		form[k] = v
	}
	return form
}

// passwordRe finds the password in the shown-once panel.
var passwordRe = regexp.MustCompile(`<code class="cv-once-value" id="team-password-value">([^<]+)</code>`)

func TestDomainOptionsForm(t *testing.T) {
	e := optionsEnv(t)
	body := e.pageOf(t, "/domains")

	row := dmRowOf(body, "www.example.com")
	got := text(row)
	for _, want := range []string{"Options", "Force HTTPS", "On for every domain.", "Team only (a shared password)", "Redirect example.com here", "Path",
		"Only requests under this path reach the app; the path is kept.", "Save and deploy", "Remove…"} {
		if !strings.Contains(got, want) {
			t.Errorf("a www host's Options lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "New password") {
		t.Error("a domain that isn't Team only offers a new password")
	}
	// The form posts to the row's own address with the app, the service and the page's revision; Force HTTPS is
	// on and locked, and isn't a field.
	form := formWith(t, row, "/domains/www.example.com/options")
	if form.Values.Get("app") != "shop" || form.Values.Get("service") != "web" || form.Values.Get("revision") != e.revisionOf(t, "shop") || form.Values.Has("page") {
		t.Errorf("the form's fields = %v", form.Values)
	}
	if !regexp.MustCompile(`<input type="checkbox" class="cv-switch" role="switch" checked disabled><span>Force HTTPS</span>`).MatchString(row) {
		t.Error("Force HTTPS isn't drawn on and locked")
	}
	if !strings.Contains(row, `name="teamOnly"`) || strings.Contains(row, `name="teamOnly" value="on" checked`) {
		t.Error("Team only isn't an unchecked switch")
	}
	if !strings.Contains(row, `placeholder="/api"`) || !strings.Contains(row, `name="path" value=""`) {
		t.Error("Path isn't an empty mono field")
	}

	// The switch is worded per host: an apex redirects its www; a subdomain has none.
	if got := text(dmRowOf(body, "example.net")); !strings.Contains(got, "Redirect www.example.net here") {
		t.Errorf("an apex's Options:\n%s", got)
	}
	if sub := dmRowOf(body, "app.example.com"); strings.Contains(sub, `name="wwwRedirect"`) || strings.Contains(text(sub), "Redirect") {
		t.Error("a subdomain has a redirect switch")
	}
	// Remove… is a confirmation of its own, a form of its own.
	remove := formWith(t, row, "/domains/www.example.com/remove")
	if remove.Values.Get("app") != "shop" || remove.Values.Get("service") != "web" || remove.Values.Get("revision") == "" {
		t.Errorf("Remove's fields = %v", remove.Values)
	}
	if !strings.Contains(row, `<summary class="cv-btn cv-btn-secondary cv-btn-sm" aria-label="Remove www.example.com">Remove…</summary>`) || !strings.Contains(text(row), "Remove and deploy") {
		t.Error("Remove… isn't a confirmation")
	}

	// The app's own page draws the same form and comes back to itself.
	page := dmRowOf(e.pageOf(t, "/apps/shop/domains"), "www.example.com")
	if f := formWith(t, page, "/domains/www.example.com/options"); f.Values.Get("page") != "app" || f.Values.Get("app") != "shop" {
		t.Errorf("the app page's form fields = %v", f.Values)
	}
	if f := formWith(t, page, "/domains/www.example.com/remove"); f.Values.Get("page") != "app" {
		t.Errorf("the app page's Remove fields = %v", f.Values)
	}
}

// Each option is saved with a deploy and shown back; the password is in the answer that made it, once.
func TestDomainOptionsSaved(t *testing.T) {
	e := optionsEnv(t)
	const host = "www.example.com"
	domainOf := func() app.Domain {
		for _, d := range e.storedApp(t, "shop").Services[0].Domains {
			if d.Host == host {
				return d
			}
		}
		t.Fatalf("no %s in the stored app", host)
		return app.Domain{}
	}
	n := e.deployCount(t, "shop")

	// Path alone: 303 back to the row, open, and nothing shown once.
	resp, body := e.postForm(t, "/domains/"+host+"/options", optionsOf(e, t, "shop", "web", url.Values{"path": {"/api"}}))
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/domains?open=www.example.com#domain-www.example.com" {
		t.Fatalf("save a path = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	if d := domainOf(); d.Path != "/api" || d.TeamOnly || d.WWWRedirect || e.deployCount(t, "shop") != n+1 {
		t.Errorf("after the path: %+v, deployments %d", d, e.deployCount(t, "shop"))
	}
	page := e.pageOf(t, "/domains?open=www.example.com")
	row := dmRowOf(page, host)
	if !strings.Contains(row, `name="path" value="/api"`) || !strings.Contains(row, `<span class="cv-dmpath">/api</span>`) || !strings.Contains(row, ` open>`) && !strings.Contains(page, `id="domain-www.example.com" open>`) {
		t.Errorf("the path isn't shown back:\n%s", text(row))
	}

	// The www redirect on (its pair is the apex): saved, and the switch is on.
	resp, body = e.postForm(t, "/domains/"+host+"/options", optionsOf(e, t, "shop", "web", url.Values{"path": {"/api"}, "wwwRedirect": {"on"}}))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("save the redirect = %d:\n%s", resp.StatusCode, text(body))
	}
	if d := domainOf(); !d.WWWRedirect || d.Path != "/api" {
		t.Errorf("after the redirect: %+v", d)
	}
	if row = dmRowOf(e.pageOf(t, "/domains"), host); !regexp.MustCompile(`name="wwwRedirect" value="on" checked`).MatchString(row) {
		t.Errorf("the redirect switch isn't on:\n%s", row)
	}
	// A subdomain can't carry the redirect: the post's flag is dropped, not saved.
	if resp, _ = e.postForm(t, "/domains/app.example.com/options", optionsOf(e, t, "shop", "web", url.Values{"wwwRedirect": {"on"}})); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a subdomain's options = %d", resp.StatusCode)
	}
	for _, d := range e.storedApp(t, "shop").Services[0].Domains {
		if d.Host == "app.example.com" && d.WWWRedirect {
			t.Error("a subdomain got the redirect")
		}
	}

	// Team only on: 200, the panel with the password, never cached.
	resp, body = e.postForm(t, "/domains/"+host+"/options", optionsOf(e, t, "shop", "web", url.Values{"path": {"/api"}, "wwwRedirect": {"on"}, "teamOnly": {"on"}}))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Team only = %d, Cache-Control %q:\n%s", resp.StatusCode, resp.Header.Get("Cache-Control"), text(body))
	}
	m := passwordRe.FindStringSubmatch(body)
	if m == nil || len(m[1]) != 32 {
		t.Fatalf("no password of 32 characters in the panel:\n%s", text(body))
	}
	password := m[1]
	if got := text(body); !strings.Contains(got, "Copy the password for www.example.com now. User name team.") || strings.Count(body, password) != 1 {
		t.Errorf("the panel's words, or the password appears more than once:\n%s", got)
	}
	if !strings.Contains(body, `data-copy="team-password-value" hidden`) {
		t.Error("the Copy button isn't hidden until script reveals it")
	}
	// The row is open under the panel, with the switch on and its lock.
	if r := dmRowOf(body, host); !strings.Contains(r, `name="teamOnly" value="on" checked`) || !strings.Contains(r, `class="cv-dmlock"`) || !strings.Contains(text(r), "New password") {
		t.Errorf("the Team-only row after the post:\n%s", text(r))
	}
	if d := domainOf(); !d.TeamOnly || !d.WWWRedirect || d.Path != "/api" {
		t.Errorf("after Team only: %+v", d)
	}
	// Saving again keeps the same password: nothing is shown, 303.
	resp, body = e.postForm(t, "/domains/"+host+"/options", optionsOf(e, t, "shop", "web", url.Values{"path": {"/v2"}, "wwwRedirect": {"on"}, "teamOnly": {"on"}}))
	if resp.StatusCode != http.StatusSeeOther || strings.Contains(body, password) {
		t.Errorf("saving a Team-only domain again = %d", resp.StatusCode)
	}

	// Every option off: back to a plain domain, with no lock.
	if resp, _ = e.postForm(t, "/domains/"+host+"/options", optionsOf(e, t, "shop", "web", nil)); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("every option off = %d", resp.StatusCode)
	}
	if d := domainOf(); d.HasOptions() {
		t.Errorf("after turning everything off: %+v", d)
	}
	if r := dmRowOf(e.pageOf(t, "/domains"), host); strings.Contains(r, "cv-dmlock") || strings.Contains(text(r), "New password") {
		t.Errorf("a plain row still shows Team only:\n%s", text(r))
	}
}

// The same form on the app's page comes back to it.
func TestDomainOptionsOnTheAppPage(t *testing.T) {
	e := optionsEnv(t)
	form := optionsOf(e, t, "shop", "web", url.Values{"page": {"app"}, "path": {"/x"}})
	resp, body := e.postForm(t, "/domains/app.example.com/options", form)
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/apps/shop/domains?open=app.example.com#domain-app.example.com" {
		t.Fatalf("save on the app page = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	// Team only on the app's page: the password is above the app's page.
	form = optionsOf(e, t, "shop", "web", url.Values{"page": {"app"}, "path": {"/x"}, "teamOnly": {"on"}})
	resp, body = e.postForm(t, "/domains/app.example.com/options", form)
	if resp.StatusCode != http.StatusOK || passwordRe.FindStringSubmatch(body) == nil || !strings.Contains(body, `id="add"`) || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Team only on the app page = %d:\n%s", resp.StatusCode, text(body))
	}
	if !strings.Contains(body, `id="domain-app.example.com" open>`) {
		t.Error("the row isn't open under the password")
	}
}

// New password: only for a Team-only domain, the password once, the old one gone, and no deploy.
func TestDomainNewPassword(t *testing.T) {
	e := optionsEnv(t)
	const host = "app.example.com"
	resp, body := e.postForm(t, "/domains/"+host+"/options", optionsOf(e, t, "shop", "web", url.Values{"teamOnly": {"on"}}))
	first := passwordRe.FindStringSubmatch(body)
	if resp.StatusCode != http.StatusOK || first == nil {
		t.Fatalf("Team only = %d:\n%s", resp.StatusCode, text(body))
	}
	n := e.deployCount(t, "shop")

	row := dmRowOf(e.pageOf(t, "/domains"), host)
	rotate := formWith(t, row, "/domains/"+host+"/team-password")
	if rotate.Values.Get("app") != "shop" || !strings.Contains(text(row), "The old password stops working at once.") {
		t.Errorf("New password's form = %v:\n%s", rotate.Values, text(row))
	}
	resp, body = e.submit(t, rotate, nil)
	second := passwordRe.FindStringSubmatch(body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || second == nil || second[1] == first[1] {
		t.Fatalf("New password = %d, %v then %v:\n%s", resp.StatusCode, first, second, text(body))
	}
	if got := text(body); !strings.Contains(got, "Copy the new password for app.example.com now. User name team.") || strings.Contains(body, first[1]) {
		t.Errorf("the panel:\n%s", got)
	}
	if e.deployCount(t, "shop") != n {
		t.Error("a new password deployed")
	}

	// A domain that isn't Team only has no password to replace.
	resp, body = e.postForm(t, "/domains/www.example.com/team-password", url.Values{"app": {"shop"}, "service": {"web"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "www.example.com isn't Team only, so it has no password.") || passwordRe.MatchString(body) {
		t.Errorf("a new password for a plain domain = %d:\n%s", resp.StatusCode, text(body))
	}
	// A host the app doesn't have.
	if resp, _ = e.postForm(t, "/domains/nope.example.com/team-password", url.Values{"app": {"shop"}, "service": {"web"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a new password for a missing domain = %d, want 422", resp.StatusCode)
	}
}

// Remove: a deploy without the host, on the page's revision, from either page.
func TestDomainRemove(t *testing.T) {
	e := optionsEnv(t)
	hosts := func() []string {
		var out []string
		for _, d := range e.storedApp(t, "shop").Services[0].Domains {
			out = append(out, d.Host)
		}
		return out
	}
	n := e.deployCount(t, "shop")
	resp, body := e.postForm(t, "/domains/app.example.com/remove", optionsOf(e, t, "shop", "web", nil))
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/apps/shop/deployments#deployment-") {
		t.Fatalf("remove = %d to %q:\n%s", resp.StatusCode, location(t, resp), text(body))
	}
	if strings.Join(hosts(), ",") != "www.example.com,example.net" || e.deployCount(t, "shop") != n+1 {
		t.Errorf("after the remove: %v, %d deployments", hosts(), e.deployCount(t, "shop"))
	}
	if resp, _ = e.postForm(t, "/domains/example.net/remove", optionsOf(e, t, "shop", "web", url.Values{"page": {"app"}})); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove from the app page = %d", resp.StatusCode)
	}
	if strings.Join(hosts(), ",") != "www.example.com" {
		t.Errorf("after the second remove: %v", hosts())
	}
	// A host that is gone now: the page again with the words, nothing deployed.
	n = e.deployCount(t, "shop")
	resp, body = e.postForm(t, "/domains/app.example.com/remove", optionsOf(e, t, "shop", "web", nil))
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "This app has no domain app.example.com on that service. It may have been removed already.") || e.deployCount(t, "shop") != n {
		t.Errorf("removing it twice = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestDomainOptionsRefusals(t *testing.T) {
	e := optionsEnv(t)
	n := e.deployCount(t, "shop")
	unchanged := func(t *testing.T) {
		t.Helper()
		if e.deployCount(t, "shop") != n {
			t.Error("a refused change deployed")
		}
	}

	t.Run("a path without its slash is refused under the field, with what was typed", func(t *testing.T) {
		resp, body := e.postForm(t, "/domains/www.example.com/options", optionsOf(e, t, "shop", "web", url.Values{"path": {"api"}, "teamOnly": {"on"}}))
		row := dmRowOf(body, "www.example.com")
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(row, `aria-invalid="true"`) || !strings.Contains(row, `name="path" value="api"`) ||
			!strings.Contains(row, `aria-describedby="domain-path-www.example.com-hint domain-path-www.example.com-error"`) || !strings.Contains(text(row), "path") {
			t.Errorf("a bad path = %d:\n%s", resp.StatusCode, text(row))
		}
		// What was typed stays: Team only is still on in the redrawn form, and nothing was saved.
		if !strings.Contains(row, `name="teamOnly" value="on" checked`) {
			t.Error("the redrawn form lost the Team only switch")
		}
		for _, d := range e.storedApp(t, "shop").Services[0].Domains {
			if d.HasOptions() {
				t.Errorf("a refused post saved %+v", d)
			}
		}
		unchanged(t)
	})

	t.Run("a stale page", func(t *testing.T) {
		old := e.revisionOf(t, "shop")
		other := e.storedApp(t, "shop")
		other.Services[0].Resources.MemoryMB = 222
		e.deploy(t, other)
		n = e.deployCount(t, "shop")
		resp, body := e.postForm(t, "/domains/www.example.com/options", url.Values{"app": {"shop"}, "service": {"web"}, "revision": {old}, "path": {"/api"}})
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), changedSincePage) {
			t.Errorf("a stale save = %d:\n%s", resp.StatusCode, text(body))
		}
		if resp, _ = e.postForm(t, "/domains/www.example.com/remove", url.Values{"app": {"shop"}, "service": {"web"}, "revision": {old}}); resp.StatusCode != http.StatusConflict {
			t.Errorf("a stale remove = %d, want 409", resp.StatusCode)
		}
		unchanged(t)
	})

	t.Run("the redirect's pair is another app's", func(t *testing.T) {
		e.deploy(t, domainsApp("rival", "www.rival.org"))
		e.deploy(t, domainsApp("squat", "rival.org"))
		n = e.deployCount(t, "rival")
		resp, body := e.postForm(t, "/domains/www.rival.org/options", optionsOf(e, t, "rival", "web", url.Values{"wwwRedirect": {"on"}}))
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "The redirect can't be saved: www.rival.org would redirect rival.org, which is a domain of app squat.") {
			t.Errorf("a redirect over another app's host = %d:\n%s", resp.StatusCode, text(body))
		}
		if e.deployCount(t, "rival") != n {
			t.Error("a refused change deployed")
		}
	})

	t.Run("a hand-made post", func(t *testing.T) {
		n = e.deployCount(t, "shop")
		resp, _ := e.postForm(t, "/domains/www.example.com/options", url.Values{"app": {"shop"}, "service": {"web"}})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("a post with no revision = %d, want 400", resp.StatusCode)
		}
		if resp, _ = e.postForm(t, "/domains/www.example.com/options", url.Values{"app": {"nope"}, "service": {"web"}, "revision": {"x"}}); resp.StatusCode != http.StatusNotFound {
			t.Errorf("a post for a missing app = %d, want 404", resp.StatusCode)
		}
		resp, body := e.postForm(t, "/domains/www.example.com/options", url.Values{"app": {"shop"}, "service": {"db"}, "revision": {e.revisionOf(t, "shop")}})
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(body), "This app has no domain www.example.com on that service.") {
			t.Errorf("a post for another service = %d:\n%s", resp.StatusCode, text(body))
		}
		// Without the session, the post is refused before anything is read.
		resp, _ = e.do(t, request{method: http.MethodPost, path: "/domains/www.example.com/options", form: optionsOf(e, t, "shop", "web", nil),
			headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther && !strings.HasPrefix(location(t, resp), "/login") {
			t.Errorf("a post without the session = %d", resp.StatusCode)
		}
		unchanged(t)
	})
}

// A stopped app is refused with the stopped sentence on every post; its rows show the note, not the form.
func TestDomainOptionsStoppedApp(t *testing.T) {
	e := optionsEnv(t)
	e.postForm(t, "/domains/app.example.com/options", optionsOf(e, t, "shop", "web", url.Values{"teamOnly": {"on"}}))
	if err := e.Store.SetAppStopped(context.Background(), "shop", new(e.Clock.Now())); err != nil {
		t.Fatal(err)
	}
	row := dmRowOf(e.pageOf(t, "/domains"), "www.example.com")
	if !strings.Contains(text(row), "The app is stopped: start it first.") || strings.Contains(row, "Save and deploy") || strings.Contains(row, "/options") {
		t.Errorf("a stopped app's row:\n%s", text(row))
	}
	n := e.deployCount(t, "shop")
	sentence := "The app is stopped: start it first."
	for path, form := range map[string]url.Values{
		"/domains/www.example.com/options":       optionsOf(e, t, "shop", "web", url.Values{"path": {"/api"}}),
		"/domains/www.example.com/remove":        optionsOf(e, t, "shop", "web", nil),
		"/domains/app.example.com/team-password": {"app": {"shop"}, "service": {"web"}},
	} {
		resp, body := e.postForm(t, path, form)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), sentence) {
			t.Errorf("POST %s on a stopped app = %d:\n%s", path, resp.StatusCode, text(body))
		}
	}
	// On the app's page too.
	form := optionsOf(e, t, "shop", "web", url.Values{"page": {"app"}, "path": {"/api"}})
	if resp, body := e.postForm(t, "/domains/www.example.com/options", form); resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), sentence) {
		t.Errorf("POST on the app page of a stopped app = %d:\n%s", resp.StatusCode, text(body))
	}
	if e.deployCount(t, "shop") != n {
		t.Error("a stopped app deployed")
	}
}

// An app that keeps its Compose file is edited through the file's overlay: an option change keeps the
// file as typed, kept, with no secret value in it.
func TestDomainOptionsKeptFile(t *testing.T) {
	k, _ := keptShopEnv(t)
	n := k.deployCount(t, "shop")
	row := dmRowOf(k.pageOf(t, "/domains"), "shop.example.com")
	if !strings.Contains(row, `action="/domains/shop.example.com/options"`) || strings.Contains(text(row), "This app is edited through its Compose file.") {
		t.Errorf("a kept-file app's row:\n%s", text(row))
	}
	resp, body := k.postForm(t, "/domains/shop.example.com/options", optionsOf(k, t, "shop", "web", url.Values{"path": {"/shop"}}))
	if resp.StatusCode != http.StatusSeeOther || k.deployCount(t, "shop") != n+1 {
		t.Fatalf("an option change on a kept-file app = %d:\n%s", resp.StatusCode, text(body))
	}
	rec, err := k.Store.GetApp(context.Background(), "shop")
	if err != nil || rec.Compose == nil {
		t.Fatalf("the kept file: %v", err)
	}
	if d := rec.Compose.Services["web"].Domains; len(d) != 1 || d[0].Host != "shop.example.com" || d[0].Path != "/shop" {
		t.Errorf("the overlay's domains = %+v", d)
	}
	if rec.Compose.Compose != shopCompose {
		t.Error("the Compose file as typed changed")
	}
	// Team only on a kept-file app: the password once, the file kept.
	resp, body = k.postForm(t, "/domains/shop.example.com/options", optionsOf(k, t, "shop", "web", url.Values{"path": {"/shop"}, "teamOnly": {"on"}}))
	if resp.StatusCode != http.StatusOK || passwordRe.FindStringSubmatch(body) == nil {
		t.Fatalf("Team only on a kept-file app = %d:\n%s", resp.StatusCode, text(body))
	}
	if rec, _ = k.Store.GetApp(context.Background(), "shop"); rec.Compose == nil || !rec.Compose.Services["web"].Domains[0].TeamOnly {
		t.Error("the kept file lost the Team-only flag")
	}
	// A bad path names the field in the row, as the definition's error does.
	resp, body = k.postForm(t, "/domains/shop.example.com/options", optionsOf(k, t, "shop", "web", url.Values{"path": {"nope"}}))
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(dmRowOf(body, "shop.example.com"), `aria-invalid="true"`) {
		t.Errorf("a bad path on a kept-file app = %d:\n%s", resp.StatusCode, text(body))
	}
	// Remove works through the overlay too.
	if resp, _ = k.postForm(t, "/domains/shop.example.com/remove", optionsOf(k, t, "shop", "web", nil)); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("remove on a kept-file app = %d", resp.StatusCode)
	}
}

// The password is in the one answer that made it: no later page, part, history, header or log line has it.
func TestDomainPasswordIsShownOnce(t *testing.T) {
	e := optionsEnv(t)
	resp, body := e.postForm(t, "/domains/app.example.com/options", optionsOf(e, t, "shop", "web", url.Values{"teamOnly": {"on"}}))
	m := passwordRe.FindStringSubmatch(body)
	if resp.StatusCode != http.StatusOK || m == nil {
		t.Fatalf("Team only = %d:\n%s", resp.StatusCode, text(body))
	}
	first := m[1]
	_, body = e.submit(t, formWith(t, dmRowOf(e.pageOf(t, "/domains"), "app.example.com"), "/domains/app.example.com/team-password"), nil)
	second := passwordRe.FindStringSubmatch(body)[1]

	for _, path := range []string{"/domains", "/domains?open=app.example.com", "/parts/domains", "/parts/domains?app=shop&page=app", "/apps/shop/domains",
		"/apps/shop", "/apps/shop/deployments", "/apps/shop/configure", "/apps/shop/variables", "/apps", "/parts/activity", "/settings", "/settings/certificates", "/settings/tokens"} {
		resp, page := e.get(t, path)
		for _, pw := range []string{first, second} {
			if strings.Contains(page, pw) || strings.Contains(resp.Header.Get("Location"), pw) {
				t.Errorf("%s shows a Team-only password", path)
			}
		}
	}
	if logs := e.Logs.String(); strings.Contains(logs, first) || strings.Contains(logs, second) {
		t.Error("a Team-only password is in the logs")
	}
	// The stored deployment records name no password either.
	for _, d := range e.deployments(t, "shop") {
		if strings.Contains(d.Error, first) || strings.Contains(d.Error, second) {
			t.Errorf("deployment %s holds a password", d.Deployment.ID)
		}
	}
}

// "Domains go to": a service with several ports and domains has a select of its ports in Configure's
// Deploy panel; saving sets the port of every one of its domains, and nothing else of them.
func TestConfigureDomainsGoTo(t *testing.T) {
	e := newConsoleEnv(t)
	a := domainsApp("ports", "a.example.com", "b.example.com")
	a.Services[0].Ports = []app.Port{{Name: "http", ContainerPort: 80}, {Name: "admin", ContainerPort: 9000}}
	a.Services[0].Domains[1].Path = "/b"
	e.deploy(t, a)

	body := e.pageOf(t, "/apps/ports/configure")
	if got := text(body); !strings.Contains(got, "Domains go to") || !strings.Contains(got, "Every domain of this service goes to this port.") {
		t.Errorf("Configure lacks Domains go to:\n%s", got)
	}
	if !regexp.MustCompile(`<select class="cv-field cv-select" id="svc-web-domainPort" name="svc.web.domainPort"[^>]*>\s*<option value="http" selected>http · :80</option>\s*<option value="admin">admin · :9000</option>`).MatchString(body) {
		t.Errorf("the select isn't http · :80 and admin · :9000 with http chosen:\n%s", text(body))
	}
	// There is no single Container port input for a service with several ports.
	if strings.Contains(body, `name="svc.web.port"`) {
		t.Error("a service with several ports has a Container port input")
	}

	n := e.deployCount(t, "ports")
	resp, out := e.submit(t, e.configureForm(t, "ports"), url.Values{"svc.web.domainPort": {"admin"}})
	if resp.StatusCode != http.StatusSeeOther || e.deployCount(t, "ports") != n+1 {
		t.Fatalf("save = %d:\n%s", resp.StatusCode, text(out))
	}
	for _, d := range e.storedApp(t, "ports").Services[0].Domains {
		if d.Port != "admin" {
			t.Errorf("%s goes to %q, want admin", d.Host, d.Port)
		}
	}
	if d := e.storedApp(t, "ports").Services[0].Domains[1]; d.Path != "/b" {
		t.Errorf("the domain's path = %q: saving the port changed another option", d.Path)
	}
	if got := text(e.pageOf(t, "/apps/ports/configure")); !strings.Contains(got, "admin · :9000") {
		t.Errorf("the select doesn't show admin after the save:\n%s", got)
	}
	// The Domains page shows where they go.
	if got := text(e.pageOf(t, "/apps/ports/domains")); !strings.Contains(got, ":9000") {
		t.Errorf("the Domains page doesn't show the port:\n%s", got)
	}

	// A port the service doesn't have is refused under the select, and nothing is deployed.
	n = e.deployCount(t, "ports")
	resp, out = e.submit(t, e.configureForm(t, "ports"), url.Values{"svc.web.domainPort": {"nope"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(text(out), "Choose one of the service's ports.") || !strings.Contains(out, `aria-invalid="true"`) || e.deployCount(t, "ports") != n {
		t.Errorf("an unknown port = %d:\n%s", resp.StatusCode, text(out))
	}
	// A post without the field (a form drawn before) keeps the stored port.
	f := e.configureForm(t, "ports")
	delete(f.Values, "svc.web.domainPort")
	if resp, _ = e.submit(t, f, url.Values{"svc.web.memory": {"200"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a post without the select = %d", resp.StatusCode)
	}
	if d := e.storedApp(t, "ports").Services[0].Domains[0]; d.Port != "admin" {
		t.Errorf("a post without the select moved the domains to %q", d.Port)
	}

	// One port, or no domain: no select.
	e.deploy(t, whoamiApp())
	if strings.Contains(e.pageOf(t, "/apps/whoami/configure"), "Domains go to") {
		t.Error("a one-port service offers Domains go to")
	}
}
