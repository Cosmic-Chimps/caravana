package service

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/certs/certstest"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// hmacOne and hmacTwo are valid EAB HMAC keys (24 bytes, base64url without padding).
var (
	hmacOne = base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdefghijklmn"))
	hmacTwo = base64.RawURLEncoding.EncodeToString([]byte("NMLKJIHGFEDCBA9876543210"))
)

// certEnv is a service whose Traefik job renders the issuer and the uploads (feature 034), installed
// with Let's Encrypt, with the clock at the real time and a fake edge.
func certEnv(t *testing.T) *teamEnv {
	t.Helper()
	e := newTeamEnv(t)
	e.nomad.SetTraefikCertificates("traefik:v3.7")
	e.svc.SetACME("production", "ops@example.com")
	e.svc.clock.(*fakeClock).Set(time.Now().UTC())
	e.svc.SetPublicURL("https://console.example.com")
	e.svc.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{"console.example.com": {hostV4.String()}}}, &edgetest.FakeDialer{})
	e.svc.SetEdgeAddress("127.0.0.1:8443")
	return e
}

// acmeDirectory is a fake ACME directory over TLS. It records every path it was asked for, so a test
// can see that an account was never registered. eab says it requires External Account Binding.
type acmeDirectory struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newACMEDirectory(t *testing.T, eab bool, handler func(w http.ResponseWriter, r *http.Request) bool) *acmeDirectory {
	t.Helper()
	d := &acmeDirectory{}
	d.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.paths = append(d.paths, r.Method+" "+r.URL.Path)
		d.mu.Unlock()
		if handler != nil && handler(w, r) {
			return
		}
		doc := map[string]any{
			"newNonce": d.URL + "/nonce", "newAccount": d.URL + "/new-acct", "newOrder": d.URL + "/new-order",
			"meta": map[string]any{"externalAccountRequired": eab},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(d.Close)
	return d
}

func (d *acmeDirectory) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.paths...)
}

// use makes the service read this directory with a client that trusts its certificate, and records the
// CA it is given.
func (d *acmeDirectory) use(e *teamEnv, gotCA *string) {
	e.svc.SetACMEDirectoryClient(func(ca string) *http.Client {
		if gotCA != nil {
			*gotCA = ca
		}
		return d.Client()
	})
}

func (d *acmeDirectory) address() string { return d.URL + "/directory" }

func TestCertificateSettingsOfTheInstallersIssuer(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		mode, issuer, name string
	}{
		{"production", IssuerLetsEncrypt, "Let's Encrypt"},
		{"staging", IssuerLetsEncryptStaging, "Let's Encrypt (staging)"},
		{"none", IssuerNone, ""},
		{"", IssuerUnknown, ""},
	} {
		t.Run(tc.issuer, func(t *testing.T) {
			e := certEnv(t)
			e.svc.SetACME(tc.mode, "ops@example.com")
			cs, err := e.svc.CertificateSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if cs.Issuer != tc.issuer || cs.IssuerName != tc.name || cs.ACME != nil || cs.ContactEmail != "ops@example.com" {
				t.Errorf("settings = %+v, want issuer %q named %q with no custom issuer", cs, tc.issuer, tc.name)
			}
			wantIssuer := ""
			if tc.mode == "none" {
				wantIssuer = UnavailableNoIssuer
			}
			if cs.IssuerUnavailable != wantIssuer || cs.UploadUnavailable != "" {
				t.Errorf("unavailable = %q / %q, want %q / none", cs.IssuerUnavailable, cs.UploadUnavailable, wantIssuer)
			}
		})
	}
}

