package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/audit"
)

// Certificates (034 and 056, merged after the audit log): the issuer's save and reset, an upload, its
// removal and a drop request each leave one entry, and none holds the HMAC key or the private key.
func TestCertificatesAreAudited(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	dir := newACMEDirectory(t, true, nil)
	dir.use(e, nil)

	hmac := hmacOne
	got := notedOne(t, e.store, func() {
		if _, err := e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{
			Server: dir.address(), Email: "ops@example.com", EABKeyID: "kid-1", EABHMACKey: &hmac}); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSettings, "certificates")
	if c := change(got.Changes, "issuer"); c == nil || c.To != dir.address() {
		t.Errorf("issuer change = %+v (all: %+v)", c, got.Changes)
	}
	if c := change(got.Changes, "eabKeyId"); c == nil || c.To != "kid-1" {
		t.Errorf("eabKeyId change = %+v", c)
	}
	if c := change(got.Changes, "eabHmacSecret"); c == nil || c.To != "" || c.From != "" {
		t.Errorf("the HMAC key's change = %+v, want one with no value", c)
	}

	// The same key ID with a new key: the key's leaf says changed, still with no value.
	other := hmacTwo
	got = notedOne(t, e.store, func() {
		if _, err := e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{
			Server: dir.address(), Email: "ops@example.com", EABKeyID: "kid-1", EABHMACKey: &other}); err != nil {
			t.Fatal(err)
		}
	})
	if c := change(got.Changes, "eabHmacSecret"); c == nil || c.Op != audit.OpChanged || c.To != "" || c.From != "" {
		t.Errorf("a new key = %+v, want changed with no value", c)
	}

	got = notedOne(t, e.store, func() {
		if _, err := e.svc.ResetCertificateIssuer(ctx); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionUpdated, audit.KindSettings, "certificates")
	if c := change(got.Changes, "issuer"); c == nil || c.Op != audit.OpRemoved {
		t.Errorf("reset's issuer change = %+v", c)
	}
	// A second reset changes nothing.
	if n := len(noted(t, e.store, func() {
		if _, err := e.svc.ResetCertificateIssuer(ctx); err != nil {
			t.Fatal(err)
		}
	})); n != 0 {
		t.Errorf("a reset with no custom issuer left %d entries", n)
	}

	c := newBYOCert(t, []string{"*.byo.test", "byo.test"}, 90*24*time.Hour)
	var id string
	got = notedOne(t, e.store, func() { id = c.mustUpload(t, e).ID })
	wantEntry(t, got, audit.ActionCreated, audit.KindCertificate, id)
	if got.Resource.Name != "*.byo.test" {
		t.Errorf("the upload's name = %q, want its subject", got.Resource.Name)
	}

	// Replacing the same names: one entry for the new one, the old one listed as deleted.
	c2 := newBYOCert(t, []string{"*.byo.test", "byo.test"}, 60*24*time.Hour)
	got = notedOne(t, e.store, func() { c2.mustUpload(t, e) })
	if got.Action != audit.ActionCreated || len(got.Related) != 1 || got.Related[0].ID != id || got.Related[0].Action != audit.ActionDeleted {
		t.Errorf("a replacing upload = %+v, want the old one related as deleted", got)
	}

	list, err := e.svc.UploadedCertificates(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	got = notedOne(t, e.store, func() {
		if err := e.svc.DeleteUploadedCertificate(ctx, list[0].ID); err != nil {
			t.Fatal(err)
		}
	})
	wantEntry(t, got, audit.ActionDeleted, audit.KindCertificate, list[0].ID)

	// No entry holds a key.
	for _, row := range auditRows(t, e.store) {
		b, _ := json.Marshal(row)
		for _, secret := range []string{hmacOne, other, "PRIVATE KEY", c.keyPEM, c2.keyPEM} {
			if strings.Contains(string(b), secret) {
				t.Errorf("an audit entry holds a key: %s", b)
			}
		}
	}
}
