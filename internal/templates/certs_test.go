package templates

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"io"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"golang.org/x/crypto/bcrypt"
)

// Feature 060: certificates made at install, and {{name|bcrypt}}.

func dn(cn string, rest ...string) []DNPart {
	out := []DNPart{{Type: "CN", Value: cn}}
	for i := 0; i+1 < len(rest); i += 2 {
		out = append(out, DNPart{Type: rest[i], Value: rest[i+1]})
	}
	return out
}

// wazuhDN is the subject Wazuh's single-node stack gives its certificates.
func wazuhDN(cn string) []DNPart {
	return dn(cn, "OU", "Wazuh", "O", "Wazuh", "L", "California", "C", "US")
}

// certsTemplate is a small version of the Wazuh template's shape: a service that holds its own certificate and
// key and the authority's as secret files, a password hash in a file, a client certificate for the admin.
func certsTemplate() Template {
	secret := func(s string) *string { return &s }
	return Template{
		Name: "demo", Title: "Demo", Version: "1.0.0-1", Description: "A demo app.", Tags: []string{"demo"},
		Upstream: "https://example.com/demo", AppName: "demo", Requires: "0.24.0",
		Notes: "Open https://{{domain}}.",
		Inputs: []Input{
			{Name: "domain", Label: "Domain", Type: TypeDomain, Example: "demo.example.com"},
			{Name: "admin_password", Label: "Admin password", Type: TypeSecret, Example: "a-long-passphrase"},
		},
		Generated: []Generated{{Name: "api_password", Length: 28, Alphabet: AlphabetAlnum}},
		Certificates: &Certificates{
			CA: CertAuthority{Subject: dn("Demo root CA", "OU", "Wazuh", "O", "Wazuh", "L", "California", "C", "US")},
			Leaves: []CertLeaf{
				{Name: "node", Subject: wazuhDN("node"), DNSNames: []string{"node", "node.internal"}},
				{Name: "admin", Subject: wazuhDN("admin")},
			},
		},
		Services: map[string]compose.Overlay{
			"web": {
				Domains: []compose.Domain{{Host: "{{domain}}", Port: 8080}},
				Health:  &compose.Health{Path: "/healthz"},
				Secrets: []app.Secret{
					{Name: "CA_CERT", Value: secret("{{ca_cert}}")},
					{Name: "NODE_CERT", Value: secret("{{node_cert}}")},
					{Name: "NODE_KEY", Value: secret("{{node_key}}")},
					{Name: "ADMIN_CERT", Value: secret("{{admin_cert}}")},
					{Name: "ADMIN_KEY", Value: secret("{{admin_key}}")},
					{Name: "USERS", Value: secret("admin:\n  hash: \"{{admin_password|bcrypt}}\"\napi:\n  hash: \"{{ api_password | bcrypt }}\"\n  plain: {{api_password}}.Wz1\n")},
				},
				Files: []app.File{
					{Path: "/certs/ca.pem", Secret: "CA_CERT", Mode: "0444"},
					{Path: "/certs/node.pem", Secret: "NODE_CERT", Mode: "0444"},
					{Path: "/certs/node.key", Secret: "NODE_KEY", Mode: "0400"},
					{Path: "/certs/admin.pem", Secret: "ADMIN_CERT"},
					{Path: "/certs/admin.key", Secret: "ADMIN_KEY", Mode: "0400"},
					{Path: "/etc/users.yml", Secret: "USERS", Mode: "0400"},
				},
			},
		},
		Compose: "services:\n  web:\n    image: example/demo:1.0.0\n    expose: [\"8080\"]\n",
	}
}

func generated(t *testing.T, tpl Template) map[string]string {
	t.Helper()
	got, err := tpl.Generate(&counting{}, testNow)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return got
}

