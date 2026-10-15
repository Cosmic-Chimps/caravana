package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// US3: the console's set and remove are deploys that keep the other secrets.
func TestSetAndRemoveSecret(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withSecrets(set("DATABASE_URL", "db"), set("API_KEY", "k1")))

	settle := func(rec store.DeploymentRecord, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if rec.Deployment.Trigger != app.TriggerConsole {
			t.Errorf("trigger = %s, want console", rec.Deployment.Trigger)
		}
		if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
			t.Fatalf("deploy = %s (%s)", final.Status, final.Error)
		}
	}

	settle(e.svc.SetSecret(ctx, "whoami", "web", "API_KEY", "k2", app.TriggerConsole))
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"DATABASE_URL": "db", "API_KEY": "k2"}}) {
		t.Errorf("after rotating API_KEY: %v", got)
	}
	settle(e.svc.SetSecret(ctx, "whoami", "web", "NEW", "", app.TriggerConsole))
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"DATABASE_URL": "db", "API_KEY": "k2", "NEW": ""}}) {
		t.Errorf("after adding NEW: %v", got)
	}
	settle(e.svc.RemoveSecret(ctx, "whoami", "web", "API_KEY", app.TriggerConsole))
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"DATABASE_URL": "db", "NEW": ""}}) {
		t.Errorf("after removing API_KEY: %v", got)
	}

	stored, _ := e.svc.GetApp(ctx, "whoami")
	for _, s := range stored.App.Services[0].Secrets {
		if s.Value != nil {
			t.Errorf("stored secret %s has a value", s.Name)
		}
	}
}

func TestSetAndRemoveSecretErrors(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	deployHealthy(t, e, withSecrets(set("API_KEY", "k1")))

	tests := []struct {
		name      string
		call      func() error
		wantField string
		wantErr   error
	}{
		{"unknown app", func() error {
			_, err := e.svc.SetSecret(ctx, "ghost", "web", "A", "1", app.TriggerConsole)
			return err
		}, "", store.ErrNotFound},
		{"unknown service", func() error {
			_, err := e.svc.SetSecret(ctx, "whoami", "api", "A", "1", app.TriggerConsole)
			return err
		}, "service", nil},
		{"bad name", func() error {
			_, err := e.svc.SetSecret(ctx, "whoami", "web", "1A", "1", app.TriggerConsole)
			return err
		}, "services[0].secrets[1].name", nil},
		{"unknown secret", func() error {
			_, err := e.svc.RemoveSecret(ctx, "whoami", "web", "NOPE", app.TriggerConsole)
			return err
		}, "name", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tt.wantField {
				t.Errorf("err = %v, want a field error on %s", err, tt.wantField)
			}
		})
	}
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"API_KEY": "k1"}}) {
		t.Errorf("a refused change touched the secrets: %v", got)
	}
}

// Feature 015: a secret edit on an app that keeps its Compose file keeps the
// file as it is. The overlay's secret names follow the app, and the drop
// warning never appears.
func TestEditSecretsKeepsFile(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	req := compose.Request{Compose: keptFile, Services: map[string]compose.Overlay{
		"web": {Secrets: []app.Secret{{Name: "TOKEN", Value: new(keptSentinel)}}},
	}}
	composeHealthy(t, e, req)

	steps := []struct {
		name string
		do   func() (store.DeploymentRecord, error)
		want []app.Secret // the overlay's secrets for web after the step
	}{
		{"add", func() (store.DeploymentRecord, error) {
			return e.svc.SetSecret(ctx, "whoami", "web", "API_KEY", "k1", app.TriggerConsole)
		}, []app.Secret{{Name: "TOKEN"}, {Name: "API_KEY"}}},
		{"rotate", func() (store.DeploymentRecord, error) {
			return e.svc.SetSecret(ctx, "whoami", "web", "API_KEY", "k2", app.TriggerConsole)
		}, []app.Secret{{Name: "TOKEN"}, {Name: "API_KEY"}}},
		{"remove", func() (store.DeploymentRecord, error) {
			return e.svc.RemoveSecret(ctx, "whoami", "web", "TOKEN", app.TriggerConsole)
		}, []app.Secret{{Name: "API_KEY"}}},
		{"remove the last, the overlay entry goes", func() (store.DeploymentRecord, error) {
			return e.svc.RemoveSecret(ctx, "whoami", "web", "API_KEY", app.TriggerConsole)
		}, nil},
	}
	for _, tt := range steps {
		t.Run(tt.name, func(t *testing.T) {
			rec, err := tt.do()
			if err != nil {
				t.Fatal(err)
			}
			waitFinal(t, e.store, rec.Deployment.ID)
			if n := droppedFile(rec); n != 0 {
				t.Errorf("warnings = %+v, want no drop warning", rec.Warnings)
			}
			stored, err := e.svc.GetApp(ctx, "whoami")
			if err != nil {
				t.Fatal(err)
			}
			if stored.Compose == nil || stored.Compose.Compose != keptFile {
				t.Fatalf("kept file = %+v, want the same text", stored.Compose)
			}
			got, present := stored.Compose.Services["web"]
			if !reflect.DeepEqual(got.Secrets, tt.want) || present != (tt.want != nil) {
				t.Errorf("overlay for web = %+v (present %v), want secrets %+v", got, present, tt.want)
			}
			dep, _ := e.svc.GetDeployment(ctx, "whoami", rec.Deployment.ID)
			if !reflect.DeepEqual(dep.Compose, stored.Compose) {
				t.Errorf("the deployment records %+v, want the app's %+v", dep.Compose, stored.Compose)
			}
		})
	}
}

// Feature 015: the overlay's other fields survive a secret edit, and the
// request it starts from is never changed in place.
func TestWithSecretNames(t *testing.T) {
	health := &compose.Health{Path: "/up"}
	tests := []struct {
		name string
		file compose.Request
		list []app.Secret
		want map[string]compose.Overlay
	}{
		{"no overlay yet", compose.Request{Compose: "x"},
			[]app.Secret{{Name: "A"}}, map[string]compose.Overlay{"web": {Secrets: []app.Secret{{Name: "A"}}}}},
		{"keeps the rest of the overlay",
			compose.Request{Compose: "x", Services: map[string]compose.Overlay{"web": {Health: health, Secrets: []app.Secret{{Name: "A"}}}}},
			nil, map[string]compose.Overlay{"web": {Health: health}}},
		{"empty list on an empty overlay",
			compose.Request{Compose: "x", Services: map[string]compose.Overlay{"web": {Secrets: []app.Secret{{Name: "A"}}}}},
			nil, nil},
		{"other services untouched, values never kept",
			compose.Request{Compose: "x", Services: map[string]compose.Overlay{"db": {Health: health}}},
			[]app.Secret{{Name: "A", Value: new("never kept")}},
			map[string]compose.Overlay{"db": {Health: health}, "web": {Secrets: []app.Secret{{Name: "A"}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := tt.file.WithoutSecretValues()
			got := withSecretNames(tt.file, "web", tt.list)
			if !reflect.DeepEqual(got.Services, tt.want) {
				t.Errorf("Services = %+v, want %+v", got.Services, tt.want)
			}
			if !reflect.DeepEqual(tt.file.WithoutSecretValues(), orig) {
				t.Errorf("the input changed: %+v, was %+v", tt.file, orig)
			}
		})
	}
}
