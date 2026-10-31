package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// TextPart is a piece of a sentence. Mono marks a name Nomad or the owner's config
// produced (an app, a service, a schedule, a program, a version, a mount, a domain),
// which a page sets in its mono face. The activity feed sends sentences as parts so
// that the console draws them as it draws the needs-you rows, and the API's plain
// `message` is the parts joined.
//
// Muted marks the place in front of an app's name ("Shop › staging › "): the console sets it in its
// third text colour. It is the console's own, never part of the API's answer.
type TextPart struct {
	Text  string `json:"text"`
	Mono  bool   `json:"mono,omitempty"`
	Muted bool   `json:"muted,omitempty"`
}

// txt and mono make parts; join gives a sentence as plain text.
func txt(s string) TextPart   { return TextPart{Text: s} }
func mono(s string) TextPart  { return TextPart{Text: s, Mono: true} }
func muted(s string) TextPart { return TextPart{Text: s, Muted: true} }

func plainText(parts []TextPart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// parts joins pieces, where each is a TextPart or a []TextPart, into one sentence.
func parts(pieces ...any) []TextPart {
	var out []TextPart
	for _, p := range pieces {
		switch v := p.(type) {
		case TextPart:
			out = append(out, v)
		case []TextPart:
			out = append(out, v...)
		case string:
			out = append(out, txt(v))
		}
	}
	return out
}

// encodeParts is the stored form of a sentence: JSON, never empty text.
func encodeParts(p []TextPart) string {
	b, err := json.Marshal(p)
	if err != nil { // cannot happen for strings and a bool
		return ""
	}
	return string(b)
}

// decodeParts reads a stored sentence. ok is false for text that isn't parts.
func decodeParts(s string) (out []TextPart, ok bool) {
	if strings.TrimSpace(s) == "" || json.Unmarshal([]byte(s), &out) != nil || len(out) == 0 {
		return nil, false
	}
	return out, true
}

// labelParts says where an app is, the one wording of every sentence of the feed and of the
// needs-you list: its ID for an app in Default's production, else "<project display> › <environment>
// › <name>", the place muted (the environment in mono) and the name in mono. suffix ("/web") is glued
// to the app's name.
func labelParts(display func(string) string, pl app.Place, id, suffix string) []TextPart {
	if pl.Project == "" || (pl.Project == app.DefaultProject && pl.Environment == app.DefaultEnvironment) {
		return []TextPart{mono(id + suffix)}
	}
	return []TextPart{muted(display(pl.Project) + " › "), {Text: pl.Environment, Mono: true, Muted: true}, muted(" › "), mono(pl.Name + suffix)}
}

// placeParts is labelParts for a needs-you item.
func placeParts(display func(string) string, it AttentionItem, suffix string) []TextPart {
	return labelParts(display, app.Place{Project: it.Project, Environment: it.Environment, Name: it.AppName}, it.AppID, suffix)
}

// ProblemParts is the one wording of a needs-you item, as parts, with the names (apps, services,
// schedules, mounts, domains, kernels, packages and versions) in mono. The console's needs-you list
// (internal/ui/attention.go), the host pages' banners and the activity feed all draw these parts, so
// they cannot say different things; the feed stores them when the event is written (the item is gone
// by the time the problem resolves). display says a project's name for people.
//
// The API's `detail` is the plain text of the same words for the host kinds (the parts joined); the
// list's one addition is the "keeps running" sentence of a failed deploy, which needs the app's
// state and so is the console's.
func ProblemParts(it AttentionItem, display func(string) string) []TextPart {
	who := placeParts(display, it, "")
	where := placeParts(display, it, slashed(it.Service))
	n := len(it.Copies)
	switch it.Kind {
	case AttentionDeployFailed:
		version := txt("This deploy")
		if it.Version != nil {
			version = mono(fmt.Sprintf("v%d", *it.Version))
		}
		tail := " didn't start."
		switch {
		case it.DeploymentStatus == store.StatusReverted:
			tail = " failed its health checks."
		case it.Version != nil:
			tail = " failed during its rollout."
		default:
			// Detail is the deployment's error; only the cause the words know is said.
			if cause := FailureCause(it.Detail); cause != "" {
				tail = " didn't start: " + cause + "."
			}
		}
		return parts(who, ": ", version, tail)
	case AttentionOutOfMemory:
		return parts(copyCount(n)+" of ", where, " ran out of memory.")
	case AttentionRestarting:
		return parts(copyCount(n)+" of ", where, " "+verbFor(n, "keeps", "keep")+" restarting.")
	case AttentionUnhealthy:
		return parts(copyCount(n)+" of ", where, " "+verbFor(n, "is", "are")+" failing its health check.")
	case AttentionBackupFailed:
		return parts("The latest backup of ", where, " failed.")
	case AttentionRestoreFailed:
		return parts("The latest restore of ", where, " failed.")
	case AttentionNoBackupDestination:
		return parts("Backups are set up, but no backup destination is.")
	case AttentionScheduleFailed:
		// Detail is the run's sentence in Caravana's words ("Stopped at the 5 min limit."), never its output.
		return parts(who, ": the last run of ", mono(it.Schedule), " failed. "+it.Detail)
	case AttentionScheduleSkipping:
		return parts(who, ": "+it.Detail)
	case AttentionDiskNearlyFull:
		return parts("The disk at ", mono(it.Mount), notify.DiskTail(it.UsedPercent, it.FreeBytes))
	case AttentionOSRebootRequired:
		return rebootParts(it)
	case AttentionOSSecurityUpdates:
		return parts(securitySentence(it.Updates, it.Security))
	case AttentionDomainDNSElsewhere:
		if it.AddressSeen == "" {
			return parts(who, ": ", mono(it.Domain), " points at another host.")
		}
		return parts(who, ": ", mono(it.Domain), " points at ", mono(it.AddressSeen), ", which isn't this host.")
	case AttentionDomainCertExpiring:
		// Detail is "The certificate for <host> expires in 6 days and hasn't been renewed."
		if rest, ok := strings.CutPrefix(it.Detail, "The certificate for "+it.Domain); ok {
			return parts(who, ": the certificate for ", mono(it.Domain), rest)
		}
		return parts(who, ": "+it.Detail)
	}
	// A kind added to the list and not yet worded here: still say something.
	if it.AppID != "" {
		return parts(who, ": needs you ("+string(it.Kind)+").")
	}
	return parts("Needs you (" + string(it.Kind) + ").")
}

// AttentionKinds lists every kind of item, so a test can walk them all. A test reads the constants
// from attention.go and fails when this list misses one.
var AttentionKinds = []AttentionKind{
	AttentionDeployFailed, AttentionOutOfMemory, AttentionRestarting, AttentionUnhealthy,
	AttentionBackupFailed, AttentionRestoreFailed, AttentionNoBackupDestination,
	AttentionScheduleFailed, AttentionScheduleSkipping,
	AttentionDomainDNSElsewhere, AttentionDomainCertExpiring,
	AttentionDiskNearlyFull, AttentionOSRebootRequired, AttentionOSSecurityUpdates,
}

// slashed is "/web" for a service name, "" for none.
func slashed(service string) string {
	if service == "" {
		return ""
	}
	return "/" + service
}
