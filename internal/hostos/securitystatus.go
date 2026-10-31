package hostos

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
)

// ErrProUnavailable is what RunPro returns on a host that has no `pro` (Ubuntu
// Pro's client, which every Ubuntu has and Debian does not).
var ErrProUnavailable = errors.New("the pro command isn't on this host")

// ErrBadSecurityStatus is returned for an answer that isn't the JSON
// `pro security-status --format json` writes. It never carries the answer: it
// is data from the host and never goes into an error or a log.
var ErrBadSecurityStatus = errors.New("pro's answer isn't the JSON that was expected")

// maxSecurityPackages is how many package names one answer may give. An Ubuntu
// host has about 700 packages installed; anything near this is not an answer.
const maxSecurityPackages = 5000

// SecurityStatus is what `pro security-status` knows about the packages that can
// be updated from Ubuntu's security pocket. Only pro says what a security update
// is: a kernel waiting in -updates is not one.
type SecurityStatus struct {
	// Packages are the names of the installed packages that have a standard
	// security update waiting, sorted, once each.
	Packages []string
	// Kernel is true when a kernel image (linux-image-*) is among them.
	Kernel bool
	// Count is pro's own number of standard security updates
	// (summary.num_standard_security_updates); it can be larger than the list
	// when pro lists a package more than once.
	Count int
}

// proPackage is the part of a packages[] entry that matters.
type proPackage struct {
	Package     string `json:"package"`
	ServiceName string `json:"service_name"`
	Status      string `json:"status"`
}

// packageName is what apt accepts as a package name: lower-case letters,
// digits and + - . (a ":arch" suffix is cut before the check).
var packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)

// ParseSecurityStatus reads the JSON of `pro security-status --format json`
// (ubuntu-pro-client's security_status.py: packages[] has one entry per
// upgradable package and service): the packages of the "standard-security"
// service that can be updated. The packages for ESM services are left out: they
// need an Ubuntu Pro subscription this host may not have, so they are not
// updates it can apply.
func ParseSecurityStatus(data []byte) (SecurityStatus, error) {
	var doc struct {
		Packages []proPackage `json:"packages"`
		Summary  struct {
			Standard int `json:"num_standard_security_updates"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.Packages) > maxSecurityPackages {
		return SecurityStatus{}, ErrBadSecurityStatus
	}
	var st SecurityStatus
	st.Count = max(doc.Summary.Standard, 0)
	for _, p := range doc.Packages {
		// pro writes "upgrade_available_not_preferred" when the security version
		// isn't apt's candidate (a newer one from -updates is): the package still
		// has the fix waiting, and pro counts it.
		waiting := p.Status == "upgrade_available" || p.Status == "upgrade_available_not_preferred"
		if !waiting || p.ServiceName != "standard-security" {
			continue
		}
		name, _, _ := strings.Cut(p.Package, ":")
		if !packageName.MatchString(name) {
			continue
		}
		st.Packages = append(st.Packages, name)
	}
	slices.Sort(st.Packages)
	st.Packages = slices.Compact(st.Packages)
	for _, name := range st.Packages {
		if strings.HasPrefix(name, "linux-image-") {
			st.Kernel = true
		}
	}
	return st, nil
}
