package templates

import (
	"errors"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

func TestParseCatalogAcceptsAGoodTemplate(t *testing.T) {
	c, err := ParseCatalog(bundleOf(t, "2026.11.12", goodTemplate()))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if got, ok := c.Find("demo"); !ok || got.Title != "Demo" {
		t.Errorf("Find(demo) = %v, %v", got, ok)
	}
	if _, ok := c.Find("nope"); ok {
		t.Error("Find(nope) found a template")
	}
}

// TestParseCatalogRefusals: each rule of the format contract, and the error
// names the template and the field.
func TestParseCatalogRefusals(t *testing.T) {
	str := func(s string) *string { return &s }
	tests := []struct {
		name   string
		change func(*Template)
		want   string // the error's text contains it
	}{
		{"bad name", func(x *Template) { x.Name = "Bad_Name" }, "name:"},
		{"name with --", func(x *Template) { x.Name = "a--b" }, "name"},
		{"long name", func(x *Template) { x.Name = strings.Repeat("a", 32) }, "name"},
		{"empty title", func(x *Template) { x.Title = "" }, "demo: title:"},
		{"long title", func(x *Template) { x.Title = strings.Repeat("t", 61) }, "demo: title:"},
		{"title with a line break", func(x *Template) { x.Title = "a\nb" }, "demo: title:"},
		{"bad version", func(x *Template) { x.Version = "1 0" }, "demo: version:"},
		{"empty description", func(x *Template) { x.Description = "" }, "demo: description:"},
		{"two-line description", func(x *Template) { x.Description = "a\nb" }, "demo: description:"},
		{"seven tags", func(x *Template) { x.Tags = []string{"a", "b", "c", "d", "e", "f", "g"} }, "demo: tags:"},
		{"bad tag", func(x *Template) { x.Tags = []string{"Bad Tag"} }, "demo: tags[0]:"},
		{"duplicate tag", func(x *Template) { x.Tags = []string{"a", "a"} }, "demo: tags[1]:"},
		{"http upstream", func(x *Template) { x.Upstream = "http://example.com" }, "demo: upstream:"},
		{"no upstream", func(x *Template) { x.Upstream = "" }, "demo: upstream:"},
		{"bad appName", func(x *Template) { x.AppName = "Demo App" }, "demo: appName:"},
		{"long notes", func(x *Template) { x.Notes = strings.Repeat("n", 2001) }, "demo: notes:"},
		{"notes with an unknown name", func(x *Template) { x.Notes = "{{nope}}" }, "demo: notes:"},
		{"notes with a secret", func(x *Template) { x.Notes = "{{db_password}}" }, "demo: notes:"},
		{"bad requires", func(x *Template) { x.Requires = "v1" }, "demo: requires:"},
		{"a broken reason with a line break", func(x *Template) { x.Broken = "a\nb" }, "demo: broken:"},
		{"thirteen inputs", func(x *Template) {
			x.Inputs = nil
			for i := 0; i < 13; i++ {
				x.Inputs = append(x.Inputs, Input{Name: "i" + string(rune('a'+i)), Label: "L", Type: TypeSecret, Example: "x"})
			}
		}, "demo: inputs:"},
		{"input name uppercase", func(x *Template) { x.Inputs[0].Name = "Domain" }, "demo: inputs[0].name:"},
		{"input name twice", func(x *Template) { x.Inputs[1].Name = "domain" }, "demo: inputs[1].name:"},
		{"input name as generated", func(x *Template) { x.Generated[0].Name = "domain" }, "demo: generated[0].name:"},
		{"no label", func(x *Template) { x.Inputs[0].Label = "" }, "demo: inputs[0].label:"},
		{"long help", func(x *Template) { x.Inputs[0].Help = strings.Repeat("h", 201) }, "demo: inputs[0].help:"},
		{"unknown type", func(x *Template) { x.Inputs[0].Type = "number" },
			"demo: inputs[0].type: must be one of domain, email, text, secret"},
		{"text without pattern", func(x *Template) { x.Inputs[2].Pattern = "" }, "demo: inputs[2].pattern:"},
		{"bad pattern", func(x *Template) { x.Inputs[2].Pattern = "(" }, "demo: inputs[2].pattern:"},
		{"long pattern", func(x *Template) { x.Inputs[2].Pattern = strings.Repeat("a", 201) }, "demo: inputs[2].pattern:"},
		{"pattern on a domain", func(x *Template) { x.Inputs[0].Pattern = "a" }, "demo: inputs[0].pattern:"},
		{"default on a secret", func(x *Template) { x.Inputs[3].Default = "x" }, "demo: inputs[3].default:"},
		{"no example and no default", func(x *Template) { x.Inputs[0].Example = "" }, "demo: inputs[0].example:"},
		{"example not a domain", func(x *Template) { x.Inputs[0].Example = "not a domain" }, "demo: inputs[0].example:"},
		{"default not matching its pattern", func(x *Template) { x.Inputs[2].Default = "UPPER" }, "demo: inputs[2].default:"},
		{"generated too short", func(x *Template) { x.Generated[0].Length = 15 }, "demo: generated[0].length:"},
		{"generated too long", func(x *Template) { x.Generated[0].Length = 129 }, "demo: generated[0].length:"},
		{"generated alphabet", func(x *Template) { x.Generated[0].Alphabet = "base64" }, "demo: generated[0].alphabet:"},
		{"unknown placeholder in the file", func(x *Template) { x.Compose += "# {{other}}\n" }, "demo: compose:"},
		{"a generated secret in the file", func(x *Template) { x.Compose += "# {{db_password}}\n" }, "demo: compose:"},
		{"a secret input in the file", func(x *Template) { x.Compose += "# {{ api_key }}\n" }, "demo: compose:"},
		{"a secret in a domain", func(x *Template) {
			ov := x.Services["web"]
			ov.Domains = []compose.Domain{{Host: "{{db_password}}", Port: 8080}}
			x.Services["web"] = ov
		}, "demo: services.web.domains[0].host:"},
		{"a secret in a secret's name", func(x *Template) {
			ov := x.Services["db"]
			ov.Secrets = []app.Secret{{Name: "{{db_password}}", Value: str("x")}}
			x.Services["db"] = ov
		}, "demo: services.db.secrets[0].name:"},
		{"unknown placeholder in the overlay", func(x *Template) {
			ov := x.Services["web"]
			ov.Health = &compose.Health{Path: "/{{nope}}"}
			x.Services["web"] = ov
		}, "demo: services.web.health.path:"},
		{"an input never used", func(x *Template) {
			x.Inputs = append(x.Inputs, Input{Name: "extra", Label: "Extra", Type: TypeDomain, Example: "x.example.com"})
		}, "demo: inputs.extra:"},
		{"an input used only in the notes", func(x *Template) {
			x.Inputs = append(x.Inputs, Input{Name: "extra", Label: "Extra", Type: TypeDomain, Example: "x.example.com"})
			x.Notes += " {{extra}}"
		}, "demo: inputs.extra:"},
		{"a generated secret never used", func(x *Template) {
			x.Generated = append(x.Generated, Generated{Name: "unused", Length: 16, Alphabet: AlphabetHex})
		}, "demo: generated.unused:"},
		{"empty compose", func(x *Template) { x.Compose = "" }, "demo: compose:"},
		{"compose too big", func(x *Template) { x.Compose = strings.Repeat("#", compose.MaxComposeBytes+1) }, "demo: compose:"},
		{"a logo with script", func(x *Template) { x.Logo = `<svg xmlns="http://www.w3.org/2000/svg"><script>x</script></svg>` }, "demo: logo:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := goodTemplate()
			tt.change(&x)
			_, err := ParseCatalog(bundleOf(t, "2026.11.12", x))
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

func TestParseCatalogCatalogRules(t *testing.T) {
	good := goodTemplate()
	dup := goodTemplate()
	for name, tt := range map[string]struct {
		data string
		want string
		is   error
	}{
		"unknown format": {`{"format": 2, "version": "2026.11.12", "templates": []}`, "format 2", ErrUnknownFormat},
		"no format":      {`{"version": "2026.11.12"}`, "format 0", ErrUnknownFormat},
		"not JSON":       {`nope`, "read catalog", nil},
		"bad version":    {`{"format": 1, "version": "v1", "templates": []}`, "version:", nil},
		"unknown field":  {`{"format": 1, "version": "2026.11.12", "templates": [], "extra": 1}`, "unknown field", nil},
		"two values":     {`{"format": 1, "version": "2026.11.12"} {}`, "read catalog", nil},
		"duplicate name": {string(bundleOf(t, "2026.11.12", good, dup)), "demo: name: is used by two", nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCatalog([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.want)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("error %v isn't %v", err, tt.is)
			}
		})
	}

	t.Run("an unknown field of a template", func(t *testing.T) {
		data := `{"format": 1, "version": "2026.11.12", "templates": [{"name": "x", "surprise": true}]}`
		if _, err := ParseCatalog([]byte(data)); err == nil || !strings.Contains(err.Error(), "surprise") {
			t.Errorf("error = %v, want it to name the field", err)
		}
	})
}

func TestCheckLogo(t *testing.T) {
	const ok = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="#123456"/></svg>`
	if err := CheckLogo(ok); err != nil {
		t.Fatalf("a plain logo: %v", err)
	}
	if err := CheckLogo(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><defs><style>.a{fill:red}</style></defs><use href="#a"/></svg>`); err != nil {
		t.Errorf("a logo with a local use: %v", err)
	}
	for name, svg := range map[string]string{
		"script":         `<svg xmlns="http://www.w3.org/2000/svg"><SCRIPT>1</SCRIPT></svg>`,
		"javascript:":    `<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"/></svg>`,
		"onload":         `<svg xmlns="http://www.w3.org/2000/svg" onload="x()"></svg>`,
		"onclick spaced": `<svg xmlns="http://www.w3.org/2000/svg"><rect onclick = "x()"/></svg>`,
		"foreignObject":  `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject/></svg>`,
		"iframe":         `<svg xmlns="http://www.w3.org/2000/svg"><iframe/></svg>`,
		"image":          `<svg xmlns="http://www.w3.org/2000/svg"><image href="a.png"/></svg>`,
		"external use":   `<svg xmlns="http://www.w3.org/2000/svg"><use href="https://example.com/a.svg#b"/></svg>`,
		"xlink use":      `<svg xmlns="http://www.w3.org/2000/svg"><use xlink:href="a.svg#b"/></svg>`,
		"import":         `<svg xmlns="http://www.w3.org/2000/svg"><style>@import "x.css";</style></svg>`,
		"url(http":       `<svg xmlns="http://www.w3.org/2000/svg"><rect style="fill:url(http://x/y)"/></svg>`,
		"url( //":        `<svg xmlns="http://www.w3.org/2000/svg"><rect style="fill:url( '//x/y')"/></svg>`,
		"entity":         `<!DOCTYPE svg [<!ENTITY a "b">]><svg xmlns="http://www.w3.org/2000/svg"/>`,
		"not svg":        `<html></html>`,
		"not xml":        `<svg><rect></svg>`,
		"too big":        `<svg xmlns="http://www.w3.org/2000/svg">` + strings.Repeat(" ", 33<<10) + `</svg>`,
		"control byte":   "<svg xmlns=\"http://www.w3.org/2000/svg\">\x01</svg>",
	} {
		if err := CheckLogo(svg); err == nil {
			t.Errorf("%s: CheckLogo accepted %.60q", name, svg)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"2026.11.13", "2026.11.12", true},
		{"2026.11.12", "2026.11.12", false},
		{"2026.11.12", "2026.11.13", false},
		{"2026.11.12.1", "2026.11.12", true},
		{"2026.11.12", "2026.11.12.1", false},
		{"2026.11.12.10", "2026.11.12.9", true},
		{"2026.12.01", "2026.11.30", true},
		{"2027.01.01", "2026.12.31", true},
		{"x", "2026.11.12", false},
		{"2026.11.12", "x", false},
		{"", "", false},
	} {
		if got := Newer(tt.a, tt.b); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
