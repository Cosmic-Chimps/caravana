package ui

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// This file turns what the service knows into what the pages say: sentences,
// status words, version labels, short IDs and relative times. Nothing here
// touches HTTP or a template, so view_test.go tests every rule with a table.

// part is a piece of a sentence. Mono marks what Nomad or the user's config
// produced (an app name, a version), which the pages set in the mono face
// (DESIGN.md, The Mono-Is-Real Rule).
type part struct {
	Text string
	Mono bool
}

// sentence is text the pages show as one line of prose, with its names and
// versions marked. The "sentence" template in parts.html draws one. (C#:
// think of it as a tiny rich-text string; html/template can't mix a plain
// string with styled pieces, so the pieces are kept apart until drawing.)
type sentence []part

func plain(text string) part { return part{Text: text} }
func mono(text string) part  { return part{Text: text, Mono: true} }

// plainS is plain text as a sentence of its own, for join.
func plainS(text string) sentence { return sentence{plain(text)} }

// join puts sentences one after another.
func join(parts ...sentence) sentence {
	var out sentence
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// String is the sentence as plain text, for tests and logs.
func (s sentence) String() string {
	var b strings.Builder
	for _, p := range s {
		b.WriteString(p.Text)
	}
	return b.String()
}

// when is a time as the page shows it: a relative phrase, with the exact time
// for the title attribute (hover).
type when struct {
	Text  string
	Exact string
	ISO   string // machine-readable, for <time datetime>; "" when there is no time
}

// whenOf describes t relative to now; a nil t is the given words.
func whenOf(now time.Time, t *time.Time, none string) when {
	if t == nil {
		return when{Text: none}
	}
	return when{Text: relative(now, *t), Exact: t.UTC().Format("2006-01-02 15:04:05 UTC"), ISO: t.UTC().Format(time.RFC3339)}
}

// relative is "5 minutes ago" or "in 3 days"; past two months, the date.
func relative(now, t time.Time) string {
	d := now.Sub(t)
	future := d < 0
	if future {
		d = -d
	}
	var text string
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		text = plural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		text = plural(int(d/time.Hour), "hour")
	case d < 60*24*time.Hour:
		text = plural(int(d/(24*time.Hour)), "day")
	default:
		return t.UTC().Format("2006-01-02")
	}
	if future {
		return "in " + text
	}
	return text + " ago"
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// copyCount is "1 copy" or "3 copies".
func copyCount(n int) string {
	if n == 1 {
		return "1 copy"
	}
	return fmt.Sprintf("%d copies", n)
}

// appCount is "1 app" or "3 apps".
func appCount(n int) string { return plural(n, "app") }

// versionLabel is a Nomad job version as the pages write it: v7. A nil
// version (nothing registered yet) has no label.
func versionLabel(v *uint64) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("v%d", *v)
}

// shortID is the first 8 characters of a Nomad ID. Pages show it and put the
// full value in a title attribute.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// shortDigest shortens an image reference pinned to a digest,
// "repo@sha256:<64 hex>", to "sha256:" and the first 12 hex digits. Anything
// that isn't pinned comes back as it is.
func shortDigest(ref string) string {
	_, digest, ok := strings.Cut(ref, "@")
	if !ok {
		return ref
	}
	algo, hex, ok := strings.Cut(digest, ":")
	if !ok || len(hex) <= 12 {
		return digest
	}
	return algo + ":" + hex[:12]
}

// triggerWords says what started something, in lowercase words that follow a
// lead: "from the console". deployWho builds on it for deployments, which also
// know their origin, token and promotion; the Recent actions list uses it alone.
func triggerWords(t app.Trigger) string {
	switch t {
	case app.TriggerConsole:
		return "from the console"
	case app.TriggerManual:
		return "through the API"
	case app.TriggerWebhook:
		return "by the webhook"
	}
	return "by " + string(t)
}

// The kinds the Apps home tells apart (feature 021, page 4). An image app is the
// plain case and has no word.
const (
	kindImage    = ""
	kindCompose  = "compose"
	kindDatabase = "database"
	kindVolume   = "volume"
)

// databaseImages are the image repositories that make an app a database: the
// last path segment of the image, before its tag or digest.
var databaseImages = []string{"postgres", "mysql", "mariadb", "redis", "valkey"}

// imageName is the last path segment of an image reference before its tag or
// digest, in lowercase: "ghcr.io/acme/postgres:16" and "postgres@sha256:..." are
// both "postgres".
func imageName(image string) string {
	ref, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	name, _, _ := strings.Cut(ref, ":")
	return strings.ToLower(name)
}

