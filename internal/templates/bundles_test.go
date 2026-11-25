package templates

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/cosmic-chimps/caravana/internal/compose"
)

// legacyParse is Caravana 0.22's reading of catalog.json, kept as a test: the
// strict decode of format 1's own types and its limits. catalog.json must pass it
// forever, since an older host that can't read one template refuses the whole
// catalog and its list stops updating. (tools: the same was run once against a
// checkout of v0.22.0's ParseCatalog, see specs/058-template-inputs.)
func legacyParse(data []byte) error {
	type input struct {
		Name    string `json:"name"`
		Label   string `json:"label"`
		Type    string `json:"type"`
		Help    string `json:"help,omitempty"`
		Default string `json:"default,omitempty"`
		Pattern string `json:"pattern,omitempty"`
		Example string `json:"example,omitempty"`
	}
	type tmpl struct {
		Name        string                     `json:"name"`
		Title       string                     `json:"title"`
		Version     string                     `json:"version"`
		Description string                     `json:"description"`
		Tags        []string                   `json:"tags,omitempty"`
		Upstream    string                     `json:"upstream"`
		AppName     string                     `json:"appName"`
		Notes       string                     `json:"notes,omitempty"`
		Requires    string                     `json:"requires,omitempty"`
		Broken      string                     `json:"broken,omitempty"`
		Lab         *bool                      `json:"lab,omitempty"`
		Inputs      []input                    `json:"inputs,omitempty"`
		Generated   []Generated                `json:"generated,omitempty"`
		Services    map[string]compose.Overlay `json:"services,omitempty"`
		Compose     string                     `json:"compose,omitempty"`
		Logo        string                     `json:"logo,omitempty"`
	}
	var c struct {
		Format    int    `json:"format"`
		Version   string `json:"version"`
		Templates []tmpl `json:"templates"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return err
	}
	if c.Format != 1 {
		return &Error{Message: "format isn't 1"}
	}
	for _, t := range c.Templates {
		if len(t.Inputs) > 12 {
			return &Error{Template: t.Name, Field: "inputs", Message: "more than 12"}
		}
		for _, in := range t.Inputs {
			switch in.Type {
			case "domain", "email", "text", "secret":
			default:
				return &Error{Template: t.Name, Field: "inputs." + in.Name, Message: "unknown type " + in.Type}
			}
		}
	}
	return nil
}

func sourceWithFormat2(t *testing.T) fstest.MapFS {
	t.Helper()
	fsys := sourceOf(t)
	add := func(name string, tpl Template, v1 bool) {
		manifest, err := json.MarshalIndent(tpl, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, composeFile := "template.json", "compose.yaml"
		if v1 {
			file, composeFile = "template.v1.json", "compose.v1.yaml"
		}
		stripped := tpl
		stripped.Compose = ""
		manifest, _ = json.MarshalIndent(stripped, "", "  ")
		fsys["templates/"+name+"/"+file] = &fstest.MapFile{Data: manifest}
		fsys["templates/"+name+"/"+composeFile] = &fstest.MapFile{Data: []byte(tpl.Compose)}
	}
	// signin: format 2, and a frozen format-1 variant of an older version.
	v2 := signinTemplate()
	add("signin", v2, false)
	frozen := goodTemplate()
	frozen.Name, frozen.Version = "signin", "0.9.0-1"
	add("signin", frozen, true)
	// choices: format 2 only, no frozen variant.
	only := signinTemplate()
	only.Name = "choices"
	add("choices", only, false)
	// old: frozen only.
	old := goodTemplate()
	old.Name = "old"
	add("old", old, true)
	return fsys
}

func TestReadSourceMakesBothBundles(t *testing.T) {
	fsys := sourceWithFormat2(t)
	b, err := ReadSource(fsys, "2026.11.25")
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	v1, err := ParseCatalog(b.V1)
	if err != nil || v1.Format != 1 {
		t.Fatalf("V1 = %+v, %v", v1.Format, err)
	}
	v2, err := ParseCatalog(b.V2)
	if err != nil || v2.Format != 2 {
		t.Fatalf("V2 = %+v, %v", v2.Format, err)
	}
	names := func(c Catalog) string {
		var out []string
		for _, tpl := range c.Templates {
			out = append(out, tpl.Name+" "+tpl.Version)
		}
		return strings.Join(out, ", ")
	}
	if got, want := names(v1), "another 1.0.0-1, demo 1.0.0-1, old 1.0.0-1, signin 0.9.0-1"; got != want {
		t.Errorf("catalog.json has %s; want %s", got, want)
	}
	if got, want := names(v2), "another 1.0.0-1, choices 1.0.0-1, demo 1.0.0-1, signin 1.0.0-1"; got != want {
		t.Errorf("catalog.v2.json has %s; want %s", got, want)
	}
	if strings.Join(b.OnlyV2, ",") != "choices" || strings.Join(b.OnlyV1, ",") != "old" {
		t.Errorf("OnlyV2 = %v, OnlyV1 = %v", b.OnlyV2, b.OnlyV1)
	}

	t.Run("catalog.json is read by Caravana 0.22's rules and holds no format 2", func(t *testing.T) {
		if err := legacyParse(b.V1); err != nil {
			t.Fatalf("the legacy parser refuses catalog.json: %v", err)
		}
		for _, banned := range []string{`"when"`, `"required"`, `"choices"`, `"choice"`, `# when`} {
			if strings.Contains(string(b.V1), banned) {
				t.Errorf("catalog.json has %s", banned)
			}
		}
		if err := legacyParse(b.V2); err == nil {
			t.Error("the legacy parser accepted the format-2 bundle: the test proves nothing")
		}
	})
	t.Run("the format-1 bytes of an unchanged template are what they were", func(t *testing.T) {
		// demo has no format-2 field: both bundles carry the very same text for it.
		var a, c Catalog
		_ = json.Unmarshal(b.V1, &a)
		_ = json.Unmarshal(b.V2, &c)
		da, _ := a.Find("demo")
		dc, _ := c.Find("demo")
		ja, _ := json.Marshal(da)
		jc, _ := json.Marshal(dc)
		if !bytes.Equal(ja, jc) {
			t.Errorf("demo differs between the bundles:\n%s\n%s", ja, jc)
		}
	})
	t.Run("deterministic", func(t *testing.T) {
		again, err := ReadSource(fsys, "2026.11.25")
		if err != nil || !bytes.Equal(b.V1, again.V1) || !bytes.Equal(b.V2, again.V2) {
			t.Errorf("second read differs (%v)", err)
		}
	})
	t.Run("the sums name both files", func(t *testing.T) {
		sums := Sums(b)
		if err := VerifySums(b.V1, sums, BundleV1File); err != nil {
			t.Error(err)
		}
		if err := VerifySums(b.V2, sums, BundleV2File); err != nil {
			t.Error(err)
		}
	})
}

