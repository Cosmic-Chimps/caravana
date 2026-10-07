package service

import (
	"context"
	"errors"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
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
