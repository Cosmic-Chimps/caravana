package deploy

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
)

// registrySecret is a password no error may contain.
const registrySecret = "s3cr3t-SENTINEL-registry-pw"

// nomadPath is Nomad's rule for a variable path.
var nomadPath = regexp.MustCompile(`^[a-zA-Z0-9-_~/]{1,128}$`)

func TestRegistryVariablePath(t *testing.T) {
	tests := map[string]string{
		"docker.io":                       "caravana/registries/docker~io",
		"ghcr.io":                         "caravana/registries/ghcr~io",
		"registry.example.com:5000":       "caravana/registries/registry~example~com_5000",
		"127.0.0.1:5000":                  "caravana/registries/127~0~0~1_5000",
		"localhost":                       "caravana/registries/localhost",
		"[::1]:5000":                      "caravana/registries/__1_5000",
		"my-registry.example.com":         "caravana/registries/my-registry~example~com",
		"registry.example.com":            "caravana/registries/registry~example~com",
		"registry.example.com.5000.local": "caravana/registries/registry~example~com~5000~local",
	}
	seen := map[string]string{}
	for host, want := range tests {
		got := RegistryVariablePath(host)
		if got != want {
			t.Errorf("RegistryVariablePath(%q) = %q, want %q", host, got, want)
		}
		if !nomadPath.MatchString(got) {
			t.Errorf("RegistryVariablePath(%q) = %q, which Nomad would refuse as a variable path", host, got)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("%q and %q share the path %q", host, other, got)
		}
		seen[got] = host
	}
}

func TestRegistrySecrets(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	const host = "registry.example.com:5000"

	if _, _, ok, err := d.RegistryCredentials(ctx, host); ok || err != nil {
		t.Fatalf("RegistryCredentials() before a save = ok %v, %v; want not ok, nil", ok, err)
	}
	if err := d.SaveRegistrySecret(ctx, host, "deploy", registrySecret); err != nil {
		t.Fatalf("SaveRegistrySecret() = %v", err)
	}
	if got := nomad.VariablePaths(); len(got) != 1 || got[0] != "caravana/registries/registry~example~com_5000" {
		t.Errorf("variables = %v, want the one registry variable", got)
	}
	user, pass, ok, err := d.RegistryCredentials(ctx, host)
	if user != "deploy" || pass != registrySecret || !ok || err != nil {
		t.Errorf("RegistryCredentials() = %q, (password), %v, %v; want deploy, the password, true, nil", user, ok, err)
	}

	if err := d.SaveRegistrySecret(ctx, host, "deploy", "new-"+registrySecret); err != nil {
		t.Fatal(err)
	}
	if _, pass, _, _ := d.RegistryCredentials(ctx, host); pass != "new-"+registrySecret {
		t.Error("a second save didn't replace the password")
	}

	if err := d.DeleteRegistrySecret(ctx, host); err != nil {
		t.Fatalf("DeleteRegistrySecret() = %v", err)
	}
	if _, _, ok, err := d.RegistryCredentials(ctx, host); ok || err != nil {
		t.Errorf("RegistryCredentials() after a delete = ok %v, %v; want not ok, nil", ok, err)
	}
	if err := d.DeleteRegistrySecret(ctx, host); err != nil {
		t.Errorf("DeleteRegistrySecret() of a host with none = %v, want nil", err)
	}

	t.Run("a refused token is an error, not 'no credentials'", func(t *testing.T) {
		nomad.FailVariables(403)
		defer nomad.FailVariables(0)
		if _, _, ok, err := d.RegistryCredentials(ctx, host); ok || !errors.Is(err, ErrNomadUnavailable) {
			t.Errorf("RegistryCredentials() = ok %v, %v; want ErrNomadUnavailable", ok, err)
		}
	})
}

// privateRegistry makes the fake registry want a login (as a Bearer token
// service, or Basic), puts an image on it, and returns an app using it and the
// digests that image pins to.
func privateRegistry(t *testing.T, reg *registrytest.Server, bearer bool) (a app.App, digests map[string]string) {
	t.Helper()
	reg.RequireToken(bearer)
	reg.RequireLogin("deploy", registrySecret)
	reg.AddIndex("acme/shop", "v1", whoamiDigest)
	a = whoami()
	a.Services[0].Image = reg.Host() + "/acme/shop:v1"
	return a, map[string]string{"web": reg.Host() + "/acme/shop@" + whoamiDigest}
}

