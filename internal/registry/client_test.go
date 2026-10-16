package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/registry/registrytest"
)

const otherDigest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// newClient returns a client pointed at fake, with retries fast enough for
// tests.
func newClient(fake *registrytest.Server) *Client {
	return &Client{Endpoints: fake.Endpoints(), retryDelays: []time.Duration{time.Millisecond, time.Millisecond}}
}

func mustParse(t *testing.T, image string) Reference {
	t.Helper()
	ref, err := Parse(image)
	if err != nil {
		t.Fatalf("Parse(%q): %v", image, err)
	}
	return ref
}

func TestResolve(t *testing.T) {
	ctx := context.Background()

	t.Run("multi-arch tag pins the index", func(t *testing.T) {
		fake := registrytest.New(t)
		fake.AddIndex("traefik/whoami", "latest", testDigest)
		got, err := newClient(fake).Resolve(ctx, mustParse(t, "traefik/whoami:latest"))
		if err != nil || got != "traefik/whoami@"+testDigest {
			t.Errorf("Resolve() = %q, %v; want the index digest", got, err)
		}
	})

	t.Run("single-arch tag pins the manifest", func(t *testing.T) {
		fake := registrytest.New(t)
		fake.AddManifest("acme/shop", "main", otherDigest)
		got, err := newClient(fake).Resolve(ctx, mustParse(t, "ghcr.io/acme/shop:main"))
		if err != nil || got != "ghcr.io/acme/shop@"+otherDigest {
			t.Errorf("Resolve() = %q, %v", got, err)
		}
	})

	t.Run("library image", func(t *testing.T) {
		fake := registrytest.New(t)
		fake.AddIndex("library/nginx", "latest", testDigest)
		got, err := newClient(fake).Resolve(ctx, mustParse(t, "nginx"))
		if err != nil || got != "nginx@"+testDigest {
			t.Errorf("Resolve() = %q, %v", got, err)
		}
	})

	t.Run("token handshake", func(t *testing.T) {
		fake := registrytest.New(t)
		fake.RequireToken(true)
		fake.AddIndex("traefik/whoami", "latest", testDigest)
		got, err := newClient(fake).Resolve(ctx, mustParse(t, "traefik/whoami:latest"))
		if err != nil || got != "traefik/whoami@"+testDigest {
			t.Errorf("Resolve() = %q, %v", got, err)
		}
		if n := fake.Requests(); n != 2 {
			t.Errorf("manifest requests = %d, want 2 (challenge, then with the token)", n)
		}
	})

	t.Run("digest needs no request", func(t *testing.T) {
		fake := registrytest.New(t)
		got, err := newClient(fake).Resolve(ctx, mustParse(t, "traefik/whoami:v1@"+testDigest))
		if err != nil || got != "traefik/whoami@"+testDigest {
			t.Errorf("Resolve() = %q, %v", got, err)
		}
		if n := fake.Requests(); n != 0 {
			t.Errorf("requests = %d, want 0", n)
		}
	})

	t.Run("no digest header: hash the body", func(t *testing.T) {
		fake := registrytest.New(t)
		want := fake.AddWithoutDigestHeader("traefik/whoami", "latest")
		got, err := newClient(fake).Resolve(ctx, mustParse(t, "traefik/whoami"))
		if err != nil || got != "traefik/whoami@"+want {
			t.Errorf("Resolve() = %q, %v; want the body's digest %s", got, err, want)
		}
	})

	t.Run("bad digest header", func(t *testing.T) {
		fake := registrytest.New(t)
		fake.AddBadDigestHeader("traefik/whoami", "latest")
		_, err := newClient(fake).Resolve(ctx, mustParse(t, "traefik/whoami"))
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("Resolve() error = %v, want ErrUnavailable", err)
		}
	})
}

