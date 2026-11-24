package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const hmacSentinel = "s3cret-sentinel-034-hmac-key"

func TestACMEStorage(t *testing.T) {
	re := regexp.MustCompile(`^acme-[0-9a-f]{12}\.json$`)
	a := ACMEStorage("https://acme.example.com/dir", "kid-1")
	if !re.MatchString(a) {
		t.Fatalf("ACMEStorage = %q, want acme-<12 hex>.json", a)
	}
	if a != ACMEStorage("https://acme.example.com/dir", "kid-1") {
		t.Error("ACMEStorage is not stable")
	}
	for _, other := range [][2]string{
		{"https://acme.example.com/dir", "kid-2"},
		{"https://acme.example.com/dir", ""},
		{"https://acme.example.org/dir", "kid-1"},
		// The separator keeps "ab" + "c" and "a" + "bc" apart.
		{"https://acme.example.com/dirkid", "-1"},
	} {
		if got := ACMEStorage(other[0], other[1]); got == a {
			t.Errorf("ACMEStorage(%q, %q) = %q, the same as another issuer's", other[0], other[1], got)
		}
	}
	// The rule of contracts/traefik.md, written out: the first 12 hex of SHA-256(server "\n" key ID).
	sum := sha256.Sum256([]byte("https://a\nb"))
	if got, want := ACMEStorage("https://a", "b"), "acme-"+hex.EncodeToString(sum[:6])+".json"; got != want {
		t.Errorf("ACMEStorage = %q, want %q", got, want)
	}
	if got := nomadPath; !got.MatchString(ACMEVariablePath) {
		t.Errorf("%q is not a legal Nomad variable path", ACMEVariablePath)
	}
}

func TestSaveACMEIssuer(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	in := ACMEIssuer{
		Server: "https://acme.zerossl.com/v2/DV90", Email: "ops@example.com",
		EABKeyID: "kid-1", EABHMACKey: hmacSentinel,
		CA: "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
	}
	if err := d.SaveACMEIssuer(ctx, in); err != nil {
		t.Fatalf("SaveACMEIssuer: %v", err)
	}
	got := nomad.Variable(ACMEVariablePath)
	want := map[string]string{
		"server": in.Server, "email": in.Email, "eab_kid": "kid-1", "eab_hmac": hmacSentinel,
		"ca": in.CA, "storage": ACMEStorage(in.Server, "kid-1"),
	}
	if !maps.Equal(got, want) {
		t.Errorf("variable = %v, want %v", got, want)
	}

	// Without EAB and CA all six items are still there, the empty ones empty.
	plain := ACMEIssuer{Server: "https://acme.example.com/dir", Email: "ops@example.com"}
	if err := d.SaveACMEIssuer(ctx, plain); err != nil {
		t.Fatal(err)
	}
	got = nomad.Variable(ACMEVariablePath)
	if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, []string{"ca", "eab_hmac", "eab_kid", "email", "server", "storage"}) {
		t.Errorf("items = %v, want all six", keys)
	}
	if got["eab_kid"] != "" || got["eab_hmac"] != "" || got["ca"] != "" || got["storage"] != ACMEStorage(plain.Server, "") {
		t.Errorf("variable = %v", got)
	}

	if err := d.SaveACMEIssuer(ctx, ACMEIssuer{Email: "x@example.com"}); err == nil {
		t.Error("an issuer without a server was written")
	}
}

func TestACMEIssuerSecret(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	if k, found, err := d.ACMEIssuerSecret(ctx); err != nil || found || k != "" {
		t.Errorf("no variable: %q, %v, %v; want not found", k, found, err)
	}
	if err := d.SaveACMEIssuer(ctx, ACMEIssuer{Server: "https://a.example.com/d", Email: "e@example.com"}); err != nil {
		t.Fatal(err)
	}
	if k, found, err := d.ACMEIssuerSecret(ctx); err != nil || found || k != "" {
		t.Errorf("a variable with no key: %q, %v, %v; want not found", k, found, err)
	}
	if err := d.SaveACMEIssuer(ctx, ACMEIssuer{Server: "https://a.example.com/d", Email: "e@example.com", EABKeyID: "k", EABHMACKey: hmacSentinel}); err != nil {
		t.Fatal(err)
	}
	if k, found, err := d.ACMEIssuerSecret(ctx); err != nil || !found || k != hmacSentinel {
		t.Errorf("with a key: %q, %v, %v", k, found, err)
	}

	nomad.FailVariables(403)
	_, _, err := d.ACMEIssuerSecret(ctx)
	if !errors.Is(err, ErrNomadUnavailable) {
		t.Errorf("a refused read = %v, want ErrNomadUnavailable", err)
	}
}

func TestDeleteACMEIssuer(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)

	if err := d.DeleteACMEIssuer(ctx); err != nil {
		t.Errorf("deleting what isn't there: %v", err)
	}
	if err := d.SaveACMEIssuer(ctx, ACMEIssuer{Server: "https://a.example.com/d", Email: "e@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteACMEIssuer(ctx); err != nil {
		t.Fatal(err)
	}
	if v := nomad.Variable(ACMEVariablePath); v != nil {
		t.Errorf("variable after delete = %v, want none", v)
	}
	// Other variables are left alone.
	nomad.SetVariable("caravana/other", map[string]string{"a": "b"})
	if err := d.DeleteACMEIssuer(ctx); err != nil {
		t.Fatal(err)
	}
	if v := nomad.Variable("caravana/other"); v["a"] != "b" {
		t.Errorf("another variable = %v", v)
	}
}

// No error of the issuer's calls holds the HMAC key.
func TestACMEErrorsNeverHoldTheKey(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	in := ACMEIssuer{Server: "https://a.example.com/d", Email: "e@example.com", EABKeyID: "k", EABHMACKey: hmacSentinel}

	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s: want an error", name)
			return
		}
		if strings.Contains(err.Error(), hmacSentinel) {
			t.Errorf("%s: the error holds the key: %v", name, err)
		}
	}
	for _, status := range []int{400, 403, 500} {
		nomad.FailVariables(status)
		check("save", d.SaveACMEIssuer(ctx, in))
	}
	nomad.FailVariables(0)
	nomad.Down()
	check("save while down", d.SaveACMEIssuer(ctx, in))
	nomad.Up()
}
