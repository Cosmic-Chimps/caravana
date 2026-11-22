package ui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 049: the preview settings form (the Previews page's setup tab, previewspage_test.go), the
// preview rows with their expiry on a project's settings page, and Close preview behind a
// confirmation page.

const previewSentinel = "s3cret-sentinel-049-password"

// previewShop is the shop console with an open preview, pr-3, holding the app web.
func previewShop(t *testing.T) *consoleEnv {
	t.Helper()
	e := shopConsole(t)
	if _, err := e.Svc.CreateEnvironment(context.Background(), "shop", "pr-3"); err != nil {
		t.Fatalf("CreateEnvironment pr-3: %v", err)
	}
	e.deploy(t, shopWeb("pr-3", "latest"))
	return e
}

func TestProjectSettingsShowsThePreviewSummaryAndTheRows(t *testing.T) {
	e := previewShop(t)
	resp, body := e.get(t, "/projects/shop/settings")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET settings = %d", resp.StatusCode)
	}
	got := text(body)
	for _, want := range []string{"Previews", "At most 5 open previews, closed after 7 days without a deploy, public.",
		"Change on the Previews page", "pr-3 · preview · expires", "Close preview…"} {
		if !strings.Contains(got, want) {
			t.Errorf("settings lacks %q:\n%s", want, got)
		}
	}
	for _, want := range []string{`href="/previews?project=shop&amp;tab=setup"`,
		`href="/projects/shop/previews/pr-3/close"`, `class="cv-clock"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings lacks %s:\n%s", want, body)
		}
	}
	// The form moved to the Previews page: nothing here posts the settings.
	for _, gone := range []string{`name="limit"`, `name="expireAfterDays"`, `type="password"`, "Save preview settings"} {
		if strings.Contains(body, gone) {
			t.Errorf("the project's settings still draws %s", gone)
		}
	}
	// Only a preview has the line and the button.
	if n := strings.Count(body, "Close preview…"); n != 1 {
		t.Errorf("Close preview… appears %d times, want 1 (staging and production aren't previews)", n)
	}

	// The sentence follows the settings: a password, and off.
	ctx := context.Background()
	pw, zero := previewSentinel, 0
	if _, err := e.Svc.SetPreviewSettings(ctx, "shop", service.PreviewSettingsInput{Password: &pw}); err != nil {
		t.Fatal(err)
	}
	if _, body = e.get(t, "/projects/shop/settings"); !strings.Contains(text(body), "closed after 7 days without a deploy, with a password.") {
		t.Errorf("the summary doesn't say there is a password:\n%s", text(body))
	}
	if _, err := e.Svc.SetPreviewSettings(ctx, "shop", service.PreviewSettingsInput{Limit: &zero}); err != nil {
		t.Fatal(err)
	}
	if _, body = e.get(t, "/projects/shop/settings"); !strings.Contains(text(body), "Previews are off: at most 0.") {
		t.Errorf("the summary doesn't say previews are off:\n%s", text(body))
	}
}

func TestPreviewSettingsSave(t *testing.T) {
	e := shopConsole(t)
	resp, body := e.postForm(t, "/projects/shop/previews/settings", url.Values{"limit": {"2"}, "expireAfterDays": {"3"}, "password": {""}, "from": {"previews"}})
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/previews?project=shop&saved=1&tab=setup" {
		t.Fatalf("save = %d to %q:\n%s", resp.StatusCode, location(t, resp), body)
	}
	list, err := e.Svc.Previews(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if st := list.Settings; st.Limit != 2 || st.ExpireAfterDays != 3 || st.PasswordSet {
		t.Errorf("settings = %+v, want limit 2, 3 days, no password", st)
	}
	_, page := e.get(t, location(t, resp))
	if !strings.Contains(text(page), "Preview settings saved.") || !strings.Contains(page, `name="limit" value="2"`) {
		t.Errorf("the page after the save lacks the notice or the values:\n%s", text(page))
	}
	if _, plain := e.get(t, "/previews?tab=setup&project=shop"); strings.Contains(text(plain), "Preview settings saved.") {
		t.Errorf("the notice is drawn without saved=1")
	}
	// A form that doesn't say it came from the Previews page goes back to the project's settings.
	if resp, _ := e.postForm(t, "/projects/shop/previews/settings", url.Values{"limit": {"2"}}); resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/projects/shop/settings?saved=previews#previews" {
		t.Errorf("save without from = %d to %q", resp.StatusCode, location(t, resp))
	}

	// Empty numbers keep what is there.
	if resp, _ := e.postForm(t, "/projects/shop/previews/settings", url.Values{"limit": {""}, "expireAfterDays": {""}, "from": {"previews"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("empty save = %d", resp.StatusCode)
	}
	if list, _ = e.Svc.Previews(context.Background(), "shop"); list.Settings.Limit != 2 || list.Settings.ExpireAfterDays != 3 {
		t.Errorf("an empty form changed the settings: %+v", list.Settings)
	}
	if resp, _ := e.postForm(t, "/projects/nope/previews/settings", url.Values{"limit": {"1"}, "from": {"previews"}}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown project = %d, want 404", resp.StatusCode)
	}
}

func TestPreviewSettingsRefusalsAreDrawnAtTheirField(t *testing.T) {
	e := shopConsole(t)
	for _, tc := range []struct {
		name   string
		form   url.Values
		status int
		want   string
		keeps  string // what the page keeps of what was typed
	}{
		{"limit not a number", url.Values{"limit": {"many"}}, 422, "Enter a whole number from 0 to 20.", `name="limit" value="many"`},
		{"limit too big", url.Values{"limit": {"30"}}, 422, "The limit is 30; it must be 0 to 20.", `name="limit" value="30"`},
		{"days not a number", url.Values{"expireAfterDays": {"x"}}, 422, "Enter a whole number of days from 1 to 30.", `name="expireAfterDays" value="x"`},
		{"days out of range", url.Values{"expireAfterDays": {"45"}}, 422, "The number of days is 45; it must be 1 to 30.", `name="expireAfterDays" value="45"`},
		{"password too short", url.Values{"password": {previewSentinel[:8]}}, 422, "The password is 8 characters; it must be 12 to 128.", `name="limit" value="5"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"from": {"previews"}}
			for k, v := range tc.form {
				form[k] = v
			}
			resp, body := e.postForm(t, "/projects/shop/previews/settings", form)
			if resp.StatusCode != tc.status || resp.Header.Get("Location") != "" {
				t.Fatalf("status = %d, Location %q, want %d with no redirect", resp.StatusCode, resp.Header.Get("Location"), tc.status)
			}
			if !strings.Contains(text(body), tc.want) {
				t.Errorf("the page lacks %q:\n%s", tc.want, text(body))
			}
			// The refusal is the Previews page's setup tab, with the project's own form.
			for _, want := range []string{`<h1 class="cv-headline">Previews</h1>`, `action="/projects/shop/previews/settings"`, `aria-invalid="true"`} {
				if !strings.Contains(body, want) {
					t.Errorf("the refusal isn't the setup tab: lacks %s", want)
				}
			}
			if !strings.Contains(body, tc.keeps) {
				t.Errorf("the page lacks %s", tc.keeps)
			}
			if pw := tc.form.Get("password"); pw != "" && strings.Contains(body, pw) {
				t.Errorf("the page draws the password back")
			}
		})
	}
	if list, _ := e.Svc.Previews(context.Background(), "shop"); list.Settings.Limit != 5 {
		t.Errorf("a refused form changed the settings: %+v", list.Settings)
	}
}