func TestResolveErrors(t *testing.T) {
	ctx := context.Background()
	ref := func(t *testing.T) Reference { return mustParse(t, "traefik/whoami:nope") }

	tests := []struct {
		name         string
		setup        func(fake *registrytest.Server)
		want         error
		wantMsg      string
		wantRequests int // manifest requests; 0 means don't check
	}{
		{name: "404", setup: func(f *registrytest.Server) {}, want: ErrNotFound, wantMsg: "or private"},
		{name: "401 after a valid token", setup: func(f *registrytest.Server) {
			f.RequireToken(true)
			f.Fail("traefik/whoami", "nope", http.StatusUnauthorized)
		}, want: ErrNotFound, wantMsg: "or private"},
		{name: "token refused with 401", setup: func(f *registrytest.Server) {
			f.RequireToken(true)
			f.FailToken(http.StatusUnauthorized)
		}, want: ErrNotFound, wantMsg: "or private"},
		{name: "token refused with 403", setup: func(f *registrytest.Server) {
			f.RequireToken(true)
			f.FailToken(http.StatusForbidden)
		}, want: ErrNotFound, wantMsg: "or private"},
		{name: "429", setup: func(f *registrytest.Server) {
			f.Fail("traefik/whoami", "nope", http.StatusTooManyRequests)
		}, want: ErrUnavailable, wantMsg: "rate-limiting", wantRequests: 1},
		{name: "500 is retried twice", setup: func(f *registrytest.Server) {
			f.Fail("traefik/whoami", "nope", http.StatusInternalServerError)
		}, want: ErrUnavailable, wantMsg: "500", wantRequests: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := registrytest.New(t)
			tc.setup(fake)
			_, err := newClient(fake).Resolve(ctx, ref(t))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Resolve() error = %v, want %v", err, tc.want)
			}
			for _, s := range []string{tc.wantMsg, "docker.io", "traefik/whoami:nope"} {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not mention %q", err, s)
				}
			}
			if strings.Contains(err.Error(), "test-token") {
				t.Errorf("error %q contains a registry token", err)
			}
			if tc.wantRequests != 0 && fake.Requests() != tc.wantRequests {
				t.Errorf("manifest requests = %d, want %d", fake.Requests(), tc.wantRequests)
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		c := &Client{Endpoints: map[string]string{"docker.io": url}, retryDelays: []time.Duration{time.Millisecond}}
		_, err := c.Resolve(ctx, ref(t))
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("Resolve() error = %v, want ErrUnavailable", err)
		}
	})

	t.Run("budget runs out", func(t *testing.T) {
		fake := registrytest.New(t)
		fake.AddIndex("traefik/whoami", "nope", testDigest)
		release := fake.Hold("traefik/whoami", "nope")
		defer release()
		ctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		_, err := newClient(fake).Resolve(ctx, ref(t))
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("Resolve() error = %v, want ErrUnavailable", err)
		}
	})
}

func TestExists(t *testing.T) {
	ctx := context.Background()
	fake := registrytest.New(t)
	fake.AddIndex("traefik/whoami", "latest", testDigest)
	fake.AddManifest("acme/shop", "main", otherDigest)
	fake.Delete("acme/shop", otherDigest)
	fake.AddIndex("acme/flaky", "v1", testDigest)
	fake.Fail("acme/flaky", testDigest, http.StatusServiceUnavailable)

	tests := []struct {
		name, image string
		want        error
	}{
		{"pinned image still there", "traefik/whoami@" + testDigest, nil},
		{"pinned image deleted", "ghcr.io/acme/shop@" + otherDigest, ErrNotFound},
		{"never pushed", "traefik/whoami@" + otherDigest, ErrNotFound},
		{"registry down", "acme/flaky@" + testDigest, ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newClient(fake).Exists(ctx, mustParse(t, tt.image))
			if !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
				t.Errorf("Exists(%s) = %v, want %v", tt.image, err, tt.want)
			}
		})
	}

	t.Run("token handshake", func(t *testing.T) {
		fake.RequireToken(true)
		defer fake.RequireToken(false)
		if err := newClient(fake).Exists(ctx, mustParse(t, "traefik/whoami@"+testDigest)); err != nil {
			t.Errorf("Exists = %v, want nil", err)
		}
	})

	t.Run("a tag is not a digest", func(t *testing.T) {
		if err := newClient(fake).Exists(ctx, mustParse(t, "traefik/whoami:latest")); err == nil {
			t.Error("Exists of a tag = nil, want an error")
		}
	})
}

