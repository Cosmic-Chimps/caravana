package app

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func ip(n int) *int { return &n }

func TestValidateScale(t *testing.T) {
	cpu := ScaleTarget{CPU: 70}
	web := func(s *Scale) func(*Service) {
		return func(svc *Service) { svc.Scale = s }
	}
	tests := []struct {
		name      string
		mutate    func(*Service) // applied to services[0], "web", which has a domain
		wantField string         // empty means valid
	}{
		{"none", func(*Service) {}, ""},
		{"cpu", web(&Scale{Min: 1, Max: 3, Target: cpu}), ""},
		{"requests with a domain", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{RequestsPerCopy: 50}}), ""},
		{"p95 with a domain", web(&Scale{Min: 2, Max: 20, Target: ScaleTarget{P95: 300}, CooldownSeconds: 600}), ""},
		{"min equals max", web(&Scale{Min: 2, Max: 2, Target: cpu}), ""},
		{"replicas inside", func(s *Service) { s.Replicas = ip(2); s.Scale = &Scale{Min: 1, Max: 3, Target: cpu} }, ""},
		{"cooldown 60", web(&Scale{Min: 1, Max: 3, Target: cpu, CooldownSeconds: 60}), ""},
		{"cooldown 3600", web(&Scale{Min: 1, Max: 3, Target: cpu, CooldownSeconds: 3600}), ""},

		{"min zero", web(&Scale{Min: 0, Max: 3, Target: cpu}), "services[0].scale.min"},
		{"min negative", web(&Scale{Min: -1, Max: 3, Target: cpu}), "services[0].scale.min"},
		{"max under min", web(&Scale{Min: 3, Max: 2, Target: cpu}), "services[0].scale.max"},
		{"max zero", web(&Scale{Min: 1, Target: cpu}), "services[0].scale.max"},
		{"max 20", web(&Scale{Min: 1, Max: 20, Target: cpu}), ""},
		{"max 21", web(&Scale{Min: 1, Max: 21, Target: cpu}), "services[0].scale.max"},

		{"no target", web(&Scale{Min: 1, Max: 3}), "services[0].scale.target"},
		{"two targets", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{CPU: 70, P95: 300}}), "services[0].scale.target"},
		{"three targets", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{CPU: 70, RequestsPerCopy: 5, P95: 300}}), "services[0].scale.target"},
		{"cpu 9", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{CPU: 9}}), "services[0].scale.target.cpu"},
		{"cpu 10", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{CPU: 10}}), ""},
		{"cpu 200", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{CPU: 200}}), ""},
		{"cpu 201", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{CPU: 201}}), "services[0].scale.target.cpu"},
		{"cpu negative", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{CPU: -5}}), "services[0].scale.target.cpu"},
		{"requests 100000", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{RequestsPerCopy: 100000}}), ""},
		{"requests 100001", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{RequestsPerCopy: 100001}}), "services[0].scale.target.requestsPerCopy"},
		{"requests negative", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{RequestsPerCopy: -1}}), "services[0].scale.target.requestsPerCopy"},
		{"p95 9", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{P95: 9}}), "services[0].scale.target.p95"},
		{"p95 60000", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{P95: 60000}}), ""},
		{"p95 60001", web(&Scale{Min: 1, Max: 3, Target: ScaleTarget{P95: 60001}}), "services[0].scale.target.p95"},

		{"cooldown 59", web(&Scale{Min: 1, Max: 3, Target: cpu, CooldownSeconds: 59}), "services[0].scale.cooldownSeconds"},
		{"cooldown 3601", web(&Scale{Min: 1, Max: 3, Target: cpu, CooldownSeconds: 3601}), "services[0].scale.cooldownSeconds"},
		{"cooldown negative", web(&Scale{Min: 1, Max: 3, Target: cpu, CooldownSeconds: -1}), "services[0].scale.cooldownSeconds"},

		{"replicas under min", func(s *Service) { s.Replicas = ip(1); s.Scale = &Scale{Min: 2, Max: 3, Target: cpu} }, "services[0].replicas"},
		{"replicas over max", func(s *Service) { s.Replicas = ip(4); s.Scale = &Scale{Min: 1, Max: 3, Target: cpu} }, "services[0].replicas"},

		{"volume", func(s *Service) {
			s.Volumes = []Volume{{Name: "data", Path: "/data"}}
			s.Scale = &Scale{Min: 1, Max: 3, Target: cpu}
		}, "services[0].scale"},
		{"published port", func(s *Service) {
			s.Ports = append(s.Ports, Port{Name: "turn", ContainerPort: 3478, Published: 3478})
			s.Scale = &Scale{Min: 1, Max: 3, Target: cpu}
		}, "services[0].scale"},
		{"requests without a domain", func(s *Service) {
			s.Domains = nil
			s.Scale = &Scale{Min: 1, Max: 3, Target: ScaleTarget{RequestsPerCopy: 50}}
		}, "services[0].scale.target"},
		{"p95 without a domain", func(s *Service) {
			s.Domains = nil
			s.Scale = &Scale{Min: 1, Max: 3, Target: ScaleTarget{P95: 300}}
		}, "services[0].scale.target"},
		{"cpu without a domain", func(s *Service) {
			s.Domains = nil
			s.Scale = &Scale{Min: 1, Max: 3, Target: cpu}
		}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := validApp()
			tc.mutate(&a.Services[0])
			err := ValidateApp(a)
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("ValidateApp() = %v, want nil", err)
				}
				return
			}
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("ValidateApp() = %v, want a *FieldError for %q", err, tc.wantField)
			}
			if fe.Field != tc.wantField {
				t.Errorf("Field = %q, want %q (error: %v)", fe.Field, tc.wantField, err)
			}
			if fe.Message == "" {
				t.Errorf("Message is empty for %q", fe.Field)
			}
		})
	}
}

