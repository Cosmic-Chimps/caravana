package deploy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestNomadDiscovery(t *testing.T) {
	ctx := context.Background()
	const sentinel = "sentinel-nomad-text-0038"
	doc := `{"issuer":"https://console.example.com"}`

	t.Run("the document comes back as Nomad sent it", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetDiscovery(DiscoveryConfigPath, 200, []byte(doc))
		nomad.SetDiscovery(DiscoveryJWKSPath, 200, []byte(`{"keys":[]}`))
		got, err := d.NomadDiscovery(ctx, DiscoveryConfigPath)
		if err != nil || string(got) != doc {
			t.Errorf("config = %q, %v", got, err)
		}
		got, err = d.NomadDiscovery(ctx, DiscoveryJWKSPath)
		if err != nil || string(got) != `{"keys":[]}` {
			t.Errorf("jwks = %q, %v", got, err)
		}
	})
	t.Run("only the two discovery paths are read", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetDiscovery("/.well-known/other", 200, []byte(doc))
		for _, p := range []string{"/.well-known/other", "/v1/status/leader", "", "/.well-known/jwks.json/../x"} {
			if _, err := d.NomadDiscovery(ctx, p); err == nil {
				t.Errorf("NomadDiscovery(%q) read something", p)
			}
		}
	})
	t.Run("a refusal is ErrNomadUnavailable and never quotes the body", func(t *testing.T) {
		for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
			d, _, nomad := newTestDeployer(t)
			nomad.SetDiscovery(DiscoveryConfigPath, status, []byte(sentinel))
			_, err := d.NomadDiscovery(ctx, DiscoveryConfigPath)
			if !errors.Is(err, ErrNomadUnavailable) || strings.Contains(err.Error(), sentinel) {
				t.Errorf("status %d: %v", status, err)
			}
		}
	})
	t.Run("nothing set is a 404 from Nomad", func(t *testing.T) {
		d, _, _ := newTestDeployer(t)
		if _, err := d.NomadDiscovery(ctx, DiscoveryConfigPath); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("invalid JSON and a body over 64 KiB are ErrDiscoveryInvalid", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.SetDiscovery(DiscoveryConfigPath, 200, []byte("<html>"+sentinel))
		nomad.SetDiscovery(DiscoveryJWKSPath, 200, []byte(`{"x":"`+strings.Repeat("a", 64<<10)+`"}`))
		for _, p := range []string{DiscoveryConfigPath, DiscoveryJWKSPath} {
			_, err := d.NomadDiscovery(ctx, p)
			if !errors.Is(err, ErrDiscoveryInvalid) || strings.Contains(err.Error(), sentinel) {
				t.Errorf("%s: %v", p, err)
			}
		}
	})
	t.Run("exactly 64 KiB is allowed", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		body := `{"x":"` + strings.Repeat("a", 64<<10-8) + `"}`
		if len(body) != 64<<10 {
			t.Fatalf("test body is %d bytes", len(body))
		}
		nomad.SetDiscovery(DiscoveryConfigPath, 200, []byte(body))
		if got, err := d.NomadDiscovery(ctx, DiscoveryConfigPath); err != nil || len(got) != 64<<10 {
			t.Errorf("len %d, %v", len(got), err)
		}
	})
	t.Run("Nomad down is ErrNomadUnavailable", func(t *testing.T) {
		d, _, nomad := newTestDeployer(t)
		nomad.Down()
		if _, err := d.NomadDiscovery(ctx, DiscoveryJWKSPath); !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("err = %v", err)
		}
	})
}
