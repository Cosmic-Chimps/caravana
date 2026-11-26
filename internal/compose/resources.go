package compose

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

// Resources is what SetResources writes into a service of a Compose file. A nil field leaves the
// file's value as it is.
type Resources struct {
	// Replicas is the service's copies: deploy.replicas, or scale when the file says it that way.
	Replicas *int
	// MemReservationMB and MemLimitMB are the memory the service reserves and may burst to, in MB:
	// mem_reservation and mem_limit, or deploy.resources.reservations.memory and
	// deploy.resources.limits.memory when the file says them there.
	MemReservationMB *int
	MemLimitMB       *int
}

// EditError says why a value can't be written into the Compose file without guessing: Path is the
// value's place in the file ("services.web.mem_limit"), Reason the sentence.
type EditError struct {
	Path, Reason string
}

func (e *EditError) Error() string { return e.Path + ": " + e.Reason }

// SetResources returns text with service's copies and memory set to r, and every other byte of
// text as it was: comments, blank lines, quoting and key order stay. A value the file has is
// rewritten in place, in its quoting; one it doesn't have is added as the first key of the
// service's mapping (or of its deploy mapping, for replicas when the service has one), indented as
// its siblings are. Each value goes where Translate reads it from first: deploy.replicas before
// scale, deploy.resources.*.memory before mem_reservation and mem_limit.
//
// It refuses, with an *EditError naming the place, what it can't write without guessing: a service
// or a mapping on the way that is an alias, has an anchor or a merge key (<<), a value that is an
// alias, has an anchor or a tag, spans lines or holds a variable, a key to add into a flow mapping
// ({...}) or an empty one, and indentation that isn't spaces. Pure: no I/O.
func SetResources(text, service string, r Resources) (string, error) {
	type want struct {
		field fieldKind
		value int
	}
	var wants []want
	if r.Replicas != nil {
		wants = append(wants, want{fieldReplicas, *r.Replicas})
	}
	if r.MemReservationMB != nil {
		wants = append(wants, want{fieldReservation, *r.MemReservationMB})
	}
	if r.MemLimitMB != nil {
		wants = append(wants, want{fieldLimit, *r.MemLimitMB})
	}
	for _, w := range wants {
		if w.value < 1 {
			return "", &EditError{Path: "services." + service + "." + w.field.key(), Reason: "must be 1 or more"}
		}
	}

	// One value at a time, reading the file again after each: an added key moves the lines after it.
	for _, w := range wants {
		services, err := parseServices(text)
		if err != nil {
			return "", err
		}
		next, err := setOne(text, services, service, w.field, w.field.text(w.value))
		if err != nil {
			return "", err
		}
		text = next
	}

	// The guard: the file must now read back with each value where Translate looks for it.
	services, err := parseServices(text)
	if err != nil {
		return "", fmt.Errorf("the edited Compose file doesn't read: %w", err)
	}
	for _, w := range wants {
		place, err := placeOf(services, service, w.field)
		if err != nil {
			return "", err
		}
		if place.node == nil || place.node.Value != w.field.text(w.value) {
			return "", &EditError{Path: place.path, Reason: "the edited file doesn't hold the new value"}
		}
	}
	return text, nil
}

// CheckResources says whether SetResources can write service's copies (when replicas) and memory
// into text: nil when it can, else the first *EditError. The Services page asks it before it draws
// those inputs for an app that keeps its file.
func CheckResources(text, service string, replicas bool) error {
	r := Resources{MemReservationMB: new(1), MemLimitMB: new(1)}
	if replicas {
		r.Replicas = new(1)
	}
	_, err := SetResources(text, service, r)
	return err
}

// fieldKind is one of the values SetResources writes.
type fieldKind int

const (
	fieldReplicas fieldKind = iota
	fieldReservation
	fieldLimit
)

// key is the service-level key the value is added as when the file doesn't have it.
func (f fieldKind) key() string {
	switch f {
	case fieldReplicas:
		return "deploy.replicas"
	case fieldReservation:
		return "mem_reservation"
	}
	return "mem_limit"
}

