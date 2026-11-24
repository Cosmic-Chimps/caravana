package api

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/certs/certstest"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 034: bring your own certificates. Every answer is checked against the contract, and the
// private key and the EAB HMAC key are searched for in everything the API said, logged or stored.

// certsEnv is a test API whose Traefik job renders the issuer and the uploads, installed with Let's
// Encrypt, with a fake ACME directory (over TLS) the service reads.
func certsEnv(t *testing.T, eab bool) (*testEnv, *httptest.Server) {
	t.Helper()
	e := domainsEnv(t)
	e.Nomad.SetTraefikCertificates("traefik:v3.7")
	e.Service.SetACME("production", "ops@example.com")
	directory := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "https://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]any{
			"newNonce": base + "/nonce", "newAccount": base + "/acct", "newOrder": base + "/order",
			"meta": map[string]any{"externalAccountRequired": eab},
		})
	}))
	t.Cleanup(directory.Close)
	e.Service.SetACMEDirectoryClient(func(string) *http.Client { return directory.Client() })
	return e, directory
}

// byo is a leaf of a private CA, with the PEM an upload sends.
type byo struct {
	leaf     *certstest.Leaf
	chainPEM string
	keyPEM   string
}

func newBYO(t *testing.T, valid time.Duration, names ...string) byo {
	t.Helper()
	ca := certstest.NewCA(t, "Private Root", certstest.Organization("Acme Internal"))
	leaf := certstest.NewLeaf(t, ca, names, time.Now().Add(-time.Hour), time.Now().Add(valid), certstest.Organization("Acme Internal"))
	return byo{leaf: leaf, chainPEM: string(certstest.Chain(leaf.Cert, ca.Cert)), keyPEM: string(leaf.KeyPEM(t, certstest.PKCS8))}
}

func (b byo) body(t *testing.T) string {
	t.Helper()
	return string(mustJSON(t, map[string]string{"certificate": b.chainPEM, "key": b.keyPEM}))
}