// kindOf is the word the Apps home puts after an app's name, and what its kind
// filter selects: the first rule that applies. compose is an app whose source is
// Compose or that keeps its Compose file; database is an app with a service
// backed up as Postgres or whose image is one of databaseImages; volume is an
// app with any volume; an image app is kindImage ("").
func kindOf(a app.App, keptFile bool) string {
	if a.Source.Kind == app.SourceCompose || keptFile {
		return kindCompose
	}
	for _, svc := range a.Services {
		if svc.Backup != nil && svc.Backup.Engine == app.BackupPostgres {
			return kindDatabase
		}
		if slices.Contains(databaseImages, imageName(svc.Image)) {
			return kindDatabase
		}
	}
	for _, svc := range a.Services {
		if len(svc.Volumes) > 0 {
			return kindVolume
		}
	}
	return kindImage
}

// statusRank orders kinds for The Problems-First Rule: failed first, idle last.
var statusRank = map[string]int{"failed": 4, "warning": 3, "rolling": 2, "healthy": 1, "idle": 0}

// appStatusView is an app's status as the Apps table and the app header show it.
// An app Nomad runs nothing of is a failure when something was deployed (it
// should be running), and just idle when nothing ever was. The app is also a
// failure ("deploy failed") when its newest deployment failed or was reverted
// but copies still run, so the list sorts it with the problems.
func appStatusView(sum service.AppSummary) statusView {
	st := sum.Status
	switch st.State {
	case service.StateStopped:
		return statusView{"idle", "stopped"}
	case service.StateRollingOut:
		return statusView{"rolling", "rolling out"}
	case service.StateHealthy, service.StateDegraded:
		// Healthy copies don't hide a deploy that failed or was reverted: the app is
		// failed, and the old version that keeps running is in its summary.
		if deployEnded(sum) {
			return statusView{"failed", "deploy failed"}
		}
		if st.State == service.StateHealthy {
			return statusView{"healthy", "healthy"}
		}
		return statusView{"warning", fmt.Sprintf("%d of %d healthy", st.HealthyCopies, st.WantedCopies)}
	case service.StateNotRunning:
		switch {
		case !sum.App.Defined():
			return statusView{"idle", "not defined yet"}
		case sum.LastDeployment == nil:
			return statusView{"idle", "never deployed"}
		}
		return statusView{"failed", "not running"}
	}
	return statusView{"idle", "unknown"}
}

// copyStatusView is a copy's status as the Running table shows it. A copy that
// fails its checks is a warning (it may still recover); one that crashed or
// whose node was lost is a failure; one that ended on purpose is idle.
func copyStatusView(s deploy.CopyStatus) statusView {
	switch s {
	case deploy.CopyStarting:
		return statusView{"rolling", "starting"}
	case deploy.CopyHealthy:
		return statusView{"healthy", "healthy"}
	case deploy.CopyUnhealthy:
		return statusView{"warning", "unhealthy"}
	case deploy.CopyFailed:
		return statusView{"failed", "failed"}
	}
	return statusView{"idle", "stopped"}
}

// failedDeploySentence says why an app with no copies has none, when its
// newest deployment ended badly: "v8 failed: see " (the page adds the link).
// A deployment that is still going, or that ended well or was replaced, is no
// reason, so the sentence is empty.
func failedDeploySentence(rec store.DeploymentRecord) sentence {
	var what string
	switch rec.Status {
	case store.StatusFailed:
		what = " failed: see "
	case store.StatusReverted:
		what = " was reverted: see "
	default:
		return nil
	}
	if rec.JobVersion == nil {
		return sentence{plain("The last deploy failed: see ")}
	}
	return sentence{mono(versionLabel(rec.JobVersion)), plain(what)}
}

// deploymentStatusView is a deployment's status as the activity and history show it.
// A reverted deployment failed, and Nomad put the old version back.
func deploymentStatusView(s store.Status) statusView {
	switch s {
	case store.StatusPending, store.StatusRunning:
		return statusView{"rolling", "rolling out"}
	case store.StatusHealthy:
		return statusView{"healthy", "healthy"}
	case store.StatusFailed:
		return statusView{"failed", "failed"}
	case store.StatusReverted:
		return statusView{"failed", "reverted"}
	case store.StatusSuperseded:
		return statusView{"idle", "superseded"}
	}
	return statusView{"idle", string(s)}
}

// deployOutcome is how a deployment ended, when that is worth a word beside
// the app's status: "v8 reverted". A deployment that is under way or
// healthy needs none, since the status already says so.
func deployOutcome(rec *store.DeploymentRecord) string {
	if rec == nil {
		return ""
	}
	switch rec.Status {
	case store.StatusFailed, store.StatusReverted, store.StatusSuperseded:
		word := deploymentStatusView(rec.Status).Word
		if v := versionLabel(rec.JobVersion); v != "" {
			return v + " " + word
		}
		return "deploy " + word
	}
	return ""
}