func TestReadSourceFrozenRefusals(t *testing.T) {
	t.Run("a frozen variant that isn't format 1 is refused", func(t *testing.T) {
		fsys := sourceWithFormat2(t)
		x := signinTemplate()
		x.Compose = ""
		manifest, _ := json.Marshal(x)
		fsys["templates/signin/template.v1.json"] = &fstest.MapFile{Data: manifest}
		fsys["templates/signin/compose.v1.yaml"] = &fstest.MapFile{Data: []byte(signinTemplate().Compose)}
		if _, err := ReadSource(fsys, ""); err == nil || !strings.Contains(err.Error(), "the frozen format-1 variant") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a folder with neither", func(t *testing.T) {
		fsys := sourceOf(t)
		fsys["templates/empty/README"] = &fstest.MapFile{Data: []byte("x")}
		if _, err := ReadSource(fsys, ""); err == nil || !strings.Contains(err.Error(), "needs template.json") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a frozen manifest without its compose file", func(t *testing.T) {
		fsys := sourceWithFormat2(t)
		delete(fsys, "templates/old/compose.v1.yaml")
		if _, err := ReadSource(fsys, ""); err == nil || !strings.Contains(err.Error(), "compose.v1.yaml") {
			t.Errorf("err = %v", err)
		}
	})
}

// ---- the tolerant parser ----

