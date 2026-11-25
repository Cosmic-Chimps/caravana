package templates

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
)

// checkSVGRoot says svg is well-formed XML whose root element is <svg>.
func checkSVGRoot(svg string) error {
	dec := xml.NewDecoder(strings.NewReader(svg))
	root := ""
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("isn't well-formed SVG: %v", err)
		}
		if start, ok := tok.(xml.StartElement); ok && root == "" {
			root = start.Name.Local
		}
	}
	if root != "svg" {
		return errors.New("must be an <svg> document")
	}
	return nil
}

// The files of a release.
const (
	// BundleV1File is the bundle in format 1. It stays in format 1 for good:
	// every Caravana up to 0.22 refuses a whole catalog that holds one thing it
	// doesn't know, so it only ever holds templates that pass format 1's rules.
	BundleV1File = "catalog.json"
	// BundleV2File is the bundle in format 2, with every template.
	BundleV2File = "catalog.v2.json"
)

// Bundles are the two bundles of one source: V1 for Caravana 0.22 and older
// (catalog.json) and V2 (catalog.v2.json).
type Bundles struct {
	V1, V2 []byte
	// OnlyV2 names the templates left out of V1 because they use format 2.
	OnlyV2 []string
	// OnlyV1 names the templates frozen in format 1 that V2 no longer has.
	OnlyV1 []string
}

// Sums is the text of the SHA256SUMS file for the bundles: one line each,
// "<64 hex>  catalog.json" and "<64 hex>  catalog.v2.json". A reader that
// knows one name (Caravana 0.22 reads only catalog.json) skips the other line.
func Sums(b Bundles) []byte {
	var out []byte
	for _, f := range []struct {
		name string
		data []byte
	}{{BundleV1File, b.V1}, {BundleV2File, b.V2}} {
		if len(f.data) > 0 {
			out = append(out, []byte(SumOf(f.data)+"  "+f.name+"\n")...)
		}
	}
	return out
}

// SumOf is the SHA-256 of a bundle in hex.
func SumOf(bundle []byte) string {
	sum := sha256.Sum256(bundle)
	return hex.EncodeToString(sum[:])
}

// SumFromSums reads the hex sum of the file called name out of a SHA256SUMS
// file. It accepts the text-mode and binary-mode forms of the line ("<hex>
// name" and "<hex> *name") and ignores other files' lines.
func SumFromSums(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		if len(fields[0]) != 64 {
			return "", fmt.Errorf("the checksum for %s isn't 64 hex digits", name)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", fmt.Errorf("the checksum for %s isn't hex", name)
		}
		return strings.ToLower(fields[0]), nil
	}
	return "", fmt.Errorf("SHA256SUMS has no line for %s", name)
}

// VerifySums checks that bundle has the sum SHA256SUMS gives for the file
// called name.
func VerifySums(bundle, sums []byte, name string) error {
	want, err := SumFromSums(sums, name)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(want), []byte(SumOf(bundle))) != 1 {
		return fmt.Errorf("%s doesn't match its checksum", name)
	}
	return nil
}

