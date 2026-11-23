package mesh

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// hostLabel is one label of a host name: letters, digits and dashes, none at either end.
const hostLabel = `[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?`

var hostNameRE = regexp.MustCompile(`^` + hostLabel + `(\.` + hostLabel + `)+$`)

// CleanIssuer says whether s is an issuer Nomad may be given as `oidc_issuer` (spec 038): "" (none),
// or https:// and a host name with an optional port and nothing after it. A host name has labels of
// letters, digits and dashes and at least one dot (or is exactly localhost); an IP address is not one.
// One trailing "/" is allowed and removed. The console and the agent share this rule, so the console
// never sends an answer the agent would refuse.
func CleanIssuer(s string) (string, bool) {
	if s == "" {
		return "", true
	}
	rest, ok := strings.CutPrefix(strings.TrimSuffix(s, "/"), "https://")
	if !ok || rest == "" || strings.ContainsAny(rest, "/?#@\\ \t\r\n[]") {
		return "", false
	}
	host, port, hasPort := strings.Cut(rest, ":")
	if hasPort {
		if port == "" || len(port) > 5 || strings.Trim(port, "0123456789") != "" {
			return "", false
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", false
		}
	}
	if !validIssuerHost(host) {
		return "", false
	}
	return "https://" + rest, true
}

func validIssuerHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	if host == "localhost" {
		return true
	}
	if !hostNameRE.MatchString(host) {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	last := host[strings.LastIndexByte(host, '.')+1:]
	return strings.Trim(last, "0123456789") != "" // a name whose last label is all digits is an address in disguise
}

// ValidConsoleDomain says whether s is a domain the console's address may be changed to (feature 051):
// lower case letters, digits, dashes and dots, labels of 1 to 63 bytes without a dash at either end,
// at least one dot, at most 253 bytes, and not an IP address (nor a name whose last label is all
// digits). It is the installer's host-name rule (install.sh's is_hostname) in lower case, and the
// console and the node agent share it, so the console never sends a domain the agent would refuse.
func ValidConsoleDomain(s string) bool {
	return s != "localhost" && s == strings.ToLower(s) && validIssuerHost(s)
}