func TestParseCatalogToleratesWhatItDoesntKnowInFormat2(t *testing.T) {
	good, _ := json.Marshal(goodTemplate())
	newer := func(requires string) bool { return Newer(requires, "0.22.0") }
	// A template from the future: a field this Caravana doesn't have.
	future := `{"name": "future", "title": "Future", "version": "9-1", "description": "From a newer catalog.", "tags": ["x"],
		"upstream": "https://example.com", "appName": "future", "teleport": true, "compose": "x"}`
	// One that needs a newer release and is written in a way this one can't read.
	needs := `{"name": "needs", "title": "Needs", "version": "9-1", "description": "d", "upstream": "https://example.com", "appName": "needs",
		"requires": "9.9.9", "inputs": [{"name": "a", "label": "A", "type": "knob"}], "compose": "x"}`
	doc := func(parts ...string) []byte {
		return []byte(`{"format": 2, "version": "2026.11.25", "templates": [` + strings.Join(parts, ",") + `]}`)
	}

	t.Run("the rest of the catalog is kept", func(t *testing.T) {
		c, err := ParseCatalogFor(doc(string(good), future, needs), newer)
		if err != nil {
			t.Fatalf("ParseCatalogFor: %v", err)
		}
		if len(c.Templates) != 3 {
			t.Fatalf("%d templates", len(c.Templates))
		}
		demo, _ := c.Find("demo")
		if demo.Unsupported != "" || len(demo.Inputs) == 0 {
			t.Errorf("demo = %+v", demo)
		}
		f, _ := c.Find("future")
		if !strings.Contains(f.Unsupported, "made for a newer caravana") || f.Title != "Future" || f.Description != "From a newer catalog." || f.Compose != "" {
			t.Errorf("future = %+v", f)
		}
		n, _ := c.Find("needs")
		if n.Unsupported != "needs caravana 9.9.9 or newer" || n.Requires != "9.9.9" {
			t.Errorf("needs = %+v", n)
		}
	})
	t.Run("Check and the catalog check skip them", func(t *testing.T) {
		c, _ := ParseCatalogFor(doc(string(good), future), newer)
		f, _ := c.Find("future")
		if err := Check(f, CheckSettings()); err != nil {
			t.Errorf("Check(stub) = %v", err)
		}
		if err := CheckCatalog(c, CheckSettings()); err != nil {
			t.Errorf("CheckCatalog = %v", err)
		}
	})
	t.Run("a requires this release has is no excuse", func(t *testing.T) {
		bad := strings.Replace(needs, `"requires": "9.9.9"`, `"requires": "0.1.0"`, 1)
		if _, err := ParseCatalogFor(doc(bad), newer); err == nil {
			t.Error("a template that fails the rules was kept")
		}
	})
	t.Run("an unknown field at the top level still fails the catalog", func(t *testing.T) {
		data := strings.Replace(string(doc(future)), `"version"`, `"surprise": 1, "version"`, 1)
		if _, err := ParseCatalogFor([]byte(data), newer); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("a template that isn't JSON of the right shape still fails the catalog", func(t *testing.T) {
		if _, err := ParseCatalogFor(doc(`{"name": "x", "title": 5}`), newer); err == nil {
			t.Error("accepted")
		}
		if _, err := ParseCatalogFor(doc(`"not an object"`), newer); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("a template without a usable name has no stub", func(t *testing.T) {
		if _, err := ParseCatalogFor(doc(`{"name": "Bad Name", "teleport": 1}`), newer); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("two templates of one name still fail it", func(t *testing.T) {
		if _, err := ParseCatalogFor(doc(future, future), newer); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("format 1 tolerates nothing", func(t *testing.T) {
		data := strings.Replace(string(doc(future)), `"format": 2`, `"format": 1`, 1)
		if _, err := ParseCatalogFor([]byte(data), newer); err == nil {
			t.Error("a format-1 catalog kept a template it can't read")
		}
	})
	t.Run("ParseCatalog without a release to compare keeps only unknown fields", func(t *testing.T) {
		if _, err := ParseCatalog(doc(future)); err != nil {
			t.Errorf("unknown field: %v", err)
		}
		if _, err := ParseCatalog(doc(needs)); err == nil {
			t.Error("a template that fails the rules was kept with nothing to compare")
		}
	})
}
