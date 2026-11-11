package service

import (
	"fmt"
	"time"

	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/metrics"
)

// The host's two OS items on the needs-you list (feature 027 page 3): a reboot
// is pending, and security updates are waiting. Both come from the files the
// sampler reads, are derived on every read like the rest of the list, and name
// no vulnerability: they say what is true (a fix is installed and the old kernel
// still runs; updates are waiting).

// securityGrace is how long security updates may wait, with the host's own
// automatic updates on, before they are an item: unattended-upgrades installs
// them by itself overnight, so an item before then would be noise.
const securityGrace = 24 * time.Hour

// osReadFresh is how old the sampler's reading of the OS files may be to judge
// by it: the same five minutes a host sample may be (the disk kind's rule).
func osReadFresh(readAt, now time.Time) bool { return !metrics.Stale(readAt, now) }

// osItems is the OS kinds for the list. known is false when they couldn't be
// judged: the sampler hasn't read the files yet, or its newest reading is older
// than 5 minutes. The notifier then neither opens nor resolves them.
func (s *Service) osItems(now time.Time) (items []AttentionItem, known bool) {
	state, readAt, ok := s.osState()
	if !ok || !osReadFresh(readAt, now) {
		return nil, false
	}
	running := ""
	if node := s.hostNode(); node != nil {
		running = node.Kernel
	}
	return osItemsOf(now, state, running, s.securityClock()), true
}

// osItemsOf is the pure part: the reboot item while the marker exists, and the
// security item when the list is known, under 48 hours old, names security
// updates and, with unattended-upgrades on, they have waited 24 hours
// (securitySince is when the count first went above zero; zero means not known,
// which with the automatic updates on is "not yet"). running is the node's
// kernel release.
func osItemsOf(now time.Time, state hostos.State, running string, securitySince time.Time) []AttentionItem {
	var items []AttentionItem
	if state.Reboot.Required {
		it := AttentionItem{
			Kind: AttentionOSRebootRequired, Packages: state.Reboot.Packages,
			InstalledKernel: state.Reboot.InstalledKernel, RunningKernel: running, When: state.Reboot.Since,
		}
		it.Detail = rebootSentence(it)
		items = append(items, it)
	}
	u := state.Updates
	if state.Readable && u.Known && u.Security > 0 && !u.Stale(now) {
		auto := state.Unattended.Enabled != nil && *state.Unattended.Enabled
		if !auto || (!securitySince.IsZero() && now.Sub(securitySince) >= securityGrace) {
			it := AttentionItem{Kind: AttentionOSSecurityUpdates, Updates: u.Total, Security: u.Security, When: securitySince}
			if it.When.IsZero() {
				it.When = u.CheckedAt
			}
			it.Detail = securitySentence(u.Total, u.Security)
			items = append(items, it)
		}
	}
	return items
}

// rebootSentence is the reboot item's words. With a kernel installed that is
// newer than the one that runs: "This server needs a reboot: kernel 6.8.0-149
// is installed, but 6.8.0-147 is still running." Otherwise it names what asked
// for the reboot: "This server needs a reboot: libc6 was updated and needs a
// reboot."; with no names, only the first part.
func rebootSentence(it AttentionItem) string {
	head := "This server needs a reboot"
	if it.Server != "" { // the cluster has more than one server: say which
		head = it.Server + " needs a reboot"
	}
	if it.InstalledKernel != "" && it.RunningKernel != "" && hostos.KernelNewer(it.InstalledKernel, it.RunningKernel) {
		return fmt.Sprintf("%s: kernel %s is installed, but %s is still running.", head,
			kernelShort(it.InstalledKernel), kernelShort(it.RunningKernel))
	}
	if what := rebootWhat(it.Packages); what != "" {
		return head + ": " + what
	}
	return head + "."
}

