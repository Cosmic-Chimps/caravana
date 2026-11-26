package compose

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

// plainImage is what a new image may look like to be written as a plain YAML
// scalar. Anything else is double-quoted.
var plainImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]*$`)

// imageEdit is one scalar to replace: its place in the text (line index from 0, rune
// offsets) and the text to put there.
type imageEdit struct {
	line       int
	start, end int // rune offsets in the line, end exclusive
	text       string
}

// ReplaceImages returns text with the `image:` of each named service set to the new
// value, and every other byte of text exactly as it was: comments, quoting elsewhere,
// blank lines and key order stay. images maps a service name to its new image.
//
// It is for an images-only deploy of an app that keeps its Compose file: the file is
// rewritten in place instead of being dropped. It refuses what it can't rewrite
// without guessing, with an error naming the service ("services.web.image: ..."): a
// service or its image missing, an image behind an alias or an anchor, an image on
// several lines, an image that holds a `$` (a variable can't be rewritten), or text
// that isn't one YAML document. An image whose value is already the new one is left
// as it is. Pure: no I/O.
func ReplaceImages(text string, images map[string]string) (string, error) {
	services, err := parseServices(text)
	if err != nil {
		return "", err
	}

	lines := strings.Split(text, "\n")
	names := make([]string, 0, len(images))
	for name := range images {
		names = append(names, name)
	}
	sort.Strings(names)

	var edits []imageEdit
	for _, name := range names {
		next := images[name]
		node, err := imageNode(services, name)
		if err != nil {
			return "", fmt.Errorf("services.%s.image: %w", name, err)
		}
		if node.Value == next {
			continue
		}
		edit, err := imageEditFor(lines, node, next)
		if err != nil {
			return "", fmt.Errorf("services.%s.image: %w", name, err)
		}
		edits = append(edits, edit)
	}

	// Last place first, so an edit never moves the offsets of one still to make.
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].line != edits[j].line {
			return edits[i].line > edits[j].line
		}
		return edits[i].start > edits[j].start
	})
	for _, e := range edits {
		runes := []rune(lines[e.line])
		lines[e.line] = string(runes[:e.start]) + e.text + string(runes[e.end:])
	}
	return strings.Join(lines, "\n"), nil
}

// parseServices reads text as one YAML document and returns its services mapping.
func parseServices(text string) (*yaml.Node, error) {
	dec := yaml.NewDecoder(strings.NewReader(text))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("read the Compose file: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("the Compose file has more than one YAML document")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("the Compose file is not a mapping")
	}
	services := mappingValue(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, errors.New("services: not found as a mapping")
	}
	return services, nil
}

// mappingValue is the value of key in a mapping node, nil when there is none.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// imageNode is the scalar node of services.<name>.image, or the reason there is none to
// rewrite.
func imageNode(services *yaml.Node, name string) (*yaml.Node, error) {
	svc := mappingValue(services, name)
	if svc == nil {
		return nil, errors.New("the service is not in the file")
	}
	if svc.Kind != yaml.MappingNode {
		return nil, errors.New("the service is not a plain mapping (an alias?)")
	}
	node := mappingValue(svc, "image")
	if node == nil {
		return nil, errors.New("the service has no image key in its own mapping")
	}
	if err := editableScalar(node, "the image"); err != nil {
		return nil, err
	}
	return node, nil
}

// editableScalar says why node, a value the file holds, can't be rewritten in place, or nil
// when it can: one plain or quoted scalar on one line, with no alias, anchor, tag or variable.
// what names it in the reason ("the image").
func editableScalar(node *yaml.Node, what string) error {
	switch {
	case node.Kind == yaml.AliasNode:
		return errors.New(what + " is an alias")
	case node.Kind != yaml.ScalarNode:
		return errors.New(what + " is not a scalar")
	case node.Anchor != "":
		return errors.New(what + " has an anchor")
	case node.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return errors.New(what + " is a multi-line block scalar")
	case node.Style&yaml.TaggedStyle != 0:
		return errors.New(what + " has a tag")
	case strings.Contains(node.Value, "$"):
		return errors.New(what + " holds a variable, which can't be rewritten")
	}
	return nil
}

// imageEditFor finds the exact characters of node's scalar on its line and says what to
// write in their place, in the same style.
func imageEditFor(lines []string, node *yaml.Node, next string) (imageEdit, error) {
	if next == "" || strings.ContainsAny(next, "\r\n\x00$") || strings.TrimSpace(next) != next {
		return imageEdit{}, errors.New("the new image is empty, has a line break or a variable")
	}
	if node.Line < 1 || node.Line > len(lines) || node.Column < 1 {
		return imageEdit{}, errors.New("the image's place in the file is unknown")
	}
	runes := []rune(lines[node.Line-1])
	start := node.Column - 1
	if start >= len(runes) {
		return imageEdit{}, errors.New("the image's place in the file is unknown")
	}
	end, err := tokenEnd(runes, start, node)
	if err != nil {
		return imageEdit{}, err
	}
	edit := imageEdit{line: node.Line - 1, start: start, end: end}
	switch {
	case node.Style&yaml.SingleQuotedStyle != 0:
		edit.text = "'" + strings.ReplaceAll(next, "'", "''") + "'"
	case node.Style&yaml.DoubleQuotedStyle != 0:
		edit.text = strconv.Quote(next)
	case isPlainString(next):
		edit.text = next
	default:
		edit.text = strconv.Quote(next)
	}
	return edit, nil
}

// tokenEnd is where the scalar that starts at runes[start] ends (exclusive), after
// checking that the text there decodes to node.Value in node's style: the guard that
// the position is right and nothing is guessed.
func tokenEnd(runes []rune, start int, node *yaml.Node) (int, error) {
	mismatch := errors.New("the value's text in the file is not what the parser read (an anchor, a tag or several lines?)")
	switch {
	case node.Style&yaml.DoubleQuotedStyle != 0:
		if runes[start] != '"' {
			return 0, mismatch
		}
		for i := start + 1; i < len(runes); i++ {
			switch runes[i] {
			case '\\':
				i++
			case '"':
				got, err := strconv.Unquote(string(runes[start : i+1]))
				if err != nil || got != node.Value {
					return 0, mismatch
				}
				return i + 1, nil
			}
		}
		return 0, errors.New("the value is not closed on its line")
	case node.Style&yaml.SingleQuotedStyle != 0:
		if runes[start] != '\'' {
			return 0, mismatch
		}
		for i := start + 1; i < len(runes); i++ {
			if runes[i] != '\'' {
				continue
			}
			if i+1 < len(runes) && runes[i+1] == '\'' {
				i++
				continue
			}
			got := strings.ReplaceAll(string(runes[start+1:i]), "''", "'")
			if got != node.Value {
				return 0, mismatch
			}
			return i + 1, nil
		}
		return 0, errors.New("the value is not closed on its line")
	default:
		value := []rune(node.Value)
		end := start + len(value)
		if end > len(runes) || string(runes[start:end]) != node.Value {
			return 0, mismatch
		}
		if end < len(runes) && !strings.ContainsRune(" \t\r,]}", runes[end]) {
			return 0, mismatch
		}
		return end, nil
	}
}

// isPlainString says whether s can be written as a plain YAML scalar and still read
// back as the same string (not a number, a boolean or null).
func isPlainString(s string) bool {
	if !plainImage.MatchString(s) {
		return false
	}
	var back any
	if err := yaml.Unmarshal([]byte(s), &back); err != nil {
		return false
	}
	str, ok := back.(string)
	return ok && str == s
}
