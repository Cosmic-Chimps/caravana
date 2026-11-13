package templates

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// sourceOf lays goodTemplate out as the catalog repository does.
func sourceOf(t *testing.T) fstest.MapFS {
	t.Helper()
	tpl := goodTemplate()
	compose, logo := tpl.Compose, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8"/></svg>`
	tpl.Compose = ""
	manifest, err := json.MarshalIndent(tpl, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{
		"catalog.json":                    {Data: []byte(`{"format": 1, "version": "2026.11.12"}`)},
		"templates/demo/template.json":    {Data: manifest},
		"templates/demo/compose.yaml":     {Data: []byte(compose)},
		"templates/demo/logo.svg":         {Data: []byte(logo)},
		"templates/another/template.json": {Data: bytes.ReplaceAll(manifest, []byte(`"name": "demo"`), []byte(`"name": "another"`))},
		"templates/another/compose.yaml":  {Data: []byte(compose)},
	}
}

func TestReadSource(t *testing.T) {
	fsys := sourceOf(t)
	bundle, err := ReadSource(fsys, "")
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	c, err := ParseCatalog(bundle)
	if err != nil {
		t.Fatalf("the bundle doesn't parse: %v", err)
	}
	if c.Version != "2026.11.12" || len(c.Templates) != 2 || c.Templates[0].Name != "another" || c.Templates[1].Name != "demo" {
		t.Errorf("catalog = %s, %d templates, sorted by name? %q", c.Version, len(c.Templates), c.Templates[0].Name)
	}
	demo, _ := c.Find("demo")
	if !strings.Contains(demo.Compose, "image: example/demo:1.0.0") || !strings.HasPrefix(demo.Logo, "<svg") {
		t.Errorf("compose or logo not read: %.40q / %.20q", demo.Compose, demo.Logo)
	}
	if another, _ := c.Find("another"); another.Logo != "" {
		t.Error("a template without logo.svg has a logo")
	}

	t.Run("deterministic", func(t *testing.T) {
		again, err := ReadSource(fsys, "")
		if err != nil || !bytes.Equal(bundle, again) {
			t.Errorf("second read differs (%v)", err)
		}
	})
	t.Run("a version replaces the source's", func(t *testing.T) {
		b, err := ReadSource(fsys, "2026.12.01.2")
		if err != nil {
			t.Fatal(err)
		}
		if c, _ := ParseCatalog(b); c.Version != "2026.12.01.2" {
			t.Errorf("version = %q", c.Version)
		}
		if _, err := ReadSource(fsys, "bad"); err == nil {
			t.Error("a bad version was accepted")
		}
	})
}

func TestReadSourceRefusals(t *testing.T) {
	tests := []struct {
		name   string
		change func(fstest.MapFS)
		want   string
	}{
		{"no catalog.json", func(m fstest.MapFS) { delete(m, "catalog.json") }, "catalog.json"},
		{"unknown format", func(m fstest.MapFS) {
			m["catalog.json"] = &fstest.MapFile{Data: []byte(`{"format": 9, "version": "2026.11.12"}`)}
		}, "format 9"},
		{"unknown catalog field", func(m fstest.MapFS) {
			m["catalog.json"] = &fstest.MapFile{Data: []byte(`{"format": 1, "version": "2026.11.12", "x": 1}`)}
		}, "catalog.json"},
		{"a template without compose.yaml", func(m fstest.MapFS) { delete(m, "templates/demo/compose.yaml") }, "compose.yaml"},
		{"a template without a manifest", func(m fstest.MapFS) { delete(m, "templates/demo/template.json") }, "template.json"},
		{"a file where a folder belongs", func(m fstest.MapFS) { m["templates/README.md"] = &fstest.MapFile{Data: []byte("x")} }, "README.md"},
		{"a manifest with another name", func(m fstest.MapFS) {
			m["templates/demo/template.json"] = &fstest.MapFile{Data: bytes.ReplaceAll(m["templates/demo/template.json"].Data, []byte(`"name": "demo"`), []byte(`"name": "other"`))}
		}, "the folder is"},
		{"compose inside the manifest", func(m fstest.MapFS) {
			m["templates/demo/template.json"] = &fstest.MapFile{Data: bytes.Replace(m["templates/demo/template.json"].Data, []byte(`{`), []byte(`{"compose": "x",`), 1)}
		}, "belong in compose.yaml"},
		{"an unknown manifest field", func(m fstest.MapFS) {
			m["templates/demo/template.json"] = &fstest.MapFile{Data: bytes.Replace(m["templates/demo/template.json"].Data, []byte(`{`), []byte(`{"surprise": 1,`), 1)}
		}, "surprise"},
		{"an invalid template", func(m fstest.MapFS) {
			m["templates/demo/compose.yaml"] = &fstest.MapFile{Data: []byte("# {{unknown}}\n")}
		}, "demo: compose:"},
		{"no templates folder", func(m fstest.MapFS) {
			for k := range m {
				if strings.HasPrefix(k, "templates/") {
					delete(m, k)
				}
			}
		}, "templates"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := sourceOf(t)
			tt.change(fsys)
			_, err := ReadSource(fsys, "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestSums(t *testing.T) {
	bundle := []byte("hello\n")
	sums := Sums(bundle)
	if want := "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03  catalog.json\n"; string(sums) != want {
		t.Errorf("Sums = %q, want %q", sums, want)
	}
	if err := VerifySums(bundle, sums); err != nil {
		t.Errorf("VerifySums: %v", err)
	}
	if err := VerifySums([]byte("hello!\n"), sums); err == nil || !strings.Contains(err.Error(), "doesn't match") {
		t.Errorf("a changed bundle: %v", err)
	}
	other := "0000000000000000000000000000000000000000000000000000000000000000  other.txt\n"
	for name, text := range map[string]string{
		"no catalog line": other,
		"short sum":       "abc  catalog.json\n",
		"not hex":         strings.Repeat("z", 64) + "  catalog.json\n",
		"empty":           "",
	} {
		if err := VerifySums(bundle, []byte(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Binary mode and other lines are fine, and the sum may be uppercase.
	text := other + "5891B5B522D5DF086D0FF0B110FBD9D21BB4FC7163AF34D08286A2E846F6BE03 *catalog.json\r\n"
	if err := VerifySums(bundle, []byte(text)); err != nil {
		t.Errorf("binary-mode line: %v", err)
	}
}

// TestCheck: a good template passes, and each defect the catalog's CI exists to
// catch fails naming the template and the path.
func TestCheck(t *testing.T) {
	settings := CheckSettings()
	if err := Check(goodTemplate(), settings); err != nil {
		t.Fatalf("Check(good): %v", err)
	}

	tests := []struct {
		name   string
		change func(*Template)
		want   string
	}{
		{"depends_on is a warning", func(x *Template) {
			x.Compose = strings.Replace(x.Compose, "    image: example/demo:1.0.0\n", "    image: example/demo:1.0.0\n    depends_on: [db]\n", 1)
		}, "demo: services.web.depends_on:"},
		{"a service the file lacks", func(x *Template) {
			x.Services["ghost"] = compose.Overlay{}
		}, `the file has no service "ghost"`},
		{"a floating tag", func(x *Template) {
			x.Compose = strings.Replace(x.Compose, "example/demo:1.0.0", "example/demo:latest", 1)
		},
			"services.web.image"},
		{"no tag", func(x *Template) { x.Compose = strings.Replace(x.Compose, "example/demo:1.0.0", "example/demo", 1) },
			"services.web.image"},
		{"a bad image", func(x *Template) { x.Compose = strings.Replace(x.Compose, "example/demo:1.0.0", "Example/Demo:1", 1) },
			"services.web.image"},
		{"a build instead of an image", func(x *Template) {
			x.Compose = strings.Replace(x.Compose, "    image: example/demo:1.0.0\n", "    build: .\n", 1)
		}, "services.web"},
		{"a bind mount", func(x *Template) {
			x.Compose = strings.Replace(x.Compose, "    expose: [\"8080\"]\n", "    expose: [\"8080\"]\n    volumes:\n      - /etc:/etc\n", 1)
		}, "services.web.volumes[0]"},
		{"a backup without its password", func(x *Template) {
			ov := x.Services["db"]
			ov.Secrets = nil
			x.Services["db"] = ov
		}, "POSTGRES_PASSWORD"},
		{"the backed-up password in the environment", func(x *Template) {
			ov := x.Services["db"]
			ov.Secrets = nil
			x.Services["db"] = ov
			x.Compose = strings.Replace(x.Compose, "POSTGRES_DB: demo", "POSTGRES_DB: demo\n      POSTGRES_PASSWORD: plain", 1)
		}, "services.db.environment.POSTGRES_PASSWORD"},
		{"a domain on a service without ports", func(x *Template) {
			x.Compose = strings.Replace(x.Compose, "    expose: [\"8080\"]\n", "", 1)
		}, "services.web"},
		{"a restart policy that is a warning", func(x *Template) {
			x.Compose = strings.Replace(x.Compose, "    image: example/demo:1.0.0\n", "    image: example/demo:1.0.0\n    restart: \"no\"\n", 1)
		}, "services.web.restart"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := goodTemplate()
			tt.change(&x)
			err := Check(x, settings)
			if err == nil {
				t.Fatal("Check accepted it")
			}
			if !strings.Contains(err.Error(), tt.want) || !strings.HasPrefix(err.Error(), "demo: ") {
				t.Errorf("error = %q, want it to start with the template and contain %q", err, tt.want)
			}
		})
	}

	t.Run("bad settings fail the check", func(t *testing.T) {
		if err := Check(goodTemplate(), nomadjob.Settings{}); err == nil {
			t.Error("empty settings were accepted")
		}
	})

	t.Run("the examples must pass their own inputs", func(t *testing.T) {
		x := goodTemplate()
		x.Inputs[1].Example = "" // admin has no default: nothing to render with
		if err := Check(x, settings); err == nil {
			t.Error("accepted")
		}
	})
}

// TestEmbeddedCatalog: the catalog built into the binary matches its sums,
// parses, and every template passes the same check the catalog repository's CI
// runs (SC-003).
func TestEmbeddedCatalog(t *testing.T) {
	c, bundle := Embedded()
	if len(c.Templates) == 0 || len(bundle) == 0 {
		t.Fatal("the embedded catalog is empty or can't be read")
	}
	if err := VerifySums(bundle, EmbeddedSums()); err != nil {
		t.Errorf("the embedded bundle and its SHA256SUMS disagree: %v (run caravana template bundle)", err)
	}
	if string(EmbeddedSums()) != string(Sums(bundle)) {
		t.Error("SHA256SUMS isn't exactly what `caravana template bundle` writes")
	}
	again, err := ParseCatalog(bundle)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if again.Version != c.Version {
		t.Errorf("version %q vs %q", again.Version, c.Version)
	}
	for _, name := range []string{"uptime-kuma", "hollo", "postgres", "adminer", "umbral", "codemagic-patch"} {
		if _, ok := c.Find(name); !ok {
			t.Errorf("the embedded catalog has no %q", name)
		}
	}
	for _, tpl := range c.Templates {
		t.Run(tpl.Name, func(t *testing.T) {
			if err := Check(tpl, CheckSettings()); err != nil {
				t.Error(err)
			}
			// The compose file never holds a secret-valued name: validation says so,
			// and the rendered file carries none of the generated values.
			values := map[string]string{}
			for k, v := range tpl.ExampleInputs() {
				values[k] = v
			}
			for _, g := range tpl.Generated {
				values[g.Name] = "s3cret-sentinel-035-embedded"
			}
			req, err := tpl.Render(values)
			if err != nil {
				t.Fatal(err)
			}
			for _, in := range tpl.Inputs {
				if in.Type == TypeSecret && strings.Contains(req.Compose, values[in.Name]) {
					t.Errorf("secret input %s is in the Compose file", in.Name)
				}
			}
			if strings.Contains(req.Compose, "s3cret-sentinel-035-embedded") {
				t.Error("a generated secret is in the Compose file")
			}
		})
	}
	if c2, b2 := Embedded(); c2.Version != c.Version || !bytes.Equal(b2, bundle) {
		t.Error("a second Embedded() call answered differently")
	}
}
