package ui

import (
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/edge/edgetest"
)

// A DNS-elsewhere row whose banner is on the page leads with the record to change it to, the same box
// the no-record row draws, with this host's address and the name by DNSRecordName.
func TestDomainsElsewhereLeadsWithTheRecord(t *testing.T) {
	e := statesEnv(t)
	e.Svc.SetPublicURL("https://console.example.com")
	e.Svc.SetEdge(edgetest.FakeResolver{Hosts: map[string][]string{"console.example.com": {"203.0.113.1"}}}, nil)
	row := text(dmRowOf(e.pageOf(t, "/domains"), "elsewhere.example.com"))
	for _, want := range []string{"Change the record to: Type A Name elsewhere Value 203.0.113.1", "now A · 185.199.108.153", "last check 40 s ago"} {
		if !strings.Contains(row, want) {
			t.Errorf("the elsewhere row lacks %q:\n%s", want, row)
		}
	}
	none := text(dmRowOf(e.pageOf(t, "/domains"), "none.example.com"))
	if !strings.Contains(none, "Type A Name none Value 203.0.113.1") {
		t.Errorf("the no-record row's box:\n%s", none)
	}
}
