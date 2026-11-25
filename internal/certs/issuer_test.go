package certs

import (
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/certs/certstest"
)

func TestValidServer(t *testing.T) {
	good := []string{
		"https://dv.acme-v02.api.pki.goog/directory",
		"https://acme.zerossl.com/v2/DV90",
		"https://acme.digicert.com/v2/acme/directory/",
		"https://ca.internal:9000/acme/acme/directory",
		"https://10.0.0.5/dir?x=1",
	}
	for _, s := range good {
		if err := ValidServer(s); err != nil {
			t.Errorf("ValidServer(%q) = %v", s, err)
		}
	}
	bad := map[string]string{
		"":                                              "required",
		"http://acme.test/directory":                    "https",
		"ftp://acme.test/":                              "https",
		"acme.test/directory":                           "https",
		"https:///directory":                            "no host",
		"https://user:pw@acme.test/d":                   "user name",
		"https://user@acme.test/d":                      "user name",
		"https://acme.test/d#frag":                      "fragment",
		"https://acme.test/d e":                         "printable",
		"https://acme.test/d\n":                         "printable",
		"https://acme.test/\"d":                         "may not contain",
		"https://acme.test/'d":                          "may not contain",
		"https://acme.test/d\\x":                        "may not contain",
		"https://acme.test/{d}":                         "may not contain",
		"https://acme.test/$d":                          "may not contain",
		"https://acme.test/`d`":                         "may not contain",
		"https://acme.test/é":                           "printable",
		"https://acme.test/" + strings.Repeat("a", 500): "512",
	}
	for s, part := range bad {
		err := ValidServer(s)
		if err == nil {
			t.Errorf("ValidServer(%q) was accepted", s)
			continue
		}
		wantError(t, err, "server", part)
	}
	// 512 exactly is fine.
	if err := ValidServer("https://acme.test/" + strings.Repeat("a", 512-len("https://acme.test/"))); err != nil {
		t.Errorf("a 512-character address: %v", err)
	}
}

func TestValidEABKeyID(t *testing.T) {
	for _, s := range []string{"a", "kid-1", "AbC.d_e~f+g/h=i-0", strings.Repeat("k", 256)} {
		if err := ValidEABKeyID(s); err != nil {
			t.Errorf("ValidEABKeyID(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"", strings.Repeat("k", 257), "a b", "a\nb", "a\"b", "a$b", "é", "a;b", "a{b"} {
		err := ValidEABKeyID(s)
		if err == nil {
			t.Errorf("ValidEABKeyID(%q) was accepted", s)
			continue
		}
		wantError(t, err, "eabKeyId")
	}
}

func TestNormalizeHMAC(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"AAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAA"},                                           // 16 zero bytes, base64url, no padding
		{"AAAAAAAAAAAAAAAAAAAAAA==", "AAAAAAAAAAAAAAAAAAAAAA"},                                         // padded
		{"  AAAAAAAAAAAAAAAAAAAAAA\n", "AAAAAAAAAAAAAAAAAAAAAA"},                                       // whitespace around
		{"-_-_-_-_-_-_-_-_-_-_-w", "-_-_-_-_-_-_-_-_-_-_-w"},                                           // already base64url
		{"+/+/+/+/+/+/+/+/+/+/+w", "-_-_-_-_-_-_-_-_-_-_-w"},                                           // standard alphabet, converted
		{"+/+/+/+/+/+/+/+/+/+/+w==", "-_-_-_-_-_-_-_-_-_-_-w"},                                         // standard, padded
		{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}, // 32 bytes
	}
	for _, tt := range tests {
		got, err := NormalizeHMAC(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("NormalizeHMAC(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{"", "AAAA", "AAAAAAAAAAAAAAA", "not base64!!!!!!!!!!!!!!!", "AAAAAAAAAAAA AAAAAAAAAA"} {
		_, err := NormalizeHMAC(in)
		if err == nil {
			t.Errorf("NormalizeHMAC(%q) was accepted", in)
			continue
		}
		wantError(t, err, "eabHmacKey")
		if strings.Contains(err.Error(), in) && in != "" {
			t.Errorf("the error repeats the key: %v", err)
		}
	}
}

func TestNormalizeCAPEM(t *testing.T) {
	a := certstest.NewCA(t, "CA One")
	b := certstest.NewCA(t, "CA Two")
	one := string(a.PEM())
	two := string(certstest.Chain(a.Cert, b.Cert))

	got, err := NormalizeCAPEM(one)
	if err != nil || got != one {
		t.Errorf("one certificate = %q, %v", got, err)
	}
	got, err = NormalizeCAPEM("\n\n" + two + "\n\n")
	if err != nil || got != two {
		t.Errorf("two certificates with whitespace = %q, %v; want them re-encoded", got, err)
	}
	// Nothing but whitespace may surround the blocks, and a block must start its line: pem.Decode
	// would skip the rest, and a certificate would be dropped quietly.
	for _, in := range []string{"comment\n" + one, "  " + two, one + "comment\n"} {
		if _, err := NormalizeCAPEM(in); err == nil {
			t.Errorf("NormalizeCAPEM(%.40q...) was accepted", in)
		}
	}

	bad := map[string]string{
		"":           "no certificate",
		"   \n":      "no certificate",
		"hello":      "isn't a PEM certificate",
		one + "tail": "isn't a PEM certificate",
		"-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n":                   "can't be read",
		string(a.PEM()) + "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n": "PRIVATE KEY",
		strings.Repeat(one, 100): "32 KiB",
	}
	for in, part := range bad {
		_, err := NormalizeCAPEM(in)
		if err == nil {
			t.Errorf("NormalizeCAPEM(%.30q...) was accepted", in)
			continue
		}
		wantError(t, err, "caCertificate", part)
	}
}
