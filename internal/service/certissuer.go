package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/certs"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The ACME issuer of the installation (feature 034, spec User Story 1). The owner may set an issuer
// of their own (another ACME directory, with or without External Account Binding) instead of the
// installer's Let's Encrypt. Caravana only writes the Nomad Variable caravana/acme: Traefik's job
// renders its static configuration from it and restarts its own task, so the console needs no root
// and no installer run. The EAB HMAC key is in that variable and nowhere else; SQLite holds the
// address, the email, the key ID and the issuer's CA certificate, which are public.

// Why a part of the certificate settings is unavailable (CertificateSettings.IssuerUnavailable and
// UploadUnavailable).
const (
	// UnavailableNoIssuer: the installer ran with --acme none, so Traefik has no ACME resolver.
	UnavailableNoIssuer = "installed_without_issuer"
	// UnavailableTraefikOutdated: Traefik's job predates the feature and doesn't read the variables.
	UnavailableTraefikOutdated = "traefik_outdated"
	// UnavailableNomad: Traefik's job couldn't be read from Nomad.
	UnavailableNomad = "nomad_unreachable"
)

var (
	// ErrIssuerUnavailable is returned when the issuer can't be changed here: the message says why and
	// what to run (UnavailableWords).
	ErrIssuerUnavailable = errors.New("the certificate issuer can't be changed")
	// ErrCertificatesUnavailable is returned when certificates can't be uploaded here.
	ErrCertificatesUnavailable = errors.New("certificates can't be uploaded")
)

// UnavailableWords says in a sentence why a reason of CertificateSettings makes a part unavailable,
// and what to run. "" for a reason it doesn't know.
func UnavailableWords(reason string) string {
	switch reason {
	case UnavailableNoIssuer:
		return "Caravana was installed without an issuer (--acme none): run the installer with --acme production, then choose it here."
	case UnavailableTraefikOutdated:
		return "Traefik's job is older than this feature: run the installer again on this server."
	case UnavailableNomad:
		return "Caravana couldn't read Traefik's job from Nomad."
	}
	return ""
}

// ACMEPreset is an issuer the console offers by name.
type ACMEPreset struct {
	Key, Name, Server string
}

// ACMEPresets are the issuers named in Settings › Certificates. All three need External Account
// Binding (research R9).
var ACMEPresets = []ACMEPreset{
	{Key: "google", Name: "Google Trust Services", Server: "https://dv.acme-v02.api.pki.goog/directory"},
	{Key: "zerossl", Name: "ZeroSSL", Server: "https://acme.zerossl.com/v2/DV90"},
	{Key: "digicert", Name: "DigiCert", Server: "https://acme.digicert.com/v2/acme/directory/"},
}

// customIssuerName is a custom issuer's name in words: a preset's when the server is its address,
// else the server's host.
func customIssuerName(server string) string {
	for _, p := range ACMEPresets {
		if p.Server == server {
			return p.Name
		}
	}
	if u, err := url.Parse(server); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return server
}

// certificateFeatures says whether the issuer and uploads are available, each as the reason it
// isn't, or "". mode is CARAVANA_ACME. Traefik's job is read from Nomad every time: the answer is
// small, and a rerun of the installer shows at once.
func (s *Service) certificateFeatures(ctx context.Context, mode string) (issuer, upload string) {
	features, err := s.deployer.IngressFeatures(ctx)
	reason := ""
	switch {
	case err != nil:
		reason = UnavailableNomad
	case !features.Certificates:
		reason = UnavailableTraefikOutdated
	}
	upload = reason
	issuer = reason
	if mode == "none" {
		issuer = UnavailableNoIssuer
	}
	return issuer, upload
}

// CertificateIssuerInput is what SetCertificateIssuer takes. EABHMACKey and CACertificate are nil
// to keep what is stored; an empty CACertificate removes it.
type CertificateIssuerInput struct {
	Server, Email string
	EABKeyID      string
	EABHMACKey    *string
	CACertificate *string
}

// directoryTimeout is how long Caravana waits for an ACME directory.
const directoryTimeout = 10 * time.Second

// SetACMEDirectoryClient sets how the ACME directory of a custom issuer is reached: it is given the
// issuer's CA certificate (PEM, "" for none) and returns the http.Client that reads the directory.
// Tests give one that trusts their server. A nil function restores the default, which trusts the
// system's roots and the CA.
func (s *Service) SetACMEDirectoryClient(f func(ca string) *http.Client) {
	if f == nil {
		s.acmeClient.Store(nil)
		return
	}
	s.acmeClient.Store(&f)
}