// sentinelPass is a secret no error, URL or log line may ever contain.
const sentinelPass = "s3cr3t-SENTINEL-pw"

// storedCredentials returns a Client.Credentials that answers user and pass
// for any host, and the hosts it was asked about.
func storedCredentials(user, pass string) (fn func(context.Context, string) (string, string, bool, error), asked *[]string) {
	asked = new([]string)
	return func(_ context.Context, host string) (string, string, bool, error) {
		*asked = append(*asked, host)
		return user, pass, true, nil
	}, asked
}

// noCredentials is a Client.Credentials with nothing stored.
func noCredentials(context.Context, string) (string, string, bool, error) {
	return "", "", false, nil
}

// assertNoSecret fails if err's text holds the secret, or its Basic form.
func assertNoSecret(t *testing.T, err error, user, pass string) {
	t.Helper()
	if err == nil {
		return
	}
	basic := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	for _, secret := range []string{pass, basic} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error %q contains a credential", err)
		}
	}
}

// loginFake returns a fake private registry that wants user and sentinelPass,
// as a Bearer token service (bearer) or a Basic login, and has an image on it.
func loginFake(t *testing.T, bearer bool) *registrytest.Server {
	t.Helper()
	fake := registrytest.New(t)
	fake.RequireToken(bearer)
	fake.RequireLogin("deploy", sentinelPass)
	fake.AddIndex("acme/shop", "v1", testDigest)
	return fake
}

