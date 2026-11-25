package ui

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/templates"
)

// Feature 058: choice inputs, optional inputs and the groups of inputs only one answer asks for.

func whenSignin(values ...string) templates.Condition {
	return templates.Condition{"signin": templates.CondValue{Values: values}}
}

// signinTemplate asks for a Sign-in choice and, under each answer, that provider's inputs.
func signinTemplate() templates.Template {
	secret := func(s string) *string { return &s }
	no := false
	return templates.Template{
		Name: "signin", Title: "Signin", Version: "1.0-1", Description: "Chooses.", Tags: []string{"demo"},
		Upstream: "https://example.com", AppName: "signin",
		Notes: "Sign in at https://{{domain}} with {{signin}}.",
		Inputs: []templates.Input{
			{Name: "domain", Label: "Domain", Type: templates.TypeDomain, Example: "demo.example.com"},
			{Name: "signin", Label: "Sign-in", Type: templates.TypeChoice, Default: "github", Choices: []templates.Choice{
				{Value: "github", Label: "GitHub"}, {Value: "bitbucket", Label: "Bitbucket Cloud"}, {Value: "gitlab", Label: "GitLab"}}},
			{Name: "gh_id", Label: "GitHub client ID", Type: templates.TypeText, Pattern: `[A-Za-z0-9]{8,20}`, Example: "abcdefgh1234", When: whenSignin("github")},
			{Name: "gh_secret", Label: "GitHub client secret", Type: templates.TypeSecret, Example: "gh-example", When: whenSignin("github")},
			{Name: "bb_id", Label: "Bitbucket key", Type: templates.TypeText, Pattern: `[A-Za-z0-9]{8,20}`, Example: "bbbbbbbb1234", When: whenSignin("bitbucket", "gitlab")},
			{Name: "bb_secret", Label: "Bitbucket secret", Type: templates.TypeSecret, Example: "bb-example", When: whenSignin("bitbucket", "gitlab")},
			{Name: "region", Label: "Region", Type: templates.TypeText, Pattern: `[a-z0-9-]{2,20}`, Example: "us-east-1", Required: &no},
		},
		Services: map[string]compose.Overlay{"web": {
			Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}},
			Health:  &compose.Health{Path: "/"},
			Secrets: []app.Secret{{Name: "GITHUB_SECRET", Value: secret("{{gh_secret}}")}, {Name: "OTHER_SECRET", Value: secret("{{bb_secret}}")}},
		}},
		ItemWhens: map[string]map[string][]templates.Condition{"web": {"secrets": {whenSignin("github"), whenSignin("bitbucket", "gitlab")}}},
		Compose: "services:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"80\"]\n    environment:\n" +
			"      SIGNIN: \"{{signin}}\"\n      GITHUB_ID: \"{{gh_id}}\" # when signin=github\n      OTHER_ID: \"{{bb_id}}\" # when signin=bitbucket,gitlab\n" +
			"      REGION: \"{{region}}\" # when region\n",
	}
}