// needsYouOf says an app is one the headline names as needing the user: its
// status says so, its last deploy failed, or listed (the needs-you list has an
// item for it). The state filter's "Needs you" uses it, so both name the same apps.
func needsYouOf(sum service.AppSummary, listed bool) bool {
	switch sum.Status.State {
	case service.StateHealthy, service.StateRollingOut:
		return lastDeployFailed(sum) || listed
	case service.StateNotRunning:
		return !neverDeployed(sum)
	case service.StateDegraded:
		return true
	}
	return false
}

// headlineOf is the one sentence the Apps page opens with: how things stand
// right now (research R7). Apps that need a human come first, named; the rest
// are counted. An app needs a human when its status says so, or when the
// needs-you list has an item for it (items without an app, like a missing
// backup destination, are in the list but are no app's trouble).
func headlineOf(sums []service.AppSummary, items []service.AttentionItem) sentence {
	if len(sums) == 0 {
		return sentence{plain("No apps yet.")}
	}

	first := map[string]service.AttentionItem{} // each app's newest item
	for _, it := range items {
		if _, ok := first[it.AppID]; !ok && it.AppID != "" {
			first[it.AppID] = it
		}
	}

	type troubled struct{ id, words string }
	var healthy, rolling, waiting, stopped int
	var trouble []troubled
	for _, sum := range sums {
		item, listed := first[sum.App.ID]
		switch sum.Status.State {
		case service.StateHealthy, service.StateRollingOut:
			// Healthy copies don't clear a deploy that failed: that is still on the user.
			if lastDeployFailed(sum) {
				trouble = append(trouble, troubled{sum.App.ID, troubleWords(sum)})
			} else if listed {
				trouble = append(trouble, troubled{sum.App.ID, headlineWords(item)})
			} else if sum.Status.State == service.StateHealthy {
				healthy++
			} else {
				rolling++
			}
		case service.StateNotRunning:
			if neverDeployed(sum) {
				// Created without deploying: waiting for its first deploy is what the owner asked for.
				waiting++
			} else {
				trouble = append(trouble, troubled{sum.App.ID, troubleWords(sum)})
			}
		case service.StateDegraded:
			trouble = append(trouble, troubled{sum.App.ID, troubleWords(sum)})
		case service.StateStopped:
			// Stopped on purpose: not trouble, and nothing is expected of it.
			stopped++
		case service.StateUnknown:
			return sentence{plain("Nomad can't be reached; showing what caravana last recorded.")}
		}
	}

	var out sentence
	if len(trouble) == 1 {
		out = append(sentence{plain("1 app needs you: "), mono(trouble[0].id)}, plain(" "+trouble[0].words+"."))
	} else if len(trouble) > 1 {
		out = sentence{plain(fmt.Sprintf("%d apps need you: ", len(trouble)))}
		const shown = 3
		for i, t := range trouble[:min(len(trouble), shown)] {
			if i > 0 {
				out = append(out, plain(", "))
			}
			out = append(out, mono(t.id))
		}
		if len(trouble) > shown {
			out = append(out, plain(fmt.Sprintf(" and %d more", len(trouble)-shown)))
		}
		out = append(out, plain("."))
	}

	var rest []string
	if healthy > 0 {
		rest = append(rest, appCount(healthy)+" healthy")
	}
	if rolling > 0 {
		rest = append(rest, plural(rolling, "rollout")+" in progress")
	}
	if waiting > 0 {
		rest = append(rest, appCount(waiting)+" waiting for a first deploy")
	}
	if stopped > 0 {
		rest = append(rest, appCount(stopped)+" stopped")
	}
	if len(rest) > 0 {
		if len(out) > 0 {
			out = append(out, plain(" "))
		}
		out = append(out, plain(strings.Join(rest, ", ")+"."))
	}
	return out
}

// neverDeployed says whether the app was created and has no deployment at all:
// an app made without deploying it (feature 019.5) until its first deploy.
func neverDeployed(sum service.AppSummary) bool {
	return sum.LastDeployment == nil && sum.Status.State == service.StateNotRunning
}

// lastDeployFailed says whether the app's newest deployment failed (it never
// started, or its rollout failed). Reverted ones aren't counted: Nomad put the
// old version back, and what runs is what was running.
func lastDeployFailed(sum service.AppSummary) bool {
	return sum.LastDeployment != nil && sum.LastDeployment.Status == store.StatusFailed
}

