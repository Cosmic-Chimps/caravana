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
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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

// The catalog formats this Caravana reads. Format 1 is what every Caravana up to
// 0.22 reads and what catalog.json keeps being; format 2 (feature 058) adds
// choice inputs, optional inputs, conditions and 20 inputs, in catalog.v2.json.
const (
	FormatV1 = 1
	// Format is the newest format, the one the embedded catalog is in.
	Format = 2
)

// Limits of the format.
const (
	maxTitle       = 60
	maxDescription = 200
	maxTags        = 6
	maxNotes       = 2000
	maxInputsV1    = 12
	maxInputs      = 20
	maxChoices     = 8
	maxCombos      = 64
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
	// TypeChoice (format 2) is one of a short list of answers.
	TypeChoice = "choice"
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
	// ItemWhens holds the conditions (format 2) of the items of each service's
	// lists: ItemWhens[service][field] has one entry per item of the list
	// (domains, secrets, identities, publish), nil for an item with none. In
	// JSON each is the item's own "when". A list with no condition has no entry.
	ItemWhens map[string]map[string][]Condition `json:"-"`
	// Compose is the Compose file with placeholders.
	Compose string `json:"compose,omitempty"`
	// Logo is an SVG, or empty.
	Logo string `json:"logo,omitempty"`
	// Unsupported, when not empty, says the catalog lists this template but this
	// Caravana can't read it (it was made for a newer one): only its name,
	// title and description are known. It is never in JSON.
	Unsupported string `json:"-"`
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
	// Required (format 2) false lets the owner leave the input empty; an empty
	// optional input renders as an empty string. nil means true.
	Required *bool `json:"required,omitempty"`
	// Choices are the answers of a choice input, 2 to 8.
	Choices []Choice `json:"choices,omitempty"`
	// When (format 2) asks the input only when earlier choice inputs have the
	// given answers. An input that isn't asked is empty, and anything sent for
	// it is ignored.
	When Condition `json:"when,omitempty"`
}

// IsRequired says an empty answer is refused (when the input is asked and
// has no default).
func (in Input) IsRequired() bool { return in.Required == nil || *in.Required }

// ChoiceLabel is the label of a choice value, or the value itself.
func (in Input) ChoiceLabel(value string) string {
	for _, c := range in.Choices {
		if c.Value == value {
			return c.Label
		}
	}
	return value
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
	choiceValueRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	placeholderRE = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)
)

// ParseCatalog reads a bundle (format 1 or 2) and checks every rule of the
// format contract. An unknown format is ErrUnknownFormat; any other defect is an
// *Error naming the template and the field. Unknown fields are refused: a field a
// newer catalog adds means a new format, and a field silently dropped would be
// a template doing less than it says. See ParseCatalogFor for the one tolerance
// format 2 has.
func ParseCatalog(data []byte) (Catalog, error) { return ParseCatalogFor(data, nil) }

