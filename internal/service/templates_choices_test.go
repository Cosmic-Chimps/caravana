package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/templates"
)

// Feature 058: choice inputs, optional inputs, conditions and catalog format 2.

func scopedTo(values ...string) templates.Condition {
	return templates.Condition{"signin": templates.CondValue{Values: values}}
}

// signinWhoami is whoamiTemplate with a Sign-in choice: each provider's inputs are asked only
// for it, and so are its Compose lines and its secret.
func signinWhoami() templates.Template {
	secret := func(s string) *string { return &s }
	no := false
	return templates.Template{
		Name: "demo", Title: "Demo", Version: "2.0-1", Description: "A demo.", Tags: []string{"demo"},
		Upstream: "https://example.com", AppName: "demo", Notes: "Open https://{{domain}} and sign in with {{signin}}.",
		Inputs: []templates.Input{
			{Name: "domain", Label: "Domain", Type: templates.TypeDomain, Example: "demo.example.com"},
			{Name: "signin", Label: "Sign-in", Type: templates.TypeChoice, Default: "github", Choices: []templates.Choice{
				{Value: "github", Label: "GitHub"}, {Value: "bitbucket", Label: "Bitbucket Cloud"}}},
			{Name: "gh_id", Label: "GitHub client ID", Type: templates.TypeText, Pattern: `[A-Za-z0-9]{8,20}`, Example: "abcdefgh1234", When: scopedTo("github")},
			{Name: "gh_secret", Label: "GitHub client secret", Type: templates.TypeSecret, Example: "gh-example", When: scopedTo("github")},
			{Name: "bb_id", Label: "Bitbucket key", Type: templates.TypeText, Pattern: `[A-Za-z0-9]{8,20}`, Example: "bbbbbbbb1234", When: scopedTo("bitbucket")},
			{Name: "bb_secret", Label: "Bitbucket secret", Type: templates.TypeSecret, Example: "bb-example", When: scopedTo("bitbucket")},
			{Name: "region", Label: "Region", Type: templates.TypeText, Pattern: `[a-z0-9-]{2,20}`, Example: "us-east-1", Required: &no},
		},
		Services: map[string]compose.Overlay{"web": {
			Domains: []compose.Domain{{Host: "{{domain}}", Port: 80}},
			Health:  &compose.Health{Path: "/"},
			Secrets: []app.Secret{
				{Name: "GITHUB_SECRET", Value: secret("{{gh_secret}}")},
				{Name: "BITBUCKET_SECRET", Value: secret("{{bb_secret}}")},
			},
		}},
		ItemWhens: map[string]map[string][]templates.Condition{"web": {"secrets": {scopedTo("github"), scopedTo("bitbucket")}}},
		Compose: `services:
  web:
    image: traefik/whoami:latest
    expose: ["80"]
    environment:
      PUBLIC_URL: "https://{{domain}}"
      SIGNIN: "{{signin}}"
      GITHUB_ID: "{{gh_id}}" # when signin=github
      BITBUCKET_ID: "{{bb_id}}" # when signin=bitbucket
      REGION: "{{region}}" # when region
`,
	}
}

