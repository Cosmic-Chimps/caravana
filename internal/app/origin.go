package app

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The names of the headers a deploy may carry its origin in (feature 020).
// They are also the Field of the *FieldError Validate returns, so the API can
// name the header that broke a rule.
const (
	HeaderOriginCommit     = "Caravana-Origin-Commit"
	HeaderOriginRef        = "Caravana-Origin-Ref"
	HeaderOriginActor      = "Caravana-Origin-Actor"
	HeaderOriginRun        = "Caravana-Origin-Run"
	HeaderOriginRepository = "Caravana-Origin-Repository"
)

// Origin says where a deploy came from: the commit, branch or tag, person,
// CI run and repository that started it. Every part is optional; none is
// checked against anything, it is what the caller said. A deployment records
// it, and the history and notifications show it.
type Origin struct {
	Commit     string `json:"commit,omitempty"`     // 7 to 64 hex digits
	Ref        string `json:"ref,omitempty"`        // e.g. refs/heads/main
	Actor      string `json:"actor,omitempty"`      // who pushed
	Run        string `json:"run,omitempty"`        // the CI run's https URL
	Repository string `json:"repository,omitempty"` // owner/name
}

// Limits of Origin's parts, in bytes.
const (
	maxOriginRef        = 255
	maxOriginActor      = 100
	maxOriginRun        = 500
	maxOriginRepository = 200
	minOriginCommit     = 7
	maxOriginCommit     = 64
)

// IsZero reports whether no part is set.
func (o Origin) IsZero() bool { return o == Origin{} }

// Normalized returns o with each part trimmed and the commit in lowercase.
func (o Origin) Normalized() Origin {
	return Origin{
		Commit:     strings.ToLower(strings.TrimSpace(o.Commit)),
		Ref:        strings.TrimSpace(o.Ref),
		Actor:      strings.TrimSpace(o.Actor),
		Run:        strings.TrimSpace(o.Run),
		Repository: strings.TrimSpace(o.Repository),
	}
}

// Validate checks every part that is set (an empty part is fine) and returns
// the first that breaks its rule as a *FieldError whose Field is the part's
// header name, for example "Caravana-Origin-Commit". o should be Normalized.
func (o Origin) Validate() error {
	if o.Commit != "" {
		if n := len(o.Commit); n < minOriginCommit || n > maxOriginCommit || !isHex(o.Commit) {
			return NewFieldError(HeaderOriginCommit, "must be %d to %d hexadecimal digits", minOriginCommit, maxOriginCommit)
		}
	}
	for _, p := range []struct {
		header, value string
		max           int
	}{
		{HeaderOriginRef, o.Ref, maxOriginRef},
		{HeaderOriginActor, o.Actor, maxOriginActor},
		{HeaderOriginRepository, o.Repository, maxOriginRepository},
	} {
		if p.value == "" {
			continue
		}
		if len(p.value) > p.max {
			return NewFieldError(p.header, "must be at most %d bytes", p.max)
		}
		if !printable(p.value) {
			return NewFieldError(p.header, "must be printable text, without control characters")
		}
	}
	if o.Run != "" {
		if len(o.Run) > maxOriginRun {
			return NewFieldError(HeaderOriginRun, "must be at most %d bytes", maxOriginRun)
		}
		u, err := url.Parse(o.Run)
		if err != nil || u.Scheme != "https" || u.Host == "" || !printable(o.Run) || strings.ContainsAny(o.Run, " \t") {
			return NewFieldError(HeaderOriginRun, "must be an https:// URL")
		}
	}
	return nil
}

// ShortCommit is the first 7 characters of the commit, as people write it.
func (o Origin) ShortCommit() string {
	if len(o.Commit) > 7 {
		return o.Commit[:7]
	}
	return o.Commit
}

// BranchOrTag is the ref without its refs/heads/ or refs/tags/ prefix.
func (o Origin) BranchOrTag() string {
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		if rest, ok := strings.CutPrefix(o.Ref, prefix); ok {
			return rest
		}
	}
	return o.Ref
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}