func certOf(t *testing.T, text string) *x509.Certificate {
	t.Helper()
	block, rest := pem.Decode([]byte(text))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatalf("not one CERTIFICATE PEM:\n%s", text)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func keyOf(t *testing.T, text string) *rsa.PrivateKey {
	t.Helper()
	if !strings.HasPrefix(text, "-----BEGIN PRIVATE KEY-----\n") {
		t.Fatalf("the key isn't PKCS#8 (BEGIN PRIVATE KEY):\n%.60s", text)
	}
	block, _ := pem.Decode([]byte(text))
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return k.(*rsa.PrivateKey)
}

// rfc2253 is the subject as a Java service reads it: the sequence printed backwards.
func rfc2253(t *testing.T, c *x509.Certificate) string {
	t.Helper()
	var seq pkix.RDNSequence
	if _, err := asn1.Unmarshal(c.RawSubject, &seq); err != nil {
		t.Fatal(err)
	}
	return seq.String()
}

func TestGenerateCertificates(t *testing.T) {
	tpl := certsTemplate()
	got := generated(t, tpl)
	for _, name := range []string{"ca_cert", "node_cert", "node_key", "admin_cert", "admin_key"} {
		if got[name] == "" {
			t.Errorf("no value for %s", name)
		}
	}
	if _, has := got["ca_key"]; has {
		t.Error("the authority's key is kept")
	}
	if strings.Contains(strings.Join(mapValues(got), "\n"), "RSA PRIVATE KEY") {
		t.Error("a key isn't PKCS#8")
	}

	ca := certOf(t, got["ca_cert"])
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Errorf("the authority: IsCA %v, usage %v", ca.IsCA, ca.KeyUsage)
	}
	if ca.SignatureAlgorithm != x509.SHA256WithRSA {
		t.Errorf("the authority is signed with %v", ca.SignatureAlgorithm)
	}
	if err := ca.CheckSignatureFrom(ca); err != nil {
		t.Errorf("the authority is not self-signed: %v", err)
	}
	if !ca.NotBefore.Equal(testNow.Add(-time.Hour)) || !ca.NotAfter.Equal(testNow.AddDate(10, 0, 0)) {
		t.Errorf("the authority is valid %s to %s", ca.NotBefore, ca.NotAfter)
	}

	for _, leaf := range []string{"node", "admin"} {
		cert := certOf(t, got[leaf+"_cert"])
		key := keyOf(t, got[leaf+"_key"])
		if key.N.BitLen() != 2048 {
			t.Errorf("%s: a %d bit key", leaf, key.N.BitLen())
		}
		if !key.PublicKey.Equal(cert.PublicKey) {
			t.Errorf("%s: the key isn't the certificate's", leaf)
		}
		if cert.IsCA {
			t.Errorf("%s is a CA", leaf)
		}
		if cert.SignatureAlgorithm != x509.SHA256WithRSA {
			t.Errorf("%s is signed with %v", leaf, cert.SignatureAlgorithm)
		}
		if !cert.NotBefore.Equal(testNow.Add(-time.Hour)) || !cert.NotAfter.Equal(testNow.AddDate(10, 0, 0)) {
			t.Errorf("%s is valid %s to %s", leaf, cert.NotBefore, cert.NotAfter)
		}
		// The chain verifies for both ends of a connection, at a time inside its life.
		for _, usage := range []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth} {
			opts := x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{usage}, CurrentTime: testNow.AddDate(5, 0, 0)}
			if _, err := cert.Verify(opts); err != nil {
				t.Errorf("%s doesn't verify for usage %v: %v", leaf, usage, err)
			}
		}
		// ...and not after its ten years.
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: testNow.AddDate(10, 0, 1)}); err == nil {
			t.Errorf("%s verifies after it ended", leaf)
		}
	}
	node := certOf(t, got["node_cert"])
	if strings.Join(node.DNSNames, ",") != "node,node.internal" {
		t.Errorf("SANs = %v", node.DNSNames)
	}
	if _, err := node.Verify(x509.VerifyOptions{Roots: roots, DNSName: "node.internal", CurrentTime: testNow}); err != nil {
		t.Errorf("node doesn't verify for its alternative name: %v", err)
	}
	if len(certOf(t, got["admin_cert"]).DNSNames) != 0 {
		t.Error("admin has alternative names")
	}
	if !bytes.Equal(node.AuthorityKeyId, ca.SubjectKeyId) || len(ca.SubjectKeyId) == 0 {
		t.Error("the leaf doesn't name the authority's key")
	}

	// Two installs make different certificates and keys.
	again := generated(t, tpl)
	for _, name := range tpl.Certificates.Names() {
		if got[name] == again[name] {
			t.Errorf("%s is the same on two installs", name)
		}
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// OpenSearch compares admin_dn and nodes_dn with the certificate's subject as text, so each subject comes out
// exactly as the template wrote it.
func TestCertificateSubjectsAreExact(t *testing.T) {
	got := generated(t, certsTemplate())
	for name, want := range map[string]string{
		"admin_cert": "CN=admin,OU=Wazuh,O=Wazuh,L=California,C=US",
		"node_cert":  "CN=node,OU=Wazuh,O=Wazuh,L=California,C=US",
		"ca_cert":    "CN=Demo root CA,OU=Wazuh,O=Wazuh,L=California,C=US",
	} {
		c := certOf(t, got[name])
		if s := c.Subject.String(); s != want {
			t.Errorf("%s: Subject.String() = %q, want %q", name, s, want)
		}
		if s := rfc2253(t, c); s != want {
			t.Errorf("%s: as a Java service reads it: %q, want %q", name, s, want)
		}
		if name != "ca_cert" && c.Issuer.String() != "CN=Demo root CA,OU=Wazuh,O=Wazuh,L=California,C=US" {
			t.Errorf("%s: issuer = %q", name, c.Issuer)
		}
	}

	// The order written is the order read, also when it isn't Go's own, and a state is kept.
	tpl := certsTemplate()
	tpl.Certificates.Leaves[1].Subject = []DNPart{{"C", "ES"}, {"ST", "Madrid"}, {"O", "Org, Inc."}, {"CN", "odd"}}
	c := certOf(t, generated(t, tpl)["admin_cert"])
	if s, want := rfc2253(t, c), `C=ES,ST=Madrid,O=Org\, Inc.,CN=odd`; s != want {
		t.Errorf("subject = %q, want %q", s, want)
	}
	if c.Subject.CommonName != "odd" || c.Subject.Country[0] != "ES" || c.Subject.Province[0] != "Madrid" || c.Subject.Organization[0] != "Org, Inc." {
		t.Errorf("fields = %+v", c.Subject)
	}
}

func TestCertificateFormatErrors(t *testing.T) {
	str := func(s string) *string { return &s }
	tests := []struct {
		name   string
		change func(*Template)
		want   string
	}{
		{"no CN in the authority", func(x *Template) { x.Certificates.CA.Subject = []DNPart{{"O", "Wazuh"}} }, "demo: certificates.ca.subject: needs a CN"},
		{"an empty authority subject", func(x *Template) { x.Certificates.CA.Subject = nil }, "demo: certificates.ca.subject:"},
		{"no CN in a leaf", func(x *Template) { x.Certificates.Leaves[1].Subject = []DNPart{{"O", "Wazuh"}} }, "demo: certificates.leaves[1].subject: needs a CN"},
		{"a bad type", func(x *Template) { x.Certificates.Leaves[0].Subject[1].Type = "DC" }, "demo: certificates.leaves[0].subject[1].type:"},
		{"a type twice", func(x *Template) {
			x.Certificates.Leaves[0].Subject = append(x.Certificates.Leaves[0].Subject, DNPart{"OU", "again"})
		}, "demo: certificates.leaves[0].subject[5].type: OU is listed twice"},
		{"an empty value", func(x *Template) { x.Certificates.CA.Subject[0].Value = "" }, "demo: certificates.ca.subject[0].value:"},
		{"a long value", func(x *Template) { x.Certificates.CA.Subject[0].Value = strings.Repeat("a", 65) }, "demo: certificates.ca.subject[0].value:"},
		{"a value with a line break", func(x *Template) { x.Certificates.CA.Subject[0].Value = "a\nb" }, "demo: certificates.ca.subject[0].value:"},
		{"a country in lower case", func(x *Template) { x.Certificates.Leaves[0].Subject[4].Value = "us" }, "demo: certificates.leaves[0].subject[4].value:"},
		{"a leaf name with capitals", func(x *Template) { x.Certificates.Leaves[0].Name = "Node" }, "demo: certificates.leaves[0].name:"},
		{"a leaf name too long", func(x *Template) { x.Certificates.Leaves[0].Name = strings.Repeat("a", 25) }, "demo: certificates.leaves[0].name:"},
		{"a leaf name twice", func(x *Template) { x.Certificates.Leaves[1].Name = "node" }, "demo: certificates.leaves[1].name:"},
		{"a leaf named as an input", func(x *Template) {
			x.Inputs = append(x.Inputs, Input{Name: "node_key", Label: "K", Type: TypeText, Pattern: "x", Example: "x"})
		}, "demo: certificates.leaves[0].name: makes \"node_key\""},
		{"a leaf named as a generated secret", func(x *Template) { x.Generated[0].Name = "admin_cert" }, "demo: certificates.leaves[1].name: makes \"admin_cert\""},
		{"a leaf named ca", func(x *Template) { x.Certificates.Leaves[0].Name = "ca" }, "demo: certificates.leaves[0].name: makes \"ca_cert\""},
		{"nine leaves", func(x *Template) {
			for i := 0; i < 7; i++ {
				x.Certificates.Leaves = append(x.Certificates.Leaves, CertLeaf{Name: "l" + string(rune('a'+i)), Subject: dn("x")})
			}
		}, "demo: certificates.leaves: has 9 leaves"},
		{"a DNS name with capitals", func(x *Template) { x.Certificates.Leaves[0].DNSNames = []string{"Node"} }, "demo: certificates.leaves[0].dnsNames[0]:"},
		{"a DNS name with a space", func(x *Template) { x.Certificates.Leaves[0].DNSNames = []string{"a b"} }, "demo: certificates.leaves[0].dnsNames[0]:"},
		{"nine DNS names", func(x *Template) {
			x.Certificates.Leaves[0].DNSNames = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		}, "demo: certificates.leaves[0].dnsNames:"},
		{"a leaf never used", func(x *Template) {
			x.Certificates.Leaves = append(x.Certificates.Leaves, CertLeaf{Name: "spare", Subject: dn("spare")})
		}, "demo: certificates.leaves[2].name: makes spare_cert, which is never used"},
		{"the authority never used", func(x *Template) {
			ov := x.Services["web"]
			ov.Secrets = ov.Secrets[1:]
			ov.Files = ov.Files[1:]
			x.Services["web"] = ov
		}, "demo: certificates.ca: makes ca_cert, which is never used"},
		{"a key in the Compose file", func(x *Template) { x.Compose += "# {{node_key}}\n" }, "demo: compose: uses the secret {{node_key}}"},
		{"a certificate in a domain", func(x *Template) {
			ov := x.Services["web"]
			ov.Domains = []compose.Domain{{Host: "{{node_cert}}", Port: 8080}}
			x.Services["web"] = ov
		}, "demo: services.web.domains[0].host: uses the secret {{node_cert}}"},
		{"a key in the notes", func(x *Template) { x.Notes = "{{node_key}}" }, "demo: notes: uses the secret {{node_key}}"},
		{"a key in a file's content", func(x *Template) {
			ov := x.Services["web"]
			ov.Files = append(ov.Files, app.File{Path: "/x", Content: "{{node_key}}"})
			x.Services["web"] = ov
		}, "demo: services.web.files[6].content: uses the secret {{node_key}}"},
		{"a hash of a certificate", func(x *Template) {
			ov := x.Services["web"]
			ov.Secrets = append(ov.Secrets, app.Secret{Name: "H", Value: str("{{node_cert|bcrypt}}")})
			x.Services["web"] = ov
		}, "demo: services.web.secrets[6].value: uses {{node_cert|bcrypt}}: a certificate's PEM can't be hashed"},
		{"no requires", func(x *Template) { x.Requires = "" }, "demo: requires: is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := certsTemplate()
			tt.change(&x)
			err := x.Validate()
			if err == nil {
				t.Fatal("the template was accepted")
			}
			var te *Error
			if !asError(err, &te) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	if err := certsTemplate().Validate(); err != nil {
		t.Errorf("the good template: %v", err)
	}
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

func TestBcryptFormatErrors(t *testing.T) {
	str := func(s string) *string { return &s }
	tests := []struct {
		name   string
		change func(*Template)
		want   string
	}{
		{"an unknown filter", func(x *Template) {
			ov := x.Services["web"]
			ov.Secrets = append(ov.Secrets, app.Secret{Name: "H", Value: str("{{admin_password|md5}}")})
			x.Services["web"] = ov
		}, `demo: services.web.secrets[6].value: uses the filter "md5" on {{admin_password}}; the only filter is bcrypt`},
		{"a filter on a plain input", func(x *Template) {
			ov := x.Services["web"]
			ov.Secrets = append(ov.Secrets, app.Secret{Name: "H", Value: str("{{domain|bcrypt}}")})
			x.Services["web"] = ov
		}, "demo: services.web.secrets[6].value: uses {{domain|bcrypt}}, but domain is a plain input"},
		{"a filter on an unknown name", func(x *Template) {
			ov := x.Services["web"]
			ov.Secrets = append(ov.Secrets, app.Secret{Name: "H", Value: str("{{nope|bcrypt}}")})
			x.Services["web"] = ov
		}, "demo: services.web.secrets[6].value: uses {{nope}}, which is not an input or a generated secret"},
		{"a hash in the Compose file", func(x *Template) { x.Compose += "# {{admin_password|bcrypt}}\n" }, "demo: compose: uses the secret {{admin_password}}"},
		{"a hash in a domain", func(x *Template) {
			ov := x.Services["web"]
			ov.Domains = []compose.Domain{{Host: "{{admin_password|bcrypt}}", Port: 8080}}
			x.Services["web"] = ov
		}, "demo: services.web.domains[0].host: uses the secret {{admin_password}}"},
		{"a hash in a file's content", func(x *Template) {
			ov := x.Services["web"]
			ov.Files = append(ov.Files, app.File{Path: "/x", Content: "{{admin_password|bcrypt}}"})
			x.Services["web"] = ov
		}, "demo: services.web.files[6].content: uses the secret {{admin_password}}"},
		{"a filter in the notes", func(x *Template) { x.Notes = "{{domain|bcrypt}}" }, "demo: notes: uses a filter"},
		{"a hashed generated secret that is too long", func(x *Template) { x.Generated[0].Length = 80 }, "demo: generated[0].length: is 80, but {{api_password|bcrypt}} hashes it and bcrypt reads 72 bytes at most"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := certsTemplate()
			tt.change(&x)
			err := x.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}

	// A template that only hashes (no certificates) needs `requires` too.
	x := certsTemplate()
	x.Certificates = nil
	ov := x.Services["web"]
	ov.Secrets = []app.Secret{{Name: "USERS", Value: str("{{admin_password|bcrypt}}")}}
	ov.Files = []app.File{{Path: "/etc/users.yml", Secret: "USERS"}}
	x.Services["web"] = ov
	x.Generated = nil
	if err := x.Validate(); err != nil {
		t.Fatalf("a template that hashes: %v", err)
	}
	x.Requires = ""
	if err := x.Validate(); err == nil || !strings.Contains(err.Error(), "demo: requires: is required") {
		t.Errorf("a hash without requires: %v", err)
	}
}

// {{name}} keeps working however it is spaced, and so does the filter.
func TestPlaceholderSpacing(t *testing.T) {
	values := map[string]string{"a": "x", "b": "pw"}
	hash := func(s string) (string, error) { return "H(" + s + ")", nil }
	got := substitute("{{a}} {{ a }} {{a|bcrypt}} {{ b | bcrypt }} {{b |bcrypt}}", values, func(string) string { return "?" }, hash, func(string, error) {})
	if want := "x x H(x) H(pw) H(pw)"; got != want {
		t.Errorf("substitute = %q, want %q", got, want)
	}
	// Text that isn't a placeholder stays text.
	if got := substitute("{{ not a placeholder }} {{.Name}} {{a.b}}", values, func(string) string { return "?" }, hash, nil); got != "{{ not a placeholder }} {{.Name}} {{a.b}}" {
		t.Errorf("substitute changed %q", got)
	}
}

func installValues(t *testing.T, tpl Template, inputs map[string]string) map[string]string {
	t.Helper()
	checked, err := tpl.CheckInputs(inputs)
	if err != nil {
		t.Fatal(err)
	}
	values := generated(t, tpl)
	for k, v := range checked {
		values[k] = v
	}
	return values
}

func TestRenderCertificatesAndHashes(t *testing.T) {
	tpl := certsTemplate()
	values := installValues(t, tpl, map[string]string{"domain": "Demo.Example.com", "admin_password": "correct horse battery"})
	req, err := tpl.Render(values)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	web := req.Services["web"]
	value := func(name string) string {
		for _, s := range web.Secrets {
			if s.Name == name {
				return *s.Value
			}
		}
		t.Fatalf("no secret %s", name)
		return ""
	}
	if value("NODE_KEY") != values["node_key"] || value("CA_CERT") != values["ca_cert"] || !strings.HasPrefix(value("NODE_KEY"), "-----BEGIN PRIVATE KEY-----") {
		t.Error("a certificate or key isn't in its secret")
	}
	// The kept Compose file and the files' content hold no key or certificate.
	for _, text := range []string{req.Compose, mustMarshalString(t, web.Files)} {
		if strings.Contains(text, "BEGIN") || strings.Contains(text, "PRIVATE KEY") {
			t.Errorf("a PEM is in the file or the paths:\n%s", text)
		}
	}
	for _, f := range web.Files {
		if f.Secret == "" || f.Content != "" {
			t.Errorf("file %+v holds content", f)
		}
	}

	users := value("USERS")
	var hashes []string
	for _, line := range strings.Split(users, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "hash:") {
			h := strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "hash:")), `"`)
			hashes = append(hashes, h)
		}
	}
	if len(hashes) != 2 || !strings.HasPrefix(hashes[0], "$2y$12$") || !strings.HasPrefix(hashes[1], "$2y$12$") || len(hashes[0]) != 60 {
		t.Fatalf("hashes = %q in:\n%s", hashes, users)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashes[0]), []byte("correct horse battery")); err != nil {
		t.Errorf("the admin hash doesn't match the password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashes[1]), []byte(values["api_password"])); err != nil {
		t.Errorf("the api hash doesn't match the generated password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashes[0]), []byte("another")); err == nil {
		t.Error("the hash matches another password")
	}
	if !strings.Contains(users, "plain: "+values["api_password"]+".Wz1") {
		t.Errorf("the plain use of a hashed secret isn't kept:\n%s", users)
	}
	// Salted: the same password makes another hash.
	req2, _ := tpl.Render(values)
	if *req2.Services["web"].Secrets[5].Value == users {
		t.Error("two renders made the same hashes")
	}
}

func mustMarshalString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A secret input that bcrypt can't read all of is refused at the input, with the limit said.
func TestHashedInputIsAtMost72Bytes(t *testing.T) {
	tpl := certsTemplate()
	ok := strings.Repeat("a", 72)
	if _, err := tpl.CheckInputs(map[string]string{"domain": "a.example.com", "admin_password": ok}); err != nil {
		t.Errorf("72 bytes: %v", err)
	}
	_, err := tpl.CheckInputs(map[string]string{"domain": "a.example.com", "admin_password": ok + "b"})
	fe, isField := err.(*app.FieldError)
	if !isField || fe.Field != "inputs.admin_password" || !strings.Contains(fe.Message, "at most 72") || strings.Contains(fe.Message, ok) {
		t.Errorf("73 bytes: %v", err)
	}
	// An input that isn't hashed keeps its own limit.
	plain := goodTemplate()
	if _, err := plain.CheckInputs(map[string]string{"domain": "a.example.com", "admin": "you@example.com", "api_key": ok + "bcdefgh"}); err != nil {
		t.Errorf("an input that isn't hashed: %v", err)
	}
}

// A preview shows names only: it hashes nothing and makes no certificate.
func TestPreviewMakesNoKeysOrHashes(t *testing.T) {
	tpl := certsTemplate()
	checked, err := tpl.CheckPreviewInputs(map[string]string{"domain": "a.example.com", "admin_password": "typed"})
	if err != nil {
		t.Fatal(err)
	}
	pv := tpl.PreviewValues(checked)
	for _, name := range tpl.Certificates.Names() {
		if v, ok := pv[name]; !ok || v != "" {
			t.Errorf("preview %s = %q, %v", name, v, ok)
		}
	}
	start := time.Now()
	req, err := tpl.RenderPreview(pv)
	if err != nil {
		t.Fatalf("RenderPreview: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the preview took %s: it hashed", time.Since(start))
	}
	// The hash is a stand-in: not the hash of what was typed.
	users := *req.Services["web"].Secrets[5].Value
	if err := bcrypt.CompareHashAndPassword([]byte(strings.Split(strings.Split(users, `"`)[1], `"`)[0]), []byte("typed")); err == nil {
		t.Error("the preview holds the hash of the typed password")
	}
}

// Check renders with stand-ins, so a template with eight leaves and hashes in many places checks in well under
// the time one real bcrypt hash takes at cost 12 times the number of cases.
func TestCheckStaysFast(t *testing.T) {
	tpl := certsTemplate()
	for i := 0; i < 6; i++ {
		name := "extra" + string(rune('a'+i))
		tpl.Certificates.Leaves = append(tpl.Certificates.Leaves, CertLeaf{Name: name, Subject: dn(name)})
		ov := tpl.Services["web"]
		ov.Secrets = append(ov.Secrets, app.Secret{Name: strings.ToUpper(name) + "_CERT", Value: new("{{" + name + "_cert}}")}, app.Secret{Name: strings.ToUpper(name) + "_KEY", Value: new("{{" + name + "_key}}")})
		tpl.Services["web"] = ov
	}
	start := time.Now()
	if err := Check(tpl, CheckSettings()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Check took %s", d)
	}
}

// The embedded randomness exception: certificates and hashes are made even when the reader is empty, and are
// never read from it.
func TestGenerateDoesNotNeedTheReaderForCertificates(t *testing.T) {
	tpl := certsTemplate()
	tpl.Generated = nil
	ov := tpl.Services["web"]
	ov.Secrets = ov.Secrets[:5]
	ov.Files = ov.Files[:5]
	tpl.Services["web"] = ov
	got, err := tpl.Generate(io.LimitReader(&counting{}, 0), testNow)
	if err != nil || got["node_key"] == "" {
		t.Errorf("Generate with an empty reader: %v", err)
	}
}

// A template with certificates or a hash is format 2 only: catalog.json (format 1) leaves it out, and the format-1
// rules refuse it by name.
func TestCertificatesAndHashesAreNotInCatalogV1(t *testing.T) {
	fsys := sourceOf(t)
	tpl := certsTemplate()
	tpl.Name, tpl.AppName = "wz", "wz"
	stripped := tpl
	stripped.Compose = ""
	manifest, err := json.MarshalIndent(stripped, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	fsys["templates/wz/template.json"] = &fstest.MapFile{Data: manifest}
	fsys["templates/wz/compose.yaml"] = &fstest.MapFile{Data: []byte(tpl.Compose)}
	b, err := ReadSource(fsys, "2026.11.27")
	if err != nil {
		t.Fatalf("ReadSource: %v", err)
	}
	if strings.Join(b.OnlyV2, ",") != "wz" {
		t.Errorf("OnlyV2 = %v, want wz", b.OnlyV2)
	}
	if strings.Contains(string(b.V1), `"certificates"`) || strings.Contains(string(b.V1), `|bcrypt`) || strings.Contains(string(b.V1), `"wz"`) {
		t.Error("catalog.json holds the template")
	}
	if err := legacyParse(b.V1); err != nil {
		t.Errorf("the legacy parser refuses catalog.json: %v", err)
	}
	v2, err := ParseCatalog(b.V2)
	if err != nil {
		t.Fatalf("catalog.v2.json: %v", err)
	}
	got, ok := v2.Find("wz")
	if !ok || got.Certificates == nil || len(got.Certificates.Leaves) != 2 {
		t.Fatalf("catalog.v2.json lacks the template or its certificates: %+v", got)
	}
	if err := legacyParse(b.V2); err == nil {
		t.Error("the legacy parser accepted catalog.v2.json")
	}

	// The format-1 rules name what they refuse.
	if err := tpl.validate(FormatV1); err == nil || !strings.Contains(err.Error(), "certificates: are catalog format 2") {
		t.Errorf("format 1 with certificates: %v", err)
	}
	x := certsTemplate()
	x.Certificates = nil
	ov := x.Services["web"]
	ov.Secrets = []app.Secret{{Name: "USERS", Value: new("{{admin_password|bcrypt}}")}}
	ov.Files = []app.File{{Path: "/etc/users.yml", Secret: "USERS"}}
	x.Services["web"] = ov
	x.Generated = nil
	if err := x.validate(FormatV1); err == nil || !strings.Contains(err.Error(), "a filter is catalog format 2") {
		t.Errorf("format 1 with a hash: %v", err)
	}
}

// A newer Caravana's catalog is read by this one, with the template listed as unsupported when a field is unknown:
// an unknown field inside certificates is such a field.
func TestUnknownFieldInCertificatesMakesAStub(t *testing.T) {
	tpl := certsTemplate()
	b, _ := json.Marshal(tpl)
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	raw["certificates"].(map[string]any)["ca"].(map[string]any)["keyBits"] = 4096
	one, _ := json.Marshal(raw)
	bundle := `{"format":2,"version":"2026.11.27","templates":[` + string(one) + `]}`
	c, err := ParseCatalog([]byte(bundle))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	got, _ := c.Find("demo")
	if got.Unsupported == "" || !strings.Contains(got.Unsupported, "newer caravana") {
		t.Errorf("template = %+v, want a stub", got)
	}
}
