package registry

import (
	"slices"
	"testing"
)

func TestEntryCandidates(t *testing.T) {
	tests := []struct {
		name       string
		host, repo string
		want       []string
	}{
		{"three segments", "ghcr.io", "a/b/c", []string{"ghcr.io/a/b/c", "ghcr.io/a/b", "ghcr.io/a", "ghcr.io"}},
		{"one segment", "ghcr.io", "acme", []string{"ghcr.io/acme", "ghcr.io"}},
		{"no repository", "ghcr.io", "", []string{"ghcr.io"}},
		{"docker hub official image", "docker.io", "library/nginx", []string{"docker.io/library/nginx", "docker.io/library", "docker.io"}},
		{"host with a port", "127.0.0.1:15000", "teama/busybox", []string{"127.0.0.1:15000/teama/busybox", "127.0.0.1:15000/teama", "127.0.0.1:15000"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EntryCandidates(tt.host, tt.repo); !slices.Equal(got, tt.want) {
				t.Errorf("EntryCandidates(%q, %q) = %v, want %v", tt.host, tt.repo, got, tt.want)
			}
		})
	}
}

func TestMatchEntry(t *testing.T) {
	all := map[string]bool{"ghcr.io": true, "ghcr.io/acme": true, "ghcr.io/acme/deep": true, "docker.io/library": true}
	tests := []struct {
		name       string
		entries    map[string]bool
		host, repo string
		want       string
		wantOK     bool
	}{
		{"the most specific wins", all, "ghcr.io", "acme/deep/x", "ghcr.io/acme/deep", true},
		{"a prefix entry matches its own repository", all, "ghcr.io", "acme", "ghcr.io/acme", true},
		{"a prefix entry matches below it", all, "ghcr.io", "acme/shop", "ghcr.io/acme", true},
		{"acme never matches acme-old", map[string]bool{"ghcr.io/acme": true}, "ghcr.io", "acme-old/x", "", false},
		{"acme-old falls back to the host entry", map[string]bool{"ghcr.io/acme": true, "ghcr.io": true}, "ghcr.io", "acme-old/x", "ghcr.io", true},
		{"another owner uses the host entry", all, "ghcr.io", "other/x", "ghcr.io", true},
		{"nothing matches: anonymous", map[string]bool{"ghcr.io/acme": true}, "ghcr.io", "other/x", "", false},
		{"another host", all, "quay.io", "acme/x", "", false},
		{"docker hub library", all, "docker.io", "library/nginx", "docker.io/library", true},
		{"no entries", nil, "ghcr.io", "acme/x", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := MatchEntry(tt.entries, tt.host, tt.repo)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("MatchEntry(%q, %q) = %q, %v; want %q, %v", tt.host, tt.repo, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestSplitEntry(t *testing.T) {
	tests := []struct{ entry, host, path string }{
		{"ghcr.io", "ghcr.io", ""},
		{"ghcr.io/acme", "ghcr.io", "acme"},
		{"127.0.0.1:15000/teama/sub", "127.0.0.1:15000", "teama/sub"},
	}
	for _, tt := range tests {
		host, path := SplitEntry(tt.entry)
		if host != tt.host || path != tt.path {
			t.Errorf("SplitEntry(%q) = %q, %q; want %q, %q", tt.entry, host, path, tt.host, tt.path)
		}
		if got := EntryHost(tt.entry); got != tt.host {
			t.Errorf("EntryHost(%q) = %q, want %q", tt.entry, got, tt.host)
		}
	}
}
