package bella

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/secrets"
	"github.com/cosmic-chimps/caravana/internal/secrets/bella/bellatest"
)

// sentinel is a value set in the fake. It must appear in no error text.
const sentinel = "s3cret-sentinel-037-bella"

func newSource(t *testing.T, srv *bellatest.Server) *Source {
	t.Helper()
	src, err := New(configFor(srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return src
}

func configFor(srv *bellatest.Server) Config {
	return Config{
		URL: srv.URL(), Project: srv.Project(), Environment: srv.Environment(),
		Auth: APIKey{Key: srv.APIKey(), PrivateKey: srv.PrivateKeyPEM()},
	}
}

func newFake(t *testing.T) *bellatest.Server {
	t.Helper()
	srv := bellatest.New()
	t.Cleanup(srv.Close)
	srv.Set("DATABASE_URL", sentinel)
	srv.Set("API_KEY", "key-value")
	return srv
}

func TestReadFiltersAndOmitsMissing(t *testing.T) {
	srv := newFake(t)
	src := newSource(t, srv)

	got, err := src.Read(context.Background(), []string{"DATABASE_URL", "NOT_THERE"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got["DATABASE_URL"] != sentinel {
		t.Errorf("Read = %v, want only DATABASE_URL", got)
	}
	if n := srv.Reads(); n != 1 {
		t.Errorf("Bella was read %d times, want one bulk read", n)
	}
	n, err := src.Probe(context.Background())
	if err != nil || n != 2 {
		t.Errorf("Probe = %d, %v; want 2 secrets", n, err)
	}
}

func TestReadSeesChanges(t *testing.T) {
	srv := newFake(t)
	src := newSource(t, srv)
	srv.Set("API_KEY", "changed")
	got, err := src.Read(context.Background(), []string{"API_KEY"})
	if err != nil || got["API_KEY"] != "changed" {
		t.Fatalf("Read = %v, %v; want the new value", got, err)
	}
	srv.Delete("API_KEY")
	got, err = src.Read(context.Background(), []string{"API_KEY"})
	if err != nil || len(got) != 0 {
		t.Fatalf("Read after delete = %v, %v; want it absent", got, err)
	}
}

// Every failure is a *SourceError of the right kind with Caravana's own words,
// and none of the texts holds a value, the API key or the private key.
func TestFailures(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*bellatest.Server)
		cfg     func(*bellatest.Server) Config
		want    error
		message string
	}{
		{"down", func(s *bellatest.Server) { s.SetDown(true) }, nil, secrets.ErrSourceUnavailable, "Bella answered 503"},
		{"server error", func(s *bellatest.Server) { s.SetStatus(500) }, nil, secrets.ErrSourceUnavailable, "Bella answered 500"},
		{"bad gateway", func(s *bellatest.Server) { s.SetStatus(502) }, nil, secrets.ErrSourceUnavailable, "Bella answered 502"},
		{"unauthorized", func(s *bellatest.Server) { s.SetStatus(401) }, nil, secrets.ErrSourceRefused, "Bella refused the API key"},
		{"forbidden", func(s *bellatest.Server) { s.SetStatus(403) }, nil, secrets.ErrSourceRefused, "no private key registered"},
		{"not this environment", func(s *bellatest.Server) { s.SetStatus(404) }, nil, secrets.ErrSourceRefused, "this API key isn't for shop/production"},
		{"another environment", nil, func(s *bellatest.Server) Config {
			c := configFor(s)
			c.Environment = "staging"
			return c
		}, secrets.ErrSourceRefused, "this API key isn't for shop/staging"},
		{"a key Bella doesn't know", nil, func(s *bellatest.Server) Config {
			c := configFor(s)
			c.Auth = APIKey{Key: "bax-" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 64), PrivateKey: s.PrivateKeyPEM()}
			return c
		}, secrets.ErrSourceRefused, "Bella refused the API key"},
		{"wrong device key", nil, func(s *bellatest.Server) Config {
			c := configFor(s)
			c.Auth = APIKey{Key: s.APIKey(), PrivateKey: bellatest.GeneratePrivateKeyPEM()}
			return c
		}, secrets.ErrSourceRefused, "no private key registered"},
		{"tampered envelope", func(s *bellatest.Server) { s.SetTamper(true) }, nil, secrets.ErrSourceRefused, "could not be decrypted"},
		{"plaintext answer", func(s *bellatest.Server) { s.SetPlaintext(true) }, nil, secrets.ErrSourceRefused, "without encryption"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newFake(t)
			if tt.setup != nil {
				tt.setup(srv)
			}
			cfg := configFor(srv)
			if tt.cfg != nil {
				cfg = tt.cfg(srv)
			}
			src, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			for name, call := range map[string]func() error{
				"Read":  func() error { _, err := src.Read(context.Background(), []string{"DATABASE_URL"}); return err },
				"Probe": func() error { _, err := src.Probe(context.Background()); return err },
			} {
				err := call()
				var se *secrets.SourceError
				if !errors.As(err, &se) || !errors.Is(err, tt.want) {
					t.Fatalf("%s error = %v, want a *SourceError matching %v", name, err, tt.want)
				}
				if !strings.Contains(err.Error(), tt.message) {
					t.Errorf("%s error = %q, want it to say %q", name, err, tt.message)
				}
				for _, secret := range []string{sentinel, srv.APIKey(), srv.PrivateKeyPEM(), cfg.Auth.(APIKey).Key, "bella-go-sdk", srv.URL()} {
					if strings.Contains(err.Error(), secret) {
						t.Errorf("%s error %q holds %q", name, err, secret)
					}
				}
			}
		})
	}
}

