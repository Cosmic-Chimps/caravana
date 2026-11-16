package app

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// Limits of a service's DNS settings. resolv.conf reads at most three
// nameservers and six search domains.
const (
	// MaxDNSServers is how many resolvers one service may name.
	MaxDNSServers = 3
	// MaxDNSSearches is how many search domains one service may name.
	MaxDNSSearches = 6
	// MaxDNSOptions is how many resolver options one service may name.
	MaxDNSOptions = 8
)

// dnsLabelRE is one label of a DNS name: lowercase letters, digits and "-",
// not starting or ending with "-", at most 63 characters.
var dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// dnsOptionRE is a resolv.conf option: a word, with an optional number
// ("ndots:2", "edns0", "timeout:3", "rotate").
var dnsOptionRE = regexp.MustCompile(`^[a-z][a-z0-9-]*(:[0-9]{1,4})?$`)

// DNS is the resolver configuration a service's copies get: Nomad writes it
// to the task's resolv.conf (the group network's dns block). A service
// without it keeps the node's resolvers.
type DNS struct {
	// Servers are the nameservers' IP addresses, at most 3, in the order
	// they are asked.
	Servers []string `json:"servers,omitempty"`
	// Searches are the search domains, at most 6, lowercase, without a
	// trailing dot.
	Searches []string `json:"searches,omitempty"`
	// Options are resolv.conf options such as "ndots:2" or "edns0", at most 8.
	Options []string `json:"options,omitempty"`
}

// Empty reports whether d sets nothing.
func (d DNS) Empty() bool {
	return len(d.Servers) == 0 && len(d.Searches) == 0 && len(d.Options) == 0
}

// CheckDNS checks d. The three field arguments are the paths its errors
// name for the servers, the search domains and the options, so Compose can
// report the paths of its own attributes ("services.web.dns"); an item is
// named as field[i].
func CheckDNS(d DNS, serversField, searchesField, optionsField string) error {
	if len(d.Servers) > MaxDNSServers {
		return NewFieldError(serversField, "has %d servers; at most %d are allowed", len(d.Servers), MaxDNSServers)
	}
	seen := map[string]bool{}
	for i, s := range d.Servers {
		f := fmt.Sprintf("%s[%d]", serversField, i)
		if strings.Contains(s, "%") {
			return NewFieldError(f, "%q must be an IP address without a zone", s)
		}
		if _, err := netip.ParseAddr(s); err != nil {
			return NewFieldError(f, "%q must be an IP address (a name is not accepted)", s)
		}
		if seen[s] {
			return NewFieldError(f, "%q is listed twice", s)
		}
		seen[s] = true
	}

	if len(d.Searches) > MaxDNSSearches {
		return NewFieldError(searchesField, "has %d search domains; at most %d are allowed", len(d.Searches), MaxDNSSearches)
	}
	seen = map[string]bool{}
	for i, s := range d.Searches {
		f := fmt.Sprintf("%s[%d]", searchesField, i)
		if !validDNSName(s) {
			return NewFieldError(f, "%q must be a lowercase DNS name (labels of letters, digits and \"-\", at most 253 characters, no trailing dot)", s)
		}
		if seen[s] {
			return NewFieldError(f, "%q is listed twice", s)
		}
		seen[s] = true
	}

	if len(d.Options) > MaxDNSOptions {
		return NewFieldError(optionsField, "has %d options; at most %d are allowed", len(d.Options), MaxDNSOptions)
	}
	seen = map[string]bool{}
	for i, o := range d.Options {
		f := fmt.Sprintf("%s[%d]", optionsField, i)
		if !dnsOptionRE.MatchString(o) {
			return NewFieldError(f, "%q must be a resolver option such as \"ndots:2\" or \"edns0\"", o)
		}
		if seen[o] {
			return NewFieldError(f, "%q is listed twice", o)
		}
		seen[o] = true
	}
	return nil
}

// validDNSName reports whether s is a lowercase DNS name without a trailing
// dot: 1 to 253 characters in labels of 1 to 63.
func validDNSName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !dnsLabelRE.MatchString(label) {
			return false
		}
	}
	return true
}

// validateDNS checks a service's dns. field is the service's path, e.g.
// "services[0]".
func validateDNS(field string, d *DNS) error {
	if d == nil {
		return nil
	}
	if d.Empty() {
		return NewFieldError(field+".dns", "is empty: set servers, searches or options, or leave dns out")
	}
	return CheckDNS(*d, field+".dns.servers", field+".dns.searches", field+".dns.options")
}
