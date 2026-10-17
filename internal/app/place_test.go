package app

import (
	"errors"
	"strings"
	"testing"
)

func TestPlaceOf(t *testing.T) {
	tests := []struct {
		id   string
		want Place
	}{
		{"whoami", Place{"default", "production", "whoami"}},
		{"a", Place{"default", "production", "a"}},
		{"my-app-2", Place{"default", "production", "my-app-2"}},
		{"shop--staging--web", Place{"shop", "staging", "web"}},
		{"shop--production--web", Place{"shop", "production", "web"}},
		{"default--staging--web", Place{"default", "staging", "web"}},
		{"my-shop--pre-prod--web-2", Place{"my-shop", "pre-prod", "web-2"}},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			got, err := PlaceOf(tt.id)
			if err != nil {
				t.Fatalf("PlaceOf(%q): %v", tt.id, err)
			}
			if got != tt.want {
				t.Errorf("PlaceOf(%q) = %+v, want %+v", tt.id, got, tt.want)
			}
			if id := got.ID(); id != tt.id {
				t.Errorf("ID() = %q, want %q", id, tt.id)
			}
		})
	}
}

func TestPlaceOfRefusals(t *testing.T) {
	tests := []struct {
		name, id string
		contains string // part of the message, so the owner knows what is wrong
	}{
		{"empty", "", "not a lowercase DNS label"},
		{"uppercase", "Whoami", "not a lowercase DNS label"},
		{"underscore", "my_app", "not a lowercase DNS label"},
		{"two parts", "a--b", "three names"},
		{"four parts", "a--b--c--d", "three names"},
		{"three dashes", "a---b--c", "environment part"},
		{"empty project", "--staging--web", "project part"},
		{"empty environment", "shop----web", "environment part"},
		{"empty name", "shop--staging--", "name part"},
		{"uppercase part", "Shop--staging--web", "project part"},
		{"name ends with a dash", "shop--staging--web-", "name part"},
		{"default production spelled out", "default--production--web", `written "web"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PlaceOf(tt.id)
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("PlaceOf(%q) = %v, want a *FieldError", tt.id, err)
			}
			if fe.Field != "id" {
				t.Errorf("field = %q, want id", fe.Field)
			}
			if !strings.Contains(fe.Message, tt.contains) {
				t.Errorf("message %q does not mention %q", fe.Message, tt.contains)
			}
		})
	}
}

func TestPlaceID(t *testing.T) {
	tests := []struct {
		place Place
		want  string
	}{
		{Place{"default", "production", "whoami"}, "whoami"},
		{Place{"default", "staging", "whoami"}, "default--staging--whoami"},
		{Place{"shop", "production", "web"}, "shop--production--web"},
		{Place{"shop", "staging", "web"}, "shop--staging--web"},
	}
	for _, tt := range tests {
		got := tt.place.ID()
		if got != tt.want {
			t.Errorf("%+v.ID() = %q, want %q", tt.place, got, tt.want)
		}
		back, err := PlaceOf(got)
		if err != nil || back != tt.place {
			t.Errorf("PlaceOf(%q) = %+v, %v; want %+v", got, back, err, tt.place)
		}
	}
}

func TestValidName(t *testing.T) {
	good := []string{"a", "shop", "my-shop", "web2", "0day", strings.Repeat("a", 63)}
	for _, s := range good {
		if err := ValidName("name", s); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{"", "Shop", "-shop", "shop-", "a--b", "a---b", "my_shop", "a b", "a.b", strings.Repeat("a", 64)}
	for _, s := range bad {
		err := ValidName("project", s)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != "project" {
			t.Errorf("ValidName(%q) = %v, want a *FieldError on project", s, err)
		}
	}
}
