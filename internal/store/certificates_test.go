package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestACMEIssuer(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if _, ok, err := s.ACMEIssuer(ctx); err != nil || ok {
		t.Fatalf("a new store: ACMEIssuer = _, %v, %v; want none (the installer's issuer)", ok, err)
	}
	// The existing setting is untouched by the new columns.
	if days, _, err := s.CertificateSettings(ctx); err != nil || days != 14 {
		t.Fatalf("CertificateSettings = %d, %v; want 14", days, err)
	}

	in := ACMEIssuer{
		Server: "https://dv.acme-v02.api.pki.goog/directory", Email: "ops@example.com",
		EABKeyID: "kid-1", EABKeySet: true,
		CA: "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n",
	}
	if err := s.SetACMEIssuer(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.ACMEIssuer(ctx)
	if err != nil || !ok {
		t.Fatalf("ACMEIssuer = %+v, %v, %v", got, ok, err)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero: the store sets it")
	}
	in.UpdatedAt = got.UpdatedAt
	if !reflect.DeepEqual(got, in) {
		t.Errorf("got %+v, want %+v", got, in)
	}

	// Replacing: no EAB, no CA.
	plain := ACMEIssuer{Server: "https://acme.example.com/dir", Email: "b@example.com"}
	if err := s.SetACMEIssuer(ctx, plain); err != nil {
		t.Fatal(err)
	}
	got, _, err = s.ACMEIssuer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plain.UpdatedAt = got.UpdatedAt
	if !reflect.DeepEqual(got, plain) || !got.UpdatedAt.After(in.UpdatedAt) {
		t.Errorf("got %+v, want %+v saved later than %v", got, plain, in.UpdatedAt)
	}

	// The warning window lives on.
	if err := s.SetWarnBeforeDays(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.ACMEIssuer(ctx); !ok || got.Server != plain.Server {
		t.Errorf("after SetWarnBeforeDays the issuer = %+v, %v", got, ok)
	}

	if err := s.SetACMEIssuer(ctx, ACMEIssuer{Email: "x@example.com"}); err == nil {
		t.Error("an issuer without a server was accepted")
	}

	if err := s.ClearACMEIssuer(ctx); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.ACMEIssuer(ctx); err != nil || ok || !reflect.DeepEqual(got, ACMEIssuer{}) {
		t.Errorf("after Clear: %+v, %v, %v; want none", got, ok, err)
	}
	if days, _, _ := s.CertificateSettings(ctx); days != 30 {
		t.Errorf("Clear changed the warning window to %d", days)
	}
	if err := s.ClearACMEIssuer(ctx); err != nil {
		t.Errorf("clearing twice: %v", err)
	}
}

func testUploaded(id, fingerprint string, uploaded time.Time) UploadedCertificate {
	return UploadedCertificate{
		ID: id, Names: []string{"*.example.com", "example.com"},
		Subject: "example.com", Issuer: "Acme Trust",
		NotBefore:   time.Date(2026, 10, 1, 0, 0, 0, 123456789, time.UTC),
		NotAfter:    time.Date(2027, 1, 20, 8, 30, 0, 0, time.UTC),
		Fingerprint: fingerprint, KeyType: "ECDSA P-256", Trusted: true,
		UploadedAt: uploaded,
	}
}

func TestUploadedCertificates(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if n, err := s.CountUploadedCertificates(ctx); err != nil || n != 0 {
		t.Fatalf("Count on a new store = %d, %v", n, err)
	}
	if list, err := s.ListUploadedCertificates(ctx); err != nil || len(list) != 0 {
		t.Fatalf("List on a new store = %v, %v", list, err)
	}

	t0 := time.Date(2026, 11, 12, 9, 0, 0, 5, time.UTC)
	a := testUploaded("crt_aaaaaaaaaaaa", "aa", t0)
	b := testUploaded("crt_bbbbbbbbbbbb", "bb", t0.Add(time.Hour))
	c := testUploaded("crt_cccccccccccc", "cc", t0) // the same moment as a: by ID
	c.Trusted = false
	c.Names = nil // read back as an empty list, not null
	for _, x := range []UploadedCertificate{b, c, a} {
		if err := s.InsertUploadedCertificate(ctx, x); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.GetUploadedCertificate(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, a) {
		t.Errorf("Get = %+v, want %+v (nine fraction digits kept)", got, a)
	}

	list, err := s.ListUploadedCertificates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range list {
		ids = append(ids, x.ID)
	}
	if want := []string{a.ID, c.ID, b.ID}; !reflect.DeepEqual(ids, want) {
		t.Errorf("List order = %v, want %v (uploaded_at, then id)", ids, want)
	}
	if list[1].Trusted || len(list[1].Names) != 0 || list[1].Names == nil {
		t.Errorf("c read back as %+v: want untrusted and an empty (not nil) names list", list[1])
	}
	if n, _ := s.CountUploadedCertificates(ctx); n != 3 {
		t.Errorf("Count = %d, want 3", n)
	}

	if err := s.InsertUploadedCertificate(ctx, a); err == nil {
		t.Error("the same ID was inserted twice")
	}

	if _, err := s.GetUploadedCertificate(ctx, "crt_000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get of a missing ID = %v, want ErrNotFound", err)
	}
	if err := s.DeleteUploadedCertificate(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUploadedCertificate(ctx, b.ID); err != nil {
		t.Errorf("deleting twice: %v", err)
	}
	if _, err := s.GetUploadedCertificate(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
	if n, _ := s.CountUploadedCertificates(ctx); n != 2 {
		t.Errorf("Count after delete = %d, want 2", n)
	}
}

// TestUpgradeTo0044 migrates a store that holds 023's data as 0043 left it: the warning window and a
// domain check stay as they were and read back with the new columns empty.
func TestUpgradeTo0044(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := testDB(t, dir)
	if _, err := migrate(ctx, db, migrationsUpTo(t, 43)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE certificate_settings SET warn_before_days = 30"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO domain_checks
		(host, app_id, service, state, dns_status, cert_status, cert_issuer, cert_not_after, checked_at)
		VALUES ('shop.example.com', 'shop', 'web', 'live', 'ok', 'issued', 'Let''s Encrypt',
		        '2027-01-20T08:30:00.000000000Z', '2026-10-24T09:15:30.123456789Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open of a store at 0043: %v", err)
	}
	defer s.Close()

	if days, _, err := s.CertificateSettings(ctx); err != nil || days != 30 {
		t.Errorf("warning window after the upgrade = %d, %v; want 30 kept", days, err)
	}
	if _, ok, err := s.ACMEIssuer(ctx); err != nil || ok {
		t.Errorf("ACMEIssuer after the upgrade = %v, %v; want none", ok, err)
	}
	rec, err := s.GetDomainCheck(ctx, "shop.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if rec.CertIssuer != "Let's Encrypt" || rec.CertFingerprint != "" || rec.CertStatus != "issued" {
		t.Errorf("domain check after the upgrade = %+v, want it kept with an empty fingerprint", rec)
	}
	if n, err := s.CountUploadedCertificates(ctx); err != nil || n != 0 {
		t.Errorf("Count after the upgrade = %d, %v", n, err)
	}
	// And the check can be written again, with a fingerprint.
	rec.CertFingerprint = "ff"
	if err := s.UpsertDomainCheck(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetDomainCheck(ctx, rec.Host); again.CertFingerprint != "ff" {
		t.Errorf("fingerprint after Upsert = %q", again.CertFingerprint)
	}
}
