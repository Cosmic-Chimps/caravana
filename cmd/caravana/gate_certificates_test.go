package main

// The certificates gate step (feature 034, tasks.md T017). It is in its own file only because
// gate_test.go is long; runGate lists it with the other steps. It runs only in the lab: it needs a
// Pebble ACME server in the lab VM, restarts Caravana with a certificate resolver and restarts
// Traefik.

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/certs/certstest"
)

const (
	certPebbleJob = "gate034-pebble"
	certPebbleDir = "https://localhost:14000/dir"
	certACMEApp   = "gate034-acme"
	certUpApp     = "gate034-byo"
	certACMEHost  = "acme.gate034.test"
	certHostA     = "a.byo.gate034.test"
	certHostB     = "b.byo.gate034.test"
	certWildcard  = "*.byo.gate034.test"
	certProbeHost = "probe.byo.gate034.test" // no router: Traefik answers it from its store alone
)

// certPebble is a Pebble ACME server that requires External Account Binding, validates every
// challenge without calling back (PEBBLE_VA_ALWAYS_VALID) and issues 20-day certificates, so Traefik
// renews them whenever it starts (it renews what ends within 30 days). Its own HTTPS certificate is
// the step's, so the step knows the CA Caravana and Traefik must trust to talk to it.
func certPebble(kid, hmac string, serverCert, serverKey []byte) *api.Job {
	config := fmt.Sprintf(`{"pebble": {"listenAddress": "0.0.0.0:14000", "managementListenAddress": "0.0.0.0:15000",
  "certificate": "/local/cert.pem", "privateKey": "/local/key.pem", "httpPort": 5002, "tlsPort": 5001,
  "ocspResponderURL": "", "externalAccountBindingRequired": true,
  "externalAccountMACKeys": {%q: %q},
  "profiles": {"default": {"description": "short", "validityPeriod": 1728000}}}}`, kid, hmac)
	return &api.Job{
		ID: new(certPebbleJob), Name: new(certPebbleJob), Type: new("service"),
		TaskGroups: []*api.TaskGroup{{
			Name:     new("pebble"),
			Networks: []*api.NetworkResource{{Mode: "host"}},
			Tasks: []*api.Task{{
				Name: "pebble", Driver: "docker",
				Env: map[string]string{"PEBBLE_VA_ALWAYS_VALID": "1", "PEBBLE_VA_NOSLEEP": "1", "PEBBLE_WFE_NONCEREJECT": "0"},
				Config: map[string]any{
					"image": "ghcr.io/letsencrypt/pebble:latest", "network_mode": "host",
					"args": []string{"-config", "/local/pebble-config.json"},
				},
				Templates: []*api.Template{
					{DestPath: new("local/pebble-config.json"), EmbeddedTmpl: new(config)},
					{DestPath: new("local/cert.pem"), EmbeddedTmpl: new(string(serverCert))},
					{DestPath: new("local/key.pem"), EmbeddedTmpl: new(string(serverKey))},
				},
				Resources: &api.Resources{CPU: new(50), MemoryMB: new(64)},
			}},
		}},
	}
}

// certApp is a whoami app answering on hosts, all on the service's one router.
func certApp(hosts ...string) string {
	domains := make([]any, len(hosts))
	for i, h := range hosts {
		domains[i] = map[string]string{"host": h, "port": "http"}
	}
	body, _ := json.Marshal(map[string]any{
		"source": map[string]string{"kind": "image"},
		"services": []any{map[string]any{
			"name": "web", "image": "traefik/whoami:latest",
			"ports":     []any{map[string]any{"name": "http", "containerPort": 80}},
			"domains":   domains,
			"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
			"resources": map[string]any{"memoryMB": 16, "memoryMaxMB": 32},
		}},
	})
	return string(body)
}

// presented is what the edge shows for host: the leaf's issuer, serial and names.
type presented struct {
	Issuer string
	Serial string
	Names  []string
}

