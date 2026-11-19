package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateDisconnect(t *testing.T) {
	set := func(secs int) func(*Service) {
		return func(svc *Service) { svc.Disconnect = &Disconnect{LostAfterSeconds: secs} }
	}
	tests := []struct {
		name      string
		mutate    func(*Service) // applied to services[0], "web": a domain and an HTTP check on its port
		wantField string         // empty means valid
	}{
		{"none", func(*Service) {}, ""},
		{"60", set(60), ""},
		{"300", set(300), ""},
		{"86400", set(86400), ""},
		{"empty object", func(s *Service) { s.Disconnect = &Disconnect{} }, "services[0].disconnect.lostAfterSeconds"},
		{"59", set(59), "services[0].disconnect.lostAfterSeconds"},
		{"86401", set(86401), "services[0].disconnect.lostAfterSeconds"},
		{"negative", set(-1), "services[0].disconnect.lostAfterSeconds"},

		{"health on the domains' port by name", func(s *Service) {
			s.Health = &HealthCheck{Path: "/up", Port: "http"}
			s.Disconnect = &Disconnect{LostAfterSeconds: 120}
		}, ""},
		{"tcp health with a domain", func(s *Service) {
			s.Health = &HealthCheck{}
			s.Disconnect = &Disconnect{LostAfterSeconds: 120}
		}, "services[0].disconnect"},
		{"health on another port", func(s *Service) {
			s.Ports = append(s.Ports, Port{Name: "admin", ContainerPort: 9000})
			s.Health = &HealthCheck{Path: "/up", Port: "admin"}
			s.Disconnect = &Disconnect{LostAfterSeconds: 120}
		}, "services[0].disconnect"},
		{"tcp health without a domain", func(s *Service) {
			s.Domains = nil
			s.Health = &HealthCheck{}
			s.Disconnect = &Disconnect{LostAfterSeconds: 120}
		}, ""},
		{"worker without ports", func(s *Service) {
			s.Ports, s.Domains, s.Health = nil, nil, nil
			s.Disconnect = &Disconnect{LostAfterSeconds: 120}
		}, ""},
		{"volume service", func(s *Service) {
			s.Volumes = []Volume{{Name: "data", Path: "/data"}}
			s.Disconnect = &Disconnect{LostAfterSeconds: 3600}
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
			if tc.wantField == "services[0].disconnect" && !strings.Contains(fe.Message, "health.path") {
				t.Errorf("Message = %q, want it to name health.path", fe.Message)
			}
		})
	}
}

func TestServiceProxyChecks(t *testing.T) {
	domains := []Domain{{Host: "a.example.com", Port: "http"}}
	tests := []struct {
		name string
		svc  Service
		want bool
	}{
		{"no domains", Service{Health: &HealthCheck{Path: "/"}}, false},
		{"no health", Service{Domains: domains}, false},
		{"tcp health", Service{Domains: domains, Health: &HealthCheck{}}, false},
		{"http health", Service{Domains: domains, Health: &HealthCheck{Path: "/"}}, true},
		{"http health on the domains' port", Service{Domains: domains, Health: &HealthCheck{Path: "/", Port: "http"}}, true},
		{"http health on another port", Service{Domains: domains, Health: &HealthCheck{Path: "/", Port: "admin"}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.svc.ProxyChecks(); got != tc.want {
				t.Errorf("ProxyChecks() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKeepsCopiesOnDisconnect(t *testing.T) {
	domains := []Domain{{Host: "a.example.com", Port: "http"}}
	tests := []struct {
		name string
		svc  Service
		want bool
	}{
		{"worker", Service{}, true},
		{"internal service with tcp health", Service{Health: &HealthCheck{}}, true},
		{"routed with http health", Service{Domains: domains, Health: &HealthCheck{Path: "/"}}, true},
		{"routed with tcp health", Service{Domains: domains, Health: &HealthCheck{}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.svc.KeepsCopiesOnDisconnect(); got != tc.want {
				t.Errorf("KeepsCopiesOnDisconnect() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLostAfter(t *testing.T) {
	if got := (Service{}).LostAfter(); got != 5*time.Minute {
		t.Errorf("default = %v, want 5m", got)
	}
	if got := (Service{Disconnect: &Disconnect{LostAfterSeconds: 120}}).LostAfter(); got != 2*time.Minute {
		t.Errorf("set = %v, want 2m", got)
	}
}

// An app that sets no disconnect policy encodes exactly as before, so no
// stored app's revision moves.
func TestDisconnectOmittedFromJSON(t *testing.T) {
	b, err := json.Marshal(Service{Name: "web", Image: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "disconnect") {
		t.Errorf("json = %s, want no disconnect", b)
	}
	b, err = json.Marshal(Service{Name: "web", Image: "x", Disconnect: &Disconnect{LostAfterSeconds: 120}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"disconnect":{"lostAfterSeconds":120}`) {
		t.Errorf("json = %s, want the policy", b)
	}
}
