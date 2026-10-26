package metrics

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestParseText(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		names []string
		want  []Sample
	}{
		{
			name: "plain and labelled",
			in: `# HELP up Up.
# TYPE up gauge
up 1
x_total{a="b",c="d"} 42
`,
			want: []Sample{
				{Name: "up", Value: 1},
				{Name: "x_total", Labels: map[string]string{"a": "b", "c": "d"}, Value: 42},
			},
		},
		{
			name:  "only the named families",
			in:    "a 1\nb{x=\"y\"} 2\nc 3\n",
			names: []string{"b"},
			want:  []Sample{{Name: "b", Labels: map[string]string{"x": "y"}, Value: 2}},
		},
		{
			name: "escapes",
			in:   `m{a="q\"uote",b="back\\slash",c="new\nline"} 1` + "\n",
			want: []Sample{{Name: "m", Labels: map[string]string{"a": `q"uote`, "b": `back\slash`, "c": "new\nline"}, Value: 1}},
		},
		{
			name: "a brace and comma inside a value",
			in:   `m{a="x}y,z"} 5` + "\n",
			want: []Sample{{Name: "m", Labels: map[string]string{"a": "x}y,z"}, Value: 5}},
		},
		{
			name: "timestamp ignored",
			in:   "m 3 1700000000000\n",
			want: []Sample{{Name: "m", Value: 3}},
		},
		{
			name: "empty labels and trailing comma",
			in:   "m{} 1\nn{a=\"b\",} 2\n",
			want: []Sample{{Name: "m", Labels: map[string]string{}, Value: 1}, {Name: "n", Labels: map[string]string{"a": "b"}, Value: 2}},
		},
		{
			name: "exponent and blank lines and CRLF",
			in:   "\nm 1.5e3\r\n\r\nn 2\r\n",
			want: []Sample{{Name: "m", Value: 1500}, {Name: "n", Value: 2}},
		},
		{
			name: "le +Inf label",
			in:   `h_bucket{le="+Inf"} 7` + "\n",
			want: []Sample{{Name: "h_bucket", Labels: map[string]string{"le": "+Inf"}, Value: 7}},
		},
		{
			name: "no trailing newline",
			in:   "m 1",
			want: []Sample{{Name: "m", Value: 1}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseText(strings.NewReader(tt.in), tt.names...)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d samples, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range got {
				w, g := tt.want[i], got[i]
				if g.Name != w.Name || g.Value != w.Value || len(g.Labels) != len(w.Labels) {
					t.Fatalf("sample %d = %+v, want %+v", i, g, w)
				}
				for k, v := range w.Labels {
					if g.Labels[k] != v {
						t.Fatalf("sample %d label %s = %q, want %q", i, k, g.Labels[k], v)
					}
				}
			}
		})
	}
}

func TestParseTextSpecialValues(t *testing.T) {
	got, err := ParseText(strings.NewReader("a +Inf\nb -Inf\nc NaN\nd Inf\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsInf(got[0].Value, 1) || !math.IsInf(got[1].Value, -1) || !math.IsNaN(got[2].Value) || !math.IsInf(got[3].Value, 1) {
		t.Fatalf("values = %v %v %v %v", got[0].Value, got[1].Value, got[2].Value, got[3].Value)
	}
}

func TestParseTextErrorsNameTheLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		line int
	}{
		{"no value", "# c\nok 1\nbad\n", 3},
		{"bad value", "ok 1\n\nm abc\n", 3},
		{"bad timestamp", "m 1 yesterday\n", 1},
		{"unclosed labels", "ok 1\nm{a=\"b\" 1\n", 2},
		{"unclosed value", "m{a=\"b} 1\n", 1},
		{"no quotes", "m{a=b} 1\n", 1},
		{"label without equals", "m{a} 1\n", 1},
		{"bad escape", `m{a="\t"} 1` + "\n", 1},
		{"extra text", "m 1 2 3\n", 1},
		{"missing value after labels", "m{a=\"b\"}\n", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseText(strings.NewReader(tt.in))
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v, want a *ParseError", err)
			}
			if pe.Line != tt.line {
				t.Fatalf("line = %d, want %d (%v)", pe.Line, tt.line, err)
			}
			if !strings.Contains(err.Error(), "line ") {
				t.Fatalf("error doesn't name the line: %v", err)
			}
		})
	}
}

func TestParseTextSkipsUnwantedMalformedLines(t *testing.T) {
	// A line of a family nobody asked for is never looked at.
	got, err := ParseText(strings.NewReader("junk{{{ nonsense\nwanted 1\n"), "wanted")
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestParseTextLineTooLong(t *testing.T) {
	in := "m{a=\"" + strings.Repeat("x", maxLine) + "\"} 1\n"
	_, err := ParseText(strings.NewReader(in))
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v", err)
	}
}
