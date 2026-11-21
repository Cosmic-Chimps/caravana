package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const dnsSecret = "cf-s3cret-sentinel-048-deploy"

func TestDNSZoneVariablePath(t *testing.T) {
	for zone, want := range map[string]string{
		"example.com":    "caravana/dns/example~com",
		"dev.example.co": "caravana/dns/dev~example~co",
	} {
		got := DNSZoneVariablePath(zone)
		if got != want {
			t.Errorf("DNSZoneVariablePath(%q) = %q, want %q", zone, got, want)
		}
		if !nomadPath.MatchString(got) {
			t.Errorf("%q is not a legal Nomad variable path", got)
		}
	}
}

func TestDNSZoneToken(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	const zone = "example.com"

	if _, ok, err := d.DNSZoneTokenFor(ctx, zone); err != nil || ok {
		t.Errorf("DNSZoneTokenFor() before a save = %v, %v; want none", ok, err)
	}
	if err := d.SaveDNSZoneToken(ctx, zone, dnsSecret); err != nil {
		t.Fatalf("SaveDNSZoneToken() = %v", err)
	}
	if got := nomad.VariablePaths(); len(got) != 1 || got[0] != "caravana/dns/example~com" {
		t.Errorf("variables = %v, want the one zone variable", got)
	}
	got, ok, err := d.DNSZoneTokenFor(ctx, zone)
	if err != nil || !ok || got != dnsSecret {
		t.Errorf("DNSZoneTokenFor() = %q, %v, %v", got, ok, err)
	}
	if err := d.SaveDNSZoneToken(ctx, zone, "second"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := d.DNSZoneTokenFor(ctx, zone); got != "second" {
		t.Errorf("token after a second save = %q", got)
	}
	if err := d.SaveDNSZoneToken(ctx, zone, ""); err == nil {
		t.Error("SaveDNSZoneToken(empty) succeeded")
	}
	if err := d.DeleteDNSZoneToken(ctx, zone); err != nil {
		t.Fatalf("DeleteDNSZoneToken() = %v", err)
	}
	if _, ok, _ := d.DNSZoneTokenFor(ctx, zone); ok {
		t.Error("token found after a delete")
	}
	if err := d.DeleteDNSZoneToken(ctx, zone); err != nil {
		t.Errorf("DeleteDNSZoneToken() of a zone with none = %v, want nil", err)
	}
}

func TestDNSZoneTokenRefusesBadNames(t *testing.T) {
	ctx := context.Background()
	d, _, _ := newTestDeployer(t)
	for _, bad := range []string{"", "com", "a/b.com", "../x.com", "A.com", "a b.com", "a..com", "-a.com"} {
		if _, _, err := d.DNSZoneTokenFor(ctx, bad); err == nil {
			t.Errorf("DNSZoneTokenFor(%q) succeeded", bad)
		}
		if err := d.SaveDNSZoneToken(ctx, bad, dnsSecret); err == nil {
			t.Errorf("SaveDNSZoneToken(%q) succeeded", bad)
		}
		if err := d.DeleteDNSZoneToken(ctx, bad); err == nil {
			t.Errorf("DeleteDNSZoneToken(%q) succeeded", bad)
		}
	}
}

func TestDNSZoneTokenErrorsHoldNoToken(t *testing.T) {
	ctx := context.Background()
	d, _, nomad := newTestDeployer(t)
	nomad.FailVariables(403)
	defer nomad.FailVariables(0)
	err := d.SaveDNSZoneToken(ctx, "example.com", dnsSecret)
	if !errors.Is(err, ErrNomadUnavailable) {
		t.Fatalf("SaveDNSZoneToken() = %v, want ErrNomadUnavailable", err)
	}
	if strings.Contains(err.Error(), dnsSecret) {
		t.Errorf("the error holds the token: %v", err)
	}
}