// deployEnded says whether the app's newest deployment failed or was reverted.
// It is the condition of the needs-you list's deploy item, and of the "deploy
// failed" pill. (lastDeployFailed, which the Apps headline uses, leaves
// reverted ones out: the headline gets those from the list.)
func deployEnded(sum service.AppSummary) bool {
	d := sum.LastDeployment
	return d != nil && (d.Status == store.StatusFailed || d.Status == store.StatusReverted)
}

// runningNow is what Nomad says runs while a deployment's failure is told: the
// version of the healthy copies (nil for none), how many copies it reports that
// haven't ended and how many are wanted. Known is false when Nomad couldn't be read.
type runningNow struct {
	Version  *uint64
	Reported int
	Wanted   int
	Known    bool
	// Again is the deployment of the owner's that Version repeats after a revert
	// (repeatedVersion); nil says nothing.
	Again *uint64
}

// deployFailedSentence says a deployment failed, and what runs now: "v3 failed
// its health checks. v2 keeps running.", "... 0 of 1 copies healthy." when Nomad
// has copies and none is healthy, or "... No copies are running." when it has
// none. It is the one sentence the app's summary and the needs-you list share, so
// the pages never disagree. When Nomad couldn't be read the second sentence is left out.
//
// The first sentence says why as far as it is known, as the Deployments tab does: a
// reverted deployment failed its health checks, one that registered and failed
// went wrong during its rollout, and one that never registered didn't start.
func deployFailedSentence(rec store.DeploymentRecord, now runningNow) sentence {
	v := plain("This deploy")
	if rec.JobVersion != nil {
		v = mono(versionLabel(rec.JobVersion))
	}
	var out sentence
	switch {
	case rec.Status == store.StatusReverted:
		out = sentence{v, plain(" failed its health checks.")}
	case rec.JobVersion != nil:
		out = sentence{v, plain(" failed during its rollout.")}
	default:
		out = sentence{v, plain(" didn't start")}
		if cause := failureCause(rec.Error); cause != "" {
			out = append(out, plain(": "+cause))
		}
		out = append(out, plain("."))
	}
	switch {
	case !now.Known:
	case now.Version != nil:
		out = append(out, plain(" "), mono(versionLabel(now.Version)))
		out = append(out, againWords(now.Version, now.Again)...)
		out = append(out, plain(" keeps running."))
	case now.Reported > 0:
		out = append(out, plain(fmt.Sprintf(" 0 of %d copies healthy.", max(now.Wanted, now.Reported))))
	default:
		out = append(out, plain(" No copies are running."))
	}
	return out
}

// runningOf is what an app's status says runs now, for deployFailedSentence.
func runningOf(st service.AppStatus) runningNow {
	return runningNow{Version: st.LiveVersion, Reported: st.ReportedCopies, Wanted: st.WantedCopies,
		Known: st.State != service.StateUnknown}
}

// repeating is r with the deployment its version repeats after a revert.
func (r runningNow) repeating(again *uint64) runningNow {
	r.Again = again
	return r
}

// troubleWords says in a few words what is wrong with an app that needs the
// user, to follow its name: "isn't running", "has 1 of 2 copies healthy", "has a
// failed deploy". A failed deploy is added to the others.
func troubleWords(sum service.AppSummary) string {
	st := sum.Status
	var words string
	switch st.State {
	case service.StateDegraded:
		words = fmt.Sprintf("has %d of %d copies healthy", st.HealthyCopies, st.WantedCopies)
	case service.StateNotRunning:
		words = "isn't running"
	default:
		return "has a failed deploy"
	}
	if lastDeployFailed(sum) {
		words += ", and its last deploy failed"
	}
	return words
}

// copiesSentence says what runs now, for a healthy or degraded app, with the
// version in mono. One service is "2 copies running v7." (or, when short,
// "1 of 2 copies healthy, running v7."). Several services name each one, so
// "2 copies" never reads as two replicas of one thing:
//
//	2 services running v2: hollo 2 copies, postgres 1.
//	2 services running v2: hollo and postgres, 1 copy each.
//	2 services running v2: hollo 1 of 2 copies healthy, postgres 1.
//
// The noun is said once, by the first service; a short service always says
// its own ("1 of 2 copies healthy"). The counts are the healthy copies of the
// live version, as in AppStatus.
func copiesSentence(st service.AppStatus, live string) sentence {
	if len(st.Services) < 2 {
		if st.State == service.StateDegraded {
			return sentence{plain(fmt.Sprintf("%d of %d copies healthy, running ", st.HealthyCopies, st.WantedCopies)), mono(live), plain(".")}
		}
		out := sentence{plain(copyCount(st.HealthyCopies) + " running")}
		if live != "" {
			out = append(out, plain(" "), mono(live))
		}
		return append(out, plain("."))
	}

	same, short := true, false
	for _, svc := range st.Services {
		same = same && svc.Healthy == st.Services[0].Healthy
		short = short || svc.Healthy < svc.Wanted
	}
	var tail string
	if same && !short {
		names := make([]string, len(st.Services))
		for i, svc := range st.Services {
			names[i] = svc.Name
		}
		tail = nameList(names) + ", " + copyCount(st.Services[0].Healthy) + " each"
	} else {
		parts := make([]string, len(st.Services))
		for i, svc := range st.Services {
			switch {
			case svc.Healthy < svc.Wanted:
				parts[i] = fmt.Sprintf("%s %d of %s healthy", svc.Name, svc.Healthy, copyCount(svc.Wanted))
			case i == 0:
				parts[i] = svc.Name + " " + copyCount(svc.Healthy)
			default:
				parts[i] = fmt.Sprintf("%s %d", svc.Name, svc.Healthy)
			}
		}
		tail = strings.Join(parts, ", ")
	}
	out := sentence{plain(fmt.Sprintf("%d services running", len(st.Services)))}
	if live != "" {
		out = append(out, plain(" "), mono(live))
	}
	return append(out, plain(": "+tail+"."))
}

