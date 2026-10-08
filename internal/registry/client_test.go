package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