// text is the value as it is written: a count, or megabytes with Compose's "m" (MiB, which
// Translate reads back as the same number of MB).
func (f fieldKind) text(n int) string {
	if f == fieldReplicas {
		return strconv.Itoa(n)
	}
	return strconv.Itoa(n) + "m"
}

// place is where a value is in a service: node and path when the file has it; else the mapping to
// add it to (into, with its key node) and the lines to add (one per line, indentation relative to
// into's keys).
type place struct {
	node *yaml.Node
	path string

	into, intoKey *yaml.Node
	add           []string
}

// setOne writes one value into text and returns the new text.
func setOne(text string, services *yaml.Node, service string, f fieldKind, value string) (string, error) {
	p, err := placeOf(services, service, f)
	if err != nil {
		return "", err
	}
	lines := strings.Split(text, "\n")
	if p.node != nil {
		if err := editableScalar(p.node, "the value"); err != nil {
			return "", &EditError{Path: p.path, Reason: err.Error()}
		}
		if p.node.Value == value {
			return text, nil
		}
		edit, err := scalarEditFor(lines, p.node, value)
		if err != nil {
			return "", &EditError{Path: p.path, Reason: err.Error()}
		}
		runes := []rune(lines[edit.line])
		lines[edit.line] = string(runes[:edit.start]) + edit.text + string(runes[edit.end:])
		return strings.Join(lines, "\n"), nil
	}

	indent, step, err := childIndent(lines, p.intoKey, p.into)
	if err != nil {
		return "", &EditError{Path: p.path, Reason: err.Error()}
	}
	eol := ""
	if strings.Contains(text, "\r\n") {
		eol = "\r"
	}
	added := make([]string, len(p.add))
	for i, l := range p.add {
		// p.add's lines start with one tab per level below into's keys; the last one takes the value.
		key := strings.TrimLeft(l, "\t")
		added[i] = indent + strings.Repeat(step, len(l)-len(key)) + key
		if i == len(p.add)-1 {
			added[i] += " " + value
		}
		added[i] += eol
	}
	at := p.intoKey.Line // right after the key's line: the mapping's first key
	out := make([]string, 0, len(lines)+len(added))
	out = append(out, lines[:at]...)
	out = append(out, added...)
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n"), nil
}

// placeOf finds where f is in service, or where to add it.
func placeOf(services *yaml.Node, service string, f fieldKind) (place, error) {
	base := "services." + service
	svcKey, svc := mappingPair(services, service)
	if svc == nil {
		return place{}, &EditError{Path: base, Reason: "the service is not in the file"}
	}
	if err := plainMapping(svc, "the service"); err != nil {
		return place{}, &EditError{Path: base, Reason: err.Error()}
	}

	// child is the mapping under key of m, nil when m has no such key.
	child := func(m *yaml.Node, key, path string) (*yaml.Node, *yaml.Node, error) {
		k, v := mappingPair(m, key)
		if v == nil {
			return nil, nil, nil
		}
		if err := plainMapping(v, "it"); err != nil {
			return nil, nil, &EditError{Path: path, Reason: err.Error()}
		}
		return k, v, nil
	}

	deployKey, deploy, err := child(svc, "deploy", base+".deploy")
	if err != nil {
		return place{}, err
	}

	if f == fieldReplicas {
		if deploy != nil {
			if n := mappingValue(deploy, "replicas"); n != nil {
				return place{node: n, path: base + ".deploy.replicas"}, nil
			}
		}
		if n := mappingValue(svc, "scale"); n != nil {
			return place{node: n, path: base + ".scale"}, nil
		}
		if deploy != nil {
			return place{path: base + ".deploy.replicas", into: deploy, intoKey: deployKey, add: []string{"replicas:"}}, nil
		}
		return place{path: base + ".deploy.replicas", into: svc, intoKey: svcKey, add: []string{"deploy:", "\treplicas:"}}, nil
	}

	group, key := "reservations", "mem_reservation"
	if f == fieldLimit {
		group, key = "limits", "mem_limit"
	}
	if deploy != nil {
		_, resources, err := child(deploy, "resources", base+".deploy.resources")
		if err != nil {
			return place{}, err
		}
		if resources != nil {
			_, g, err := child(resources, group, base+".deploy.resources."+group)
			if err != nil {
				return place{}, err
			}
			if g != nil {
				if n := mappingValue(g, "memory"); n != nil {
					return place{node: n, path: base + ".deploy.resources." + group + ".memory"}, nil
				}
			}
		}
	}
	if n := mappingValue(svc, key); n != nil {
		return place{node: n, path: base + "." + key}, nil
	}
	return place{path: base + "." + key, into: svc, intoKey: svcKey, add: []string{key + ":"}}, nil
}

