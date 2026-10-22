package ui

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/deploy"
)

// registrySecret is a password no page, redirect or log line may contain.
const registrySecret = "s3cr3t-SENTINEL-registry-pw"

// registryForms returns the forms of the Registries section: the add form
// (no hidden host) and each row's edit form (a hidden host and edit=1).
func registryForms(page string) (add plainForm, edits map[string]plainForm) {
	edits = map[string]plainForm{}
	for _, f := range formsIn(page) {
		if f.Action != "/settings/registries" {
			continue
		}
		if f.Values.Get("edit") == "1" {
			edits[f.Values.Get("host")] = f
		} else {
			add = f
		}
	}
	return add, edits
}

// storedCredentials reads what Caravana keeps for host in the fake Nomad.
func (e *consoleEnv) storedCredentials(host string) (user, pass string, ok bool) {
	items := e.Nomad.Variable(deploy.RegistryVariablePath(host))
	return items["username"], items["password"], items != nil
}

// leaks fails the test when any of the outputs holds the password.
func leaks(t *testing.T, what string, outputs ...string) {
	t.Helper()
	for i, out := range outputs {
		if strings.Contains(out, registrySecret) {
			t.Errorf("%s: output %d holds the password", what, i)
		}
	}
}

// US2: the section lists what is stored, adds, edits, tests and removes, with
// plain forms, and the password is in no page, redirect or log line.
func TestSettingsRegistries(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	e.Reg.RequireToken(true)
	e.Reg.RequireLogin("deploy", registrySecret)
	host := e.Reg.Host() // 127.0.0.1:<port>: a host with a port in the route

	_, page := e.get(t, "/settings/registries")
	got := text(page)
	for _, want := range []string{
		"Registries",
		"Credentials Caravana uses to read private images.",
		"Use a token that can only read images: it reaches each private app's own container as CARAVANA_REGISTRY_USERNAME and CARAVANA_REGISTRY_PASSWORD.",
		"No registries yet. Public images from any registry deploy without one.",
		"The host in the image name: ghcr.io, registry.gitlab.com, registry.example.com:5000. docker.io for Docker Hub.",
		"Password or token",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Settings lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(page, `<form method="post" action="/settings/registries" id="add"`) || !strings.Contains(page, `placeholder="ghcr.io"`) {
		t.Error("the page lacks the add form's anchor or the host placeholder")
	}
	add, _ := registryForms(page)
	if add.Action == "" || add.Method != "post" {
		t.Fatalf("no add form in:\n%s", page)
	}

	t.Run("add, with a host in capitals", func(t *testing.T) {
		resp, body := e.submit(t, add, url.Values{"host": {"GHCR.io"}, "username": {"acme-bot"}, "password": {registrySecret}})
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/registries?registries=saved" {
			t.Fatalf("POST = %d to %q, want 303 to /settings/registries?registries=saved:\n%s", resp.StatusCode, location(t, resp), body)
		}
		_, page := e.get(t, "/settings/registries?registries=saved")
		got := text(page)
		for _, want := range []string{"Saved. The next deploy uses them; running apps pick them up the next time they start.", "ghcr.io", "acme-bot", "not tested yet"} {
			if !strings.Contains(got, want) {
				t.Errorf("after the save the page lacks %q:\n%s", want, got)
			}
		}
		if _, pass, ok := e.storedCredentials("ghcr.io"); !ok || pass != registrySecret {
			t.Error("the secret wasn't kept in the Nomad Variable")
		}
		leaks(t, "after add", body, page, e.Logs.String())
	})

	t.Run("refusals draw the page again with the error at its input and nothing secret", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			form   url.Values
			errID  string
			errMsg string
			typed  string
		}{
			{"not a host", url.Values{"host": {"acme"}, "username": {"u"}, "password": {registrySecret}}, "registry-add-host-error", "isn't a registry host", `value="acme"`},
			{"no user name", url.Values{"host": {"quay.io"}, "username": {""}, "password": {registrySecret}}, "registry-add-username-error", "The user name is required", `value="quay.io"`},
			{"no password the first time", url.Values{"host": {"quay.io"}, "username": {"u"}, "password": {""}}, "registry-add-password-error", "The password or token is required the first time", `value="u"`},
		} {
			resp, body := e.submit(t, add, tc.form)
			if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="`+tc.errID+`"`) || !strings.Contains(text(body), tc.errMsg) || !strings.Contains(body, tc.typed) {
				t.Errorf("%s: POST = %d, want the page with %q at %s and %s kept:\n%s", tc.name, resp.StatusCode, tc.errMsg, tc.errID, tc.typed, text(body))
			}
			leaks(t, tc.name, body)
		}
		if regs, _ := e.Svc.Registries(ctx); len(regs) != 1 {
			t.Errorf("a refused form stored something: %+v", regs)
		}
	})

	t.Run("edit keeps the secret when the field is empty", func(t *testing.T) {
		_, page := e.get(t, "/settings/registries")
		_, edits := registryForms(page)
		edit, ok := edits["ghcr.io"]
		if !ok || edit.Values.Get("username") != "acme-bot" {
			t.Fatalf("no edit form for ghcr.io with its user name prefilled: %+v\n%s", edits, page)
		}
		if _, hasPassword := edit.Values["password"]; !hasPassword || edit.Values.Get("password") != "" {
			t.Errorf("the edit form's password field is %q, want present and empty", edit.Values.Get("password"))
		}
		if !strings.Contains(text(page), "Leave empty to keep the current one.") {
			t.Error("the edit form lacks its hint")
		}
		resp, body := e.submit(t, edit, url.Values{"username": {"other-bot"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST edit = %d:\n%s", resp.StatusCode, text(body))
		}
		if user, pass, ok := e.storedCredentials("ghcr.io"); !ok || user != "other-bot" || pass != registrySecret {
			t.Errorf("after an edit without a password: user %q, password kept %v", user, pass == registrySecret)
		}

		// A refused edit shows that row's form open, with the error under its field.
		resp, body = e.submit(t, edit, url.Values{"username": {""}})
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="registry-edit-0-username-error"`) ||
			!regexp.MustCompile(`<details class="cv-confirm cv-confirm-row" id="registry-edit-0"[^>]* open>`).MatchString(body) {
			t.Errorf("a refused edit = %d, want the page with the edit form open and the error under the user name:\n%s", resp.StatusCode, text(body))
		}
		leaks(t, "edit", body)
	})

	t.Run("test announces what the registry said", func(t *testing.T) {
		// A registry the fake wants a login for, with a wrong secret first.
		resp, _ := e.submit(t, add, url.Values{"host": {host}, "username": {"deploy"}, "password": {"wrong-" + registrySecret}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST add = %d", resp.StatusCode)
		}
		_, page := e.get(t, "/settings/registries")
		resp, body := e.submit(t, formWith(t, page, "/settings/registries/"+host+"/test"), nil)
		want := "/settings/registries?tested=" + url.QueryEscape(host)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != want {
			t.Fatalf("POST test = %d to %q, want 303 to %q:\n%s", resp.StatusCode, location(t, resp), want, body)
		}
		_, page = e.get(t, strings.TrimSuffix(location(t, resp), "#registries"))
		got := text(page)
		if !strings.Contains(got, host+" refused the credentials: ") || !strings.Contains(got, "refused · tested just now") {
			t.Errorf("after a refused test the page says:\n%s", got)
		}
		if !strings.Contains(page, `role="alert" class="cv-notice"`) {
			t.Error("a refusal isn't an alert")
		}
		leaks(t, "refused test", body, page, e.Logs.String())

		// Fix the secret: the check is forgotten until the next test, which is ok.
		_, edits := registryForms(page)
		resp, _ = e.submit(t, edits[host], url.Values{"password": {registrySecret}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("POST edit = %d", resp.StatusCode)
		}
		_, page = e.get(t, "/settings/registries")
		if !strings.Contains(text(page), "not tested yet") {
			t.Error("a changed secret kept the old check")
		}
		resp, _ = e.submit(t, formWith(t, page, "/settings/registries/"+host+"/test"), nil)
		_, page = e.get(t, strings.TrimSuffix(location(t, resp), "#registries"))
		got = text(page)
		if !strings.Contains(got, host+" accepted the credentials.") || !strings.Contains(got, "works · tested just now") {
			t.Errorf("after an ok test the page says:\n%s", got)
		}
		leaks(t, "ok test", page, e.Logs.String())
	})

	t.Run("remove asks first and forgets the credentials", func(t *testing.T) {
		_, page := e.get(t, "/settings/registries")
		if !strings.Contains(text(page), "Apps already running keep running. The next deploy of a private image from this registry fails until you add credentials again.") {
			t.Errorf("the Remove confirmation lacks its sentence:\n%s", text(page))
		}
		if !regexp.MustCompile(`<details class="cv-confirm cv-confirm-row" name="conn-\d+">\s*<summary[^>]*aria-label="Remove credentials for ghcr.io"`).MatchString(page) {
			t.Error("Remove isn't a <details>")
		}
		resp, body := e.submit(t, formWith(t, page, "/settings/registries/ghcr.io/remove"), nil)
		if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/settings/registries" {
			t.Fatalf("POST remove = %d to %q:\n%s", resp.StatusCode, location(t, resp), body)
		}
		if _, _, ok := e.storedCredentials("ghcr.io"); ok {
			t.Error("the credentials are still in Nomad")
		}
		_, page = e.get(t, "/settings/registries")
		if strings.Contains(text(page), "other-bot") {
			t.Error("the row is still listed")
		}
		// A host that isn't stored is not an error.
		if resp, _ := e.postForm(t, "/settings/registries/ghcr.io/remove", nil); resp.StatusCode != http.StatusSeeOther {
			t.Errorf("POST remove of an unknown host = %d, want 303", resp.StatusCode)
		}
	})
}

