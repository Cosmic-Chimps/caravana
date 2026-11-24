package api

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 051: the console's address operations, checked against the contract. The service's own tests
// cover the checks; these cover the routes, the answers, the codes and the headers.

// consoleAddressEnv is an API whose console is https://console.example.com, with fake DNS that knows
// new.example.com (here) and elsewhere.example.com (another address), a CA the edge trusts, and a
// Traefik job that reads the console's hosts from its variable.
type consoleAddressEnv struct {
	*testEnv
	routeOK bool
}

func newConsoleAddressEnv(t *testing.T) *consoleAddressEnv {
	t.Helper()
	e := &consoleAddressEnv{routeOK: true}
	here := []string{"203.0.113.1"}
	ca := edgetest.NewCA(t, "Test CA")
	chains := map[string][]*x509.Certificate{}
	for _, h := range []string{"console.example.com", "new.example.com"} {
		chains[h] = []*x509.Certificate{ca.Issue(t, []string{h}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))}
	}
	e.testEnv = newTestEnvWith(t, "", func(s *service.Service) {
		s.SetMetricsSampleGap(time.Hour)
		s.SetConsoleAddressPassGap(time.Hour)
		s.SetPublicURL("https://console.example.com")
		s.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{
			"console.example.com":   here,
			"new.example.com":       here,
			"elsewhere.example.com": {"198.51.100.7"},
		}}, &edgetest.FakeDialer{Chains: chains, RootCAs: ca.Pool(), Default: []*x509.Certificate{edgetest.TraefikDefault(t)}})
		s.SetEdgeAddress("127.0.0.1:8443")
		s.SetCertResolver("letsencrypt")
		s.SetConsoleRouteProbe(func(context.Context, string, string) error {
			if !e.routeOK {
				return errors.New("not routed")
			}
			return nil
		})
	})
	e.Nomad.SetTraefikConsoleRoute("traefik:v3.7.13")
	return e
}

// agent makes the first server report that its agent runs the console.
func (e *consoleAddressEnv) agent(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	caCert, caKey, err := clusterca.New(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.SetVariable("caravana/cluster/ca", map[string]string{"cert": string(caCert), "key": string(caKey)})
	e.Nomad.SetVariable("caravana/cluster/gossip", map[string]string{"key": "c2VjcmV0LXNlbnRpbmVsLWdvc3NpcDMyYnl0ZXMhISE=\n"})
	_, pub, err := mesh.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	token, err := e.Service.AdoptFirst(ctx, "hel-1", pub, "203.0.113.4")
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.AddNode("node-1", "hel-1", "ready", "10.99.0.1")
	r := service.NodeReport{Agent: "0.18.0", Nomad: "2.0.7", Docker: "27.5.1", DockerRunning: true, DiskFreeMB: 160000,
		DiskTotalMB: 190000, MemoryMB: 3916, Kernel: "6.8.0", Role: "client", Reach: []service.NodeReach{}, RunsConsole: true}
	if _, err := e.Service.NodeSync(ctx, token, r); err != nil {
		t.Fatalf("NodeSync: %v", err)
	}
}

// getNoStore is getJSON for an answer that must say Cache-Control: no-store.
func getNoStore(t *testing.T, e *testEnv, path, schema string) map[string]any {
	t.Helper()
	status, header, raw := call(t, http.MethodGet, e.URL+path, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, status, raw)
	}
	if cc := header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("GET %s Cache-Control = %q, want no-store", path, cc)
	}
	checkSchema(t, loadContract(t), raw, schema)
	return decodeAs[map[string]any](t, raw)
}

func (e *consoleAddressEnv) post(t *testing.T, path, body string) (int, http.Header, []byte) {
	t.Helper()
	return call(t, http.MethodPost, e.URL+path, "application/json", body)
}