func (g *gate) presentedCert(host string) (presented, error) {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", labEdgeAddress,
		&tls.Config{ServerName: host, InsecureSkipVerify: true}) // the step reads the certificate; it trusts nothing
	if err != nil {
		return presented{}, err
	}
	defer conn.Close()
	leaf := conn.ConnectionState().PeerCertificates[0]
	issuer := leaf.Issuer.CommonName
	if len(leaf.Issuer.Organization) > 0 {
		issuer = leaf.Issuer.Organization[0] + " / " + issuer
	}
	return presented{Issuer: issuer, Serial: leaf.SerialNumber.String(), Names: leaf.DNSNames}, nil
}

func (g *gate) waitPresented(t *testing.T, host, what string, within time.Duration, ok func(presented) bool) presented {
	t.Helper()
	var p presented
	waitFor(t, host+" to present "+what, within, func() bool {
		var err error
		p, err = g.presentedCert(host)
		return err == nil && ok(p)
	})
	return p
}

// certsSettings is the part of CertificateSettings the step reads.
type certsSettings struct {
	Issuer            string `json:"issuer"`
	IssuerName        string `json:"issuerName"`
	IssuerUnavailable string `json:"issuerUnavailable"`
	UploadUnavailable string `json:"uploadUnavailable"`
	ACME              *struct {
		Server    string `json:"server"`
		EABKeyID  string `json:"eabKeyId"`
		EABKeySet bool   `json:"eabKeySet"`
		PrivateCA bool   `json:"privateCA"`
	} `json:"acme"`
}

// certsUpload is the part of UploadedCertificate the step reads.
type certsUpload struct {
	ID          string   `json:"id"`
	Names       []string `json:"names"`
	Status      string   `json:"status"`
	Trusted     bool     `json:"trusted"`
	Replaced    string   `json:"replaced"`
	Domains     []string `json:"domains"`
	KeptEarlier []struct {
		Host string `json:"host"`
	} `json:"keptEarlier"`
}