func TestTemplatePageDrawsChoicesAndGroups(t *testing.T) {
	e := newConsoleEnv(t)
	useCatalog(t, e, signinTemplate())
	resp, body := e.get(t, "/apps/new/template/signin")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d:\n%s", resp.StatusCode, body)
	}
	page := text(body)

	t.Run("a choice is a group of radios with the default checked", func(t *testing.T) {
		for _, want := range []string{
			`<legend class="cv-label">Sign-in</legend>`,
			`<input type="radio" name="in_signin" value="github" checked required>`,
			`<input type="radio" name="in_signin" value="bitbucket" required>`,
			`<input type="radio" name="in_signin" value="gitlab" required>`,
			`<span>Bitbucket Cloud</span>`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the page lacks %q:\n%s", want, body)
			}
		}
		if strings.Contains(body, `<select class="cv-field cv-select" id="in_signin"`) {
			t.Error("three answers are drawn as a select")
		}
	})
	t.Run("the inputs a choice asks for are in a group with its heading", func(t *testing.T) {
		for _, want := range []string{"Only for GitHub", "Only for Bitbucket Cloud or GitLab"} {
			if !strings.Contains(page, want) {
				t.Errorf("the page lacks %q:\n%s", want, page)
			}
		}
		// The script reads the condition from the group.
		if !strings.Contains(body, `data-when="{&#34;signin&#34;:[&#34;github&#34;]}"`) ||
			!strings.Contains(body, `data-when="{&#34;signin&#34;:[&#34;bitbucket&#34;,&#34;gitlab&#34;]}"`) {
			t.Errorf("the groups carry no condition:\n%s", body)
		}
		if n := strings.Count(body, `<fieldset class="cv-tplwhen cv-fieldset"`); n != 2 {
			t.Errorf("%d groups, want 2", n)
		}
		// The domain is asked whatever is chosen, so it sits outside every group.
		if i, j := strings.Index(body, `id="in_domain"`), strings.Index(body, `cv-tplwhen`); i > j {
			t.Error("the domain is inside a group")
		}
	})
	t.Run("without script every input is on the page and none that is only sometimes asked is required", func(t *testing.T) {
		for _, id := range []string{"in_gh_id", "in_gh_secret", "in_bb_id", "in_bb_secret", "in_region"} {
			if !strings.Contains(body, `id="`+id+`"`) {
				t.Errorf("no input %s", id)
			}
		}
		for _, id := range []string{"in_gh_id", "in_gh_secret", "in_bb_id", "in_bb_secret"} {
			i := strings.Index(body, `id="`+id+`"`)
			tag := body[i : i+strings.Index(body[i:], ">")]
			if strings.Contains(tag, " required") || !strings.Contains(tag, " data-required") {
				t.Errorf("%s: %s", id, tag)
			}
		}
		i := strings.Index(body, `id="in_domain"`)
		if tag := body[i : i+strings.Index(body[i:], ">")]; !strings.Contains(tag, " required") {
			t.Errorf("the domain is not required: %s", tag)
		}
		// The optional input says so and isn't required.
		i = strings.Index(body, `id="in_region"`)
		if tag := body[i : i+strings.Index(body[i:], ">")]; strings.Contains(tag, "required") {
			t.Errorf("the optional input is required: %s", tag)
		}
		if !strings.Contains(page, "Region optional") {
			t.Errorf("the optional input doesn't say so:\n%s", page)
		}
	})
	t.Run("the notes stay above the form and may name a choice", func(t *testing.T) {
		n := strings.Index(page, "Sign in at https://<your domain> with <your sign-in>.")
		if n < 0 {
			t.Errorf("the notes lack the choice's words:\n%s", page)
		}
		if i, f := strings.Index(body, `id="tpl-notes"`), strings.Index(body, "data-template-form"); i < 0 || i > f {
			t.Errorf("the notes (%d) aren't above the form (%d)", i, f)
		}
	})
	t.Run("the script is ours, from the console's own address, and nothing is inline", func(t *testing.T) {
		if !strings.Contains(body, `js/template-form.js`) || !strings.Contains(body, `data-template-form`) {
			t.Error("the page doesn't load the script")
		}
		for _, bad := range []string{"<script>", "onclick=", "onchange="} {
			if strings.Contains(body, bad) {
				t.Errorf("the page has %q", bad)
			}
		}
		resp, js := e.get(t, "/static/js/template-form.js")
		if resp.StatusCode != http.StatusOK || !strings.Contains(js, "data-when") {
			t.Errorf("the script = %d", resp.StatusCode)
		}
	})
	t.Run("more than four answers are a select that starts on Choose", func(t *testing.T) {
		many := signinTemplate()
		many.Inputs[1].Default = ""
		many.Inputs[1].Choices = nil
		for _, v := range []string{"aa", "bb", "cc", "dd", "ee"} {
			many.Inputs[1].Choices = append(many.Inputs[1].Choices, templates.Choice{Value: v, Label: strings.ToUpper(v)})
		}
		for i := range many.Inputs {
			many.Inputs[i].When = nil
		}
		many.Services = map[string]compose.Overlay{"web": {Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}}, Health: &compose.Health{Path: "/"}}}
		many.ItemWhens = nil
		many.Notes = ""
		many.Inputs = []templates.Input{many.Inputs[0], many.Inputs[1]}
		many.Compose = "services:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"80\"]\n    environment:\n      SIGNIN: \"{{signin}}\"\n"
		e2 := newConsoleEnv(t)
		useCatalog(t, e2, many)
		_, b := e2.get(t, "/apps/new/template/signin")
		for _, want := range []string{`<select class="cv-field cv-select" id="in_signin" name="in_signin" required>`,
			`<option value="" disabled selected>Choose…</option>`, `<option value="ee">EE</option>`} {
			if !strings.Contains(b, want) {
				t.Errorf("the select lacks %q:\n%s", want, b)
			}
		}
		if strings.Contains(b, `type="radio"`) {
			t.Error("five answers are drawn as radios")
		}
	})
}

