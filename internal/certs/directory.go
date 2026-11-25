package certs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// maxDirectory is the most of an ACME directory's answer CheckDirectory reads.
const maxDirectory = 64 << 10

// Directory is what Caravana needs to know of an ACME directory.
type Directory struct {
	// ExternalAccountRequired says the issuer wants External Account Binding (meta.externalAccountRequired).
	ExternalAccountRequired bool
}

// CheckDirectory reads the ACME directory at url with one GET through client (the caller's, so
// its timeout, proxy and roots apply) and checks it is one: status 200, at most 64 KiB of a JSON
// object with newNonce, newAccount and newOrder. A redirect is refused, never followed. It never
// registers an account: an EAB key can be single-use.
//
// A refusal is an *Error on field "server" in plain words; the answer's body is never quoted.
func CheckDirectory(ctx context.Context, client *http.Client, address string) (Directory, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return Directory{}, fieldError("server", "isn't a usable address")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "caravana")

	// A copy of the client that stops at the first redirect: the directory must answer where it was asked.
	c := http.Client{}
	if client != nil {
		c = *client
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := c.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return Directory{}, fieldError("server", "the directory couldn't be reached: "+err.Error())
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return Directory{}, fieldError("server", fmt.Sprintf("the directory answered with a redirect (%d): give the address it redirects to", resp.StatusCode))
	case resp.StatusCode != http.StatusOK:
		return Directory{}, fieldError("server", fmt.Sprintf("the directory answered %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDirectory+1))
	if err != nil {
		return Directory{}, fieldError("server", "the directory's answer couldn't be read")
	}
	if len(body) > maxDirectory {
		return Directory{}, fieldError("server", "the directory's answer is larger than 64 KiB")
	}
	var doc struct {
		NewNonce   string `json:"newNonce"`
		NewAccount string `json:"newAccount"`
		NewOrder   string `json:"newOrder"`
		Meta       struct {
			ExternalAccountRequired bool `json:"externalAccountRequired"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return Directory{}, fieldError("server", "the address isn't an ACME directory: the answer isn't the expected JSON")
	}
	for _, f := range []struct{ name, value string }{
		{"newNonce", doc.NewNonce}, {"newAccount", doc.NewAccount}, {"newOrder", doc.NewOrder},
	} {
		if f.value == "" {
			return Directory{}, fieldError("server", "the address isn't an ACME directory: no "+f.name)
		}
	}
	return Directory{ExternalAccountRequired: doc.Meta.ExternalAccountRequired}, nil
}