func TestPreviewPasswordLifecycleNeverDrawsThePassword(t *testing.T) {
	e := previewShop(t)
	password := previewSentinel

	// First time, with a preview open: the page says it gets the password at its next deploy (200, no redirect).
	resp, body := e.postForm(t, "/projects/shop/previews/settings", url.Values{"password": {password}, "from": {"previews"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set password = %d:\n%s", resp.StatusCode, text(body))
	}
	if !strings.Contains(text(body), "Open previews pr-3 get the password at their next deploy.") {
		t.Errorf("the answer doesn't name the open preview:\n%s", text(body))
	}
	if !strings.Contains(text(body), "Every preview address asks for it, user team.") || !strings.Contains(body, `name="removePassword"`) ||
		!strings.Contains(body, `placeholder="Leave empty to keep it"`) {
		t.Errorf("the section doesn't say a password is set, with Remove:\n%s", text(body))
	}

	// Removing it with a preview open is a 409 at the checkbox, and nothing changed.
	resp, body = e.postForm(t, "/projects/shop/previews/settings", url.Values{"removePassword": {"1"}, "from": {"previews"}})
	if resp.StatusCode != http.StatusConflict || resp.Header.Get("Location") != "" {
		t.Fatalf("remove with a preview open = %d", resp.StatusCode)
	}
	if !strings.Contains(text(body), "Can't remove the password while previews are open (pr-3)") {
		t.Errorf("the refusal doesn't name the preview:\n%s", text(body))
	}
	if !regexp.MustCompile(`id="preview-remove-error"`).MatchString(body) {
		t.Errorf("the refusal isn't drawn at the checkbox:\n%s", body)
	}
	if list, _ := e.Svc.Previews(context.Background(), "shop"); !list.Settings.PasswordSet {
		t.Errorf("the password was removed")
	}

	// The password with removePassword is refused at removePassword.
	if resp, _ := e.postForm(t, "/projects/shop/previews/settings", url.Values{"password": {password}, "removePassword": {"1"}, "from": {"previews"}}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("password with removePassword = %d, want 422", resp.StatusCode)
	}

	// Close the preview; now the password can go.
	if resp, _ := e.postForm(t, "/projects/shop/previews/pr-3/close", nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("close = %d", resp.StatusCode)
	}
	if resp, _ := e.postForm(t, "/projects/shop/previews/settings", url.Values{"removePassword": {"1"}, "from": {"previews"}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("remove with no preview open = %d, want 303", resp.StatusCode)
	}
	if list, _ := e.Svc.Previews(context.Background(), "shop"); list.Settings.PasswordSet {
		t.Errorf("the password is still set")
	}

	// The password is in no page and no log line.
	for _, path := range []string{"/projects/shop/settings", "/projects/shop", "/projects/shop/previews/pr-3/close", "/apps", "/projects",
		"/previews", "/previews?tab=setup&project=shop", "/parts/previews"} {
		_, page := e.get(t, path)
		if strings.Contains(page, password) {
			t.Errorf("GET %s holds the password", path)
		}
	}
	if logs := e.Logs.String(); logs == "" || strings.Contains(logs, password) {
		t.Errorf("the log is empty (the check proves nothing) or holds the password")
	}
}

func TestClosePreviewAsksThenDeletesTheAppsAndTheEnvironment(t *testing.T) {
	e := previewShop(t)
	resp, body := e.get(t, "/projects/shop/previews/pr-3/close")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET close = %d:\n%s", resp.StatusCode, text(body))
	}
	got := text(body)
	for _, want := range []string{"Close pr-3?", "every app in the preview and then the environment", "web", "Close pr-3", "Cancel"} {
		if !strings.Contains(got, want) {
			t.Errorf("the confirmation lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(body, `action="/projects/shop/previews/pr-3/close"`) {
		t.Errorf("the confirmation doesn't post to the close route:\n%s", body)
	}
	// Asking deleted nothing.
	if _, err := e.Svc.GetApp(context.Background(), "shop--pr-3--web"); err != nil {
		t.Fatalf("the app is gone after only asking: %v", err)
	}

	resp, _ = e.postForm(t, "/projects/shop/previews/pr-3/close", nil)
	if resp.StatusCode != http.StatusSeeOther || location(t, resp) != "/projects/shop/settings?closed=pr-3" {
		t.Fatalf("close = %d to %q", resp.StatusCode, location(t, resp))
	}
	if _, err := e.Svc.GetApp(context.Background(), "shop--pr-3--web"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the app is still there: %v", err)
	}
	if list, _ := e.Svc.Previews(context.Background(), "shop"); len(list.Previews) != 0 {
		t.Errorf("previews still open: %+v", list.Previews)
	}
	_, page := e.get(t, location(t, resp))
	if !strings.Contains(text(page), "Closed pr-3: its apps and the environment are deleted.") {
		t.Errorf("the settings after closing lack the notice:\n%s", text(page))
	}
	// Closing again is what was asked for: it is already closed, and nothing refuses.
	if resp, _ := e.postForm(t, "/projects/shop/previews/pr-3/close", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("close of a preview that is gone = %d, want 404", resp.StatusCode)
	}
}

func TestClosePreviewRefusesWhatIsNotAPreview(t *testing.T) {
	e := previewShop(t)
	for _, method := range []string{"GET", "POST"} {
		var resp *http.Response
		var body string
		if method == "GET" {
			resp, body = e.get(t, "/projects/shop/previews/staging/close")
		} else {
			resp, body = e.postForm(t, "/projects/shop/previews/staging/close", nil)
		}
		if resp.StatusCode != http.StatusConflict || resp.Header.Get("Location") != "" {
			t.Errorf("%s close of staging = %d, want 409 with no redirect", method, resp.StatusCode)
		}
		if !strings.Contains(text(body), "staging isn't a preview: only environments named pr-<pull request number> are.") {
			t.Errorf("%s close of staging doesn't say why:\n%s", method, text(body))
		}
		if strings.Contains(body, `<button type="submit" class="cv-btn cv-btn-danger">Close`) {
			t.Errorf("%s: the refusal still offers the button", method)
		}
	}
	if resp, _ := e.get(t, "/projects/shop/previews/pr-99/close"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET close of an unknown environment = %d, want 404", resp.StatusCode)
	}
	if resp, _ := e.get(t, "/projects/nope/previews/pr-3/close"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET close in an unknown project = %d, want 404", resp.StatusCode)
	}
}
