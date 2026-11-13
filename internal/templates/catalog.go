// Package templates reads, checks and renders app templates (feature 035).
//
// A template is a manifest plus a Compose file with {{placeholders}}. The
// package is pure: it never touches the network, the disk (but an fs.FS it is
// given) or the clock, and its only source of randomness is an io.Reader the
// caller passes. Rendering turns a template and its inputs into a
// compose.Request, which the service deploys like any other Compose app.
//
// A template is data, not a program: placeholders are replaced once, left to
// right, with no logic (research R1). Secret-valued names (secret inputs and
// generated secrets) may appear only in an overlay secret's value, so a
// secret is never part of the Compose file Caravana keeps and shows (R2).
package templates

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

// Format is the catalog format this Caravana reads.
const Format = 1

// Limits of the format.
const (
	maxTitle       = 60
	maxDescription = 200
	maxTags        = 6
	maxNotes       = 2000
	maxInputs      = 12
	maxGenerated   = 12
	maxLabel       = 40
	maxHelp        = 200
	maxPattern     = 200
	maxLogoBytes   = 32 << 10
	maxName        = 31
)

// ErrUnknownFormat is returned for a catalog whose format this Caravana
// doesn't read. The fetch keeps the stored catalog when it sees it.
var ErrUnknownFormat = errors.New("the catalog's format isn't one this Caravana reads")

// Input types.
const (
	TypeDomain = "domain"
	TypeEmail  = "email"
	TypeText   = "text"
	TypeSecret = "secret"
)

// Generated secret alphabets.
const (
	AlphabetHex       = "hex"
	AlphabetAlnum     = "alnum"
	AlphabetBase64URL = "base64url"
)

// Catalog is a parsed bundle.
type Catalog struct {
	Format    int        `json:"format"`
	Version   string     `json:"version"`
	Templates []Template `json:"templates"`
}

// Find returns the template called name.
func (c Catalog) Find(name string) (Template, bool) {
	for _, t := range c.Templates {
		if t.Name == name {
			return t, true
		}
	}
	return Template{}, false
}

// Template is one manifest with its Compose text and logo, as the bundle
// holds it. The json tags are the bundle's and the API's field names.
type Template struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Upstream    string   `json:"upstream"`
	// AppName is the console's default app name.
	AppName string `json:"appName"`
	// Notes is plain text shown after install; it may hold plain placeholders.
	Notes string `json:"notes,omitempty"`
	// Requires is the oldest Caravana release that can install the template.
	Requires string `json:"requires,omitempty"`
	// Broken, when not empty, keeps the template from being offered; the text
	// is the reason.
	Broken string `json:"broken,omitempty"`
	// Lab is false when the catalog's lab job must skip the template. nil
	// means true.
	Lab       *bool                      `json:"lab,omitempty"`
	Inputs    []Input                    `json:"inputs,omitempty"`
	Generated []Generated                `json:"generated,omitempty"`
	Services  map[string]compose.Overlay `json:"services,omitempty"`
	// Compose is the Compose file with placeholders.
	Compose string `json:"compose,omitempty"`
	// Logo is an SVG, or empty.
	Logo string `json:"logo,omitempty"`
}

// InLab says whether the catalog's lab job installs the template.
func (t Template) InLab() bool { return t.Lab == nil || *t.Lab }

// Input is one question the owner answers.
type Input struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	Help    string `json:"help,omitempty"`
	Default string `json:"default,omitempty"`
	// Pattern is a Go regular expression a text input must match entirely.
	Pattern string `json:"pattern,omitempty"`
	Example string `json:"example,omitempty"`
}

// Generated is a secret Caravana makes at install.
type Generated struct {
	Name     string `json:"name"`
	Length   int    `json:"length"`
	Alphabet string `json:"alphabet"`
}

// Error is a defect of a template, naming it and the field.
type Error struct {
	Template string
	Field    string
	Message  string
}

func (e *Error) Error() string {
	switch {
	case e.Template == "":
		return e.Field + ": " + e.Message
	case e.Field == "":
		return e.Template + ": " + e.Message
	}
	return e.Template + ": " + e.Field + ": " + e.Message
}

func defect(t Template, field, format string, args ...any) *Error {
	return &Error{Template: t.Name, Field: field, Message: fmt.Sprintf(format, args...)}
}

