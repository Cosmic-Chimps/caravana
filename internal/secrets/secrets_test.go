package secrets

import (
	"errors"
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
	tests := []struct {
		name      string
		app       app.App
		want      Sets
		wantField string
	}{
		{"values replace, keeps keep, the rest is gone",
			twoServices([]app.Secret{{Name: "DATABASE_URL"}, {Name: "NEW", Value: value("n")}}, nil),
			Sets{"web": {"DATABASE_URL": "old-db", "NEW": "n"}}, ""},
		{"same name, different service, own value",
			twoServices([]app.Secret{{Name: "DATABASE_URL", Value: value("a")}}, []app.Secret{{Name: "DATABASE_URL"}}),
			Sets{"web": {"DATABASE_URL": "a"}, "worker": {"DATABASE_URL": "worker-db"}}, ""},
		{"an empty value is a value",
			twoServices([]app.Secret{{Name: "API_KEY", Value: value("")}}, nil),
			Sets{"web": {"API_KEY": ""}}, ""},
		{"no secrets at all", twoServices(nil, nil), Sets{}, ""},
		{"keep with nothing stored",
			twoServices(nil, []app.Secret{{Name: "API_KEY"}}),
			nil, "services[1].secrets[0].value"},
		{"over the size limit",
			twoServices([]app.Secret{{Name: "BIG", Value: value(strings.Repeat("x", MaxSize))}}, nil),
			nil, "services[0].secrets"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Build(tt.app, current)
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
	_, err := Build(a, nil)
	if err == nil || strings.Contains(err.Error(), marker) {
		t.Fatalf("Build error = %v, want one that doesn't contain the value", err)
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