func TestCertificateSettingsUnavailableReasons(t *testing.T) {
	ctx := context.Background()
	t.Run("a Traefik job from before the feature", func(t *testing.T) {
		e := certEnv(t)
		e.nomad.SetTraefikJob("traefik:v3.6") // no caravana_certificates meta
		cs, err := e.svc.CertificateSettings(ctx)
		if err != nil || cs.IssuerUnavailable != UnavailableTraefikOutdated || cs.UploadUnavailable != UnavailableTraefikOutdated {
			t.Errorf("settings = %+v, %v; want traefik_outdated for both", cs, err)
		}
	})
	t.Run("no Traefik job", func(t *testing.T) {
		e := newTeamEnv(t)
		e.svc.SetACME("production", "")
		cs, err := e.svc.CertificateSettings(ctx)
		if err != nil || cs.IssuerUnavailable != UnavailableTraefikOutdated {
			t.Errorf("settings = %+v, %v; want traefik_outdated", cs, err)
		}
	})
	t.Run("Nomad can't be read", func(t *testing.T) {
		e := certEnv(t)
		e.nomad.Down()
		cs, err := e.svc.CertificateSettings(ctx)
		if err != nil || cs.IssuerUnavailable != UnavailableNomad || cs.UploadUnavailable != UnavailableNomad {
			t.Errorf("settings = %+v, %v; want nomad_unreachable for both and no error", cs, err)
		}
	})
	t.Run("--acme none outranks an old job for the issuer only", func(t *testing.T) {
		e := certEnv(t)
		e.svc.SetACME("none", "")
		e.nomad.SetTraefikJob("traefik:v3.6")
		cs, _ := e.svc.CertificateSettings(ctx)
		if cs.IssuerUnavailable != UnavailableNoIssuer || cs.UploadUnavailable != UnavailableTraefikOutdated {
			t.Errorf("unavailable = %q / %q", cs.IssuerUnavailable, cs.UploadUnavailable)
		}
	})
	for _, reason := range []string{UnavailableNoIssuer, UnavailableTraefikOutdated, UnavailableNomad} {
		if UnavailableWords(reason) == "" {
			t.Errorf("UnavailableWords(%q) is empty", reason)
		}
	}
}

func TestSetCertificateIssuer(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	dir := newACMEDirectory(t, true, nil)
	dir.use(e, nil)

	hmac := hmacOne
	cs, err := e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{
		Server: dir.address(), Email: "ops@example.com", EABKeyID: "kid-1", EABHMACKey: &hmac})
	if err != nil {
		t.Fatalf("SetCertificateIssuer: %v", err)
	}
	if cs.Issuer != IssuerCustom || cs.ContactEmail != "ops@example.com" || cs.ACME == nil || cs.ACME.Server != dir.address() ||
		cs.ACME.EABKeyID != "kid-1" || !cs.ACME.EABKeySet || cs.ACME.PrivateCA || cs.ACME.UpdatedAt.IsZero() {
		t.Errorf("settings = %+v (%+v)", cs, cs.ACME)
	}
	if cs.IssuerName != "127.0.0.1" {
		t.Errorf("issuer name = %q, want the server's host", cs.IssuerName)
	}
	v := e.nomad.Variable(deploy.ACMEVariablePath)
	if v["server"] != dir.address() || v["email"] != "ops@example.com" || v["eab_kid"] != "kid-1" || v["eab_hmac"] != hmacOne ||
		v["ca"] != "" || v["storage"] != deploy.ACMEStorage(dir.address(), "kid-1") {
		t.Errorf("variable = %+v", v)
	}
	// Only the directory was read: no account, no nonce, no order.
	if got := dir.requests(); len(got) != 1 || got[0] != "GET /directory" {
		t.Errorf("the directory saw %v, want one GET of the directory", got)
	}
	if again, _ := e.svc.CertificateSettings(ctx); again.Issuer != IssuerCustom || again.ACME == nil {
		t.Errorf("the settings read again = %+v", again)
	}
}

