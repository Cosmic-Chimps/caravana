package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseTemplate(t *testing.T) {
	lit := func(s string) TemplatePart { return TemplatePart{Text: s} }
	ref := func(s string) TemplatePart { return TemplatePart{Text: s, Ref: true} }
	tests := []struct {
		name string
		in   string
		want []TemplatePart
	}{
		{"text only", "Host=postgres", []TemplatePart{lit("Host=postgres")}},
		{"one ref", "${pw}", []TemplatePart{ref("pw")}},
		{"ref in text", "Host=db;Password=${pw};Port=1", []TemplatePart{lit("Host=db;Password="), ref("pw"), lit(";Port=1")}},
		{"dollar dollar", "a$$b", []TemplatePart{lit("a$b")}},
		{"dollar dollar then ref", "$$${x}", []TemplatePart{lit("$"), ref("x")}},
		{"escaped placeholder", "$${x}", []TemplatePart{lit("${x}")}},
		{"adjacent refs", "${a}${b}", []TemplatePart{ref("a"), ref("b")}},
		{"same ref twice", "${a}-${a}", []TemplatePart{ref("a"), lit("-"), ref("a")}},
		{"underscore and digits", "${_a1}", []TemplatePart{ref("_a1")}},
		{"only dollars", "$$$$", []TemplatePart{lit("$$")}},
		{"utf8", "é${x}ñ", []TemplatePart{lit("é"), ref("x"), lit("ñ")}},
		{"braces without dollar", "{x} }", []TemplatePart{lit("{x} }")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTemplate(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseTemplateErrors(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		offset int
		has    string
	}{
		{"lone dollar at the end", "abc$", 3, "lone"},
		{"dollar then letter", "ab$c", 2, `"$" must be followed`},
		{"dollar then space", "$ x", 0, `"$" must be followed`},
		{"open without close", "a${pw", 1, "closing"},
		{"empty name", "x${}", 1, "must be letters"},
		{"name starts with digit", "${1a}", 0, "must be letters"},
		{"name with a dash", "${a-b}", 0, "must be letters"},
		{"name with a space", "${a b}", 0, "must be letters"},
		{"name too long", "${" + strings.Repeat("a", 65) + "}", 0, "at most 64"},
		{"nested", "${a${b}}", 0, "must be letters"},
		{"error after good text", "$$ok ${x} $", 10, "lone"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseTemplate(tt.in)
			te, ok := err.(*TemplateError)
			if !ok {
				t.Fatalf("err = %v, want *TemplateError", err)
			}
			if te.Offset != tt.offset {
				t.Errorf("offset = %d, want %d (%v)", te.Offset, tt.offset, te)
			}
			if !strings.Contains(te.Error(), tt.has) || !strings.Contains(te.Error(), "at byte") {
				t.Errorf("error = %q, want %q and a byte offset", te.Error(), tt.has)
			}
		})
	}
}

func TestRenderTemplate(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		values map[string]string
		want   string
	}{
		{"connection string", "Host=postgres;Username=banana;Password=${pw}", map[string]string{"pw": "s3cr3t"}, "Host=postgres;Username=banana;Password=s3cr3t"},
		{"adjacent", "${a}${b}", map[string]string{"a": "1", "b": "2"}, "12"},
		{"dollar dollar", "cost $$5 ${a}", map[string]string{"a": "x"}, "cost $5 x"},
		{"value with a dollar", "p=${a}", map[string]string{"a": "pa$$word"}, "p=pa$$word"},
		{"value that looks like a placeholder", "p=${a}", map[string]string{"a": "${b}", "b": "BAD"}, "p=${b}"},
		{"value with a lone dollar", "${a}", map[string]string{"a": "$"}, "$"},
		{"same ref twice", "${a}/${a}", map[string]string{"a": "z"}, "z/z"},
		{"empty value", "x${a}y", map[string]string{"a": ""}, "xy"},
		{"extra values are ignored", "${a}", map[string]string{"a": "1", "z": "2"}, "1"},
		{"newline in value", "${a}", map[string]string{"a": "l1\nl2"}, "l1\nl2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderTemplate(tt.in, tt.values)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := RenderTemplate("${a}", map[string]string{}); err == nil || !strings.Contains(err.Error(), `"a"`) {
		t.Errorf("a missing value: err = %v", err)
	}
	if _, err := RenderTemplate("$", nil); err == nil {
		t.Error("an unparsable template rendered")
	}
}

func TestTemplateRefNames(t *testing.T) {
	if got, want := TemplateRefNames("${b}-${a}-${b}"), []string{"b", "a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := TemplateRefNames("$"); got != nil {
		t.Errorf("unparsable: got %v", got)
	}
}

// A secret without a template marshals as before 053, so no stored app's
// revision moves; one with a template has both fields.
func TestSecretTemplateJSON(t *testing.T) {
	b, err := json.Marshal([]Secret{{Name: "A"}, {Name: "B", From: &SecretRef{Provider: "bella", Path: "B"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `[{"name":"A"},{"name":"B","from":{"provider":"bella","path":"B"}}]`; got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
	b, err = json.Marshal(Secret{Name: "C", Template: "Password=${pw}", Refs: map[string]SecretRef{"pw": {Provider: "bella", Path: "DB_PASSWORD"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"C","template":"Password=${pw}","refs":{"pw":{"provider":"bella","path":"DB_PASSWORD"}}}`
	if string(b) != want {
		t.Errorf("json = %s, want %s", b, want)
	}
}

func TestSecretWithoutValueKeepsTemplate(t *testing.T) {
	s := Secret{Name: "C", Template: "x=${a}", Refs: map[string]SecretRef{"a": {Provider: "p", Path: "A"}}}
	got := s.WithoutValue()
	if got.Template != s.Template || !reflect.DeepEqual(got.Refs, s.Refs) {
		t.Errorf("WithoutValue lost the template: %#v", got)
	}
	got.Refs["a"] = SecretRef{Provider: "other", Path: "B"}
	if s.Refs["a"].Provider != "p" {
		t.Error("WithoutValue shares the Refs map with the original")
	}
	if !s.IsReference() || (Secret{Name: "x"}).IsReference() || (Secret{Name: "x", Value: new("v")}).IsReference() {
		t.Error("IsReference is wrong")
	}
}
