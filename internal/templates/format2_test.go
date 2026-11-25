package templates

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// Feature 058: catalog format 2 (choice inputs, optional inputs, conditions, 20 inputs).

func boolPtr(b bool) *bool { return &b }

func is(values ...string) Condition { return Condition{"signin": CondValue{Values: values}} }

// signinTemplate is goodTemplate with a Sign-in choice, each provider's inputs
// asked only for it, their lines of the Compose file under markers, their secrets
// as conditional overlay items, and an optional input.
func signinTemplate() Template {
	secret := func(s string) *string { return &s }
	t := goodTemplate()
	t.Name = "signin"
	t.Notes = "Open https://{{domain}} and sign in with {{signin}}."
	t.Inputs = []Input{
		{Name: "domain", Label: "Domain", Type: TypeDomain, Example: "demo.example.com"},
		{Name: "signin", Label: "Sign-in", Type: TypeChoice, Default: "github", Choices: []Choice{
			{Value: "github", Label: "GitHub"}, {Value: "bitbucket", Label: "Bitbucket Cloud"}, {Value: "gitlab", Label: "GitLab"}}},
		{Name: "gh_id", Label: "GitHub client ID", Type: TypeText, Pattern: `[A-Za-z0-9]{8,20}`, Example: "abcdefgh1234", When: is("github")},
		{Name: "gh_secret", Label: "GitHub client secret", Type: TypeSecret, Example: "gh-example", When: is("github")},
		{Name: "bb_id", Label: "Bitbucket key", Type: TypeText, Pattern: `[A-Za-z0-9]{8,20}`, Example: "bbbbbbbb1234", When: is("bitbucket")},
		{Name: "bb_secret", Label: "Bitbucket secret", Type: TypeSecret, Example: "bb-example", When: is("bitbucket")},
		{Name: "gl_id", Label: "GitLab application ID", Type: TypeText, Pattern: `[A-Za-z0-9]{8,20}`, Example: "gggggggg1234", When: is("gitlab")},
		{Name: "region", Label: "Region", Type: TypeText, Pattern: `[a-z0-9-]{2,20}`, Example: "us-east-1", Required: boolPtr(false)},
	}
	t.Generated = nil
	t.Services = map[string]compose.Overlay{
		"web": {
			Domains: []compose.Domain{{Host: "{{domain}}", Port: 8080}},
			Health:  &compose.Health{Path: "/healthz"},
			Secrets: []app.Secret{
				{Name: "GITHUB_SECRET", Value: secret("{{gh_secret}}")},
				{Name: "BITBUCKET_SECRET", Value: secret("{{bb_secret}}")},
			},
		},
		"db": {},
	}
	t.ItemWhens = map[string]map[string][]Condition{"web": {"secrets": {is("github"), is("bitbucket")}}}
	t.Compose = `services:
  web:
    image: example/demo:1.0.0
    expose: ["8080"]
    environment:
      PUBLIC_URL: "https://{{domain}}"
      SIGNIN: "{{signin}}"
      GITHUB_ID: "{{gh_id}}" # when signin=github
      BITBUCKET_ID: "{{bb_id}}" # when signin=bitbucket
      BITBUCKET_REDIRECT: "https://{{domain}}/auth/callback" # when signin=bitbucket
      GITLAB_ID: "{{gl_id}}" # when signin=gitlab
      REGION: "{{region}}" # when region
  db:
    image: postgres:17
    expose: ["5432"]
    environment:
      POSTGRES_PASSWORD_FILE: /nothing
`
	return t
}

func TestFormat2ParsesAndRoundTrips(t *testing.T) {
	tpl := signinTemplate()
	data := bundleOf(t, "2026.11.25", tpl)
	c, err := ParseCatalog(data)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	got, _ := c.Find("signin")
	if len(got.ItemWhens["web"]["secrets"]) != 2 || got.ItemWhens["web"]["secrets"][1]["signin"].Values[0] != "bitbucket" {
		t.Errorf("ItemWhens = %v", got.ItemWhens)
	}
	if got.Inputs[1].Choices[2].Label != "GitLab" || got.Inputs[7].IsRequired() || !got.Inputs[0].IsRequired() {
		t.Errorf("inputs = %+v", got.Inputs)
	}
	// The overlay's items carry their "when" in the JSON, and the Go types stay clean.
	if !strings.Contains(string(data), `"when":{"signin":"github"}`) {
		t.Errorf("the item's when isn't in the JSON: %s", data)
	}
	again, err := json.Marshal(c)
	if err != nil || string(again) != string(data) {
		t.Errorf("a second marshal differs (%v)", err)
	}
	if err := Check(got, CheckSettings()); err != nil {
		t.Errorf("Check: %v", err)
	}
}

