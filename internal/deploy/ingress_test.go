package deploy

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/nomad/api"
)

func TestIngressFeatures(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	// No job named traefik: no features, no error.
	if got, err := d.IngressFeatures(ctx); err != nil || got != (IngressFeatures{}) {
		t.Fatalf("no job: %+v, %v; want none and no error", got, err)
	}
	// A job from before the feature, even one with the console's route.
	nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true")
	if got, err := d.IngressFeatures(ctx); err != nil || got.Certificates {
		t.Errorf("older job: %+v, %v; want no certificates", got, err)
	}
	nomad.SetTraefikConsoleRoute("traefik:v3.7.13")
	if got, err := d.IngressFeatures(ctx); err != nil || got.Certificates {
		t.Errorf("job with the console's route only: %+v, %v", got, err)
	}
	nomad.SetTraefikCertificates("traefik:v3.7.13", "--configFile=/secrets/traefik.yml")
	if got, err := d.IngressFeatures(ctx); err != nil || !got.Certificates {
		t.Errorf("job with the meta: %+v, %v; want certificates", got, err)
	}
	// The console's route and the certificates are separate facts.
	if got, err := d.ConsoleRouteSupported(ctx); err != nil || got {
		t.Errorf("ConsoleRouteSupported on the new job = %v, %v; want it unchanged (false: no template)", got, err)
	}

	nomad.Down()
	defer nomad.Up()
	if _, err := d.IngressFeatures(ctx); !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("Nomad down: err = %v, want ErrNomadUnavailable", err)
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	nomad.Up()
	if _, err := d.IngressFeatures(cctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: err = %v, want context.Canceled", err)
	}
}

func TestIngressFeaturesMetaValue(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	// Only "1" turns it on.
	for _, v := range []string{"", "0", "true", "yes"} {
		nomad.AddForeignJob(&api.Job{ID: new("traefik"), Name: new("traefik"), Meta: map[string]string{"caravana_certificates": v}})
		if got, err := d.IngressFeatures(ctx); err != nil || got.Certificates {
			t.Errorf("meta %q: %+v, %v; want no certificates", v, got, err)
		}
	}
}
