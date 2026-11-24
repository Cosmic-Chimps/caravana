package service

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/certs"
	"github.com/cosmic-chimps/caravana/internal/certs/certstest"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/store"
)

var certIDRE = regexp.MustCompile(`^crt_[0-9a-f]{12}$`)

// byoCert is a leaf of a private CA, with the PEM an upload sends: the leaf and its root, and the key.
type byoCert struct {
	leaf     *certstest.Leaf
	ca       *certstest.Authority
	chainPEM string
	keyPEM   string
}

// newBYOCert makes a certificate for names that is valid from an hour ago for the given time.
func newBYOCert(t *testing.T, names []string, valid time.Duration) byoCert {
	t.Helper()
	ca := certstest.NewCA(t, "Private Root", certstest.Organization("Acme Internal"))
	leaf := certstest.NewLeaf(t, ca, names, time.Now().Add(-time.Hour), time.Now().Add(valid), certstest.Organization("Acme Internal"))
	return byoCert{leaf: leaf, ca: ca, chainPEM: string(certstest.Chain(leaf.Cert, ca.Cert)), keyPEM: string(leaf.KeyPEM(t, certstest.PKCS8))}
}

func (c byoCert) upload(e *teamEnv) (UploadedCertificateView, error) {
	return e.svc.UploadCertificate(context.Background(), c.chainPEM, c.keyPEM)
}

// mustUpload uploads and fails the test on an error.
func (c byoCert) mustUpload(t *testing.T, e *teamEnv) UploadedCertificateView {
	t.Helper()
	v, err := c.upload(e)
	if err != nil {
		t.Fatalf("UploadCertificate: %v", err)
	}
	return v
}

func certVariablePaths(e *teamEnv) []string {
	var out []string
	for _, p := range e.nomad.VariablePaths() {
		if strings.HasPrefix(p, deploy.CertificatesVariablePrefix) {
			out = append(out, p)
		}
	}
	return out
}

func TestUploadCertificate(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	c := newBYOCert(t, []string{"*.byo.test", "byo.test"}, 90*24*time.Hour)

	v, err := c.upload(e)
	if err != nil {
		t.Fatalf("UploadCertificate: %v", err)
	}
	if !certIDRE.MatchString(v.ID) || !slices.Equal(v.Names, []string{"*.byo.test", "byo.test"}) || v.Subject != "*.byo.test" ||
		v.Issuer != "Acme Internal" || v.KeyType != "ECDSA P-256" || v.Trusted || v.Status != UploadValid ||
		v.Fingerprint != certs.Fingerprint(c.leaf.Cert) || v.Replaced != "" || v.UploadedAt.IsZero() ||
		len(v.Domains) != 0 || len(v.KeptEarlier) != 0 || v.NotAfter.Before(time.Now().Add(80*24*time.Hour)) {
		t.Errorf("view = %+v", v)
	}

	// The variable Traefik reads: the chain (leaf first, no root), the key and the names.
	vars := e.nomad.Variable(deploy.CertificatesVariablePrefix + v.ID)
	if vars["names"] != "*.byo.test,byo.test" || !strings.Contains(vars["key"], "PRIVATE KEY") {
		t.Errorf("variable = %v", vars)
	}
	if got := strings.Count(vars["certificate"], "BEGIN CERTIFICATE"); got != 1 {
		t.Errorf("the stored chain has %d certificates, want the leaf alone (the root is left out)", got)
	}
	// SQLite holds only what is public.
	row, err := e.store.GetUploadedCertificate(ctx, v.ID)
	if err != nil || row.Fingerprint != v.Fingerprint || len(row.Names) != 2 {
		t.Errorf("row = %+v, %v", row, err)
	}

	list, err := e.svc.UploadedCertificates(ctx)
	if err != nil || len(list) != 1 || list[0].ID != v.ID {
		t.Errorf("list = %+v, %v", list, err)
	}
	got, err := e.svc.UploadedCertificate(ctx, v.ID)
	if err != nil || got.ID != v.ID || got.Replaced != "" {
		t.Errorf("get = %+v, %v", got, err)
	}
	if _, err := e.svc.UploadedCertificate(ctx, "crt_000000000000"); !errors.Is(err, ErrCertificateNotFound) {
		t.Errorf("get of an unknown ID = %v, want ErrCertificateNotFound", err)
	}
}