// ReadSource turns the source layout of the catalog repository (catalog.json,
// templates/<name>/template.json, compose.yaml and logo.svg) into the two
// bundles. version, when not empty, is the bundles' version and replaces the one in
// catalog.json (the release workflow passes its tag's). The output is
// deterministic: templates sorted by name, the same bytes for the same
// source. Each bundle is parsed before it is returned, so every rule of its
// format has been checked; a bundle of an invalid catalog is never made.
//
// A template folder holds the template as format 2 (template.json and
// compose.yaml), and may hold a frozen format-1 variant of it (template.v1.json
// and compose.v1.yaml). catalog.json (V1) gets the frozen variant when there is
// one; else the format-2 files when they pass format 1's rules; else nothing.
// A folder with only the frozen variant is a template that Caravana 0.22 and
// older still offer and newer ones no longer do.
func ReadSource(fsys fs.FS, version string) (Bundles, error) {
	head, err := fs.ReadFile(fsys, "catalog.json")
	if err != nil {
		return Bundles{}, fmt.Errorf("read catalog.json: %w", err)
	}
	var cat Catalog
	if err := decodeStrict(head, &struct {
		Format  *int    `json:"format"`
		Version *string `json:"version"`
	}{&cat.Format, &cat.Version}); err != nil {
		return Bundles{}, fmt.Errorf("catalog.json: %w", err)
	}
	if cat.Format != FormatV1 && cat.Format != Format {
		return Bundles{}, fmt.Errorf("catalog.json: format %d: %w", cat.Format, ErrUnknownFormat)
	}
	if version != "" {
		cat.Version = version
	}

	dirs, err := fs.ReadDir(fsys, "templates")
	if err != nil {
		return Bundles{}, fmt.Errorf("read templates: %w", err)
	}
	v1 := Catalog{Format: FormatV1, Version: cat.Version}
	v2 := Catalog{Format: Format, Version: cat.Version}
	var out Bundles
	for _, d := range dirs {
		if !d.IsDir() {
			return Bundles{}, fmt.Errorf("templates/%s: only template folders belong here", d.Name())
		}
		name := d.Name()
		current, hasCurrent, err := readTemplate(fsys, name, "template.json", "compose.yaml")
		if err != nil {
			return Bundles{}, err
		}
		frozen, hasFrozen, err := readTemplate(fsys, name, "template.v1.json", "compose.v1.yaml")
		if err != nil {
			return Bundles{}, err
		}
		switch {
		case !hasCurrent && !hasFrozen:
			return Bundles{}, fmt.Errorf("templates/%s: needs template.json (and compose.yaml), or a frozen template.v1.json", name)
		case hasCurrent:
			v2.Templates = append(v2.Templates, current)
		default:
			out.OnlyV1 = append(out.OnlyV1, name)
		}
		switch {
		case hasFrozen:
			if err := frozen.validate(FormatV1); err != nil {
				return Bundles{}, fmt.Errorf("the frozen format-1 variant: %w", err)
			}
			v1.Templates = append(v1.Templates, frozen)
		case current.validate(FormatV1) == nil:
			v1.Templates = append(v1.Templates, current)
		default:
			out.OnlyV2 = append(out.OnlyV2, name)
		}
	}
	for _, c := range []*Catalog{&v1, &v2} {
		slices.SortFunc(c.Templates, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })
	}
	if out.V1, err = marshalBundle(v1); err != nil {
		return Bundles{}, err
	}
	if out.V2, err = marshalBundle(v2); err != nil {
		return Bundles{}, err
	}
	return out, nil
}

// marshalBundle writes a catalog and parses what it wrote.
func marshalBundle(c Catalog) ([]byte, error) {
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("write bundle: %w", err)
	}
	out = append(out, '\n')
	if _, err := ParseCatalog(out); err != nil {
		return nil, err
	}
	return out, nil
}

// readTemplate reads the template of folder name from a manifest and its
// Compose file; ok is false when the manifest isn't there.
func readTemplate(fsys fs.FS, name, manifestFile, composeFile string) (t Template, ok bool, err error) {
	dir := "templates/" + name
	manifest, err := fs.ReadFile(fsys, dir+"/"+manifestFile)
	if errors.Is(err, fs.ErrNotExist) {
		return Template{}, false, nil
	}
	if err != nil {
		return Template{}, false, fmt.Errorf("read %s/%s: %w", dir, manifestFile, err)
	}
	if err := json.Unmarshal(manifest, &t); err != nil {
		return Template{}, false, fmt.Errorf("%s/%s: %w", dir, manifestFile, err)
	}
	if t.Compose != "" || t.Logo != "" {
		return Template{}, false, fmt.Errorf("%s/%s: compose and logo belong in %s and logo.svg", dir, manifestFile, composeFile)
	}
	if t.Name != name {
		return Template{}, false, &Error{Template: name, Field: "name", Message: fmt.Sprintf("is %q, but the folder is %q", t.Name, name)}
	}
	text, err := fs.ReadFile(fsys, dir+"/"+composeFile)
	if err != nil {
		return Template{}, false, fmt.Errorf("read %s/%s: %w", dir, composeFile, err)
	}
	t.Compose = string(text)
	if logo, err := fs.ReadFile(fsys, dir+"/logo.svg"); err == nil {
		t.Logo = string(logo)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Template{}, false, fmt.Errorf("read %s/logo.svg: %w", dir, err)
	}
	return t, true, nil
}

// decodeStrict reads one JSON value into v and refuses unknown fields.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("more than one JSON value")
	}
	return nil
}
