package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/edge"
	"github.com/cosmic-chimps/caravana/internal/registry"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The Settings nav and the count-or-mark rule (feature 021, page 3). Each entry
// has ONE function that decides what it says about itself: the nav reads its
// count and mark, and the page's rows read their marks, so the two never
// disagree. They are pure: the facts go in, an entryState comes out.

// The Settings entries, by the ID the pages pass to renderSettings.
const (
	entryAccount       = "account"
	entryRegistries    = "registries"
	entryBackupStorage = "backup-storage"
	entryNotifications = "notifications"
	entryCertificates  = "certificates"
	entryTokens        = "tokens"
)

// settingsEntry is one entry of the nav. Account's page is /settings itself.
type settingsEntry struct{ ID, Label, Href, Icon string }

// settingsGroups are the nav's groups, in order. A group with no entries isn't
// drawn; teams and the rest arrive with their features (Phases 3 and 4).
var settingsGroups = []struct {
	Title   string // "" for the first block
	Entries []settingsEntry
}{
	{"", []settingsEntry{{entryAccount, "Account", "/settings", "user"}}},
	{"Connections", []settingsEntry{
		{entryRegistries, "Registries", "/settings/registries", "package"},
		{entryBackupStorage, "Backup storage", "/settings/backup-storage", "hard-drive"},
		{entryCertificates, "Certificates", "/settings/certificates", "lock"},
		{entryNotifications, "Notifications", "/settings/notifications", "bell"},
	}},
	{"Security", []settingsEntry{{entryTokens, "API tokens", "/settings/tokens", "key"}}},
}

// settingsLabel is an entry's name, for the crumb and the title.
func settingsLabel(id string) string {
	for _, g := range settingsGroups {
		for _, e := range g.Entries {
			if e.ID == id {
				return e.Label
			}
		}
	}
	return ""
}

// entryState is what one entry says about itself on every render. Count is the
// nav's number when Mark is "" (0 draws nothing). Mark is "warning" or "failed"
// when something is breaking, and wins over the count; Why says what, for a
// screen reader (a failure first, and both when both hold). Rows are the marks of
// the entry's rows, in the order they were given (healthy, warning, failed or
// idle). Rows mark more than the nav: a state the owner chose (a paused channel)
// or one that is chronic (a full token that never expires) marks its row and has
// a note there, but doesn't call the owner from the nav.
type entryState struct {
	Count int
	Mark  string
	Why   string
	Rows  []string
}

// registriesState: a registry's mark is ■ when its last test was refused, ●
// when it passed and ○ when it was never tested or its credentials changed since
// (the service forgets the last test when a save changes them); the nav shows ■
// when any is refused, else the number of registries.
func registriesState(regs []store.Registry) entryState {
	st := entryState{Count: len(regs), Rows: make([]string, len(regs))}
	for i, r := range regs {
		switch {
		case r.LastTest == nil:
			st.Rows[i] = "idle"
		case r.LastTest.OK:
			st.Rows[i] = "healthy"
		default:
			st.Rows[i] = "failed"
			st.Mark, st.Why = "failed", "a registry refused its credentials"
		}
	}
	return st
}

// backupFacts is what the backup destination's state is decided from: whether a
// destination is set, the newest backup run that ended (nil for none) and how
// many services have a backup policy.
type backupFacts struct {
	Destination bool
	Last        *store.BackupRecord
	// Failing is the newest of the services' newest runs that failed (nil for none): it can be
	// another service's than Last, when a later run of that one went well.
	Failing  *store.BackupRecord
	Policies int
}

// backupStorageState: the destination's row is ● when the last run that used it
// succeeded, ■ when it failed and ○ when none ran, and ◆ when that run went well
// but some service's own newest run failed. The nav, which has no count (there is
// one destination), follows the row, and shows ◆ when no destination is set while
// a service has a policy.
func backupStorageState(f backupFacts) entryState {
	var st entryState
	if !f.Destination {
		if f.Policies > 0 {
			st.Mark, st.Why = "warning", "backups have nowhere to go"
		}
		return st
	}
	switch {
	case f.Last == nil:
		st.Rows = []string{"idle"}
	case f.Last.Status != store.BackupSucceeded:
		st.Rows = []string{"failed"}
		st.Mark, st.Why = "failed", "the last backup failed"
	case f.Failing != nil:
		st.Rows = []string{"warning"}
		st.Mark, st.Why = "warning", "a service's last backup failed"
	default:
		st.Rows = []string{"healthy"}
	}
	return st
}

// channelFacts is what a channel's state is decided from: paused, and its last
// delivery that ended (nil for none yet).
type channelFacts struct {
	Paused bool
	Last   *lastDelivery
}

