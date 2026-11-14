package main

// The workload-identity gate step (spec 038, plan.md "Tests"). It is in its
// own file only because gate_test.go is long; runGate lists it with the
// other steps.

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// identityClaims are the claims of a Nomad workload identity the step checks
// (research R4).
type identityClaims struct {
	Iss       string `json:"iss"`
	Aud       string `json:"aud"`
	Exp       int64  `json:"exp"`
	Iat       int64  `json:"iat"`
	Job       string `json:"nomad_job_id"`
	Task      string `json:"nomad_task"`
	Namespace string `json:"nomad_namespace"`
}

// workloadIdentityStep (spec 038): a service asks for a workload identity and
// every copy gets a token the cluster signed, which Caravana never holds.
//
//  1. A busybox app with the identity bella (audience bella-spiffe/lab038,
//     TTL 60 s) deploys healthy, and the job's task has the identity block.
//  2. runCommand reads /secrets/nomad_bella.jwt in the copy (the owner's own
//     act; the answer is never kept): the signature verifies with one of the
//     keys Nomad publishes, aud is the audience, exp-iat the TTL, and the job,
//     task and namespace are the app, the service and default. On a host with
//     an issuer, iss is the issuer Caravana's discovery document names; in the
//     lab (no https public URL) the discovery answers 404 issuer_not_set.
//  3. Nomad renews the token within the TTL: the file holds a newer one.
//  4. Neither token is in any later answer about the app, the server's log or
//     the SQLite file, and the export plans against the live job with no diff.
func (g *gate) workloadIdentityStep(t *testing.T) {
	const id, audience, ttl = "lab038", "bella-spiffe/lab038", 60

	remove := func() {
		if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
			resp.Body.Close()
		}
	}
	remove() // leftovers from an aborted run, if any
	waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
		_, _, err := g.target.Nomad.Jobs().Info(id, nil)
		return err != nil
	})
	t.Cleanup(remove)

	// 1. Deploy.
	web := actionsWeb("", 1)
	web["identities"] = []map[string]any{{"name": "bella", "audience": []string{audience}, "ttlSeconds": ttl}}
	raw := g.sendWant(t, http.StatusAccepted, http.MethodPost, "/v1/apps/"+id+"/deployments", labApp(web))
	var first gateDeployment
	if err := json.Unmarshal(raw, &first); err != nil || first.ID == "" {
		t.Fatalf("deploy %s: %s", id, raw)
	}
	if final := g.waitFinalOf(t, id, first.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
	}
	job, _, err := g.target.Nomad.Jobs().Info(id, nil)
	if err != nil {
		t.Fatalf("read the job %s: %v", id, err)
	}
	ids := job.TaskGroups[0].Tasks[0].Identities
	if len(ids) != 1 || ids[0].Name != "bella" || !ids[0].File || ids[0].Env || ids[0].TTL != ttl*time.Second ||
		len(ids[0].Audience) != 1 || ids[0].Audience[0] != audience {
		t.Fatalf("the job's identities = %+v, want one file identity bella for %s lasting %ds", ids, audience, ttl)
	}
	var copies []string
	waitFor(t, "a running copy of "+id, time.Minute, func() bool {
		copies = g.runningCopies(t, id)
		return len(copies) == 1
	})

	// 2. The token.
	issuer := g.discoveryIssuer(t)
	keys := g.nomadKeys(t)
	// The first run of the step read an empty file right after the copy started, so a read waits
	// (briefly) until the file holds a JWT.
	readToken := func() string {
		var token string
		waitFor(t, "a token in /secrets/nomad_bella.jwt", 30*time.Second, func() bool {
			run, _, _ := g.runIn(t, id, copies[0], "cat", "/secrets/nomad_bella.jwt")
			if run.ExitCode == nil || *run.ExitCode != 0 {
				t.Fatalf("reading the token in the copy = exit %v, stderr %q", run.ExitCode, run.Stderr)
			}
			token = strings.TrimSpace(run.Stdout)
			return strings.Count(token, ".") == 2
		})
		return token
	}
	token := readToken()
	claims := verifyIdentity(t, token, keys)
	if claims.Aud != audience || claims.Exp-claims.Iat != ttl || claims.Job != id || claims.Task != "web" || claims.Namespace != "default" {
		t.Errorf("token claims = %+v, want aud %s, a %d s life, job %s, task web, namespace default", claims, audience, ttl, id)
	}
	if claims.Iss != issuer {
		t.Errorf("token iss = %q, want the issuer the discovery names, %q", claims.Iss, issuer)
	}
	// No environment variable: Nomad wouldn't refresh it (D1).
	if run, _, _ := g.runIn(t, id, copies[0], "sh", "-c", `env | grep -c '^NOMAD_TOKEN_bella=' || true`); strings.TrimSpace(run.Stdout) != "0" {
		t.Errorf("the copy has NOMAD_TOKEN_bella (%q), want only the file", run.Stdout)
	}

	// 3. Renewal: Nomad rewrites the file at about half the TTL.
	var renewed string
	waitFor(t, "Nomad to renew the token", 2*ttl*time.Second, func() bool {
		renewed = readToken()
		return renewed != token
	})
	if c := verifyIdentity(t, renewed, keys); c.Iat <= claims.Iat {
		t.Errorf("the renewed token was issued at %d, not after the first (%d)", c.Iat, claims.Iat)
	}

	// 4. Caravana holds neither.
	var later []string
	for _, p := range []string{"/v1/apps/" + id, "/v1/apps/" + id + "/deployments", "/v1/apps/" + id + "/copies",
		"/v1/apps/" + id + "/actions", "/v1/apps/" + id + "/logs", "/v1/apps/" + id + "/jobspec", "/v1/apps", "/v1/attention"} {
		later = append(later, string(g.sendWant(t, http.StatusOK, http.MethodGet, p, "")))
	}
	if g.target.ServerLog != nil {
		later = append(later, g.target.ServerLog())
	}
	if g.target.DataDir != "" {
		files, _ := filepath.Glob(filepath.Join(g.target.DataDir, "*"))
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil {
				later = append(later, string(b))
			}
		}
	}
	for i, text := range later {
		for _, tok := range []string{token, renewed} {
			// The signature alone is a credential's proof; search for it too.
			sig := tok[strings.LastIndex(tok, ".")+1:]
			if strings.Contains(text, tok) || strings.Contains(text, sig) {
				t.Fatalf("a workload identity token is in Caravana's answer, log or data (#%d)", i)
			}
		}
	}
	g.exportMatchesLive(t, id)
}

