package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	discoveryConfig = `{"issuer":"https://console.example.com","jwks_uri":"https://console.example.com/.well-known/jwks.json"}`
	discoveryJWKS   = `{"keys":[{"kty":"RSA","alg":"RS256","use":"sig","kid":"k1","n":"abc","e":"AQAB"}]}`
	sentinelNomad   = "sentinel-nomad-body-0038"
)

var discoveryPaths = []string{"/.well-known/openid-configuration", "/.well-known/jwks.json"}

func TestDiscoveryWithoutAnIssuerIs404(t *testing.T) {
	e := newTestEnv(t, "")
	e.Nomad.SetDiscovery(discoveryPaths[0], 200, []byte(discoveryConfig))
	for _, p := range discoveryPaths {
		// No credentials at all, and with a public URL that isn't https: both are no issuer.
		for _, url := range []string{"", "http://console.example.com", "https://console.example.com/app"} {
			e.Service.SetPublicURL(url)
			status, _, raw := call(t, http.MethodGet, e.RawURL+p, "", "")
			wantError(t, p+" with public URL "+url, status, raw, http.StatusNotFound, "issuer_not_set", "")
		}
	}
	if n := e.Nomad.DiscoveryRequests(discoveryPaths[0]); n != 0 {
		t.Errorf("Nomad was asked %d times for a document without an issuer", n)
	}
}

func TestDiscoveryPassesNomadsDocumentsOn(t *testing.T) {
	e := newTestEnv(t, "a-nomad-token")
	e.Service.SetPublicURL("https://console.example.com")
	e.Nomad.SetDiscovery(discoveryPaths[0], 200, []byte(discoveryConfig))
	e.Nomad.SetDiscovery(discoveryPaths[1], 200, []byte(discoveryJWKS))
	for i, want := range []string{discoveryConfig, discoveryJWKS} {
		status, hdr, raw := call(t, http.MethodGet, e.RawURL+discoveryPaths[i], "", "")
		if status != http.StatusOK || string(raw) != want {
			t.Errorf("%s = %d %s, want 200 %s", discoveryPaths[i], status, raw, want)
		}
		if ct := hdr.Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s Content-Type = %q", discoveryPaths[i], ct)
		}
		if cc := hdr.Get("Cache-Control"); cc != "public, max-age=60" {
			t.Errorf("%s Cache-Control = %q", discoveryPaths[i], cc)
		}
	}
	// A wrong method is the API's JSON 405, and an unknown path under /.well-known its JSON 404.
	if status, _, raw := call(t, http.MethodPost, e.RawURL+discoveryPaths[0], "application/json", `{}`); status != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d %s", status, raw)
	}
	if status, _, raw := call(t, http.MethodGet, e.RawURL+"/.well-known/other", "", ""); status != http.StatusNotFound {
		t.Errorf("an unknown /.well-known path = %d %s", status, raw)
	}
}

func TestDiscoveryFailuresAre502AndNeverQuoteNomad(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"nomad refuses":     {http.StatusForbidden, sentinelNomad},
		"nomad errs":        {http.StatusInternalServerError, sentinelNomad},
		"nomad has no data": {http.StatusNotFound, sentinelNomad},
		"not JSON":          {http.StatusOK, "<html>" + sentinelNomad + "</html>"},
		"too large":         {http.StatusOK, `{"x":"` + strings.Repeat("a", 64<<10) + sentinelNomad + `"}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newTestEnv(t, "")
			e.Service.SetPublicURL("https://console.example.com")
			for _, p := range discoveryPaths {
				e.Nomad.SetDiscovery(p, c.status, []byte(c.body))
				status, _, raw := call(t, http.MethodGet, e.RawURL+p, "", "")
				wantError(t, p, status, raw, http.StatusBadGateway, "nomad_unavailable", "")
				if strings.Contains(string(raw), sentinelNomad) {
					t.Errorf("%s: the answer quotes Nomad's body: %s", p, raw)
				}
			}
			if strings.Contains(e.Logs.String(), sentinelNomad) {
				t.Errorf("the log quotes Nomad's body")
			}
		})
	}
	t.Run("nomad is down", func(t *testing.T) {
		e := newTestEnv(t, "")
		e.Service.SetPublicURL("https://console.example.com")
		e.Nomad.Down()
		status, _, raw := call(t, http.MethodGet, e.RawURL+discoveryPaths[1], "", "")
		wantError(t, "jwks with Nomad down", status, raw, http.StatusBadGateway, "nomad_unavailable", "")
	})
}

// The documents are on the main listener only.
func TestDiscoveryIsNotOnTheWebhookListener(t *testing.T) {
	e := newTestEnv(t, "")
	e.Service.SetPublicURL("https://console.example.com")
	for _, p := range discoveryPaths {
		e.Nomad.SetDiscovery(p, 200, []byte(discoveryConfig))
	}
	hook := httptest.NewServer(NewWebhook(e.Service, e.log))
	t.Cleanup(hook.Close)
	for _, p := range discoveryPaths {
		if status, _, raw := call(t, http.MethodGet, hook.URL+p, "", ""); status != http.StatusNotFound {
			t.Errorf("GET %s on the webhook listener = %d %s, want 404", p, status, raw)
		}
	}
	if n := e.Nomad.DiscoveryRequests(discoveryPaths[0]); n != 0 {
		t.Errorf("the webhook listener read Nomad %d times", n)
	}
}
