package mesh

import (
	"fmt"
	"regexp"
	"strings"
)

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ValidName says s is a server or pool name: a lowercase DNS label, at most 63 characters. The
// agent and the console apply the same rule, which is why it lives here (contracts/node.md §3).
func ValidName(s string) bool {
	return len(s) <= 63 && nameRE.MatchString(s)
}

// NameOfHost makes a valid name from a host name: lowercase, the first label only, every character
// outside [a-z0-9-] turned into "-", dashes trimmed from the ends, at most 63 characters; empty
// becomes "server-<index>".
func NameOfHost(host string, index int) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, '.'); i >= 0 {
		host = host[:i]
	}
	host = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, host)
	host = strings.Trim(host, "-")
	if len(host) > 63 {
		host = strings.Trim(host[:63], "-")
	}
	if host == "" {
		return fmt.Sprintf("server-%d", index)
	}
	if ReservedName(host) {
		return fmt.Sprintf("%s-%d", host, index)
	}
	return host
}

// ReservedName says name can't be a server's: the console has pages at /ops/servers/new and
// /ops/servers/joins/…, so a server with one of these names would have no page of its own.
func ReservedName(name string) bool {
	return name == "new" || name == "joins"
}
