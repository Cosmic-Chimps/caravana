package edge

// State is what a domain's row says, one of nine words. The values are stored
// in domain_checks.state, so they never change.
type State string

const (
	StateNotServed    State = "not_served"
	StateDNSElsewhere State = "dns_elsewhere"
	StateCertExpiring State = "cert_expiring"
	StateCertPending  State = "cert_pending"
	StateDNSNone      State = "dns_none"
	StateSelfSigned   State = "self_signed"
	StateRedirect     State = "redirect"
	StateLive         State = "live"
	StateUnknown      State = "unknown"
)

// States lists every State.
var States = []State{
	StateNotServed, StateDNSElsewhere, StateCertExpiring, StateCertPending, StateDNSNone,
	StateSelfSigned, StateRedirect, StateLive, StateUnknown,
}

// StateOf derives the state from the facts: the first rule that applies.
//
//  1. not served (the app is stopped, or has no healthy deployment): not_served;
//  2. DNS points elsewhere: dns_elsewhere;
//  3. the certificate is expiring: cert_expiring;
//  4. the certificate is pending: cert_pending;
//  5. DNS has no record: dns_none;
//  6. Traefik's own certificate with no resolver: self_signed;
//  7. the host only redirects to its pair: redirect;
//  8. DNS ok and the certificate issued: live;
//  9. anything else (a check that couldn't be made): unknown.
func StateOf(f Facts, served, redirecting bool) State {
	switch {
	case !served:
		return StateNotServed
	case f.DNS.Status == DNSElsewhere:
		return StateDNSElsewhere
	case f.Certificate.Status == CertExpiring:
		return StateCertExpiring
	case f.Certificate.Status == CertPending:
		return StateCertPending
	case f.DNS.Status == DNSNone:
		return StateDNSNone
	case f.Certificate.Status == CertSelf:
		return StateSelfSigned
	case redirecting:
		return StateRedirect
	case f.DNS.Status == DNSOK && f.Certificate.Status == CertIssued:
		return StateLive
	case f.DNS.Status == DNSUnknown && f.Certificate.Status == CertIssued:
		// The resolvers didn't answer, but a trusted certificate was issued for
		// the host, which proves the issuer reached it: live, not "couldn't check".
		return StateLive
	}
	return StateUnknown
}

// Words is the state's words on its pill. A redirect's target and a stopped or
// undeployed app's reason are drawn by the caller.
func (s State) Words() string {
	switch s {
	case StateDNSElsewhere:
		return "DNS points elsewhere"
	case StateCertExpiring:
		return "certificate expiring"
	case StateCertPending:
		return "getting certificate"
	case StateDNSNone:
		return "no DNS record"
	case StateSelfSigned:
		return "Traefik's own certificate"
	case StateRedirect:
		return "redirects"
	case StateLive:
		return "live · HTTPS"
	case StateNotServed:
		return "not served"
	}
	return "couldn't check"
}

// Mark is the state's mark: failed, warning, rolling, healthy or idle.
func (s State) Mark() string {
	switch s {
	case StateDNSElsewhere:
		return "failed"
	case StateCertExpiring, StateDNSNone:
		return "warning"
	case StateCertPending:
		return "rolling"
	case StateLive:
		return "healthy"
	}
	return "idle"
}
