package compose

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// An overlay's placement (spec 042) reaches the service, round-trips through
// JSON, survives WithoutSecretValues, and is checked like the rest of the
// model, with the errors naming the service's path in the request.
func TestOverlayPlacement(t *testing.T) {
	file := "services:\n  api:\n    image: traefik/whoami:latest\n  worker:\n    image: busybox:1\n"
	overlay := func(p *app.Placement) Request {
		return Request{Compose: file, Services: map[string]Overlay{"api": {Placement: p}}}
	}

	good := &app.Placement{
		Require: map[string]string{"ssd": "true"},
		Prefer:  []app.Preference{{Label: "region", Value: "eu", Weight: 80}},
		Avoid:   []string{"three"},
	}
	a, _, err := Translate("shop", overlay(good))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got := a.Services[0].Placement; !reflect.DeepEqual(got, good) {
		t.Errorf("api placement = %+v, want %+v", got, good)
	}
	if got := a.Services[1].Placement; got != nil {
		t.Errorf("worker placement = %+v, want none", got)
	}

	raw, err := json.Marshal(overlay(good).Services["api"])
	if err != nil {
		t.Fatal(err)
	}
	var back Overlay
	if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back.Placement, good) {
		t.Errorf("JSON round trip = %+v (%v) of %s", back.Placement, err, raw)
	}

	kept := overlay(good).WithoutSecretValues().Services["api"].Placement
	if !reflect.DeepEqual(kept, good) {
		t.Errorf("WithoutSecretValues placement = %+v, want %+v", kept, good)
	}

	for _, tt := range []struct {
		name  string
		p     *app.Placement
		field string
	}{
		{"an empty placement", &app.Placement{}, "services.api.placement"},
		{"an upper-case label", &app.Placement{Require: map[string]string{"SSD": "true"}}, "services.api.placement.require"},
		{"a bad value", &app.Placement{Require: map[string]string{"ssd": "no way"}}, "services.api.placement.require.ssd"},
		{"a weight of 101", &app.Placement{Prefer: []app.Preference{{Label: "a", Value: "b", Weight: 101}}}, "services.api.placement.prefer[0].weight"},
		{"a reserved server name", &app.Placement{Avoid: []string{"new"}}, "services.api.placement.avoid[0]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Translate("shop", overlay(tt.p))
			fe, ok := err.(*app.FieldError)
			if !ok || fe.Field != tt.field {
				t.Errorf("Translate = %v, want a field error on %s", err, tt.field)
			}
		})
	}
}
