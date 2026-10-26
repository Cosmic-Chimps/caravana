package edge

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
)

const day = 24 * time.Hour

func TestCheckCertificate(t *testing.T) {
	const host = "shop.example.com"
	now := time.Now().UTC().Truncate(time.Second)
	warn := 14 * day

	le := edgetest.NewCA(t, "Let's Encrypt")
	staging := edgetest.NewCA(t, "(STAGING) Let's Encrypt")

	valid := le.Issue(t, []string{host}, now.Add(-30*day), now.Add(60*day))
	inWindow := le.Issue(t, []string{host}, now.Add(-80*day), now.Add(10*day))
	atWindow := le.Issue(t, []string{host}, now.Add(-76*day), now.Add(warn))
	expired := le.Issue(t, []string{host}, now.Add(-100*day), now.Add(-1*day))
	notYet := le.Issue(t, []string{host}, now.Add(1*day), now.Add(90*day))
	wrongName := le.Issue(t, []string{"other.example.com"}, now.Add(-30*day), now.Add(60*day))
	untrusted := staging.Issue(t, []string{host}, now.Add(-30*day), now.Add(60*day))
	selfSignedForHost := edgetest.SelfSigned(t, host, []string{host})
	selfSignedOther := edgetest.SelfSigned(t, "some.cert", []string{"nothing.example.org"})
	traefik := edgetest.TraefikDefault(t)

	tests := []struct {
		name        string
		chain       []*x509.Certificate
		dialErr     error
		resolverSet bool
		want        string
		wantIssuer  string
		wantAfter   time.Time // zero: not checked as set
		wantRenews  bool
	}{
		{"trusted for the host", []*x509.Certificate{valid}, nil, true, CertIssued, "Let's Encrypt", valid.NotAfter, true},
		{"trusted, no resolver set", []*x509.Certificate{valid}, nil, false, CertIssued, "Let's Encrypt", valid.NotAfter, true},
		{"within the warning window", []*x509.Certificate{inWindow}, nil, true, CertExpiring, "Let's Encrypt", inWindow.NotAfter, true},
		{"exactly at the window", []*x509.Certificate{atWindow}, nil, true, CertExpiring, "Let's Encrypt", atWindow.NotAfter, true},
		{"expired", []*x509.Certificate{expired}, nil, true, CertExpiring, "Let's Encrypt", expired.NotAfter, true},
		{"not valid yet", []*x509.Certificate{notYet}, nil, true, CertError, "Let's Encrypt", notYet.NotAfter, false},
		{"Traefik's default, resolver set", []*x509.Certificate{traefik}, nil, true, CertPending, TraefikOwn, time.Time{}, false},
		{"Traefik's default, no resolver", []*x509.Certificate{traefik}, nil, false, CertSelf, TraefikOwn, time.Time{}, false},
		{"self-signed with other names counts as Traefik's", []*x509.Certificate{selfSignedOther}, nil, true, CertPending, TraefikOwn, time.Time{}, false},
		{"names that don't include the host", []*x509.Certificate{wrongName}, nil, true, CertError, "Let's Encrypt", wrongName.NotAfter, false},
		{"a staging chain nobody trusts", []*x509.Certificate{untrusted}, nil, true, CertError, "(STAGING) Let's Encrypt", untrusted.NotAfter, false},
		{"a self-signed certificate for the host is not trusted", []*x509.Certificate{selfSignedForHost}, nil, true, CertError, selfSignedForHost.Issuer.CommonName, selfSignedForHost.NotAfter, false},
		{"nothing presented", nil, nil, true, CertError, "", time.Time{}, false},
		{"dial failed", nil, errors.New("connection refused"), true, CertError, "", time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &edgetest.FakeDialer{
				Chains:  map[string][]*x509.Certificate{host: tt.chain},
				RootCAs: le.Pool(),
			}
			if tt.dialErr != nil {
				d.Errors = map[string]error{host: tt.dialErr}
			}
			got := checkCertificate(context.Background(), d, "127.0.0.1:443", host, tt.resolverSet, warn, now)
			if got.Status != tt.want {
				t.Fatalf("status = %q, want %q (%+v)", got.Status, tt.want, got)
			}
			if got.Issuer != tt.wantIssuer {
				t.Errorf("issuer = %q, want %q", got.Issuer, tt.wantIssuer)
			}
			if !got.NotAfter.Equal(tt.wantAfter) {
				t.Errorf("notAfter = %v, want %v", got.NotAfter, tt.wantAfter)
			}
			if tt.wantRenews {
				if want := got.NotAfter.Add(-30 * day); !got.RenewsAt.Equal(want) {
					t.Errorf("renewsAt = %v, want %v", got.RenewsAt, want)
				}
			} else if !got.RenewsAt.IsZero() {
				t.Errorf("renewsAt = %v, want zero", got.RenewsAt)
			}
		})
	}
}

// The leaf is trusted through an intermediate that the server presents.
func TestCheckCertificateUsesPresentedIntermediates(t *testing.T) {
	const host = "shop.example.com"
	now := time.Now()
	root := edgetest.NewCA(t, "Root Org")
	valid := root.Issue(t, []string{host}, now.Add(-day), now.Add(60*day))

	// A chain with an unrelated extra certificate still verifies.
	extra := edgetest.NewCA(t, "Unrelated").Cert
	d := &edgetest.FakeDialer{Chains: map[string][]*x509.Certificate{host: {valid, extra}}, RootCAs: root.Pool()}
	got := checkCertificate(context.Background(), d, "e:443", host, true, 14*day, now)
	if got.Status != CertIssued {
		t.Errorf("status = %q, want issued", got.Status)
	}
}

// With no RootsProvider the system roots decide, so a test CA is not trusted.
func TestCheckCertificateSystemRootsByDefault(t *testing.T) {
	const host = "shop.example.com"
	now := time.Now()
	ca := edgetest.NewCA(t, "Private")
	d := &edgetest.FakeDialer{Chains: map[string][]*x509.Certificate{host: {ca.Issue(t, []string{host}, now.Add(-day), now.Add(60*day))}}}
	if got := checkCertificate(context.Background(), d, "e:443", host, true, 14*day, now); got.Status != CertError {
		t.Errorf("status = %q, want error (a private CA isn't a system root)", got.Status)
	}
}
