package templates

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

func fieldOf(t *testing.T, err error) *app.FieldError {
	t.Helper()
	var fe *app.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("error %T isn't an *app.FieldError: %v", err, err)
	}
	return fe
}

func TestCheckInputs(t *testing.T) {
	tpl := goodTemplate()
	good := map[string]string{"domain": "demo.example.com", "admin": "you@example.com", "api_key": "k"}

	t.Run("trims, lowercases a domain and applies a default", func(t *testing.T) {
		got, err := tpl.CheckInputs(map[string]string{
			"domain": "  Demo.Example.COM ", "admin": " you@example.com ", "api_key": " k ", "bucket": "  ",
		})
		if err != nil {
			t.Fatalf("CheckInputs: %v", err)
		}
		want := map[string]string{"domain": "demo.example.com", "admin": "you@example.com", "bucket": "demo-bucket", "api_key": "k"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	tests := []struct {
		name  string
		input map[string]string
		field string
	}{
		{"unknown input", map[string]string{"domain": "a.example.com", "admin": "a@b.co", "api_key": "k", "nope": "x"}, "inputs.nope"},
		{"missing domain", map[string]string{"admin": "a@b.co", "api_key": "k"}, "inputs.domain"},
		{"domain with spaces", map[string]string{"domain": "not a domain", "admin": "a@b.co", "api_key": "k"}, "inputs.domain"},
		{"domain without a dot", map[string]string{"domain": "localhost", "admin": "a@b.co", "api_key": "k"}, "inputs.domain"},
		{"domain with a hyphen at the start", map[string]string{"domain": "-a.example.com", "admin": "a@b.co", "api_key": "k"}, "inputs.domain"},
		{"domain too long", map[string]string{"domain": strings.Repeat("a.", 130) + "com", "admin": "a@b.co", "api_key": "k"}, "inputs.domain"},
		{"domain with a port", map[string]string{"domain": "a.example.com:8080", "admin": "a@b.co", "api_key": "k"}, "inputs.domain"},
		{"email without @", map[string]string{"domain": "a.example.com", "admin": "you.example.com", "api_key": "k"}, "inputs.admin"},
		{"email with a bad domain", map[string]string{"domain": "a.example.com", "admin": "you@localhost", "api_key": "k"}, "inputs.admin"},
		{"email with a %", map[string]string{"domain": "a.example.com", "admin": "a%b@example.com", "api_key": "k"}, "inputs.admin"},
		{"text not matching", map[string]string{"domain": "a.example.com", "admin": "a@b.co", "api_key": "k", "bucket": "UP"}, "inputs.bucket"},
		{"text matching only in part", map[string]string{"domain": "a.example.com", "admin": "a@b.co", "api_key": "k", "bucket": "ok-name!"}, "inputs.bucket"},
		{"missing secret", map[string]string{"domain": "a.example.com", "admin": "a@b.co"}, "inputs.api_key"},
		{"secret with a newline", map[string]string{"domain": "a.example.com", "admin": "a@b.co", "api_key": "a\nb"}, "inputs.api_key"},
		{"secret with NUL", map[string]string{"domain": "a.example.com", "admin": "a@b.co", "api_key": "a\x00b"}, "inputs.api_key"},
		{"secret too long", map[string]string{"domain": "a.example.com", "admin": "a@b.co", "api_key": strings.Repeat("k", 4097)}, "inputs.api_key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tpl.CheckInputs(tt.input)
			if err == nil {
				t.Fatal("CheckInputs accepted it")
			}
			if fe := fieldOf(t, err); fe.Field != tt.field {
				t.Errorf("field = %q, want %q (%v)", fe.Field, tt.field, err)
			}
		})
	}

	t.Run("a secret may be long and have any character", func(t *testing.T) {
		in := map[string]string{"domain": "a.example.com", "admin": "a@b.co", "api_key": `p"a ss#$w{{x}}'` + strings.Repeat("k", 4000)}
		if _, err := tpl.CheckInputs(in); err != nil {
			t.Errorf("CheckInputs: %v", err)
		}
	})

	t.Run("a secret input is optional in a preview", func(t *testing.T) {
		got, err := tpl.CheckPreviewInputs(map[string]string{"domain": "a.example.com", "admin": "a@b.co"})
		if err != nil || got["api_key"] != "" {
			t.Errorf("CheckPreviewInputs = %v, %v", got, err)
		}
		if _, err := tpl.CheckPreviewInputs(map[string]string{"admin": "a@b.co"}); err == nil {
			t.Error("a preview without the domain was accepted")
		}
	})

	t.Run("the error never holds a secret's value", func(t *testing.T) {
		const secret = "s3cret-sentinel-035-input"
		in := map[string]string{"domain": "a.example.com", "admin": "a@b.co", "api_key": "a\n" + secret}
		_, err := tpl.CheckInputs(in)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Errorf("error = %v", err)
		}
	})
	_ = good
}

// TestCheckInputsComposeSafe: a plain value can't change a YAML file's
// structure, so what is outside the Compose-safe set is refused for every
// plain type, with a pattern that would otherwise let it through.
func TestCheckInputsComposeSafe(t *testing.T) {
	tpl := Template{Inputs: []Input{
		{Name: "t", Label: "T", Type: TypeText, Pattern: `.*`, Example: "x"},
	}}
	for _, v := range []string{"a\nservices:", "x # y", "${HOME}", "{{other}}", "a b", `a"b`, "a'b", "a,b", "a[b]", "a{b}", "a&b", "a*b", "a!b", "a|b", "a>b", "a%b", "a\\b", "a`b", "a;b", "a<b", "a?b", "a~b", "a^b", "a(b)", "a$b"} {
		if _, err := tpl.CheckInputs(map[string]string{"t": v}); err == nil {
			t.Errorf("%q was accepted", v)
		}
	}
	for _, v := range []string{"ghcr.io/acme/app:1.2.3", "a_b-c.d", "x@y+z=1", "A"} {
		if _, err := tpl.CheckInputs(map[string]string{"t": v}); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
	if _, err := tpl.CheckInputs(map[string]string{"t": strings.Repeat("a", 254)}); err == nil {
		t.Error("a 254-byte value was accepted")
	}
}

// counting yields 0, 1, 2 ... 255, 0 ... so a sampler's output can be
// computed by hand.
type counting struct{ n int }

func (c *counting) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(c.n)
		c.n++
	}
	return len(p), nil
}

