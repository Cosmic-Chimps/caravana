package templates

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Certificates a template asks Caravana to make at install (feature 060): one certificate authority and
// the certificates it signs, for services that talk to each other over TLS with their own CA (OpenSearch,
// Wazuh). Each yields secret-valued names:
//
//	ca_cert            the authority's certificate (PEM)
//	<leaf>_cert        a leaf's certificate (PEM)
//	<leaf>_key         a leaf's private key (PKCS#8 PEM, "-----BEGIN PRIVATE KEY-----")
//
// Like a generated secret they may stand only in an overlay secret's value, and they exist only in the map
// Generate returns. The authority's key is thrown away once it has signed: nothing can issue under it later.
// Keys are RSA 2048 and certificates last ten years, SHA-256.
type Certificates struct {
	CA     CertAuthority `json:"ca"`
	Leaves []CertLeaf    `json:"leaves,omitempty"`
}

// CertAuthority is the certificate authority.
type CertAuthority struct {
	Subject []DNPart `json:"subject"`
}

// CertLeaf is one certificate signed by the authority. It has the server and the client key usage, so one
// certificate serves both ends of a connection. DNSNames are its subject alternative names.
type CertLeaf struct {
	// Name is a lowercase identifier: the made names are <name>_cert and <name>_key.
	Name     string   `json:"name"`
	Subject  []DNPart `json:"subject"`
	DNSNames []string `json:"dnsNames,omitempty"`
}

// DNPart is one attribute of a distinguished name. A subject is a list of them in the order the name is
// written (the common name first: "CN=admin,OU=Wazuh,O=Wazuh,L=California,C=US"), which is the order a Java
// service such as OpenSearch compares it in.
type DNPart struct {
	// Type is CN, OU, O, L, ST or C.
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Limits of the certificates.
const (
	maxLeaves     = 8
	maxLeafName   = 24 // so that <name>_key is a valid name
	maxDNSNames   = 8
	maxDNValue    = 64
	certYears     = 10
	certKeyBits   = 2048
	certBackdated = time.Hour // for a server whose clock is a little behind
)

var (
	leafNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)
	dnsNameRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
)

// dnOIDs are the attribute types a subject may use.
var dnOIDs = map[string]asn1.ObjectIdentifier{
	"CN": {2, 5, 4, 3},
	"OU": {2, 5, 4, 11},
	"O":  {2, 5, 4, 10},
	"L":  {2, 5, 4, 7},
	"ST": {2, 5, 4, 8},
	"C":  {2, 5, 4, 6},
}

// Names are the names the certificates make, in a fixed order: the authority's, then each leaf's
// certificate and key.
func (c Certificates) Names() []string {
	out := []string{"ca_cert"}
	for _, l := range c.Leaves {
		out = append(out, l.Name+"_cert", l.Name+"_key")
	}
	return out
}

// madeNames lists the names the certificates make with the path of what makes each.
func (c Certificates) madeNames() (names, fields []string) {
	names, fields = []string{"ca_cert"}, []string{"certificates.ca"}
	for i, l := range c.Leaves {
		f := fmt.Sprintf("certificates.leaves[%d].name", i)
		names = append(names, l.Name+"_cert", l.Name+"_key")
		fields = append(fields, f, f)
	}
	return names, fields
}

// checkSubject checks a subject; field is its path.
func checkSubject(t Template, field string, subject []DNPart) error {
	if len(subject) == 0 {
		return defect(t, field, "needs a subject with at least a CN")
	}
	seen := map[string]bool{}
	for i, p := range subject {
		f := fmt.Sprintf("%s[%d]", field, i)
		if _, ok := dnOIDs[p.Type]; !ok {
			return defect(t, f+".type", "%q must be one of CN, OU, O, L, ST, C", p.Type)
		}
		if seen[p.Type] {
			return defect(t, f+".type", "%s is listed twice", p.Type)
		}
		seen[p.Type] = true
		if n := len([]rune(p.Value)); n < 1 || n > maxDNValue || strings.ContainsFunc(p.Value, func(r rune) bool { return !unicode.IsPrint(r) }) {
			return defect(t, f+".value", "must be 1 to %d printable characters", maxDNValue)
		}
		if p.Type == "C" && !(len(p.Value) == 2 && p.Value == strings.ToUpper(p.Value) && isLetters(p.Value)) {
			return defect(t, f+".value", "a country is two capital letters, like US")
		}
	}
	if !seen["CN"] {
		return defect(t, field, "needs a CN")
	}
	return nil
}

func isLetters(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 'A' || r > 'Z' })
}

