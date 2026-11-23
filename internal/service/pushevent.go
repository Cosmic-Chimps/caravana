package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

// Push webhooks (feature 050, plan D6). The app's webhook is also the
// repository's push webhook: GitHub, Gitea, Forgejo and GitLab call it with a
// body that says which branch moved to which commit. This file only reads that
// body and headers, so it is pure and tested with real-shaped bodies.

// Push is what a push webhook's call says. The zero value is a call that came
// from no provider we know (or none at all), which builds the branch's newest
// commit, as the webhook did before builds existed.
type Push struct {
	// Event is "push", "ping" or "other" for a call from a provider, and ""
	// when its headers name none.
	Event string
	// Name is the provider's own name for an event that is "other" (for
	// example "issues"), cleaned for printing.
	Name string
	// Ref is the ref that moved: "refs/heads/main". Empty when the body gave none.
	Ref string
	// Commit is the commit the ref moved to, lower-case hex; empty when the
	// body gave none or the ref was deleted.
	Commit string
	// Deleted says the push deleted the ref.
	Deleted bool
}

// The headers of the providers.
const (
	headerGitHubEvent  = "X-GitHub-Event"
	headerGiteaEvent   = "X-Gitea-Event"
	headerForgejoEvent = "X-Forgejo-Event"
	headerGitLabEvent  = "X-Gitlab-Event"
)

// MaxPushBody is how much of a webhook's body is read: a push body lists its
// commits, and 1 MiB is far more than the fields read here sit in.
const MaxPushBody = 1 << 20

// PushEvent reads a webhook call's headers and body. A body that can't be read
// as JSON, or leaves a field out, leaves that field empty: the call is then a
// push of an unknown commit, which builds the branch's newest.
func PushEvent(h http.Header, body []byte) Push {
	var p Push
	gitlab := false
	switch {
	case h.Get(headerGitHubEvent) != "":
		p.Event, p.Name = eventOf(h.Get(headerGitHubEvent), "push", "ping")
	case h.Get(headerGiteaEvent) != "":
		p.Event, p.Name = eventOf(h.Get(headerGiteaEvent), "push", "ping")
	case h.Get(headerForgejoEvent) != "":
		p.Event, p.Name = eventOf(h.Get(headerForgejoEvent), "push", "ping")
	case h.Get(headerGitLabEvent) != "":
		gitlab = true
		// GitLab's names are words: "Push Hook", "Tag Push Hook", "Issue Hook"...
		switch name := h.Get(headerGitLabEvent); name {
		case "Push Hook":
			p.Event = "push"
		default:
			p.Event, p.Name = "other", cleanEventName(name)
		}
	default:
		return p
	}
	if p.Event != "push" {
		return p
	}

	var doc struct {
		Ref         string  `json:"ref"`
		After       string  `json:"after"`
		Deleted     bool    `json:"deleted"`
		CheckoutSHA *string `json:"checkout_sha"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return p
	}
	p.Ref = doc.Ref
	commit := doc.After
	if gitlab && doc.CheckoutSHA != nil && *doc.CheckoutSHA != "" {
		commit = *doc.CheckoutSHA
	}
	commit = strings.ToLower(strings.TrimSpace(commit))
	switch {
	case isZeroSHA(commit) || (gitlab && doc.CheckoutSHA == nil && doc.After != ""):
		// A deleted branch: GitHub and Gitea say so, GitLab sends an all-zero
		// "after" and no checkout.
		p.Deleted = true
	case doc.Deleted:
		p.Deleted = true
	case isHexCommit(commit):
		p.Commit = commit
	}
	return p
}

// eventOf names a GitHub-style event: push and ping are known, anything else
// is "other" with its cleaned name.
func eventOf(name, push, ping string) (event, other string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case push:
		return "push", ""
	case ping:
		return "ping", ""
	}
	return "other", cleanEventName(name)
}

// cleanEventName keeps a provider's event name printable and short, since it is
// shown in a sentence.
func cleanEventName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\n' {
			return -1
		}
		return r
	}, s)
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.TrimSpace(s)
}

func isZeroSHA(s string) bool {
	return s != "" && strings.Trim(s, "0") == ""
}

func isHexCommit(s string) bool {
	if n := len(s); n < 7 || n > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// SkipReason says why a call builds nothing for an app that builds branch: a
// sentence, or "" when it should build. A ping, an event that is no push, a
// deleted branch and a push to another ref are skipped; the same commit as the
// live one is the caller's check, since it needs the history.
func (p Push) SkipReason(branch string) string {
	switch {
	case p.Event == "ping":
		return "a ping"
	case p.Event == "other":
		if p.Name == "" {
			return "an event that is not a push"
		}
		return fmt.Sprintf("an event that is not a push (%s)", p.Name)
	case p.Event != "push":
		return ""
	case p.Deleted:
		return "the branch was deleted"
	case p.Ref != "" && p.Ref != "refs/heads/"+branch:
		return fmt.Sprintf("a push to %s, not %s", p.Ref, branch)
	}
	return ""
}