// mappingPair is the key and value nodes of key in a mapping node, nils when there is none.
func mappingPair(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return k, m.Content[i+1]
		}
	}
	return nil, nil
}

// plainMapping says why m can't be edited as a mapping of its own: an alias, an anchor (other
// places would change too), a merge key (its values come from elsewhere) or not a mapping at all.
func plainMapping(m *yaml.Node, what string) error {
	switch {
	case m.Kind == yaml.AliasNode:
		return errors.New(what + " is an alias")
	case m.Kind != yaml.MappingNode:
		return errors.New(what + " is not a mapping")
	case m.Anchor != "":
		return errors.New(what + " has an anchor")
	}
	if k, _ := mappingPair(m, "<<"); k != nil {
		return errors.New(what + " has a merge key (<<)")
	}
	return nil
}

// childIndent is the indentation of m's keys, and one level's step (their indentation beyond the
// key that holds m), for adding a key as m's first. m must be a block mapping with a key, starting
// on a line after its key, indented with spaces.
func childIndent(lines []string, key, m *yaml.Node) (indent, step string, err error) {
	if m.Style&yaml.FlowStyle != 0 {
		return "", "", errors.New("the mapping is written in flow style ({...}), so a key can't be added to it safely")
	}
	if len(m.Content) == 0 {
		return "", "", errors.New("the mapping is empty, so its indentation is unknown")
	}
	first := m.Content[0]
	if first.Line <= key.Line || first.Column <= key.Column || first.Line > len(lines) {
		return "", "", errors.New("the mapping's keys aren't on their own lines below it")
	}
	runes := []rune(lines[first.Line-1])
	if first.Column-1 > len(runes) {
		return "", "", errors.New("the mapping's place in the file is unknown")
	}
	indent = string(runes[:first.Column-1])
	if strings.Trim(indent, " ") != "" {
		return "", "", errors.New("the mapping is not indented with spaces")
	}
	return indent, strings.Repeat(" ", first.Column-key.Column), nil
}

// scalarEditFor finds node's scalar on its line and says what to write in its place: value in the
// node's quoting, plain when it was plain (the values written here are counts and sizes, which
// read back as what Translate wants plain).
func scalarEditFor(lines []string, node *yaml.Node, value string) (imageEdit, error) {
	if node.Line < 1 || node.Line > len(lines) || node.Column < 1 {
		return imageEdit{}, errors.New("the value's place in the file is unknown")
	}
	runes := []rune(lines[node.Line-1])
	start := node.Column - 1
	if start >= len(runes) {
		return imageEdit{}, errors.New("the value's place in the file is unknown")
	}
	end, err := tokenEnd(runes, start, node)
	if err != nil {
		return imageEdit{}, err
	}
	edit := imageEdit{line: node.Line - 1, start: start, end: end, text: value}
	switch {
	case node.Style&yaml.SingleQuotedStyle != 0:
		edit.text = "'" + value + "'"
	case node.Style&yaml.DoubleQuotedStyle != 0:
		edit.text = strconv.Quote(value)
	}
	return edit, nil
}