func TestSetCertificateIssuerKeepsTheKey(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	dir := newACMEDirectory(t, true, nil)
	dir.use(e, nil)
	set := func(in CertificateIssuerInput) (CertificateSettings, error) {
		in.Server = dir.address()
		return e.svc.SetCertificateIssuer(ctx, in)
	}
	hmac := hmacOne
	if _, err := set(CertificateIssuerInput{Email: "a@example.com", EABKeyID: "kid-1", EABHMACKey: &hmac}); err != nil {
		t.Fatal(err)
	}

	// The same key ID, no key: the stored one stays (nil and empty both say "keep").
	for _, keep := range []*string{nil, new("")} {
		cs, err := set(CertificateIssuerInput{Email: "b@example.com", EABKeyID: "kid-1", EABHMACKey: keep})
		if err != nil || cs.ContactEmail != "b@example.com" || !cs.ACME.EABKeySet {
			t.Fatalf("edit without the key = %+v, %v", cs, err)
		}
		if got := e.nomad.Variable(deploy.ACMEVariablePath); got["eab_hmac"] != hmacOne || got["email"] != "b@example.com" {
			t.Errorf("the variable after an edit without the key = %+v", got)
		}
	}

	// A new key ID needs its key.
	_, err := set(CertificateIssuerInput{Email: "b@example.com", EABKeyID: "kid-2"})
	if got := fieldOf(err); got != "eabHmacKey" {
		t.Errorf("a new key ID without a key = %v, want a field error on eabHmacKey", err)
	}
	// ... and takes it.
	two := hmacTwo
	if cs, err := set(CertificateIssuerInput{Email: "b@example.com", EABKeyID: "kid-2", EABHMACKey: &two}); err != nil || cs.ACME.EABKeyID != "kid-2" {
		t.Fatalf("a new key ID with its key = %+v, %v", cs, err)
	}
	if got := e.nomad.Variable(deploy.ACMEVariablePath); got["eab_hmac"] != hmacTwo || got["storage"] != deploy.ACMEStorage(dir.address(), "kid-2") {
		t.Errorf("the variable with the new key = %+v", got)
	}
	// A key without its ID.
	one := hmacOne
	_, err = set(CertificateIssuerInput{Email: "b@example.com", EABHMACKey: &one})
	if got := fieldOf(err); got != "eabKeyId" {
		t.Errorf("a key without an ID = %v, want a field error on eabKeyId", err)
	}
	// No key ID drops the EAB, and the stored key goes with it (for an issuer that doesn't require it).
	plain := newACMEDirectory(t, false, nil)
	plain.use(e, nil)
	dir = plain
	cs, err := set(CertificateIssuerInput{Email: "b@example.com"})
	if err != nil || cs.ACME.EABKeySet || cs.ACME.EABKeyID != "" {
		t.Fatalf("an issuer without EAB = %+v, %v", cs, err)
	}
	if got := e.nomad.Variable(deploy.ACMEVariablePath); got["eab_hmac"] != "" || got["eab_kid"] != "" {
		t.Errorf("the variable after dropping EAB = %+v", got)
	}
}

func TestSetCertificateIssuerRefusals(t *testing.T) {
	ctx := context.Background()
	plain := newACMEDirectory(t, false, nil)
	needsEAB := newACMEDirectory(t, true, nil)
	notACME := newACMEDirectory(t, false, func(w http.ResponseWriter, r *http.Request) bool {
		_, _ = w.Write([]byte(`{"hello": "world"}`))
		return true
	})
	missing := newACMEDirectory(t, false, func(w http.ResponseWriter, r *http.Request) bool {
		http.NotFound(w, r)
		return true
	})
	moved := newACMEDirectory(t, false, func(w http.ResponseWriter, r *http.Request) bool {
		http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
		return true
	})
	goodKey := hmacOne
	notBase64 := "not base64 !!"
	tooShort := "QUJD"
	notPEM := "just words"

	for _, tc := range []struct {
		name  string
		dir   *acmeDirectory
		in    CertificateIssuerInput
		field string
	}{
		{"an http address", plain, CertificateIssuerInput{Server: "http://acme.example.com/dir", Email: "a@example.com"}, "server"},
		{"an empty address", plain, CertificateIssuerInput{Email: "a@example.com"}, "server"},
		{"a directory that is a 404", missing, CertificateIssuerInput{Email: "a@example.com"}, "server"},
		{"an address that isn't a directory", notACME, CertificateIssuerInput{Email: "a@example.com"}, "server"},
		{"a redirect", moved, CertificateIssuerInput{Email: "a@example.com"}, "server"},
		{"no email", plain, CertificateIssuerInput{}, "email"},
		{"an email with a name", plain, CertificateIssuerInput{Email: "Ana <a@example.com>"}, "email"},
		{"an email that isn't one", plain, CertificateIssuerInput{Email: "ana"}, "email"},
		{"a bad key ID", plain, CertificateIssuerInput{Email: "a@example.com", EABKeyID: "has space", EABHMACKey: &goodKey}, "eabKeyId"},
		{"a key that isn't base64", plain, CertificateIssuerInput{Email: "a@example.com", EABKeyID: "k", EABHMACKey: &notBase64}, "eabHmacKey"},
		{"a key that is too short", plain, CertificateIssuerInput{Email: "a@example.com", EABKeyID: "k", EABHMACKey: &tooShort}, "eabHmacKey"},
		{"a CA that isn't PEM", plain, CertificateIssuerInput{Email: "a@example.com", CACertificate: &notPEM}, "caCertificate"},
		{"an issuer that requires EAB, given none", needsEAB, CertificateIssuerInput{Email: "a@example.com"}, "eabKeyId"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := certEnv(t)
			tc.dir.use(e, nil)
			in := tc.in
			if in.Server == "" && tc.name != "an empty address" {
				in.Server = tc.dir.address()
			}
			_, err := e.svc.SetCertificateIssuer(ctx, in)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field {
				t.Fatalf("error = %v, want a field error on %s", err, tc.field)
			}
			if _, ok, _ := e.store.ACMEIssuer(ctx); ok {
				t.Error("a refused save changed the stored issuer")
			}
			if v := e.nomad.Variable(deploy.ACMEVariablePath); v != nil {
				t.Errorf("a refused save wrote the variable: %+v", v)
			}
		})
	}
}

