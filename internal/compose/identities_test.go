package compose

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// An overlay's identities (spec 038) reach the service, round-trip through
// JSON, and are checked like the rest of the model, with the errors naming the
// service's path in the request.
func TestOverlayIdentities(t *testing.T) {
	file := "services:\n  api:\n    image: traefik/whoami:latest\n"
	overlay := func(ids ...app.Identity) Request {
		return Request{Compose: file, Services: map[string]Overlay{"api": {Identities: ids}}}
	}

	good := []app.Identity{
		{Name: "bella", Audience: []string{"bella-spiffe/env-1"}},
		{Name: "billing", Audience: []string{"billing"}, TTLSeconds: 120},
	}
	a, _, err := Translate("shop", overlay(good...))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got := a.Services[0].Identities; !reflect.DeepEqual(got, good) {
		t.Errorf("identities = %+v, want %+v", got, good)
	}

	raw, err := json.Marshal(overlay(good...).Services["api"])
	if err != nil {
		t.Fatal(err)
	}
	var back Overlay
	if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back.Identities, good) {
		t.Errorf("JSON round trip = %+v (%v) of %s", back.Identities, err, raw)
	}

	for _, tt := range []struct {
		name  string
		ids   []app.Identity
		field string
	}{
		{"an upper-case name", []app.Identity{{Name: "Bella", Audience: []string{"x"}}}, "services.api.identities[0].name"},
		{"default", []app.Identity{{Name: "default", Audience: []string{"x"}}}, "services.api.identities[0].name"},
		{"a name twice", []app.Identity{{Name: "a", Audience: []string{"x"}}, {Name: "a", Audience: []string{"y"}}}, "services.api.identities[1].name"},
		{"no audience", []app.Identity{{Name: "a"}}, "services.api.identities[0].audience"},
		{"a short ttl", []app.Identity{{Name: "a", Audience: []string{"x"}, TTLSeconds: 10}}, "services.api.identities[0].ttlSeconds"},
		{"five identities", []app.Identity{
			{Name: "a", Audience: []string{"x"}}, {Name: "b", Audience: []string{"x"}}, {Name: "c", Audience: []string{"x"}},
			{Name: "d", Audience: []string{"x"}}, {Name: "e", Audience: []string{"x"}},
		}, "services.api.identities"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Translate("shop", overlay(tt.ids...))
			fe, ok := err.(*app.FieldError)
			if !ok || fe.Field != tt.field {
				t.Errorf("Translate = %v, want a field error on %s", err, tt.field)
			}
		})
	}
}
