package service

import (
	"context"
	"errors"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func TestVersion(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	rec, err := e.svc.Deploy(ctx, whoami(), app.TriggerManual)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)

	other := whoami()
	other.ID = "other"
	if _, err := e.store.SaveApp(ctx, other, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateDeployment(ctx, "whoami", app.Deployment{ID: "old", Trigger: app.TriggerManual, Digests: map[string]string{}}, nil, nil, ""); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, app, id string
		want          error
	}{
		{"recorded", "whoami", rec.Deployment.ID, nil},
		{"unknown", "whoami", "nope", store.ErrNotFound},
		{"another app's", "other", rec.Deployment.ID, store.ErrNotFound},
		{"from before definitions", "whoami", "old", ErrDefinitionNotRecorded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.svc.version(ctx, tt.app, tt.id)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if tt.want == nil && (got.Definition == nil || got.Definition.ID != "whoami") {
				t.Errorf("Definition = %+v, want the whoami app", got.Definition)
			}
		})
	}
}