// certificatesStep (feature 034):
//
//  1. a Pebble ACME server requiring EAB runs in the lab, and Caravana restarts with the resolver
//     letsencrypt, so routers ask Traefik's ACME resolver;
//  2. the issuer is refused without the EAB pair (the directory requires it) and at an address that
//     isn't a directory, then set to Pebble: Traefik restarts on its own and a new domain gets
//     Pebble's certificate; restarting Traefik renews it (20-day certificates);
//  3. a wildcard uploaded for *.byo.gate034.test serves a. and b. with no ACME request for them, and
//     Domains names the upload; a replacement that ends in 5 days is a needs-you item;
//  4. removing the upload hands both names back to Pebble;
//  5. the HMAC key and the uploaded private key are in no answer, log line or data file;
//  6. resetting the issuer gives the installer's back.
func (g *gate) certificatesStep(t *testing.T) {
	if g.target.SetEnv == nil || g.target.RestartCaravana == nil || g.target.Public != nil {
		t.Skip("lab only: the step runs a Pebble ACME server in the lab VM and restarts Caravana")
	}
	nomad := g.target.Nomad
	var bodies []string // every answer, searched for the secrets at the end
	send := func(method, path, body string, want int) []byte {
		t.Helper()
		status, raw := g.send(t, method, path, body)
		bodies = append(bodies, string(raw))
		if status != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, status, want, raw)
		}
		return raw
	}
	settings := func() certsSettings {
		t.Helper()
		var s certsSettings
		if err := json.Unmarshal(send(http.MethodGet, "/v1/settings/certificates", "", http.StatusOK), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// The EAB key: random, so it is its own sentinel.
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	hmac := base64.RawURLEncoding.EncodeToString(raw)
	// The key ID names this run's store file in Traefik (acme-<hash of server and key ID>.json), so the
	// certificates an earlier run got from Pebble aren't in it: an exact name Traefik already holds
	// beats an uploaded wildcard (research R5).
	kid := "kid-gate034-" + hmac[:8]

	// Pebble's own HTTPS certificate, and the CA that signs it.
	pebbleCA := certstest.NewCA(t, "Gate 034 Pebble server CA")
	pebbleLeaf := certstest.NewLeaf(t, pebbleCA, []string{"localhost"}, time.Now().Add(-time.Hour), time.Now().Add(48*time.Hour))

	cleanup := func() {
		for _, id := range []string{certACMEApp, certUpApp} {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
				resp.Body.Close()
			}
		}
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/settings/certificates/issuer", "", ""); err == nil {
			resp.Body.Close()
		}
		var list struct {
			Certificates []certsUpload `json:"certificates"`
		}
		if resp, err := g.apiRequest(http.MethodGet, "/v1/certificates/uploaded", "", ""); err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&list)
			resp.Body.Close()
		}
		for _, c := range list.Certificates {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/certificates/uploaded/"+c.ID, "", ""); err == nil {
				resp.Body.Close()
			}
		}
		_, _, _ = nomad.Jobs().Deregister(certPebbleJob, true, nil)
	}
	cleanup() // leftovers of an aborted run
	t.Cleanup(func() {
		cleanup()
		g.target.SetEnv("CARAVANA_CERT_RESOLVER", "")
		g.target.RestartCaravana(t)
	})

	if s := settings(); s.IssuerUnavailable == "traefik_outdated" {
		t.Fatal("the lab's Traefik job is older than feature 034: run deploy/dev/lab.sh up from this branch")
	}

	// 1. Pebble, and Caravana with the resolver.
	if _, _, err := nomad.Jobs().Register(certPebble(kid, hmac, pebbleLeaf.PEM(), pebbleLeaf.KeyPEM(t, certstest.PKCS8)), nil); err != nil {
		t.Fatalf("register Pebble: %v", err)
	}
	pebbleClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: certstest.Pool(pebbleCA)},
	}}
	waitFor(t, "Pebble's directory to answer on the Mac's localhost:14000", 2*time.Minute, func() bool {
		resp, err := pebbleClient.Get(certPebbleDir)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	g.target.SetEnv("CARAVANA_CERT_RESOLVER", "letsencrypt")
	g.target.RestartCaravana(t)

	// 2. The issuer.
	issuer := func(fields map[string]any) string {
		fields["server"], fields["email"] = certPebbleDir, "ops@gate034.test"
		fields["caCertificate"] = string(pebbleCA.PEM())
		b, _ := json.Marshal(fields)
		return string(b)
	}
	if raw := send(http.MethodPut, "/v1/settings/certificates/issuer", issuer(map[string]any{}), http.StatusUnprocessableEntity); !strings.Contains(string(raw), `"field":"eabKeyId"`) {
		t.Errorf("an issuer that requires EAB, without it: want a refusal on eabKeyId, got %s", raw)
	}
	notDir, _ := json.Marshal(map[string]any{"server": "https://localhost:15000/no-such-directory", "email": "ops@gate034.test",
		"caCertificate": string(pebbleCA.PEM())})
	if raw := send(http.MethodPut, "/v1/settings/certificates/issuer", string(notDir), http.StatusUnprocessableEntity); !strings.Contains(string(raw), `"field":"server"`) {
		t.Errorf("an address that isn't a directory: want a refusal on server, got %s", raw)
	}
	send(http.MethodPut, "/v1/settings/certificates/issuer", issuer(map[string]any{"eabKeyId": kid, "eabHmacKey": hmac}), http.StatusOK)
	if s := settings(); s.Issuer != "custom" || s.ACME == nil || s.ACME.Server != certPebbleDir || !s.ACME.EABKeySet || !s.ACME.PrivateCA {
		t.Errorf("settings after the save = %+v", s)
	}

	var d gateDeployment
	_ = json.Unmarshal(send(http.MethodPost, "/v1/apps/"+certACMEApp+"/deployments", certApp(certACMEHost), http.StatusAccepted), &d)
	if fin := g.waitFinalOf(t, certACMEApp, d.ID, 3*time.Minute); fin.Status != "healthy" {
		t.Fatalf("%s: deployment %s", certACMEApp, fin.Status)
	}
	isPebble := func(p presented) bool { return strings.Contains(p.Issuer, "Pebble") }
	first := g.waitPresented(t, certACMEHost, "a certificate from Pebble", 2*time.Minute, isPebble)

	// Renewal: Traefik renews at start what ends within 30 days, and Pebble's last 20.
	allocs, _, err := nomad.Jobs().Allocations("traefik", false, nil)
	if err != nil || len(allocs) == 0 {
		t.Fatalf("Traefik's allocations: %v", err)
	}
	for _, a := range allocs {
		if a.ClientStatus == "running" {
			alloc, _, err := nomad.Allocations().Info(a.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := nomad.Allocations().Restart(alloc, "", nil); err != nil {
				t.Fatalf("restart Traefik: %v", err)
			}
		}
	}
	g.waitPresented(t, certACMEHost, "a renewed certificate from Pebble", 2*time.Minute, func(p presented) bool {
		return isPebble(p) && p.Serial != first.Serial
	})

	// 3. An uploaded wildcard from a private CA.
	company := certstest.NewCA(t, "Gate 034 Company Root", certstest.Organization("Gate034 Company"))
	wild := certstest.NewLeaf(t, company, []string{certWildcard}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))
	wildKey := string(wild.KeyPEM(t, certstest.PKCS8))
	upload := func(leaf string, key string) certsUpload {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"certificate": leaf, "key": key})
		var u certsUpload
		if err := json.Unmarshal(send(http.MethodPost, "/v1/certificates/uploaded", string(b), http.StatusCreated), &u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	up := upload(string(certstest.Chain(wild.Cert, company.Cert)), wildKey)
	if len(up.Names) != 1 || up.Names[0] != certWildcard || up.Trusted || up.Status != "valid" {
		t.Errorf("the upload = %+v", up)
	}
	uploadedAt := time.Now()
	// Traefik picks the upload up through its file watch, a few seconds after the variable. The
	// domains are added only once it serves the upload: a router that came first would make Traefik
	// order a certificate for its names, and an exact name beats the wildcard (research R5; the
	// console reports such a host as still on its earlier certificate).
	isCompany := func(p presented) bool { return strings.Contains(p.Issuer, "Gate034 Company") }
	g.waitPresented(t, certProbeHost, "the uploaded certificate", time.Minute, isCompany)
	_ = json.Unmarshal(send(http.MethodPost, "/v1/apps/"+certUpApp+"/deployments", certApp(certHostA, certHostB), http.StatusAccepted), &d)
	if fin := g.waitFinalOf(t, certUpApp, d.ID, 3*time.Minute); fin.Status != "healthy" {
		t.Fatalf("%s: deployment %s", certUpApp, fin.Status)
	}
	for _, h := range []string{certHostA, certHostB} {
		g.waitPresented(t, h, "the uploaded certificate", time.Minute, isCompany)
	}
	// Give Traefik's resolver time to (not) ask, then read its log since the upload.
	time.Sleep(15 * time.Second)
	if log := g.traefikLogSince(t, uploadedAt); strings.Contains(log, "byo.gate034.test") {
		t.Errorf("Traefik's log names the uploaded hosts, so the ACME resolver was asked for them:\n%s", grepLines(log, "byo.gate034.test"))
	}
	waitFor(t, "Domains to name the upload for both hosts", time.Minute, func() bool {
		var u certsUpload
		_ = json.Unmarshal(send(http.MethodGet, "/v1/certificates/uploaded/"+up.ID, "", http.StatusOK), &u)
		g.domainRows(t, certUpApp, true) // check them now
		return len(u.Domains) == 2
	})
	for host, row := range g.domainRows(t, certUpApp, false) {
		if !strings.Contains(row.Certificate.Issuer, "Gate034 Company") {
			t.Errorf("%s: certificate issuer %q, want the upload's", host, row.Certificate.Issuer)
		}
	}

	// The replacement ends in 5 days: one needs-you item for the certificate.
	soon := certstest.NewLeaf(t, company, []string{certWildcard}, time.Now().Add(-time.Hour), time.Now().Add(5*24*time.Hour))
	rep := upload(string(certstest.Chain(soon.Cert, company.Cert)), string(soon.KeyPEM(t, certstest.PKCS8)))
	if rep.Replaced != up.ID || rep.Status != "expiring" {
		t.Errorf("the replacement = %+v, want it to replace %s and be expiring", rep, up.ID)
	}
	waitFor(t, "the needs-you item certificate_expiring", time.Minute, func() bool {
		return strings.Contains(string(send(http.MethodGet, "/v1/attention", "", http.StatusOK)), `"certificate":"`+rep.ID+`"`)
	})
	g.waitPresented(t, certHostA, "the replacement", time.Minute, func(p presented) bool {
		return isCompany(p) && p.Serial == soon.Cert.SerialNumber.String()
	})

	// 4. Removed: both names go back to the ACME issuer.
	send(http.MethodDelete, "/v1/certificates/uploaded/"+rep.ID, "", http.StatusNoContent)
	for _, h := range []string{certHostA, certHostB} {
		g.waitPresented(t, h, "a certificate from Pebble after the removal", 2*time.Minute, isPebble)
	}

	// 6. Back to the installer's issuer.
	send(http.MethodDelete, "/v1/settings/certificates/issuer", "", http.StatusOK)
	if s := settings(); s.Issuer == "custom" || s.ACME != nil {
		t.Errorf("after the reset: %+v", s)
	}

	// 5. The secrets: the HMAC key and the uploaded key's body.
	keyBody := pemBody(wildKey)
	for _, b := range bodies {
		if strings.Contains(b, hmac) || strings.Contains(b, keyBody) {
			t.Fatalf("an answer holds a secret: %.200s", b)
		}
	}
	if log := g.target.ServerLog(); strings.Contains(log, hmac) || strings.Contains(log, keyBody) {
		t.Error("Caravana's log holds a secret")
	}
	_ = filepath.Walk(g.target.DataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), hmac) || strings.Contains(string(data), keyBody) {
			t.Errorf("%s holds a secret", path)
		}
		return nil
	})
	job, _, err := nomad.Jobs().Info("traefik", nil)
	if err == nil {
		spec, _ := json.Marshal(job)
		if strings.Contains(string(spec), hmac) {
			t.Error("Traefik's job holds the HMAC key")
		}
	}
}