// nameList is names as a sentence's list: "a", "a and b", "a, b and c".
func nameList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// summaryOf is the app page's one-line summary: what runs now, and when and
// how it was last deployed ("2 copies running v7. Last deployed 3 minutes ago,
// from the console.").
func summaryOf(sum service.AppSummary, now time.Time) sentence {
	return summaryOfAgain(sum, now, nil)
}

// summaryOfAgain is summaryOf for an app whose newest deployment was reverted:
// again is the deployment of the owner's that the running version repeats
// ("v2 (v0 again) keeps running"), or nil.
func summaryOfAgain(sum service.AppSummary, now time.Time, again *uint64) sentence {
	st := sum.Status
	var out sentence
	live := versionLabel(st.LiveVersion)
	switch st.State {
	case service.StateStopped:
		out = sentence{plain("Stopped")}
		if sum.StoppedAt != nil {
			out = append(out, plain(" "+relative(now, *sum.StoppedAt)))
		}
		out = append(out, plain(". Start it to run what ran before."))
	case service.StateHealthy, service.StateDegraded:
		if deployEnded(sum) {
			out = deployFailedSentence(*sum.LastDeployment, runningOf(st).repeating(again))
		} else {
			out = copiesSentence(st, live)
		}
	case service.StateRollingOut:
		var next string
		if sum.LastDeployment != nil {
			next = versionLabel(sum.LastDeployment.JobVersion)
		}
		if next != "" {
			out = sentence{plain("Rolling out "), mono(next), plain(".")}
		} else {
			out = sentence{plain("Rolling out a new version.")}
		}
		if st.HealthyCopies > 0 && live != "" {
			out = append(out, plain(" "+copyCount(st.HealthyCopies)+" still run "), mono(live), plain("."))
		}
	case service.StateNotRunning:
		switch {
		case !sum.App.Defined() && sum.LastDeployment == nil:
			return sentence{plain("No definition yet, so nothing can be deployed.")}
		case neverDeployed(sum):
			return sentence{plain("Nothing is deployed yet.")}
		}
		switch {
		case deployEnded(sum):
			out = deployFailedSentence(*sum.LastDeployment, runningOf(st).repeating(again))
		case st.ReportedCopies > 0:
			// Nomad has copies and none is healthy: say so, not "none are running".
			out = sentence{plain(fmt.Sprintf("0 of %d copies healthy.", st.WantedCopies))}
		default:
			out = sentence{plain("No copies are running.")}
		}
	default:
		out = sentence{plain("Nomad can't be reached right now, so caravana can't say what's running.")}
	}

	if d := sum.LastDeployment; d != nil {
		// "Last deployed 26 minutes ago, promoted from staging." The same who-words as the rail
		// and the history entry, without the commit.
		out = append(out, plain(" Last deployed "+relative(now, d.CreatedAt)+", "))
		out = append(out, deployWho(*d, false)...)
		out = append(out, plain("."))
	} else {
		out = append(out, plain(" Never deployed."))
	}
	return out
}

// sortProblemsFirst orders rows failed, warning, rolling, healthy, idle, and
// by ID within a kind (DESIGN.md, The Problems-First Rule). The sort is
// stable, so a list that arrives sorted by ID stays so inside each kind.
func sortProblemsFirst[T any](rows []T, kind func(T) string) {
	slices.SortStableFunc(rows, func(a, b T) int { return statusRank[kind(b)] - statusRank[kind(a)] })
}

