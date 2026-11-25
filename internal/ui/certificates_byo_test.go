package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/certs/certstest"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 034: bring your own certificates, in Settings › Certificates and on Domains.

// hmacSentinel is a valid EAB HMAC key (base64url, 26 bytes) that no page, redirect or log line may hold.
const hmacSentinel = "s3cret-hmac-034AAAAAAAAAAAAAAAAAAAA"

// byoEnv is a console whose Traefik job renders the issuer and the uploads, installed with Let's Encrypt.
func byoEnv(t *testing.T) *consoleEnv {
	t.Helper()
	e := newConsoleEnvWith(t, func(svc *service.Service, n *deploytest.Nomad) {
		n.SetTraefikCertificates("traefik:v3.7")
		svc.SetACME("production", "ops@example.com")
	})
	return e
}

// byoCert is a certificate for names from a private CA: the chain and the key as a person pastes them.
type byoCert struct {
	leaf        *certstest.Leaf
	ca          *certstest.Authority
	chain, key  string
	keyBodyLine string // a line of the key's base64 that no page may hold
}

func newByoCert(t *testing.T, valid time.Duration, names ...string) byoCert {
	t.Helper()
	ca := certstest.NewCA(t, "Private Root", certstest.Organization("Acme Internal"))
	leaf := certstest.NewLeaf(t, ca, names, time.Now().Add(-time.Hour), time.Now().Add(valid), certstest.Organization("Acme Internal"))
	key := string(leaf.KeyPEM(t, certstest.PKCS8))
	lines := strings.Split(strings.TrimSpace(key), "\n")
	return byoCert{leaf: leaf, ca: ca, chain: string(certstest.Chain(leaf.Cert, ca.Cert)), key: key, keyBodyLine: lines[1]}
}

func (c byoCert) form() url.Values {
	return url.Values{"certificate": {c.chain}, "key": {c.key}}
}

