package compose

import (
	"reflect"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

func translateDNS(t *testing.T, serviceYAML string) (app.App, error) {
	t.Helper()
	file := "services:\n  web:\n    image: traefik/whoami:latest\n" + serviceYAML
	a, _, err := Translate("shop", Request{Compose: file})
	return a, err
}

// Compose's dns, dns_search and dns_opt become the service's DNS settings, in
// the string form and the list form.
func TestDNS(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want *app.DNS
	}{
		{"none", "", nil},
		{"string form", "    dns: 1.1.1.1\n    dns_search: corp.example.com\n", &app.DNS{Servers: []string{"1.1.1.1"}, Searches: []string{"corp.example.com"}}},
		{"list form", "    dns:\n      - 1.1.1.1\n      - 2606:4700:4700::1111\n    dns_search:\n      - a.example\n      - b.example\n    dns_opt:\n      - ndots:2\n      - edns0\n",
			&app.DNS{Servers: []string{"1.1.1.1", "2606:4700:4700::1111"}, Searches: []string{"a.example", "b.example"}, Options: []string{"ndots:2", "edns0"}}},
		{"only options", "    dns_opt:\n      - rotate\n", &app.DNS{Options: []string{"rotate"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := translateDNS(t, tt.yaml)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			if got := a.Services[0].DNS; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DNS = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// What the model refuses is refused with the path the Compose file used.
func TestDNSRefusals(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		field string
	}{
		{"a hostname as a server", "    dns: dns.example.com\n", "services.web.dns[0]"},
		{"second server a name", "    dns:\n      - 1.1.1.1\n      - ns1\n", "services.web.dns[1]"},
		{"four servers", "    dns: [1.1.1.1, 1.0.0.1, 8.8.8.8, 8.8.4.4]\n", "services.web.dns"},
		{"an upper-case search domain", "    dns_search: Corp.Example\n", "services.web.dns_search[0]"},
		{"a bad option", "    dns_opt: ['ndots: 2']\n", "services.web.dns_opt[0]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := translateDNS(t, tt.yaml)
			fe, ok := err.(*app.FieldError)
			if !ok || fe.Field != tt.field {
				t.Errorf("Translate = %v, want a field error on %s", err, tt.field)
			}
		})
	}
}
