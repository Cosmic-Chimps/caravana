package certs

import "testing"

func TestCovers(t *testing.T) {
	tests := []struct {
		names []string
		host  string
		want  bool
	}{
		{[]string{"a.example.test"}, "a.example.test", true},
		{[]string{"A.Example.Test"}, "a.EXAMPLE.test", true},
		{[]string{"a.example.test"}, "b.example.test", false},
		{[]string{"a.example.test"}, "a.example.test.", true}, // a trailing dot is the same name
		{[]string{"*.example.test"}, "a.example.test", true},
		{[]string{"*.Example.test"}, "A.example.test", true},
		{[]string{"*.example.test"}, "example.test", false},     // never the bare name
		{[]string{"*.example.test"}, "a.b.example.test", false}, // exactly one label
		{[]string{"*.example.test"}, ".example.test", false},
		{[]string{"*.example.test"}, "aexample.test", false},
		{[]string{"*.example.test"}, "a.xexample.test", false},
		{[]string{"*.example.test", "example.test"}, "example.test", true},
		{[]string{"*.b.example.test"}, "a.b.example.test", true},
		{[]string{"*.b.example.test"}, "a.example.test", false},
		{[]string{"*."}, "a.", false},
		{[]string{"*"}, "a", false},
		{[]string{"*.other.test"}, "*.example.test", false},
		{[]string{"x.test", "*.example.test"}, "q.example.test", true},
		{nil, "a.example.test", false},
		{[]string{"a.example.test"}, "", false},
	}
	for _, tt := range tests {
		if got := Covers(tt.names, tt.host); got != tt.want {
			t.Errorf("Covers(%v, %q) = %v, want %v", tt.names, tt.host, got, tt.want)
		}
	}
}

func TestSameNames(t *testing.T) {
	tests := []struct {
		a, b []string
		want bool
	}{
		{[]string{"a.test", "b.test"}, []string{"b.test", "a.test"}, true},
		{[]string{"A.test"}, []string{"a.TEST"}, true},
		{[]string{"a.test", "a.test"}, []string{"a.test"}, true},
		{[]string{"a.test"}, []string{"a.test", "b.test"}, false},
		{[]string{"*.test"}, []string{"a.test"}, false},
		{nil, []string{}, true},
		{nil, []string{"a.test"}, false},
	}
	for _, tt := range tests {
		if got := SameNames(tt.a, tt.b); got != tt.want {
			t.Errorf("SameNames(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
