// Package metrics is the pure part of Monitoring (feature 025): it reads
// Traefik's Prometheus text, turns two scrapes into per-minute counts, and
// answers percentiles from stored bucket counts. It holds no state and knows
// nothing of Nomad, the service layer or the store. Only real.go touches the
// network.
package metrics

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Sample is one line of the text format: a metric name, its labels and its
// value. A timestamp, when the line has one, is checked and dropped.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// ParseError names the line (counted from 1, comments included) that could
// not be read.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("metrics text: line %d: %s", e.Line, e.Msg) }

// maxLine is the longest line ParseText reads. Traefik's longest label sets
// are a few hundred bytes.
const maxLine = 1 << 20

// ParseText reads the Prometheus text format (version 0.0.4) and returns the
// samples whose name is in names (every sample when names is empty). Lines of
// other metrics are skipped after one look at their name, so a big scrape
// costs little. # HELP, # TYPE and other comments are skipped. A value may be
// +Inf, -Inf or NaN. A malformed line of a wanted metric is a *ParseError.
func ParseText(r io.Reader, names ...string) ([]Sample, error) {
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	var out []Sample
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		end := strings.IndexAny(line, "{ \t")
		if end < 0 {
			if len(want) > 0 {
				if _, ok := want[line]; !ok {
					continue
				}
			}
			return nil, &ParseError{n, "no value"}
		}
		name := line[:end]
		if len(want) > 0 {
			if _, ok := want[name]; !ok {
				continue
			}
		}
		s, err := parseSample(name, line[end:])
		if err != nil {
			return nil, &ParseError{n, err.Error()}
		}
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, &ParseError{n + 1, "line too long"}
		}
		return nil, err
	}
	return out, nil
}

// parseSample reads what follows the name: optional {labels}, a value and an
// optional timestamp.
func parseSample(name, rest string) (Sample, error) {
	s := Sample{Name: name}
	if rest[0] == '{' {
		labels, tail, err := parseLabels(rest[1:])
		if err != nil {
			return Sample{}, err
		}
		s.Labels, rest = labels, tail
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return Sample{}, errors.New("no value")
	}
	if len(fields) > 2 {
		return Sample{}, errors.New("unexpected text after the value")
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return Sample{}, fmt.Errorf("bad value %q", fields[0])
	}
	s.Value = v
	if len(fields) == 2 {
		if _, err := strconv.ParseInt(fields[1], 10, 64); err != nil {
			return Sample{}, fmt.Errorf("bad timestamp %q", fields[1])
		}
	}
	return s, nil
}

// parseLabels reads `name="value",...}` (the opening brace already cut) and
// returns the labels and the text after the closing brace. The escapes are
// \\, \" and \n, as in the format.
func parseLabels(s string) (map[string]string, string, error) {
	labels := make(map[string]string, 6)
	i := 0
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == ',') {
			i++
		}
		if i >= len(s) {
			return nil, "", errors.New("labels not closed")
		}
		if s[i] == '}' {
			return labels, s[i+1:], nil
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq <= 0 {
			return nil, "", errors.New("label without =")
		}
		key := strings.TrimSpace(s[i : i+eq])
		if key == "" {
			return nil, "", errors.New("empty label name")
		}
		i += eq + 1
		if i >= len(s) || s[i] != '"' {
			return nil, "", fmt.Errorf("label %q has no quoted value", key)
		}
		i++
		var b strings.Builder
		closed := false
		for i < len(s) {
			c := s[i]
			if c == '"' {
				closed = true
				i++
				break
			}
			if c == '\\' {
				if i+1 >= len(s) {
					break
				}
				switch s[i+1] {
				case '\\':
					b.WriteByte('\\')
				case '"':
					b.WriteByte('"')
				case 'n':
					b.WriteByte('\n')
				default:
					return nil, "", fmt.Errorf("label %q: bad escape \\%c", key, s[i+1])
				}
				i += 2
				continue
			}
			b.WriteByte(c)
			i++
		}
		if !closed {
			return nil, "", fmt.Errorf("label %q: value not closed", key)
		}
		labels[key] = b.String()
	}
}
