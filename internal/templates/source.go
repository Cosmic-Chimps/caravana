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

// Sums is the text of the SHA256SUMS file for a bundle: one line,
// "<64 hex>  catalog.json".
func Sums(bundle []byte) []byte {
	sum := sha256.Sum256(bundle)
	return []byte(hex.EncodeToString(sum[:]) + "  catalog.json\n")
}

// SumOf is the SHA-256 of a bundle in hex.
func SumOf(bundle []byte) string {
	sum := sha256.Sum256(bundle)
	return hex.EncodeToString(sum[:])
}

// SumFromSums reads the hex sum of catalog.json out of a SHA256SUMS file. It
// accepts the text-mode and binary-mode forms of the line ("<hex>  name" and
// "<hex> *name") and ignores other files' lines.
func SumFromSums(sums []byte) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != "catalog.json" {
			continue
		}
		if len(fields[0]) != 64 {
			return "", errors.New("the checksum for catalog.json isn't 64 hex digits")
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", errors.New("the checksum for catalog.json isn't hex")
		}
		return strings.ToLower(fields[0]), nil
	}
	return "", errors.New("SHA256SUMS has no line for catalog.json")
}

// VerifySums checks that bundle has the sum SHA256SUMS gives for catalog.json.
func VerifySums(bundle, sums []byte) error {
	want, err := SumFromSums(sums)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(want), []byte(SumOf(bundle))) != 1 {
		return errors.New("catalog.json doesn't match its checksum")
	}
	return nil
}

// ReadSource turns the source layout of the catalog repository (catalog.json,
// templates/<name>/template.json, compose.yaml and logo.svg) into a bundle.
// version, when not empty, is the bundle's version and replaces the one in
// catalog.json (the release workflow passes its tag's). The output is
// deterministic: templates sorted by name, the same bytes for the same
// source. The bundle is parsed before it is returned, so every rule of the
// format has been checked; a bundle of an invalid catalog is never made.
func ReadSource(fsys fs.FS, version string) ([]byte, error) {
	head, err := fs.ReadFile(fsys, "catalog.json")
	if err != nil {
		return nil, fmt.Errorf("read catalog.json: %w", err)
	}
	var cat Catalog
	if err := decodeStrict(head, &struct {
		Format  *int    `json:"format"`
		Version *string `json:"version"`
	}{&cat.Format, &cat.Version}); err != nil {
		return nil, fmt.Errorf("catalog.json: %w", err)
	}
	if cat.Format != Format {
		return nil, fmt.Errorf("catalog.json: format %d: %w", cat.Format, ErrUnknownFormat)
	}
	if version != "" {
		cat.Version = version
	}

	dirs, err := fs.ReadDir(fsys, "templates")
	if err != nil {
		return nil, fmt.Errorf("read templates: %w", err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			return nil, fmt.Errorf("templates/%s: only template folders belong here", d.Name())
		}
		t, err := readTemplate(fsys, d.Name())
		if err != nil {
			return nil, err
		}
		cat.Templates = append(cat.Templates, t)
	}
	slices.SortFunc(cat.Templates, func(a, b Template) int { return strings.Compare(a.Name, b.Name) })

	out, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("write bundle: %w", err)
	}
	out = append(out, '\n')
	if _, err := ParseCatalog(out); err != nil {
		return nil, err
	}
	return out, nil
}

func readTemplate(fsys fs.FS, name string) (Template, error) {
	dir := "templates/" + name
	manifest, err := fs.ReadFile(fsys, dir+"/template.json")
	if err != nil {
		return Template{}, fmt.Errorf("read %s/template.json: %w", dir, err)
	}
	var t Template
	if err := decodeStrict(manifest, &t); err != nil {
		return Template{}, fmt.Errorf("%s/template.json: %w", dir, err)
	}
	if t.Compose != "" || t.Logo != "" {
		return Template{}, fmt.Errorf("%s/template.json: compose and logo belong in compose.yaml and logo.svg", dir)
	}
	if t.Name != name {
		return Template{}, &Error{Template: name, Field: "name", Message: fmt.Sprintf("is %q, but the folder is %q", t.Name, name)}
	}
	text, err := fs.ReadFile(fsys, dir+"/compose.yaml")
	if err != nil {
		return Template{}, fmt.Errorf("read %s/compose.yaml: %w", dir, err)
	}
	t.Compose = string(text)
	if logo, err := fs.ReadFile(fsys, dir+"/logo.svg"); err == nil {
		t.Logo = string(logo)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Template{}, fmt.Errorf("read %s/logo.svg: %w", dir, err)
	}
	return t, nil
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