// wait polls getConsoleAddressChange until the change has the status.
func (e *consoleAddressEnv) wait(t *testing.T, id, status string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		c := getNoStore(t, e.testEnv, "/v1/console-address/changes/"+id, "ConsoleAddressChange")
		if c["status"] == status {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("change %s is %v, want %s after 15s: %v", id, c["status"], status, c)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestConsoleAddressRead(t *testing.T) {
	e := newConsoleAddressEnv(t)
	got := getNoStore(t, e.testEnv, "/v1/console-address", "ConsoleAddress")
	if got["domain"] != "console.example.com" || got["publicUrl"] != "https://console.example.com" || got["canChange"] != true ||
		got["cannotChange"] != "" || got["agent"] != false || got["change"] != nil {
		t.Errorf("console address = %v", got)
	}
	if list, _ := got["oldDomains"].([]any); list == nil || len(list) != 0 {
		t.Errorf("oldDomains = %v, want []", got["oldDomains"])
	}
	_, header, _ := call(t, http.MethodGet, e.URL+"/v1/console-address", "", "")
	if header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", header.Get("Cache-Control"))
	}

	// An installation that can't change says why in one sentence.
	e2 := newTestEnv(t, "")
	got = getNoStore(t, e2, "/v1/console-address", "ConsoleAddress")
	if got["canChange"] != false || !strings.Contains(got["cannotChange"].(string), "no public https address") {
		t.Errorf("without a public URL: %v", got)
	}
}

func TestConsoleAddressStartRefusals(t *testing.T) {
	tests := []struct {
		name, body  string
		status      int
		code, field string
	}{
		{"not a host name", `{"domain":"a b.test"}`, http.StatusBadRequest, "invalid_field", "domain"},
		{"an IP address", `{"domain":"203.0.113.1"}`, http.StatusBadRequest, "invalid_field", "domain"},
		{"the current domain", `{"domain":"console.example.com"}`, http.StatusBadRequest, "invalid_field", "domain"},
		{"a window that isn't offered", `{"domain":"new.example.com","redirectDays":5}`, http.StatusBadRequest, "invalid_field", "redirectDays"},
		{"DNS elsewhere", `{"domain":"elsewhere.example.com"}`, http.StatusUnprocessableEntity, "domain_not_here", "domain"},
		{"no DNS at all", `{"domain":"nowhere.example.com"}`, http.StatusUnprocessableEntity, "domain_not_here", "domain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newConsoleAddressEnv(t)
			status, _, raw := e.post(t, "/v1/console-address/changes", tt.body)
			if status != tt.status {
				t.Fatalf("status %d body %s, want %d", status, raw, tt.status)
			}
			checkSchema(t, loadContract(t), raw, "Error")
			got := decodeAs[errorBody](t, raw).Error
			if got.Code != tt.code || got.Field != tt.field || got.Message == "" {
				t.Errorf("error = %+v, want %s on %s", got, tt.code, tt.field)
			}
			if _, ok, err := e.Store.LatestConsoleAddressChange(context.Background()); err != nil || ok {
				t.Errorf("a refusal recorded a change (%v, %v)", ok, err)
			}
		})
	}

	t.Run("an app's domain", func(t *testing.T) {
		e := newConsoleAddressEnv(t)
		deployOK(t, e.testEnv, "whoami", strings.Replace(whoamiBody, "whoami.localhost", "new.example.com", 1))
		status, _, raw := e.post(t, "/v1/console-address/changes", `{"domain":"new.example.com"}`)
		got := decodeAs[errorBody](t, raw).Error
		if status != http.StatusConflict || got.Code != "domain_taken" || got.Field != "domain" {
			t.Errorf("status %d error %+v, want 409 domain_taken on domain", status, got)
		}
	})

	t.Run("a Traefik job from before the feature", func(t *testing.T) {
		e := newTestEnv(t, "")
		e.Service.SetPublicURL("https://console.example.com")
		e.Nomad.SetTraefikJob("traefik:v3.7.13")
		status, _, raw := call(t, http.MethodPost, e.URL+"/v1/console-address/changes", "application/json", `{"domain":"new.example.com"}`)
		got := decodeAs[errorBody](t, raw).Error
		if status != http.StatusConflict || got.Code != "console_address_unsupported" || !strings.Contains(got.Message, "installer") {
			t.Errorf("status %d error %+v, want 409 console_address_unsupported naming the installer", status, got)
		}
	})

	t.Run("not JSON", func(t *testing.T) {
		e := newConsoleAddressEnv(t)
		status, _, _ := call(t, http.MethodPost, e.URL+"/v1/console-address/changes", "text/plain", `domain=new.example.com`)
		if status != http.StatusUnsupportedMediaType {
			t.Errorf("status %d, want 415", status)
		}
	})
}

