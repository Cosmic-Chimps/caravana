package logline

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLineIDRoundTrip(t *testing.T) {
	for _, id := range []LineID{
		{Copy: "0a1b2c3d", Stream: Stdout, File: 0, Offset: 0},
		{Copy: "0a1b2c3d", Stream: Stderr, File: 3, Offset: 123456},
		{Copy: "0a1b2c3d-1234-5678-9abc-def012345678", Stream: Stdout, File: 12, Offset: 9223372036854775807},
		{Copy: "deadbeef", Stream: Stderr, File: 2147483647, Offset: 1},
	} {
		s := id.String()
		got, err := ParseLineID(s)
		if err != nil {
			t.Errorf("ParseLineID(%q): %v", s, err)
			continue
		}
		if got != id {
			t.Errorf("ParseLineID(%q) = %+v, want %+v", s, got, id)
		}
	}
	if got := (LineID{Copy: "0a1b2c3d", Stream: Stderr, File: 3, Offset: 77}).String(); got != "0a1b2c3d.stderr.3.77" {
		t.Errorf("String = %q", got)
	}
}

func TestParseLineIDRefusals(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"empty", ""},
		{"three parts", "0a1b2c3d.stdout.0"},
		{"five parts", "0a1b2c3d.stdout.0.1.2"},
		{"short copy", "0a1b2c.stdout.0.1"},
		{"copy not hex", "0a1b2c3z.stdout.0.1"},
		{"copy in capitals", "0A1B2C3D.stdout.0.1"},
		{"copy of the wrong length", "0a1b2c3d4.stdout.0.1"},
		{"full copy without dashes", "0a1b2c3d123456789abcdef012345678abcd.stdout.0.1"},
		{"empty copy", ".stdout.0.1"},
		{"unknown stream", "0a1b2c3d.stdin.0.1"},
		{"both streams", "0a1b2c3d.both.0.1"},
		{"empty stream", "0a1b2c3d..0.1"},
		{"negative file", "0a1b2c3d.stdout.-1.1"},
		{"signed file", "0a1b2c3d.stdout.+1.1"},
		{"file not a number", "0a1b2c3d.stdout.x.1"},
		{"file with a leading zero", "0a1b2c3d.stdout.01.1"},
		{"file too big", "0a1b2c3d.stdout.2147483648.1"},
		{"empty file", "0a1b2c3d.stdout..1"},
		{"negative offset", "0a1b2c3d.stdout.0.-5"},
		{"offset with a fraction", "0a1b2c3d.stdout.0.1.5"},
		{"offset too big", "0a1b2c3d.stdout.0.9223372036854775808"},
		{"offset with a leading zero", "0a1b2c3d.stdout.0.007"},
		{"empty offset", "0a1b2c3d.stdout.0."},
		{"spaces", "0a1b2c3d.stdout.0. 1"},
		{"path-like", "../../etc.stdout.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ParseLineID(tc.in); err == nil {
				t.Errorf("ParseLineID(%q) = %+v, want an error", tc.in, got)
			} else if !errors.Is(err, ErrBadLineID) {
				t.Errorf("error %v is not ErrBadLineID", err)
			}
		})
	}
}