func TestCertificateIssuerAPI(t *testing.T) {
	doc := loadContract(t)
	e, directory := certsEnv(t, true)
	address := directory.URL + "/directory"
	hmac := base64.RawURLEncoding.EncodeToString([]byte("s3cret-hmac-034-0123456789"))

	t.Run("the settings of the installer's issuer", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/settings/certificates", "", "")
		if status != http.StatusOK {
			t.Fatalf("getCertificateSettings = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "CertificateSettings")
		got := decodeAs[map[string]any](t, raw)
		if got["issuer"] != "letsencrypt" || got["issuerName"] != "Let's Encrypt" || got["acme"] != nil ||
			got["issuerUnavailable"] != "" || got["uploadUnavailable"] != "" {
			t.Errorf("settings = %s", raw)
		}
	})

	t.Run("putCertificateIssuer", func(t *testing.T) {
		body := string(mustJSON(t, map[string]string{"server": address, "email": "ops@example.com", "eabKeyId": "kid-1", "eabHmacKey": hmac}))
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/settings/certificates/issuer", "application/json", body)
		if status != http.StatusOK {
			t.Fatalf("putCertificateIssuer = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "CertificateSettings")
		got := decodeAs[struct {
			Issuer string `json:"issuer"`
			ACME   struct {
				Server    string `json:"server"`
				EABKeyID  string `json:"eabKeyId"`
				EABKeySet bool   `json:"eabKeySet"`
				PrivateCA bool   `json:"privateCA"`
			} `json:"acme"`
		}](t, raw)
		if got.Issuer != "custom" || got.ACME.Server != address || got.ACME.EABKeyID != "kid-1" || !got.ACME.EABKeySet || got.ACME.PrivateCA {
			t.Errorf("settings = %s", raw)
		}
		if v := e.Nomad.Variable(deploy.ACMEVariablePath); v["eab_hmac"] != hmac || v["server"] != address {
			t.Errorf("variable = %v", v)
		}

		// An edit that leaves the key out keeps it.
		body = string(mustJSON(t, map[string]string{"server": address, "email": "other@example.com", "eabKeyId": "kid-1"}))
		if status, _, raw = call(t, http.MethodPut, e.URL+"/v1/settings/certificates/issuer", "application/json", body); status != http.StatusOK {
			t.Fatalf("an edit without the key = %d %s", status, raw)
		}
		if v := e.Nomad.Variable(deploy.ACMEVariablePath); v["eab_hmac"] != hmac || v["email"] != "other@example.com" {
			t.Errorf("variable after the edit = %v", v)
		}
	})

	t.Run("deleteCertificateIssuer", func(t *testing.T) {
		status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/settings/certificates/issuer", "", "")
		if status != http.StatusOK {
			t.Fatalf("deleteCertificateIssuer = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "CertificateSettings")
		if got := decodeAs[map[string]any](t, raw); got["issuer"] != "letsencrypt" || got["acme"] != nil {
			t.Errorf("settings = %s", raw)
		}
		if e.Nomad.Variable(deploy.ACMEVariablePath) != nil {
			t.Error("the variable is still there")
		}
		// 200 again when none was set.
		if status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/settings/certificates/issuer", "", ""); status != http.StatusOK {
			t.Errorf("a second delete = %d %s", status, raw)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		put := func(m map[string]string) (int, []byte) {
			status, _, raw := call(t, http.MethodPut, e.URL+"/v1/settings/certificates/issuer", "application/json", string(mustJSON(t, m)))
			return status, raw
		}
		for _, tc := range []struct {
			name  string
			body  map[string]string
			field string
		}{
			{"an http server", map[string]string{"server": "http://acme.example.com/dir", "email": "a@example.com"}, "server"},
			{"no email", map[string]string{"server": address}, "email"},
			{"an issuer that needs EAB, given none", map[string]string{"server": address, "email": "a@example.com"}, "eabKeyId"},
			{"a new key ID without its key", map[string]string{"server": address, "email": "a@example.com", "eabKeyId": "kid-9"}, "eabHmacKey"},
			{"a key that isn't base64", map[string]string{"server": address, "email": "a@example.com", "eabKeyId": "k", "eabHmacKey": "!!"}, "eabHmacKey"},
			{"a CA that isn't PEM", map[string]string{"server": address, "email": "a@example.com", "eabKeyId": "k", "eabHmacKey": hmac, "caCertificate": "nope"}, "caCertificate"},
		} {
			status, raw := put(tc.body)
			wantError(t, tc.name, status, raw, http.StatusUnprocessableEntity, codeInvalidField, tc.field)
		}
		status, _, raw := call(t, http.MethodPut, e.URL+"/v1/settings/certificates/issuer", "application/json", `{"server": "x", "email": "y", "nope": 1}`)
		wantError(t, "an unknown field", status, raw, http.StatusBadRequest, codeUnknownField, "nope")
		if e.Nomad.Variable(deploy.ACMEVariablePath) != nil {
			t.Error("a refused request wrote the variable")
		}
	})

	t.Run("an installation that can't take an issuer", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func()
			want  string
		}{
			{"installed without an issuer", func() { e.Service.SetACME("none", "") }, "installed_without_issuer"},
			{"a Traefik job from before the feature", func() { e.Service.SetACME("production", ""); e.Nomad.SetTraefikJob("traefik:v3.6") }, "traefik_outdated"},
		} {
			tc.setup()
			status, _, raw := call(t, http.MethodGet, e.URL+"/v1/settings/certificates", "", "")
			if got := decodeAs[map[string]any](t, raw); status != http.StatusOK || got["issuerUnavailable"] != tc.want {
				t.Errorf("%s: settings = %d %s", tc.name, status, raw)
			}
			body := string(mustJSON(t, map[string]string{"server": address, "email": "a@example.com", "eabKeyId": "k", "eabHmacKey": hmac}))
			status, _, raw = call(t, http.MethodPut, e.URL+"/v1/settings/certificates/issuer", "application/json", body)
			wantError(t, tc.name, status, raw, http.StatusConflict, codeIssuerUnavailable, "")
			status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/settings/certificates/issuer", "", "")
			wantError(t, tc.name+" (delete)", status, raw, http.StatusConflict, codeIssuerUnavailable, "")
		}
	})
}

func TestUploadedCertificatesAPI(t *testing.T) {
	doc := loadContract(t)
	e, _ := certsEnv(t, false)
	a := newBYO(t, 90*24*time.Hour, "*.byo.test", "byo.test")

	var id string
	t.Run("uploadCertificate", func(t *testing.T) {
		status, header, raw := call(t, http.MethodPost, e.URL+"/v1/certificates/uploaded", "application/json", a.body(t))
		if status != http.StatusCreated {
			t.Fatalf("uploadCertificate = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "UploadedCertificate")
		got := decodeAs[service.UploadedCertificateView](t, raw)
		id = got.ID
		if header.Get("Location") != "/v1/certificates/uploaded/"+id || got.Status != "valid" || got.Trusted || got.KeyType != "ECDSA P-256" ||
			got.Subject != "*.byo.test" || got.Issuer != "Acme Internal" || strings.Join(got.Names, ",") != "*.byo.test,byo.test" || got.Replaced != "" {
			t.Errorf("answer = %s, location %q", raw, header.Get("Location"))
		}
		vars := e.Nomad.Variable(deploy.CertificatesVariablePrefix + id)
		if vars["names"] != "*.byo.test,byo.test" || !strings.Contains(vars["key"], "PRIVATE KEY") {
			t.Errorf("variable = %v", vars)
		}
	})

	t.Run("list and get", func(t *testing.T) {
		status, _, raw := call(t, http.MethodGet, e.URL+"/v1/certificates/uploaded", "", "")
		if status != http.StatusOK {
			t.Fatalf("listUploadedCertificates = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "UploadedCertificateList")
		if got := decodeAs[struct {
			Certificates []map[string]any `json:"certificates"`
		}](t, raw); len(got.Certificates) != 1 || got.Certificates[0]["id"] != id {
			t.Errorf("list = %s", raw)
		}
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/certificates/uploaded/"+id, "", "")
		if status != http.StatusOK {
			t.Fatalf("getUploadedCertificate = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "UploadedCertificate")
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/certificates/uploaded/crt_000000000000", "", "")
		wantError(t, "getUploadedCertificate unknown", status, raw, http.StatusNotFound, codeCertificateNotFound, "")
	})

	t.Run("the same names replace it", func(t *testing.T) {
		b := newBYO(t, 60*24*time.Hour, "byo.test", "*.byo.test")
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/certificates/uploaded", "application/json", b.body(t))
		if status != http.StatusCreated {
			t.Fatalf("a replacement = %d %s", status, raw)
		}
		checkSchema(t, doc, raw, "UploadedCertificate")
		got := decodeAs[service.UploadedCertificateView](t, raw)
		if got.Replaced != id || got.ID == id {
			t.Errorf("answer = %s, want it to replace %s", raw, id)
		}
		if status, _, raw = call(t, http.MethodGet, e.URL+"/v1/certificates/uploaded/"+id, "", ""); status != http.StatusNotFound {
			t.Errorf("the replaced certificate = %d %s", status, raw)
		}
		id = got.ID
	})

	t.Run("refusals", func(t *testing.T) {
		other := newBYO(t, 90*24*time.Hour, "*.other.test")
		for _, tc := range []struct {
			name, certificate, key, field string
		}{
			{"a key of another certificate", other.chainPEM, a.keyPEM, "key"},
			{"an encrypted key", other.chainPEM, string(certstest.EncryptedKeyPEM()), "key"},
			{"no certificate", "", other.keyPEM, "certificate"},
			{"text that isn't PEM", "hello", other.keyPEM, "certificate"},
		} {
			body := string(mustJSON(t, map[string]string{"certificate": tc.certificate, "key": tc.key}))
			status, _, raw := call(t, http.MethodPost, e.URL+"/v1/certificates/uploaded", "application/json", body)
			wantError(t, tc.name, status, raw, http.StatusUnprocessableEntity, codeInvalidField, tc.field)
		}
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/certificates/uploaded", "application/json", `{"certificate": "x", "key": "y", "extra": 1}`)
		wantError(t, "an unknown field", status, raw, http.StatusBadRequest, codeUnknownField, "extra")
	})

	t.Run("deleteUploadedCertificate", func(t *testing.T) {
		status, _, raw := call(t, http.MethodDelete, e.URL+"/v1/certificates/uploaded/"+id, "", "")
		if status != http.StatusNoContent {
			t.Fatalf("deleteUploadedCertificate = %d %s", status, raw)
		}
		status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/certificates/uploaded/"+id, "", "")
		wantError(t, "a second delete", status, raw, http.StatusNotFound, codeCertificateNotFound, "")
		if e.Nomad.Variable(deploy.CertificatesVariablePrefix+id) != nil {
			t.Error("the variable is still there")
		}
	})

	t.Run("an installation whose Traefik can't take uploads", func(t *testing.T) {
		e.Nomad.SetTraefikJob("traefik:v3.6")
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/certificates/uploaded", "application/json", a.body(t))
		wantError(t, "an old Traefik job", status, raw, http.StatusConflict, codeCertificatesUnavailable, "")
		status, _, raw = call(t, http.MethodGet, e.URL+"/v1/settings/certificates", "", "")
		if got := decodeAs[map[string]any](t, raw); status != http.StatusOK || got["uploadUnavailable"] != "traefik_outdated" {
			t.Errorf("settings = %d %s", status, raw)
		}
	})
}

func TestUploadedCertificatesLimitAPI(t *testing.T) {
	e, _ := certsEnv(t, false)
	for i := range 30 {
		a := newBYO(t, 90*24*time.Hour, "h"+strings.Repeat("a", i%5)+string(rune('a'+i/5))+".limit.test")
		if status, _, raw := call(t, http.MethodPost, e.URL+"/v1/certificates/uploaded", "application/json", a.body(t)); status != http.StatusCreated {
			t.Fatalf("upload %d = %d %s", i, status, raw)
		}
	}
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/certificates/uploaded", "application/json", newBYO(t, 90*24*time.Hour, "*.one-more.test").body(t))
	wantError(t, "the 31st certificate", status, raw, http.StatusConflict, codeTooManyCertificates, "")
}

// A deploy token may use none of it, and a certificate an upload covers is named by the domains and
// the certificates, with its fingerprint.
func TestUploadedCertificateInDomainsAPI(t *testing.T) {
	doc := loadContract(t)
	e, _ := certsEnv(t, false)
	d := deployOK(t, e, "whoami", whoamiBody)
	waitHealthy(t, e, "whoami", d.ID)

	a := newBYO(t, 5*24*time.Hour, "*.whoami.localhost", "whoami.localhost")
	e.Service.SetEdge(nil, &edgetest.FakeDialer{Chains: map[string][]*x509.Certificate{"whoami.localhost": {a.leaf.Cert}}, Default: []*x509.Certificate{edgetest.TraefikDefault(t)}})
	status, _, raw := call(t, http.MethodPost, e.URL+"/v1/certificates/uploaded", "application/json", a.body(t))
	if status != http.StatusCreated {
		t.Fatalf("upload = %d %s", status, raw)
	}
	up := decodeAs[service.UploadedCertificateView](t, raw)
	if up.Status != "expiring" {
		t.Fatalf("status = %s, want expiring (5 days)", up.Status)
	}

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/domains?fresh=true", "", "")
	if status != http.StatusOK {
		t.Fatalf("listDomains = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "DomainList")
	domains := decodeAs[struct {
		Domains []struct {
			Host        string `json:"host"`
			State       string `json:"state"`
			Certificate struct {
				Status      string               `json:"status"`
				Issuer      string               `json:"issuer"`
				RenewsAt    *time.Time           `json:"renewsAt"`
				Fingerprint string               `json:"fingerprint"`
				Uploaded    *service.UploadedRef `json:"uploaded"`
			} `json:"certificate"`
		} `json:"domains"`
	}](t, raw).Domains
	if len(domains) != 1 || domains[0].Certificate.Uploaded == nil || domains[0].Certificate.Uploaded.ID != up.ID ||
		domains[0].Certificate.Fingerprint != up.Fingerprint || domains[0].Certificate.Issuer != "Acme Internal" ||
		domains[0].Certificate.RenewsAt != nil || domains[0].Certificate.Status != "expiring" || domains[0].State != "cert_expiring" {
		t.Errorf("domains = %s", raw)
	}

	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/certificates", "", "")
	checkSchema(t, doc, raw, "CertificateList")
	if got := decodeAs[struct {
		Certificates []map[string]any `json:"certificates"`
	}](t, raw).Certificates; status != http.StatusOK || len(got) != 1 || got[0]["fingerprint"] != up.Fingerprint || got[0]["uploaded"] == nil {
		t.Errorf("certificates = %d %s", status, raw)
	}

	status, _, raw = call(t, http.MethodPost, e.URL+"/v1/domains/check", "application/json", `{"host": "whoami.localhost"}`)
	checkSchema(t, doc, raw, "DomainCheck")
	if got := decodeAs[map[string]any](t, raw)["certificate"].(map[string]any); status != http.StatusOK || got["uploaded"] == nil {
		t.Errorf("checkDomain = %d %s", status, raw)
	}

	// The needs-you list has one item for the certificate, and the webhook-able fields.
	status, _, raw = call(t, http.MethodGet, e.URL+"/v1/attention", "", "")
	if status != http.StatusOK {
		t.Fatalf("listAttention = %d %s", status, raw)
	}
	var item map[string]any
	for _, it := range decodeAs[struct {
		Items []map[string]any `json:"items"`
	}](t, raw).Items {
		if it["kind"] == "certificate_expiring" {
			item = it
		}
		if it["kind"] == "domain_cert_expiring" {
			t.Errorf("a domain with an uploaded certificate raised domain_cert_expiring: %v", it)
		}
	}
	if item == nil || item["certificate"] != up.ID || item["level"] != "warning" || item["appId"] != nil || item["expiresAt"] == nil ||
		!strings.Contains(item["detail"].(string), "Upload its replacement in Settings › Certificates.") {
		t.Errorf("item = %v in %s", item, raw)
	}
	for _, it := range decodeAs[struct {
		Items []json.RawMessage `json:"items"`
	}](t, raw).Items {
		checkSchema(t, doc, it, "AttentionItem")
	}

	// A deploy token is refused by the default deny.
	shopProject(t, e)
	auth := scopedToken(t, e, "shop", "web")
	for _, r := range [][2]string{
		{http.MethodPut, "/v1/settings/certificates/issuer"}, {http.MethodDelete, "/v1/settings/certificates/issuer"},
		{http.MethodGet, "/v1/certificates/uploaded"}, {http.MethodPost, "/v1/certificates/uploaded"},
		{http.MethodGet, "/v1/certificates/uploaded/" + up.ID}, {http.MethodDelete, "/v1/certificates/uploaded/" + up.ID},
	} {
		status, _, raw := callWith(t, auth, r[0], e.RawURL+r[1], "application/json", "{}")
		wantError(t, "a deploy token on "+r[0]+" "+r[1], status, raw, http.StatusForbidden, codeTokenScope, "")
	}
}

// The private key of an upload and the EAB HMAC key of an issuer are in no answer, no log line, no
// stored row and no file of the store (FR-012).
func TestCertificateSecretsNeverLeakAPI(t *testing.T) {
	ctx := context.Background()
	e, directory := certsEnv(t, true)
	address := directory.URL + "/directory"
	hmacBytes := []byte("s3cret-hmac-034-0123456789")
	hmac := base64.StdEncoding.EncodeToString(hmacBytes)
	a := newBYO(t, 90*24*time.Hour, "*.byo.test")
	stranger := newBYO(t, 90*24*time.Hour, "*.byo.test")
	secrets := []string{"s3cret-hmac-034", hmac, strings.TrimRight(hmac, "="), base64.RawURLEncoding.EncodeToString(hmacBytes),
		pemInner(a.keyPEM), pemInner(stranger.keyPEM)}

	var seen []string
	do := func(method, path, body string) {
		_, _, raw := call(t, method, e.URL+path, "application/json", body)
		seen = append(seen, string(raw))
	}
	put := func(m map[string]string) {
		do(http.MethodPut, "/v1/settings/certificates/issuer", string(mustJSON(t, m)))
	}
	put(map[string]string{"server": address, "email": "a@example.com", "eabKeyId": "kid", "eabHmacKey": hmac})
	put(map[string]string{"server": address, "email": "b@example.com", "eabKeyId": "kid"})
	put(map[string]string{"server": "http://bad", "email": "b@example.com", "eabKeyId": "kid", "eabHmacKey": hmac})
	put(map[string]string{"server": address, "email": "b@example.com", "eabHmacKey": hmac})
	do(http.MethodGet, "/v1/settings/certificates", "")
	do(http.MethodPost, "/v1/certificates/uploaded", a.body(t))
	do(http.MethodPost, "/v1/certificates/uploaded", string(mustJSON(t, map[string]string{"certificate": a.chainPEM, "key": stranger.keyPEM})))
	do(http.MethodPost, "/v1/certificates/uploaded", string(mustJSON(t, map[string]string{"certificate": a.chainPEM, "key": string(certstest.EncryptedKeyPEM())})))
	do(http.MethodGet, "/v1/certificates/uploaded", "")
	do(http.MethodGet, "/v1/certificates", "")
	do(http.MethodGet, "/v1/attention", "")
	do(http.MethodGet, "/v1/domains", "")

	check := func(where, text string) {
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("%s holds a secret (%.12s…)", where, s)
			}
		}
	}
	for i, s := range seen {
		check("answer "+string(rune('0'+i%10)), s)
	}
	check("the log", e.Logs.String())
	for _, name := range []string{"caravana.db", "caravana.db-wal"} {
		if raw, err := os.ReadFile(filepath.Join(e.Dir, name)); err == nil {
			check(name, string(raw))
		}
	}
	if list, err := e.Service.UploadedCertificates(ctx); err != nil || len(list) != 1 {
		t.Fatalf("uploaded = %+v, %v; want the one that went through", list, err)
	}
	if got := e.Nomad.Variable(deploy.ACMEVariablePath)["eab_hmac"]; got != base64.RawURLEncoding.EncodeToString(hmacBytes) {
		t.Errorf("the issuer's variable holds %q, want the key there", got)
	}
}

// pemInner is a stretch of a PEM block's base64 that no other text contains.
func pemInner(pemText string) string {
	var body string
	for _, l := range strings.Split(pemText, "\n") {
		if l != "" && !strings.HasPrefix(l, "-----") {
			body += l
		}
	}
	if len(body) < 60 {
		return body
	}
	return body[10 : len(body)-10]
}