// validate checks the certificates block of a template and returns the names it makes. The names are
// the caller's to check against the template's others.
func (c Certificates) validate(t Template) error {
	if err := checkSubject(t, "certificates.ca.subject", c.CA.Subject); err != nil {
		return err
	}
	if len(c.Leaves) > maxLeaves {
		return defect(t, "certificates.leaves", "has %d leaves; at most %d are allowed", len(c.Leaves), maxLeaves)
	}
	for i, l := range c.Leaves {
		f := fmt.Sprintf("certificates.leaves[%d]", i)
		if !leafNameRE.MatchString(l.Name) {
			return defect(t, f+".name", "%q must be lowercase letters, digits and _, starting with a letter, at most %d characters", l.Name, maxLeafName)
		}
		if err := checkSubject(t, f+".subject", l.Subject); err != nil {
			return err
		}
		if len(l.DNSNames) > maxDNSNames {
			return defect(t, f+".dnsNames", "has %d names; at most %d are allowed", len(l.DNSNames), maxDNSNames)
		}
		for j, n := range l.DNSNames {
			if len(n) > 253 || !dnsNameRE.MatchString(n) {
				return defect(t, fmt.Sprintf("%s.dnsNames[%d]", f, j), "%q must be a lowercase DNS name", n)
			}
		}
	}
	return nil
}

// rdnSequence is the subject as ASN.1 holds it: the attributes in the reverse of the order they are
// written, so that RFC 2253's text (which prints the sequence backwards) reads as written.
func rdnSequence(subject []DNPart) pkix.RDNSequence {
	seq := make(pkix.RDNSequence, 0, len(subject))
	for i := len(subject) - 1; i >= 0; i-- {
		p := subject[i]
		seq = append(seq, pkix.RelativeDistinguishedNameSET{{Type: dnOIDs[p.Type], Value: p.Value}})
	}
	return seq
}

// generate makes the authority and the leaves, dated from now, and returns the PEM of each name. Its
// randomness is crypto/rand's, whatever reader the caller gave Generate: key generation uses the system's
// source in any case (since Go 1.26), and a reader of a test can't stand for it.
func (c Certificates) generate(now time.Time) (map[string]string, error) {
	notBefore := now.Add(-certBackdated).UTC().Truncate(time.Second)
	notAfter := now.AddDate(certYears, 0, 0).UTC().Truncate(time.Second)

	caSubject, err := subjectBytes(c.CA.Subject)
	if err != nil {
		return nil, err
	}
	caKey, err := rsa.GenerateKey(rand.Reader, certKeyBits)
	if err != nil {
		return nil, fmt.Errorf("make the certificate authority's key: %w", err)
	}
	caSerial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          caSerial,
		RawSubject:            caSubject,
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SignatureAlgorithm:    x509.SHA256WithRSA,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("make the certificate authority: %w", err)
	}
	// The parsed certificate has the key identifier x509 made for it, which the leaves point at.
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("read the certificate authority back: %w", err)
	}

	out := map[string]string{"ca_cert": pemOf("CERTIFICATE", caDER)}
	for _, l := range c.Leaves {
		key, err := rsa.GenerateKey(rand.Reader, certKeyBits)
		if err != nil {
			return nil, fmt.Errorf("make the key of %s: %w", l.Name, err)
		}
		serial, err := serialNumber()
		if err != nil {
			return nil, err
		}
		subject, err := subjectBytes(l.Subject)
		if err != nil {
			return nil, err
		}
		leaf := &x509.Certificate{
			SerialNumber:          serial,
			RawSubject:            subject,
			NotBefore:             notBefore,
			NotAfter:              notAfter,
			BasicConstraintsValid: true,
			KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
			DNSNames:              slices.Clone(l.DNSNames),
			SignatureAlgorithm:    x509.SHA256WithRSA,
		}
		der, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
		if err != nil {
			return nil, fmt.Errorf("make the certificate of %s: %w", l.Name, err)
		}
		pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("write the key of %s: %w", l.Name, err)
		}
		out[l.Name+"_cert"] = pemOf("CERTIFICATE", der)
		out[l.Name+"_key"] = pemOf("PRIVATE KEY", pkcs8)
	}
	// caKey goes out of scope here: nothing keeps it.
	return out, nil
}

func pemOf(kind string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}))
}

func serialNumber() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("make a serial number: %w", err)
	}
	return n.Add(n, big.NewInt(1)), nil
}

// subjectBytes encodes a subject for a certificate's RawSubject.
func subjectBytes(subject []DNPart) ([]byte, error) {
	b, err := asn1.Marshal(rdnSequence(subject))
	if err != nil {
		return nil, fmt.Errorf("encode a subject: %w", err)
	}
	return b, nil
}

// Stand-ins for Check and the preview: the text stands where a PEM would, so a template that puts a made
// value somewhere it doesn't fit is found without making a key.
const (
	standInCert = "-----BEGIN CERTIFICATE-----\nCHECK\n-----END CERTIFICATE-----\n"
	standInKey  = "-----BEGIN PRIVATE KEY-----\nCHECK\n-----END PRIVATE KEY-----\n"
)

// standIns gives each made name a stand-in value.
func (c Certificates) standIns() map[string]string {
	out := map[string]string{"ca_cert": standInCert}
	for _, l := range c.Leaves {
		out[l.Name+"_cert"] = standInCert
		out[l.Name+"_key"] = standInKey
	}
	return out
}