func TestScaleCooldown(t *testing.T) {
	if got := (Scale{}).Cooldown(); got != 5*time.Minute {
		t.Errorf("default = %v, want 5m", got)
	}
	if got := (Scale{CooldownSeconds: 90}).Cooldown(); got != 90*time.Second {
		t.Errorf("set = %v, want 1m30s", got)
	}
}

func TestScaleTargetKind(t *testing.T) {
	tests := []struct {
		t    ScaleTarget
		want string
	}{
		{ScaleTarget{}, ""},
		{ScaleTarget{CPU: 70}, "cpu"},
		{ScaleTarget{RequestsPerCopy: 5}, "requestsPerCopy"},
		{ScaleTarget{P95: 300}, "p95"},
	}
	for _, tc := range tests {
		if got := tc.t.Kind(); got != tc.want {
			t.Errorf("%+v Kind() = %q, want %q", tc.t, got, tc.want)
		}
	}
}

func TestCountFor(t *testing.T) {
	sc := &Scale{Min: 2, Max: 5, Target: ScaleTarget{CPU: 70}}
	tests := []struct {
		name    string
		svc     Service
		current *int
		want    int
	}{
		{"unscaled, unset", Service{}, nil, 1},
		{"unscaled, replicas", Service{Replicas: ip(3)}, nil, 3},
		{"unscaled ignores current", Service{Replicas: ip(3)}, ip(7), 3},
		{"scaled first deploy starts at replicas", Service{Replicas: ip(4), Scale: sc}, nil, 4},
		{"scaled first deploy without replicas starts at min", Service{Scale: sc}, nil, 2},
		{"scaled keeps the current count", Service{Replicas: ip(2), Scale: sc}, ip(4), 4},
		{"current above max is clamped", Service{Scale: sc}, ip(9), 5},
		{"current below min is clamped", Service{Scale: sc}, ip(1), 2},
		{"replicas outside is clamped", Service{Replicas: ip(9), Scale: sc}, nil, 5},
	}
	for _, tc := range tests {
		if got := tc.svc.CountFor(tc.current); got != tc.want {
			t.Errorf("%s: CountFor() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// A service without scale marshals exactly as before feature 043, so no
// stored app's revision moves; one with scale writes only what is set.
func TestScaleJSONUnchanged(t *testing.T) {
	b, err := json.Marshal(Service{Name: "web", Image: "x", Replicas: ip(2)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"name":"web","image":"x","replicas":2}`; got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
	b, err = json.Marshal(Service{Name: "web", Image: "x", Scale: &Scale{Min: 1, Max: 3, Target: ScaleTarget{RequestsPerCopy: 50}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"web","image":"x","scale":{"min":1,"max":3,"target":{"requestsPerCopy":50}}}`
	if got := string(b); got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
}