// TestParseEstimatedTimes covers the lines that carry no usable time.
func TestParseEstimatedTimes(t *testing.T) {
	start := ts(t, "2026-10-25T00:00:00Z")
	b := Bounds{NotBefore: start.Add(-Slack), NotAfter: ts(t, "2026-10-25T01:00:00Z")}
	id := LineID{Copy: copyA, Stream: Stdout}

	first := Parse("no time at all", Stdout, id, b, nil)
	if first.TimeSource != Estimated || !first.Time.Equal(start) {
		t.Errorf("first untimed line = %s %q, want the copy's start, estimated", first.Time, first.TimeSource)
	}

	timed := Parse("2026-10-25T00:30:00Z started", Stdout, id, b, &first)
	if timed.TimeSource != FromLine || !timed.Time.Equal(ts(t, "2026-10-25T00:30:00Z")) {
		t.Errorf("timed line = %s %q", timed.Time, timed.TimeSource)
	}

	next := Parse("  and then it went on", Stdout, id, b, &timed)
	if next.TimeSource != Estimated || !next.Time.Equal(timed.Time) {
		t.Errorf("untimed after timed = %s %q, want the line before's time, estimated", next.Time, next.TimeSource)
	}
	again := Parse("one more", Stdout, id, b, &next)
	if again.TimeSource != Estimated || !again.Time.Equal(timed.Time) {
		t.Errorf("a second untimed line = %s %q, want the same time carried", again.Time, again.TimeSource)
	}

	// A time an hour before the copy started is not trusted.
	old := Parse("2026-10-24T23:00:00Z replayed", Stdout, id, b, &timed)
	if old.TimeSource != Estimated || !old.Time.Equal(timed.Time) {
		t.Errorf("out of bounds = %s %q, want the line before's time, estimated", old.Time, old.TimeSource)
	}
	if got := TextWithoutTime(old); got != "2026-10-24T23:00:00Z replayed" {
		t.Errorf("an untrusted time was cut out: %q", got)
	}

	// With no bounds and nothing before it, there is nothing to carry.
	bare := Parse("hello", Stdout, id, Bounds{}, nil)
	if bare.TimeSource != Estimated || !bare.Time.IsZero() {
		t.Errorf("no bounds, no previous = %s %q", bare.Time, bare.TimeSource)
	}
}

func TestParseKeepsIDAndStream(t *testing.T) {
	id := LineID{Copy: copyB, Stream: Stderr, File: 2, Offset: 4096}
	l := Parse("\x1b[31m2026/10/25 00:16:42 Starting up\x1b[0m", Stderr, id, wide, nil)
	if l.ID != id || l.Stream != Stderr {
		t.Errorf("ID, stream = %v, %q", l.ID, l.Stream)
	}
	if l.Text != "2026/10/25 00:16:42 Starting up" {
		t.Errorf("Text = %q, want the stripped line with its time", l.Text)
	}
	if l.Raw != "" {
		t.Errorf("Raw = %q, want none for a plain line", l.Raw)
	}
}

func TestParseJSONKeepsRaw(t *testing.T) {
	raw := `{"level":"warn","time":"2026-10-25T00:33:21Z","msg":"slow \"query\""}`
	l := Parse("\x1b[0m"+raw, Stdout, LineID{Copy: copyA, Stream: Stdout}, wide, nil)
	if l.Level != Warn || l.Text != `slow "query"` || l.Raw != raw {
		t.Errorf("Level, Text, Raw = %q, %q, %q", l.Level, l.Text, l.Raw)
	}
	if l.TimeSource != FromLine || !l.Time.Equal(ts(t, "2026-10-25T00:33:21Z")) {
		t.Errorf("time = %s %q", l.Time, l.TimeSource)
	}
}

func TestParseTimeIsUTC(t *testing.T) {
	l := Parse("2026-10-25T02:26:49+02:00 x", Stdout, LineID{}, wide, nil)
	if l.Time.Location() != time.UTC {
		t.Errorf("location = %v", l.Time.Location())
	}
}

// TestNoNetworkImports keeps the package pure: it may not import net/http,
// Nomad, or Caravana's service and deploy packages (or any other Caravana
// package at all).
func TestNoNetworkImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{
		"net/http",
		"net",
		"os/exec",
		"github.com/hashicorp/nomad",
		"github.com/cosmic-chimps/caravana/",
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if path == bad || ((strings.HasSuffix(bad, "/") || strings.Contains(bad, ".")) && strings.HasPrefix(path, bad)) {
					t.Errorf("%s imports %q, which a pure package must not", name, path)
				}
			}
		}
	}
	if checked < 5 {
		t.Errorf("checked %d files, want at least 5", checked)
	}
}