// notificationsState: a channel's mark is ◆ when its last delivery failed or it
// is paused, ○ when nothing was sent yet and ● otherwise; the nav shows ◆ only
// when a delivery failed (a paused channel was the owner's choice), else the
// number of channels (paused ones included).
func notificationsState(chs []channelFacts) entryState {
	st := entryState{Count: len(chs), Rows: make([]string, len(chs))}
	for i, c := range chs {
		failed := c.Last != nil && !c.Last.OK
		switch {
		case c.Paused || failed:
			st.Rows[i] = "warning"
		case c.Last == nil:
			st.Rows[i] = "idle"
		default:
			st.Rows[i] = "healthy"
		}
		if failed {
			st.Mark, st.Why = "warning", "a channel's last delivery failed"
		}
	}
	return st
}

// certRowMark is a certificate's mark: ● issued, ◐ pending, ◆ expiring, and ○
// Traefik's own, a failed handshake or anything else.
func certRowMark(status string) string {
	switch status {
	case edge.CertIssued:
		return "healthy"
	case edge.CertPending:
		return "rolling"
	case edge.CertExpiring:
		return "warning"
	}
	return "idle"
}

// certificatesState: a certificate's mark is certRowMark's; the nav shows ◆ when
// any certificate is expiring (it wins), else the number of certificates issued
// (Traefik's own and pending ones aren't counted). Rows follow the order given.
func certificatesState(certs []service.CertificateView) entryState {
	st := entryState{Rows: make([]string, len(certs))}
	for i, c := range certs {
		st.Rows[i] = certRowMark(c.Status)
		switch c.Status {
		case edge.CertIssued:
			st.Count++
		case edge.CertExpiring:
			st.Mark, st.Why = "warning", "a certificate is expiring"
		}
	}
	return st
}

// staleTokenDays is how long an active token may go unused before it is flagged.
const staleTokenDays = 90

// tokenNote is a sentence under a token's row. Link, when set, is a phrase of the
// sentence that links to the New token form ("Make one"), Before and After being
// the text around it.
type tokenNote struct {
	Kind          string // noexpiry or unused
	Before, After string
	Link          string
}

// Text is the note as a plain sentence.
func (n tokenNote) Text() string { return n.Before + n.Link + n.After }

// tokenNotes says what is wrong with a token, in the sentences the row shows
// under it: an active full token with no expiry (chronic: it marks its row, not
// the nav), and an active token unused for more than 90 days (a token never used
// is counted from when it was made, so a fresh token is never flagged). A token
// that isn't active has none. A row is marked ◆ exactly when this has a sentence.
func tokenNotes(now time.Time, t service.APITokenInfo) []tokenNote {
	if t.State != service.TokenActive {
		return nil
	}
	var notes []tokenNote
	if t.Scope == nil && t.ExpiresAt == nil {
		notes = append(notes, tokenNote{Kind: "noexpiry", Before: "A token that can do everything and never expires. ", Link: "Make one",
			After: " with an end date, or one that only deploys an app, then revoke this one."})
	}
	since := t.CreatedAt
	if t.LastUsedAt != nil {
		since = *t.LastUsedAt
	}
	if days := int(now.Sub(since) / (24 * time.Hour)); days > staleTokenDays {
		notes = append(notes, tokenNote{Kind: "unused", Before: fmt.Sprintf("Not used in %d days. Revoke it if nothing needs it.", days)})
	}
	return notes
}

// tokensState: a token's mark is ◆ when it is active and tokenNotes has
// something to say, ○ when it expired or was revoked and ● otherwise; the nav
// shows ◆ only when an active token went unused for 90 days (a full token
// without an end date is chronic, and noted on its row), else the number of
// active tokens.
func tokensState(now time.Time, tokens []service.APITokenInfo) entryState {
	st := entryState{Rows: make([]string, len(tokens))}
	for i, t := range tokens {
		if t.State != service.TokenActive {
			st.Rows[i] = "idle"
			continue
		}
		st.Count++
		notes := tokenNotes(now, t)
		st.Rows[i] = "healthy"
		if len(notes) > 0 {
			st.Rows[i] = "warning"
		}
		if slices.ContainsFunc(notes, func(n tokenNote) bool { return n.Kind == "unused" }) {
			st.Mark, st.Why = "warning", "a token went unused for 90 days"
		}
	}
	return st
}

// settingsFacts is everything the nav and the pages are decided from, read once
// per render.
type settingsFacts struct {
	Registries []store.Registry
	Channels   []service.NotificationChannel
	Deliveries map[string][]store.Delivery // by channel ID, newest first
	Tokens     []service.APITokenInfo
	Certs      []service.CertificateView // problems first
	Dest       *store.BackupDestination
	LastBackup *store.BackupRecord
	// FailingBackup is the newest of the services' newest runs that failed; see backupFacts.Failing.
	FailingBackup *store.BackupRecord
	Apps          []store.AppRecord
}