func TestResolveWithCredentials(t *testing.T) {
	ctx := context.Background()

	for _, mode := range []struct {
		name   string
		bearer bool
	}{{"bearer", true}, {"basic", false}} {
		t.Run(mode.name+": right credentials pin the image", func(t *testing.T) {
			fake := loginFake(t, mode.bearer)
			c := newClient(fake)
			var asked *[]string
			c.Credentials, asked = storedCredentials("deploy", sentinelPass)
			image := fake.Host() + "/acme/shop:v1"
			got, err := c.Resolve(ctx, mustParse(t, image))
			if want := fake.Host() + "/acme/shop@" + testDigest; err != nil || got != want {
				t.Fatalf("Resolve() = %q, %v; want %q", got, err, want)
			}
			if len(*asked) != 1 || (*asked)[0] != fake.Host() {
				t.Errorf("credentials asked for %v, want the registry host %q once", *asked, fake.Host())
			}
			if n := fake.Requests(); n != 2 {
				t.Errorf("manifest requests = %d, want 2 (challenge, then logged in)", n)
			}
			if mode.bearer {
				if scopes := fake.TokenScopes(); len(scopes) != 1 || scopes[0] != "repository:acme/shop:pull" {
					t.Errorf("token scopes = %q, want the repository's pull scope", scopes)
				}
			}
		})

		t.Run(mode.name+": wrong credentials are ErrUnauthorized", func(t *testing.T) {
			fake := loginFake(t, mode.bearer)
			c := newClient(fake)
			c.Credentials, _ = storedCredentials("deploy", "wrong-"+sentinelPass)
			_, err := c.Resolve(ctx, mustParse(t, fake.Host()+"/acme/shop:v1"))
			if !errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNotFound) {
				t.Fatalf("Resolve() error = %v, want ErrUnauthorized only", err)
			}
			assertNoSecret(t, err, "deploy", "wrong-"+sentinelPass)
		})

		t.Run(mode.name+": no stored credentials say where to add them", func(t *testing.T) {
			for name, creds := range map[string]func(context.Context, string) (string, string, bool, error){
				"nothing stored": noCredentials,
				"no Credentials": nil,
			} {
				fake := loginFake(t, mode.bearer)
				c := newClient(fake)
				c.Credentials = creds
				_, err := c.Resolve(ctx, mustParse(t, fake.Host()+"/acme/shop:v1"))
				want := "image not found, or private: add credentials for " + fake.Host() + " in Settings › Registries"
				if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), want) {
					t.Errorf("%s: Resolve() error = %v, want ErrNotFound containing %q", name, err, want)
				}
			}
		})

		t.Run(mode.name+": Exists uses the credentials too", func(t *testing.T) {
			fake := loginFake(t, mode.bearer)
			c := newClient(fake)
			c.Credentials, _ = storedCredentials("deploy", sentinelPass)
			ref := mustParse(t, fake.Host()+"/acme/shop@"+testDigest)
			if err := c.Exists(ctx, ref); err != nil {
				t.Errorf("Exists() = %v, want nil", err)
			}
			c.Credentials, _ = storedCredentials("deploy", "wrong")
			if err := c.Exists(ctx, ref); !errors.Is(err, ErrUnauthorized) {
				t.Errorf("Exists() with wrong credentials = %v, want ErrUnauthorized", err)
			}
		})
	}

	t.Run("a public image never asks for credentials", func(t *testing.T) {
		fake := registrytest.New(t)
		fake.AddIndex("acme/shop", "v1", testDigest)
		c := newClient(fake)
		var asked *[]string
		c.Credentials, asked = storedCredentials("deploy", sentinelPass)
		if _, err := c.Resolve(ctx, mustParse(t, fake.Host()+"/acme/shop:v1")); err != nil {
			t.Fatalf("Resolve() = %v", err)
		}
		if len(*asked) != 0 {
			t.Errorf("credentials asked for %v, want no lookup for a public image", *asked)
		}
	})

	t.Run("a public token service still gets an anonymous pull when nothing is stored", func(t *testing.T) {
		fake := registrytest.New(t)
		fake.RequireToken(true)
		fake.AddIndex("traefik/whoami", "latest", testDigest)
		c := newClient(fake)
		c.Credentials = noCredentials
		if _, err := c.Resolve(ctx, mustParse(t, "traefik/whoami")); err != nil {
			t.Errorf("Resolve() = %v", err)
		}
	})

	t.Run("a refused manifest with credentials is ErrUnauthorized", func(t *testing.T) {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			fake := loginFake(t, false)
			fake.Fail("acme/shop", "v2", status)
			c := newClient(fake)
			c.Credentials, _ = storedCredentials("deploy", sentinelPass)
			_, err := c.Resolve(ctx, mustParse(t, fake.Host()+"/acme/shop:v2"))
			if !errors.Is(err, ErrUnauthorized) {
				t.Errorf("status %d: Resolve() error = %v, want ErrUnauthorized", status, err)
			}
		}
	})

	t.Run("a missing tag with credentials doesn't ask for credentials again", func(t *testing.T) {
		fake := loginFake(t, true)
		c := newClient(fake)
		c.Credentials, _ = storedCredentials("deploy", sentinelPass)
		_, err := c.Resolve(ctx, mustParse(t, fake.Host()+"/acme/shop:nope"))
		if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), "add credentials") {
			t.Errorf("Resolve() error = %v, want ErrNotFound without the add-credentials hint", err)
		}
	})

	t.Run("the stored credentials can't be read", func(t *testing.T) {
		fake := loginFake(t, false)
		c := newClient(fake)
		c.Credentials = func(context.Context, string) (string, string, bool, error) {
			return "", "", false, errors.New("nomad is down")
		}
		_, err := c.Resolve(ctx, mustParse(t, fake.Host()+"/acme/shop:v1"))
		if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "nomad is down") {
			t.Errorf("Resolve() error = %v, want ErrUnavailable naming the cause", err)
		}
	})

	t.Run("credentials never go to a plain-HTTP token service of an HTTPS registry", func(t *testing.T) {
		var tokenHits atomic.Int32
		tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenHits.Add(1)
			_, _ = w.Write([]byte(`{"token":"t"}`))
		}))
		defer tokenSrv.Close()
		regSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+tokenSrv.URL+`/token",service="x"`)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer regSrv.Close()
		c := &Client{HTTP: regSrv.Client(), Endpoints: map[string]string{"registry.example.com": regSrv.URL}}
		c.Credentials, _ = storedCredentials("deploy", sentinelPass)
		_, err := c.Resolve(ctx, mustParse(t, "registry.example.com/acme/shop:v1"))
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("Resolve() error = %v, want ErrUnavailable", err)
		}
		if n := tokenHits.Load(); n != 0 {
			t.Errorf("the plain-HTTP token service got %d requests, want 0", n)
		}
		assertNoSecret(t, err, "deploy", sentinelPass)
	})

	t.Run("no error holds the secret", func(t *testing.T) {
		const user = "deploy"
		scenarios := map[string]func(*registrytest.Server){
			"token refused": func(f *registrytest.Server) { f.FailToken(http.StatusForbidden) },
			"manifest 429":  func(f *registrytest.Server) { f.Fail("acme/shop", "v1", http.StatusTooManyRequests) },
			"manifest 500":  func(f *registrytest.Server) { f.Fail("acme/shop", "v1", http.StatusInternalServerError) },
			"manifest 404":  func(f *registrytest.Server) { f.Fail("acme/shop", "v1", http.StatusNotFound) },
		}
		for name, setup := range scenarios {
			fake := loginFake(t, true)
			setup(fake)
			c := newClient(fake)
			c.Credentials, _ = storedCredentials(user, sentinelPass)
			_, err := c.Resolve(ctx, mustParse(t, fake.Host()+"/acme/shop:v1"))
			if err == nil {
				t.Errorf("%s: Resolve() = nil, want an error", name)
			}
			assertNoSecret(t, err, user, sentinelPass)
		}
	})
}

