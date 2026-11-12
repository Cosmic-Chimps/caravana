package templates

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
)

var (
	hostRE      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	emailLocal  = regexp.MustCompile(`^[A-Za-z0-9._%+-]{1,64}$`)
	composeSafe = regexp.MustCompile(`^[A-Za-z0-9._@+:/=-]{1,253}$`)
)

// maxSecretInput is the longest secret an owner may type.
const maxSecretInput = 4096

// checkValue checks one value (already trimmed) against its input and returns
// the value to use: a domain is lowercased. The error's text is a sentence
// for the owner; it never holds the value of a secret.
func checkValue(in Input, v string) (string, error) {
	switch in.Type {
	case TypeSecret:
		switch {
		case v == "":
			return "", errors.New("is required")
		case len(v) > maxSecretInput:
			return "", fmt.Errorf("is %d bytes; at most %d are allowed", len(v), maxSecretInput)
		case strings.ContainsAny(v, "\x00\r\n"):
			return "", errors.New("must be one line without NUL characters")
		}
		return v, nil
	case TypeDomain:
		v = strings.ToLower(v)
		if len(v) > 253 || !hostRE.MatchString(v) {
			return "", errors.New("must be a host name like status.example.com")
		}
	case TypeEmail:
		local, host, ok := strings.Cut(v, "@")
		if !ok || !emailLocal.MatchString(local) || len(host) > 253 || !hostRE.MatchString(strings.ToLower(host)) {
			return "", errors.New("must be an email address like you@example.com")
		}
	case TypeText:
		re, err := compilePattern(in.Pattern)
		if err != nil {
			return "", errors.New("has a pattern Caravana can't read")
		}
		if !re.MatchString(v) {
			return "", errors.New("isn't in the form this template expects")
		}
	default:
		return "", fmt.Errorf("has the type %q, which isn't supported", in.Type)
	}
	if !composeSafe.MatchString(v) {
		return "", errors.New("may only have letters, digits and . _ @ + : / = -")
	}
	return v, nil
}

// CheckInputs checks what the owner typed against the template's inputs and
// returns the values to render with: each trimmed of surrounding spaces, an
// empty answer replaced by the input's default. The error is an
// *app.FieldError with the field "inputs.<name>". A name the template doesn't
// ask for is refused too.
func (t Template) CheckInputs(in map[string]string) (map[string]string, error) {
	return t.checkInputs(in, true)
}

// CheckPreviewInputs is CheckInputs for a preview: a secret input may be
// empty, since the console never keeps what was typed in one.
func (t Template) CheckPreviewInputs(in map[string]string) (map[string]string, error) {
	return t.checkInputs(in, false)
}

func (t Template) checkInputs(in map[string]string, secretsRequired bool) (map[string]string, error) {
	known := map[string]bool{}
	for _, input := range t.Inputs {
		known[input.Name] = true
	}
	for _, name := range slices.Sorted(maps.Keys(in)) {
		if !known[name] {
			return nil, app.NewFieldError("inputs."+name, "is not an input of this template")
		}
	}
	out := make(map[string]string, len(t.Inputs))
	for _, input := range t.Inputs {
		v := strings.TrimSpace(in[input.Name])
		if v == "" {
			v = input.Default
		}
		if v == "" && input.Type == TypeSecret && !secretsRequired {
			out[input.Name] = ""
			continue
		}
		if v == "" {
			return nil, app.NewFieldError("inputs."+input.Name, "is required")
		}
		v, err := checkValue(input, v)
		if err != nil {
			return nil, app.NewFieldError("inputs."+input.Name, "%s", err.Error())
		}
		out[input.Name] = v
	}
	return out, nil
}

var alphabets = map[string]string{
	AlphabetHex:       "0123456789abcdef",
	AlphabetAlnum:     "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
	AlphabetBase64URL: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_",
}

// Generate makes the template's generated secrets from r (crypto/rand.Reader
// in production; a test gives its own). The values exist only in the returned
// map: the caller sends them in the deploy request and nowhere else.
func (t Template) Generate(r io.Reader) (map[string]string, error) {
	out := make(map[string]string, len(t.Generated))
	for _, g := range t.Generated {
		alphabet, ok := alphabets[g.Alphabet]
		if !ok {
			return nil, fmt.Errorf("generate %q: alphabet %q isn't supported", g.Name, g.Alphabet)
		}
		v, err := sample(r, alphabet, g.Length)
		if err != nil {
			return nil, fmt.Errorf("generate %q: %w", g.Name, err)
		}
		out[g.Name] = v
	}
	return out, nil
}

// sample draws length characters of alphabet from r. A random byte is used
// only when it is below the largest multiple of len(alphabet) that fits in
// 256, so every character is equally likely (no modulo bias).
func sample(r io.Reader, alphabet string, length int) (string, error) {
	limit := 256 - 256%len(alphabet)
	out := make([]byte, 0, length)
	buf := make([]byte, length*2)
	for len(out) < length {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", fmt.Errorf("read random bytes: %w", err)
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, alphabet[int(b)%len(alphabet)])
			if len(out) == length {
				break
			}
		}
	}
	return string(out), nil
}

// PreviewValues returns values for a preview: the checked inputs, with each
// secret input and each generated secret empty. A preview makes nothing.
func (t Template) PreviewValues(inputs map[string]string) map[string]string {
	values := maps.Clone(inputs)
	if values == nil {
		values = map[string]string{}
	}
	for _, in := range t.Inputs {
		if in.Type == TypeSecret {
			values[in.Name] = ""
		}
	}
	for _, g := range t.Generated {
		values[g.Name] = ""
	}
	return values
}

