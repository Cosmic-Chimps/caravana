package mesh

import (
	"strings"
	"testing"
)

func TestCleanIssuer(t *testing.T) {
	long := strings.Repeat("a", 64)
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{"https://console.example.com", "https://console.example.com", true},
		{"https://console.example.com/", "https://console.example.com", true},
		{"https://console.example.com:8443", "https://console.example.com:8443", true},
		{"https://localhost", "https://localhost", true},
		{"https://localhost:7070", "https://localhost:7070", true},
		{"https://a-b.example.com", "https://a-b.example.com", true},
		{"http://console.example.com", "", false},
		{"console.example.com", "", false},
		{"https://", "", false},
		{"https://console", "", false}, // one label that isn't localhost
		{"https://console.example.com//", "", false},
		{"https://console.example.com/app", "", false},
		{"https://console.example.com?x=1", "", false},
		{"https://console.example.com#f", "", false},
		{"https://u@console.example.com", "", false},
		{"https://u:p@console.example.com", "", false},
		{"https://console.example.com:", "", false},
		{"https://console.example.com:0", "", false},
		{"https://console.example.com:65536", "", false},
		{"https://console.example.com:80a", "", false},
		{"https://console.example.com:1:2", "", false},
		{"https://1.2.3.4", "", false},
		{"https://1.2.3.4:443", "", false},
		{"https://[::1]", "", false},
		{"https://[::1]:443", "", false},
		{"https://-bad.example.com", "", false},
		{"https://bad-.example.com", "", false},
		{"https://bad..example.com", "", false},
		{"https://" + long + ".example.com", "", false},
		{"https://console.example.com ", "", false},
		{"https://console.exa mple.com", "", false},
		{"https://console.example.com\n", "", false},
		{"HTTPS://console.example.com", "", false},
	}
	for _, c := range cases {
		got, ok := CleanIssuer(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("CleanIssuer(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
