package mesh

import "testing"

// A server can't be named after a word the console's addresses use.
func TestReservedNames(t *testing.T) {
	for _, tt := range []struct {
		host  string
		index int
		want  string
	}{
		{"new", 2, "new-2"},
		{"joins.example.com", 5, "joins-5"},
		{"newer", 2, "newer"},
		{"fsn-2", 3, "fsn-2"},
	} {
		if got := NameOfHost(tt.host, tt.index); got != tt.want {
			t.Errorf("NameOfHost(%q, %d) = %q, want %q", tt.host, tt.index, got, tt.want)
		}
	}
	if !ReservedName("new") || !ReservedName("joins") || ReservedName("news") {
		t.Error("ReservedName is wrong about new, joins or news")
	}
}
