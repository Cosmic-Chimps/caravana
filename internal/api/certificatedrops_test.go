package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/nodeagent"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 056: dropping the earlier certificates an upload covers. The routes are an administrator's;
// the test then plays the whole flow with nothing in the middle: the real agent of the first server
// holds a Traefik store with a sentinel in every certificate, key and account, the service hands it the
// request in the sync answer, the agent rewrites the store and says so in its report, and Caravana
// restarts Traefik and ends the drop. Every JSON is checked against the contract, and the sentinels are
// searched for in everything the API said.

const (
	dropSentinelCert    = "c2VjcmV0LWNlcnQtc2VudGluZWwtMDU2"
	dropSentinelKey     = "c2VjcmV0LWtleS1zZW50aW5lbC0wNTY="
	dropSentinelAccount = "c2VjcmV0LWFjY291bnQtc2VudGluZWwtMDU2"
)

type dropAnswer struct {
	ID          string     `json:"id"`
	Server      string     `json:"server"`
	Status      string     `json:"status"`
	Dropped     []string   `json:"dropped"`
	Error       string     `json:"error"`
	Actor       string     `json:"actor"`
	RequestedAt time.Time  `json:"requestedAt"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

func acmeStoreJSON() string {
	cert := func(main string) map[string]any {
		return map[string]any{"domain": map[string]any{"main": main}, "certificate": dropSentinelCert + "-" + main, "key": dropSentinelKey + "-" + main}
	}
	b, _ := json.MarshalIndent(map[string]any{"letsencrypt": map[string]any{
		"Account":      map[string]any{"Email": "ops@example.com", "PrivateKey": dropSentinelAccount},
		"Certificates": []any{cert("a.example.org"), cert("other.test")},
	}}, "", "  ")
	return string(b)
}

func TestDropEarlierCertificatesThroughTheRealAgent(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t, "")
	doc := loadContract(t)
	caCert, caKey, err := clusterca.New(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": string(caCert), "key": string(caKey)})
	e.Nomad.SetVariable("caravana/cluster/gossip", map[string]string{"key": "c2VjcmV0LXNlbnRpbmVsLWdvc3NpcDMyYnl0ZXMhISE=\n"})
	priv, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	token, err := e.Service.AdoptFirst(ctx, "hel-1", pub, "203.0.113.4")
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.AddNode("node-1", "hel-1", "ready", "10.99.0.1")

	var stdout, stderr bytes.Buffer
	root := t.TempDir()
	agent := &nodeagent.Agent{
		Root: root, Runner: &fakeHost{hostname: "hel-1.example.com"}, HTTP: nodeagent.NewHTTPClient(false),
		Version: "0.0.1-e2e", NomadAddr: localNomad(t).URL, Stdout: &stdout, Stderr: &stderr, Sleep: func(time.Duration) {},
	}
	if err := agent.SaveNodeEnv(nodeagent.NodeEnv{URL: e.RawURL, Token: token}); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveState(nodeagent.State{Index: 1, Name: "hel-1", Pool: "default", First: true, Role: nodeagent.RoleServer, Servers: []int{1}}); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("etc/caravana-node/mesh.key", priv+"\n")
	write("etc/caravana-node/gossip.key", "c2VjcmV0LXNlbnRpbmVsLWdvc3NpcDMyYnl0ZXMhISE=\n")
	write("etc/nomad.d/tls/ca.pem", string(caCert))
	storePath := write("var/lib/caravana-host/traefik/acme.json", acmeStoreJSON())

	said := []string{} // everything the API answered
	do := func(method, path, body string) (int, http.Header, []byte) {
		status, hdr, raw := call(t, method, e.URL+path, "application/json", body)
		said = append(said, string(raw))
		return status, hdr, raw
	}
	sync := func() {
		t.Helper()
		if err := agent.Sync(ctx, nodeagent.SyncOptions{}); err != nil {
			t.Fatalf("Sync: %v\n%s", err, stderr.String())
		}
	}

	// Nothing is uploaded: nothing to drop.
	status, hdr, raw := do(http.MethodPost, "/v1/certificates/drops", "")
	wantError(t, "no upload", status, raw, http.StatusConflict, codeNoUploadedCertificates, "")
	if hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", hdr.Get("Cache-Control"))
	}
	if err := e.Store.InsertUploadedCertificate(ctx, store.UploadedCertificate{ID: "crt_000000000001", Names: []string{"a.example.org", "*.byo.test"},
		Subject: "CN=a.example.org", Issuer: "CN=Test", NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		Fingerprint: strings.Repeat("a", 64), KeyType: "ECDSA P-256", Trusted: true, UploadedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// The agent hasn't reported that it holds the store: run the installer.
	status, _, raw = do(http.MethodPost, "/v1/certificates/drops", "")
	body := wantError(t, "no agent", status, raw, http.StatusConflict, codeNoAgent, "")
	if !strings.Contains(body.Error.Message, "run the installer on server hel-1") {
		t.Errorf("message = %q", body.Error.Message)
	}

	sync() // the agent says it holds the store
	status, hdr, raw = do(http.MethodPost, "/v1/certificates/drops", "")
	if status != http.StatusAccepted {
		t.Fatalf("dropEarlierCertificates = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "CertificateDrop")
	d := decodeAs[dropAnswer](t, raw)
	if d.Status != "requested" || d.Server != "hel-1" || len(d.ID) != 32 || d.Dropped == nil || len(d.Dropped) != 0 ||
		hdr.Get("Location") != "/v1/certificates/drops/"+d.ID || hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("drop = %+v, Location %q", d, hdr.Get("Location"))
	}
	status, _, raw = do(http.MethodPost, "/v1/certificates/drops", "")
	wantError(t, "a second drop", status, raw, http.StatusConflict, codeCertificateDropRunning, "")

	// The answer of a sync, as the real handler writes it, is the contract's, and carries the request.
	report, _ := json.Marshal(map[string]any{
		"agent": "0.0.1", "nomad": "2.0.7", "docker": "27.5.1", "dockerRunning": true, "diskFreeMb": 1, "diskTotalMb": 2,
		"memoryMb": 3, "kernel": "6", "role": "server", "nomadLeader": true, "serverRunning": true, "reach": []any{},
		"traefikStore":    true,
		"certificateDrop": map[string]any{"id": strings.Repeat("0", 32), "state": "dropped", "dropped": []string{"old.example.org+www.old.example.org"}},
	})
	checkSchema(t, doc, report, "NodeReport")
	status, _, raw = callWith(t, "Bearer "+token, http.MethodPost, e.RawURL+"/v1/node/sync", "application/json", string(report))
	if status != http.StatusOK {
		t.Fatalf("sync = %d %s", status, raw)
	}
	checkSchema(t, doc, raw, "NodeSyncAnswer")
	var syncAns struct {
		CertificateDrop struct {
			ID    string   `json:"id"`
			Names []string `json:"names"`
		} `json:"certificateDrop"`
	}
	if err := json.Unmarshal(raw, &syncAns); err != nil {
		t.Fatal(err)
	}
	if syncAns.CertificateDrop.ID != d.ID || strings.Join(syncAns.CertificateDrop.Names, ",") != "*.byo.test,a.example.org" {
		t.Errorf("the sync answer's certificateDrop = %+v", syncAns.CertificateDrop)
	}
	status, _, raw = do(http.MethodGet, "/v1/certificates/drops/"+d.ID, "")
	if got := decodeAs[dropAnswer](t, raw); status != http.StatusOK || got.Status != "running" || got.StartedAt == nil {
		t.Fatalf("after the hand-out = %d %s", status, raw)
	}

	// Traefik runs, started before the rewrite; Nomad's restart moves its start.
	started := time.Now().Add(-time.Hour)
	stub := api.AllocationListStub{ID: "tr1", TaskGroup: "traefik", ClientStatus: "running", JobVersion: 3,
		TaskStates: map[string]*api.TaskState{"traefik": {StartedAt: started}}, CreateTime: started.UnixNano()}
	e.Nomad.SetTraefikJob("traefik:v3.7.13", "--accesslog=true")
	e.Nomad.SetCopies("traefik", stub)
	e.Nomad.OnRestart(func(id string) {
		next := stub
		next.TaskStates = map[string]*api.TaskState{"traefik": {StartedAt: time.Now().Add(time.Second)}}
		e.Nomad.SetCopies("traefik", next)
	})

	sync() // the real agent drops what the upload covers and reports it at once
	var got dropAnswer
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, _, raw = do(http.MethodGet, "/v1/certificates/drops/"+d.ID, "")
		checkSchema(t, doc, raw, "CertificateDrop")
		got = decodeAs[dropAnswer](t, raw)
		if got.Status == "done" || got.Status == "failed" || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != http.StatusOK || got.Status != "done" || strings.Join(got.Dropped, ",") != "a.example.org" || got.FinishedAt == nil ||
		got.Error != "" || got.Actor != "admin@example.com" {
		t.Fatalf("the drop = %+v (stderr %s)", got, stderr.String())
	}
	if rs := e.Nomad.Restarts(); len(rs) != 1 || rs[0].Copy != "tr1" {
		t.Errorf("restarts = %+v, want Traefik's copy once", rs)
	}
	after, _ := os.ReadFile(storePath)
	if strings.Contains(string(after), "a.example.org") || !strings.Contains(string(after), "other.test") ||
		!strings.Contains(string(after), dropSentinelAccount) {
		t.Errorf("the store after the drop:\n%s", after)
	}

	// The list and the unknown ID.
	status, hdr, raw = do(http.MethodGet, "/v1/certificates/drops", "")
	if status != http.StatusOK || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("listCertificateDrops = %d %s", status, raw)
	}
	list := decodeAs[struct {
		Drops []dropAnswer `json:"drops"`
	}](t, raw)
	if len(list.Drops) != 1 || list.Drops[0].ID != d.ID {
		t.Errorf("list = %s", raw)
	}
	for _, drop := range list.Drops {
		b, _ := json.Marshal(drop)
		checkSchema(t, doc, b, "CertificateDrop")
	}
	status, _, raw = do(http.MethodGet, "/v1/certificates/drops/"+strings.Repeat("0", 32), "")
	wantError(t, "an unknown drop", status, raw, http.StatusNotFound, codeCertificateDropNotFound, "id")

	// Once the first has ended another may be asked for.
	status, _, raw = do(http.MethodPost, "/v1/certificates/drops", "")
	if status != http.StatusAccepted {
		t.Fatalf("a second drop after the first = %d %s", status, raw)
	}

	// The certificates, keys and accounts of the store are in no answer, and not in the agent's output.
	for _, text := range append(said, stdout.String(), stderr.String()) {
		for _, secret := range []string{dropSentinelCert, dropSentinelKey, dropSentinelAccount} {
			if strings.Contains(text, secret) {
				t.Errorf("%q reached %.200s", secret, text)
			}
		}
	}
}

// The three routes are for an administrator with a credential.
func TestCertificateDropsNeedCredentials(t *testing.T) {
	e := newTestEnv(t, "")
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/certificates/drops"},
		{http.MethodGet, "/v1/certificates/drops"},
		{http.MethodGet, "/v1/certificates/drops/" + strings.Repeat("a", 32)},
	} {
		status, _, raw := call(t, c.method, e.RawURL+c.path, "application/json", `{}`)
		wantError(t, c.method+" "+c.path+" without credentials", status, raw, http.StatusUnauthorized, codeUnauthorized, "")
	}
}
