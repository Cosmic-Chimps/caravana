package ui

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/clusterca"
	"github.com/cosmic-chimps/caravana/internal/deploy/deploytest"
	"github.com/cosmic-chimps/caravana/internal/dns/dnstest"
	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
	"github.com/cosmic-chimps/caravana/internal/mesh"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 051: Operator › Console address. The service's own tests cover the checks; these cover what the
// page says, where a refusal lands, the panel's polling and that every action is a plain form.

type addressEnv struct {
	*consoleEnv
	provider *dnstest.Provider
}

func newAddressEnv(t *testing.T) *addressEnv {
	t.Helper()
	here := []string{"203.0.113.1"}
	ca := edgetest.NewCA(t, "Test CA")
	chains := map[string][]*x509.Certificate{}
	for _, h := range []string{"console.example.com", "new.example.com"} {
		chains[h] = []*x509.Certificate{ca.Issue(t, []string{h}, time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour))}
	}
	e := &addressEnv{provider: dnstest.NewProvider()}
	e.provider.AddZone("example.com")
	e.consoleEnv = newConsoleEnvWith(t, func(s *service.Service, n *deploytest.Nomad) {
		s.SetPublicURL("https://console.example.com")
		s.SetConsoleAddressPassGap(time.Hour)
		s.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{
			"console.example.com":   here,
			"new.example.com":       here,
			"elsewhere.example.com": {"198.51.100.7"},
		}}, &edgetest.FakeDialer{Chains: chains, RootCAs: ca.Pool(), Default: []*x509.Certificate{edgetest.TraefikDefault(t)}})
		s.SetEdgeAddress("127.0.0.1:8443")
		s.SetCertResolver("letsencrypt")
		s.SetConsoleRouteProbe(func(context.Context, string, string) error { return nil })
		s.SetDNSProviders(e.provider.Factory())
		n.SetTraefikConsoleRoute("traefik:v3.7.13")
	})
	return e
}

// agent makes the first server report that its agent runs the console.
func (e *addressEnv) agent(t *testing.T) {
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
	token, err := e.Svc.AdoptFirst(ctx, "hel-1", pub, "203.0.113.4")
	if err != nil {
		t.Fatal(err)
	}
	e.Nomad.AddNode("node-1", "hel-1", "ready", "10.99.0.1")
	r := service.NodeReport{Agent: "0.18.0", Nomad: "2.0.7", Docker: "27.5.1", DockerRunning: true, DiskFreeMB: 160000,
		DiskTotalMB: 190000, MemoryMB: 3916, Kernel: "6.8.0", Role: "client", Reach: []service.NodeReach{}, RunsConsole: true}
	if _, err := e.Svc.NodeSync(ctx, token, r); err != nil {
		t.Fatalf("NodeSync: %v", err)
	}
}