func TestSetCertificateIssuerUnavailable(t *testing.T) {
	ctx := context.Background()
	dir := newACMEDirectory(t, false, nil)
	for _, tc := range []struct {
		name  string
		setup func(e *teamEnv)
		words string
	}{
		{"installed without an issuer", func(e *teamEnv) { e.svc.SetACME("none", "") }, "--acme production"},
		{"a Traefik job from before the feature", func(e *teamEnv) { e.nomad.SetTraefikJob("traefik:v3.6") }, "run the installer again"},
		{"Nomad can't be read", func(e *teamEnv) { e.nomad.Down() }, "couldn't read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := certEnv(t)
			dir.use(e, nil)
			tc.setup(e)
			_, err := e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: dir.address(), Email: "a@example.com"})
			if !errors.Is(err, ErrIssuerUnavailable) || !strings.Contains(err.Error(), tc.words) {
				t.Errorf("error = %v, want ErrIssuerUnavailable saying %q", err, tc.words)
			}
			if _, err := e.svc.ResetCertificateIssuer(ctx); !errors.Is(err, ErrIssuerUnavailable) {
				t.Errorf("a reset = %v, want ErrIssuerUnavailable too", err)
			}
		})
	}
}

// Nomad is written first: when it refuses, SQLite is as it was.
func TestSetCertificateIssuerNomadRefuses(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	dir := newACMEDirectory(t, false, nil)
	dir.use(e, nil)
	e.nomad.FailVariables(http.StatusForbidden)
	if _, err := e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: dir.address(), Email: "a@example.com"}); err == nil {
		t.Fatal("a save with Nomad refusing succeeded")
	}
	if _, ok, _ := e.store.ACMEIssuer(ctx); ok {
		t.Error("SQLite holds an issuer Nomad never got")
	}
	e.nomad.FailVariables(0)
	if cs, _ := e.svc.CertificateSettings(ctx); cs.Issuer != IssuerLetsEncrypt || cs.ACME != nil {
		t.Errorf("the settings = %+v, want the installer's issuer", cs)
	}
}

func TestResetCertificateIssuer(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	dir := newACMEDirectory(t, false, nil)
	dir.use(e, nil)

	// Nothing set: 200 all the same.
	if cs, err := e.svc.ResetCertificateIssuer(ctx); err != nil || cs.Issuer != IssuerLetsEncrypt {
		t.Fatalf("a reset with no issuer = %+v, %v", cs, err)
	}
	if _, err := e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: dir.address(), Email: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	cs, err := e.svc.ResetCertificateIssuer(ctx)
	if err != nil || cs.Issuer != IssuerLetsEncrypt || cs.IssuerName != "Let's Encrypt" || cs.ACME != nil || cs.ContactEmail != "ops@example.com" {
		t.Fatalf("a reset = %+v, %v; want the installer's issuer back", cs, err)
	}
	if v := e.nomad.Variable(deploy.ACMEVariablePath); v != nil {
		t.Errorf("the variable survived a reset: %+v", v)
	}
}