// acmeDirectoryUI is a fake ACME directory over TLS for the Change issuer form.
func acmeDirectoryUI(t *testing.T, eab bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{"newNonce": "x/nonce", "newAccount": "x/acct", "newOrder": "x/order",
			"meta": map[string]any{"externalAccountRequired": eab}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// useDirectory makes the service read srv as the issuer's directory.
func useDirectory(e *consoleEnv, srv *httptest.Server) {
	e.Svc.SetACMEDirectoryClient(func(string) *http.Client { return srv.Client() })
}

// noSecrets fails when a body holds the HMAC sentinel or a line of a private key.
func noSecrets(t *testing.T, what string, body string, keys ...byoCert) {
	t.Helper()
	if strings.Contains(body, "s3cret-hmac-034") {
		t.Errorf("%s holds the HMAC key", what)
	}
	for _, k := range keys {
		if k.keyBodyLine != "" && strings.Contains(body, k.keyBodyLine) {
			t.Errorf("%s holds a private key", what)
		}
	}
}

func TestCertificatesPageByIssuer(t *testing.T) {
	dir := acmeDirectoryUI(t, false)
	for _, tc := range []struct {
		name   string
		setup  func(e *consoleEnv)
		env    func(t *testing.T) *consoleEnv
		want   []string
		forbid []string
		form   bool // the Change issuer form is drawn
	}{
		{name: "the installer's Let's Encrypt", env: byoEnv, form: true,
			want: []string{"HTTPS certificates for every domain come from Let's Encrypt and renew 30 days before they expire.",
				"Issuer Let's Encrypt", "Let's Encrypt (what the installer set)", "No uploaded certificates.", "Upload one to use a certificate you bought, or your company's.",
				"Uploaded certificates · 0 certificates", "Saving restarts Traefik once", "Save and restart Traefik", "Google Trust Services · ZeroSSL · DigiCert"},
			forbid: []string{"Hosts an uploaded certificate covers use it instead."}},
		{name: "staging", env: func(t *testing.T) *consoleEnv {
			e := byoEnv(t)
			e.Svc.SetACME("staging", "ops@example.com")
			return e
		}, form: true, want: []string{"Let's Encrypt (staging) (what the installer set)"}},
		{name: "a custom issuer", env: byoEnv, setup: func(e *consoleEnv) {
			useDirectory(e, dir)
			if _, err := e.Svc.SetCertificateIssuer(context.Background(), service.CertificateIssuerInput{Server: dir.URL + "/directory", Email: "pki@example.com"}); err != nil {
				t.Fatal(err)
			}
		}, form: true, want: []string{"come from 127.0.0.1 and renew 30 days before they expire.", "Issuer 127.0.0.1 " + dir.URL + "/directory. Set here.",
			"Contact email pki@example.com Gets expiry warnings from the issuer. Set here."}},
		{name: "installed without an issuer", env: func(t *testing.T) *consoleEnv {
			e := byoEnv(t)
			e.Svc.SetACME("none", "")
			return e
		}, want: []string{"Caravana was installed without an issuer (--acme none): run the installer with --acme production, then choose it here."},
			forbid: []string{"Save and restart Traefik"}},
		{name: "a Traefik job from before the feature", env: newConsoleEnv, want: []string{"Traefik's job is older than this feature: run the installer again on this server."},
			forbid: []string{"Save and restart Traefik", "Check and upload"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.env(t)
			if tc.setup != nil {
				tc.setup(e)
			}
			body := e.pageOf(t, "/settings/certificates")
			got := text(body)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("the page lacks %q:\n%s", w, got)
				}
			}
			for _, w := range tc.forbid {
				if strings.Contains(got, w) {
					t.Errorf("the page has %q", w)
				}
			}
			if has := strings.Contains(body, `action="/settings/certificates/issuer"`); has != tc.form {
				t.Errorf("Change issuer form drawn = %v, want %v", has, tc.form)
			}
			if strings.Contains(body, `class="cv-confirm cv-issueredit" open`) {
				t.Error("the Change issuer form is open without a refusal")
			}
		})
	}
}

func TestUploadCertificateAndItsRow(t *testing.T) {
	e := byoEnv(t)
	c := newByoCert(t, 61*day+time.Hour, "*.byo.test", "byo.test", "a.byo.test", "b.byo.test", "c.byo.test")
	resp, body := e.postForm(t, "/settings/certificates/upload", c.form())
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/settings/certificates?upload=saved&certificate=crt_") {
		t.Fatalf("POST = %d %q\n%s", resp.StatusCode, loc, text(body))
	}
	noSecrets(t, "the redirect", loc, c)

	page := e.pageOf(t, loc)
	got := text(page)
	for _, want := range []string{"Uploaded. It covers *.byo.test, a.byo.test, b.byo.test, byo.test, c.byo.test.",
		"Hosts an uploaded certificate covers use it instead.", "Uploaded certificates · 1 certificate",
		"*.byo.test, a.byo.test, b.byo.test +2", "Acme Internal", "expires in 61 d", "not trusted by browsers",
		"Remove this certificate? Its hosts go back to Let's Encrypt."} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(page, `cv-mark cv-mark-healthy`) || !strings.Contains(page, `title="*.byo.test, a.byo.test, b.byo.test, byo.test, c.byo.test"`) {
		t.Errorf("the row's mark or title:\n%s", got)
	}
	noSecrets(t, "the page", page, c)

	// The same names again replace the certificate.
	c2 := newByoCert(t, 90*day, "*.byo.test", "byo.test", "a.byo.test", "b.byo.test", "c.byo.test")
	resp, _ = e.postForm(t, "/settings/certificates/upload", c2.form())
	if loc2 := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc2, "/settings/certificates?upload=replaced&certificate=") {
		t.Fatalf("second POST = %d %q", resp.StatusCode, loc2)
	}
	if got := text(e.pageOf(t, resp.Header.Get("Location"))); !strings.Contains(got, "It replaces the earlier certificate for the same names.") || !strings.Contains(got, "Uploaded certificates · 1 certificate") {
		t.Errorf("after the replacement:\n%s", got)
	}
}