// directoryClient is the client that reads an issuer's directory.
func (s *Service) directoryClient(ca string) *http.Client {
	if f := s.acmeClient.Load(); f != nil {
		return (*f)(ca)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if ca != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM([]byte(ca))
		cfg.RootCAs = pool
	}
	return &http.Client{Timeout: directoryTimeout, Transport: &http.Transport{TLSClientConfig: cfg, Proxy: http.ProxyFromEnvironment}}
}

// SetCertificateIssuer sets the custom ACME issuer and returns the settings as they are now.
//
// It refuses (ErrIssuerUnavailable) when the installation can't take an issuer, and answers an
// *app.FieldError naming server, email, eabKeyId, eabHmacKey or caCertificate for what is wrong.
// The directory at server is read before anything is saved (it must be an ACME directory, and when
// it requires External Account Binding the key ID must be given); an account is never registered,
// since an EAB key can be single-use. The Nomad Variable is written first and SQLite second, so a
// Nomad that refuses leaves everything as it was.
//
// The HMAC key: nil (or empty) keeps the stored one only when eabKeyId is the stored key's ID; a new
// key ID needs a key; a key with no key ID is refused.
func (s *Service) SetCertificateIssuer(ctx context.Context, in CertificateIssuerInput) (CertificateSettings, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return CertificateSettings{}, err
	}
	mode, _ := s.ACME()
	if reason, _ := s.certificateFeatures(ctx, mode); reason != "" {
		return CertificateSettings{}, fmt.Errorf("%w: %s", ErrIssuerUnavailable, UnavailableWords(reason))
	}
	server := strings.TrimSpace(in.Server)
	if err := certs.ValidServer(server); err != nil {
		return CertificateSettings{}, certFieldError(err)
	}
	email, err := checkIssuerEmail(in.Email)
	if err != nil {
		return CertificateSettings{}, err
	}
	kid := strings.TrimSpace(in.EABKeyID)
	if kid != "" {
		if err := certs.ValidEABKeyID(kid); err != nil {
			return CertificateSettings{}, certFieldError(err)
		}
	}

	current, hasCurrent, err := s.store.ACMEIssuer(ctx)
	if err != nil {
		return CertificateSettings{}, err
	}
	hmac, err := s.eabKeyFor(ctx, kid, in.EABHMACKey, current, hasCurrent)
	if err != nil {
		return CertificateSettings{}, err
	}
	ca := ""
	if hasCurrent {
		ca = current.CA
	}
	if in.CACertificate != nil {
		ca = ""
		if strings.TrimSpace(*in.CACertificate) != "" {
			if ca, err = certs.NormalizeCAPEM(*in.CACertificate); err != nil {
				return CertificateSettings{}, certFieldError(err)
			}
		}
	}

	dir, err := certs.CheckDirectory(ctx, s.directoryClient(ca), server)
	if err != nil {
		return CertificateSettings{}, certFieldError(err)
	}
	if dir.ExternalAccountRequired && kid == "" {
		return CertificateSettings{}, app.NewFieldError("eabKeyId",
			"this issuer requires External Account Binding: give the key ID and HMAC key from its console")
	}

	if err := s.deployer.SaveACMEIssuer(ctx, deploy.ACMEIssuer{Server: server, Email: email, EABKeyID: kid, EABHMACKey: hmac, CA: ca}); err != nil {
		return CertificateSettings{}, err
	}
	if err := s.store.SetACMEIssuer(ctx, store.ACMEIssuer{Server: server, Email: email, EABKeyID: kid, EABKeySet: hmac != "", CA: ca}); err != nil {
		return CertificateSettings{}, err
	}
	s.issuerCA.set(ca)
	return s.CertificateSettings(ctx)
}