func TestSlowBellaIsUnavailable(t *testing.T) {
	srv := newFake(t)
	srv.SetDelay(3 * time.Second)
	src := newSource(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := src.Read(ctx, []string{"API_KEY"})
	if !errors.Is(err, secrets.ErrSourceUnavailable) || !strings.Contains(err.Error(), "can't be reached") {
		t.Errorf("Read error = %v, want unavailable: Bella can't be reached", err)
	}
}

func TestNothingListening(t *testing.T) {
	srv := newFake(t)
	cfg := configFor(srv)
	srv.Close()
	src, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.Probe(context.Background())
	if !errors.Is(err, secrets.ErrSourceUnavailable) {
		t.Errorf("Probe error = %v, want unavailable", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error %q quotes the address", err)
	}
}

func TestNewRefusesBadSettings(t *testing.T) {
	srv := newFake(t)
	good := configFor(srv)
	der := strings.TrimSpace(strings.NewReplacer("-----BEGIN PRIVATE KEY-----", "", "-----END PRIVATE KEY-----", "", "\n", "").Replace(srv.PrivateKeyPEM()))
	sec1 := "-----BEGIN EC PRIVATE KEY-----\n" + base64.StdEncoding.EncodeToString([]byte("not a key")) + "\n-----END EC PRIVATE KEY-----\n"

	with := func(mod func(*Config)) Config { c := good; mod(&c); return c }
	tests := []struct {
		name      string
		cfg       Config
		wantField string
		want      error
	}{
		{"https is fine", with(func(c *Config) { c.URL = "https://api.bella-baxter.io" }), "", nil},
		{"base64 DER private key is fine", with(func(c *Config) { c.Auth = APIKey{Key: srv.APIKey(), PrivateKey: der} }), "", nil},
		{"http on localhost is fine", with(func(c *Config) { c.URL = "http://localhost:8080" }), "", nil},
		{"http on a loopback address is fine", with(func(c *Config) { c.URL = "http://[::1]:8080/" }), "", nil},
		{"http away from home", with(func(c *Config) { c.URL = "http://bella.example.com" }), "url", ErrInvalidURL},
		{"http on a private address", with(func(c *Config) { c.URL = "http://10.0.0.5" }), "url", ErrInvalidURL},
		{"not a URL", with(func(c *Config) { c.URL = "bella" }), "url", ErrInvalidURL},
		{"ftp", with(func(c *Config) { c.URL = "ftp://bella.example.com" }), "url", ErrInvalidURL},
		{"user info in the URL", with(func(c *Config) { c.URL = "https://u:p@bella.example.com" }), "url", ErrInvalidURL},
		{"empty URL", with(func(c *Config) { c.URL = "" }), "url", ErrInvalidURL},
		{"empty project", with(func(c *Config) { c.Project = "" }), "project", ErrInvalidProject},
		{"project with a slash", with(func(c *Config) { c.Project = "a/b" }), "project", ErrInvalidProject},
		{"empty environment", with(func(c *Config) { c.Environment = "" }), "environment", ErrInvalidEnvironment},
		{"no auth", with(func(c *Config) { c.Auth = nil }), "apiKey", ErrInvalidAPIKey},
		{"API key without the prefix", with(func(c *Config) {
			c.Auth = APIKey{Key: "abc-" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 64), PrivateKey: srv.PrivateKeyPEM()}
		}), "apiKey", ErrInvalidAPIKey},
		{"API key with one part", with(func(c *Config) { c.Auth = APIKey{Key: "bax-abcdef", PrivateKey: srv.PrivateKeyPEM()} }), "apiKey", ErrInvalidAPIKey},
		{"API key not hex", with(func(c *Config) { c.Auth = APIKey{Key: "bax-zz-yy", PrivateKey: srv.PrivateKeyPEM()} }), "apiKey", ErrInvalidAPIKey},
		{"empty API key", with(func(c *Config) { c.Auth = APIKey{PrivateKey: srv.PrivateKeyPEM()} }), "apiKey", ErrInvalidAPIKey},
		{"no private key", with(func(c *Config) { c.Auth = APIKey{Key: srv.APIKey()} }), "privateKey", ErrInvalidPrivateKey},
		{"SEC1 private key", with(func(c *Config) { c.Auth = APIKey{Key: srv.APIKey(), PrivateKey: sec1} }), "privateKey", ErrInvalidPrivateKey},
		{"garbage private key", with(func(c *Config) { c.Auth = APIKey{Key: srv.APIKey(), PrivateKey: "hello world"} }), "privateKey", ErrInvalidPrivateKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				return
			}
			var ce *ConfigError
			if !errors.As(err, &ce) || ce.Field != tt.wantField || !errors.Is(err, tt.want) {
				t.Fatalf("New error = %v, want a ConfigError on %s matching %v", err, tt.wantField, tt.want)
			}
			if auth, ok := tt.cfg.Auth.(APIKey); ok {
				for _, secret := range []string{auth.Key, auth.PrivateKey, tt.cfg.URL} {
					if secret != "" && strings.Contains(err.Error(), secret) {
						t.Errorf("error %q holds a setting's value", err)
					}
				}
			}
		})
	}
}
