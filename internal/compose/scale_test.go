package compose

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// An overlay's scale (spec 043) reaches the service, round-trips through
// JSON, survives WithoutSecretValues, and is checked like the rest of the
// model, with the errors naming the service's path in the request.
func TestOverlayScale(t *testing.T) {
	file := "services:\n  api:\n    image: traefik/whoami:latest\n    ports:\n      - \"8080\"\n    deploy:\n      replicas: 2\n  worker:\n    image: busybox:1\n"
	domain := []Domain{{Host: "api.example.com", Port: 8080}}
	overlay := func(sc *app.Scale, domains []Domain) Request {
		return Request{Compose: file, Services: map[string]Overlay{"api": {Scale: sc, Domains: domains}}}
	}

	good := &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{RequestsPerCopy: 50}, CooldownSeconds: 120}
	a, _, err := Translate("shop", overlay(good, domain))
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got := a.Services[0].Scale; !reflect.DeepEqual(got, good) {
		t.Errorf("api scale = %+v, want %+v", got, good)
	}
	if got := a.Services[0].Replicas; got == nil || *got != 2 {
		t.Errorf("api replicas = %v, want the file's 2 as the starting count", got)
	}
	if got := a.Services[1].Scale; got != nil {
		t.Errorf("worker scale = %+v, want none", got)
	}

	raw, err := json.Marshal(overlay(good, domain).Services["api"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"scale":{"min":1,"max":3,"target":{"requestsPerCopy":50},"cooldownSeconds":120}`) {
		t.Errorf("overlay JSON = %s", raw)
	}
	var back Overlay
	if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back.Scale, good) {
		t.Errorf("JSON round trip = %+v (%v) of %s", back.Scale, err, raw)
	}

	kept := overlay(good, domain).WithoutSecretValues().Services["api"].Scale
	if !reflect.DeepEqual(kept, good) {
		t.Errorf("WithoutSecretValues scale = %+v, want %+v", kept, good)
	}

	cpu := app.ScaleTarget{CPU: 70}
	for _, tt := range []struct {
		name    string
		scale   *app.Scale
		domains []Domain
		field   string
	}{
		{"min zero", &app.Scale{Min: 0, Max: 3, Target: cpu}, domain, "services.api.scale.min"},
		{"max under min", &app.Scale{Min: 3, Max: 2, Target: cpu}, domain, "services.api.scale.max"},
		{"no target", &app.Scale{Min: 1, Max: 3}, domain, "services.api.scale.target"},
		{"cpu out of range", &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{CPU: 5}}, domain, "services.api.scale.target.cpu"},
		{"cooldown out of range", &app.Scale{Min: 1, Max: 3, Target: cpu, CooldownSeconds: 10}, domain, "services.api.scale.cooldownSeconds"},
		{"the file's replicas outside the scale", &app.Scale{Min: 3, Max: 4, Target: cpu}, domain, "services.api.replicas"},
		{"requests without a domain", &app.Scale{Min: 1, Max: 3, Target: app.ScaleTarget{RequestsPerCopy: 5}}, nil, "services.api.scale.target"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Translate("shop", overlay(tt.scale, tt.domains))
			fe, ok := err.(*app.FieldError)
			if !ok || fe.Field != tt.field {
				t.Errorf("Translate = %v, want a field error on %s", err, tt.field)
			}
		})
	}

	// A service with a volume runs one copy, so it can't scale.
	withVolume := "services:\n  db:\n    image: postgres:17\n    volumes:\n      - data:/var/lib/postgresql/data\nvolumes:\n  data: {}\n"
	_, _, err = Translate("shop", Request{Compose: withVolume, Services: map[string]Overlay{"db": {Scale: &app.Scale{Min: 1, Max: 2, Target: cpu}}}})
	if fe, ok := err.(*app.FieldError); !ok || fe.Field != "services.db.scale" {
		t.Errorf("a volume with a scale: Translate = %v, want a field error on services.db.scale", err)
	}
}