func TestUploadCertificateReplacesTheSameNames(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	first := newBYOCert(t, []string{"*.byo.test", "byo.test"}, 90*24*time.Hour).mustUpload(t, e)
	other := newBYOCert(t, []string{"*.other.test"}, 90*24*time.Hour).mustUpload(t, e)

	// The same names in another order, a new certificate: it takes the place of the first.
	second := newBYOCert(t, []string{"byo.test", "*.byo.test"}, 60*24*time.Hour).mustUpload(t, e)
	if second.Replaced != first.ID || second.ID == first.ID {
		t.Fatalf("second = %+v, want Replaced = %s", second, first.ID)
	}
	if _, err := e.store.GetUploadedCertificate(ctx, first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the replaced row = %v, want it gone", err)
	}
	paths := certVariablePaths(e)
	if len(paths) != 2 || slices.Contains(paths, deploy.CertificatesVariablePrefix+first.ID) {
		t.Errorf("variables = %v, want the replaced one gone and the other kept", paths)
	}
	if list, _ := e.svc.UploadedCertificates(ctx); len(list) != 2 || list[0].ID != other.ID && list[1].ID != other.ID {
		t.Errorf("list = %+v", list)
	}
}

func TestUploadCertificateRefusals(t *testing.T) {
	ctx := context.Background()
	good := newBYOCert(t, []string{"*.byo.test"}, 90*24*time.Hour)
	stranger := newBYOCert(t, []string{"*.byo.test"}, 90*24*time.Hour)
	expiredCA := certstest.NewCA(t, "Old Root")
	expired := certstest.NewLeaf(t, expiredCA, []string{"old.byo.test"}, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	noNames := certstest.NewLeaf(t, good.ca, nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), certstest.CommonName("no-names"))
	// A leaf with its root missing: the chain can't be completed.
	incomplete := string(good.leaf.PEM())

	for _, tc := range []struct {
		name         string
		chain, key   string
		field, saysA string
	}{
		{"a key of another certificate", good.chainPEM, stranger.keyPEM, "key", "match"},
		{"a chain without its issuer", incomplete, good.keyPEM, "certificate", "incomplete"},
		{"an expired certificate", string(certstest.Chain(expired.Cert, expiredCA.Cert)), string(expired.KeyPEM(t, certstest.PKCS8)), "certificate", "expired"},
		{"an encrypted key", good.chainPEM, string(certstest.EncryptedKeyPEM()), "key", "encrypted"},
		{"no DNS names", string(certstest.Chain(noNames.Cert, good.ca.Cert)), string(noNames.KeyPEM(t, certstest.PKCS8)), "certificate", "DNS names"},
		{"no certificate", "", good.keyPEM, "certificate", "no certificate"},
		{"no key", good.chainPEM, "", "key", "no private key"},
		{"text that isn't PEM", "hello", good.keyPEM, "certificate", "PEM"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := certEnv(t)
			_, err := e.svc.UploadCertificate(ctx, tc.chain, tc.key)
			var fe *app.FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field || !strings.Contains(fe.Message, tc.saysA) {
				t.Fatalf("error = %v, want a field error on %s saying %q", err, tc.field, tc.saysA)
			}
			if rows, _ := e.store.ListUploadedCertificates(ctx); len(rows) != 0 || len(certVariablePaths(e)) != 0 {
				t.Errorf("a refused upload left %d rows and %v", len(rows), certVariablePaths(e))
			}
		})
	}
}

func TestUploadCertificateUnavailable(t *testing.T) {
	ctx := context.Background()
	c := newBYOCert(t, []string{"*.byo.test"}, 90*24*time.Hour)
	for _, tc := range []struct {
		name  string
		setup func(e *teamEnv)
		words string
	}{
		{"a Traefik job from before the feature", func(e *teamEnv) { e.nomad.SetTraefikJob("traefik:v3.6") }, "run the installer again"},
		{"Nomad can't be read", func(e *teamEnv) { e.nomad.Down() }, "couldn't read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := certEnv(t)
			tc.setup(e)
			_, err := c.upload(e)
			if !errors.Is(err, ErrCertificatesUnavailable) || !strings.Contains(err.Error(), tc.words) {
				t.Errorf("error = %v, want ErrCertificatesUnavailable saying %q", err, tc.words)
			}
		})
	}
	// --acme none has no issuer, and uploads still work.
	e := certEnv(t)
	e.svc.SetACME("none", "")
	if _, err := c.upload(e); err != nil {
		t.Errorf("an upload with --acme none = %v, want it accepted", err)
	}
	_ = ctx
}

