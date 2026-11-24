package main

// The console-address step of the lab test (feature 051, tasks.md T018): the lab's Traefik has no
// console route (its job doesn't read the variable nomad/jobs/traefik/traefik, and the lab may have no
// public https address either), so the console says it can't change the address, in words, and the API
// refuses a change with console_address_unsupported. What a change does on a real host (DNS, route,
// certificate, the agent, the redirect) is deploy/dev/install-test.sh's check_console_address_host: the
// lab has neither the agent nor a console behind Traefik. It is in its own file only because
// console_lab_test.go is long; runConsole lists it after console-servers.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// consoleAddress: through the API and the console's page, against the lab's real Nomad.
//
//  1. GET /v1/console-address answers 200: canChange false with a sentence, no open change, no old
//     domain, and the dependents' counts.
//  2. GET /ops/console-address draws that same sentence and no form to change the address.
//  3. POST /v1/console-address/changes is 409 console_address_unsupported with the same sentence,
//     and nothing is recorded: the read still shows no change.
//  4. A change that doesn't exist is 404 console_address_change_not_found, and a switch of it too.
func (c *console) consoleAddress(t *testing.T) {
	var view struct {
		Domain       string `json:"domain"`
		CanChange    bool   `json:"canChange"`
		CannotChange string `json:"cannotChange"`
		OldDomains   []any  `json:"oldDomains"`
		Change       any    `json:"change"`
		Dependents   *struct {
			Passkeys int `json:"passkeys"`
		} `json:"dependents"`
	}
	c.g.getJSON(t, "/v1/console-address", &view)
	if view.CanChange || view.CannotChange == "" {
		t.Fatalf("GET /v1/console-address = %+v, want canChange false with a sentence", view)
	}
	if len(view.OldDomains) != 0 || view.Change != nil || view.Dependents == nil {
		t.Errorf("GET /v1/console-address = %+v, want no old domain, no change and the dependents", view)
	}

	t.Logf("the console says it can't change the address: %s", view.CannotChange)

	resp, page := c.get(t, "/ops/console-address")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ops/console-address = %d:\n%s", resp.StatusCode, htmlText(page))
	}
	if !strings.Contains(htmlText(page), view.CannotChange) {
		t.Errorf("the page lacks the API's sentence %q:\n%s", view.CannotChange, htmlText(page))
	}
	if strings.Contains(page, `action="/ops/console-address/changes"`) {
		t.Error("an installation that can't change its address shows the form")
	}

	body := `{"domain":"console2.lab.test","redirectDays":30}`
	refused := c.g.api(t, http.MethodPost, "/v1/console-address/changes", "application/json", body)
	raw, _ := io.ReadAll(refused.Body)
	refused.Body.Close()
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	if refused.StatusCode != http.StatusConflict || e.Error.Code != "console_address_unsupported" || e.Error.Message != view.CannotChange {
		t.Errorf("POST /v1/console-address/changes = %d %s, want 409 console_address_unsupported with %q", refused.StatusCode, raw, view.CannotChange)
	}
	var after struct {
		Change any `json:"change"`
	}
	c.g.getJSON(t, "/v1/console-address", &after)
	if after.Change != nil {
		t.Errorf("a refused change was recorded: %v", after.Change)
	}

	const missing = "0123456789abcdef0123456789abcdef"
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/v1/console-address/changes/" + missing},
		{http.MethodPost, "/v1/console-address/changes/" + missing + "/switch"},
	} {
		resp := c.g.api(t, r.method, r.path, "", "")
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(raw), "console_address_change_not_found") {
			t.Errorf("%s %s = %d %s, want 404 console_address_change_not_found", r.method, r.path, resp.StatusCode, raw)
		}
	}
}
