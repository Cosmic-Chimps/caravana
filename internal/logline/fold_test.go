package logline

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	copyA = "0a1b2c3d"
	copyB = "4e5f6a7b"
)

// nginxHealth is the health check that fills nginx's log: every ten seconds,
// differing only in the Common Log Format bracket.
func nginxHealth(sec int) string {
	return fmt.Sprintf(`192.168.5.15 - - [25/Oct/2026:00:33:%02d +0000] "GET / HTTP/1.1" 200 896 "-" "Nomad/2.0.7 health check"`, sec)
}

func TestFoldNginxHealthChecks(t *testing.T) {
	var lines []Line
	for i, sec := range []int{1, 11, 21, 31} {
		id := LineID{Copy: copyA, Stream: Stdout, File: 0, Offset: int64(i * 100)}
		lines = append(lines, Parse(nginxHealth(sec), Stdout, id, wide, nil))
	}
	got := Fold(lines)
	if len(got) != 1 {
		t.Fatalf("Fold gave %d lines, want 1", len(got))
	}
	if got[0].Repeat != 3 {
		t.Errorf("Repeat = %d, want 3 (four lines, N-1)", got[0].Repeat)
	}
	if want := (LineID{Copy: copyA, Stream: Stdout, File: 0, Offset: 300}); got[0].ID != want {
		t.Errorf("ID = %v, want the newest line's, %v", got[0].ID, want)
	}
	if want := ts(t, "2026-10-25T00:33:31Z"); !got[0].Time.Equal(want) {
		t.Errorf("Time = %s, want the newest line's", got[0].Time.Format(time.RFC3339))
	}
	if !strings.Contains(got[0].Text, "00:33:31") {
		t.Errorf("Text = %q, want the newest line's own text", got[0].Text)
	}
}

func TestFoldKeepsDifferentLines(t *testing.T) {
	texts := []string{"A", "A", "B", "A", "A", "A", "C", "B"}
	var lines []Line
	for i, s := range texts {
		lines = append(lines, Parse(s, Stdout, LineID{Copy: copyA, Stream: Stdout, Offset: int64(i)}, wide, nil))
	}
	got := Fold(lines)
	want := []struct {
		text   string
		repeat int
		offset int64
	}{{"A", 1, 1}, {"B", 0, 2}, {"A", 2, 5}, {"C", 0, 6}, {"B", 0, 7}}
	if len(got) != len(want) {
		t.Fatalf("Fold gave %d lines, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Text != w.text || got[i].Repeat != w.repeat || got[i].ID.Offset != w.offset {
			t.Errorf("line %d = %q repeat %d offset %d, want %q repeat %d offset %d",
				i, got[i].Text, got[i].Repeat, got[i].ID.Offset, w.text, w.repeat, w.offset)
		}
	}
}

func TestFoldDoesNotCrossCopiesOrStreams(t *testing.T) {
	a := Parse(nginxHealth(1), Stdout, LineID{Copy: copyA, Stream: Stdout}, wide, nil)
	b := Parse(nginxHealth(2), Stdout, LineID{Copy: copyB, Stream: Stdout}, wide, nil)
	if got := Fold([]Line{a, b}); len(got) != 2 {
		t.Errorf("two copies' identical health checks folded into %d line(s), want 2", len(got))
	}
	c := Parse(nginxHealth(3), Stderr, LineID{Copy: copyA, Stream: Stderr}, wide, nil)
	if got := Fold([]Line{a, c}); len(got) != 2 {
		t.Errorf("two streams' identical lines folded into %d line(s), want 2", len(got))
	}
}

func TestFoldJSONLinesByMessage(t *testing.T) {
	var lines []Line
	for i, sec := range []string{"21", "31", "41"} {
		raw := `{"level":"info","time":"2026-10-25T00:33:` + sec + `Z","msg":"tick"}`
		lines = append(lines, Parse(raw, Stdout, LineID{Copy: copyA, Stream: Stdout, Offset: int64(i)}, wide, nil))
	}
	got := Fold(lines)
	if len(got) != 1 || got[0].Repeat != 2 {
		t.Fatalf("Fold = %+v, want one line with Repeat 2", got)
	}
	if !got[0].Time.Equal(ts(t, "2026-10-25T00:33:41Z")) {
		t.Errorf("Time = %s, want the newest", got[0].Time)
	}
}

func TestFoldOfFoldedAddsUp(t *testing.T) {
	l := Parse("x", Stdout, LineID{Copy: copyA, Stream: Stdout}, wide, nil)
	first := l
	first.Repeat = 4 // already five lines
	got := Fold([]Line{first, l})
	if len(got) != 1 || got[0].Repeat != 5 {
		t.Errorf("Fold = %+v, want Repeat 5", got)
	}
}

func TestFoldEdgesAndInputUntouched(t *testing.T) {
	if got := Fold(nil); len(got) != 0 {
		t.Errorf("Fold(nil) = %v", got)
	}
	one := []Line{Parse("only", Stdout, LineID{Copy: copyA, Stream: Stdout}, wide, nil)}
	if got := Fold(one); len(got) != 1 || got[0].Repeat != 0 {
		t.Errorf("Fold(one) = %+v", got)
	}
	in := []Line{
		Parse("A", Stdout, LineID{Copy: copyA, Stream: Stdout, Offset: 1}, wide, nil),
		Parse("A", Stdout, LineID{Copy: copyA, Stream: Stdout, Offset: 2}, wide, nil),
	}
	_ = Fold(in)
	if in[0].Repeat != 0 || in[1].Repeat != 0 || in[0].ID.Offset != 1 {
		t.Errorf("Fold changed its input: %+v", in)
	}
}

// TestFoldBlankLines: equal blank lines fold like any other text.
func TestFoldBlankLines(t *testing.T) {
	a := Parse("", Stdout, LineID{Copy: copyA, Stream: Stdout, Offset: 1}, wide, nil)
	b := Parse("", Stdout, LineID{Copy: copyA, Stream: Stdout, Offset: 2}, wide, nil)
	if got := Fold([]Line{a, b}); len(got) != 1 || got[0].Repeat != 1 {
		t.Errorf("Fold = %+v", got)
	}
}
