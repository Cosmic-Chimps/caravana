package deploy

import (
	"context"
	"maps"
	"strings"
	"testing"
)

const keySentinel = "s3cret-sentinel-034-private-key"

func TestSaveAndDeleteCertificate(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	const id = "crt_0123456789ab"
	const chain = "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
	key := "-----BEGIN PRIVATE KEY-----\n" + keySentinel + "\n-----END PRIVATE KEY-----\n"

	if err := d.SaveCertificate(ctx, id, chain, key, []string{"*.example.com", "example.com"}); err != nil {
		t.Fatalf("SaveCertificate: %v", err)
	}
	want := map[string]string{"certificate": chain, "key": key, "names": "*.example.com,example.com"}
	if got := nomad.Variable(CertificatesVariablePrefix + id); !maps.Equal(got, want) {
		t.Errorf("variable = %v, want %v", got, want)
	}
	if !nomadPath.MatchString(CertificatesVariablePrefix + id) {
		t.Errorf("%q is not a legal Nomad variable path", CertificatesVariablePrefix+id)
	}

	// A second certificate is a second variable; saving again replaces the first's items.
	const id2 = "crt_ba9876543210"
	if err := d.SaveCertificate(ctx, id2, chain, key, []string{"a.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveCertificate(ctx, id, chain, key, []string{"b.example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := nomad.Variable(CertificatesVariablePrefix + id); got["names"] != "b.example.com" {
		t.Errorf("names after a second save = %q", got["names"])
	}

	if err := d.DeleteCertificate(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := nomad.Variable(CertificatesVariablePrefix + id); got != nil {
		t.Errorf("variable after delete = %v", got)
	}
	if got := nomad.Variable(CertificatesVariablePrefix + id2); got == nil {
		t.Error("deleting one certificate removed another")
	}
	if err := d.DeleteCertificate(ctx, id); err != nil {
		t.Errorf("deleting what isn't there: %v", err)
	}
}

func TestCertificateIDIsCheckedBeforeItIsAPath(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	for _, id := range []string{
		"", "crt_", "crt_0123456789a", "crt_0123456789abc", "crt_0123456789AB", "crt_0123456789ag",
		"../acme", "crt_0123456789ab/../x", "caravana/acme", "crt_0123456789ab\n", " crt_0123456789ab",
	} {
		if err := d.SaveCertificate(ctx, id, "c", "k", []string{"a.test"}); err == nil {
			t.Errorf("SaveCertificate(%q) was accepted", id)
		}
		if err := d.DeleteCertificate(ctx, id); err == nil {
			t.Errorf("DeleteCertificate(%q) was accepted", id)
		}
	}
	if paths := nomad.VariablePaths(); len(paths) != 0 {
		t.Errorf("variables after refused IDs = %v, want none", paths)
	}
	if err := d.SaveCertificate(ctx, "crt_0123456789ab", "", "k", nil); err == nil {
		t.Error("an empty chain was accepted")
	}
	if err := d.SaveCertificate(ctx, "crt_0123456789ab", "c", "", nil); err == nil {
		t.Error("an empty key was accepted")
	}
}

func TestSaveCertificateErrorsNeverHoldTheKey(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	key := "-----BEGIN PRIVATE KEY-----\n" + keySentinel + "\n-----END PRIVATE KEY-----\n"
	for _, status := range []int{400, 403, 500} {
		nomad.FailVariables(status)
		err := d.SaveCertificate(ctx, "crt_0123456789ab", "chain", key, []string{"a.test"})
		if err == nil || strings.Contains(err.Error(), keySentinel) {
			t.Errorf("status %d: error = %v, want one without the key", status, err)
		}
	}
	nomad.FailVariables(0)
	nomad.Down()
	if err := d.SaveCertificate(ctx, "crt_0123456789ab", "chain", key, []string{"a.test"}); err == nil || strings.Contains(err.Error(), keySentinel) {
		t.Errorf("while down: error = %v", err)
	}
	nomad.Up()
}