// rebootWhat is "libc6 was updated and needs a reboot.", with up to three names
// and a count of the rest, or "" for none.
func rebootWhat(pkgs []string) string {
	switch n := len(pkgs); {
	case n == 0:
		return ""
	case n == 1:
		return pkgs[0] + " was updated and needs a reboot."
	case n == 2:
		return pkgs[0] + " and " + pkgs[1] + " were updated and need a reboot."
	case n == 3:
		return pkgs[0] + ", " + pkgs[1] + " and " + pkgs[2] + " were updated and need a reboot."
	default:
		return fmt.Sprintf("%s, %s, %s and %d more were updated and need a reboot.", pkgs[0], pkgs[1], pkgs[2], n-3)
	}
}

// securitySentence is "14 updates are waiting, 9 of them security updates." (one
// update: "1 update is waiting, and it is a security update."; all of them:
// "9 updates are waiting, all of them security updates.").
func securitySentence(total, security int) string {
	total = max(total, security)
	switch {
	case total == 1:
		return "1 update is waiting, and it is a security update."
	case security == total:
		return fmt.Sprintf("%d updates are waiting, all of them security updates.", total)
	case security == 1:
		return fmt.Sprintf("%d updates are waiting, 1 of them a security update.", total)
	}
	return fmt.Sprintf("%d updates are waiting, %d of them security updates.", total, security)
}

// osTitle is the short title of a notification about an OS item.
func osTitle(it AttentionItem) string {
	title := "Security updates waiting"
	if it.Kind == AttentionOSRebootRequired {
		title = "Reboot needed"
	}
	if it.Server != "" {
		title += " on " + it.Server
	}
	return title
}

// securityDetail is the security item's sentence, with the server's name first when the cluster has more than
// one server: "fsn-1: 14 updates are waiting, 9 of them security updates."
func securityDetail(it AttentionItem) string {
	text := securitySentence(it.Updates, it.Security)
	if it.Server != "" {
		return it.Server + ": " + text
	}
	return text
}

// nameOSItems says which server the OS items are about, once the cluster has more than one (feature 031):
// "fsn-1 needs a reboot: …". The wording stays in one place, so the list, the pages, the feed and the
// notifications read the same. With one server the items keep their words, and their key never changes.
func nameOSItems(items []AttentionItem, server string) {
	for i := range items {
		it := &items[i]
		if it.Kind != AttentionOSRebootRequired && it.Kind != AttentionOSSecurityUpdates {
			continue
		}
		it.Server = server
		if it.Kind == AttentionOSRebootRequired {
			it.Detail = rebootSentence(*it)
		} else {
			it.Detail = securityDetail(*it)
		}
	}
}

// rebootParts and securityParts are the sentences in parts for the activity feed
// (the names are the mono ones).
func rebootParts(it AttentionItem) []TextPart {
	var lead []any // "This server" or the server's name in mono
	head := " needs a reboot"
	if it.Server != "" {
		lead = []any{mono(it.Server)}
	} else {
		head = "This server needs a reboot"
	}
	with := func(rest ...any) []TextPart { return parts(append(append(lead, head), rest...)...) }
	if it.InstalledKernel != "" && it.RunningKernel != "" && hostos.KernelNewer(it.InstalledKernel, it.RunningKernel) {
		return with(": kernel ", mono(kernelShort(it.InstalledKernel)), " is installed, but ",
			mono(kernelShort(it.RunningKernel)), " is still running.")
	}
	n := len(it.Packages)
	if n == 0 {
		return with(".")
	}
	shown := it.Packages[:min(n, 3)]
	out := append(append([]any{}, lead...), head+": ")
	for i, p := range shown {
		switch {
		case i > 0 && i == len(shown)-1 && n <= 3:
			out = append(out, " and ")
		case i > 0:
			out = append(out, ", ")
		}
		out = append(out, mono(p))
	}
	switch {
	case n > 3:
		out = append(out, fmt.Sprintf(" and %d more were updated and need a reboot.", n-3))
	case n == 1:
		out = append(out, " was updated and needs a reboot.")
	default:
		out = append(out, " were updated and need a reboot.")
	}
	return parts(out...)
}