func TestInstallTemplateWithChoices(t *testing.T) {
	ctx := context.Background()
	const typedUnasked = "s3cret-sentinel-058-github-secret"
	const typedAsked = "s3cret-sentinel-058-bitbucket-secret"

	l := newTemplateLab(t)
	l.useCatalog(t, futureCatVer, signinWhoami())

	inputs := map[string]string{
		"domain": "demo.example.com", "signin": "bitbucket",
		"bb_id": "bbbbbbbb1234", "bb_secret": typedAsked,
		// The form of a browser without JavaScript sends every provider's inputs.
		"gh_id": "zzzzzzzz9999", "gh_secret": typedUnasked,
	}
	preview, err := l.svc.PreviewTemplate(ctx, "demo", "demo", inputs, TemplateInstallOptions{})
	if err != nil {
		t.Fatalf("PreviewTemplate: %v", err)
	}
	if strings.Contains(preview.Compose, "GITHUB") || !strings.Contains(preview.Compose, `BITBUCKET_ID: "bbbbbbbb1234"`) || strings.Contains(preview.Compose, "when") {
		t.Errorf("preview compose:\n%s", preview.Compose)
	}
	if names := preview.App.Services[0].Secrets; len(names) != 1 || names[0].Name != "BITBUCKET_SECRET" || names[0].Value != nil {
		t.Errorf("preview secrets = %+v", names)
	}

	var res DeployResult
	got := notedOne(t, l.store, func() {
		res, err = l.svc.InstallTemplate(ctx, "demo", "demo", inputs, TemplateInstallOptions{}, app.TriggerManual)
		if err != nil {
			t.Fatalf("InstallTemplate: %v", err)
		}
		installed(t, l, res)
	})
	wantEntry(t, got, "installed", "app", "demo")
	if c := change(got.Changes, "template"); c == nil || c.To != "demo 2.0-1" {
		t.Errorf("template change = %v", got.Changes)
	}
	if c := change(got.Changes, "choices.signin"); c == nil || c.To != "bitbucket" {
		t.Errorf("the chosen alternative isn't in the entry: %v", got.Changes)
	}
	waitFor(t, "the secrets to be committed", func() bool { owner, _ := l.secrets.Kept("demo"); return owner == "" })

	// Only the chosen provider's secret reached the secret store.
	live := l.secrets.Live("demo")["web"]
	if live["BITBUCKET_SECRET"] != typedAsked || len(live) != 1 {
		t.Errorf("the secret store has %v", live)
	}
	rec, _ := l.svc.GetApp(ctx, "demo")
	env := map[string]string{}
	for _, svc := range rec.App.Services {
		for _, e := range svc.Env {
			env[e.Name] = e.Value
		}
	}
	if env["BITBUCKET_ID"] != "bbbbbbbb1234" || env["SIGNIN"] != "bitbucket" || env["GITHUB_ID"] != "" || env["REGION"] != "" {
		t.Errorf("env = %v", env)
	}
	if rec.Compose == nil || strings.Contains(rec.Compose.Compose, "when") || strings.Contains(rec.Compose.Compose, "GITHUB") {
		t.Errorf("the kept file: %+v", rec.Compose)
	}
	if secrets := rec.Compose.Services["web"].Secrets; len(secrets) != 1 || secrets[0].Name != "BITBUCKET_SECRET" || secrets[0].Value != nil {
		t.Errorf("the kept overlay's secrets = %+v", secrets)
	}

	// The secret of the provider that wasn't chosen is nowhere, and neither is the chosen one.
	blobs := map[string]any{"the preview": preview, "the answer": res, "the app": rec, "the Nomad job": l.nomad.Job("demo"),
		"the audit entry": got, "the deployments": mustList(t, l)}
	for what, v := range blobs {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		for _, secret := range []string{typedUnasked, typedAsked} {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s holds %q", what, secret)
			}
		}
	}
	raw, logs := rawStore(t, l.dir), l.logs.String()
	for _, secret := range []string{typedUnasked, typedAsked} {
		if strings.Contains(raw, secret) {
			t.Errorf("the SQLite file holds %q", secret)
		}
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
	// The unasked provider's plain answer isn't kept either.
	if strings.Contains(raw, "zzzzzzzz9999") {
		t.Error("the SQLite file holds the id typed for a provider that wasn't chosen")
	}
}

func TestInstallTemplateChoiceRefusals(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)
	l.useCatalog(t, futureCatVer, signinWhoami())

	field := func(err error) string {
		var fe *app.FieldError
		if errors.As(err, &fe) {
			return fe.Field + ": " + fe.Message
		}
		return "no field error: " + err.Error()
	}
	tests := []struct {
		name   string
		inputs map[string]string
		want   string
	}{
		{"a choice outside the list", map[string]string{"domain": "demo.example.com", "signin": "svn"}, "inputs.signin: is not one of the choices"},
		{"an input the choice asks for", map[string]string{"domain": "demo.example.com", "signin": "bitbucket", "bb_secret": "x"}, "inputs.bb_id: is required"},
		{"the default choice asks its own", map[string]string{"domain": "demo.example.com"}, "inputs.gh_id: is required"},
		{"an optional answer is still checked", map[string]string{"domain": "demo.example.com", "gh_id": "abcdefgh1234", "gh_secret": "x", "region": "BAD"}, "inputs.region:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := l.svc.InstallTemplate(ctx, "demo", "demo", tt.inputs, TemplateInstallOptions{}, app.TriggerManual)
			if err == nil || !strings.HasPrefix(field(err), tt.want) {
				t.Errorf("error = %q, want it to start with %q", field(err), tt.want)
			}
		})
	}
	t.Run("optional inputs left out install", func(t *testing.T) {
		res, err := l.svc.InstallTemplate(ctx, "demo", "demo", map[string]string{"domain": "demo.example.com", "gh_id": "abcdefgh1234", "gh_secret": "x"}, TemplateInstallOptions{}, app.TriggerManual)
		if err != nil {
			t.Fatal(err)
		}
		installed(t, l, res)
	})
}

// ---- the fetch: format 2 first, format 1 for a release made before it ----

