package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/certs/certstest"
	"github.com/cosmic-chimps/caravana/internal/deploy"
)

// The private key of an uploaded certificate and the EAB HMAC key of an issuer live only in their
// Nomad Variables (feature 034, FR-012, constitution VII). No answer, error, log line, stored row or
// SQLite file, and no Nomad job, may hold either.
func TestCertificateSecretsAreOnlyInTheirVariables(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	dir := newACMEDirectory(t, true, nil)
	dir.use(e, nil)

	// The HMAC key is the sentinel's own bytes, as an issuer shows it; its encodings are searched too.
	hmacBytes := []byte("s3cret-hmac-034-0123456789")
	hmacURL := base64.RawURLEncoding.EncodeToString(hmacBytes)
	hmacStd := base64.StdEncoding.EncodeToString(hmacBytes)
	secrets := []string{"s3cret-hmac-034", hmacURL, hmacStd, strings.TrimRight(hmacStd, "=")}

	c := newBYOCert(t, []string{"*.byo.test"}, 90*24*time.Hour)
	body := pemBody(t, c.keyPEM)
	stranger := newBYOCert(t, []string{"*.byo.test"}, 90*24*time.Hour)
	second := newBYOCert(t, []string{"*.byo.test"}, 60*24*time.Hour)
	secrets = append(secrets, body, pemBody(t, stranger.keyPEM), pemBody(t, second.keyPEM))

	var errs []error
	var answers []string
	record := func(v any, err error) {
		if err != nil {
			errs = append(errs, err)
		}
		if v != nil {
			raw, _ := json.Marshal(v)
			answers = append(answers, string(raw))
		}
	}

	// The issuer: a save, an edit that keeps the key, refusals that carry it, a reset.
	typed := hmacStd // padding and the standard alphabet are accepted
	cs, err := e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: dir.address(), Email: "a@example.com", EABKeyID: "kid-1", EABHMACKey: &typed})
	record(cs, err)
	cs, err = e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: dir.address(), Email: "b@example.com", EABKeyID: "kid-1"})
	record(cs, err)
	_, err = e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: "http://nope", Email: "b@example.com", EABKeyID: "kid-1", EABHMACKey: &typed})
	record(nil, err)
	_, err = e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: dir.address(), Email: "b@example.com", EABHMACKey: &typed})
	record(nil, err) // a key with no key ID
	short := "QUJD"
	_, err = e.svc.SetCertificateIssuer(ctx, CertificateIssuerInput{Server: dir.address(), Email: "b@example.com", EABKeyID: "kid-2", EABHMACKey: &short})
	record(nil, err)
	cs, err = e.svc.CertificateSettings(ctx)
	record(cs, err)

	// The uploads: one made, one replaced, refusals that carry a key, a list, a delete.
	v, err := c.upload(e)
	record(v, err)
	_, err = e.svc.UploadCertificate(ctx, c.chainPEM, stranger.keyPEM) // the wrong key
	record(nil, err)
	_, err = e.svc.UploadCertificate(ctx, c.chainPEM, string(certstest.EncryptedKeyPEM()))
	record(nil, err)
	_, err = e.svc.UploadCertificate(ctx, "not a certificate", c.keyPEM)
	record(nil, err)
	v2, err := second.upload(e)
	record(v2, err)
	list, err := e.svc.UploadedCertificates(ctx)
	record(list, err)
	got, err := e.svc.UploadedCertificate(ctx, v2.ID)
	record(got, err)
	record(nil, e.svc.DeleteUploadedCertificate(ctx, "crt_000000000000"))
	atts, err := e.svc.Attention(ctx)
	record(atts.Items, err)
	doms, err := e.svc.Certificates(ctx)
	record(doms, err)

	// Where the secrets must be, and nowhere else.
	if got := e.nomad.Variable(deploy.ACMEVariablePath)["eab_hmac"]; got != hmacURL {
		t.Fatalf("the issuer's variable holds %q, want the key there", got)
	}
	if got := e.nomad.Variable(deploy.CertificatesVariablePrefix + v2.ID)["key"]; strings.TrimSpace(got) != strings.TrimSpace(second.keyPEM) {
		t.Fatal("the certificate's variable doesn't hold its key")
	}

	search := func(where, text string) {
		for _, s := range secrets {
			if s != "" && strings.Contains(text, s) {
				t.Errorf("%s holds a secret (%.12s…)", where, s)
			}
		}
	}
	for i, err := range errs {
		search(fmt.Sprintf("error %d (%v)", i, err), err.Error())
	}
	for i, a := range answers {
		search(fmt.Sprintf("answer %d", i), a)
	}
	search("the log", e.logs.String())
	for _, row := range storeRows(t, e.dir) {
		search("a stored row", row)
	}
	for _, name := range []string{"caravana.db", "caravana.db-wal"} {
		if raw, err := os.ReadFile(filepath.Join(e.dir, name)); err == nil {
			search(name, string(raw))
		}
	}
	for _, id := range []string{"traefik"} {
		if j := e.nomad.Job(id); j != nil {
			search("job "+id, mustMarshal(t, j))
		}
	}
	// Every other variable of the fake Nomad is free of them too: only the two paths hold secrets.
	for _, path := range e.nomad.VariablePaths() {
		if path == deploy.ACMEVariablePath || strings.HasPrefix(path, deploy.CertificatesVariablePrefix) {
			continue
		}
		raw, _ := json.Marshal(e.nomad.Variable(path))
		search("variable "+path, string(raw))
	}
}

// pemBody is the middle of a PEM block's base64, a stretch that no other text contains.
func pemBody(t *testing.T, pemText string) string {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(pemText, "\n") {
		if l != "" && !strings.HasPrefix(l, "-----") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		t.Fatalf("no PEM body in %q", pemText)
	}
	body := strings.Join(lines, "")
	if len(body) < 60 {
		return body
	}
	return body[10 : len(body)-10]
}
