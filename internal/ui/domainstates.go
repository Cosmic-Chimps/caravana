package ui

import "github.com/cosmic-chimps/caravana/internal/edge"

// One table of states (feature 023, the critique's "one fact, three vocabularies"). What a
// domain's state is called, how it is marked and what its certificate column says are written
// here once, and three places read it: the list (its pill, its Certificate column, the fixed
// sentences of its detail), the Add panel's check lines and Settings › Certificates. A DNS result
// or a certificate result is first named as the state it would give (dnsState, certState), so
// "no DNS record" is ◆ in the list, in the check line and in the count, and a certificate that is
// pending says "waiting for Let's Encrypt" wherever it is drawn.

// domainState is what the console says of one edge.State.
type domainState struct {
	// Mark is the mark: failed, warning, rolling, healthy or idle.
	Mark string
	// Words are the pill's words (a redirect's target and a stopped app's reason are the caller's).
	Words string
	// Cert is the list's Certificate column when the state has no date to give; "" when the date
	// decides (live, expiring, redirect).
	Cert string
	// Expiry is Settings › Certificates' Expiry column when there is no date; "" when it decides.
	// It says "—" for Traefik's own, whose Issuer column says it already.
	Expiry string
	// Sentence is the detail's sentence when it is the same for every domain in the state.
	Sentence string
}

// domainStates is the table. Words and Mark are edge.State's own (a test holds them together);
// the rest is the console's.
var domainStates = map[edge.State]domainState{
	edge.StateNotServed:    {Mark: "idle", Words: "not served", Cert: "—", Expiry: "—"},
	edge.StateDNSElsewhere: {Mark: "failed", Words: "DNS points elsewhere", Cert: "—", Expiry: "—"},
	edge.StateCertExpiring: {Mark: "warning", Words: "certificate expiring"},
	edge.StateCertPending: {Mark: "rolling", Words: "getting certificate", Cert: "waiting for Let's Encrypt", Expiry: "waiting for Let's Encrypt",
		Sentence: "Waiting for Let's Encrypt. DNS points here; the certificate usually arrives within a minute of the first visit."},
	edge.StateDNSNone: {Mark: "warning", Words: "no DNS record", Cert: "—", Expiry: "—"},
	edge.StateSelfSigned: {Mark: "idle", Words: "Traefik's own certificate", Cert: edge.TraefikOwn, Expiry: "—",
		Sentence: "Traefik serves this domain with its own certificate: browsers will warn. Certificates are issued when caravana is installed with Let's Encrypt."},
	edge.StateRedirect: {Mark: "idle", Words: "redirects"},
	edge.StateLive:     {Mark: "healthy", Words: "live · HTTPS"},
	edge.StateUnknown:  {Mark: "idle", Words: "couldn't check", Cert: "—", Expiry: "—"},
}

// stateInfo is the table's row for s; a state it doesn't know reads as unknown.
func stateInfo(s edge.State) domainState {
	if d, ok := domainStates[s]; ok {
		return d
	}
	return domainStates[edge.StateUnknown]
}

// certState is the state a certificate's status gives: the row the list, the check line and the
// Certificates table read for it. A certificate that wasn't probed or couldn't be read is unknown.
func certState(status string) edge.State {
	switch status {
	case edge.CertIssued:
		return edge.StateLive
	case edge.CertExpiring:
		return edge.StateCertExpiring
	case edge.CertPending:
		return edge.StateCertPending
	case edge.CertSelf:
		return edge.StateSelfSigned
	}
	return edge.StateUnknown
}

// dnsState is the state a DNS result gives; ok is false for DNS that points here, which gives
// no state of its own (the certificate's does).
func dnsState(status string) (state edge.State, ok bool) {
	switch status {
	case edge.DNSElsewhere:
		return edge.StateDNSElsewhere, true
	case edge.DNSNone:
		return edge.StateDNSNone, true
	case edge.DNSOK:
		return "", false
	}
	return edge.StateUnknown, true
}
