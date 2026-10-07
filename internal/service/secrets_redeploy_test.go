package service

import (
	"context"
	"errors"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// US2: redeploys deploy the stored app, which knows names only, so they keep
// every value.
func TestRedeployKeepsSecrets(t *testing.T) {
	ctx := context.Background()
	want := secrets.Sets{"web": {"API_KEY": "k1", "DATABASE_URL": "db"}}

	t.Run("empty-body redeploy", func(t *testing.T) {
		e := newEnv(t)
		deployHealthy(t, e, withSecrets(set("API_KEY", "k1"), set("DATABASE_URL", "db")))
		rec, err := e.svc.Redeploy(ctx, "whoami", app.TriggerManual)
		if err != nil {
			t.Fatalf("Redeploy: %v", err)
		}
		if final := waitFinal(t, e.store, rec.Deployment.ID); final.Status != store.StatusHealthy {
			t.Fatalf("redeploy = %s (%s)", final.Status, final.Error)
		}
		if got := e.secrets.Live("whoami"); !got.Equal(want) {
			t.Errorf("live = %v, want %v", got, want)
		}
	})

	t.Run("webhook with a moved tag, ten times", func(t *testing.T) {
		e := newEnv(t)
		deployHealthy(t, e, withSecrets(set("API_KEY", "k1"), set("DATABASE_URL", "db")))
		tok, err := e.svc.CreateWebhookToken(ctx, "whoami")
		if err != nil {
			t.Fatal(err)
		}
		for i := range 10 {
			digest := whoamiDigest
			if i%2 == 0 {
				digest = movedDigest
			}
			e.registry.AddIndex("traefik/whoami", "latest", digest)
			res, err := e.svc.Webhook(ctx, "whoami", tok.Token)
			if err != nil || !res.Changed {
				t.Fatalf("webhook %d = changed %v, %v", i, res.Changed, err)
			}
			if final := waitFinal(t, e.store, res.Deployment.Deployment.ID); final.Status != store.StatusHealthy {
				t.Fatalf("webhook deploy %d = %s (%s)", i, final.Status, final.Error)
			}
			if got := e.secrets.Live("whoami"); !got.Equal(want) {
				t.Fatalf("live after webhook %d = %v, want %v", i, got, want)
			}
		}
	})

	t.Run("a value gone from the store fails loudly", func(t *testing.T) {
		e := newEnv(t)
		deployHealthy(t, e, withSecrets(set("API_KEY", "k1")))
		e.secrets.SetLive("whoami", secrets.Sets{}) // removed outside Caravana
		regs := e.nomad.Registrations()

		_, err := e.svc.Redeploy(ctx, "whoami", app.TriggerWebhook)
		var fe *app.FieldError
		if !errors.As(err, &fe) || fe.Field != "services[0].secrets[0].value" {
			t.Fatalf("Redeploy = %v, want a field error on services[0].secrets[0].value", err)
		}
		if e.nomad.Registrations() != regs {
			t.Error("Nomad got a job whose secret has no value")
		}
	})
}