// ResetCertificateIssuer brings the installer's issuer back: the variable goes, then the settings.
// It is 200 also when no custom issuer was set. The previous issuer's account and certificates stay
// in Traefik's store, one file per issuer.
func (s *Service) ResetCertificateIssuer(ctx context.Context) (CertificateSettings, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return CertificateSettings{}, err
	}
	mode, _ := s.ACME()
	if reason, _ := s.certificateFeatures(ctx, mode); reason != "" {
		return CertificateSettings{}, fmt.Errorf("%w: %s", ErrIssuerUnavailable, UnavailableWords(reason))
	}
	if err := s.deployer.DeleteACMEIssuer(ctx); err != nil {
		return CertificateSettings{}, err
	}
	if err := s.store.ClearACMEIssuer(ctx); err != nil {
		return CertificateSettings{}, err
	}
	s.issuerCA.set("")
	return s.CertificateSettings(ctx)
}

// eabKeyFor decides the EAB HMAC key a save ends with, base64url, "" for none. The error is a field
// error that never holds the key.
func (s *Service) eabKeyFor(ctx context.Context, kid string, given *string, current store.ACMEIssuer, hasCurrent bool) (string, error) {
	provided := given != nil && strings.TrimSpace(*given) != ""
	switch {
	case kid == "" && provided:
		return "", app.NewFieldError("eabKeyId", "is required with an HMAC key")
	case kid == "":
		return "", nil
	case provided:
		key, err := certs.NormalizeHMAC(*given)
		if err != nil {
			return "", certFieldError(err)
		}
		return key, nil
	}
	// No key given: keep the stored one, but only for the key ID it belongs to.
	if hasCurrent && current.EABKeySet && current.EABKeyID == kid {
		key, found, err := s.deployer.ACMEIssuerSecret(ctx)
		if err != nil {
			return "", err
		}
		if found {
			return key, nil
		}
	}
	return "", app.NewFieldError("eabHmacKey", "is required for a new key ID")
}

// checkIssuerEmail checks the contact email: a plain address, without a display name.
func checkIssuerEmail(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", app.NewFieldError("email", "is required")
	}
	a, err := mail.ParseAddress(raw)
	if err != nil || a.Name != "" || a.Address != raw || len(raw) > 254 || strings.ContainsAny(raw, "\r\n\"'`\\{}$ ") {
		return "", app.NewFieldError("email", "must be a plain email address, like ops@example.com")
	}
	return raw, nil
}

// certFieldError turns a certs refusal into the field error the API and the console draw. Any other error
// passes through unchanged.
func certFieldError(err error) error {
	var ce *certs.Error
	if errors.As(err, &ce) {
		return &app.FieldError{Field: ce.Field, Message: ce.Message}
	}
	return err
}

// ---- the issuer's CA in the domain checks -----------------------------------------

// issuerCAState is the PEM of the custom issuer's CA, kept in memory for the domain checks, which
// have to trust it to judge a certificate it signed. It is loaded from the store on first use, and
// set by every save and reset.
type issuerCAState struct {
	mu     sync.Mutex
	loaded bool
	pem    string
}

func (c *issuerCAState) set(pem string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded, c.pem = true, pem
}

// get returns the PEM, loading it with load the first time (and again after a load that failed).
func (c *issuerCAState) get(load func() (string, error)) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		pem, err := load()
		if err != nil {
			return ""
		}
		c.loaded, c.pem = true, pem
	}
	return c.pem
}

// withIssuerCA wraps a dialer so the checks also trust the custom issuer's CA. A dialer is wrapped
// only while a CA is stored, and its own roots are kept (the lab's, a test's).
func (s *Service) withIssuerCA(d edge.Dialer) edge.Dialer {
	pem := s.issuerCA.get(func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		issuer, ok, err := s.store.ACMEIssuer(ctx)
		if err != nil || !ok {
			return "", err
		}
		return issuer.CA, nil
	})
	if pem == "" {
		return d
	}
	return issuerCADialer{Dialer: d, ca: pem}
}

// issuerCADialer is a Dialer whose roots are the wrapped dialer's (the system's when it names
// none) plus the issuer's CA certificates.
type issuerCADialer struct {
	edge.Dialer
	ca string
}

// Roots makes the wrapper an edge.RootsProvider.
func (d issuerCADialer) Roots() *x509.CertPool {
	var pool *x509.CertPool
	if p, ok := d.Dialer.(edge.RootsProvider); ok {
		pool = p.Roots()
	}
	switch {
	case pool != nil:
		pool = pool.Clone()
	default:
		if sys, err := x509.SystemCertPool(); err == nil && sys != nil {
			pool = sys
		} else {
			pool = x509.NewCertPool()
		}
	}
	pool.AppendCertsFromPEM([]byte(d.ca))
	return pool
}