// ParseCatalogFor is ParseCatalog for a Caravana that knows which `requires`
// it is too old for: needsNewer(requires) says the release is newer than the
// running one (nil: none is). In a format-2 catalog a template that this
// Caravana can't read keeps the rest of the catalog: one with a field it doesn't
// know, or one that fails the rules and whose `requires` is newer than the
// running release, is listed with Unsupported set instead of failing the whole
// catalog. A broken file, an unknown field at the catalog's top level and a
// template that fails the rules with no such excuse still fail it.
func ParseCatalogFor(data []byte, needsNewer func(requires string) bool) (Catalog, error) {
	var head struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return Catalog{}, fmt.Errorf("read catalog: %w", err)
	}
	if head.Format != FormatV1 && head.Format != Format {
		return Catalog{}, fmt.Errorf("catalog format %d: %w", head.Format, ErrUnknownFormat)
	}
	var raw struct {
		Format    int               `json:"format"`
		Version   string            `json:"version"`
		Templates []json.RawMessage `json:"templates"`
	}
	if err := decodeStrict(data, &raw); err != nil {
		return Catalog{}, fmt.Errorf("read catalog: %w", err)
	}
	c := Catalog{Format: raw.Format, Version: raw.Version}
	if !catalogVerRE.MatchString(c.Version) {
		return Catalog{}, &Error{Field: "version", Message: fmt.Sprintf("%q must look like 2026.11.12 or 2026.11.12.2", c.Version)}
	}
	seen := map[string]bool{}
	for i, one := range raw.Templates {
		var t Template
		err := json.Unmarshal(one, &t)
		if err == nil {
			err = t.validate(c.Format)
		}
		if err != nil {
			stub, ok := unsupportedStub(one, err, c.Format, needsNewer)
			if !ok {
				var e *Error
				if errors.As(err, &e) {
					return Catalog{}, err
				}
				return Catalog{}, fmt.Errorf("read catalog: templates[%d]: %w", i, err)
			}
			t = stub
		}
		if seen[t.Name] {
			return Catalog{}, defect(t, "name", "is used by two templates")
		}
		seen[t.Name] = true
		c.Templates = append(c.Templates, t)
	}
	return c, nil
}