func TestConsoleAddressChangeLifecycle(t *testing.T) {
	e := newConsoleAddressEnv(t)

	// Unknown IDs.
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/console-address/changes/nope"},
		{http.MethodDelete, "/v1/console-address/changes/nope"},
		{http.MethodPost, "/v1/console-address/changes/nope/switch"},
	} {
		status, _, raw := call(t, tt.method, e.URL+tt.path, "application/json", "")
		if status != http.StatusNotFound || decodeAs[errorBody](t, raw).Error.Code != "console_address_change_not_found" {
			t.Errorf("%s %s = %d %s, want 404 console_address_change_not_found", tt.method, tt.path, status, raw)
		}
	}

	// A start answers 202 with the change and its Location; redirectDays defaults to 30.
	status, header, raw := e.post(t, "/v1/console-address/changes", `{"domain":"New.Example.com"}`)
	if status != http.StatusAccepted {
		t.Fatalf("start = %d %s, want 202", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "ConsoleAddressChange")
	c := decodeAs[map[string]any](t, raw)
	id, _ := c["id"].(string)
	if len(id) != 32 || c["status"] != "checking" || c["domain"] != "new.example.com" || c["oldDomain"] != "console.example.com" ||
		c["redirectDays"] != float64(30) || header.Get("Location") != "/v1/console-address/changes/"+id ||
		header.Get("Cache-Control") != "no-store" {
		t.Errorf("change = %v, headers %v", c, header)
	}

	// A second start while it is open.
	status, _, raw = e.post(t, "/v1/console-address/changes", `{"domain":"new.example.com"}`)
	if got := decodeAs[errorBody](t, raw).Error; status != http.StatusConflict || got.Code != "console_address_change_open" {
		t.Errorf("second start = %d %s, want 409 console_address_change_open", status, raw)
	}

	ready := e.wait(t, id, "ready")
	if ready["readyAt"] == nil || ready["settledAt"] != nil {
		t.Errorf("ready change = %v", ready)
	}
	if read := getNoStore(t, e.testEnv, "/v1/console-address", "ConsoleAddress"); read["change"] == nil ||
		read["change"].(map[string]any)["id"] != id {
		t.Errorf("getConsoleAddress change = %v, want the open one", read["change"])
	}

	// Without an agent beside Caravana the switch is refused, naming no field, and the change stays ready.
	status, _, raw = e.post(t, "/v1/console-address/changes/"+id+"/switch", "")
	if got := decodeAs[errorBody](t, raw).Error; status != http.StatusConflict || got.Code != "no_agent" || got.Field != "" {
		t.Errorf("switch without an agent = %d %s, want 409 no_agent with no field", status, raw)
	}

	// With one it is 202 and switching; then it can't be switched again or cancelled.
	e.agent(t)
	status, _, raw = e.post(t, "/v1/console-address/changes/"+id+"/switch", "")
	if status != http.StatusAccepted {
		t.Fatalf("switch = %d %s, want 202", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "ConsoleAddressChange")
	if got := decodeAs[map[string]any](t, raw); got["status"] != "switching" || got["switchAt"] == nil {
		t.Errorf("switching change = %v", got)
	}
	status, _, raw = e.post(t, "/v1/console-address/changes/"+id+"/switch", "")
	if got := decodeAs[errorBody](t, raw).Error; status != http.StatusConflict || got.Code != "console_address_not_ready" {
		t.Errorf("second switch = %d %s, want 409 console_address_not_ready", status, raw)
	}
	status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/console-address/changes/"+id, "", "")
	if got := decodeAs[errorBody](t, raw).Error; status != http.StatusConflict || got.Code != "console_address_not_cancellable" {
		t.Errorf("cancel while switching = %d %s, want 409 console_address_not_cancellable", status, raw)
	}
}

func TestConsoleAddressCancel(t *testing.T) {
	e := newConsoleAddressEnv(t)
	status, _, raw := e.post(t, "/v1/console-address/changes", `{"domain":"new.example.com","redirectDays":0}`)
	if status != http.StatusAccepted {
		t.Fatalf("start = %d %s", status, raw)
	}
	c := decodeAs[map[string]any](t, raw)
	if c["redirectDays"] != float64(0) {
		t.Errorf("redirectDays = %v, want the 0 that was sent, not the default", c["redirectDays"])
	}
	id := c["id"].(string)
	status, header, raw := call(t, http.MethodDelete, e.URL+"/v1/console-address/changes/"+id, "", "")
	if status != http.StatusOK {
		t.Fatalf("cancel = %d %s, want 200", status, raw)
	}
	checkSchema(t, loadContract(t), raw, "ConsoleAddressChange")
	if got := decodeAs[map[string]any](t, raw); got["status"] != "cancelled" || got["settledAt"] == nil || header.Get("Cache-Control") != "no-store" {
		t.Errorf("cancelled change = %v, headers %v", got, header)
	}
	// Cancelling again: it has ended.
	status, _, raw = call(t, http.MethodDelete, e.URL+"/v1/console-address/changes/"+id, "", "")
	if got := decodeAs[errorBody](t, raw).Error; status != http.StatusConflict || got.Code != "console_address_not_cancellable" {
		t.Errorf("second cancel = %d %s, want 409", status, raw)
	}
	// A new one may start.
	if status, _, raw := e.post(t, "/v1/console-address/changes", `{"domain":"new.example.com"}`); status != http.StatusAccepted {
		t.Errorf("start after cancel = %d %s, want 202", status, raw)
	}
}

func TestConsoleAddressNeedsCredentials(t *testing.T) {
	e := newConsoleAddressEnv(t)
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/console-address"},
		{http.MethodPost, "/v1/console-address/changes"},
		{http.MethodGet, "/v1/console-address/changes/x"},
		{http.MethodDelete, "/v1/console-address/changes/x"},
		{http.MethodPost, "/v1/console-address/changes/x/switch"},
	} {
		if status, _, _ := call(t, tt.method, e.RawURL+tt.path, "application/json", "{}"); status != http.StatusUnauthorized {
			t.Errorf("%s %s without credentials = %d, want 401", tt.method, tt.path, status)
		}
	}
}