func TestUploadedRowSaysWhatItServesAndWhatStaysEarlier(t *testing.T) {
	e := byoEnv(t)
	healthy := seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1}
	// a, b and c are under the wildcard.
	e.seed(t, domainsApp("shop", "a.byo.test", "b.byo.test", "c.byo.test"), healthy)
	c := newByoCert(t, 61*day, "*.byo.test")
	resp, _ := e.postForm(t, "/settings/certificates/upload", c.form())
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload = %d", resp.StatusCode)
	}
	list, err := e.Svc.UploadedCertificates(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("uploaded = %v, %v", list, err)
	}
	here := []string{"203.0.113.1"}
	now := time.Now().UTC()
	// a is served by the upload; b and c keep an earlier certificate (Let's Encrypt's).
	for host, rec := range map[string]store.DomainCheckRecord{
		"a.byo.test": {CertStatus: edge.CertIssued, CertIssuer: list[0].Issuer, CertFingerprint: list[0].Fingerprint, CertNotAfter: &list[0].NotAfter},
		"b.byo.test": {CertStatus: edge.CertIssued, CertIssuer: "Let's Encrypt", CertFingerprint: "aa11", CertNotAfter: new(now.Add(60 * day))},
		"c.byo.test": {CertStatus: edge.CertIssued, CertIssuer: "Let's Encrypt", CertFingerprint: "bb22", CertNotAfter: new(now.Add(60 * day))},
	} {
		rec.Host, rec.AppID, rec.Service, rec.State = host, "shop", "web", string(edge.StateUnknown)
		rec.DNSStatus, rec.DNSSeen, rec.DNSExpected, rec.CheckedAt = edge.DNSOK, here, here, now.Add(-time.Minute)
		if err := e.Store.UpsertDomainCheck(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	got := text(e.pageOf(t, "/settings/certificates"))
	for _, want := range []string{"Serves a.byo.test.", "Still on their earlier certificate: b.byo.test, c.byo.test.",
		"Traefik keeps a certificate it already has: drop the earlier certificates, below, to serve this one."} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	// The domain table says what the host presents.
	if !strings.Contains(got, "a.byo.test shop uploaded: ") {
		t.Errorf("the Domain certificates table lacks the uploaded row:\n%s", got)
	}
	// A host that shares its route with one the upload doesn't cover says so.
	other := domainsApp("route", "m.byo.test", "n.other.test")
	e.seed(t, other, seedDeploy{ID: "d2", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1})
	for _, host := range []string{"m.byo.test", "n.other.test"} {
		rec := store.DomainCheckRecord{Host: host, AppID: "route", Service: "web", State: string(edge.StateUnknown), DNSStatus: edge.DNSOK, DNSSeen: here, DNSExpected: here,
			CertStatus: edge.CertIssued, CertIssuer: "Let's Encrypt", CertFingerprint: "dd44", CertNotAfter: new(now.Add(60 * day)), CheckedAt: now.Add(-time.Minute)}
		if err := e.Store.UpsertDomainCheck(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	// Both of m's route hosts must be one router for AlsoAsks; the plain domains share <app>-<service>.
	if got := text(e.pageOf(t, "/settings/certificates")); !strings.Contains(got, "The route of m.byo.test also asks for n.other.test: upload a certificate that covers them too.") {
		t.Errorf("no also-asks line:\n%s", got)
	}
}

func TestUploadRefusals(t *testing.T) {
	e := byoEnv(t)
	good := newByoCert(t, 61*day, "a.byo.test")
	other := newByoCert(t, 61*day, "a.byo.test")
	for _, tc := range []struct {
		name         string
		form         url.Values
		field, words string
	}{
		{"a key that doesn't match", url.Values{"certificate": {good.chain}, "key": {other.key}}, "key", "This key doesn't match any certificate given."},
		{"no certificate", url.Values{"certificate": {"hello"}, "key": {good.key}}, "certificate", "The certificate contains data that isn't PEM"},
		{"no key", url.Values{"certificate": {good.chain}, "key": {""}}, "key", "No private key found."},
		// The key pasted into the certificate's box too: refused, and the box comes back without it.
		{"a key in the certificate box as well", url.Values{"certificate": {good.chain + other.key}, "key": {good.key}}, "key", "More than one private key found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := e.postForm(t, "/settings/certificates/upload", tc.form)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d\n%s", resp.StatusCode, text(body))
			}
			// The message is at its field, the form is open, the certificate text is kept and no key comes back.
			if !strings.Contains(body, `class="cv-confirm cv-upform" open`) {
				t.Error("the Upload form isn't open")
			}
			re := regexp.MustCompile(`(?s)id="upload-` + tc.field + `-error"[^>]*>(.*?)</span></span>`)
			m := re.FindStringSubmatch(body)
			if m == nil {
				t.Fatalf("no error at %s:\n%s", tc.field, text(body))
			}
			if !strings.Contains(text(m[1]), tc.words) {
				t.Errorf("error = %q, want %q", text(m[1]), tc.words)
			}
			if !strings.Contains(text(body), strings.TrimSpace(tc.form.Get("certificate"))[:min(len(strings.TrimSpace(tc.form.Get("certificate"))), 20)]) {
				t.Error("the certificate text wasn't kept")
			}
			noSecrets(t, "the page", body, good, other)
			if tc.form.Get("key") != "" && strings.Contains(body, tc.form.Get("key")) {
				t.Error("the key came back")
			}
		})
	}
	if list, _ := e.Svc.UploadedCertificates(context.Background()); len(list) != 0 {
		t.Errorf("a refusal stored %d certificates", len(list))
	}
	// An expired certificate is refused at the certificate.
	old := certstest.NewCA(t, "Old")
	leaf := certstest.NewLeaf(t, old, []string{"old.byo.test"}, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	_, body := e.postForm(t, "/settings/certificates/upload", url.Values{"certificate": {string(certstest.Chain(leaf.Cert, old.Cert))}, "key": {string(leaf.KeyPEM(t, certstest.PKCS8))}})
	if got := text(body); !strings.Contains(got, "It expired on") {
		t.Errorf("expired:\n%s", got)
	}
}

func TestUploadUnavailableIsRefused(t *testing.T) {
	e := newConsoleEnv(t) // Traefik's job doesn't render uploads
	c := newByoCert(t, 61*day, "a.byo.test")
	resp, body := e.postForm(t, "/settings/certificates/upload", c.form())
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "Traefik's job is older than this feature: run the installer again on this server.") {
		t.Errorf("POST = %d\n%s", resp.StatusCode, text(body))
	}
	noSecrets(t, "the page", body, c)
}

