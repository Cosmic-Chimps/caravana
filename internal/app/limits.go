package app

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Limits a service may set on its container (feature 060): the two ulimits OpenSearch, Elasticsearch and
// databases ask for, and the namespaced kernel settings.
const (
	// UlimitMemlock and UlimitNofile are the two ulimits a service may set.
	UlimitMemlock = "memlock"
	UlimitNofile  = "nofile"
	// MaxNofile is the kernel's default nr_open: a limit above it can't be set in a container.
	MaxNofile = 1048576
	// MaxSysctls is how many sysctls one service may set.
	MaxSysctls = 16
	// MaxSysctlValue is the longest value of a sysctl, in bytes.
	MaxSysctlValue = 64
	// MinMapCount is the vm.max_map_count the installer sets on every server. OpenSearch and Elasticsearch
	// need at least that; a container can't set it itself.
	MinMapCount = 262144
	// MapCountKey is the one non-namespaced sysctl Compose files carry (Elasticsearch's and Wazuh's do).
	MapCountKey = "vm.max_map_count"
)

// sysctlKeyRE is a namespaced network sysctl: net.<group>.<name>, as in net.ipv4.tcp_keepalive_time.
// Only these are namespaced per container on a Docker host; the rest change the server.
var sysctlKeyRE = regexp.MustCompile(`^net\.[a-z0-9_]+(\.[a-z0-9_-]+)+$`)

// Ulimit is one limit as the docker driver takes it, "soft:hard". -1 is unlimited (memlock only).
type Ulimit struct {
	Soft int64 `json:"soft"`
	Hard int64 `json:"hard"`
}

// String is the value of the docker driver's ulimit map: "65536:65536".
func (u Ulimit) String() string { return fmt.Sprintf("%d:%d", u.Soft, u.Hard) }

// CheckUlimits checks a service's ulimits in the order of their names. field is the path of the map
// ("services[0].ulimits"); an error names field.<name>[.soft|.hard].
func CheckUlimits(field string, ulimits map[string]Ulimit) error {
	names := make([]string, 0, len(ulimits))
	for name := range ulimits {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := CheckUlimit(field+"."+name, name, ulimits[name], false); err != nil {
			return err
		}
	}
	return nil
}

// CheckUlimit checks one limit. field is its own path; single says it was written as one number (Compose's
// short form), so an error names the limit itself and not its soft or hard part.
func CheckUlimit(field, name string, u Ulimit, single bool) error {
	part := func(p string) string {
		if single {
			return field
		}
		return field + "." + p
	}
	var lo int64
	switch name {
	case UlimitMemlock:
		lo = -1
	case UlimitNofile:
		lo = 1
	default:
		return NewFieldError(field, "%q isn't supported: Caravana sets only %s and %s", name, UlimitMemlock, UlimitNofile)
	}
	for _, p := range []struct {
		part string
		v    int64
	}{{"soft", u.Soft}, {"hard", u.Hard}} {
		switch {
		case name == UlimitMemlock && p.v < -1:
			return NewFieldError(part(p.part), "is %d: use -1 for unlimited, or a number of bytes", p.v)
		case name == UlimitNofile && (p.v < lo || p.v > MaxNofile):
			return NewFieldError(part(p.part), "is %d: nofile is between 1 and %d", p.v, MaxNofile)
		}
	}
	// -1 is unlimited, so it is above every number.
	rank := func(v int64) int64 {
		if v == -1 {
			return math.MaxInt64
		}
		return v
	}
	if rank(u.Soft) > rank(u.Hard) {
		return NewFieldError(part("soft"), "is %d, above the hard limit %d", u.Soft, u.Hard)
	}
	return nil
}

// CheckSysctls checks a service's sysctls in the order of their keys. field is the path of the map
// ("services[0].sysctls"); an error names field.<key>.
func CheckSysctls(field string, sysctls map[string]string) error {
	if len(sysctls) > MaxSysctls {
		return NewFieldError(field, "has %d sysctls; at most %d are allowed", len(sysctls), MaxSysctls)
	}
	keys := make([]string, 0, len(sysctls))
	for k := range sysctls {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		f := field + "." + k
		if !sysctlKeyRE.MatchString(k) {
			if strings.HasPrefix(k, "net.") {
				return NewFieldError(f, "%q isn't a network sysctl name (net.<group>.<name>, lowercase)", k)
			}
			return NewFieldError(f, "%s isn't namespaced: it can only be set on the server", k)
		}
		v := sysctls[k]
		if v == "" || len(v) > MaxSysctlValue {
			return NewFieldError(f, "must be 1 to %d characters", MaxSysctlValue)
		}
		if strings.TrimSpace(v) != v || strings.ContainsFunc(v, func(r rune) bool { return !unicode.IsPrint(r) }) {
			return NewFieldError(f, "must be printable text without spaces at its ends")
		}
	}
	return nil
}

// validateLimits checks a service's ulimits and sysctls. field is the service's path, e.g. "services[0]".
func validateLimits(field string, svc Service) error {
	if err := CheckUlimits(field+".ulimits", svc.Ulimits); err != nil {
		return err
	}
	return CheckSysctls(field+".sysctls", svc.Sysctls)
}
