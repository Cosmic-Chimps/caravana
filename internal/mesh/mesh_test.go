package mesh

import (
	"encoding/base64"
	"encoding/hex"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestOf(t *testing.T) {
	tests := []struct {
		index                int
		mesh, app, container string
	}{
		{1, "10.98.0.1", "10.99.0.1", "172.26.64.0/20"},
		{2, "10.98.0.2", "10.99.0.2", "10.97.8.0/22"},
		{63, "10.98.0.63", "10.99.0.63", "10.97.252.0/22"},
	}
	for _, tt := range tests {
		a, err := Of(tt.index)
		if err != nil {
			t.Fatalf("Of(%d): %v", tt.index, err)
		}
		if a.Mesh.String() != tt.mesh || a.App.String() != tt.app || a.Containers.String() != tt.container {
			t.Errorf("Of(%d) = %v %v %v, want %s %s %s", tt.index, a.Mesh, a.App, a.Containers, tt.mesh, tt.app, tt.container)
		}
	}
	for _, bad := range []int{0, 64, -1} {
		if _, err := Of(bad); err == nil {
			t.Errorf("Of(%d) was accepted", bad)
		}
	}
}

// No two servers share a container subnet or an address, and none overlaps the first's.
func TestOfNeverOverlaps(t *testing.T) {
	var subnets []netip.Prefix
	for i := 1; i <= MaxIndex; i++ {
		a, _ := Of(i)
		for _, s := range subnets {
			if s.Overlaps(a.Containers) {
				t.Errorf("server %d's %s overlaps %s", i, a.Containers, s)
			}
		}
		subnets = append(subnets, a.Containers)
	}
}

func TestIndexOfApp(t *testing.T) {
	tests := []struct {
		addr string
		want int
		ok   bool
	}{
		{"10.99.0.1", 1, true},
		{"10.99.0.63", 63, true},
		{"::ffff:10.99.0.5", 5, true},
		{"10.99.0.0", 0, false},
		{"10.99.0.64", 0, false},
		{"10.98.0.3", 0, false},
		{"10.99.1.3", 0, false},
		{"2001:db8::1", 0, false},
	}
	for _, tt := range tests {
		got, ok := IndexOfApp(netip.MustParseAddr(tt.addr))
		if got != tt.want || ok != tt.ok {
			t.Errorf("IndexOfApp(%s) = %d, %v; want %d, %v", tt.addr, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRoutes(t *testing.T) {
	join := func(ps []netip.Prefix) string {
		var s []string
		for _, p := range ps {
			s = append(s, p.String())
		}
		return strings.Join(s, " ")
	}
	if got, want := join(Routes(1)), "10.98.0.0/24 10.99.0.0/24 10.97.0.0/16"; got != want {
		t.Errorf("Routes(1) = %s, want %s", got, want)
	}
	if got, want := join(Routes(3)), "10.98.0.0/24 10.99.0.0/24 10.97.0.0/16 172.26.64.0/20"; got != want {
		t.Errorf("Routes(3) = %s, want %s", got, want)
	}
	if Routes(0) != nil || Routes(64) != nil {
		t.Error("an invalid index has routes")
	}
}

// RFC 7748 §6.1: Alice's private key and its public key.
func TestPublicKeyVector(t *testing.T) {
	priv, _ := hex.DecodeString("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
	wantPub, _ := hex.DecodeString("8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a")
	got, err := PublicKey(base64.StdEncoding.EncodeToString(priv))
	if err != nil {
		t.Fatal(err)
	}
	if want := base64.StdEncoding.EncodeToString(wantPub); got != want {
		t.Errorf("PublicKey = %s, want %s", got, want)
	}
}

func TestNewKey(t *testing.T) {
	priv, pub, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(priv) != 44 || !ValidPublicKey(pub) {
		t.Fatalf("keys have the wrong shape: %q %q", priv, pub)
	}
	again, err := PublicKey(priv)
	if err != nil || again != pub {
		t.Errorf("PublicKey(private) = %q, %v; NewKey said %q", again, err, pub)
	}
	raw, _ := base64.StdEncoding.DecodeString(priv)
	if raw[0]&7 != 0 || raw[31]&128 != 0 || raw[31]&64 == 0 {
		t.Error("the private key isn't clamped")
	}
	priv2, _, _ := NewKey()
	if priv == priv2 {
		t.Error("two keys are equal")
	}
}

func TestPublicKeyRefusesWithoutQuoting(t *testing.T) {
	const bad = "s3cret-sentinel-not-a-key"
	_, err := PublicKey(bad)
	if err == nil {
		t.Fatal("accepted a bad key")
	}
	if strings.Contains(err.Error(), bad) {
		t.Errorf("the error holds the key: %v", err)
	}
}

func TestValidPublicKey(t *testing.T) {
	good := "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo="
	tests := []struct {
		in   string
		want bool
	}{
		{good, true},
		{"", false},
		{good[:43], false},
		{good + "A", false},
		{strings.Repeat("A", 43) + "=", true},
		{strings.Repeat("A", 42) + "B=", false}, // spare bits set: not what base64 of 32 bytes writes
		{strings.Repeat("!", 44), false},
		{base64.StdEncoding.EncodeToString(make([]byte, 33))[:44], false},
	}
	for _, tt := range tests {
		if got := ValidPublicKey(tt.in); got != tt.want {
			t.Errorf("ValidPublicKey(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{"203.0.113.4:51820", true},
		{"[2001:db8::1]:51820", true},
		{"[::ffff:203.0.113.4]:51820", true},
		{"fsn-1.example.com:51820", false}, // a name
		{"localhost:51820", false},
		{"127.0.0.1:51820", false},
		{"[::1]:51820", false},
		{"0.0.0.0:51820", false},
		{"[::]:51820", false},
		{"224.0.0.1:51820", false},
		{"[ff02::1]:51820", false},
		{"169.254.1.1:51820", false},
		{"[fe80::1]:51820", false},
		{"[fe80::1%eth0]:51820", false},
		{"203.0.113.4", false}, // no port
		{"203.0.113.4:0", false},
		{"203.0.113.4:70000", false},
		{"", false},
	}
	for _, tt := range tests {
		ap, err := ParseEndpoint(tt.in)
		if (err == nil) != tt.ok {
			t.Errorf("ParseEndpoint(%q) = %v, %v; want ok=%v", tt.in, ap, err, tt.ok)
		}
	}
	if ap, _ := ParseEndpoint("[::ffff:203.0.113.4]:51820"); ap.String() != "203.0.113.4:51820" {
		t.Errorf("a mapped address isn't unmapped: %s", ap)
	}
}

func TestConfigGolden(t *testing.T) {
	const keyA, keyB = "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo=", "3p7bfXt9wbTTW2HC7OQ1Nz+DQ8hbeGdNrfx+FG+IK08="
	peers := []Peer{
		{Index: 3, PublicKey: keyB, Endpoint: "[2001:db8::3]:51820"},
		{Index: 1, PublicKey: keyA, Endpoint: "203.0.113.4:51820"},
		{Index: 2, PublicKey: keyB, Endpoint: "198.51.100.7:51820"},
	}
	got := Config("PRIVATE-KEY-PLACEHOLDER", peers)
	golden := filepath.Join("testdata", "config.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("Config differs from %s:\n%s", golden, got)
	}
	if peers[0].Index != 3 {
		t.Error("Config sorted the caller's slice")
	}
}

func TestConfigSkipsWhatIsInvalid(t *testing.T) {
	const key = "hSDwCYkwp1R0i33ctD73Wg2/Og0mOBr066SpjqqbTmo="
	got := Config("K", []Peer{
		{Index: 0, PublicKey: key, Endpoint: "203.0.113.4:51820"},
		{Index: 2, PublicKey: "x\nPersistentKeepalive = 1", Endpoint: "203.0.113.4:51820"},
		{Index: 3, PublicKey: key, Endpoint: "203.0.113.4:51820\nAllowedIPs = 0.0.0.0/0"},
		{Index: 4, PublicKey: key, Endpoint: "203.0.113.4:51820"},
	})
	if n := strings.Count(got, "[Peer]"); n != 1 || strings.Contains(got, "0.0.0.0/0") || !strings.Contains(got, "10.98.0.4/32") {
		t.Errorf("an invalid peer got through:\n%s", got)
	}
}