func TestGenerate(t *testing.T) {
	tpl := Template{Generated: []Generated{
		{Name: "a", Length: 16, Alphabet: AlphabetHex},
		{Name: "b", Length: 32, Alphabet: AlphabetAlnum},
		{Name: "c", Length: 128, Alphabet: AlphabetBase64URL},
	}}
	got, err := tpl.Generate(&counting{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, g := range tpl.Generated {
		v := got[g.Name]
		if len(v) != g.Length {
			t.Errorf("%s has %d characters, want %d", g.Name, len(v), g.Length)
		}
		for _, r := range v {
			if !strings.ContainsRune(alphabets[g.Alphabet], r) {
				t.Errorf("%s has %q, outside its alphabet", g.Name, r)
			}
		}
	}
	// 0..31 over the 16 hex characters: the first 16 bytes are 0..15.
	if got["a"] != "0123456789abcdef" {
		t.Errorf("hex value = %q, want the first 16 characters of the alphabet from bytes 0..15", got["a"])
	}

	t.Run("the same reader gives the same secrets", func(t *testing.T) {
		again, err := tpl.Generate(&counting{})
		if err != nil || !reflect.DeepEqual(got, again) {
			t.Errorf("second call = %v, %v", again, err)
		}
	})

	t.Run("a sentinel reader gives a sentinel secret", func(t *testing.T) {
		got, err := tpl.Generate(bytes.NewReader(bytes.Repeat([]byte{0}, 4096)))
		if err != nil || got["a"] != strings.Repeat("0", 16) {
			t.Errorf("Generate = %v, %v", got, err)
		}
	})

	t.Run("a reader that fails is an error that holds no value", func(t *testing.T) {
		_, err := tpl.Generate(io.LimitReader(&counting{}, 5))
		if err == nil || !strings.Contains(err.Error(), `generate "a"`) {
			t.Errorf("error = %v", err)
		}
	})

	t.Run("an unknown alphabet", func(t *testing.T) {
		bad := Template{Generated: []Generated{{Name: "x", Length: 16, Alphabet: "emoji"}}}
		if _, err := bad.Generate(&counting{}); err == nil {
			t.Error("accepted")
		}
	})
}

// TestSampleHasNoModuloBias: over a 10-character alphabet 256 isn't a multiple
// of 10, so a plain `b % 10` favours 0-5 (260 against 250 in ten rounds). The
// reader yields every byte value once per 256 reads; only the bytes below 250 may count.
func TestSampleHasNoModuloBias(t *testing.T) {
	const alphabet = "0123456789"
	r := &counting{}
	out, err := sample(r, alphabet, 2500)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[rune]int{}
	for _, c := range out {
		counts[c]++
	}
	for _, c := range alphabet {
		if counts[c] != 250 {
			t.Errorf("%q appears %d times, want 250: 2500 accepted bytes are 10 rounds of 0..249, 25 per character each round", c, counts[c])
		}
	}

	// Bytes 250..255 are never used: a reader of only those never finishes.
	_, err = sample(bytes.NewReader(bytes.Repeat([]byte{255}, 1000)), alphabet, 4)
	if err == nil {
		t.Error("bytes above the limit were used")
	}
}

func TestRender(t *testing.T) {
	tpl := goodTemplate()
	inputs, err := tpl.CheckInputs(map[string]string{"domain": "Demo.Example.com", "admin": "you@example.com", "api_key": "k-1"})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"db_password": "DBPASS"}
	for k, v := range inputs {
		values[k] = v
	}
	req, err := tpl.Render(values)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if req.Name != "" {
		t.Errorf("Name = %q, want empty", req.Name)
	}
	for _, want := range []string{`ADMIN_EMAIL: "you@example.com"`, `BUCKET: "demo-bucket"`, `PUBLIC_URL: "https://demo.example.com"`} {
		if !strings.Contains(req.Compose, want) {
			t.Errorf("the file has no %q:\n%s", want, req.Compose)
		}
	}
	if strings.Contains(req.Compose, "{{") || strings.Contains(req.Compose, "DBPASS") || strings.Contains(req.Compose, "k-1") {
		t.Errorf("the file still has a placeholder, or a secret:\n%s", req.Compose)
	}
	web := req.Services["web"]
	if web.Domains[0].Host != "demo.example.com" || web.Domains[0].Port != 8080 {
		t.Errorf("domain = %+v", web.Domains[0])
	}
	if web.Secrets[0].Name != "DATABASE_URL" || *web.Secrets[0].Value != "postgres://demo:DBPASS@db:5432/demo" {
		t.Errorf("secret 0 = %s=%v", web.Secrets[0].Name, *web.Secrets[0].Value)
	}
	if *web.Secrets[1].Value != "k-1" {
		t.Errorf("secret 1 = %v", *web.Secrets[1].Value)
	}
	if *req.Services["db"].Secrets[0].Value != "DBPASS" {
		t.Errorf("db secret = %v", *req.Services["db"].Secrets[0].Value)
	}

	t.Run("rendering never changes the template", func(t *testing.T) {
		if *tpl.Services["web"].Secrets[0].Value != "postgres://demo:{{db_password}}@db:5432/demo" ||
			tpl.Services["web"].Domains[0].Host != "{{domain}}" {
			t.Errorf("the template was written to: %+v", tpl.Services["web"])
		}
		*req.Services["web"].Secrets[1].Value = "changed"
		if *tpl.Services["web"].Secrets[1].Value != "{{api_key}}" {
			t.Error("the request shares a secret value with the template")
		}
	})

	t.Run("the kept request has no secret value", func(t *testing.T) {
		kept := req.WithoutSecretValues()
		for svc, ov := range kept.Services {
			for _, s := range ov.Secrets {
				if s.Value != nil {
					t.Errorf("%s/%s kept a value", svc, s.Name)
				}
			}
		}
	})

	t.Run("a missing value is an error naming the input", func(t *testing.T) {
		_, err := tpl.Render(map[string]string{"domain": "a.example.com"})
		if fe := fieldOf(t, err); !strings.HasPrefix(fe.Field, "inputs.") {
			t.Errorf("field = %q", fe.Field)
		}
	})
}

// TestRenderInjection: a value is substituted once and never looked at again,
// and a secret travels as a string leaf, so none of these changes the file's
// or the overlay's structure.
func TestRenderInjection(t *testing.T) {
	tpl := goodTemplate()
	base := map[string]string{"domain": "demo.example.com", "admin": "you@example.com", "bucket": "demo-bucket", "db_password": "p"}

	t.Run("a secret that looks like a placeholder stays text", func(t *testing.T) {
		values := map[string]string{"api_key": "{{db_password}}"}
		for k, v := range base {
			values[k] = v
		}
		req, err := tpl.Render(values)
		if err != nil {
			t.Fatal(err)
		}
		if got := *req.Services["web"].Secrets[1].Value; got != "{{db_password}}" {
			t.Errorf("value = %q: it was substituted again", got)
		}
	})

	t.Run("a secret with quotes, newlines and JSON stays one string", func(t *testing.T) {
		const nasty = "a\"},{\"name\":\"X\nservices:\n  evil: {}\n# \\u0000"
		values := map[string]string{"api_key": nasty}
		for k, v := range base {
			values[k] = v
		}
		req, err := tpl.Render(values)
		if err != nil {
			t.Fatal(err)
		}
		web := req.Services["web"]
		if len(web.Secrets) != 2 || *web.Secrets[1].Value != nasty {
			t.Errorf("secrets = %+v", web.Secrets)
		}
		if strings.Contains(req.Compose, "evil") {
			t.Error("a secret reached the Compose file")
		}
	})

	t.Run("plain values that would open a structure never get that far", func(t *testing.T) {
		for _, v := range []string{"a\nservices:", "x # y", "${HOME}", "{{other}}"} {
			in := map[string]string{"domain": "demo.example.com", "admin": "you@example.com", "api_key": "k", "bucket": v}
			if _, err := tpl.CheckInputs(in); err == nil {
				t.Errorf("bucket %q was accepted", v)
			}
		}
	})
}

func TestRenderNotes(t *testing.T) {
	tpl := goodTemplate()
	if got := tpl.RenderNotes(map[string]string{"domain": "a.example.com"}); got != "Open https://a.example.com now." {
		t.Errorf("with the domain: %q", got)
	}
	if got := tpl.RenderNotes(nil); got != "Open https://<your domain> now." {
		t.Errorf("without: %q", got)
	}
	tpl.Notes = "Mail {{admin}}."
	if got := tpl.RenderNotes(nil); got != "Mail <your admin email>." {
		t.Errorf("label words: %q", got)
	}
}

func TestExampleInputsAndPreviewValues(t *testing.T) {
	tpl := goodTemplate()
	ex := tpl.ExampleInputs()
	want := map[string]string{"domain": "demo.example.com", "admin": "you@example.com", "bucket": "demo-bucket", "api_key": "example-key"}
	if !reflect.DeepEqual(ex, want) {
		t.Errorf("ExampleInputs = %v", ex)
	}
	if _, err := tpl.CheckInputs(ex); err != nil {
		t.Errorf("the examples don't pass: %v", err)
	}

	pv := tpl.PreviewValues(map[string]string{"domain": "a.example.com", "api_key": "typed"})
	if pv["api_key"] != "" || pv["db_password"] != "" || pv["domain"] != "a.example.com" {
		t.Errorf("PreviewValues = %v", pv)
	}
	if _, err := tpl.Render(pv); err == nil {
		// admin and bucket are missing here: a preview passes CheckPreviewInputs first.
		t.Error("Render accepted values without admin")
	}
}

func TestDomainInputs(t *testing.T) {
	tpl := goodTemplate()
	ov := tpl.Services["web"]
	ov.Domains = append(ov.Domains, compose.Domain{Host: "static.example.com", Port: 8080})
	tpl.Services["web"] = ov
	got := tpl.DomainInputs()
	want := map[string]string{"services.web.domains[0].host": "domain"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DomainInputs = %v, want %v", got, want)
	}
}
