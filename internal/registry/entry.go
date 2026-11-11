package registry

import "strings"

// A registry entry is how Caravana names a set of credentials: a registry host
// ("ghcr.io") or a host with a path prefix ("ghcr.io/acme"). The credentials of
// an entry cover the images whose repository is the path or lies under it,
// matched by whole segments. An entry with no path is the fallback for every
// other image on that host.

// EntryCandidates lists the entries that could cover an image on host with this
// repository, most specific first, ending with the host alone. Each candidate
// is a whole-segment prefix of the repository:
// ("ghcr.io", "a/b/c") gives ghcr.io/a/b/c, ghcr.io/a/b, ghcr.io/a, ghcr.io.
func EntryCandidates(host, repository string) []string {
	repository = strings.Trim(repository, "/")
	if repository == "" {
		return []string{host}
	}
	parts := strings.Split(repository, "/")
	candidates := make([]string, 0, len(parts)+1)
	for n := len(parts); n > 0; n-- {
		candidates = append(candidates, host+"/"+strings.Join(parts[:n], "/"))
	}
	return append(candidates, host)
}

// MatchEntry returns the most specific entry of entries that covers an image on
// host with this repository. ok is false when none does (the image is pulled
// anonymously).
func MatchEntry(entries map[string]bool, host, repository string) (entry string, ok bool) {
	for _, candidate := range EntryCandidates(host, repository) {
		if entries[candidate] {
			return candidate, true
		}
	}
	return "", false
}

// SplitEntry splits an entry at its first "/" into the host and the path
// prefix ("" for a host-only entry).
func SplitEntry(entry string) (host, path string) {
	host, path, _ = strings.Cut(entry, "/")
	return host, path
}

// EntryHost is the registry host of an entry.
func EntryHost(entry string) string {
	host, _ := SplitEntry(entry)
	return host
}