// durationText is a span as the pages write it: "42 s", "1 min 30 s", "1 h 2 min".
// Under a second, and anything negative (a clock that stepped back), is
// "under 1 s"; from an hour on the seconds are dropped.
func durationText(d time.Duration) string {
	secs := int(d / time.Second)
	switch {
	case secs < 1:
		return "under 1 s"
	case secs < 60:
		return fmt.Sprintf("%d s", secs)
	case secs < 3600:
		if secs%60 == 0 {
			return fmt.Sprintf("%d min", secs/60)
		}
		return fmt.Sprintf("%d min %d s", secs/60, secs%60)
	}
	mins := secs % 3600 / 60
	if mins == 0 {
		return fmt.Sprintf("%d h", secs/3600)
	}
	return fmt.Sprintf("%d h %d min", secs/3600, mins)
}

// deploymentTiming is how long a deployment took, or how long it has been
// going: "took 42 s", "running for 40 s".
func deploymentTiming(rec store.DeploymentRecord, now time.Time) string {
	switch rec.Status {
	case store.StatusPending, store.StatusRunning:
		return "running for " + durationText(now.Sub(rec.CreatedAt))
	}
	return "took " + durationText(rec.UpdatedAt.Sub(rec.CreatedAt))
}

// progressLine is a rollout's progress in a sentence (research R9). Old copies
// keep serving until the new ones pass their checks, which is what Nomad's
// update strategy guarantees (the deploys are blue/green); once the new
// copies are promoted, they serve and the old ones go.
func progressLine(p service.Progress) string {
	if p.Wanted == 0 && p.Placed == 0 && p.Healthy == 0 && p.Unhealthy == 0 {
		return "Nomad hasn't reported any copies for this rollout yet."
	}
	noun := "new copies"
	if p.Wanted == 1 {
		noun = "new copy"
	}
	line := fmt.Sprintf("%d of %d %s started, %d healthy", p.Placed, p.Wanted, noun, p.Healthy)
	if p.Unhealthy > 0 {
		line += fmt.Sprintf(", %d unhealthy", p.Unhealthy)
	}
	line += "."
	if p.CanariesWanted > 0 && p.Promoted {
		return line + " The new copies are serving; the old ones are being stopped."
	}
	return line + " Old copies keep serving until the new ones pass their checks."
}

// deployFacts is what a deployment's sentence needs to know about its
// neighbours in the history.
type deployFacts struct {
	// Current is true for the app's newest deployment. Only then does a
	// sentence say what is serving now ("v6 keeps serving"); for an older
	// entry the claim could be stale, so it is left out.
	Current bool
	// Previous is the job version of the newest healthy deployment before this one.
	Previous *uint64
	// ReplacedBy is the job version of the deployment that came after this one.
	ReplacedBy *uint64
	// Again is, for a reverted deployment, the version of the owner's that
	// Nomad's rollback put back (repeatedVersion); nil for the others and when
	// the history doesn't say.
	Again *uint64
	// Live is the version that runs now, set only for the newest deployment of
	// an app that was reverted: the rollback registered it as a new job version.
	Live *uint64
}

// deployFactsOf gives each entry of a page of the history (newest first) its
// facts. first says the page starts at the app's newest deployment.
func deployFactsOf(page []store.DeploymentRecord, first bool) []deployFacts {
	facts := make([]deployFacts, len(page))
	for i := range page {
		facts[i].Current = first && i == 0
		for _, older := range page[i+1:] {
			if older.Status == store.StatusHealthy && older.JobVersion != nil {
				facts[i].Previous = older.JobVersion
				break
			}
		}
		facts[i].Again = repeatedVersion(page[i], page[i+1:])
		for j := i - 1; j >= 0; j-- {
			if page[j].JobVersion != nil {
				facts[i].ReplacedBy = page[j].JobVersion
				break
			}
		}
	}
	return facts
}

// rolledBackTo finds the version Nomad names in a rollback message ("... -
// rolling back to job version 3").
var rolledBackTo = regexp.MustCompile(`rolling back to job version (\d+)`)

// failureCause says, in words, why a deploy never reached Nomad, from the
// error text the service stored. The texts come from the sentinel errors of
// deploy and registry, which every such message contains. "" means the
// cause isn't one of the known ones; the raw error is shown under the
// sentence either way.
func failureCause(errText string) string {
	// The registry's message carries the image's name in front ("ghcr.io: x:1: image not
	// found"), so these look for the phrase, not the start of the text.
	switch msg := strings.ToLower(errText); {
	case registryRefused(errText):
		return "the registry refused the stored credentials"
	case strings.Contains(msg, "image not found"):
		return "the image wasn't found"
	case strings.Contains(msg, "registry unavailable"):
		return "the registry couldn't be reached"
	case strings.Contains(msg, "nomad unavailable"):
		return "Nomad couldn't be reached"
	case strings.Contains(msg, "nomad rejected the job"):
		return "Nomad rejected the job"
	case foreignJob(errText):
		return "a job with this name already exists in Nomad and caravana didn't create it"
	case strings.Contains(msg, "job conflict"):
		return "the Nomad job changed while deploying"
	}
	return ""
}