func TestTemplateCatalogCheckFormats(t *testing.T) {
	ctx := context.Background()
	check := func(t *testing.T) (*templateLab, *catalogServer) {
		t.Helper()
		l := newTemplateLab(t)
		cs := newCatalogServer(t)
		l.svc.SetTemplatesURL(cs.URL)
		return l, cs
	}

	t.Run("a release with both files is read from the format-2 one", func(t *testing.T) {
		l, cs := check(t)
		v1 := catalogBundle(t, futureCatVer, goodCatalogTemplate("old-one"))
		v2 := catalogBundle(t, futureCatVer, goodCatalogTemplate("new-one"))
		cs.mu.Lock()
		cs.files["/SHA256SUMS"] = string(templates.Sums(templates.Bundles{V1: v1, V2: v2}))
		cs.files["/catalog.json"] = strings.Replace(string(v1), `"format":2`, `"format":1`, 1)
		cs.files["/catalog.v2.json"] = string(v2)
		cs.mu.Unlock()
		if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(cs.paths(), ","); got != "/SHA256SUMS,/catalog.v2.json" {
			t.Errorf("requests = %s", got)
		}
		if _, err := l.svc.Template(ctx, "new-one"); err != nil {
			t.Errorf("the format-2 catalog wasn't kept: %v", err)
		}
	})

	t.Run("a release made before format 2 is read from catalog.json", func(t *testing.T) {
		l, cs := check(t)
		v1 := strings.Replace(string(catalogBundle(t, futureCatVer, goodCatalogTemplate("old-one"))), `"format":2`, `"format":1`, 1)
		cs.publishOld([]byte(v1))
		if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
			t.Fatalf("check: %v", err)
		}
		if got := strings.Join(cs.paths(), ","); got != "/SHA256SUMS,/catalog.json" {
			t.Errorf("requests = %s: no request for catalog.v2.json is made when SHA256SUMS has no line for it", got)
		}
		view, _ := l.svc.TemplateCatalog(ctx)
		if view.Source != CatalogFetched || view.Version != futureCatVer || view.Error != "" {
			t.Errorf("catalog = %+v", view)
		}
		if _, err := l.svc.Template(ctx, "old-one"); err != nil {
			t.Errorf("old-one: %v", err)
		}
		// The stored format-1 bundle keeps being read.
		l.svc.templates.cacheSum = ""
		if _, err := l.svc.Template(ctx, "old-one"); err != nil {
			t.Errorf("a stored format-1 bundle isn't read back: %v", err)
		}
	})

	t.Run("a format-1 catalog.json with a format-2 field is refused", func(t *testing.T) {
		l, cs := check(t)
		x := goodCatalogTemplate("whoami")
		no := false
		x.Inputs[0].Required = &no
		cs.publishOld([]byte(strings.Replace(string(catalogBundle(t, futureCatVer, x)), `"format":2`, `"format":1`, 1)))
		if err := l.svc.fetchAndRecordTemplates(ctx); err == nil {
			t.Error("accepted")
		}
		if view, _ := l.svc.TemplateCatalog(ctx); view.Error != failCatalogBad || view.Source != CatalogEmbedded {
			t.Errorf("catalog = %+v", view)
		}
	})
}

func TestTemplateCatalogToleratesTemplatesFromTheFuture(t *testing.T) {
	ctx := context.Background()
	l := newTemplateLab(t)
	cs := newCatalogServer(t)
	l.svc.SetTemplatesURL(cs.URL)
	l.svc.runningVersion = "0.22.0"

	good, _ := json.Marshal(goodCatalogTemplate("whoami"))
	future := `{"name": "future", "title": "Future", "version": "9-1", "description": "Needs a newer caravana.", "upstream": "https://example.com",
		"appName": "future", "teleport": true, "compose": "x"}`
	needs := `{"name": "needs", "title": "Needs", "version": "9-1", "description": "Needs 9.9.9.", "upstream": "https://example.com",
		"appName": "needs", "requires": "9.9.9", "inputs": [{"name": "a", "label": "A", "type": "knob"}], "compose": "x"}`
	bundle := `{"format": 2, "version": "` + futureCatVer + `", "templates": [` + string(good) + `,` + future + `,` + needs + `]}`
	cs.publish([]byte(bundle))
	if err := l.svc.fetchAndRecordTemplates(ctx); err != nil {
		t.Fatalf("a catalog with templates from the future failed: %v", err)
	}
	view, _ := l.svc.TemplateCatalog(ctx)
	if view.Source != CatalogFetched || view.Error != "" {
		t.Fatalf("catalog = %+v", view)
	}
	list, _ := l.svc.ListTemplates(ctx, "", "")
	byName := map[string]TemplateInfo{}
	for _, tpl := range list.Templates {
		byName[tpl.Name] = tpl
	}
	if !byName["whoami"].Available {
		t.Errorf("the template of this catalog that can be read isn't offered: %+v", byName["whoami"])
	}
	if f := byName["future"]; f.Available || !strings.Contains(f.Unavailable, "made for a newer caravana") {
		t.Errorf("future = %+v", f)
	}
	if n := byName["needs"]; n.Available || n.Unavailable != "needs caravana 9.9.9 or newer" {
		t.Errorf("needs = %+v", n)
	}
	var unavailable *TemplateUnavailableError
	if _, err := l.svc.InstallTemplate(ctx, "future", "future", map[string]string{}, TemplateInstallOptions{}, app.TriggerManual); !errors.As(err, &unavailable) {
		t.Errorf("an install of one that can't be read: %v", err)
	}
	// After a restart the stored bundle is read the same way.
	l.svc.templates.cacheSum = ""
	if info, err := l.svc.Template(ctx, "future"); err != nil || info.Available {
		t.Errorf("after a restart: %+v, %v", info, err)
	}
}
