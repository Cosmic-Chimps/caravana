package app

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"maps"
)

// Secret templates (feature 053). A template is text with `${name}` places
// where the value of one of the secret's refs goes, and `$$` for a literal
// "$". It is parsed and rendered here, with no I/O: the service reads the
// refs' values from the provider and calls RenderTemplate.

const (
	// MaxSecretTemplate is the longest template, in bytes.
	MaxSecretTemplate = 4096
	// MaxTemplateRefs is how many refs one secret may name.
	MaxTemplateRefs = 16
)

var templateRefNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// ValidTemplateRefName reports whether name may name a ref of a template.
func ValidTemplateRefName(name string) bool { return templateRefNameRE.MatchString(name) }

// TemplatePart is one piece of a parsed template: literal text, or the name
// of a ref (Ref is true).
type TemplatePart struct {
	Text string // the literal text, or the ref's name
	Ref  bool
}

// TemplateError is a template that doesn't parse. Offset is the byte offset
// of the problem. The parser sees no value, so no message can hold one.
type TemplateError struct {
	Offset  int
	Message string
}

func (e *TemplateError) Error() string {
	return fmt.Sprintf("%s at byte %d", e.Message, e.Offset)
}

// ParseTemplate splits t into literal text and refs. `${name}` is a ref,
// `$$` is a literal "$", and any other "$" is an error: a lone "$", a "${"
// without its "}", or a name that isn't a ref name. The result has no empty
// literal parts and merges neighbouring literals.
func ParseTemplate(t string) ([]TemplatePart, error) {
	var parts []TemplatePart
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, TemplatePart{Text: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(t); {
		c := t[i]
		if c != '$' {
			lit.WriteByte(c)
			i++
			continue
		}
		if i+1 >= len(t) {
			return nil, &TemplateError{Offset: i, Message: `a lone "$" (write "$$" for a dollar sign)`}
		}
		switch t[i+1] {
		case '$':
			lit.WriteByte('$')
			i += 2
		case '{':
			end := strings.IndexByte(t[i+2:], '}')
			if end < 0 {
				return nil, &TemplateError{Offset: i, Message: `"${" without a closing "}"`}
			}
			name := t[i+2 : i+2+end]
			if !templateRefNameRE.MatchString(name) {
				return nil, &TemplateError{Offset: i, Message: `the name inside "${…}" must be letters, digits and "_", not starting with a digit, at most 64 characters`}
			}
			flush()
			parts = append(parts, TemplatePart{Text: name, Ref: true})
			i += 2 + end + 1
		default:
			return nil, &TemplateError{Offset: i, Message: `"$" must be followed by "$" or "{" (write "$$" for a dollar sign)`}
		}
	}
	flush()
	return parts, nil
}

// TemplateRefNames returns the ref names t uses, in order of first use. An
// unparsable t returns nil.
func TemplateRefNames(t string) []string {
	parts, err := ParseTemplate(t)
	if err != nil {
		return nil
	}
	var names []string
	for _, p := range parts {
		if p.Ref && !slices.Contains(names, p.Text) {
			names = append(names, p.Text)
		}
	}
	return names
}

// RenderTemplate builds the value of a template from the values of its refs.
// A value is inserted as it is: a "$" or a "${x}" inside it is never
// expanded again. A ref without a value is an error naming the ref.
func RenderTemplate(t string, values map[string]string) (string, error) {
	parts, err := ParseTemplate(t)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, p := range parts {
		if !p.Ref {
			b.WriteString(p.Text)
			continue
		}
		v, ok := values[p.Text]
		if !ok {
			return "", fmt.Errorf("the template's ref %q has no value", p.Text)
		}
		b.WriteString(v)
	}
	return b.String(), nil
}

// validateSecretTemplate checks a secret's template and refs: field is
// services[i].secrets[j]. Every `${name}` must be in Refs, every ref must be
// used, and each ref is checked like a `from`.
func validateSecretTemplate(field string, s Secret) error {
	tf := field + ".template"
	if s.Value != nil || s.From != nil {
		return NewFieldError(tf, "a secret has a value, a reference or a template, only one")
	}
	t := s.Template
	switch {
	case t == "":
		return NewFieldError(tf, "must not be empty")
	case len(t) > MaxSecretTemplate:
		return NewFieldError(tf, "must be at most %d bytes", MaxSecretTemplate)
	case !utf8.ValidString(t):
		return NewFieldError(tf, "must be UTF-8 text")
	case strings.ContainsRune(t, 0):
		return NewFieldError(tf, "must not contain a NUL byte")
	}
	parts, err := ParseTemplate(t)
	if err != nil {
		return NewFieldError(tf, "%s", err.Error())
	}
	if len(s.Refs) > MaxTemplateRefs {
		return NewFieldError(field+".refs", "at most %d references", MaxTemplateRefs)
	}
	used := map[string]bool{}
	for _, p := range parts {
		if !p.Ref {
			continue
		}
		used[p.Text] = true
		if _, ok := s.Refs[p.Text]; !ok {
			return NewFieldError(tf, "uses ${%s}, which has no entry in refs", p.Text)
		}
	}
	if len(s.Refs) == 0 {
		return NewFieldError(field+".refs", "a template needs at least one reference: send a plain value instead")
	}
	names := slices.Sorted(maps.Keys(s.Refs))
	for _, name := range names {
		rf := field + ".refs." + name
		if !templateRefNameRE.MatchString(name) {
			return NewFieldError(rf, "the name must be letters, digits and \"_\", not starting with a digit, at most 64 characters")
		}
		if !used[name] {
			return NewFieldError(rf, "is not used by the template")
		}
		if err := validateSecretRefValue(rf, s.Refs[name]); err != nil {
			return err
		}
	}
	return nil
}
