package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/templates"
)

// Feature 058: choice inputs, optional inputs and conditions, over the API.

func signinIs(values ...string) templates.Condition {
	return templates.Condition{"signin": templates.CondValue{Values: values}}
}

// signinDemo is demoTemplate's shape with a Sign-in choice and conditional content.
func signinDemo() templates.Template {
	secret := func(s string) *string { return &s }
	no := false
	return templates.Template{
		Name: "signin", Title: "Signin", Version: "1.0-1", Description: "Chooses.", Tags: []string{"demo"},
		Upstream: "https://example.com", AppName: "signin", Notes: "Sign in with {{signin}}.",
		Inputs: []templates.Input{
			{Name: "domain", Label: "Domain", Type: templates.TypeDomain, Example: "demo.example.com"},
			{Name: "signin", Label: "Sign-in", Type: templates.TypeChoice, Default: "github", Choices: []templates.Choice{
				{Value: "github", Label: "GitHub"}, {Value: "bitbucket", Label: "Bitbucket Cloud"}}},
			{Name: "gh_secret", Label: "GitHub secret", Type: templates.TypeSecret, Example: "gh-example", When: signinIs("github")},
			{Name: "bb_secret", Label: "Bitbucket secret", Type: templates.TypeSecret, Example: "bb-example", When: signinIs("bitbucket")},
			{Name: "region", Label: "Region", Type: templates.TypeText, Pattern: `[a-z0-9-]{2,20}`, Example: "us-east-1", Required: &no},
		},
		Services: map[string]compose.Overlay{"web": {
			Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}},
			Health:  &compose.Health{Path: "/"},
			Secrets: []app.Secret{{Name: "GITHUB_SECRET", Value: secret("{{gh_secret}}")}, {Name: "BITBUCKET_SECRET", Value: secret("{{bb_secret}}")}},
		}},
		ItemWhens: map[string]map[string][]templates.Condition{"web": {"secrets": {signinIs("github"), signinIs("bitbucket")}}},
		Compose: "services:\n  web:\n    image: traefik/whoami:latest\n    expose: [\"80\"]\n    environment:\n" +
			"      SIGNIN: \"{{signin}}\"\n      GITHUB: \"yes\" # when signin=github\n      BITBUCKET: \"yes\" # when signin=bitbucket\n" +
			"      REGION: \"{{region}}\" # when region\n",
	}
}

func TestTemplateChoicesOverTheAPI(t *testing.T) {
	const unasked = "s3cret-sentinel-058-api-github"
	const asked = "s3cret-sentinel-058-api-bitbucket"
	e := newTestEnv(t, "")
	doc := loadContract(t)
	useCatalog(t, e, signinDemo())

	t.Run("getTemplate says choices, required and when, in the contract's words", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/templates/signin", "", "")
		if status != http.StatusOK {
			t.Fatalf("getTemplate = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "Template")
		got := decodeAs[struct {
			Inputs []struct {
				Name     string                          `json:"name"`
				Type     string                          `json:"type"`
				Required *bool                           `json:"required"`
				Choices  []struct{ Value, Label string } `json:"choices"`
				When     map[string]any                  `json:"when"`
			} `json:"inputs"`
			Services map[string]struct {
				Secrets []map[string]any `json:"secrets"`
			} `json:"services"`
		}](t, raw)
		in := got.Inputs
		if in[1].Type != "choice" || len(in[1].Choices) != 2 || in[1].Choices[1].Label != "Bitbucket Cloud" ||
			in[2].When["signin"] != "github" || in[4].Required == nil || *in[4].Required || in[0].Required != nil {
			t.Errorf("inputs = %s", raw)
		}
		secrets := got.Services["web"].Secrets
		if secrets[0]["when"] == nil || secrets[1]["when"] == nil {
			t.Errorf("the overlay's items lost their when: %s", raw)
		}
		// The list has no inputs, as before.
		_, _, list := call(t, http.MethodGet, e.URL+"/v1/templates", "", "")
		checkSchema(t, doc, list, "TemplateList")
	})

	t.Run("preview and install take the choice, and drop what it doesn't ask", func(t *testing.T) {
		body := `{"id": "chosen", "inputs": {"domain": "chosen.example.com", "signin": "bitbucket", "bb_secret": "` + asked + `", "gh_secret": "` + unasked + `"}}`
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/templates/signin/preview", "application/json", body)
		if status != http.StatusOK {
			t.Fatalf("previewTemplate = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "TemplatePreview")
		p := decodeAs[struct {
			App struct {
				Services []struct {
					Secrets []struct{ Name string } `json:"secrets"`
				} `json:"services"`
			} `json:"app"`
			Compose string `json:"compose"`
		}](t, raw)
		if strings.Contains(p.Compose, "GITHUB") || !strings.Contains(p.Compose, "BITBUCKET") || strings.Contains(p.Compose, "when") ||
			len(p.App.Services[0].Secrets) != 1 || p.App.Services[0].Secrets[0].Name != "BITBUCKET_SECRET" {
			t.Errorf("preview = %s", raw)
		}

		status, _, raw = install(t, e, "signin", body)
		if status != http.StatusAccepted {
			t.Fatalf("installTemplate = %d %s", status, raw)
		}
		d := decodeAs[deployment](t, raw)
		waitHealthy(t, e, "chosen", d.ID)
		for _, path := range []string{"/v1/apps/chosen", "/v1/apps/chosen/deployments", "/v1/apps/chosen/compose", "/v1/attention"} {
			_, _, raw := call(t, http.MethodGet, e.URL+path, "", "")
			for _, secret := range []string{asked, unasked} {
				if strings.Contains(string(raw), secret) {
					t.Errorf("%s holds %q", path, secret)
				}
			}
		}
		_, _, raw = call(t, http.MethodGet, e.URL+"/v1/apps/chosen/compose", "", "")
		var kept struct {
			Compose string `json:"compose"`
		}
		_ = json.Unmarshal(raw, &kept)
		if strings.Contains(kept.Compose, "GITHUB") || strings.Contains(kept.Compose, "when") || !strings.Contains(kept.Compose, "BITBUCKET") {
			t.Errorf("the kept file: %s", raw)
		}
	})

	errorsWanted := []struct {
		name  string
		body  string
		field string
	}{
		{"a choice outside the list", `{"id": "c2", "inputs": {"domain": "c2.example.com", "signin": "svn"}}`, "inputs.signin"},
		{"an asked input left empty", `{"id": "c2", "inputs": {"domain": "c2.example.com", "signin": "bitbucket"}}`, "inputs.bb_secret"},
		{"an optional answer that is wrong", `{"id": "c2", "inputs": {"domain": "c2.example.com", "gh_secret": "x", "region": "BAD"}}`, "inputs.region"},
		{"an input the template doesn't have", `{"id": "c2", "inputs": {"domain": "c2.example.com", "gh_secret": "x", "nope": "1"}}`, "inputs.nope"},
	}
	for _, tt := range errorsWanted {
		t.Run(tt.name, func(t *testing.T) {
			status, _, raw := install(t, e, "signin", tt.body)
			wantError(t, tt.name, status, raw, http.StatusBadRequest, codeInvalidField, tt.field)
			status, _, raw = call(t, http.MethodPost, e.URL+"/v1/templates/signin/preview", "application/json", tt.body)
			if tt.field != "inputs.bb_secret" { // a preview doesn't need a secret
				wantError(t, tt.name+" (preview)", status, raw, http.StatusBadRequest, codeInvalidField, tt.field)
			}
		})
	}
}
