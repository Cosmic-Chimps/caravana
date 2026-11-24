package main

// console-certificates (feature 034): what the owner does in Settings › Certificates and on Domains to use a
// certificate they brought, over HTTP the way a browser does it, against the lab's Traefik.

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/certs/certstest"
)

const (
	consoleCertApp  = "con034"
	consoleCertHost = "x.cert034.test"
	consoleCertWild = "*.cert034.test"
)

// consoleCertificates:
//
//  1. skips, naming deploy/dev/lab.sh up, when the page says Traefik's job is older than this feature;
//  2. uploads a wildcard for *.cert034.test from a private CA through the form: the redirect, the notice
//     naming the names, the row with "not trusted by browsers", and Traefik serving it for the host;
//  3. a key that doesn't match is refused at the key field with the certificate kept and no key in the page;
//  4. deploys a whoami app on x.cert034.test (after the upload: a router that came first would make
//     Traefik order a certificate, and an exact name beats the wildcard) and waits until /domains says
//     "uploaded:" for it, the Domain certificates table names the upload, and Check now keeps it;
//  5. Remove through the form: the redirect, and the row is gone;
//  6. the private key is in no page and no log line.
func (c *console) consoleCertificates(t *testing.T) {
	const id, host = consoleCertApp, consoleCertHost
	c.ensureSignedIn(t)
	pages := map[string]string{}
	get := func(path string) string {
		t.Helper()
		resp, body := c.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d:\n%s", path, resp.StatusCode, pageText(body))
		}
		pages[path] = body
		return body
	}

	// 1. A lab whose Traefik job predates the feature can't take uploads.
	if text := prose(get("/settings/certificates")); strings.Contains(text, "Traefik's job is older than this feature") {
		t.Skip("the lab's Traefik job is older than feature 034: run deploy/dev/lab.sh up from this branch")
	}

	removeApp := func() {
		if resp, err := c.g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	removeUploads := func() {
		resp, err := c.g.apiRequest(http.MethodGet, "/v1/certificates/uploaded", "", "")
		if err != nil {
			return
		}
		defer resp.Body.Close()
		var list struct {
			Certificates []struct {
				ID    string   `json:"id"`
				Names []string `json:"names"`
			} `json:"certificates"`
		}
		if json.NewDecoder(resp.Body).Decode(&list) != nil {
			return
		}
		for _, u := range list.Certificates {
			if len(u.Names) == 1 && u.Names[0] == consoleCertWild {
				if r, err := c.g.apiRequest(http.MethodDelete, "/v1/certificates/uploaded/"+u.ID, "", ""); err == nil {
					r.Body.Close()
				}
			}
		}
	}
	removeApp() // leftovers of an aborted run, if any
	removeUploads()
	c.appGone(t, id)
	t.Cleanup(func() {
		removeApp()
		removeUploads()
		c.appGone(t, id)
	})

	company := certstest.NewCA(t, "Console 034 Company Root", certstest.Organization("Console034 Company"))
	wild := certstest.NewLeaf(t, company, []string{consoleCertWild}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))
	wildChain, wildKey := string(certstest.Chain(wild.Cert, company.Cert)), string(wild.KeyPEM(t, certstest.PKCS8))
	keyBody := pemBody(wildKey)
	other := certstest.NewLeaf(t, company, []string{consoleCertWild}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))
	otherKey := string(other.KeyPEM(t, certstest.PKCS8))

	// 3 first: a refused upload changes nothing.
	resp, body := c.post(t, "/settings/certificates/upload", url.Values{"certificate": {wildChain}, "key": {otherKey}})
	pages["POST refused upload"] = body
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an upload with the wrong key = %d, want 422:\n%s", resp.StatusCode, pageText(body))
	}
	if !strings.Contains(body, `id="upload-key-error"`) || !strings.Contains(prose(body), "doesn't match") {
		t.Errorf("the refusal isn't at the key field:\n%s", prose(body))
	}
	if !strings.Contains(body, "BEGIN CERTIFICATE") || strings.Contains(body, pemBody(otherKey)) || strings.Contains(body, keyBody) {
		t.Error("the refused page lost the certificate text, or draws a key back")
	}

	// 2. The upload through the form.
	resp, body = c.post(t, "/settings/certificates/upload", url.Values{"certificate": {wildChain}, "key": {wildKey}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/settings/certificates?upload=saved&certificate=crt_") {
		t.Fatalf("the upload = %d → %q, want 303 to ?upload=saved:\n%s", resp.StatusCode, loc, pageText(body))
	}
	text := prose(get(loc))
	for _, want := range []string{"Uploaded. It covers " + consoleCertWild + ".", "Uploaded certificates · 1 certificate", "Console034 Company", "not trusted by browsers", "expires in "} {
		if !strings.Contains(text, want) {
			t.Errorf("the page after the upload lacks %q:\n%s", want, text)
		}
	}
	isCompany := func(p presented) bool { return strings.Contains(p.Issuer, "Console034 Company") }
	c.g.waitPresented(t, host, "Traefik to serve the upload", time.Minute, isCompany)

	// 4. The app, after the upload.
	raw, _ := json.Marshal(map[string]any{"source": map[string]string{"kind": "image"}, "services": []any{map[string]any{
		"name": "web", "image": "traefik/whoami:latest",
		"ports":     []any{map[string]any{"name": "http", "containerPort": 80}},
		"domains":   []any{map[string]string{"host": host, "port": "http"}},
		"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
		"resources": map[string]any{"memoryMB": 16, "memoryMaxMB": 32},
	}}})
	var d struct {
		ID string `json:"id"`
	}
	r := c.g.api(t, http.MethodPost, "/v1/apps/"+id+"/deployments", "application/json", string(raw))
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("deploy %s = %d, want 202", id, r.StatusCode)
	}
	_ = json.NewDecoder(r.Body).Decode(&d)
	r.Body.Close()
	if fin := c.g.waitFinalOf(t, id, d.ID, 4*time.Minute); fin.Status != "healthy" {
		t.Fatalf("%s's deployment ended %s: %s", id, fin.Status, fin.Error)
	}
	c.g.waitPresented(t, host, "the uploaded certificate for the app's host", time.Minute, isCompany)
	waitFor(t, "/domains to say the host presents the upload", time.Minute, func() bool {
		// The page's own stream checks every minute; Check now is the form's way to ask at once.
		c.post(t, "/domains/"+url.PathEscape(host)+"/check", url.Values{})
		_, page := c.get(t, "/domains")
		pages["/domains"] = page
		return strings.Contains(prose(page), "uploaded: ")
	})
	if text = prose(pages["/domains"]); !strings.Contains(text, ", expires ") {
		t.Errorf("/domains' certificate fact for %s lacks the expiry:\n%s", host, text)
	}
	text = prose(get("/settings/certificates"))
	for _, want := range []string{"Serves " + host + ".", host + " " + id + " uploaded: "} {
		if !strings.Contains(text, want) {
			t.Errorf("Settings › Certificates lacks %q:\n%s", want, text)
		}
	}

	// 5. Remove through the form.
	i := strings.Index(loc, "certificate=")
	certID := loc[i+len("certificate="):]
	page := get("/settings/certificates")
	if !strings.Contains(page, `action="/settings/certificates/uploaded/`+certID+`/remove"`) {
		t.Fatalf("no Remove form for %s:\n%s", certID, prose(page))
	}
	resp, body = c.post(t, "/settings/certificates/uploaded/"+certID+"/remove", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings/certificates?upload=removed" {
		t.Fatalf("Remove = %d → %q:\n%s", resp.StatusCode, resp.Header.Get("Location"), pageText(body))
	}
	text = prose(get("/settings/certificates?upload=removed"))
	if !strings.Contains(text, "Removed.") || !strings.Contains(text, "Uploaded certificates · 0 certificates") || !strings.Contains(text, "No uploaded certificates.") {
		t.Errorf("after Remove the page still lists the upload:\n%s", text)
	}
	// Traefik stops serving it once its file watch has run.
	waitFor(t, "Traefik to stop serving the removed upload", time.Minute, func() bool {
		conn, err := tls.Dial("tcp", labEdgeAddress, &tls.Config{ServerName: host, InsecureSkipVerify: true})
		if err != nil {
			return false
		}
		defer conn.Close()
		return !strings.Contains(strings.Join(conn.ConnectionState().PeerCertificates[0].Issuer.Organization, " "), "Console034 Company")
	})

	// 6. The private key is in no page and no log line.
	for where, html := range pages {
		if strings.Contains(html, keyBody) || strings.Contains(html, pemBody(otherKey)) {
			t.Errorf("%s holds a private key", where)
		}
	}
	if log := c.g.target.ServerLog; log != nil && strings.Contains(log(), keyBody) {
		t.Error("Caravana's log holds the private key")
	}
}
