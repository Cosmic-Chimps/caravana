package ui

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The table holds what edge.State itself says, so the two can't drift.
func TestDomainStatesMatchEdge(t *testing.T) {
	for _, s := range edge.States {
		info, ok := domainStates[s]
		if !ok {
			t.Errorf("state %s has no row in the table", s)
			continue
		}
		if info.Mark != s.Mark() || info.Words != s.Words() {
			t.Errorf("state %s: table says %s %q, edge says %s %q", s, info.Mark, info.Words, s.Mark(), s.Words())
		}
	}
	if len(domainStates) != len(edge.States) {
		t.Errorf("the table has %d rows for %d states", len(domainStates), len(edge.States))
	}
}

// Every state, in the three places that name it: the list's pill and Certificate column, the Add
// panel's check lines and Settings › Certificates. They read one table, so they agree on the mark
// and on the words; this holds them to it.
func TestDomainStatesAgreeAcrossThePages(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	notAfter := now.Add(80 * 24 * time.Hour)
	renews := notAfter.Add(-30 * 24 * time.Hour)

	// Certificates: each status gives the state the table names, and the list, the check line and
	// the Certificates table all draw that state's mark; a certificate with no date says the same
	// thing in the list's column and the table's.
	certs := []struct {
		status string
		state  edge.State
	}{
		{edge.CertIssued, edge.StateLive},
		{edge.CertExpiring, edge.StateCertExpiring},
		{edge.CertPending, edge.StateCertPending},
		{edge.CertSelf, edge.StateSelfSigned},
		{edge.CertError, edge.StateUnknown},
		{edge.CertNone, edge.StateUnknown},
	}
	for _, c := range certs {
		info := stateInfo(c.state)
		if got := certState(c.status); got != c.state {
			t.Errorf("certificate %s gives state %s, want %s", c.status, got, c.state)
			continue
		}
		cert := edge.Certificate{Status: c.status, Issuer: "Let's Encrypt", NotAfter: notAfter, RenewsAt: renews}
		// The list's pill.
		if pill := domainPill(service.DomainView{State: c.state, CheckedAt: &now}); pill.Kind != info.Mark || pill.Word != info.Words {
			t.Errorf("%s: the list's pill is %s %q, the table says %s %q", c.state, pill.Kind, pill.Word, info.Mark, info.Words)
		}
		// The Certificates table's mark.
		na := notAfter
		row := certificateRowOf(now, service.CertificateView{Host: "a.example.com", Status: c.status, Issuer: "Let's Encrypt", NotAfter: &na, RenewsAt: &renews})
		if row.Mark != info.Mark {
			t.Errorf("%s: the Certificates table's mark is %s, the table says %s", c.status, row.Mark, info.Mark)
		}
		// The Add panel's certificate line (a host not yet served has none until it is).
		if c.status != edge.CertNone {
			v := &domainAddView{}
			v.applyCheck(service.DomainCheck{Host: "a.example.com", Certificate: cert, DNS: edge.DNS{Status: edge.DNSOK}})
			if v.Cert.Mark != info.Mark {
				t.Errorf("%s: the check line's mark is %s, the table says %s", c.status, v.Cert.Mark, info.Mark)
			}
		}
		// The words a certificate with no date gives are the table's, in both columns.
		if info.Cert != "" {
			if got := certCellFor(now, cert).Text; got != info.Cert {
				t.Errorf("%s: the list's column says %q, the table says %q", c.status, got, info.Cert)
			}
		}
		if info.Expiry != "" && row.Expiry.Text != info.Expiry {
			t.Errorf("%s: the Certificates table's expiry says %q, the table says %q", c.status, row.Expiry.Text, info.Expiry)
		}
	}
	// A pending certificate reads the same in the list and in the Certificates table.
	pending := edge.Certificate{Status: edge.CertPending}
	if list, table := certCellFor(now, pending).Text, certificateRowOf(now, service.CertificateView{Status: edge.CertPending}).Expiry.Text; list != table || list != "waiting for Let's Encrypt" {
		t.Errorf("pending says %q in the list and %q in the Certificates table", list, table)
	}

	// DNS: each result that isn't "points here" gives the state whose mark the check line draws.
	dns := []struct {
		status string
		state  edge.State
	}{
		{edge.DNSElsewhere, edge.StateDNSElsewhere},
		{edge.DNSNone, edge.StateDNSNone},
		{edge.DNSUnknown, edge.StateUnknown},
	}
	for _, d := range dns {
		st, ok := dnsState(d.status)
		if !ok || st != d.state {
			t.Errorf("DNS %s gives %s (%v), want %s", d.status, st, ok, d.state)
			continue
		}
		info := stateInfo(st)
		v := &domainAddView{}
		v.applyCheck(service.DomainCheck{Host: "a.example.com", DNS: edge.DNS{Status: d.status}, Certificate: edge.Certificate{Status: edge.CertNone}})
		if v.DNS.Mark != info.Mark {
			t.Errorf("DNS %s: the check line's mark is %s, the table says %s", d.status, v.DNS.Mark, info.Mark)
		}
		if pill := domainPill(service.DomainView{State: st, CheckedAt: &now}); pill.Kind != v.DNS.Mark {
			t.Errorf("DNS %s: the list's mark is %s, the check line's %s", d.status, pill.Kind, v.DNS.Mark)
		}
	}
	// "No DNS record" is ◆ with the same words in the list and the check line (it was ■ "no record yet").
	v := &domainAddView{}
	v.applyCheck(service.DomainCheck{Host: "a.example.com", DNS: edge.DNS{Status: edge.DNSNone}})
	if v.DNS.Mark != "warning" || v.DNS.Reason.String() != "no DNS record" {
		t.Errorf("the check line for no record is %s %q", v.DNS.Mark, v.DNS.Reason.String())
	}
	if pill := domainPill(service.DomainView{State: edge.StateDNSNone, CheckedAt: &now}); pill.Kind != "warning" || pill.Word != "no DNS record" {
		t.Errorf("the list's pill for no record is %s %q", pill.Kind, pill.Word)
	}
}
