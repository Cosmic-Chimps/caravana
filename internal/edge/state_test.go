package edge

import (
	"slices"
	"testing"
)

func facts(dns, cert string) Facts {
	return Facts{DNS: DNS{Status: dns}, Certificate: Certificate{Status: cert}}
}

func TestStateOf(t *testing.T) {
	tests := []struct {
		name                string
		f                   Facts
		served, redirecting bool
		want                State
	}{
		{"not served beats everything", facts(DNSElsewhere, CertExpiring), false, true, StateNotServed},
		{"not served with nothing checked", facts("", CertNone), false, false, StateNotServed},
		{"dns elsewhere beats a bad certificate", facts(DNSElsewhere, CertExpiring), true, false, StateDNSElsewhere},
		{"dns elsewhere beats live", facts(DNSElsewhere, CertIssued), true, false, StateDNSElsewhere},
		{"dns elsewhere beats a redirect", facts(DNSElsewhere, CertIssued), true, true, StateDNSElsewhere},
		{"expiring beats pending's neighbours", facts(DNSNone, CertExpiring), true, false, StateCertExpiring},
		{"expiring beats a redirect", facts(DNSOK, CertExpiring), true, true, StateCertExpiring},
		{"pending beats no record", facts(DNSNone, CertPending), true, false, StateCertPending},
		{"pending with dns ok", facts(DNSOK, CertPending), true, false, StateCertPending},
		{"no record with a certificate error", facts(DNSNone, CertError), true, false, StateDNSNone},
		{"no record beats self-signed", facts(DNSNone, CertSelf), true, false, StateDNSNone},
		{"self-signed", facts(DNSOK, CertSelf), true, false, StateSelfSigned},
		{"self-signed with unknown dns", facts(DNSUnknown, CertSelf), true, false, StateSelfSigned},
		{"self-signed beats a redirect", facts(DNSOK, CertSelf), true, true, StateSelfSigned},
		{"redirect", facts(DNSOK, CertIssued), true, true, StateRedirect},
		{"redirect with unknown dns", facts(DNSUnknown, CertIssued), true, true, StateRedirect},
		{"live", facts(DNSOK, CertIssued), true, false, StateLive},
		{"unknown dns with an issued certificate", facts(DNSUnknown, CertIssued), true, false, StateUnknown},
		{"dns ok, certificate error", facts(DNSOK, CertError), true, false, StateUnknown},
		{"dns ok, certificate not probed", facts(DNSOK, CertNone), true, false, StateUnknown},
		{"nothing could be checked", facts(DNSUnknown, CertError), true, false, StateUnknown},
	}
	reached := map[State]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StateOf(tt.f, tt.served, tt.redirecting); got != tt.want {
				t.Errorf("StateOf = %q, want %q", got, tt.want)
			}
		})
		reached[tt.want] = true
	}
	for _, s := range States {
		if !reached[s] {
			t.Errorf("state %q is not reached by any case", s)
		}
	}
}

func TestStatesWordsAndMarks(t *testing.T) {
	if len(States) != 9 {
		t.Fatalf("%d states, want 9", len(States))
	}
	marks := []string{"failed", "warning", "rolling", "healthy", "idle"}
	seenWords := map[string]State{}
	for _, s := range States {
		w := s.Words()
		if w == "" {
			t.Errorf("%s has no words", s)
		}
		if other, dup := seenWords[w]; dup && s != StateUnknown {
			t.Errorf("%s and %s share the words %q", s, other, w)
		}
		seenWords[w] = s
		if !slices.Contains(marks, s.Mark()) {
			t.Errorf("%s has mark %q", s, s.Mark())
		}
	}
	for s, want := range map[State]string{
		StateDNSElsewhere: "failed", StateCertExpiring: "warning", StateDNSNone: "warning",
		StateCertPending: "rolling", StateLive: "healthy", StateSelfSigned: "idle",
		StateRedirect: "idle", StateUnknown: "idle", StateNotServed: "idle",
	} {
		if s.Mark() != want {
			t.Errorf("%s mark = %q, want %q", s, s.Mark(), want)
		}
	}
	if got := StateLive.Words(); got != "live · HTTPS" {
		t.Errorf("live words = %q", got)
	}
	if got := State("nonsense").Words(); got != "couldn't check" {
		t.Errorf("unknown words = %q", got)
	}
}
