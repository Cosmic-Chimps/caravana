package dns_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/dns"
	"github.com/cosmic-chimps/caravana/internal/dns/dnstest"
)

const sentinel = "cf-s3cret-sentinel-048-dns"

func client(t *testing.T) (*dns.Cloudflare, *dnstest.Server) {
	t.Helper()
	srv := dnstest.NewServer(t, sentinel)
	return dns.NewCloudflare(sentinel, dns.WithBaseURL(srv.URL)), srv
}

func TestCloudflareCalls(t *testing.T) {
	ctx := context.Background()
	c, srv := client(t)
	srv.AddZone("example.com")

	id, err := c.FindZone(ctx, "example.com")
	if err != nil || id == "" {
		t.Fatalf("FindZone() = %q, %v", id, err)
	}
	if _, err := c.FindZone(ctx, "nope.org"); !errors.Is(err, dns.ErrZoneNotFound) {
		t.Errorf("FindZone(unknown) = %v, want ErrZoneNotFound", err)
	}

	made, err := c.Create(ctx, id, dns.Record{Type: "A", Name: "web.example.com", Content: "203.0.113.1", Proxied: false, Comment: "caravana:web"})
	if err != nil || made.ID == "" || made.TTL != 1 || made.Comment != "caravana:web" {
		t.Fatalf("Create() = %+v, %v", made, err)
	}
	srv.Seed("example.com", dns.Record{Type: "A", Name: "hand.example.com", Content: "198.51.100.7"})

	one, err := c.Records(ctx, id, "web.example.com")
	if err != nil || len(one) != 1 || one[0].ID != made.ID {
		t.Fatalf("Records(name) = %+v, %v", one, err)
	}
	all, err := c.Records(ctx, id, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("Records(all) = %+v, %v", all, err)
	}

	made.Proxied = true
	if err := c.Update(ctx, id, made); err != nil {
		t.Fatalf("Update() = %v", err)
	}
	if got := srv.Records("example.com")[0]; !got.Proxied {
		t.Errorf("record after Update = %+v, want proxied", got)
	}
	if err := c.Update(ctx, id, dns.Record{ID: "gone", Proxied: true}); !errors.Is(err, dns.ErrRecordNotFound) {
		t.Errorf("Update(gone) = %v, want ErrRecordNotFound", err)
	}

	if err := c.Delete(ctx, id, made.ID); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	if err := c.Delete(ctx, id, made.ID); err != nil {
		t.Errorf("Delete() of a record already gone = %v, want nil", err)
	}
	if got := srv.Records("example.com"); len(got) != 1 || got[0].Name != "hand.example.com" {
		t.Errorf("records after Delete = %+v", got)
	}
	if _, err := c.Records(ctx, "zone-unknown", ""); !errors.Is(err, dns.ErrZoneNotFound) {
		t.Errorf("Records(unknown zone) = %v, want ErrZoneNotFound", err)
	}
}

func TestCloudflarePagesThroughAZone(t *testing.T) {
	c, srv := client(t)
	srv.AddZone("big.test")
	for i := range 230 {
		srv.Seed("big.test", dns.Record{Type: "A", Name: fmt.Sprintf("h%d.big.test", i), Content: "192.0.2.1"})
	}
	id, _ := c.FindZone(context.Background(), "big.test")
	all, err := c.Records(context.Background(), id, "")
	if err != nil || len(all) != 230 {
		t.Fatalf("Records() = %d records, %v; want 230", len(all), err)
	}
}

func TestCloudflareErrorClasses(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"unauthorized", 401, dns.ErrRefused},
		{"forbidden", 403, dns.ErrRefused},
		{"bad request", 400, dns.ErrRefused},
		{"rate limited", 429, dns.ErrUnavailable},
		{"server error", 500, dns.ErrUnavailable},
		{"bad gateway", 502, dns.ErrUnavailable},
		{"redirect", 302, dns.ErrUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, srv := client(t)
			srv.AddZone("example.com")
			srv.SetStatus(tc.status)
			for name, call := range map[string]func() error{
				"FindZone": func() error { _, err := c.FindZone(ctx, "example.com"); return err },
				"Records":  func() error { _, err := c.Records(ctx, "z", ""); return err },
				"Create":   func() error { _, err := c.Create(ctx, "z", dns.Record{Type: "A", Name: "a", Content: "b"}); return err },
				"Update":   func() error { return c.Update(ctx, "z", dns.Record{ID: "r"}) },
				"Delete":   func() error { return c.Delete(ctx, "z", "r") },
			} {
				err := call()
				if !errors.Is(err, tc.want) {
					t.Errorf("%s with %d = %v, want %v", name, tc.status, err, tc.want)
				}
				if err != nil && (strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), "http")) {
					t.Errorf("%s error holds the token or a URL: %v", name, err)
				}
			}
		})
	}
}

func TestCloudflareWrongTokenIsRefusedWithoutTheToken(t *testing.T) {
	srv := dnstest.NewServer(t, sentinel)
	srv.AddZone("example.com")
	wrong := dns.NewCloudflare("cf-wrong-token", dns.WithBaseURL(srv.URL))
	_, err := wrong.FindZone(context.Background(), "example.com")
	if !errors.Is(err, dns.ErrRefused) {
		t.Fatalf("FindZone with a wrong token = %v, want ErrRefused", err)
	}
	if strings.Contains(err.Error(), "cf-wrong-token") {
		t.Errorf("the error holds the token: %v", err)
	}
}

func TestCloudflareUnreachableHoldsNoURL(t *testing.T) {
	srv := dnstest.NewServer(t, sentinel)
	url := srv.URL
	srv.Close()
	c := dns.NewCloudflare(sentinel, dns.WithBaseURL(url))
	_, err := c.FindZone(context.Background(), "example.com")
	if !errors.Is(err, dns.ErrUnavailable) {
		t.Fatalf("FindZone with no server = %v, want ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), url) || strings.Contains(err.Error(), sentinel) {
		t.Errorf("the error holds the URL or the token: %v", err)
	}
}

func TestCloudflareFactory(t *testing.T) {
	f := dns.CloudflareFactory("http://127.0.0.1:1")
	if _, err := f("cloudflare", "tok"); err != nil {
		t.Errorf("factory(cloudflare) = %v", err)
	}
	if _, err := f("route53", "tok"); err == nil {
		t.Error("factory(route53) = nil error")
	}
	if _, err := f("cloudflare", ""); err == nil {
		t.Error("factory with no token = nil error")
	}
}