func TestTest(t *testing.T) {
	ctx := context.Background()

	for _, mode := range []struct {
		name   string
		bearer bool
	}{{"bearer", true}, {"basic", false}} {
		t.Run(mode.name+": right credentials", func(t *testing.T) {
			fake := loginFake(t, mode.bearer)
			c := newClient(fake)
			c.Credentials, _ = storedCredentials("deploy", sentinelPass)
			if err := c.Test(ctx, fake.Host()); err != nil {
				t.Fatalf("Test() = %v, want nil", err)
			}
			if mode.bearer {
				if scopes := fake.TokenScopes(); len(scopes) != 1 || scopes[0] != "" {
					t.Errorf("token scopes = %q, want one request with no scope", scopes)
				}
			}
		})

		t.Run(mode.name+": wrong credentials are refused", func(t *testing.T) {
			fake := loginFake(t, mode.bearer)
			c := newClient(fake)
			c.Credentials, _ = storedCredentials("deploy", "wrong-"+sentinelPass)
			err := c.Test(ctx, fake.Host())
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("Test() error = %v, want ErrUnauthorized", err)
			}
			assertNoSecret(t, err, "deploy", "wrong-"+sentinelPass)
		})

		t.Run(mode.name+": a login with nothing stored says so", func(t *testing.T) {
			fake := loginFake(t, mode.bearer)
			c := newClient(fake)
			c.Credentials = noCredentials
			err := c.Test(ctx, fake.Host())
			if err == nil || !strings.Contains(err.Error(), "no credentials are stored for it") {
				t.Errorf("Test() error = %v, want one saying no credentials are stored", err)
			}
		})
	}

	t.Run("an open registry answers 200", func(t *testing.T) {
		fake := registrytest.New(t)
		if err := newClient(fake).Test(ctx, fake.Host()); err != nil {
			t.Errorf("Test() = %v, want nil", err)
		}
	})

	t.Run("a token service that refuses the token", func(t *testing.T) {
		fake := loginFake(t, true)
		fake.FailToken(http.StatusUnauthorized)
		c := newClient(fake)
		c.Credentials, _ = storedCredentials("deploy", sentinelPass)
		if err := c.Test(ctx, fake.Host()); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("Test() error = %v, want ErrUnauthorized", err)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		host := strings.TrimPrefix(srv.URL, "http://")
		srv.Close()
		c := newClient(registrytest.New(t))
		c.Credentials, _ = storedCredentials("deploy", sentinelPass)
		err := c.Test(ctx, host)
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("Test() error = %v, want ErrUnavailable", err)
		}
		assertNoSecret(t, err, "deploy", sentinelPass)
	})

	t.Run("not a registry", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		defer srv.Close()
		err := (&Client{}).Test(ctx, strings.TrimPrefix(srv.URL, "http://"))
		if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "didn't answer as a container registry") {
			t.Errorf("Test() error = %v, want ErrUnavailable: didn't answer as a container registry", err)
		}
	})
}