// The posts of the section are protected like every other: a cross-site post is
// refused and changes nothing, and without a session they go to /login.
func TestRegistryPostsAreProtected(t *testing.T) {
	ctx := context.Background()
	e := newConsoleEnv(t)
	if _, err := e.Svc.SaveRegistry(ctx, "ghcr.io", "acme-bot", new(registrySecret)); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"host": {"quay.io"}, "username": {"forged"}, "password": {"x"}}
	for _, path := range []string{"/settings/registries", "/settings/registries/ghcr.io/test", "/settings/registries/ghcr.io/remove"} {
		resp, body := e.do(t, request{method: http.MethodPost, path: path, form: form, cookies: []*http.Cookie{e.Cookie},
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"}})
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "refused") {
			t.Errorf("cross-site POST %s = %d, want the 403 page", path, resp.StatusCode)
		}
		resp, _ = e.do(t, request{method: http.MethodPost, path: path, form: form, headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location(t, resp), "/login") {
			t.Errorf("POST %s without a session = %d to %q, want a redirect to /login", path, resp.StatusCode, location(t, resp))
		}
	}
	regs, _ := e.Svc.Registries(ctx)
	if len(regs) != 1 || regs[0].Host != "ghcr.io" || regs[0].LastTest != nil || regs[0].Username != "acme-bot" {
		t.Errorf("a refused post changed the registries: %+v", regs)
	}
}