// readyChange starts a change to new.example.com and returns its ID once it is ready.
func (e *addressEnv) readyChange(t *testing.T) string {
	t.Helper()
	c, err := e.Svc.StartConsoleAddressChange(context.Background(), "new.example.com", 30, false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		got, err := e.Svc.ConsoleAddressChange(context.Background(), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == store.ConsoleReady {
			return c.ID
		}
		if time.Now().After(deadline) {
			t.Fatalf("change is %q, want ready: %+v", got.Status, got.Checks)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestConsoleAddressPageFirstOpen(t *testing.T) {
	e := newAddressEnv(t)
	resp, page := e.get(t, "/ops/console-address")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	got := text(page)
	for _, want := range []string{"Console address", "https://console.example.com", "Keep the old address for", "Check",
		"You sign in again at the new address.", "Notification links and joined servers follow by themselves.",
		"Anything that trusts the issuer"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	// The form is a plain post, with the four windows and 30 days chosen.
	f := formWith(t, page, "/ops/console-address/changes")
	if f.Method != "post" {
		t.Errorf("form = %+v, want a post", f)
	}
	for _, want := range []string{`<option value="30" selected>30 days</option>`, `name="domain"`, `autocomplete="off"`, `<option value="0">Don&#39;t keep it</option>`, `<option value="7">7 days</option>`, `<option value="90">90 days</option>`} {
		if !strings.Contains(page, want) {
			t.Errorf("the form lacks %s", want)
		}
	}
	if strings.Contains(page, `hx-get="/parts/ops/console-address`) {
		t.Error("the panel polls with no change open")
	}
	// The sidebar's entry, after Updates, marked current.
	if !strings.Contains(page, `<a href="/ops/console-address" class="cv-nav-item"`) {
		t.Error("no sidebar entry")
	}
}

func TestConsoleAddressCannotChange(t *testing.T) {
	e := newConsoleEnvWith(t, nil) // no public URL
	resp, page := e.get(t, "/ops/console-address")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	got := text(page)
	if !strings.Contains(got, "no public https address") || strings.Contains(page, `action="/ops/console-address/changes"`) {
		t.Errorf("an installation that can't change shows the sentence and no form:\n%s", got)
	}
}

func TestConsoleAddressStartRefusalsLandAtTheField(t *testing.T) {
	tests := []struct {
		name   string
		form   url.Values
		status int
		want   string
	}{
		{"not a host name", url.Values{"domain": {"a b.test"}}, http.StatusBadRequest, "is not a host name"},
		{"the current domain", url.Values{"domain": {"console.example.com"}}, http.StatusBadRequest, "is the console&#39;s address already"},
		{"a window that isn't offered", url.Values{"domain": {"new.example.com"}, "redirectDays": {"5"}}, http.StatusBadRequest, "Must be 0, 7, 30 or 90 days"},
		{"DNS elsewhere", url.Values{"domain": {"elsewhere.example.com"}}, http.StatusUnprocessableEntity, "not at this server"},
		{"no DNS", url.Values{"domain": {"nowhere.test"}}, http.StatusUnprocessableEntity, "has no DNS record"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newAddressEnv(t)
			resp, page := e.postForm(t, "/ops/console-address/changes", tt.form)
			if resp.StatusCode != tt.status {
				t.Fatalf("POST = %d, want %d:\n%s", resp.StatusCode, tt.status, text(page))
			}
			if !strings.Contains(page, tt.want) {
				t.Errorf("the page lacks %q:\n%s", tt.want, text(page))
			}
			// The typed domain is kept in the field.
			if d := tt.form.Get("domain"); !strings.Contains(page, `value="`+d+`"`) {
				t.Errorf("the field lost what was typed (%q)", d)
			}
			if _, ok, _ := e.Store.LatestConsoleAddressChange(context.Background()); ok {
				t.Error("a refusal recorded a change")
			}
		})
	}
}

func TestConsoleAddressOffersTheRecordInAZone(t *testing.T) {
	e := newAddressEnv(t)
	token := "tok"
	if _, _, err := e.Svc.SaveDNSZone(context.Background(), service.DNSZoneInput{Zone: "example.com", Provider: "cloudflare", Token: &token}); err != nil {
		t.Fatal(err)
	}
	resp, page := e.postForm(t, "/ops/console-address/changes", url.Values{"domain": {"fresh.example.com"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST = %d, want 422:\n%s", resp.StatusCode, text(page))
	}
	if !strings.Contains(page, "Create the record in example.com and check") || !strings.Contains(page, `name="createDNSRecord"`) {
		t.Errorf("no button to create the record:\n%s", text(page))
	}
}

func TestConsoleAddressChecksThenSwitchesThenCancels(t *testing.T) {
	e := newAddressEnv(t)
	resp, _ := e.postForm(t, "/ops/console-address/changes", url.Values{"domain": {"New.Example.com"}, "redirectDays": {"7"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ops/console-address" {
		t.Fatalf("start = %d %s, want 303 to the page", resp.StatusCode, resp.Header.Get("Location"))
	}
	c, ok, err := e.Store.OpenConsoleAddressChange(context.Background())
	if err != nil || !ok || c.RedirectDays != 7 || c.Domain != "new.example.com" {
		t.Fatalf("change = %+v, %v, %v", c, ok, err)
	}
	// While it is checking (or just ready), the page draws the panel; a part answers for it.
	deadline := time.Now().Add(15 * time.Second)
	var page string
	for {
		_, page = e.get(t, "/ops/console-address")
		if strings.Contains(page, "All checks passed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never ready:\n%s", text(page))
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := text(page)
	for _, want := range []string{"Ready to switch", "Host name", "DNS", "Traefik route", "Certificate", "Switch to new.example.com", "Cancel"} {
		if !strings.Contains(got, want) {
			t.Errorf("the ready page lacks %q:\n%s", want, got)
		}
	}
	// No agent: the switch is refused at the page with its reason and 409, and the change stays ready.
	f := formWith(t, page, "/ops/console-address/changes/"+c.ID+"/switch")
	resp, body := e.submit(t, f, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "No server agent has reported beside Caravana") {
		t.Fatalf("switch without an agent = %d:\n%s", resp.StatusCode, text(body))
	}
	// A settled change answers the part with 286, which stops htmx's polling.
	resp, part := e.get(t, "/parts/ops/console-address?id="+c.ID)
	if resp.StatusCode != 286 || !strings.Contains(text(part), "Ready to switch") {
		t.Errorf("part of a ready change = %d %s, want 286", resp.StatusCode, text(part))
	}

	// With an agent it is 303 and then switching: the panel polls and says where to sign in.
	e.agent(t)
	resp, _ = e.submit(t, f, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("switch = %d, want 303", resp.StatusCode)
	}
	_, page = e.get(t, "/ops/console-address")
	got = text(page)
	for _, want := range []string{"Switching", "Waiting for the server agent (up to a minute). Caravana restarts at https://new.example.com/; sign in there."} {
		if !strings.Contains(got, want) {
			t.Errorf("the switching page lacks %q:\n%s", want, got)
		}
	}
	for _, want := range []string{`hx-get="/parts/ops/console-address?id=` + c.ID + `"`, `hx-trigger="every 3s"`, `<meta http-equiv="refresh" content="5">`, `href="https://new.example.com/"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the switching page lacks %s", want)
		}
	}
	if strings.Contains(page, `action="/ops/console-address/changes"`) {
		t.Error("the check form is drawn while a change is open")
	}
	resp, part = e.get(t, "/parts/ops/console-address?id="+c.ID)
	if resp.StatusCode != http.StatusOK || !strings.Contains(text(part), "Waiting for the server agent") {
		t.Errorf("part of a switching change = %d: it keeps polling (200)", resp.StatusCode)
	}
	// It can't be cancelled any more.
	resp, body = e.postForm(t, "/ops/console-address/changes/"+c.ID+"/cancel", nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(text(body), "can't be cancelled any more") {
		t.Errorf("cancel while switching = %d:\n%s", resp.StatusCode, text(body))
	}
}

func TestConsoleAddressCancel(t *testing.T) {
	e := newAddressEnv(t)
	id := e.readyChange(t)
	resp, _ := e.postForm(t, "/ops/console-address/changes/"+id+"/cancel", nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/ops/console-address" {
		t.Fatalf("cancel = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	_, page := e.get(t, "/ops/console-address")
	got := text(page)
	if !strings.Contains(got, "Cancelled") || !strings.Contains(got, "The change was cancelled") || !strings.Contains(page, `action="/ops/console-address/changes"`) {
		t.Errorf("after a cancel the page says so and offers the form again:\n%s", got)
	}
	// Unknown IDs.
	resp, body := e.postForm(t, "/ops/console-address/changes/nope/switch", nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(text(body), "No such change of the console") {
		t.Errorf("switch of an unknown change = %d:\n%s", resp.StatusCode, text(body))
	}
	resp, part := e.get(t, "/parts/ops/console-address?id=nope")
	if resp.StatusCode != 286 || !strings.Contains(text(part), "This change is gone.") {
		t.Errorf("part of an unknown change = %d %s", resp.StatusCode, text(part))
	}
}

func TestConsoleAddressSettledSentences(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	base := store.ConsoleAddressChange{ID: strings.Repeat("a", 32), Domain: "new.example.com", OldDomain: "console.example.com", RedirectDays: 30}
	tests := []struct {
		status string
		mutate func(c *store.ConsoleAddressChange)
		want   string
	}{
		{store.ConsoleApplied, func(c *store.ConsoleAddressChange) { c.RedirectUntil = now.Add(30 * 24 * time.Hour) }, "redirects its pages here, and still answers the API, until"},
		{store.ConsoleApplied, func(c *store.ConsoleAddressChange) { c.RedirectDays = 0 }, "The old address is no longer served."},
		{store.ConsoleFailed, nil, "put the old address back"},
		{store.ConsoleRefused, nil, "refused the change"},
		{store.ConsoleLost, nil, "journalctl -u caravana-node"},
		{store.ConsoleCheckFailed, nil, "out of Traefik's route"},
	}
	for _, tt := range tests {
		c := base
		c.Status = tt.status
		if tt.mutate != nil {
			tt.mutate(&c)
		}
		view := consoleChangeOf(now, c)
		var words string
		for _, p := range view.Summary {
			words += p.Text
		}
		if !strings.Contains(words, tt.want) {
			t.Errorf("%s: summary %q lacks %q", tt.status, words, tt.want)
		}
		if view.Open {
			t.Errorf("%s: a settled change is polled", tt.status)
		}
	}
}

func TestConsoleAddressDependents(t *testing.T) {
	e := newAddressEnv(t)
	if _, err := e.Auth.CreateScopedAPIToken(context.Background(), "ci", nil, &service.TokenScope{Project: "default", App: "web"}); err != nil {
		t.Fatal(err)
	}
	_, page := e.get(t, "/ops/console-address")
	got := text(page)
	if !strings.Contains(got, "1 deploy token. Their senders (CI's CARAVANA_URL, Git hosts' webhook URLs) still hold console.example.com: update them before the old address stops answering.") {
		t.Errorf("the dependents lack the token sentence:\n%s", got)
	}
}
