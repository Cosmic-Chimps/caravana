package certs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckDirectory(t *testing.T) {
	const full = `{"newNonce":"https://x/nonce","newAccount":"https://x/acct","newOrder":"https://x/order","meta":{"externalAccountRequired":%s}}`
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    Directory
		err     string // a part of the message; "" for success
	}{
		{"a directory", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Replace(full, "%s", "false", 1)))
		}, Directory{}, ""},
		{"a directory that needs EAB", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Replace(full, "%s", "true", 1)))
		}, Directory{ExternalAccountRequired: true}, ""},
		{"no meta", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"newNonce":"a","newAccount":"b","newOrder":"c"}`))
		}, Directory{}, ""},
		{"not found", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, Directory{}, "answered 404"},
		{"server error", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, Directory{}, "answered 503"},
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}, Directory{}, "redirect (302)"},
		{"no newNonce", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"newAccount":"b","newOrder":"c"}`))
		}, Directory{}, "isn't an ACME directory: no newNonce"},
		{"no newAccount", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"newNonce":"a","newOrder":"c"}`))
		}, Directory{}, "isn't an ACME directory: no newAccount"},
		{"empty newOrder", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"newNonce":"a","newAccount":"b","newOrder":""}`))
		}, Directory{}, "no newOrder"},
		{"a number where a string belongs", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"newNonce":1,"newAccount":"b","newOrder":"c"}`))
		}, Directory{}, "isn't an ACME directory"},
		{"HTML", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<html>SECRET-BODY-SENTINEL</html>`))
		}, Directory{}, "isn't an ACME directory"},
		{"a JSON array", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) }, Directory{}, "isn't an ACME directory"},
		{"too large", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"newNonce":"a","newAccount":"b","newOrder":"c","pad":"` + strings.Repeat("x", 70<<10) + `"}`))
		}, Directory{}, "larger than 64 KiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			got, err := CheckDirectory(context.Background(), srv.Client(), srv.URL+"/directory")
			if tt.err == "" {
				if err != nil || got != tt.want {
					t.Fatalf("CheckDirectory = %+v, %v; want %+v", got, err, tt.want)
				}
				return
			}
			wantError(t, err, "server", tt.err)
			if strings.Contains(err.Error(), "SECRET-BODY-SENTINEL") {
				t.Errorf("the error quotes the body: %v", err)
			}
		})
	}
}

func TestCheckDirectoryNeverFollowsARedirect(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.URL.Path == "/directory" {
			http.Redirect(w, r, "/real", http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write([]byte(`{"newNonce":"a","newAccount":"b","newOrder":"c"}`))
	}))
	defer srv.Close()
	// A client that would follow redirects, as http.DefaultClient does.
	_, err := CheckDirectory(context.Background(), &http.Client{}, srv.URL+"/directory")
	wantError(t, err, "server", "redirect (301)")
	if len(asked) != 1 {
		t.Errorf("the server was asked for %v, want only /directory", asked)
	}
}

func TestCheckDirectoryOneGetWithTheCallersClient(t *testing.T) {
	var method, accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, accept = r.Method, r.Header.Get("Accept")
		_, _ = w.Write([]byte(`{"newNonce":"a","newAccount":"b","newOrder":"c"}`))
	}))
	defer srv.Close()
	client := srv.Client()
	client.Timeout = 3 * time.Second
	if _, err := CheckDirectory(context.Background(), client, srv.URL); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || accept != "application/json" {
		t.Errorf("request = %s Accept %q", method, accept)
	}
	if client.CheckRedirect != nil {
		t.Error("the caller's client was changed")
	}
}

func TestCheckDirectoryUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there now
	_, err := CheckDirectory(context.Background(), &http.Client{Timeout: time.Second}, url)
	wantError(t, err, "server", "couldn't be reached")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = CheckDirectory(ctx, nil, url)
	wantError(t, err, "server", "couldn't be reached")

	_, err = CheckDirectory(context.Background(), nil, "://bad")
	wantError(t, err, "server", "usable address")
}