func TestInstallFromTemplatePageWithChoices(t *testing.T) {
	ctx := context.Background()
	const unasked = "s3cret-sentinel-058-ui-github"
	const asked = "s3cret-sentinel-058-ui-bitbucket"
	e := newConsoleEnv(t)
	useCatalog(t, e, signinTemplate())

	// What a browser without script sends: every provider's inputs.
	answers := map[string]string{"domain": "who.example.com", "signin": "bitbucket", "bb_id": "bbbbbbbb1234", "bb_secret": asked,
		"gh_id": "zzzzzzzz9999", "gh_secret": unasked}
	var pages []string

	t.Run("a preview shows the file the chosen answers make and draws the form as it was", func(t *testing.T) {
		resp, body := e.postForm(t, "/apps/new/template/signin", installForm("who", answers, url.Values{"action": {"preview"}}))
		pages = append(pages, body)
		page := text(body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, "With these answers it makes:") {
			t.Fatalf("preview = %d:\n%s", resp.StatusCode, page)
		}
		// The secret of the chosen provider is listed by name, the other's isn't there.
		if !strings.Contains(page, "secrets OTHER_SECRET") || strings.Contains(page, "GITHUB_SECRET") {
			t.Errorf("secrets in the preview:\n%s", page)
		}
		if !strings.Contains(body, `OTHER_ID: &#34;bbbbbbbb1234&#34;`) || strings.Contains(body, "GITHUB_ID") || strings.Contains(body, "# when") {
			t.Errorf("the Compose file of the preview:\n%s", body)
		}
		if !strings.Contains(body, `<input type="radio" name="in_signin" value="bitbucket" checked required>`) || !strings.Contains(body, `value="bbbbbbbb1234"`) {
			t.Errorf("the form lost what was chosen:\n%s", body)
		}
		// What was typed for the other provider isn't drawn back, and no secret is.
		if strings.Contains(body, "zzzzzzzz9999") {
			t.Error("an input that isn't asked for was drawn back")
		}
		if _, err := e.Store.GetApp(ctx, "who"); err == nil {
			t.Error("a preview made an app")
		}
	})
	t.Run("a refusal of an asked input comes back at it", func(t *testing.T) {
		bad := map[string]string{"domain": "who.example.com", "signin": "bitbucket", "gh_id": "zzzzzzzz9999", "gh_secret": unasked}
		resp, body := e.postForm(t, "/apps/new/template/signin", installForm("who", bad, nil))
		pages = append(pages, body)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="in_bb_id-error"`) || !strings.Contains(text(body), "This answer is required.") {
			t.Errorf("= %d:\n%s", resp.StatusCode, text(body))
		}
		// A choice outside the list is refused at the choice.
		resp, body = e.postForm(t, "/apps/new/template/signin", installForm("who", map[string]string{"domain": "who.example.com", "signin": "svn"}, nil))
		pages = append(pages, body)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="in_signin-error"`) || !strings.Contains(text(body), "This answer is not one of the choices.") {
			t.Errorf("= %d:\n%s", resp.StatusCode, text(body))
		}
	})
	t.Run("Create installs with the chosen provider only", func(t *testing.T) {
		resp, body := e.postForm(t, "/apps/new/template/signin", installForm("who", answers, nil))
		pages = append(pages, body)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d:\n%s", resp.StatusCode, text(body))
		}
		next := location(t, resp)
		if !strings.Contains(next, "v_signin=bitbucket") || strings.Contains(next, "gh_id") || strings.Contains(next, "zzzzzzzz9999") {
			t.Errorf("the address = %q", next)
		}
		rec, err := e.Store.GetApp(ctx, "who")
		if err != nil || rec.Template.Name != "signin" || rec.Compose == nil {
			t.Fatalf("the app = %+v, %v", rec, err)
		}
		if strings.Contains(rec.Compose.Compose, "GITHUB") || !strings.Contains(rec.Compose.Compose, "OTHER_ID") {
			t.Errorf("the kept file:\n%s", rec.Compose.Compose)
		}
		resp, page := e.get(t, next)
		pages = append(pages, page)
		if !strings.Contains(text(page), "Sign in at https://who.example.com with bitbucket.") {
			t.Errorf("the notes after the install:\n%s", text(page))
		}
		for _, path := range []string{"/apps/who/configure", "/apps/who/variables", "/apps/who"} {
			_, b := e.get(t, path)
			pages = append(pages, b)
		}
	})

	for _, p := range pages {
		for _, secret := range []string{unasked, asked} {
			if strings.Contains(p, secret) {
				t.Errorf("a page holds %q: %.300s", secret, p)
			}
		}
	}
	if strings.Contains(e.Logs.String(), unasked) || strings.Contains(e.Logs.String(), asked) {
		t.Error("the log holds a secret")
	}
	files, _ := filepath.Glob(filepath.Join(e.Dir, "caravana.db*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, secret := range []string{unasked, asked, "zzzzzzzz9999"} {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s holds %q", filepath.Base(f), secret)
			}
		}
	}
}