func TestRemoveUploadedCertificate(t *testing.T) {
	e := byoEnv(t)
	c := newByoCert(t, 61*day, "a.byo.test")
	resp, _ := e.postForm(t, "/settings/certificates/upload", c.form())
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload = %d", resp.StatusCode)
	}
	list, _ := e.Svc.UploadedCertificates(context.Background())
	if len(list) != 1 {
		t.Fatalf("uploaded = %d", len(list))
	}
	page := e.pageOf(t, "/settings/certificates")
	if !strings.Contains(page, `action="/settings/certificates/uploaded/`+list[0].ID+`/remove"`) {
		t.Fatalf("no Remove form:\n%s", text(page))
	}
	resp, _ = e.postForm(t, "/settings/certificates/uploaded/"+list[0].ID+"/remove", nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/certificates?upload=removed" {
		t.Fatalf("remove = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	got := text(e.pageOf(t, "/settings/certificates?upload=removed"))
	if !strings.Contains(got, "Removed.") || !strings.Contains(got, "No uploaded certificates.") || strings.Contains(got, "a.byo.test") {
		t.Errorf("after removing:\n%s", got)
	}
	// A second click, or an ID that was never one, answers the same.
	resp, _ = e.postForm(t, "/settings/certificates/uploaded/crt_000000000000/remove", nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("remove of nothing = %d", resp.StatusCode)
	}
}

func TestChangeIssuerForm(t *testing.T) {
	dir := acmeDirectoryUI(t, false)
	eab := acmeDirectoryUI(t, true)
	e := byoEnv(t)
	ca := certstest.NewCA(t, "Private ACME CA")

	// A preset link draws the form open with its address.
	body := e.pageOf(t, "/settings/certificates?preset=google")
	if !strings.Contains(body, `class="cv-confirm cv-issueredit" open`) || !strings.Contains(body, `value="https://dv.acme-v02.api.pki.goog/directory"`) ||
		!strings.Contains(body, `value="ops@example.com"`) {
		t.Errorf("the preset form:\n%s", text(body))
	}
	if m := regexp.MustCompile(`name="eabHmacKey" value="([^"]*)"`).FindStringSubmatch(body); m == nil || m[1] != "" {
		t.Errorf("the HMAC field = %v, want an empty one", m)
	}
	if body := e.pageOf(t, "/settings/certificates?preset=nobody"); strings.Contains(body, `class="cv-confirm cv-issueredit" open`) {
		t.Error("an unknown preset opened the form")
	}

	// A refusal: an unreachable directory is at the server field, every value kept, the HMAC key not.
	useDirectory(e, dir)
	resp, body := e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"custom"}, "server": {"https://127.0.0.1:1/directory"},
		"email": {"pki@example.com"}, "eabKeyId": {"kid-1"}, "eabHmacKey": {hmacSentinel}, "caCertificate": {string(ca.PEM())}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unreachable = %d\n%s", resp.StatusCode, text(body))
	}
	for _, want := range []string{`class="cv-confirm cv-issueredit" open`, `value="https://127.0.0.1:1/directory"`, `value="pki@example.com"`, `value="kid-1"`, `id="issuer-server-error"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the refused form lacks %s", want)
		}
	}
	if !strings.Contains(body, "BEGIN CERTIFICATE") { // the CA, public, is kept
		t.Error("the CA text wasn't kept")
	}
	noSecrets(t, "the refused page", body)

	// EAB required: the key ID is asked for at its field (409 is for an unavailable issuer, this is a field error).
	useDirectory(e, eab)
	resp, body = e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"custom"}, "server": {eab.URL + "/directory"}, "email": {"pki@example.com"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="issuer-eab-id-error"`) {
		t.Fatalf("EAB = %d\n%s", resp.StatusCode, text(body))
	}
	// A key ID with no key is refused at the key's field.
	resp, body = e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"custom"}, "server": {eab.URL + "/directory"}, "email": {"pki@example.com"}, "eabKeyId": {"kid-1"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `id="issuer-eab-key-error"`) || !strings.Contains(text(body), "The EAB HMAC key is required for a new key ID.") {
		t.Fatalf("no key = %d\n%s", resp.StatusCode, text(body))
	}
	// Nothing was changed by the refusals.
	if cs, _ := e.Svc.CertificateSettings(context.Background()); cs.Issuer != service.IssuerLetsEncrypt {
		t.Errorf("a refusal changed the issuer to %s", cs.Issuer)
	}

	// Saved: a redirect, then a notice naming the issuer; the stored key and CA are kept by an empty field.
	resp, _ = e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"custom"}, "server": {eab.URL + "/directory"}, "email": {"pki@example.com"},
		"eabKeyId": {"kid-1"}, "eabHmacKey": {hmacSentinel}, "caCertificate": {string(ca.PEM())}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/certificates?issuer=saved" {
		t.Fatalf("save = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	page := e.pageOf(t, "/settings/certificates?issuer=saved")
	got := text(page)
	for _, want := range []string{"Saved. Traefik is restarting; domains get their certificates from 127.0.0.1 as they are asked for.",
		"EAB key ID kid-1", "Leave empty to keep the stored key.", "A CA certificate is stored: leave the box empty to keep it.", "Remove the stored CA certificate"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	noSecrets(t, "the page", page)
	resp, _ = e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"custom"}, "server": {dir.URL + "/directory"}, "email": {"pki@example.com"}, "eabKeyId": {"kid-1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("keep = %d", resp.StatusCode)
	}
	if cs, _ := e.Svc.CertificateSettings(context.Background()); cs.ACME == nil || !cs.ACME.EABKeySet || !cs.ACME.PrivateCA {
		t.Errorf("an empty HMAC key or CA box didn't keep them: %+v", cs.ACME)
	}
	resp, _ = e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"custom"}, "server": {dir.URL + "/directory"}, "email": {"pki@example.com"}, "eabKeyId": {"kid-1"}, "removeCA": {"1"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("remove CA = %d", resp.StatusCode)
	}
	if cs, _ := e.Svc.CertificateSettings(context.Background()); cs.ACME == nil || cs.ACME.PrivateCA {
		t.Errorf("Remove the stored CA certificate left it: %+v", cs.ACME)
	}

	// Back to the installer's issuer.
	resp, _ = e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"installer"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("reset = %d", resp.StatusCode)
	}
	if cs, _ := e.Svc.CertificateSettings(context.Background()); cs.Issuer != service.IssuerLetsEncrypt || cs.ACME != nil {
		t.Errorf("after the reset: %+v", cs)
	}
	if resp, body = e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"other"}}); resp.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(text(body), "Choose the installer's issuer or another ACME issuer.") {
		t.Errorf("an unknown choice = %d", resp.StatusCode)
	}
}

