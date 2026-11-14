package ui

import (
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Spec 038: Configure shows each identity read-only, saving its form keeps
// them, and the page never has the token (it can't: nothing here reads it).
func TestConfigureShowsIdentities(t *testing.T) {
	e := newConsoleEnv(t)
	a := duoApp()
	a.Services[0].Identities = []app.Identity{
		{Name: "bella", Audience: []string{"bella-spiffe/env-1"}},
		{Name: "billing", Audience: []string{"billing", "audit"}, TTLSeconds: 120},
		{Name: "pair", Audience: []string{"x"}, TTLSeconds: 5400},
	}
	e.deploy(t, a)

	_, body := e.get(t, "/apps/duo/configure")
	got := text(body)
	for _, want := range []string{
		"Identities",
		"Identity bella Audience bella-spiffe/env-1 Lasts 1 hour. Read from /secrets/nomad_bella.jwt. For Bella Baxter.",
		"Identity billing Audience billing audit Lasts 2 minutes. Read from /secrets/nomad_billing.jwt.",
		"Lasts 1 hour 30 minutes.",
		"Set through the API, the Compose overlay or Aspire.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Identity billing Audience billing audit Lasts 2 minutes. Read from /secrets/nomad_billing.jwt. For Bella") {
		t.Error("a non-Bella identity says it is for Bella")
	}
	// The worker has none: its panel has no Identities block, and the block is once per service.
	if strings.Count(got, "Set through the API, the Compose overlay or Aspire.") != 1 {
		t.Errorf("the identities note is drawn %d times, want once (one service has identities)",
			strings.Count(got, "Set through the API, the Compose overlay or Aspire."))
	}

	// A save of the form keeps them.
	f := e.configureForm(t, "duo")
	resp, out := e.submit(t, f, url.Values{"svc.web.memory": {"192"}})
	if resp.StatusCode != 303 {
		t.Fatalf("POST = %d:\n%s", resp.StatusCode, text(out))
	}
	if got := e.storedApp(t, "duo").Services[0].Identities; !reflect.DeepEqual(got, a.Services[0].Identities) {
		t.Errorf("identities after a save = %+v, want %+v", got, a.Services[0].Identities)
	}
}

// An app edited through its Compose file shows them in the service's panel.
func TestConfigureKeptFileShowsIdentities(t *testing.T) {
	e, _ := deployShop(t, identityShopRequest())
	_, body := e.get(t, "/apps/shop/configure")
	got := text(body)
	for _, want := range []string{
		"Identity bella Audience bella-spiffe/env-1 Lasts 1 hour. Read from /secrets/nomad_bella.jwt. For Bella Baxter.",
		"Identity billing Audience billing audit Lasts 2 minutes.",
		"Set through the API, the Compose overlay or Aspire.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q:\n%s", want, got)
		}
	}
}

func TestDurationWords(t *testing.T) {
	for seconds, want := range map[int]string{
		60: "1 minute", 90: "1 minute 30 seconds", 120: "2 minutes", 3600: "1 hour", 5400: "1 hour 30 minutes",
		7200: "2 hours", 86400: "24 hours", 59: "59 seconds", 3661: "1 hour 1 minute 1 second",
	} {
		if got := durationWords(seconds); got != want {
			t.Errorf("durationWords(%d) = %q, want %q", seconds, got, want)
		}
	}
}