// lastOf is a channel's newest delivery that ended, as "Last delivery" says it.
func lastOf(now time.Time, deliveries []store.Delivery) *lastDelivery {
	for _, d := range deliveries {
		if d.Status == store.DeliveryPending {
			continue
		}
		at := d.CreatedAt
		if d.FinishedAt != nil {
			at = *d.FinishedAt
		}
		return &lastDelivery{OK: d.Status == store.DeliverySent, Kind: d.Status, When: whenOf(now, &at, ""), Error: d.Error}
	}
	return nil
}

// states derives every entry's state from the facts.
func (f settingsFacts) states(now time.Time) map[string]entryState {
	chs := make([]channelFacts, len(f.Channels))
	for i, c := range f.Channels {
		chs[i] = channelFacts{Paused: c.Paused, Last: lastOf(now, f.Deliveries[c.ID])}
	}
	return map[string]entryState{
		entryRegistries:    registriesState(f.Registries),
		entryBackupStorage: backupStorageState(backupFacts{Destination: f.Dest != nil, Last: f.LastBackup, Failing: f.FailingBackup, Policies: policyServices(f.Apps)}),
		entryNotifications: notificationsState(chs),
		entryCertificates:  certificatesState(f.Certs),
		entryTokens:        tokensState(now, f.Tokens),
	}
}

// policyServices is how many services, in every app, have a backup policy.
func policyServices(apps []store.AppRecord) int {
	n := 0
	for _, a := range apps {
		for _, svc := range a.App.Services {
			if svc.Backup != nil {
				n++
			}
		}
	}
	return n
}

// registryUse counts, by registry host, the apps with a service whose image is
// on it. An app counts once per host.
func registryUse(apps []store.AppRecord) map[string]int {
	use := map[string]int{}
	for _, a := range apps {
		seen := map[string]bool{}
		for _, svc := range a.App.Services {
			ref, err := registry.Parse(svc.Image)
			if err != nil || seen[ref.Registry] {
				continue
			}
			seen[ref.Registry] = true
			use[ref.Registry]++
		}
	}
	return use
}

// settingsNavItem is an entry as the nav draws it.
type settingsNavItem struct {
	Label, Href, Icon string
	Current           bool
	Count             int
	Mark, Why         string
}

// settingsNavGroup is a group of the nav.
type settingsNavGroup struct {
	Title string
	Items []settingsNavItem
}

// settingsNav builds the nav for the page on entry current. A mark wins over a
// count.
func settingsNav(current string, states map[string]entryState) []settingsNavGroup {
	var out []settingsNavGroup
	for _, g := range settingsGroups {
		group := settingsNavGroup{Title: g.Title}
		for _, e := range g.Entries {
			st := states[e.ID]
			item := settingsNavItem{Label: e.Label, Href: e.Href, Icon: e.Icon, Current: e.ID == current}
			if st.Mark != "" {
				item.Mark, item.Why = st.Mark, st.Why
			} else {
				item.Count = st.Count
			}
			group.Items = append(group.Items, item)
		}
		out = append(out, group)
	}
	return out
}

// markRank orders rows problems first: a failed mark, then a warning, then the rest.
func markRank(mark string) int {
	switch mark {
	case "failed":
		return 0
	case "warning":
		return 1
	}
	return 2
}

// sortStable orders rows by rank, lowest first, and keeps the order of rows of
// the same rank. (Go: a generic function, like C#'s Sort<T> with a key.)
func sortStable[T any](rows []T, rank func(T) int) {
	slices.SortStableFunc(rows, func(a, b T) int { return rank(a) - rank(b) })
}

// usedBy is "used by 3 apps" or "not used".
func usedBy(n int, unit string) string {
	if n == 0 {
		return "not used"
	}
	return "used by " + plural(n, unit)
}

// endpointName is a destination as the row names it: the endpoint without its
// scheme, then the bucket, then the prefix when there is one.
func endpointName(d store.BackupDestination) string {
	host := d.Endpoint
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	name := strings.TrimSuffix(host, "/") + "/" + d.Bucket
	if p := strings.Trim(d.Prefix, "/"); p != "" {
		name += "/" + p
	}
	return name
}

// backupWhen is when a backup ran, as the destination's row says it: the time
// for today and yesterday ("02:00 today"), else how long ago.
func backupWhen(now, t time.Time) string {
	y, m, d := now.UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	switch at := t.UTC(); {
	case !at.Before(today):
		return at.Format("15:04") + " today"
	case !at.Before(today.AddDate(0, 0, -1)):
		return at.Format("15:04") + " yesterday"
	}
	return relative(now, t)
}
