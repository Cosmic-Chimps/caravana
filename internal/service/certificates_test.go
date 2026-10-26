package service

import (
	"context"
	"crypto/x509"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
)

func TestCertificateSettings(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)

	got, err := e.svc.CertificateSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := CertificateSettings{Issuer: IssuerUnknown, WarnBeforeDays: 14, EdgeAddress: "127.0.0.1:443"}
	if got != want {
		t.Errorf("before the installer said = %+v, want %+v", got, want)
	}

	for mode, issuer := range map[string]string{"production": IssuerLetsEncrypt, "staging": IssuerLetsEncryptStaging, "none": IssuerNone, "": IssuerUnknown} {
		e.svc.SetACME(mode, "ops@example.com")
		if got, _ := e.svc.CertificateSettings(ctx); got.Issuer != issuer || got.ContactEmail != "ops@example.com" {
			t.Errorf("CARAVANA_ACME=%q: %+v, want issuer %s", mode, got, issuer)
		}
	}
	e.svc.SetEdgeAddress("127.0.0.1:8443")

	if err := e.svc.SetWarnBeforeDays(ctx, 7); err != nil {
		t.Fatalf("SetWarnBeforeDays(7): %v", err)
	}
	if got, _ := e.svc.CertificateSettings(ctx); got.WarnBeforeDays != 7 || got.EdgeAddress != "127.0.0.1:8443" {
		t.Errorf("after 7 = %+v", got)
	}
	for _, bad := range []int{0, 10, 31, -7} {
		var fe *app.FieldError
		if err := e.svc.SetWarnBeforeDays(ctx, bad); !errors.As(err, &fe) || fe.Field != "warnBeforeDays" {
			t.Errorf("SetWarnBeforeDays(%d) = %v, want a field error on warnBeforeDays", bad, err)
		}
	}
	if got, _ := e.svc.CertificateSettings(ctx); got.WarnBeforeDays != 7 {
		t.Errorf("a refused value changed the window: %+v", got)
	}
}

// The window reaches the checks: a leaf 10 days from its end is expiring with
// the default 14 days and live with 7, and the cached row follows a change of
// the window at once, without a new check.
func TestWarnWindowReachesChecks(t *testing.T) {
	ctx := context.Background()
	ca := edgetest.NewCA(t, "Test CA")
	leaf := ca.Issue(t, []string{"shop.example.com"}, time.Now().Add(-time.Hour), time.Now().Add(10*24*time.Hour))
	dialer := &edgetest.FakeDialer{RootCAs: ca.Pool(), Chains: map[string][]*x509.Certificate{"shop.example.com": {leaf}}}
	e := domainEnv(t, edgetest.FakeResolver{Hosts: map[string][]string{"shop.example.com": {hostV4.String()}}}, dialer)
	seedDomainApp(t, e, domainApp("shop", app.Domain{Host: "shop.example.com"}), true)

	if _, err := e.svc.CheckDomain(ctx, "shop.example.com", ""); err != nil {
		t.Fatal(err)
	}
	state := func() edge.State {
		v, err := e.svc.Domain(ctx, "shop.example.com")
		if err != nil {
			t.Fatal(err)
		}
		return v.State
	}
	if got := state(); got != edge.StateCertExpiring {
		t.Fatalf("10 days left with a 14-day window: %s, want cert_expiring", got)
	}
	att, _ := e.svc.Attention(ctx)
	if len(att.Items) != 1 || att.Items[0].Kind != AttentionDomainCertExpiring {
		t.Errorf("needs-you = %+v, want the expiring certificate", att.Items)
	}

	changes, cancel := e.svc.Subscribe("")
	defer cancel()
	if err := e.svc.SetWarnBeforeDays(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != edge.StateLive {
		t.Errorf("10 days left with a 7-day window, without a new check: %s, want live", got)
	}
	if notices := drainNotices(changes); !strings.Contains(notices, string(ChangeAttention)) || !strings.Contains(notices, string(ChangeDomains)) {
		t.Errorf("notices = %s, want the domains and the needs-you list told", notices)
	}
	// A fresh check reads the window from the store too.
	if _, err := e.svc.CheckDomain(ctx, "shop.example.com", ""); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != edge.StateLive {
		t.Errorf("a fresh check with a 7-day window: %s, want live", got)
	}
	if err := e.svc.SetWarnBeforeDays(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != edge.StateCertExpiring {
		t.Errorf("10 days left with a 30-day window: %s, want cert_expiring", got)
	}
}

func TestCertificates(t *testing.T) {
	ctx := context.Background()
	e := domainEnv(t, edgetest.FakeResolver{}, edgeOf(t))
	seedDomainApp(t, e, domainApp("shop", app.Domain{Host: "shop.example.com"}, app.Domain{Host: "api.example.com"},
		app.Domain{Host: "new.example.com"}, app.Domain{Host: "lab.example.com"}, app.Domain{Host: "never.example.com"}), true)
	seedDomainApp(t, e, domainApp("old", app.Domain{Host: "old.example.com"}), true)
	stopDomainApp(t, e, "old")

	cacheRow(t, e, "shop.example.com", "shop", edge.DNSOK, edge.CertIssued)
	cacheRow(t, e, "api.example.com", "shop", edge.DNSOK, edge.CertExpiring)
	cacheRow(t, e, "new.example.com", "shop", edge.DNSOK, edge.CertPending)
	cacheRow(t, e, "lab.example.com", "shop", edge.DNSOK, edge.CertSelf)
	cacheRow(t, e, "old.example.com", "old", edge.DNSOK, edge.CertIssued) // stopped: no row

	got, err := e.svc.Certificates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range got {
		order = append(order, c.Host+" "+c.Status)
	}
	want := []string{"api.example.com expiring", "new.example.com pending", "shop.example.com issued", "lab.example.com self"}
	if !slices.Equal(order, want) {
		t.Fatalf("certificates = %v, want %v", order, want)
	}
	shop := got[2]
	if shop.App != "shop" || shop.Service != "web" || shop.Issuer != "Let's Encrypt" || shop.NotAfter == nil ||
		shop.RenewsAt == nil || !shop.RenewsAt.Equal(shop.NotAfter.Add(-30*24*time.Hour)) {
		t.Errorf("the issued row = %+v", shop)
	}
	if lab := got[3]; lab.NotAfter != nil || lab.RenewsAt != nil {
		t.Errorf("Traefik's own row has dates: %+v", lab)
	}

	empty := newEnv(t)
	if list, err := empty.svc.Certificates(ctx); err != nil || list == nil || len(list) != 0 {
		t.Errorf("no domains = %v (%v), want an empty list, not nil", list, err)
	}
}
