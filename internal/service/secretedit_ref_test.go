package service

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 037: the console's Variables page sets a secret as a reference. A
// typed value and a reference replace each other, and a kept Compose file's
// overlay keeps the reference (never a value) through every secret edit.
func TestSetSecretRefIf(t *testing.T) {
	ctx := context.Background()
	e, _ := refEnv(t)
	deployHealthy(t, e, withSecrets(set("TYPED", "t")))

	rec, err := e.svc.SetSecretRefIf(ctx, "whoami", "web", "DATABASE_URL", *ref("bella-prod", "DATABASE_URL"), Condition{}, app.TriggerConsole)
	if err != nil {
		t.Fatal(err)
	}
	if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
		t.Fatalf("deploy = %s (%s)", final.Status, final.Error)
	}
	if got := e.secrets.Live("whoami"); !got.Equal(secrets.Sets{"web": {"TYPED": "t", "DATABASE_URL": refSentinel}}) {
		t.Errorf("live = %v, want the typed and the resolved value", got)
	}

	// A typed value replaces the reference.
	rec, err = e.svc.SetSecretIf(ctx, "whoami", "web", "DATABASE_URL", "typed", Condition{}, app.TriggerConsole)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	stored, _ := e.svc.GetApp(ctx, "whoami")
	for _, sec := range stored.App.Services[0].Secrets {
		if sec.From != nil {
			t.Errorf("secret %s still has a reference after a typed value", sec.Name)
		}
	}

	// An unknown provider is a field error and saves nothing.
	_, err = e.svc.SetSecretRefIf(ctx, "whoami", "web", "X", *ref("nope", "X"), Condition{}, app.TriggerConsole)
	if _, ok := err.(*app.FieldError); !ok {
		t.Errorf("err = %v, want a field error", err)
	}
}

func TestSetSecretRefKeepsFile(t *testing.T) {
	ctx := context.Background()
	e, _ := refEnv(t)
	req := compose.Request{Compose: keptFile, Services: map[string]compose.Overlay{
		"web": {Secrets: []app.Secret{{Name: "TOKEN", Value: new(keptSentinel)}}},
	}}
	composeHealthy(t, e, req)

	rec, err := e.svc.SetSecretRefIf(ctx, "whoami", "web", "DATABASE_URL", *ref("bella-prod", "DATABASE_URL"), Condition{}, app.TriggerConsole)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)
	// A later edit of another secret keeps the reference in the overlay.
	rec, err = e.svc.SetSecret(ctx, "whoami", "web", "TOKEN", "k2", app.TriggerConsole)
	if err != nil {
		t.Fatal(err)
	}
	waitFinal(t, e.store, rec.Deployment.ID)

	stored, _ := e.svc.GetApp(ctx, "whoami")
	want := []app.Secret{{Name: "TOKEN"}, {Name: "DATABASE_URL", From: ref("bella-prod", "DATABASE_URL")}}
	if got := stored.Compose.Services["web"].Secrets; !reflect.DeepEqual(got, want) {
		t.Errorf("overlay secrets = %+v, want %+v", got, want)
	}
	text := mustJSON(stored)
	for _, v := range []string{refSentinel, keptSentinel, "k2"} {
		if strings.Contains(text, v) {
			t.Errorf("the stored app holds the value %q", v)
		}
	}
}
