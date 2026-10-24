package compose

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// TestDomainOptions checks that the overlay's domain options (feature 023)
// reach the app, that a domain without them is as before, and that they
// round-trip through JSON with the API's names.
func TestDomainOptions(t *testing.T) {
	file := "services:\n  web:\n    image: traefik/whoami:latest\n    ports: [\"8080:80\"]\n"
	req := Request{Compose: file, Services: map[string]Overlay{"web": {Domains: []Domain{
		{Host: "example.com", WWWRedirect: true},
		{Host: "admin.example.com", TeamOnly: true, Path: "/admin"},
		{Host: "plain.example.com", Port: 80},
	}}}}
	a, _, err := Translate("web", req)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	port := a.Services[0].Ports[0].Name
	want := []app.Domain{
		{Host: "example.com", Port: port, WWWRedirect: true},
		{Host: "admin.example.com", Port: port, TeamOnly: true, Path: "/admin"},
		{Host: "plain.example.com", Port: port},
	}
	if got := a.Services[0].Domains; !reflect.DeepEqual(got, want) {
		t.Errorf("domains = %+v, want %+v", got, want)
	}

	raw, err := json.Marshal(req.Services["web"].Domains)
	if err != nil {
		t.Fatal(err)
	}
	const wantJSON = `[{"host":"example.com","wwwRedirect":true},` +
		`{"host":"admin.example.com","path":"/admin","teamOnly":true},` +
		`{"host":"plain.example.com","port":80}]`
	if string(raw) != wantJSON {
		t.Errorf("JSON = %s, want %s", raw, wantJSON)
	}
	var back []Domain
	if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back, req.Services["web"].Domains) {
		t.Errorf("JSON round trip = %+v (%v)", back, err)
	}
}
