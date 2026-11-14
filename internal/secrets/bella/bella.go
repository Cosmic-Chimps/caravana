// Package bella reads secrets from Bella Baxter (feature 037) through Bella's
// own Go SDK. It is the one place that imports the SDK: the rest of Caravana
// sees a secrets.Source.
//
// Caravana always presents the environment's private key (Bella's
// zero-knowledge encryption), so every read is end-to-end encrypted and a
// tenant that enforces it accepts the read. That means Caravana holds the
// private key: inherent to resolving a reference at deploy, and said on
// Settings › Secret providers.
//
// Errors never carry a value, the API key, the private key or the text of the
// SDK's errors: a failed read is a *secrets.SourceError in Caravana's words
// (plan D3), and a bad setting is a *ConfigError.
package bella

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cosmic-chimps/bella-baxter-go/bellabaxter"
	abstractions "github.com/microsoft/kiota-abstractions-go"

	"github.com/cosmic-chimps/caravana/internal/secrets"
)

// readTimeout bounds one HTTP request to Bella; the caller's context may be
// shorter.
const readTimeout = 10 * time.Second

// Why a setting is refused. Each is a fixed sentence: the setting's value is
// never put in it.
var (
	ErrInvalidURL         = errors.New("must be an https:// address (http:// only for localhost)")
	ErrInvalidProject     = errors.New("must be Bella's project slug: letters, digits, '.', '_' or '-'")
	ErrInvalidEnvironment = errors.New("must be Bella's environment slug: letters, digits, '.', '_' or '-'")
	ErrInvalidAPIKey      = errors.New("must be a Bella API key, bax- followed by a key id and a secret in hex")
	ErrInvalidPrivateKey  = errors.New("must be the PKCS#8 P-256 private key Bella showed when it made the API key (PEM or base64)")
)

// ConfigError is a setting New refuses. Field is the setting's name in the
// provider's settings: url, project, environment, apiKey or privateKey. Err is
// one of the Err… sentences above.
type ConfigError struct {
	Field string
	Err   error
}

// Error returns the sentence, which never holds the setting's value.
func (e *ConfigError) Error() string { return e.Field + " " + e.Err.Error() }

// Unwrap lets errors.Is find the Err… sentence.
func (e *ConfigError) Unwrap() error { return e.Err }

// Auth is how Caravana proves itself to Bella. The API key and the private key
// are the one way today; Bella's workload-identity grant can be another.
type Auth interface {
	apply(*bellabaxter.Options) error
}

// APIKey is an environment API key and the private key Bella showed when it
// made the key.
type APIKey struct {
	Key        string
	PrivateKey string
}

var (
	slugRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	hexRE    = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	loopback = regexp.MustCompile(`^(localhost|.+\.localhost)$`)
)

func (a APIKey) apply(o *bellabaxter.Options) error {
	// The SDK would take a missing private key from its own environment
	// variable, so Caravana requires it here, and checks the key's form
	// itself so that any later construction error is the private key's.
	parts := strings.SplitN(strings.TrimSpace(a.Key), "-", 3)
	if len(parts) != 3 || parts[0] != "bax" || !hexRE.MatchString(parts[1]) || !hexRE.MatchString(parts[2]) {
		return &ConfigError{Field: "apiKey", Err: ErrInvalidAPIKey}
	}
	if strings.TrimSpace(a.PrivateKey) == "" {
		return &ConfigError{Field: "privateKey", Err: ErrInvalidPrivateKey}
	}
	o.ApiKey = strings.TrimSpace(a.Key)
	o.PrivateKeyPEM = a.PrivateKey
	return nil
}

// Config is a Bella environment Caravana reads.
type Config struct {
	// URL is Bella's address: https://, or http:// for a loopback host.
	URL string
	// Project and Environment are Bella's slugs.
	Project     string
	Environment string
	Auth        Auth
}

// Source is a secrets.Source over one Bella environment.
type Source struct {
	client      *bellabaxter.Client
	project     string
	environment string
}