func TestRegistryHosts(t *testing.T) {
	ctx := context.Background()

	t.Run("a TLS registry's handshake failure says HTTPS is required", func(t *testing.T) {
		// A plain-HTTP server reached over HTTPS is the common mistake.
		plain := httptest.NewServer(http.NotFoundHandler())
		defer plain.Close()
		// A TLS server whose certificate the default client doesn't trust.
		untrusted := httptest.NewUnstartedServer(http.NotFoundHandler())
		untrusted.Config.ErrorLog = log.New(io.Discard, "", 0) // the failed handshakes are the point
		untrusted.StartTLS()
		defer untrusted.Close()

		tests := map[string]string{
			"http server":           "https://" + strings.TrimPrefix(plain.URL, "http://"),
			"untrusted certificate": untrusted.URL,
		}
		for name, endpoint := range tests {
			c := &Client{Endpoints: map[string]string{"registry.example.com": endpoint}, retryDelays: []time.Duration{time.Hour}}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second) // a retry would wait out the hour until this ends
			start := time.Now()
			_, err := c.Resolve(ctx, mustParse(t, "registry.example.com/acme/shop:v1"))
			if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "must serve the registry API over HTTPS") {
				t.Errorf("%s: Resolve() error = %v, want ErrUnavailable saying HTTPS is required", name, err)
			}
			if time.Since(start) > 2*time.Second {
				t.Errorf("%s: a TLS failure was retried", name)
			}
			err = c.Test(ctx, "registry.example.com")
			if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "must serve the registry API over HTTPS") {
				t.Errorf("%s: Test() error = %v, want ErrUnavailable saying HTTPS is required", name, err)
			}
			cancel()
		}
	})

	t.Run("a 401 from something that isn't a registry", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		c := &Client{}
		_, err := c.Resolve(ctx, mustParse(t, strings.TrimPrefix(srv.URL, "http://")+"/acme/shop:v1"))
		if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "didn't answer as a container registry") {
			t.Errorf("Resolve() error = %v, want ErrUnavailable: didn't answer as a container registry", err)
		}
	})

	t.Run("a registry that names its version but sends no challenge is private", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		_, err := (&Client{}).Resolve(ctx, mustParse(t, strings.TrimPrefix(srv.URL, "http://")+"/acme/shop:v1"))
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Resolve() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("a loopback registry with a port needs no endpoint override", func(t *testing.T) {
		fake := registrytest.New(t)
		fake.AddManifest("gate/busybox", "v1", otherDigest)
		got, err := (&Client{}).Resolve(ctx, mustParse(t, fake.Host()+"/gate/busybox:v1"))
		if want := fake.Host() + "/gate/busybox@" + otherDigest; err != nil || got != want {
			t.Errorf("Resolve() = %q, %v; want %q", got, err, want)
		}
	})
}

func TestParseChallenge(t *testing.T) {
	tests := []struct {
		header string
		want   challenge
		wantOK bool
	}{
		{`Bearer realm="https://auth.example.com/token",service="registry",scope="repository:a/b:pull"`,
			challenge{scheme: "bearer", realm: "https://auth.example.com/token", service: "registry"}, true},
		{`Basic realm="lab"`, challenge{scheme: "basic", realm: "lab"}, true},
		{`Basic`, challenge{scheme: "basic"}, true},
		{`basic realm=lab`, challenge{scheme: "basic", realm: "lab"}, true},
		{`Bearer service="registry"`, challenge{scheme: "bearer", service: "registry"}, false},
		{`Negotiate`, challenge{}, false},
		{``, challenge{}, false},
		{`Bearer realm="unterminated`, challenge{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.header, func(t *testing.T) {
			got, ok := parseChallenge(tc.header)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("parseChallenge(%q) = %+v, %v; want %+v, %v", tc.header, got, ok, tc.want, tc.wantOK)
			}
		})
	}

	t.Run("Bearer wins over Basic", func(t *testing.T) {
		got, ok := pickChallenge([]string{`Basic realm="x"`, `Bearer realm="https://t/token"`})
		if !ok || got.scheme != "bearer" {
			t.Errorf("pickChallenge = %+v, %v; want the Bearer one", got, ok)
		}
	})
}