// foreignJob says whether a stored error is the job conflict where Nomad
// already runs a job of that name which caravana didn't create. The other
// job conflict ("changed while deploying") is a race; the two need different
// words, and different fixes.
func foreignJob(errText string) bool {
	msg := strings.ToLower(errText)
	return strings.Contains(msg, "job conflict") && !strings.Contains(msg, "changed while deploying")
}

// actionLink is the one action a failed entry offers: a verb that says the
// outcome, and where it goes.
type actionLink struct {
	Label string
	Href  string
}

// deployFormHref is the deploy form on the Deployments tab.
const deployFormHref = "#deploy"

// registriesHref is Settings' Registries section, where the stored credentials
// are checked and fixed.
const registriesHref = "/settings/registries"

// registryRefused says whether a stored deploy error is the registry turning
// down the credentials Caravana sent (deploy.ErrRegistryAuth), which every such
// message carries. A missing image with credentials sent ("... or the stored
// credentials for X can't read it") is another error, about the image.
func registryRefused(errText string) bool {
	return strings.Contains(strings.ToLower(errText), "refused the stored credentials")
}

// failedAction is the action on a deployment that failed or was reverted, when
// it is the app's newest (an older one has nothing to fix: a newer deploy
// came after it). Every action leads to the deploy form on the same tab, except
// a registry that refused the credentials, whose fix is in Settings.
func failedAction(rec store.DeploymentRecord, f deployFacts) *actionLink {
	if !f.Current || (rec.Status != store.StatusFailed && rec.Status != store.StatusReverted) {
		return nil
	}
	if registryRefused(rec.Error) {
		return &actionLink{Label: "Check the registry's credentials", Href: registriesHref}
	}
	if strings.Contains(strings.ToLower(rec.Error), "image not found") {
		return &actionLink{Label: "Change the image", Href: deployFormHref}
	}
	return &actionLink{Label: "Deploy again", Href: deployFormHref}
}

// deploymentSentence explains where a deployment stands, in one sentence
// (data-model.md, Deployment). The raw error goes under it, set apart.
func deploymentSentence(rec store.DeploymentRecord, f deployFacts) sentence {
	// v is the deployment's version, or, before Nomad gave it one, "This deploy".
	v := plain("This deploy")
	if rec.JobVersion != nil {
		v = mono(versionLabel(rec.JobVersion))
	}
	// keeps ends a sentence with the version still serving, only when it is known to be true.
	keeps := func(out sentence) sentence {
		if f.Current && f.Previous != nil {
			out = append(out, plain(" "), mono(versionLabel(f.Previous)), plain(" keeps serving."))
		}
		return out
	}

	switch rec.Status {
	case store.StatusHealthy:
		if f.Current {
			return sentence{v, plain(" is live.")}
		}
		return sentence{v, plain(" rolled out healthy.")}

	case store.StatusReverted:
		// The version Nomad's message names, else the last healthy one. For the newest
		// deployment the version that runs now is the one the rollback registered, so
		// it is the one said, with the deployment of the owner's it repeats.
		target, shown := plain("the previous version"), (*uint64)(nil)
		switch n := revertTarget(rec.Error); {
		case f.Live != nil:
			shown = f.Live
		case n != nil:
			shown = n
		case f.Previous != nil:
			shown = f.Previous
		}
		if shown != nil {
			target = mono(versionLabel(shown))
		}
		out := sentence{v, plain(" failed its health checks. Nomad rolled back to "), target}
		out = append(out, againWords(shown, f.Again)...)
		if f.Current {
			return append(out, plain(", which is serving."))
		}
		return append(out, plain("."))

	case store.StatusFailed:
		if rec.JobVersion == nil {
			if foreignJob(rec.Error) {
				// Nothing was registered, so whatever runs under that name is untouched.
				return sentence{v, plain(" didn't start: a job named "), mono(rec.AppID),
					plain(" already exists in Nomad and caravana didn't create it. The running version, if any, was left alone. Stop that job in Nomad, then deploy again.")}
			}
			out := sentence{v, plain(" didn't start")}
			if cause := failureCause(rec.Error); cause != "" {
				out = append(out, plain(": "+cause))
			}
			return keeps(append(out, plain(".")))
		}
		return keeps(sentence{v, plain(" failed during its rollout.")})

	case store.StatusSuperseded:
		if rec.JobVersion == nil {
			return sentence{v, plain(" was replaced by a newer one before it started.")}
		}
		if f.ReplacedBy != nil {
			return sentence{v, plain(" was replaced by "), mono(versionLabel(f.ReplacedBy)), plain(" before it finished.")}
		}
		return sentence{v, plain(" was replaced by a newer deploy before it finished.")}

	case store.StatusRunning, store.StatusPending:
		if rec.JobVersion == nil {
			return sentence{plain("Rolling out a new version.")}
		}
		return sentence{plain("Rolling out "), v, plain(".")}
	}
	return sentence{plain(string(rec.Status))}
}

