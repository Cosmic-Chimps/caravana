package edge

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
)

func TestCheck(t *testing.T) {
	const host = "shop.example.com"
	now := time.Now().UTC().Truncate(time.Second)
	ca := edgetest.NewCA(t, "Let's Encrypt")
	leaf := ca.Issue(t, []string{host}, now.Add(-day), now.Add(60*day))

	r := Set{
		edgetest.FakeResolver{Hosts: map[string][]string{host: {"203.0.113.7"}}},
		edgetest.FakeResolver{Hosts: map[string][]string{host: {"203.0.113.7"}}},
	}
	d := &edgetest.FakeDialer{Chains: map[string][]*x509.Certificate{host: {leaf}}, RootCAs: ca.Pool()}

	f := Check(context.Background(), r, d, "127.0.0.1:443", host, addrs("203.0.113.7"), true, 14*day, now)
	if f.DNS.Status != DNSOK || f.Certificate.Status != CertIssued || !f.CheckedAt.Equal(now) {
		t.Fatalf("facts = %+v", f)
	}
	if got := StateOf(f, true, false); got != StateLive {
		t.Errorf("state = %q, want live", got)
	}
	if dialed := d.Dialed(); len(dialed) != 1 || dialed[0] != "127.0.0.1:443 "+host {
		t.Errorf("dialed = %v, want the edge with the host as server name", dialed)
	}

	// A host Traefik doesn't know: its own certificate, and no DNS record.
	d = &edgetest.FakeDialer{Default: []*x509.Certificate{edgetest.TraefikDefault(t)}}
	f = Check(context.Background(), edgetest.FakeResolver{}, d, "127.0.0.1:443", host, addrs("203.0.113.7"), true, 14*day, now)
	if got := StateOf(f, true, false); got != StateCertPending {
		t.Errorf("state = %q, want cert_pending (%+v)", got, f)
	}
}

// Both checks fail without Check returning anything but facts.
func TestCheckFailures(t *testing.T) {
	d := &edgetest.FakeDialer{} // refuses every connection
	f := Check(context.Background(), Set{}, d, "127.0.0.1:443", "a.example.com", nil, false, 14*day, time.Now())
	if f.DNS.Status != DNSUnknown || f.Certificate.Status != CertError {
		t.Errorf("facts = %+v, want unknown and error", f)
	}
	if got := StateOf(f, true, false); got != StateUnknown {
		t.Errorf("state = %q, want unknown", got)
	}
}

func TestTLSDialer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	d := TLSDialer{Timeout: time.Second}
	state, err := d.DialTLS(context.Background(), srv.Listener.Addr().String(), "shop.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.PeerCertificates) == 0 {
		t.Fatal("no certificate presented")
	}
	// The name doesn't match and the CA isn't trusted, and the handshake still
	// finished: Check judges that, not the dialer.
	got := classifyCertificate(state.PeerCertificates, d.Roots(), "shop.example.com", true, 14*day, time.Now())
	if got.Status != CertError {
		t.Errorf("status = %q, want error", got.Status)
	}

	// A port nobody listens on.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	if _, err := d.DialTLS(context.Background(), addr, "shop.example.com"); err == nil {
		t.Error("dial of a closed port succeeded")
	}
}

func TestSystemAndPublicResolvers(t *testing.T) {
	set := SystemAndPublicResolvers()
	if len(set) != 3 {
		t.Errorf("%d resolvers, want the system one and two public ones", len(set))
	}
}