// traefikLogSince is Traefik's own log (stderr) of its running allocation, the lines from since on.
func (g *gate) traefikLogSince(t *testing.T, since time.Time) string {
	t.Helper()
	allocs, _, err := g.target.Nomad.Jobs().Allocations("traefik", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range allocs {
		if a.ClientStatus != "running" {
			continue
		}
		alloc, _, err := g.target.Nomad.Allocations().Info(a.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		frames, errs := g.target.Nomad.AllocFS().Logs(alloc, false, "traefik", "stderr", "end", 1<<20, nil, nil)
		var b strings.Builder
		for {
			select {
			case f, ok := <-frames:
				if !ok {
					return linesSince(b.String(), since)
				}
				b.Write(f.Data)
			case err := <-errs:
				if err != nil {
					t.Fatalf("Traefik's log: %v", err)
				}
				return linesSince(b.String(), since)
			}
		}
	}
	t.Fatal("Traefik has no running allocation")
	return ""
}

// linesSince keeps the lines whose leading RFC 3339 time is at or after since (Traefik's log lines
// start with it, after the colour codes it may write).
func linesSince(log string, since time.Time) string {
	var out []string
	for _, l := range strings.Split(log, "\n") {
		clean := stripColour(l)
		ts, _, _ := strings.Cut(clean, " ")
		at, err := time.Parse(time.RFC3339, ts)
		if err == nil && !at.Before(since.Add(-time.Second)) {
			out = append(out, clean)
		}
	}
	return strings.Join(out, "\n")
}

func stripColour(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func grepLines(s, want string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, want) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// pemBody is the first base64 line of a PEM block: the part a leak would carry, with or without its
// line breaks.
func pemBody(p string) string {
	for _, l := range strings.Split(p, "\n") {
		if l != "" && !strings.HasPrefix(l, "-----") {
			return l
		}
	}
	return p
}