var (
	versionRE     = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,40}$`)
	catalogVerRE  = regexp.MustCompile(`^[0-9]{4}\.[0-9]{2}\.[0-9]{2}(\.[0-9]+)?$`)
	releaseRE     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	tagRE         = regexp.MustCompile(`^[a-z0-9][a-z0-9 -]{0,23}$`)
	inputNameRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	placeholderRE = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)
)

// ParseCatalog reads a bundle and checks every rule of the format contract.
// An unknown format is ErrUnknownFormat; any other defect is an *Error naming
// the template and the field. Unknown fields are refused: a field a newer
// catalog adds means a new format, and a field silently dropped would be a
// template doing less than it says.
func ParseCatalog(data []byte) (Catalog, error) {
	var head struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return Catalog{}, fmt.Errorf("read catalog: %w", err)
	}
	if head.Format != Format {
		return Catalog{}, fmt.Errorf("catalog format %d: %w", head.Format, ErrUnknownFormat)
	}
	var c Catalog
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Catalog{}, fmt.Errorf("read catalog: %w", err)
	}
	if dec.More() {
		return Catalog{}, errors.New("read catalog: more than one JSON value")
	}
	if !catalogVerRE.MatchString(c.Version) {
		return Catalog{}, &Error{Field: "version", Message: fmt.Sprintf("%q must look like 2026.11.12 or 2026.11.12.2", c.Version)}
	}
	seen := map[string]bool{}
	for _, t := range c.Templates {
		if seen[t.Name] {
			return Catalog{}, defect(t, "name", "is used by two templates")
		}
		seen[t.Name] = true
		if err := t.Validate(); err != nil {
			return Catalog{}, err
		}
	}
	return c, nil
}

// printable says s has 1 to max printable characters and no line break.
func printable(s string, max int) bool {
	n := utf8.RuneCountInString(s)
	if n < 1 || n > max {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) })
}

// Validate checks the template against the format contract, without
// translating it (Check does that). The error is an *Error.
func (t Template) Validate() error {
	if err := app.ValidName("name", t.Name); err != nil || len(t.Name) > maxName {
		return defect(t, "name", "must be a lowercase DNS label of at most %d characters, without %q", maxName, "--")
	}
	if !printable(t.Title, maxTitle) {
		return defect(t, "title", "must be 1 to %d printable characters", maxTitle)
	}
	if !versionRE.MatchString(t.Version) {
		return defect(t, "version", "%q must be 1 to 40 characters of letters, digits and . _ + -", t.Version)
	}
	if !printable(t.Description, maxDescription) {
		return defect(t, "description", "must be 1 to %d printable characters on one line", maxDescription)
	}
	if len(t.Tags) > maxTags {
		return defect(t, "tags", "has %d tags; at most %d are allowed", len(t.Tags), maxTags)
	}
	for i, tag := range t.Tags {
		if !tagRE.MatchString(tag) {
			return defect(t, fmt.Sprintf("tags[%d]", i), "%q must be lowercase letters, digits, spaces and hyphens, at most 24 characters", tag)
		}
		if slices.Index(t.Tags, tag) != i {
			return defect(t, fmt.Sprintf("tags[%d]", i), "%q is listed twice", tag)
		}
	}
	if u, err := url.Parse(t.Upstream); err != nil || u.Scheme != "https" || u.Host == "" {
		return defect(t, "upstream", "must be an https:// address")
	}
	if err := app.ValidName("appName", t.AppName); err != nil {
		return defect(t, "appName", "must be a lowercase DNS label")
	}
	if len(t.Notes) > maxNotes {
		return defect(t, "notes", "is %d bytes; at most %d are allowed", len(t.Notes), maxNotes)
	}
	if t.Requires != "" && !releaseRE.MatchString(t.Requires) {
		return defect(t, "requires", "%q must be a release version like 0.20.0", t.Requires)
	}
	if strings.ContainsFunc(t.Broken, unicode.IsControl) {
		return defect(t, "broken", "must be plain text on one line")
	}

	if t.Compose == "" {
		return defect(t, "compose", "is empty")
	}
	if len(t.Compose) > compose.MaxComposeBytes {
		return defect(t, "compose", "is %d bytes; at most %d are read", len(t.Compose), compose.MaxComposeBytes)
	}
	names, err := t.validateNames()
	if err != nil {
		return err
	}
	if err := t.validatePlaceholders(names); err != nil {
		return err
	}
	if t.Logo != "" {
		if err := CheckLogo(t.Logo); err != nil {
			return defect(t, "logo", "%v", err)
		}
	}
	return nil
}

// kind of a declared name.
type nameKind int

const (
	plainName  nameKind = iota + 1 // domain, email, text
	secretName                     // a secret input or a generated secret
)

// validateNames checks the inputs and the generated secrets, and returns the
// kind of each declared name.
func (t Template) validateNames() (map[string]nameKind, error) {
	names := map[string]nameKind{}
	if len(t.Inputs) > maxInputs {
		return nil, defect(t, "inputs", "has %d inputs; at most %d are allowed", len(t.Inputs), maxInputs)
	}
	for i, in := range t.Inputs {
		f := fmt.Sprintf("inputs[%d]", i)
		if !inputNameRE.MatchString(in.Name) {
			return nil, defect(t, f+".name", "%q must be lowercase letters, digits and _, starting with a letter, at most 32 characters", in.Name)
		}
		if _, dup := names[in.Name]; dup {
			return nil, defect(t, f+".name", "%q is declared twice", in.Name)
		}
		if !printable(in.Label, maxLabel) {
			return nil, defect(t, f+".label", "must be 1 to %d printable characters", maxLabel)
		}
		if len(in.Help) > maxHelp || strings.ContainsFunc(in.Help, unicode.IsControl) {
			return nil, defect(t, f+".help", "must be plain text of at most %d bytes", maxHelp)
		}
		switch in.Type {
		case TypeDomain, TypeEmail, TypeText:
			names[in.Name] = plainName
		case TypeSecret:
			names[in.Name] = secretName
		default:
			return nil, defect(t, f+".type", "must be one of %s, %s, %s, %s", TypeDomain, TypeEmail, TypeText, TypeSecret)
		}
		if in.Type == TypeText {
			if in.Pattern == "" {
				return nil, defect(t, f+".pattern", "is required for a text input")
			}
			if len(in.Pattern) > maxPattern {
				return nil, defect(t, f+".pattern", "is %d bytes; at most %d are allowed", len(in.Pattern), maxPattern)
			}
			if _, err := compilePattern(in.Pattern); err != nil {
				return nil, defect(t, f+".pattern", "isn't a regular expression: %v", err)
			}
		} else if in.Pattern != "" {
			return nil, defect(t, f+".pattern", "only a text input has a pattern")
		}
		if in.Type == TypeSecret && in.Default != "" {
			return nil, defect(t, f+".default", "a secret input has no default")
		}
		if in.Default == "" && in.Example == "" {
			return nil, defect(t, f+".example", "is required when there is no default")
		}
		// The example and the default must be valid answers.
		for _, v := range []struct{ field, value string }{{".default", in.Default}, {".example", in.Example}} {
			if v.value == "" {
				continue
			}
			if _, err := checkValue(in, v.value); err != nil {
				return nil, defect(t, f+v.field, "%s", err.Error())
			}
		}
	}
	if len(t.Generated) > maxGenerated {
		return nil, defect(t, "generated", "has %d secrets; at most %d are allowed", len(t.Generated), maxGenerated)
	}
	for i, g := range t.Generated {
		f := fmt.Sprintf("generated[%d]", i)
		if !inputNameRE.MatchString(g.Name) {
			return nil, defect(t, f+".name", "%q must be lowercase letters, digits and _, starting with a letter, at most 32 characters", g.Name)
		}
		if _, dup := names[g.Name]; dup {
			return nil, defect(t, f+".name", "%q is declared twice", g.Name)
		}
		names[g.Name] = secretName
		if g.Length < 16 || g.Length > 128 {
			return nil, defect(t, f+".length", "must be between 16 and 128, got %d", g.Length)
		}
		if _, ok := alphabets[g.Alphabet]; !ok {
			return nil, defect(t, f+".alphabet", "must be one of %s, %s, %s", AlphabetHex, AlphabetAlnum, AlphabetBase64URL)
		}
	}
	return names, nil
}

// compilePattern anchors a text input's pattern the way the contract says.
func compilePattern(p string) (*regexp.Regexp, error) {
	return regexp.Compile(`^(?:` + p + `)$`)
}

// validatePlaceholders checks where each placeholder is and that every
// declared name is used (R2).
func (t Template) validatePlaceholders(names map[string]nameKind) error {
	used := map[string]bool{}

	// A plain place accepts plain names only; a secret value place accepts both.
	scan := func(field, text string, secretPlace bool) error {
		for _, m := range placeholderRE.FindAllStringSubmatch(text, -1) {
			kind, ok := names[m[1]]
			switch {
			case !ok:
				return defect(t, field, "uses {{%s}}, which is not an input or a generated secret", m[1])
			case kind == secretName && !secretPlace:
				return defect(t, field, "uses the secret {{%s}}: secrets may only go in a service's secrets[].value", m[1])
			}
			used[m[1]] = true
		}
		return nil
	}

	if err := scan("compose", t.Compose, false); err != nil {
		return err
	}
	// Notes are shown, so they take plain names only, and a name used only
	// there doesn't count as used (it would be a question that changes nothing).
	notesUsed := map[string]bool{}
	for _, m := range placeholderRE.FindAllStringSubmatch(t.Notes, -1) {
		kind, ok := names[m[1]]
		switch {
		case !ok:
			return defect(t, "notes", "uses {{%s}}, which is not an input", m[1])
		case kind == secretName:
			return defect(t, "notes", "uses the secret {{%s}}: notes are shown, so only plain inputs may be in them", m[1])
		}
		notesUsed[m[1]] = true
	}

	for _, svc := range sortedOverlayNames(t.Services) {
		ov := cloneOverlay(t.Services[svc])
		for _, ref := range overlayStrings(&ov) {
			field := "services." + svc + "." + ref.field
			if err := scan(field, *ref.s, ref.secretValue); err != nil {
				return err
			}
		}
	}

	for _, in := range t.Inputs {
		if !used[in.Name] {
			return defect(t, "inputs."+in.Name, "is declared but never used in the Compose file or the services")
		}
	}
	for _, g := range t.Generated {
		if !used[g.Name] {
			return defect(t, "generated."+g.Name, "is declared but never used")
		}
	}
	return nil
}

func sortedOverlayNames(m map[string]compose.Overlay) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// stringRef points at one string of an overlay.
type stringRef struct {
	field       string // path inside the overlay: domains[0].host
	s           *string
	secretValue bool // the string is a secret's value
}

// overlayStrings lists every string of an overlay a placeholder may stand in.
// It points into ov, so ov must be a copy of its own (cloneOverlay): writing
// through a stringRef must never change the template.
func overlayStrings(ov *compose.Overlay) []stringRef {
	var refs []stringRef
	for i := range ov.Domains {
		d := &ov.Domains[i]
		refs = append(refs,
			stringRef{field: fmt.Sprintf("domains[%d].host", i), s: &d.Host},
			stringRef{field: fmt.Sprintf("domains[%d].path", i), s: &d.Path})
	}
	if ov.Health != nil {
		refs = append(refs, stringRef{field: "health.path", s: &ov.Health.Path})
	}
	for i := range ov.Secrets {
		sec := &ov.Secrets[i]
		refs = append(refs, stringRef{field: fmt.Sprintf("secrets[%d].name", i), s: &sec.Name})
		if sec.Value != nil {
			refs = append(refs, stringRef{field: fmt.Sprintf("secrets[%d].value", i), s: sec.Value, secretValue: true})
		}
	}
	if ov.Backup != nil {
		refs = append(refs,
			stringRef{field: "backup.file", s: &ov.Backup.File},
			stringRef{field: "backup.schedule", s: &ov.Backup.Schedule})
	}
	return refs
}

// Newer says catalog version a is newer than b: the dot-separated numbers
// compared from the left, a missing part counting as zero. A version that
// isn't numbers is never newer.
func Newer(a, b string) bool {
	pa, okA := versionParts(a)
	pb, okB := versionParts(b)
	if !okA || !okB {
		return false
	}
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func versionParts(v string) ([]int, bool) {
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}