func TestUploadCertificateLimit(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	for i := range maxUploadedCertificates {
		err := e.store.InsertUploadedCertificate(ctx, store.UploadedCertificate{
			ID: fmt.Sprintf("crt_%012x", i+1), Names: []string{fmt.Sprintf("h%d.limit.test", i)},
			Subject: "x", Issuer: "y", NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(60 * 24 * time.Hour),
			Fingerprint: fmt.Sprintf("%064x", i+1), KeyType: "ECDSA P-256", UploadedAt: time.Now().Add(-time.Minute)})
		if err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	_, err := newBYOCert(t, []string{"*.thirty-one.test"}, 90*24*time.Hour).upload(e)
	if !errors.Is(err, ErrTooManyCertificates) {
		t.Fatalf("the 31st certificate = %v, want ErrTooManyCertificates", err)
	}
	if len(certVariablePaths(e)) != 0 {
		t.Error("a refused upload wrote a variable")
	}
	// A replacement is no new certificate: it is accepted at the limit.
	rows, _ := e.store.ListUploadedCertificates(ctx)
	same := newBYOCert(t, rows[0].Names, 90*24*time.Hour)
	if v, err := same.upload(e); err != nil || v.Replaced != rows[0].ID {
		t.Errorf("a replacement at the limit = %+v, %v", v, err)
	}
	if n, _ := e.store.CountUploadedCertificates(ctx); n != maxUploadedCertificates {
		t.Errorf("%d certificates after a replacement, want %d", n, maxUploadedCertificates)
	}
}

// A row that can't be written takes the variable back.
func TestUploadCertificateRollsBackWhenTheRowFails(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	db, err := sql.Open("sqlite", filepath.Join(e.dir, "caravana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER refuse_uploads BEFORE INSERT ON uploaded_certificates BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := newBYOCert(t, []string{"*.byo.test"}, 90*24*time.Hour).upload(e); err == nil {
		t.Fatal("an upload whose row can't be written succeeded")
	}
	if paths := certVariablePaths(e); len(paths) != 0 {
		t.Errorf("variables = %v, want none left behind", paths)
	}
	if n, _ := e.store.CountUploadedCertificates(ctx); n != 0 {
		t.Errorf("%d rows left", n)
	}
}

func TestDeleteUploadedCertificate(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	v := newBYOCert(t, []string{"*.byo.test"}, 90*24*time.Hour).mustUpload(t, e)
	keep := newBYOCert(t, []string{"*.keep.test"}, 90*24*time.Hour).mustUpload(t, e)

	if err := e.svc.DeleteUploadedCertificate(ctx, v.ID); err != nil {
		t.Fatalf("DeleteUploadedCertificate: %v", err)
	}
	if paths := certVariablePaths(e); len(paths) != 1 || paths[0] != deploy.CertificatesVariablePrefix+keep.ID {
		t.Errorf("variables = %v, want only the other one", paths)
	}
	if _, err := e.svc.UploadedCertificate(ctx, v.ID); !errors.Is(err, ErrCertificateNotFound) {
		t.Errorf("get after delete = %v", err)
	}
	if err := e.svc.DeleteUploadedCertificate(ctx, v.ID); !errors.Is(err, ErrCertificateNotFound) {
		t.Errorf("a second delete = %v, want ErrCertificateNotFound", err)
	}
}

// ---- domains ----------------------------------------------------------------------

// uploadedEdge is the fake edge of the domain tests: hosts in acme present a certificate of a CA the
// checks trust, hosts in byo present the uploaded leaf, and any other gets Traefik's own.
type uploadedEdge struct {
	*edgetest.FakeDialer
	acmeCA *edgetest.CA
}

func newUploadedEdge(t *testing.T) uploadedEdge {
	t.Helper()
	acmeCA := edgetest.NewCA(t, "Test ACME")
	return uploadedEdge{
		FakeDialer: &edgetest.FakeDialer{Chains: map[string][]*x509.Certificate{}, RootCAs: acmeCA.Pool(),
			Default: []*x509.Certificate{edgetest.TraefikDefault(t)}},
		acmeCA: acmeCA,
	}
}

func (u uploadedEdge) acme(t *testing.T, hosts ...string) {
	t.Helper()
	for _, h := range hosts {
		u.Chains[h] = []*x509.Certificate{u.acmeCA.Issue(t, []string{h}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))}
	}
}

func (u uploadedEdge) presents(c byoCert, hosts ...string) {
	for _, h := range hosts {
		u.Chains[h] = []*x509.Certificate{c.leaf.Cert}
	}
}

func (u uploadedEdge) pointAt(e *teamEnv) {
	hosts := map[string][]string{"console.example.com": {hostV4.String()}}
	for h := range u.Chains {
		hosts[h] = []string{hostV4.String()}
	}
	e.svc.SetEdge(edgetest.FakeResolver{Hosts: hosts}, u.FakeDialer)
}

func TestUploadedCertificateInDomains(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	edgeSim := newUploadedEdge(t)

	// shop serves three hosts the wildcard covers and two it doesn't. a and b present the upload; d keeps
	// an ACME certificate it already had, and so would e.other.test (not covered at all); pending is
	// Traefik's own certificate while ACME is asked.
	seedDomainApp(t, e.env, domainApp("shop",
		app.Domain{Host: "a.byo.test"}, app.Domain{Host: "b.byo.test"}, app.Domain{Host: "d.byo.test"}, app.Domain{Host: "e.other.test"}), true)
	seedDomainApp(t, e.env, domainApp("later", app.Domain{Host: "p.byo.test"}), true)
	c := newBYOCert(t, []string{"*.byo.test"}, 90*24*time.Hour)
	edgeSim.presents(c, "a.byo.test", "b.byo.test")
	edgeSim.acme(t, "d.byo.test", "e.other.test")
	edgeSim.Chains["p.byo.test"] = nil
	delete(edgeSim.Chains, "p.byo.test") // p gets Traefik's own certificate
	edgeSim.pointAt(e)
	e.svc.SetCertResolver("letsencrypt")

	v := c.mustUpload(t, e)
	if err := e.svc.RefreshDomains(ctx, DomainFilter{}); err != nil {
		t.Fatal(err)
	}

	view, err := e.svc.UploadedCertificate(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(view.Domains, []string{"a.byo.test", "b.byo.test"}) {
		t.Errorf("domains = %v", view.Domains)
	}
	// d presents another certificate, and its route also asks for e.other.test, which the upload doesn't
	// cover: that is why Traefik keeps the earlier one. p has Traefik's own and is not "kept".
	if len(view.KeptEarlier) != 1 || view.KeptEarlier[0].Host != "d.byo.test" ||
		!slices.Equal(view.KeptEarlier[0].AlsoAsks, []string{"e.other.test"}) {
		t.Errorf("keptEarlier = %+v, want d.byo.test also asking e.other.test", view.KeptEarlier)
	}

	rows, err := e.svc.Domains(ctx, DomainFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byHost := map[string]DomainView{}
	for _, r := range rows {
		byHost[r.Host] = r
	}
	a := byHost["a.byo.test"]
	if a.Uploaded == nil || a.Uploaded.ID != v.ID || a.Uploaded.Subject != "*.byo.test" ||
		a.Certificate.Fingerprint != v.Fingerprint || a.Certificate.Status != edge.CertIssued ||
		a.Certificate.Issuer != "Acme Internal" || !a.Certificate.RenewsAt.IsZero() ||
		a.Certificate.NotAfter.IsZero() {
		t.Errorf("an uploaded domain's row = %+v (%+v)", a.Certificate, a.Uploaded)
	}
	if d := byHost["d.byo.test"]; d.Uploaded != nil || d.Certificate.Issuer != "Test ACME" || d.Certificate.Fingerprint == "" || d.Certificate.RenewsAt.IsZero() {
		t.Errorf("an ACME domain's row = %+v (%+v)", d.Certificate, d.Uploaded)
	}

	certsList, err := e.svc.Certificates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range certsList {
		switch r.Host {
		case "a.byo.test":
			if r.Uploaded == nil || r.Uploaded.ID != v.ID || r.Fingerprint != v.Fingerprint || r.RenewsAt != nil {
				t.Errorf("certificate row of an uploaded host = %+v", r)
			}
		case "d.byo.test":
			if r.Uploaded != nil || r.Fingerprint == "" {
				t.Errorf("certificate row of an ACME host = %+v", r)
			}
		}
	}

	// CheckDomain says it too.
	chk, err := e.svc.CheckDomain(ctx, "b.byo.test", "")
	if err != nil || chk.Uploaded == nil || chk.Uploaded.ID != v.ID || chk.Certificate.Fingerprint != v.Fingerprint {
		t.Errorf("CheckDomain = %+v, %v", chk, err)
	}
}

// An uploaded certificate near its end is judged by its own dates, even from a CA the checks don't
// trust, and raises certificate_expiring, one item for all the hosts, and no domain_cert_expiring.
func TestUploadedCertificateExpiringIsOneItem(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	edgeSim := newUploadedEdge(t)
	seedDomainApp(t, e.env, domainApp("shop",
		app.Domain{Host: "a.byo.test"}, app.Domain{Host: "b.byo.test"}), true)
	c := newBYOCert(t, []string{"*.byo.test"}, 5*24*time.Hour)
	edgeSim.presents(c, "a.byo.test", "b.byo.test")
	edgeSim.pointAt(e)
	v := c.mustUpload(t, e)
	if v.Status != UploadExpiring {
		t.Fatalf("status = %s, want expiring (5 days is inside the 14 day window)", v.Status)
	}
	if err := e.svc.RefreshDomains(ctx, DomainFilter{}); err != nil {
		t.Fatal(err)
	}

	rows, _ := e.svc.Domains(ctx, DomainFilter{})
	for _, r := range rows {
		if r.State != edge.StateCertExpiring || r.Certificate.Status != edge.CertExpiring || r.Uploaded == nil {
			t.Errorf("row %s = state %s, cert %s, uploaded %v; want expiring and uploaded", r.Host, r.State, r.Certificate.Status, r.Uploaded)
		}
	}
	att, err := e.svc.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var certItems, domainItems int
	for _, it := range att.Items {
		switch it.Kind {
		case AttentionCertificateExpiring:
			certItems++
			if it.Certificate != v.ID || it.Level != "warning" || !slices.Equal(it.Names, []string{"*.byo.test"}) ||
				it.ExpiresAt == nil || !strings.Contains(it.Detail, "expires in 4 days") && !strings.Contains(it.Detail, "expires in 5 days") ||
				problemKey(it) != "certificate_expiring|||"+v.ID {
				t.Errorf("item = %+v, key %q", it, problemKey(it))
			}
		case AttentionDomainCertExpiring:
			domainItems++
		}
	}
	if certItems != 1 || domainItems != 0 {
		t.Errorf("%d certificate_expiring and %d domain_cert_expiring items, want 1 and 0", certItems, domainItems)
	}
}

// A host an upload covers is checked again a few seconds after the upload, and after its removal.
func TestUploadRechecksTheCoveredHosts(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	e.svc.certRecheck = 10 * time.Millisecond
	edgeSim := newUploadedEdge(t)
	seedDomainApp(t, e.env, domainApp("shop", app.Domain{Host: "a.byo.test"}, app.Domain{Host: "x.other.test"}), true)
	edgeSim.acme(t, "a.byo.test", "x.other.test")
	edgeSim.pointAt(e)
	if err := e.svc.RefreshDomains(ctx, DomainFilter{}); err != nil {
		t.Fatal(err)
	}
	before, _ := e.store.GetDomainCheck(ctx, "a.byo.test")
	other, _ := e.store.GetDomainCheck(ctx, "x.other.test")

	// Traefik picks the upload up: from now on a.byo.test presents it.
	c := newBYOCert(t, []string{"*.byo.test"}, 90*24*time.Hour)
	edgeSim.presents(c, "a.byo.test")
	v := c.mustUpload(t, e)
	waitFor(t, "the covered host to be checked again", func() bool {
		rec, err := e.store.GetDomainCheck(ctx, "a.byo.test")
		return err == nil && rec.CertFingerprint == v.Fingerprint
	})
	if before.CertFingerprint == v.Fingerprint {
		t.Fatal("the fixture is wrong: the host presented the upload before it existed")
	}
	if after, _ := e.store.GetDomainCheck(ctx, "x.other.test"); !after.CheckedAt.Equal(other.CheckedAt) {
		t.Error("a host the upload doesn't cover was checked again")
	}

	// Removal: Traefik goes back to ACME for it.
	edgeSim.acme(t, "a.byo.test")
	if err := e.svc.DeleteUploadedCertificate(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the host to be checked again after the removal", func() bool {
		rec, err := e.store.GetDomainCheck(ctx, "a.byo.test")
		return err == nil && rec.CertFingerprint != v.Fingerprint && rec.CertFingerprint != ""
	})
}

func TestUploadedCertificateStatusByWindow(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, tc := range []struct {
		left time.Duration
		want string
	}{
		{90 * day, UploadValid}, {14*day + time.Hour, UploadValid}, {14 * day, UploadExpiring}, {time.Hour, UploadExpiring},
		{0, UploadExpired}, {-day, UploadExpired},
	} {
		if got := certStatus(now.Add(tc.left), now, 14*day); got != tc.want {
			t.Errorf("certStatus(%v left) = %s, want %s", tc.left, got, tc.want)
		}
	}
}
