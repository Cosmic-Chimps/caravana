package templates

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Conditions (catalog format 2): an input, an overlay item or a line of the
// Compose file may be there only when another input has a given answer.

// CondValue is what a condition asks of one input: it is not empty (Any), or
// it is one of Values. In JSON: true, "value" or ["a", "b"].
type CondValue struct {
	Any    bool
	Values []string
}

// MarshalJSON writes true, one value as a string, or a list.
func (c CondValue) MarshalJSON() ([]byte, error) {
	switch {
	case c.Any:
		return []byte("true"), nil
	case len(c.Values) == 1:
		return json.Marshal(c.Values[0])
	}
	return json.Marshal(c.Values)
}

// UnmarshalJSON reads true, a string or a list of strings.
func (c *CondValue) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch {
	case string(data) == "true":
		*c = CondValue{Any: true}
		return nil
	case len(data) > 0 && data[0] == '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*c = CondValue{Values: []string{s}}
		return nil
	case len(data) > 0 && data[0] == '[':
		var list []string
		if err := json.Unmarshal(data, &list); err != nil {
			return err
		}
		*c = CondValue{Values: list}
		return nil
	}
	return errors.New("a condition is true, a value or a list of values")
}

// Condition maps input names to what each must be. Every clause must hold.
// nil means no condition. An input that isn't asked, or that is left empty,
// has the empty answer, which no list of values holds.
type Condition map[string]CondValue

// Holds says every clause of c is true for values (the checked answers).
func (c Condition) Holds(values map[string]string) bool {
	for name, cv := range c {
		v := values[name]
		switch {
		case v == "":
			return false
		case cv.Any:
		case !slices.Contains(cv.Values, v):
			return false
		}
	}
	return true
}

// names lists the inputs c asks about, in order.
func (c Condition) names() []string {
	out := make([]string, 0, len(c))
	for n := range c {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Choice is one answer of a choice input.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ---- the marker comments of the Compose file ----

// A line of the Compose file may end with a marker comment:
//
//	GITHUB_OAUTH_CLIENT_ID: "{{github_client_id}}" # when signin=github
//	S3_REGION: "{{s3_region}}" # when s3_region
//	BITBUCKET_X: "y" # when signin=bitbucket,gitlab
//
// The line is kept, without the marker, when the condition holds and dropped
// otherwise. Clauses are separated by spaces and all must hold.
var (
	clauseRE = regexp.MustCompile(`^([a-z][a-z0-9_]*)(?:=([a-z0-9_-]+(?:,[a-z0-9_-]+)*))?$`)
	// markerRE finds a trailing marker comment: an entry, whitespace, "# when", clauses.
	markerRE = regexp.MustCompile(`^(.*?\S)[ \t]+#[ \t]*when[ \t]+(\S.*?)[ \t\r]*$`)
	// loneMarkerRE finds a whole-line comment written like a marker.
	loneMarkerRE = regexp.MustCompile(`^[ \t]*#[ \t]*when[ \t]+([a-z][a-z0-9_]*(?:=[a-z0-9_,-]+)?(?:[ \t]+[a-z][a-z0-9_]*(?:=[a-z0-9_,-]+)?)*)[ \t\r]*$`)
)

// parseClauses reads "signin=github,gitlab s3_region" into a condition.
func parseClauses(text string) (Condition, error) {
	c := Condition{}
	for _, word := range strings.Fields(text) {
		m := clauseRE.FindStringSubmatch(word)
		if m == nil {
			return nil, fmt.Errorf("%q isn't a condition: write name=value, name=a,b or just name", word)
		}
		if _, dup := c[m[1]]; dup {
			return nil, fmt.Errorf("%q is named twice", m[1])
		}
		if m[2] == "" {
			c[m[1]] = CondValue{Any: true}
		} else {
			c[m[1]] = CondValue{Values: strings.Split(m[2], ",")}
		}
	}
	if len(c) == 0 {
		return nil, errors.New("a marker needs a condition")
	}
	return c, nil
}

// markerLine is one marked line of the Compose file.
type markerLine struct {
	Line  int    // 0-based index in the file's lines
	Entry string // the line without its marker
	Cond  Condition
}

// MarkerError is a marker that can't stand where it is.
type MarkerError struct {
	Line    int // 1-based
	Message string
}

func (e *MarkerError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Message) }

// scanMarkers finds the marker comments of a Compose file. A comment that
// starts like a marker but isn't one, or a marker anywhere but after a
// single-line mapping entry or list item, is a *MarkerError.
func scanMarkers(compose string) ([]markerLine, error) {
	var out []markerLine
	for i, line := range strings.Split(compose, "\n") {
		if m := loneMarkerRE.FindStringSubmatch(line); m != nil {
			return nil, &MarkerError{Line: i + 1, Message: "a marker comment belongs at the end of the line it is about, not on a line of its own"}
		}
		m := markerRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		entry, rest := m[1], m[2]
		// The "#" must start a comment: outside quotes. entry is what comes before it.
		if inQuote(entry) {
			return nil, &MarkerError{Line: i + 1, Message: "the marker is inside a quoted string"}
		}
		cond, err := parseClauses(rest)
		if err != nil {
			return nil, &MarkerError{Line: i + 1, Message: err.Error()}
		}
		if err := singleLineEntry(entry); err != nil {
			return nil, &MarkerError{Line: i + 1, Message: err.Error()}
		}
		out = append(out, markerLine{Line: i, Entry: entry, Cond: cond})
	}
	return out, nil
}

// inQuote says the text ends inside a quoted YAML scalar.
func inQuote(s string) bool {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case quote == '\'':
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					quote = 0
				}
			}
		case c == '"' || c == '\'':
			// A quote opens only at the start of a token.
			if i == 0 || strings.IndexByte(" \t[{,", s[i-1]) >= 0 {
				quote = c
			}
		}
	}
	return quote != 0
}