// unsupportedStub makes the listing of a template this Caravana can't read, if
// the failure has the excuse of being newer than it: an unknown field, or a
// `requires` the running release is too old for. The template's own words are
// read loosely and only used when they are fit to show.
func unsupportedStub(raw json.RawMessage, cause error, format int, needsNewer func(string) bool) (Template, bool) {
	if format < Format {
		return Template{}, false
	}
	var loose struct {
		Name        string   `json:"name"`
		Title       string   `json:"title"`
		Version     string   `json:"version"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Upstream    string   `json:"upstream"`
		AppName     string   `json:"appName"`
		Requires    string   `json:"requires"`
	}
	if err := json.Unmarshal(raw, &loose); err != nil {
		return Template{}, false
	}
	if app.ValidName("name", loose.Name) != nil || len(loose.Name) > maxName {
		return Template{}, false
	}
	reason := ""
	switch {
	case releaseRE.MatchString(loose.Requires) && needsNewer != nil && needsNewer(loose.Requires):
		reason = "needs caravana " + loose.Requires + " or newer"
	case isUnknownField(cause):
		reason = "made for a newer caravana: it uses something this one doesn't know"
	default:
		return Template{}, false
	}
	t := Template{Name: loose.Name, Title: loose.Name, Version: "?", Unsupported: reason}
	if printable(loose.Title, maxTitle) {
		t.Title = loose.Title
	}
	if versionRE.MatchString(loose.Version) {
		t.Version = loose.Version
	}
	if printable(loose.Description, maxDescription) {
		t.Description = loose.Description
	} else {
		t.Description = "This template can't be read by this version of caravana."
	}
	if len(loose.Tags) <= maxTags {
		for _, tag := range loose.Tags {
			if tagRE.MatchString(tag) && !slices.Contains(t.Tags, tag) {
				t.Tags = append(t.Tags, tag)
			}
		}
	}
	if u, err := url.Parse(loose.Upstream); err == nil && u.Scheme == "https" && u.Host != "" {
		t.Upstream = loose.Upstream
	}
	if app.ValidName("appName", loose.AppName) == nil {
		t.AppName = loose.AppName
	}
	if releaseRE.MatchString(loose.Requires) {
		t.Requires = loose.Requires
	}
	return t, true
}

// printable says s has 1 to max printable characters and no line break.
func printable(s string, max int) bool {
	n := utf8.RuneCountInString(s)
	if n < 1 || n > max {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) })
}

// Validate checks the template against the format contract (format 2), without
// translating it (Check does that). The error is an *Error.
func (t Template) Validate() error { return t.validate(Format) }

// validate checks the template against the rules of a format. Format 1 is
// what Caravana 0.22 and older read: no choice inputs, no optional ones, no
// conditions and at most 12 inputs; a template that passes it can go in
// catalog.json.
func (t Template) validate(format int) error {
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
	names, err := t.validateNames(format)
	if err != nil {
		return err
	}
	if err := t.validatePlaceholders(names, format); err != nil {
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
func (t Template) validateNames(format int) (map[string]nameKind, error) {
	names := map[string]nameKind{}
	limit := maxInputs
	if format < Format {
		limit = maxInputsV1
	}
	if len(t.Inputs) > limit {
		return nil, defect(t, "inputs", "has %d inputs; at most %d are allowed", len(t.Inputs), limit)
	}
	index := map[string]int{}
	combos := 1
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
		if format < Format && (in.Required != nil || len(in.Choices) > 0 || in.When != nil) {
			return nil, defect(t, f, "uses required, choices or when, which catalog format 1 doesn't have")
		}
		switch {
		case in.Type == TypeDomain, in.Type == TypeEmail, in.Type == TypeText:
			names[in.Name] = plainName
		case in.Type == TypeChoice && format >= Format:
			names[in.Name] = plainName
		case in.Type == TypeSecret:
			names[in.Name] = secretName
		case format < Format:
			return nil, defect(t, f+".type", "must be one of %s, %s, %s, %s", TypeDomain, TypeEmail, TypeText, TypeSecret)
		default:
			return nil, defect(t, f+".type", "must be one of %s, %s, %s, %s, %s", TypeDomain, TypeEmail, TypeText, TypeSecret, TypeChoice)
		}
		index[in.Name] = i
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
		if in.Type == TypeChoice {
			if err := t.validateChoices(f, in); err != nil {
				return nil, err
			}
			combos *= len(in.Choices)
		} else if len(in.Choices) > 0 {
			return nil, defect(t, f+".choices", "only a choice input has choices")
		}
		if in.Type == TypeSecret && in.Default != "" {
			return nil, defect(t, f+".default", "a secret input has no default")
		}
		if in.Type == TypeChoice {
			if in.Example != "" {
				return nil, defect(t, f+".example", "a choice input has no example: every choice is checked")
			}
		} else if in.Default == "" && in.Example == "" {
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
		if in.When != nil {
			earlier := func(name string) bool { j, ok := index[name]; return ok && j < i }
			if err := t.validateCondition(f+".when", in.When, false, earlier); err != nil {
				return nil, err
			}
		}
	}
	if combos > maxCombos {
		return nil, defect(t, "inputs", "its choices make %d combinations; at most %d are checked", combos, maxCombos)
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

// validateChoices checks a choice input's list and default.
func (t Template) validateChoices(f string, in Input) error {
	if n := len(in.Choices); n < 2 || n > maxChoices {
		return defect(t, f+".choices", "has %d choices; a choice input has 2 to %d", n, maxChoices)
	}
	seen := map[string]bool{}
	for j, c := range in.Choices {
		cf := fmt.Sprintf("%s.choices[%d]", f, j)
		if !choiceValueRE.MatchString(c.Value) {
			return defect(t, cf+".value", "%q must be lowercase letters, digits, _ and -, starting with a letter, at most 32 characters", c.Value)
		}
		if seen[c.Value] {
			return defect(t, cf+".value", "%q is listed twice", c.Value)
		}
		seen[c.Value] = true
		if !printable(c.Label, maxLabel) {
			return defect(t, cf+".label", "must be 1 to %d printable characters", maxLabel)
		}
	}
	if in.Default != "" && !seen[in.Default] {
		return defect(t, f+".default", "%q is not one of the choices", in.Default)
	}
	return nil
}

// inputOf is the declared input called name.
func (t Template) inputOf(name string) (Input, bool) {
	for _, in := range t.Inputs {
		if in.Name == name {
			return in, true
		}
	}
	return Input{}, false
}

// validateCondition checks a condition: it names inputs (not generated secrets;
// allowed, when not nil, narrows which), compares a choice input only with its
// own values, and says "not empty" (CondValue.Any) only where allowAny.
func (t Template) validateCondition(field string, c Condition, allowAny bool, allowed func(name string) bool) error {
	if len(c) == 0 {
		return defect(t, field, "must name at least one input")
	}
	for _, name := range c.names() {
		in, ok := t.inputOf(name)
		if !ok {
			return defect(t, field, "names %q, which is not an input", name)
		}
		if allowed != nil && !allowed(name) {
			return defect(t, field, "names %q, which must be declared before this input", name)
		}
		cv := c[name]
		if cv.Any {
			if !allowAny {
				return defect(t, field, "%q must be compared with a value here", name)
			}
			continue
		}
		if len(cv.Values) == 0 {
			return defect(t, field, "%q needs a value, a list of values or true", name)
		}
		if in.Type != TypeChoice {
			return defect(t, field, "%q isn't a choice input: say true for \"not empty\"", name)
		}
		for _, v := range cv.Values {
			if !slices.ContainsFunc(in.Choices, func(ch Choice) bool { return ch.Value == v }) {
				return defect(t, field, "%q is not a choice of %q", v, name)
			}
		}
	}
	return nil
}

// compilePattern anchors a text input's pattern the way the contract says.
func compilePattern(p string) (*regexp.Regexp, error) {
	return regexp.Compile(`^(?:` + p + `)$`)
}

// validatePlaceholders checks where each placeholder is, that every
// condition names a real input and that every declared name is used (R2). A
// choice input used only in conditions counts as used.
func (t Template) validatePlaceholders(names map[string]nameKind, format int) error {
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
	useCondition := func(c Condition) {
		for name := range c {
			used[name] = true
		}
	}

	if err := scan("compose", t.Compose, false); err != nil {
		return err
	}
	marks, err := scanMarkers(t.Compose)
	if err != nil {
		var me *MarkerError
		if errors.As(err, &me) {
			return defect(t, "compose", "line %d: %s", me.Line, me.Message)
		}
		return defect(t, "compose", "%v", err)
	}
	if format < Format && len(marks) > 0 {
		return defect(t, "compose", "line %d: a marker comment is catalog format 2; format 1 has none", marks[0].Line+1)
	}
	for _, m := range marks {
		if err := t.validateCondition(fmt.Sprintf("compose: line %d", m.Line+1), m.Cond, true, nil); err != nil {
			return err
		}
		useCondition(m.Cond)
	}
	// Notes are shown, so they take plain names only, and a name used only
	// there doesn't count as used (it would be a question that changes nothing).
	for _, m := range placeholderRE.FindAllStringSubmatch(t.Notes, -1) {
		kind, ok := names[m[1]]
		switch {
		case !ok:
			return defect(t, "notes", "uses {{%s}}, which is not an input", m[1])
		case kind == secretName:
			return defect(t, "notes", "uses the secret {{%s}}: notes are shown, so only plain inputs may be in them", m[1])
		}
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
	for _, svc := range slices.Sorted(maps.Keys(t.ItemWhens)) {
		for _, field := range whenLists {
			for i, c := range t.ItemWhens[svc][field] {
				if c == nil {
					continue
				}
				f := fmt.Sprintf("services.%s.%s[%d].when", svc, field, i)
				if format < Format {
					return defect(t, f, "a condition is catalog format 2; format 1 has none")
				}
				if err := t.validateCondition(f, c, true, nil); err != nil {
					return err
				}
				useCondition(c)
			}
		}
	}
	for _, in := range t.Inputs {
		useCondition(in.When)
	}

	for _, in := range t.Inputs {
		if !used[in.Name] {
			return defect(t, "inputs."+in.Name, "is declared but never used in the Compose file, the services or a condition")
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
