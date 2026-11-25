package certs

import (
	"slices"
	"strings"
)

// Covers says a certificate with these names serves host: an exact name (case does not matter), or
// a wildcard "*.rest" with exactly one label before ".rest" in the host. A wildcard never covers
// "rest" itself nor a host with two labels before it.
func Covers(names []string, host string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	for _, name := range names {
		name = normalizeHost(name)
		if name == host {
			return true
		}
		rest, ok := strings.CutPrefix(name, "*.")
		if !ok || rest == "" {
			continue
		}
		label, ok := strings.CutSuffix(host, "."+rest)
		if ok && label != "" && !strings.Contains(label, ".") && !strings.Contains(label, "*") {
			return true
		}
	}
	return false
}

// SameNames says two lists hold the same names, as sets and without regard to case.
func SameNames(a, b []string) bool {
	return slices.Equal(uniqueLower(a), uniqueLower(b))
}

func uniqueLower(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, normalizeHost(n))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}