// cloneOverlay copies an overlay deeply enough that writing through its
// strings, slices and pointers never changes the original.
func cloneOverlay(ov compose.Overlay) compose.Overlay {
	ov.Domains = slices.Clone(ov.Domains)
	if ov.Health != nil {
		h := *ov.Health
		ov.Health = &h
	}
	if ov.Secrets != nil {
		secrets := make([]app.Secret, len(ov.Secrets))
		for i, s := range ov.Secrets {
			secrets[i].Name = s.Name
			if s.Value != nil {
				v := *s.Value
				secrets[i].Value = &v
			}
		}
		ov.Secrets = secrets
	}
	if ov.Backup != nil {
		b := *ov.Backup
		ov.Backup = &b
	}
	return ov
}

// substitute replaces each placeholder once, left to right. A value is never
// looked at again, so a value that looks like a placeholder stays text. A name
// without a value is reported through missing.
func substitute(text string, values map[string]string, missing func(name string) string) string {
	return placeholderRE.ReplaceAllStringFunc(text, func(m string) string {
		name := placeholderRE.FindStringSubmatch(m)[1]
		if v, ok := values[name]; ok {
			return v
		}
		return missing(name)
	})
}

// Render turns the template into a deploy request. values holds every input
// (checked, from CheckInputs) and every generated secret (from Generate); a
// name without a value is an error naming the input. The Compose text gets the
// plain values only (validation guarantees it names no secret); the overlay's
// strings are replaced one by one, so no value can change its structure. Name
// is left empty: the display name is the app ID, as for any Compose deploy.
func (t Template) Render(values map[string]string) (compose.Request, error) {
	var missingName string
	missing := func(name string) string {
		if missingName == "" {
			missingName = name
		}
		return ""
	}

	req := compose.Request{Compose: substitute(t.Compose, values, missing)}
	if len(t.Services) > 0 {
		req.Services = make(map[string]compose.Overlay, len(t.Services))
	}
	for svc, ov := range t.Services {
		ov = cloneOverlay(ov)
		for _, ref := range overlayStrings(&ov) {
			*ref.s = substitute(*ref.s, values, missing)
		}
		req.Services[svc] = ov
	}
	if missingName != "" {
		return compose.Request{}, app.NewFieldError("inputs."+missingName, "has no value")
	}
	return req, nil
}

// RenderNotes shows the template's notes with the plain inputs filled in. A
// name without a value reads as <your label>, so the page can show the notes
// before anything is typed.
func (t Template) RenderNotes(inputs map[string]string) string {
	labels := map[string]string{}
	for _, in := range t.Inputs {
		labels[in.Name] = in.Label
	}
	return substitute(t.Notes, inputs, func(name string) string {
		label := labels[name]
		if label == "" {
			label = name
		}
		return "<your " + strings.ToLower(label) + ">"
	})
}

// ExampleInputs are answers that pass CheckInputs: each input's default, else
// its example.
func (t Template) ExampleInputs() map[string]string {
	out := make(map[string]string, len(t.Inputs))
	for _, in := range t.Inputs {
		if in.Default != "" {
			out[in.Name] = in.Default
		} else {
			out[in.Name] = in.Example
		}
	}
	return out
}

// DomainInputs maps the overlay path of each domain host that is exactly one
// domain input ("services.hollo.domains[0].host") to that input's name, so a
// refusal at that path can be shown at the input the owner typed (R5).
func (t Template) DomainInputs() map[string]string {
	domainInput := map[string]bool{}
	for _, in := range t.Inputs {
		if in.Type == TypeDomain {
			domainInput[in.Name] = true
		}
	}
	out := map[string]string{}
	for svc, ov := range t.Services {
		for i, d := range ov.Domains {
			m := placeholderRE.FindStringSubmatch(d.Host)
			if m != nil && m[0] == d.Host && domainInput[m[1]] {
				out[fmt.Sprintf("services.%s.domains[%d].host", svc, i)] = m[1]
			}
		}
	}
	return out
}

// Logo rules (research R8): small, well-formed SVG that runs nothing.
var (
	logoEventRE = regexp.MustCompile(`(?i)[\s"'/]on[a-z]+\s*=`)
	logoURLRE   = regexp.MustCompile(`(?i)url\(\s*["']?\s*(https?:|//)`)
	logoUseRE   = regexp.MustCompile(`(?i)<use\b[^>]*\bhref\s*=\s*["']?\s*[^#"'\s]`)
)

// CheckLogo refuses an SVG that is too big, isn't XML with an svg root, or
// could run script or load something from outside. The logo is also served
// with a CSP that runs nothing and drawn with <img>; this is the first of
// those layers.
func CheckLogo(svg string) error {
	if len(svg) > maxLogoBytes {
		return fmt.Errorf("is %d bytes; at most %d are allowed", len(svg), maxLogoBytes)
	}
	lower := strings.ToLower(svg)
	for _, bad := range []string{"<script", "javascript:", "<foreignobject", "<iframe", "<image", "@import", "<!entity", "<!doctype"} {
		if strings.Contains(lower, bad) {
			return fmt.Errorf("must not contain %q", bad)
		}
	}
	switch {
	case logoEventRE.MatchString(svg):
		return errors.New("must not have event attributes (onload=...)")
	case logoURLRE.MatchString(svg):
		return errors.New("must not load anything from an outside address")
	case logoUseRE.MatchString(svg):
		return errors.New("must not use an outside <use> reference")
	}
	if strings.ContainsFunc(svg, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' }) {
		return errors.New("must be text")
	}
	return checkSVGRoot(svg)
}
