package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDomainOptionsValidation(t *testing.T) {
	with := func(d Domain) func(a *App) {
		d.Port = "http"
		return func(a *App) { a.Services[0].Domains = []Domain{d} }
	}
	tests := []struct {
		name      string
		mutate    func(a *App)
		wantField string // empty means valid
	}{
		{"no options", with(Domain{Host: "shop.example.com"}), ""},
		{"path", with(Domain{Host: "shop.example.com", Path: "/api"}), ""},
		{"path root", with(Domain{Host: "shop.example.com", Path: "/"}), ""},
		{"path at the limit", with(Domain{Host: "shop.example.com", Path: "/" + strings.Repeat("a", 199)}), ""},
		{"team only", with(Domain{Host: "shop.example.com", TeamOnly: true}), ""},
		{"redirect on an apex", with(Domain{Host: "example.com", WWWRedirect: true}), ""},
		{"redirect on a www host", with(Domain{Host: "www.example.com", WWWRedirect: true}), ""},
		{"all options", with(Domain{Host: "example.com", Path: "/app", WWWRedirect: true, TeamOnly: true}), ""},

		{"path without slash", with(Domain{Host: "shop.example.com", Path: "api"}), "services[0].domains[0].path"},
		{"path too long", with(Domain{Host: "shop.example.com", Path: "/" + strings.Repeat("a", 200)}), "services[0].domains[0].path"},
		{"path with backtick", with(Domain{Host: "shop.example.com", Path: "/a`b"}), "services[0].domains[0].path"},
		{"path with space", with(Domain{Host: "shop.example.com", Path: "/a b"}), "services[0].domains[0].path"},
		{"path with ||", with(Domain{Host: "shop.example.com", Path: "/a||b"}), "services[0].domains[0].path"},
		{"path with &&", with(Domain{Host: "shop.example.com", Path: "/a&&b"}), "services[0].domains[0].path"},
		{"path with NUL", with(Domain{Host: "shop.example.com", Path: "/a\x00b"}), "services[0].domains[0].path"},
		{"redirect on a subdomain", with(Domain{Host: "shop.example.com", WWWRedirect: true}), "services[0].domains[0].wwwRedirect"},
		{"redirect on a single label", with(Domain{Host: "localhost", WWWRedirect: true}), "services[0].domains[0].wwwRedirect"},
		{"redirect whose pair is another domain", func(a *App) {
			a.Services[0].Domains = []Domain{
				{Host: "example.com", Port: "http", WWWRedirect: true},
				{Host: "www.example.com", Port: "http"},
			}
		}, "services[0].domains[0].wwwRedirect"},
		{"redirect whose pair is a domain of another service", func(a *App) {
			a.Services[0].Domains = []Domain{{Host: "www.example.com", Port: "http", WWWRedirect: true}}
			a.Services[1].Ports = []Port{{Name: "http", ContainerPort: 8080}}
			a.Services[1].Health = &HealthCheck{}
			a.Services[1].Domains = []Domain{{Host: "example.com", Port: "http"}}
		}, "services[0].domains[0].wwwRedirect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := validApp()
			tt.mutate(&a)
			err := ValidateApp(a)
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("ValidateApp = %v, want nil", err)
				}
				return
			}
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("ValidateApp = %v, want a *FieldError", err)
			}
			if fe.Field != tt.wantField {
				t.Errorf("field = %q, want %q (%v)", fe.Field, tt.wantField, err)
			}
		})
	}
}

func TestDomainWithoutOptionsMarshalsAsBefore(t *testing.T) {
	got, err := json.Marshal(Domain{Host: "shop.example.com", Port: "http"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"host":"shop.example.com","port":"http"}`; string(got) != want {
		t.Errorf("json = %s, want %s", got, want)
	}

	full, err := json.Marshal(Domain{Host: "example.com", Port: "http", Path: "/a", WWWRedirect: true, TeamOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"host":"example.com","port":"http","path":"/a","wwwRedirect":true,"teamOnly":true}`; string(full) != want {
		t.Errorf("json = %s, want %s", full, want)
	}

	// A stored document from before 023 reads back without options.
	var d Domain
	if err := json.Unmarshal([]byte(`{"host":"a.b.com","port":"http"}`), &d); err != nil || d.HasOptions() {
		t.Errorf("old document: %+v, %v", d, err)
	}
}

func TestDomainHasOptions(t *testing.T) {
	tests := []struct {
		d    Domain
		want bool
	}{
		{Domain{Host: "a.com", Port: "http"}, false},
		{Domain{Host: "a.com", Path: "/x"}, true},
		{Domain{Host: "a.com", WWWRedirect: true}, true},
		{Domain{Host: "a.com", TeamOnly: true}, true},
	}
	for _, tt := range tests {
		if got := tt.d.HasOptions(); got != tt.want {
			t.Errorf("%+v HasOptions = %v, want %v", tt.d, got, tt.want)
		}
	}
}

func TestDomainRedirectPair(t *testing.T) {
	tests := []struct{ host, want string }{
		{"example.com", "www.example.com"},
		{"www.example.com", "example.com"},
		{"www.shop.example.com", "shop.example.com"},
		{"shop.example.com", ""},
		{"localhost", ""},
		{"www.com", "www.www.com"}, // an apex that happens to start with www.
		{"", ""},
	}
	for _, tt := range tests {
		if got := (Domain{Host: tt.host}).RedirectPair(); got != tt.want {
			t.Errorf("RedirectPair(%q) = %q, want %q", tt.host, got, tt.want)
		}
	}
}

func TestIsApex(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"example.com", true},
		{"www.example.com", false},
		{"localhost", false},
		{"a.b.c.com", false},
		{"example.co.uk", false}, // no public suffix list: read as a subdomain
		{"", false},
	}
	for _, tt := range tests {
		if got := IsApex(tt.host); got != tt.want {
			t.Errorf("IsApex(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

// A host typed with capitals is the same DNS name: WithLowercaseHosts lowercases it, copies what
// it changes and leaves an app that needs nothing exactly as it was.
func TestWithLowercaseHosts(t *testing.T) {
	a := validApp()
	a.Services[0].Domains = []Domain{{Host: "WWW.Example.com", Port: "http", Path: "/Api", TeamOnly: true}, {Host: "api.example.com", Port: "http"}}
	got := a.WithLowercaseHosts()
	if got.Services[0].Domains[0] != (Domain{Host: "www.example.com", Port: "http", Path: "/Api", TeamOnly: true}) || got.Services[0].Domains[1].Host != "api.example.com" {
		t.Errorf("lowercased = %+v", got.Services[0].Domains)
	}
	if a.Services[0].Domains[0].Host != "WWW.Example.com" {
		t.Errorf("the caller's app changed: %+v", a.Services[0].Domains)
	}
	if err := ValidateApp(got); err != nil {
		t.Errorf("a lowercased app is refused: %v", err)
	}
	if err := ValidateApp(a); err == nil {
		t.Error("ValidateApp accepts capitals in a host: its rule is lowercase")
	}

	// Nothing to change: the same slices, no copy, so a stored document is unchanged.
	b := validApp()
	b.Services[0].Domains = []Domain{{Host: "shop.example.com", Port: "http"}}
	same := b.WithLowercaseHosts()
	if &same.Services[0] != &b.Services[0] || &same.Services[0].Domains[0] != &b.Services[0].Domains[0] {
		t.Error("an app whose hosts are lowercase was copied")
	}
	before, _ := json.Marshal(b)
	after, _ := json.Marshal(same)
	if string(before) != string(after) {
		t.Errorf("document changed:\n%s\n%s", before, after)
	}
}