func TestFormat2Refusals(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Template)
		want   string
	}{
		{"twenty-one inputs", func(x *Template) {
			x.Inputs = nil
			x.Compose = "services:\n  web:\n    image: example/demo:1\n"
			for i := 0; i < 21; i++ {
				x.Inputs = append(x.Inputs, Input{Name: "i" + string(rune('a'+i)), Label: "L", Type: TypeSecret, Example: "x"})
			}
		}, "signin: inputs: has 21 inputs; at most 20"},
		{"one choice", func(x *Template) { x.Inputs[1].Choices = x.Inputs[1].Choices[:1] }, "signin: inputs[1].choices: has 1 choices"},
		{"nine choices", func(x *Template) {
			x.Inputs[1].Choices = nil
			for i := 0; i < 9; i++ {
				x.Inputs[1].Choices = append(x.Inputs[1].Choices, Choice{Value: "v" + string(rune('a'+i)), Label: "L"})
			}
			x.Inputs[1].Default = ""
		}, "inputs[1].choices: has 9 choices"},
		{"a bad choice value", func(x *Template) { x.Inputs[1].Choices[0].Value = "GitHub" }, "inputs[1].choices[0].value"},
		{"a choice value twice", func(x *Template) { x.Inputs[1].Choices[1].Value = "github" }, "inputs[1].choices[1].value: \"github\" is listed twice"},
		{"a choice without a label", func(x *Template) { x.Inputs[1].Choices[2].Label = "" }, "inputs[1].choices[2].label"},
		{"a default that isn't a choice", func(x *Template) { x.Inputs[1].Default = "svn" }, "inputs[1].default: \"svn\" is not one of the choices"},
		{"a pattern on a choice", func(x *Template) { x.Inputs[1].Pattern = "a" }, "inputs[1].pattern"},
		{"an example on a choice", func(x *Template) { x.Inputs[1].Example = "github" }, "inputs[1].example"},
		{"choices on a text input", func(x *Template) { x.Inputs[0].Choices = x.Inputs[1].Choices }, "inputs[0].choices: only a choice input"},
		{"a secret choice is no thing", func(x *Template) { x.Inputs[1].Type = TypeSecret }, "inputs[1].choices"},
		{"a when naming nothing", func(x *Template) { x.Inputs[2].When = Condition{"nope": {Values: []string{"github"}}} }, `inputs[2].when: names "nope", which is not an input`},
		{"a when naming a later input", func(x *Template) { x.Inputs[0].When = Condition{"signin": {Values: []string{"github"}}} }, "inputs[0].when: names \"signin\", which must be declared before"},
		{"a when naming itself", func(x *Template) { x.Inputs[1].When = Condition{"signin": {Values: []string{"github"}}} }, "inputs[1].when"},
		{"a when on a text input's value", func(x *Template) { x.Inputs[3].When = Condition{"domain": {Values: []string{"x"}}} }, `inputs[3].when: "domain" isn't a choice input`},
		{"a when value that isn't a choice", func(x *Template) { x.Inputs[2].When = is("svn") }, `inputs[2].when: "svn" is not a choice of "signin"`},
		{"a when that says true", func(x *Template) { x.Inputs[2].When = Condition{"signin": {Any: true}} }, "inputs[2].when"},
		{"an empty list of values", func(x *Template) { x.Inputs[2].When = Condition{"signin": {Values: []string{}}} }, "inputs[2].when"},
		{"too many combinations", func(x *Template) {
			for i := 0; i < 3; i++ {
				x.Inputs = append(x.Inputs, Input{Name: "c" + string(rune('a'+i)), Label: "C", Type: TypeChoice, Choices: []Choice{
					{Value: "a", Label: "A"}, {Value: "b", Label: "B"}, {Value: "c", Label: "C"}, {Value: "d", Label: "D"}}})
				x.Compose += "# {{c" + string(rune('a'+i)) + "}}\n"
			}
		}, "its choices make 192 combinations; at most 64"},
		{"an overlay item's when names nothing", func(x *Template) {
			x.ItemWhens["web"]["secrets"][0] = Condition{"nope": {Any: true}}
		}, `services.web.secrets[0].when: names "nope"`},
		{"an overlay item's when value that isn't a choice", func(x *Template) {
			x.ItemWhens["web"]["secrets"][1] = is("svn")
		}, `services.web.secrets[1].when: "svn" is not a choice of "signin"`},
		{"an optional input that is never used", func(x *Template) {
			x.Inputs = append(x.Inputs, Input{Name: "extra", Label: "Extra", Type: TypeText, Pattern: "a", Example: "a", Required: boolPtr(false)})
		}, "inputs.extra: is declared but never used"},
		{"a marker naming nothing", func(x *Template) {
			x.Compose = strings.Replace(x.Compose, "# when region", "# when nope", 1)
		}, `signin: compose: line 12: names "nope", which is not an input`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := signinTemplate()
			tt.change(&x)
			_, err := ParseCatalog(bundleOf(t, "2026.11.25", x))
			if err == nil {
				t.Fatal("ParseCatalog accepted it")
			}
			var te *Error
			if !errors.As(err, &te) {
				t.Fatalf("error %T isn't an *Error: %v", err, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

// TestFormat1StaysFormat1: a format-1 catalog refuses everything format 2 adds,
// naming the template and the field, so catalog.json never holds any of it.
func TestFormat1StaysFormat1(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Template)
		want   string
	}{
		{"thirteen inputs", func(x *Template) {
			x.Inputs = x.Inputs[:0]
			x.Compose = "services:\n  web:\n    image: example/demo:1\n"
			for i := 0; i < 13; i++ {
				x.Inputs = append(x.Inputs, Input{Name: "i" + string(rune('a'+i)), Label: "L", Type: TypeSecret, Example: "x"})
			}
		}, "has 13 inputs; at most 12"},
		{"required", func(x *Template) { x.Inputs[0].Required = boolPtr(false) }, "inputs[0]: uses required, choices or when"},
		{"a choice input", func(x *Template) { x.Inputs[0] = signinTemplate().Inputs[1] }, "inputs[0]"},
		{"a when on an input", func(x *Template) { x.Inputs[0].When = Condition{"x": {Any: true}} }, "inputs[0]: uses required, choices or when"},
		{"a marker", func(x *Template) { x.Compose = strings.Replace(x.Compose, "BUCKET: \"{{bucket}}\"", "BUCKET: \"{{bucket}}\" # when domain", 1) },
			"a marker comment is catalog format 2"},
		{"an overlay item's when", func(x *Template) {
			x.ItemWhens = map[string]map[string][]Condition{"web": {"secrets": {Condition{"domain": {Any: true}}, nil}}}
		}, "services.web.secrets[0].when: a condition is catalog format 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := goodTemplate()
			tt.change(&x)
			data := bundleOf(t, "2026.11.25", x)
			data = []byte(strings.Replace(string(data), `"format":2`, `"format":1`, 1))
			_, err := ParseCatalog(data)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	// And the good template is valid in both.
	for _, format := range []int{1, 2} {
		data := strings.Replace(string(bundleOf(t, "2026.11.25", goodTemplate())), `"format":2`, `"format":`+string(rune('0'+format)), 1)
		if _, err := ParseCatalog([]byte(data)); err != nil {
			t.Errorf("format %d: %v", format, err)
		}
	}
}

func TestCheckInputsFormat2(t *testing.T) {
	tpl := signinTemplate()
	base := map[string]string{"domain": "who.example.com", "gh_id": "abcdefgh1234", "gh_secret": "s"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}

	t.Run("the default choice asks its inputs; the others are empty", func(t *testing.T) {
		got, err := tpl.CheckInputs(with())
		if err != nil {
			t.Fatal(err)
		}
		if got["signin"] != "github" || got["gh_id"] != "abcdefgh1234" || got["bb_id"] != "" || got["bb_secret"] != "" || got["region"] != "" {
			t.Errorf("answers = %v", got)
		}
	})
	t.Run("a choice outside the list is refused at the input", func(t *testing.T) {
		_, err := tpl.CheckInputs(with("signin", "svn"))
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "inputs.signin" || !strings.Contains(fe.Message, "not one of the choices") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("an input asked is required", func(t *testing.T) {
		_, err := tpl.CheckInputs(with("signin", "bitbucket"))
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "inputs.bb_id" || fe.Message != "is required" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("what is sent for an input that isn't asked is ignored, even junk", func(t *testing.T) {
		got, err := tpl.CheckInputs(with("bb_id", "not valid at all!!", "bb_secret", "s3cret-sentinel-058", "gl_id", "x"))
		if err != nil {
			t.Fatal(err)
		}
		if got["bb_id"] != "" || got["bb_secret"] != "" || got["gl_id"] != "" {
			t.Errorf("answers = %v", got)
		}
		for k, v := range got {
			if strings.Contains(v, "s3cret-sentinel-058") {
				t.Errorf("%s keeps the unasked secret", k)
			}
		}
	})
	t.Run("a name the template doesn't have is still refused", func(t *testing.T) {
		if _, err := tpl.CheckInputs(with("nope", "x")); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("an optional input may be empty and may be given", func(t *testing.T) {
		got, _ := tpl.CheckInputs(with())
		if got["region"] != "" {
			t.Errorf("region = %q", got["region"])
		}
		got, err := tpl.CheckInputs(with("region", "eu-west-1"))
		if err != nil || got["region"] != "eu-west-1" {
			t.Errorf("region = %q, %v", got["region"], err)
		}
		if _, err := tpl.CheckInputs(with("region", "BAD")); err == nil {
			t.Error("a bad optional answer was accepted")
		}
	})
	t.Run("an optional secret left empty is empty", func(t *testing.T) {
		x := signinTemplate()
		x.Inputs[3].Required = boolPtr(false)
		got, err := x.CheckInputs(with("gh_secret", ""))
		if err != nil || got["gh_secret"] != "" {
			t.Errorf("gh_secret = %q, %v", got["gh_secret"], err)
		}
	})
	t.Run("Asked says which inputs the answers ask for", func(t *testing.T) {
		asked := tpl.Asked(map[string]string{"signin": "gitlab"})
		if !asked["gl_id"] || asked["gh_id"] || asked["bb_secret"] || !asked["region"] || !asked["signin"] {
			t.Errorf("asked = %v", asked)
		}
		if asked := tpl.Asked(nil); !asked["gh_id"] || asked["gl_id"] {
			t.Errorf("the default asks github: %v", asked)
		}
	})
	t.Run("a nested when follows the earlier answer", func(t *testing.T) {
		x := signinTemplate()
		x.Inputs[1].When = nil
		// gh_id is asked for github; make gl_id ask for a choice that is itself conditional.
		x.Inputs = append(x.Inputs[:3], append([]Input{{Name: "mode", Label: "Mode", Type: TypeChoice, When: is("github"),
			Choices: []Choice{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}, Default: "a"}}, x.Inputs[3:]...)...)
		x.Compose += "# {{mode}}\n"
		got, err := x.CheckInputs(with("signin", "gitlab", "gl_id", "gggggggg1234"))
		if err != nil || got["mode"] != "" {
			t.Errorf("mode = %q, %v", got["mode"], err)
		}
	})
}

func TestExampleInputsFormat2(t *testing.T) {
	ex := signinTemplate().ExampleInputs()
	if ex["signin"] != "github" || ex["region"] != "us-east-1" {
		t.Errorf("ExampleInputs = %v", ex)
	}
	x := signinTemplate()
	x.Inputs[1].Default = ""
	if got := x.ExampleInputs()["signin"]; got != "github" {
		t.Errorf("a choice without a default takes its first: %q", got)
	}
}

func TestRenderConditions(t *testing.T) {
	tpl := signinTemplate()
	render := func(t *testing.T, choice string, extra ...string) (compose.Request, string) {
		t.Helper()
		answers := map[string]string{"domain": "who.example.com", "signin": choice,
			"gh_id": "abcdefgh1234", "gh_secret": "ghs", "bb_id": "bbbbbbbb1234", "bb_secret": "bbs", "gl_id": "gggggggg1234"}
		for i := 0; i+1 < len(extra); i += 2 {
			answers[extra[i]] = extra[i+1]
		}
		checked, err := tpl.CheckInputs(answers)
		if err != nil {
			t.Fatal(err)
		}
		req, err := tpl.Render(checked)
		if err != nil {
			t.Fatal(err)
		}
		names := ""
		for _, s := range req.Services["web"].Secrets {
			names += s.Name + "=" + *s.Value + ";"
		}
		return req, names
	}
	t.Run("github keeps its lines and its secret", func(t *testing.T) {
		req, secrets := render(t, "github")
		if !strings.Contains(req.Compose, `GITHUB_ID: "abcdefgh1234"`) || strings.Contains(req.Compose, "BITBUCKET") || strings.Contains(req.Compose, "GITLAB") ||
			strings.Contains(req.Compose, "REGION") {
			t.Errorf("compose:\n%s", req.Compose)
		}
		if strings.Contains(req.Compose, "# when") || strings.Contains(req.Compose, "when") {
			t.Errorf("a marker is left in the file:\n%s", req.Compose)
		}
		if secrets != "GITHUB_SECRET=ghs;" {
			t.Errorf("secrets = %q", secrets)
		}
	})
	t.Run("bitbucket keeps two lines, one a plain placeholder", func(t *testing.T) {
		req, secrets := render(t, "bitbucket")
		for _, want := range []string{`BITBUCKET_ID: "bbbbbbbb1234"`, `BITBUCKET_REDIRECT: "https://who.example.com/auth/callback"`} {
			if !strings.Contains(req.Compose, want) {
				t.Errorf("missing %q in:\n%s", want, req.Compose)
			}
		}
		if strings.Contains(req.Compose, "GITHUB") || secrets != "BITBUCKET_SECRET=bbs;" {
			t.Errorf("compose:\n%s\nsecrets %q", req.Compose, secrets)
		}
	})
	t.Run("gitlab has no secret at all, and no empty list is left", func(t *testing.T) {
		req, secrets := render(t, "gitlab")
		if secrets != "" || req.Services["web"].Secrets != nil || !strings.Contains(req.Compose, "GITLAB_ID") {
			t.Errorf("secrets %q (%v)\n%s", secrets, req.Services["web"].Secrets, req.Compose)
		}
	})
	t.Run("an optional input given keeps its line, empty drops it", func(t *testing.T) {
		req, _ := render(t, "github", "region", "eu-west-1")
		if !strings.Contains(req.Compose, `REGION: "eu-west-1"`) {
			t.Errorf("compose:\n%s", req.Compose)
		}
	})
	t.Run("the template itself is never changed", func(t *testing.T) {
		render(t, "bitbucket")
		if len(tpl.Services["web"].Secrets) != 2 || !strings.Contains(tpl.Compose, "# when signin=github") {
			t.Error("rendering changed the template")
		}
	})
	t.Run("a typed secret of another provider reaches nothing", func(t *testing.T) {
		req, _ := render(t, "github", "bb_secret", "s3cret-sentinel-058-bb")
		blob, _ := json.Marshal(req)
		if strings.Contains(string(blob), "s3cret-sentinel-058-bb") {
			t.Errorf("the unasked secret is in the request: %s", blob)
		}
	})
	t.Run("DomainInputsFor follows the kept domains", func(t *testing.T) {
		x := signinTemplate()
		x.Services["web"] = compose.Overlay{Domains: []compose.Domain{{Host: "other.example.com", Port: 1}, {Host: "{{domain}}", Port: 8080}}, Health: x.Services["web"].Health, Secrets: x.Services["web"].Secrets}
		x.ItemWhens["web"]["domains"] = []Condition{is("gitlab"), nil}
		if got := x.DomainInputsFor(map[string]string{"signin": "github", "domain": "d"}); got["services.web.domains[0].host"] != "domain" {
			t.Errorf("github: %v", got)
		}
		if got := x.DomainInputsFor(map[string]string{"signin": "gitlab", "domain": "d"}); got["services.web.domains[1].host"] != "domain" {
			t.Errorf("gitlab: %v", got)
		}
	})
}

func TestCheckRendersEveryCombination(t *testing.T) {
	if err := Check(signinTemplate(), CheckSettings()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	cases := checkCases(signinTemplate())
	// 3 choices, optional inputs empty and filled.
	if len(cases) != 6 {
		t.Fatalf("%d cases, want 6", len(cases))
	}
	t.Run("a combination that renders a warning names the answers", func(t *testing.T) {
		x := signinTemplate()
		x.Compose = strings.Replace(x.Compose, `GITLAB_ID: "{{gl_id}}" # when signin=gitlab`,
			"GITLAB_ID: \"{{gl_id}}\"\n    restart: \"no\" # when signin=gitlab", 1)
		err := Check(x, CheckSettings())
		if err == nil || !strings.Contains(err.Error(), "with signin=gitlab") || !strings.Contains(err.Error(), "services.web.restart") {
			t.Fatalf("Check = %v", err)
		}
	})
	t.Run("a required optional-less template is checked once", func(t *testing.T) {
		if n := len(checkCases(goodTemplate())); n != 1 {
			t.Errorf("%d cases", n)
		}
	})
	t.Run("an input only an unasked branch needs doesn't fail the others", func(t *testing.T) {
		// signinTemplate passing Check already shows it: gh_id is empty under bitbucket.
		if err := Check(signinTemplate(), CheckSettings()); err != nil {
			t.Error(err)
		}
	})
}

func TestMarkers(t *testing.T) {
	values := map[string]string{"signin": "bitbucket", "region": "", "name": "x"}
	render := func(text string) (string, error) { return applyMarkers(text, values) }
	good := []struct{ name, in, want string }{
		{"kept", "a:\n  B: 1 # when signin=bitbucket\n", "a:\n  B: 1\n"},
		{"dropped", "a:\n  B: 1 # when signin=github\n  C: 2\n", "a:\n  C: 2\n"},
		{"a list of values", "a:\n  B: 1 # when signin=github,bitbucket\n", "a:\n  B: 1\n"},
		{"non-empty holds", "a:\n  B: 1 # when name\n", "a:\n  B: 1\n"},
		{"non-empty fails for an empty input", "a:\n  B: 1 # when region\n  C: 2\n", "a:\n  C: 2\n"},
		{"two clauses must both hold", "a:\n  B: 1 # when signin=bitbucket name\n  C: 2 # when signin=bitbucket region\n", "a:\n  B: 1\n"},
		{"a list item", "a:\n  - x # when signin=bitbucket\n  - y # when signin=github\n", "a:\n  - x\n"},
		{"a quoted value that has a hash", "a:\n  B: \"a # b\" # when signin=bitbucket\n", "a:\n  B: \"a # b\"\n"},
		{"a flow list", "a:\n  B: [1, 2] # when signin=bitbucket\n", "a:\n  B: [1, 2]\n"},
		{"a list item that is a path", "a:\n  - ./data:/data # when signin=bitbucket\n", "a:\n  - ./data:/data\n"},
		{"CRLF", "a:\r\n  B: 1 # when signin=bitbucket\r\n  C: 2 # when signin=github\r\n", "a:\r\n  B: 1\r\n"},
	}
	for _, tt := range good {
		t.Run(tt.name, func(t *testing.T) {
			got, err := render(tt.in)
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	bad := []struct{ name, in, want string }{
		{"a marker on its own line", "a:\n  # when signin=github\n  B: 1\n", "line 2: a marker comment belongs at the end"},
		{"a block key", "a:\n  b: # when signin=github\n    c: 1\n", "line 2: a marker can't follow a key that has a block"},
		{"a block scalar", "a:\n  b: | # when signin=github\n    text\n", "line 2: a marker can't follow a block scalar"},
		{"the first key of a mapping item", "a:\n  - name: x # when signin=github\n    b: 1\n", "line 2: a marker can't follow the first key of a list item"},
		{"inside a quoted string", "a:\n  B: \"x # when signin=github\"\n", "line 2: the marker is inside a quoted string"},
		{"a flow list that goes on", "a:\n  B: [1, # when signin=github\n   2]\n", "line 2: a marker can't follow a value that goes on over several lines"},
		{"not an entry", "a: # when signin=github\n", "line 1: a marker can't follow a key"},
		{"a bad clause", "a:\n  B: 1 # when Signin=github\n", "line 2: \"Signin=github\" isn't a condition"},
		{"a clause twice", "a:\n  B: 1 # when signin=a signin=b\n", "line 2: \"signin\" is named twice"},
		{"an empty list item", "a:\n  - # when signin=github\n", "line 2"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := render(tt.in)
			var me *MarkerError
			if !errors.As(err, &me) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	t.Run("an ordinary trailing comment stays a comment", func(t *testing.T) {
		got, err := render("a:\n  B: 1 # the value\n")
		if err != nil || got != "a:\n  B: 1 # the value\n" {
			t.Errorf("got %q, %v", got, err)
		}
	})
	t.Run("an illegal placement is a format error naming template and line", func(t *testing.T) {
		x := signinTemplate()
		x.Compose = strings.Replace(x.Compose, "    environment:\n      PUBLIC_URL", "    environment: # when signin=github\n      PUBLIC_URL", 1)
		_, err := ParseCatalog(bundleOf(t, "2026.11.25", x))
		if err == nil || !strings.Contains(err.Error(), "signin: compose: line 5: a marker can't follow a key that has a block") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestTemplateJSONWhen(t *testing.T) {
	const manifest = `{"name": "w", "title": "W", "version": "1", "description": "d", "upstream": "https://e.com", "appName": "w",
  "inputs": [{"name": "c", "label": "C", "type": "choice", "choices": [{"value": "a", "label": "A"}, {"value": "b", "label": "B"}], "default": "a"}],
  "services": {"web": {"secrets": [{"name": "X", "value": "{{c}}", "when": {"c": "a"}}, {"name": "Y", "value": "y"}],
                       "domains": [{"host": "h.example.com", "when": {"c": ["a", "b"]}}]}}}`
	var tpl Template
	if err := json.Unmarshal([]byte(manifest), &tpl); err != nil {
		t.Fatal(err)
	}
	if w := tpl.ItemWhens["web"]["secrets"]; len(w) != 2 || w[0]["c"].Values[0] != "a" || w[1] != nil {
		t.Errorf("secrets whens = %v", w)
	}
	if w := tpl.ItemWhens["web"]["domains"]; len(w) != 1 || len(w[0]["c"].Values) != 2 {
		t.Errorf("domains whens = %v", w)
	}
	if tpl.Services["web"].Secrets[0].Name != "X" {
		t.Errorf("the secret = %+v", tpl.Services["web"].Secrets)
	}
	services, err := tpl.ServicesJSON()
	if err != nil || !strings.Contains(string(services["web"]), `"when":{"c":"a"}`) || !strings.Contains(string(services["web"]), `"when":{"c":["a","b"]}`) {
		t.Errorf("ServicesJSON = %s, %v", services["web"], err)
	}
	t.Run("an empty when is a format error", func(t *testing.T) {
		var x Template
		if err := json.Unmarshal([]byte(strings.Replace(manifest, `{"c": "a"}`, `{}`, 1)), &x); err != nil {
			t.Fatal(err)
		}
		x.Compose = "services:\n  web:\n    image: example/w:1\n"
		err := x.Validate()
		if err == nil || !strings.Contains(err.Error(), "services.web.secrets[0].when: must name at least one input") {
			t.Errorf("Validate = %v", err)
		}
	})
	t.Run("an unknown field is refused, a bad when too", func(t *testing.T) {
		for _, bad := range []string{
			strings.Replace(manifest, `"title": "W"`, `"title": "W", "surprise": 1`, 1),
			strings.Replace(manifest, `{"c": "a"}`, `{"c": false}`, 1),
			strings.Replace(manifest, `"name": "Y", "value": "y"`, `"name": "Y", "value": "y", "surprise": 1`, 1),
		} {
			if err := json.Unmarshal([]byte(bad), &Template{}); err == nil {
				t.Errorf("accepted %.60s…", bad)
			}
		}
	})
	t.Run("a template with no when marshals as before", func(t *testing.T) {
		a, _ := json.Marshal(goodTemplate())
		if strings.Contains(string(a), "when") || strings.Contains(string(a), "required") || strings.Contains(string(a), "choices") {
			t.Errorf("format-2 fields leaked into a format-1 template: %s", a)
		}
	})
}