func TestResolveWithStoredCredentials(t *testing.T) {
	ctx := context.Background()

	for _, mode := range []struct {
		name   string
		bearer bool
	}{{"bearer", true}, {"basic", false}} {
		t.Run(mode.name, func(t *testing.T) {
			d, reg, _ := newTestDeployer(t)
			a, digests := privateRegistry(t, reg, mode.bearer)

			// 1. Nothing stored: missing or private, with where to add them.
			_, _, err := d.Resolve(ctx, a)
			var ie *ImageError
			if !errors.As(err, &ie) || ie.Field != "services[0].image" || !errors.Is(err, ErrImageNotFound) || errors.Is(err, ErrRegistryAuth) {
				t.Fatalf("Resolve() without credentials = %v, want an *ImageError matching ErrImageNotFound only", err)
			}
			if want := "add credentials for " + reg.Host() + " in Settings › Registries"; !strings.Contains(err.Error(), want) {
				t.Errorf("error %q doesn't say %q", err, want)
			}

			// 2. A wrong secret: the registry's refusal.
			if err := d.SaveRegistrySecret(ctx, reg.Host(), "deploy", "wrong-"+registrySecret); err != nil {
				t.Fatal(err)
			}
			_, _, err = d.Resolve(ctx, a)
			if !errors.As(err, &ie) || ie.Field != "services[0].image" || ie.Image != a.Services[0].Image {
				t.Fatalf("Resolve() with a wrong secret = %v, want an *ImageError on the image", err)
			}
			if !errors.Is(err, ErrRegistryAuth) || !errors.Is(err, registry.ErrUnauthorized) ||
				errors.Is(err, ErrImageNotFound) || errors.Is(err, ErrRegistryUnavailable) {
				t.Errorf("error %v should match ErrRegistryAuth and registry.ErrUnauthorized only", err)
			}
			if strings.Contains(err.Error(), registrySecret) {
				t.Errorf("error %q contains the secret", err)
			}
			err = d.CheckDigests(ctx, a, digests)
			if !errors.As(err, &ie) || !errors.Is(err, ErrRegistryAuth) {
				t.Errorf("CheckDigests() with a wrong secret = %v, want an *ImageError matching ErrRegistryAuth", err)
			}

			// 3. The right secret.
			if err := d.SaveRegistrySecret(ctx, reg.Host(), "deploy", registrySecret); err != nil {
				t.Fatal(err)
			}
			got, _, err := d.Resolve(ctx, a)
			if want := reg.Host() + "/acme/shop@" + whoamiDigest; err != nil || got["web"] != want {
				t.Fatalf("Resolve() with the right secret = %v, %v; want web = %s", got, err, want)
			}
			if err := d.CheckDigests(ctx, a, digests); err != nil {
				t.Errorf("CheckDigests() with the right secret = %v", err)
			}
		})
	}

	t.Run("a deleted image still reads as not found with credentials", func(t *testing.T) {
		d, reg, _ := newTestDeployer(t)
		a, digests := privateRegistry(t, reg, true)
		if err := d.SaveRegistrySecret(ctx, reg.Host(), "deploy", registrySecret); err != nil {
			t.Fatal(err)
		}
		reg.Delete("acme/shop", whoamiDigest)
		if err := d.CheckDigests(ctx, a, digests); !errors.Is(err, ErrImageNotFound) || errors.Is(err, ErrRegistryAuth) {
			t.Errorf("CheckDigests() = %v, want ErrImageNotFound", err)
		}
	})

	t.Run("a public image needs no variable", func(t *testing.T) {
		d, reg, nomad := newTestDeployer(t)
		reg.AddIndex("traefik/whoami", "latest", whoamiDigest)
		if _, _, err := d.Resolve(ctx, whoami()); err != nil {
			t.Fatalf("Resolve() = %v", err)
		}
		if got := nomad.VariablePaths(); len(got) != 0 {
			t.Errorf("variables = %v, want none for a public image", got)
		}
	})

	t.Run("credentials that can't be read are the registry being unavailable", func(t *testing.T) {
		d, reg, nomad := newTestDeployer(t)
		a, _ := privateRegistry(t, reg, true)
		nomad.FailVariables(403)
		_, _, err := d.Resolve(ctx, a)
		if !errors.Is(err, ErrRegistryUnavailable) || errors.Is(err, ErrImageNotFound) {
			t.Errorf("Resolve() = %v, want ErrRegistryUnavailable", err)
		}
	})

	t.Run("a Client of the caller's own keeps its Credentials", func(t *testing.T) {
		own := func(context.Context, string) (string, string, bool, error) { return "x", "y", true, nil }
		client := &registry.Client{Credentials: own}
		_, _, nomad := newTestDeployer(t)
		nc, err := NewNomadClient(nomad.Addr(), "")
		if err != nil {
			t.Fatal(err)
		}
		New(nc, client, labSettings)
		if u, _, _, _ := client.Credentials(context.Background(), "x"); u != "x" {
			t.Error("New replaced the caller's Credentials")
		}
	})
}

func TestTestRegistry(t *testing.T) {
	ctx := context.Background()
	d, reg, _ := newTestDeployer(t)
	reg.RequireToken(true)
	reg.RequireLogin("deploy", registrySecret)

	err := d.TestRegistry(ctx, reg.Host())
	if err == nil || !strings.Contains(err.Error(), "no credentials are stored") {
		t.Errorf("TestRegistry() with nothing stored = %v, want 'no credentials are stored'", err)
	}

	if err := d.SaveRegistrySecret(ctx, reg.Host(), "deploy", "wrong-"+registrySecret); err != nil {
		t.Fatal(err)
	}
	err = d.TestRegistry(ctx, reg.Host())
	if !errors.Is(err, registry.ErrUnauthorized) {
		t.Errorf("TestRegistry() with a wrong secret = %v, want registry.ErrUnauthorized", err)
	}
	if err != nil && strings.Contains(err.Error(), registrySecret) {
		t.Errorf("error %q contains the secret", err)
	}

	if err := d.SaveRegistrySecret(ctx, reg.Host(), "deploy", registrySecret); err != nil {
		t.Fatal(err)
	}
	if err := d.TestRegistry(ctx, reg.Host()); err != nil {
		t.Errorf("TestRegistry() with the right secret = %v, want nil", err)
	}
}
