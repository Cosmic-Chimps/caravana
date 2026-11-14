package secrets

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

func value(s string) *string { return &s }

// twoServices is an app whose web and worker both have secrets.
func twoServices(web, worker []app.Secret) app.App {
	return app.App{ID: "blog", Services: []app.Service{
		{Name: "web", Secrets: web},
		{Name: "worker", Secrets: worker},
	}}
}

func TestBuild(t *testing.T) {
	current := Sets{
		"web":    {"DATABASE_URL": "old-db", "API_KEY": "old-key"},
		"worker": {"DATABASE_URL": "worker-db"},
	}
	ref := func(provider, path string) *app.SecretRef { return &app.SecretRef{Provider: provider, Path: path} }
	tests := []struct {
		name      string
		app       app.App
		resolved  Sets
		want      Sets
		wantField string
	}{
		{"values replace, keeps keep, the rest is gone",
			twoServices([]app.Secret{{Name: "DATABASE_URL"}, {Name: "NEW", Value: value("n")}}, nil),
			nil, Sets{"web": {"DATABASE_URL": "old-db", "NEW": "n"}}, ""},
		{"same name, different service, own value",
			twoServices([]app.Secret{{Name: "DATABASE_URL", Value: value("a")}}, []app.Secret{{Name: "DATABASE_URL"}}),
			nil, Sets{"web": {"DATABASE_URL": "a"}, "worker": {"DATABASE_URL": "worker-db"}}, ""},
		{"an empty value is a value",
			twoServices([]app.Secret{{Name: "API_KEY", Value: value("")}}, nil),
			nil, Sets{"web": {"API_KEY": ""}}, ""},
		{"no secrets at all", twoServices(nil, nil), nil, Sets{}, ""},
		{"keep with nothing stored",
			twoServices(nil, []app.Secret{{Name: "API_KEY"}}),
			nil, nil, "services[1].secrets[0].value"},
		{"over the size limit",
			twoServices([]app.Secret{{Name: "BIG", Value: value(strings.Repeat("x", MaxSize))}}, nil),
			nil, nil, "services[0].secrets"},
		{"a reference takes the resolved value, not the stored one",
			twoServices([]app.Secret{{Name: "API_KEY", From: ref("bella", "KEY")}, {Name: "NEW", Value: value("n")}}, nil),
			Sets{"web": {"API_KEY": "from-bella"}}, Sets{"web": {"API_KEY": "from-bella", "NEW": "n"}}, ""},
		{"a reference with nothing resolved",
			twoServices(nil, []app.Secret{{Name: "API_KEY", From: ref("bella", "KEY")}}),
			Sets{"web": {"API_KEY": "other service"}}, nil, "services[1].secrets[0].from.path"},
		{"a resolved empty value is a value",
			twoServices([]app.Secret{{Name: "API_KEY", From: ref("bella", "KEY")}}, nil),
			Sets{"web": {"API_KEY": ""}}, Sets{"web": {"API_KEY": ""}}, ""},
		{"resolved values count against the size limit",
			twoServices([]app.Secret{{Name: "BIG", From: ref("bella", "BIG")}}, nil),
			Sets{"web": {"BIG": strings.Repeat("x", MaxSize)}}, nil, "services[0].secrets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Build(tt.app, current, tt.resolved)
			if tt.wantField != "" {
				var fe *app.FieldError
				if !errors.As(err, &fe) || fe.Field != tt.wantField {
					t.Fatalf("Build error = %v, want a field error on %s", err, tt.wantField)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("Build = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildErrorHasNoValue(t *testing.T) {
	marker := "s3cr3t-marker"
	a := twoServices([]app.Secret{{Name: "BIG", Value: value(marker + strings.Repeat("x", MaxSize))}}, nil)
	_, err := Build(a, nil, nil)
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("Build error = %v, want one that doesn't contain the value", err)
	}
}

func TestBuildReferenceErrorHasNoValue(t *testing.T) {
	marker := "s3cr3t-marker"
	a := twoServices([]app.Secret{{Name: "BIG", From: &app.SecretRef{Provider: "bella", Path: "BIG"}}}, nil)
	_, err := Build(a, nil, Sets{"web": {"BIG": marker + strings.Repeat("x", MaxSize)}})
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("Build error = %v, want one that doesn't contain the value", err)
	}
}

func TestSourceErrors(t *testing.T) {
	down, refused := Unavailable("Bella can't be reached"), Refused("Bella refused the API key")
	if !errors.Is(down, ErrSourceUnavailable) || errors.Is(down, ErrSourceRefused) {
		t.Errorf("Unavailable matches the wrong sentinel: %v", down)
	}
	if !errors.Is(refused, ErrSourceRefused) || errors.Is(refused, ErrSourceUnavailable) {
		t.Errorf("Refused matches the wrong sentinel: %v", refused)
	}
	var se *SourceError
	wrapped := fmt.Errorf("read: %w", down)
	if !errors.As(wrapped, &se) || se.Error() != "Bella can't be reached" {
		t.Errorf("errors.As = %v, %v", errors.As(wrapped, &se), se)
	}
}

func TestEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b Sets
		want bool
	}{
		{"both empty", Sets{}, nil, true},
		{"empty set counts as absent", Sets{"web": {}}, Sets{}, true},
		{"same", Sets{"web": {"A": "1"}}, Sets{"web": {"A": "1"}}, true},
		{"different value", Sets{"web": {"A": "1"}}, Sets{"web": {"A": "2"}}, false},
		{"extra service", Sets{"web": {"A": "1"}}, Sets{"web": {"A": "1"}, "worker": {"A": "1"}}, false},
		{"missing name", Sets{"web": {"A": "1", "B": "2"}}, Sets{"web": {"A": "1"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Equal(tt.b); got != tt.want {
				t.Errorf("Equal = %v, want %v", got, tt.want)
			}
			if got := tt.b.Equal(tt.a); got != tt.want {
				t.Errorf("reversed Equal = %v, want %v", got, tt.want)
			}
		})
	}
}
