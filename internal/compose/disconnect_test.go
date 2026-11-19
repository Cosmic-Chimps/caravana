package compose

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// An overlay's disconnect policy reaches the service, round-trips through
// JSON, survives WithoutSecretValues, and is checked like the rest of the
// model, with the errors naming the service's path in the request.
func TestOverlayDisconnect(t *testing.T) {
	file := "services:\n  api:\n    image: traefik/whoami:latest\n    ports:\n      - 80\n  worker:\n    image: busybox:1\n"
	overlay := func(d *app.Disconnect, domains []Domain, health *Health) Request {
		return Request{Compose: file, Services: map[string]Overlay{"api": {Disconnect: d, Domains: domains, Health: health}}}
	}
	routed := []Domain{{Host: "api.example.com"}}
	httpCheck := &Health{Path: "/up"}

	good := &app.Disconnect{LostAfterSeconds: 120}
	a, _, err := Translate("shop", overlay(good, routed, httpCheck))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got := a.Services[0].Disconnect; !reflect.DeepEqual(got, good) {
		t.Errorf("api disconnect = %+v, want %+v", got, good)
	}
	if got := a.Services[1].Disconnect; got != nil {
		t.Errorf("worker disconnect = %+v, want none", got)
	}

	raw, err := json.Marshal(overlay(good, nil, nil).Services["api"])
	if err != nil {
		t.Fatal(err)
	}
	var back Overlay
	if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back.Disconnect, good) {
		t.Errorf("JSON round trip = %+v (%v) of %s", back.Disconnect, err, raw)
	}

	kept := overlay(good, nil, nil).WithoutSecretValues().Services["api"].Disconnect
	if !reflect.DeepEqual(kept, good) {
		t.Errorf("WithoutSecretValues disconnect = %+v, want %+v", kept, good)
	}

	for _, tt := range []struct {
		name    string
		d       *app.Disconnect
		domains []Domain
		health  *Health
		field   string
	}{
		{"an empty policy", &app.Disconnect{}, nil, nil, "services.api.disconnect.lostAfterSeconds"},
		{"under a minute", &app.Disconnect{LostAfterSeconds: 30}, nil, nil, "services.api.disconnect.lostAfterSeconds"},
		{"over a day", &app.Disconnect{LostAfterSeconds: 86401}, nil, nil, "services.api.disconnect.lostAfterSeconds"},
		{"a domain with a TCP check", good, routed, nil, "services.api.disconnect"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Translate("shop", overlay(tt.d, tt.domains, tt.health))
			fe, ok := err.(*app.FieldError)
			if !ok || fe.Field != tt.field {
				t.Errorf("Translate = %v, want a field error on %s", err, tt.field)
			}
		})
	}
}