// clockTime is a moment as the pages write a clock time (feature 022's time
// rule): UTC first and named, "14:15 UTC", with the console's relative-day
// words computed in UTC on the server ("tomorrow 02:00 UTC", "in 2 d · Sun
// 03:00 UTC", "yesterday 14:00 UTC"). The "clock" template in parts.html draws
// it as <time datetime="…Z" class="cv-clock">; static/js/localtime.js then adds
// the viewer's local time beside it, in the shape Local names. Without script
// the UTC text stands alone and is right.
type clockTime struct {
	Text  string // "tomorrow 02:00 UTC"
	ISO   string // "2026-10-24T02:00:00Z", for datetime
	Title string // "2026-10-24 02:00:00 UTC", for the hover
	// Local is the shape of the local time localtime.js adds: "time" (14:15),
	// "time-s" (14:15:02), "weekday" (Sun 14:15) or "date" (2026-10-24 14:15).
	Local string
}

// The shapes of a clock's local time (clockTime.Local).
const (
	localTime    = "time"
	localSeconds = "time-s"
	localWeekday = "weekday"
	localDate    = "date"
)

// clockOf is t as a clock time read from now: "14:15 UTC" today, "tomorrow
// 02:00 UTC", "yesterday 14:00 UTC", "in 2 d · Sun 03:00 UTC" or "3 d ago · Tue
// 14:00 UTC" within a week, and the date beyond it ("2026-10-12 03:00 UTC").
func clockOf(now, t time.Time) clockTime {
	return clockWith(now, t, "15:04", false)
}

// clockSecondsOf is clockOf with the seconds: "14:00:02 UTC".
func clockSecondsOf(now, t time.Time) clockTime {
	return clockWith(now, t, "15:04:05", false)
}

// clockDateOf is clockOf for a record's own time (the run page's title): the
// same words, and the viewer's local time with its date.
func clockDateOf(now, t time.Time) clockTime {
	c := clockOf(now, t)
	c.Local = localDate
	return c
}

// clockCompactOf is one time of a list of next runs ("14:15", "tomorrow
// 03:00", "Sun 03:00", "2026-11-01 06:00"): no " UTC" (the list says it once,
// after its last time) and no "in N d ·", which would read as the list's own
// separator.
func clockCompactOf(now, t time.Time) clockTime {
	return clockWith(now, t, "15:04", true)
}

// clockAt is a clock time whose text is already written, such as the Live
// stage's "14:03:05 UTC": only the element and the local shape are added.
func clockAt(t time.Time, text, local string) clockTime {
	u := t.UTC()
	return clockTime{Text: text, ISO: u.Format(time.RFC3339), Title: u.Format("2006-01-02 15:04:05 UTC"), Local: local}
}

func clockWith(now, t time.Time, layout string, compact bool) clockTime {
	u := t.UTC()
	prefix, local := dayWords(now, u, compact)
	text := prefix + u.Format(layout)
	if !compact {
		text += " UTC"
	}
	if local == localTime && layout == "15:04:05" {
		local = localSeconds
	}
	return clockTime{Text: text, ISO: u.Format(time.RFC3339), Title: u.Format("2006-01-02 15:04:05 UTC"), Local: local}
}

// dayWords is what goes before a clock time's hour: nothing for today (in
// UTC), "tomorrow " or "yesterday ", the weekday within a week, else the
// date; and the shape of the local time that matches it.
func dayWords(now, t time.Time, compact bool) (string, string) {
	days := int(stripDay(t).Sub(stripDay(now)) / (24 * time.Hour))
	day := t.UTC().Format("Mon")
	switch {
	case days == 0:
		return "", localTime
	case days == 1:
		return "tomorrow ", localTime
	case days == -1:
		return "yesterday ", localTime
	case compact && days >= -6 && days <= 6:
		return day + " ", localWeekday
	case days >= 2 && days <= 6:
		return fmt.Sprintf("in %d d · %s ", days, day), localWeekday
	case days <= -2 && days >= -6:
		return fmt.Sprintf("%d d ago · %s ", -days, day), localWeekday
	}
	return t.UTC().Format("2006-01-02") + " ", localDate
}