// singleLineEntry says entry (a line without its comment) is a mapping entry
// with a value on the same line, or a list item that is one scalar: the only
// shapes whose removal leaves the rest of the file as it was.
func singleLineEntry(entry string) error {
	t := strings.TrimSpace(entry)
	if rest, ok := strings.CutPrefix(t, "-"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
		item := strings.TrimSpace(rest)
		if item == "" {
			return errors.New("a marker follows an entry; this list item is empty")
		}
		if looksLikeKey(item) {
			return errors.New("a marker can't follow the first key of a list item that is a mapping: mark each of its keys instead, or use a plain list item")
		}
		return balanced(item)
	}
	key, value, ok := cutKey(t)
	if !ok {
		return errors.New("a marker follows a single-line mapping entry (key: value) or a list item (- value)")
	}
	_ = key
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return errors.New("a marker can't follow a key that has a block under it: mark the lines of the block instead")
	case strings.HasPrefix(value, "|"), strings.HasPrefix(value, ">"), strings.HasPrefix(value, "&"):
		return errors.New("a marker can't follow a block scalar or an anchor")
	}
	return balanced(value)
}

// cutKey splits "key: value" at the first ": " (or a ":" that ends the line)
// that is outside quotes.
func cutKey(t string) (key, value string, ok bool) {
	var quote byte
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case (c == '"' || c == '\'') && i == 0:
			quote = c
		case c == ':' && (i+1 == len(t) || t[i+1] == ' ' || t[i+1] == '\t'):
			if i == 0 {
				return "", "", false
			}
			return t[:i], t[i+1:], true
		}
	}
	return "", "", false
}

// looksLikeKey says an unquoted list item is "key: value".
func looksLikeKey(item string) bool {
	if item[0] == '"' || item[0] == '\'' || item[0] == '[' || item[0] == '{' {
		return false
	}
	_, _, ok := cutKey(item)
	return ok
}

// balanced says the brackets of a flow value close on the same line.
func balanced(v string) error {
	if v == "" || (v[0] != '[' && v[0] != '{') {
		return nil
	}
	depth := 0
	var quote byte
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		}
	}
	if depth != 0 || quote != 0 {
		return errors.New("a marker can't follow a value that goes on over several lines")
	}
	return nil
}

// applyMarkers removes the marker comments of a Compose file: a line whose
// condition holds is kept without its marker, one whose condition doesn't is
// dropped.
func applyMarkers(compose string, values map[string]string) (string, error) {
	marks, err := scanMarkers(compose)
	if err != nil {
		return "", err
	}
	if len(marks) == 0 {
		return compose, nil
	}
	lines := strings.Split(compose, "\n")
	drop := map[int]bool{}
	for _, m := range marks {
		if m.Cond.Holds(values) {
			cr := ""
			if strings.HasSuffix(lines[m.Line], "\r") {
				cr = "\r"
			}
			lines[m.Line] = m.Entry + cr
		} else {
			drop[m.Line] = true
		}
	}
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if !drop[i] {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n"), nil
}