// New checks cfg and builds the client. A refused setting is a *ConfigError.
func New(cfg Config) (*Source, error) {
	base, err := checkURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	if !slugRE.MatchString(cfg.Project) {
		return nil, &ConfigError{Field: "project", Err: ErrInvalidProject}
	}
	if !slugRE.MatchString(cfg.Environment) {
		return nil, &ConfigError{Field: "environment", Err: ErrInvalidEnvironment}
	}
	if cfg.Auth == nil {
		return nil, &ConfigError{Field: "apiKey", Err: ErrInvalidAPIKey}
	}
	opts := bellabaxter.Options{
		BaxterURL: base,
		Timeout:   readTimeout,
		AppClient: "caravana",
	}
	if err := cfg.Auth.apply(&opts); err != nil {
		return nil, err
	}
	client, err := bellabaxter.New(opts)
	if err != nil {
		// The API key's form was checked above, so this is the private key.
		// The SDK's text is dropped: it would repeat settings.
		return nil, &ConfigError{Field: "privateKey", Err: ErrInvalidPrivateKey}
	}
	return &Source{client: client, project: cfg.Project, environment: cfg.Environment}, nil
}

// checkURL returns the address without a trailing slash.
func checkURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	bad := &ConfigError{Field: "url", Err: ErrInvalidURL}
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return "", bad
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", bad
		}
	default:
		return "", bad
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// isLoopback reports whether host is localhost, *.localhost or a loopback IP.
func isLoopback(host string) bool {
	if loopback.MatchString(strings.ToLower(host)) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var _ secrets.Source = (*Source)(nil)

// Read implements secrets.Source: one bulk read of the environment, filtered
// to paths. A path Bella doesn't have is absent.
func (s *Source) Read(ctx context.Context, paths []string) (map[string]string, error) {
	all, err := s.all(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		if v, ok := all[p]; ok {
			out[p] = v
		}
	}
	return out, nil
}

// Probe implements secrets.Source: how many secrets the environment holds.
func (s *Source) Probe(ctx context.Context) (int, error) {
	all, err := s.all(ctx)
	if err != nil {
		return 0, err
	}
	return len(all), nil
}

func (s *Source) all(ctx context.Context) (map[string]string, error) {
	resp, err := s.client.GetAllSecrets(ctx, s.project, s.environment)
	if err != nil {
		return nil, s.classify(err)
	}
	return resp.Secrets, nil
}

// classify turns anything the SDK returns into one of Caravana's sentences.
// It looks at the error's type and status only, never its text.
func (s *Source) classify(err error) *secrets.SourceError {
	var e2ee *bellabaxter.E2EEResponseError
	if errors.As(err, &e2ee) {
		if e2ee.Code == bellabaxter.E2EEPlaintextResponse {
			return secrets.Refused("Bella answered without encryption")
		}
		return secrets.Refused("Bella's answer could not be decrypted with the private key")
	}
	// Kiota gives a mapped 404 or 502 as *models.ProblemDetails and any other
	// status as *abstractions.ApiError. ProblemDetails embeds the ApiError by
	// value, so errors.As on *ApiError misses it; the ApiErrorable interface
	// both satisfy finds either.
	var api abstractions.ApiErrorable
	if errors.As(err, &api) {
		switch code := api.GetStatusCode(); code {
		case http.StatusUnauthorized:
			return secrets.Refused("Bella refused the API key")
		case http.StatusForbidden:
			return secrets.Refused("Bella refused the read: this API key has no private key registered, or not this one")
		case http.StatusNotFound:
			return secrets.Refused(fmt.Sprintf("this API key isn't for %s/%s", s.project, s.environment))
		default:
			return secrets.Unavailable(fmt.Sprintf("Bella answered %d", code))
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return secrets.Unavailable("Bella can't be reached")
	}
	var netErr net.Error
	var urlErr *url.Error
	if errors.As(err, &netErr) || errors.As(err, &urlErr) {
		return secrets.Unavailable("Bella can't be reached")
	}
	return secrets.Unavailable("Bella's answer could not be read")
}