// discoveryIssuer is the issuer Caravana's discovery document names, or ""
// when the installation has none (the lab: its public URL isn't https), in
// which case the document answers 404 issuer_not_set and the keys too.
func (g *gate) discoveryIssuer(t *testing.T) string {
	t.Helper()
	get := func(path string) (int, []byte) {
		resp, err := g.client().Get(g.target.API + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	status, body := get("/.well-known/openid-configuration")
	if status == http.StatusNotFound {
		if !strings.Contains(string(body), "issuer_not_set") {
			t.Fatalf("discovery = 404 %s, want issuer_not_set", body)
		}
		if s, b := get("/.well-known/jwks.json"); s != http.StatusNotFound {
			t.Fatalf("jwks without an issuer = %d %s, want 404", s, b)
		}
		return ""
	}
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &doc) != nil || doc.Issuer == "" {
		t.Fatalf("discovery = %d %s, want Nomad's document with an issuer", status, body)
	}
	if doc.JWKSURI != doc.Issuer+"/.well-known/jwks.json" {
		t.Errorf("discovery's jwks_uri = %q, want it under the issuer %q", doc.JWKSURI, doc.Issuer)
	}
	if s, b := get("/.well-known/jwks.json"); s != http.StatusOK || !strings.Contains(string(b), `"keys"`) {
		t.Errorf("jwks = %d %s, want Nomad's keys", s, b)
	}
	return doc.Issuer
}

// nomadKeys reads the RSA keys Nomad publishes, by key ID.
func (g *gate) nomadKeys(t *testing.T) map[string]*rsa.PublicKey {
	t.Helper()
	resp, err := http.Get(g.target.Nomad.Address() + "/.well-known/jwks.json")
	if err != nil {
		t.Fatalf("read Nomad's keys: %v", err)
	}
	defer resp.Body.Close()
	var set struct {
		Keys []struct {
			Kid, Kty, N, E string
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		t.Fatalf("Nomad's keys: %v", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil {
			t.Fatalf("Nomad's key %s isn't base64url", k.Kid)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(keys) == 0 {
		t.Fatal("Nomad publishes no RSA key")
	}
	return keys
}

// verifyIdentity checks an RS256 token's signature with the key its header
// names and returns its claims. It never prints the token.
func verifyIdentity(t *testing.T, token string, keys map[string]*rsa.PublicKey) identityClaims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("the token has %d parts, want a JWT's 3", len(parts))
	}
	decode := func(i int, into any) {
		b, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err == nil {
			err = json.Unmarshal(b, into)
		}
		if err != nil {
			t.Fatalf("the token's part %d: %v", i, err)
		}
	}
	var head struct{ Alg, Kid string }
	decode(0, &head)
	var claims identityClaims
	decode(1, &claims)
	key, ok := keys[head.Kid]
	if head.Alg != "RS256" || !ok {
		t.Fatalf("the token is %s with key %s, want RS256 with a key Nomad publishes", head.Alg, head.Kid)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("the token's signature isn't base64url")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatal(fmt.Errorf("the token's signature doesn't verify with Nomad's key %s: %w", head.Kid, err))
	}
	return claims
}
