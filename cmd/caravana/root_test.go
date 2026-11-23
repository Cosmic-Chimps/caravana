package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// /v1 and the discovery documents under /.well-known go to the API, everything else to the pages.
func TestRootRoutes(t *testing.T) {
	apiH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("api")) })
	pages := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("pages")) })
	h := root(apiH, pages, nil)
	for path, want := range map[string]string{
		"/v1/health":                        "api",
		"/v1":                               "api",
		"/.well-known/openid-configuration": "api",
		"/.well-known/jwks.json":            "api",
		"/.well-known/anything":             "api",
		"//.well-known/jwks.json":           "api",
		"/":                                 "pages",
		"/apps":                             "pages",
		"/well-known/jwks.json":             "pages",
		"/.well-knownish":                   "pages",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Body.String(); got != want {
			t.Errorf("GET %s went to %s, want %s", path, got, want)
		}
	}
}

// fakeOldConsole redirects the hosts it lists to base, like the service does for the old console domains.
type fakeOldConsole map[string]bool

func (f fakeOldConsole) ConsoleRedirect(host, requestURI string) (string, bool) {
	return "https://new.example.com" + requestURI, f[host]
}

// The old console domain keeps serving the API and the discovery documents, and its pages redirect to
// the same path and query on the new address with 308, which keeps the method (a webhook, a sign-in).
func TestRootRedirectsOldConsoleDomain(t *testing.T) {
	apiH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("api")) })
	pages := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("pages")) })
	h := root(apiH, pages, fakeOldConsole{"old.example.com": true})

	tests := []struct {
		name, method, host, uri string
		wantStatus              int
		wantBody, wantLocation  string
	}{
		{"a page redirects", "GET", "old.example.com", "/apps", 308, "", "https://new.example.com/apps"},
		{"the path and query are kept", "GET", "old.example.com", "/logs?app=web&level=error", 308, "", "https://new.example.com/logs?app=web&level=error"},
		{"the root redirects", "GET", "old.example.com", "/", 308, "", "https://new.example.com/"},
		{"a post keeps its method with 308", "POST", "old.example.com", "/login", 308, "", "https://new.example.com/login"},
		{"the API is served", "POST", "old.example.com", "/v1/webhooks/web?token=x", 200, "api", ""},
		{"/v1 itself is served", "GET", "old.example.com", "/v1", 200, "api", ""},
		{"the cleaned path decides", "GET", "old.example.com", "//v1/health", 200, "api", ""},
		{"the discovery documents are served", "GET", "old.example.com", "/.well-known/jwks.json", 200, "api", ""},
		{"/.well-knownish is a page", "GET", "old.example.com", "/.well-knownish", 308, "", "https://new.example.com/.well-knownish"},
		{"the new host is served as before", "GET", "new.example.com", "/apps", 200, "pages", ""},
		{"another host is served as before", "GET", "127.0.0.1:7070", "/apps", 200, "pages", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.uri, nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus || rec.Body.String() != tt.wantBody && tt.wantStatus == 200 || rec.Header().Get("Location") != tt.wantLocation {
				t.Errorf("%s %s%s = %d %q Location %q; want %d %q Location %q", tt.method, tt.host, tt.uri,
					rec.Code, rec.Body.String(), rec.Header().Get("Location"), tt.wantStatus, tt.wantBody, tt.wantLocation)
			}
		})
	}
}