func TestChangeIssuerRefusedWhenUnavailable(t *testing.T) {
	e := newConsoleEnv(t) // no Traefik job that takes an issuer
	resp, body := e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"installer"}})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "Traefik's job is older than this feature") {
		t.Errorf("POST = %d\n%s", resp.StatusCode, text(body))
	}
}

// The words of Domains name the installation's issuer, and an uploaded certificate says it is uploaded.
func TestDomainsNameTheIssuerAndTheUpload(t *testing.T) {
	dir := acmeDirectoryUI(t, false)
	e := byoEnv(t)
	useDirectory(e, dir)
	// Google Trust Services by name: the preset's address, served by the fake directory.
	if _, err := e.Svc.SetCertificateIssuer(context.Background(), service.CertificateIssuerInput{Server: dir.URL + "/directory", Email: "pki@example.com"}); err != nil {
		t.Fatal(err)
	}
	healthy := seedDeploy{ID: "d1", Trigger: app.TriggerManual, Status: store.StatusHealthy, Version: 1}
	e.seed(t, domainsApp("shop", "pending.byo.test", "up.byo.test", "later.byo.test"), healthy)
	c := newByoCert(t, 3*day, "up.byo.test")
	if resp, _ := e.postForm(t, "/settings/certificates/upload", c.form()); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload = %d", resp.StatusCode)
	}
	list, _ := e.Svc.UploadedCertificates(context.Background())
	here := []string{"203.0.113.1"}
	now := time.Now().UTC()
	for host, rec := range map[string]store.DomainCheckRecord{
		"pending.byo.test": {CertStatus: edge.CertPending, CertIssuer: edge.TraefikOwn},
		"up.byo.test":      {CertStatus: edge.CertExpiring, CertIssuer: list[0].Issuer, CertFingerprint: list[0].Fingerprint, CertNotAfter: &list[0].NotAfter},
		"later.byo.test":   {CertStatus: edge.CertIssued, CertIssuer: "127.0.0.1", CertFingerprint: "ee55", CertNotAfter: new(now.Add(60 * day))},
	} {
		rec.Host, rec.AppID, rec.Service, rec.State = host, "shop", "web", string(edge.StateUnknown)
		rec.DNSStatus, rec.DNSSeen, rec.DNSExpected, rec.CheckedAt = edge.DNSOK, here, here, now.Add(-time.Minute)
		if err := e.Store.UpsertDomainCheck(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/domains", "/apps/shop/domains", "/parts/domains"} {
		body := e.pageOf(t, path)
		got := text(body)
		for _, want := range []string{"waiting for 127.0.0.1", "Nothing renews an uploaded certificate: upload its replacement in Settings › Certificates."} {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %q:\n%s", path, want, got)
			}
		}
		if strings.Contains(got, "Let's Encrypt") {
			t.Errorf("%s still names Let's Encrypt:\n%s", path, got)
		}
		if path != "/parts/domains" && !strings.Contains(got, "Certificates come from 127.0.0.1 and renew by themselves, or from the certificates uploaded in Settings.") {
			t.Errorf("%s: the footer:\n%s", path, got)
		}
		if !strings.Contains(got, "uploaded: ") || !strings.Contains(got, ", expires ") || !strings.Contains(body, `href="/settings/certificates"`) {
			t.Errorf("%s: the uploaded row's certificate fact or its link:\n%s", path, got)
		}
	}
	// With no upload the footer is as before.
	if err := e.Svc.DeleteUploadedCertificate(context.Background(), list[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := text(e.pageOf(t, "/domains")); strings.Contains(got, "uploaded in Settings") || !strings.Contains(got, "Certificates come from 127.0.0.1 and renew by themselves.") {
		t.Errorf("footer without uploads:\n%s", got)
	}
}

// Whatever a person pastes, no page, redirect or log line holds an HMAC key or a private key.
func TestCertificateSecretsNeverShow(t *testing.T) {
	dir := acmeDirectoryUI(t, true)
	e := byoEnv(t)
	useDirectory(e, dir)
	c := newByoCert(t, 61*day, "a.byo.test")
	bad := newByoCert(t, 61*day, "a.byo.test")
	var seen []string
	collect := func(resp *http.Response, body string) {
		seen = append(seen, body, resp.Header.Get("Location"))
	}
	collect(e.postForm(t, "/settings/certificates/upload", c.form()))
	collect(e.postForm(t, "/settings/certificates/upload", url.Values{"certificate": {c.chain}, "key": {bad.key}}))
	collect(e.postForm(t, "/settings/certificates/upload", url.Values{"certificate": {c.chain}, "key": {c.key + "\n" + bad.key}}))
	collect(e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"custom"}, "server": {dir.URL + "/directory"}, "email": {"pki@example.com"}, "eabKeyId": {"kid-1"}, "eabHmacKey": {hmacSentinel}}))
	collect(e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"custom"}, "server": {"nope"}, "email": {"pki@example.com"}, "eabKeyId": {"kid-1"}, "eabHmacKey": {hmacSentinel}}))
	collect(e.postForm(t, "/settings/certificates/issuer", url.Values{"issuer": {"custom"}, "server": {dir.URL + "/directory"}, "email": {"bad"}, "eabKeyId": {"kid-1"}, "eabHmacKey": {hmacSentinel}}))
	for _, path := range []string{"/settings/certificates", "/settings/certificates?issuer=saved", "/settings/certificates?preset=zerossl", "/settings", "/domains"} {
		seen = append(seen, e.pageOf(t, path))
	}
	seen = append(seen, e.Logs.String())
	for i, s := range seen {
		noSecrets(t, "response "+string(rune('a'+i)), s, c, bad)
	}
}
