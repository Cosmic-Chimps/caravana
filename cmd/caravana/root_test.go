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
	h := root(apiH, pages)
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