func TestCustomIssuerNames(t *testing.T) {
	for server, want := range map[string]string{
		"https://dv.acme-v02.api.pki.goog/directory":     "Google Trust Services",
		"https://acme.zerossl.com/v2/DV90":               "ZeroSSL",
		"https://acme.digicert.com/v2/acme/directory/":   "DigiCert",
		"https://ca.internal.example.com:9000/acme/dir":  "ca.internal.example.com",
		"https://acme.zerossl.com/v2/DV90?different=yes": "acme.zerossl.com",
	} {
		if got := customIssuerName(server); got != want {
			t.Errorf("customIssuerName(%q) = %q, want %q", server, got, want)
		}
	}
	if len(ACMEPresets) != 3 {
		t.Fatalf("%d presets, want 3", len(ACMEPresets))
	}
	for _, p := range ACMEPresets {
		if p.Key == "" || p.Name == "" || !strings.HasPrefix(p.Server, "https://") {
			t.Errorf("preset %+v is incomplete", p)
		}
	}
}

// A preset's name shows when the stored server is its address.
func TestCertificateSettingsNamesAPreset(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	if err := e.store.SetACMEIssuer(ctx, store.ACMEIssuer{Server: ACMEPresets[1].Server, Email: "a@example.com", EABKeyID: "kid", EABKeySet: true}); err != nil {
		t.Fatal(err)
	}
	cs, err := e.svc.CertificateSettings(ctx)
	if err != nil || cs.IssuerName != "ZeroSSL" || cs.Issuer != IssuerCustom {
		t.Errorf("settings = %+v, %v; want ZeroSSL", cs, err)
	}
}

// An issuer's CA reaches the directory check and the domain checks, and a CA that was stored is kept
// by an edit that gives none and removed by an empty one.
func TestCertificateIssuerCA(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	dir := newACMEDirectory(t, false, nil)
	var seen string
	dir.use(e, &seen)
	ca := certstest.NewCA(t, "Private ACME CA")
	caPEM := string(ca.PEM())

	cs, err := e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: dir.address(), Email: "a@example.com", CACertificate: &caPEM})
	if err != nil || !cs.ACME.PrivateCA {
		t.Fatalf("save with a CA = %+v, %v", cs, err)
	}
	if seen != caPEM {
		t.Errorf("the directory client was given the CA %q, want the normalized PEM", seen)
	}
	if v := e.nomad.Variable(deploy.ACMEVariablePath); v["ca"] != caPEM {
		t.Errorf("the variable's ca = %q", v["ca"])
	}

	// The domain checks trust the CA, and keep the dialer's own roots.
	other := edgetest.NewCA(t, "Other CA")
	e.svc.SetEdge(nil, &edgetest.FakeDialer{RootCAs: other.Pool()})
	roots := e.svc.edgeSettings().dialer.(edge.RootsProvider).Roots()
	leaf := certstest.NewLeaf(t, ca, []string{"a.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if _, err := leaf.Cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "a.example.com"}); err != nil {
		t.Errorf("a certificate of the issuer's CA isn't trusted by the checks: %v", err)
	}
	theirs := other.Issue(t, []string{"b.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if _, err := theirs.Verify(x509.VerifyOptions{Roots: roots, DNSName: "b.example.com"}); err != nil {
		t.Errorf("the dialer's own roots were lost: %v", err)
	}

	// An edit that says nothing about the CA keeps it; an empty one removes it.
	if cs, err := e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: dir.address(), Email: "b@example.com"}); err != nil || !cs.ACME.PrivateCA {
		t.Fatalf("edit without the CA = %+v, %v", cs, err)
	}
	if seen != caPEM {
		t.Errorf("the directory client lost the stored CA: %q", seen)
	}
	empty := ""
	if cs, err := e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: dir.address(), Email: "b@example.com", CACertificate: &empty}); err != nil || cs.ACME.PrivateCA {
		t.Fatalf("edit removing the CA = %+v, %v", cs, err)
	}
	if v := e.nomad.Variable(deploy.ACMEVariablePath); v["ca"] != "" {
		t.Errorf("the variable keeps a removed CA: %q", v["ca"])
	}
	if _, wrapped := e.svc.edgeSettings().dialer.(issuerCADialer); wrapped {
		t.Error("the checks still wrap their dialer after the CA was removed")
	}
}

func TestIssuerCertificateSettingsHoldNoKey(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	dir := newACMEDirectory(t, true, nil)
	dir.use(e, nil)
	key := hmacOne
	cs, err := e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: dir.address(), Email: "a@example.com", EABKeyID: "kid-1", EABHMACKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cs)
	if strings.Contains(string(raw), hmacOne) {
		t.Errorf("the settings hold the HMAC key: %s", raw)
	}
}
