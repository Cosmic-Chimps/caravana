package service

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The live activity feed (feature 027, page 1): what happened on the host and
// to the apps on it, in Caravana's words, read from the records Caravana keeps
// plus the node's own events from Nomad's node record. The store gives plain
// rows; this file is the words table. No entry holds command output or a
// command's arguments (an action's record never had them) and no secret value.

// The kinds of an activity entry.
const (
	ActivityDeploy   = "deploy"
	ActivityRestart  = "restart"
	ActivityStop     = "stop"
	ActivityStart    = "start"
	ActivityCommand  = "command"
	ActivityBackup   = "backup"
	ActivityRestore  = "restore"
	ActivitySchedule = "schedule"
	ActivityNeedsYou = "needs you"
	ActivityServer   = "server"
)

// The marks of an activity entry, the console's status kinds.
const (
	ActivityHealthy = "healthy"
	ActivityWarning = "warning"
	ActivityFailed  = "failed"
	ActivityIdle    = "idle"
	// ActivityRolling is a deployment that is rolling out: the console's ◐, not the ○ of
	// something stopped.
	ActivityRolling = "rolling"
)

// Limits of one read of the feed.
const (
	DefaultActivityLimit = 8
	MaxActivityLimit     = 50
)

// ActivityQuery says what to read: the newest Limit entries (1-50, default 8),
// only those older than Before when it is set, and with Server only the entries
// about the server and its copies.
type ActivityQuery struct {
	Limit  int
	Before time.Time
	Server bool
	// ForServer, with Server, keeps only what is about that server (feature 031): the server's own host events
	// and update runs when Local is set (they are of the server Caravana runs on), and the problems whose item
	// is about it (its disk, its certificate, it being down). Another server's problems are left out.
	ForServer string
	Local     bool
}

// ActivityEntry is one line of the feed.
type ActivityEntry struct {
	// ID is stable for the entry (its source, row and phase), so a page can swap
	// the entry in place.
	ID   string
	Time time.Time
	Kind string
	// Status is the mark: healthy, warning, failed, rolling or idle.
	Status string
	// Parts is the entry in Caravana's words as pieces, the names (program, app,
	// service, schedule, version) marked mono so that a page sets them in its mono
	// face. Sentence is the same words as plain text.
	Parts    []TextPart
	Sentence string
	// AppID and AppName name the app the entry is about; empty for the host's own.
	AppID, AppName string
	Place          app.Place
}

// Activity returns the feed, newest first.
func (s *Service) Activity(ctx context.Context, q ActivityQuery) ([]ActivityEntry, error) {
	limit := q.Limit
	switch {
	case limit < 1:
		limit = DefaultActivityLimit
	case limit > MaxActivityLimit:
		limit = MaxActivityLimit
	}
	fetch := limit
	if q.ForServer != "" {
		fetch = min(limit*4, MaxActivityLimit*4) // some rows will be another server's
	}
	rows, err := s.store.ListActivity(ctx, fetch, q.Before, q.Server)
	if err != nil {
		return nil, fmt.Errorf("read activity: %w", err)
	}
	if q.ForServer != "" {
		rows = slices.DeleteFunc(rows, func(r store.ActivityRow) bool { return !rowIsOfServer(r, q.ForServer, q.Local) })
	}
	var entries []ActivityEntry
	display := s.projectDisplay(ctx)
	for _, r := range rows {
		if e, ok := s.activityEntry(ctx, r, display); ok {
			entries = append(entries, e)
		}
	}
	if node := s.hostNode(); node != nil {
		for _, ev := range node.Events {
			if !q.Before.IsZero() && !ev.Time.Before(q.Before) {
				continue
			}
			entries = append(entries, nodeEventEntry(ev))
		}
	}
	slices.SortStableFunc(entries, func(a, b ActivityEntry) int {
		if c := b.Time.Compare(a.Time); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// activityEntry turns a stored row into an entry. ok is false for a row of a
// source this file doesn't know. display says a project's name for people.
func (s *Service) activityEntry(ctx context.Context, r store.ActivityRow, display func(string) string) (ActivityEntry, bool) {
	e := ActivityEntry{
		ID:   r.Source + "." + r.RowID + "." + r.Phase,
		Time: r.At.UTC(), AppID: r.AppID, Place: r.Place,
		AppName: cmp.Or(r.Place.Name, r.AppID),
	}
	// The app as the needs-you list says it: its name at home, else "Shop › staging › web".
	name := labelParts(display, r.Place, e.AppName, "")
	switch r.Source {
	case store.ActivityDeployment:
		e.Kind = ActivityDeploy
		e.Status, e.Parts = s.deployWords(ctx, r, name)
	case store.ActivityAction:
		e.Kind, e.Status, e.Parts = actionWords(r, name)
	case store.ActivityBackup:
		e.Kind = ActivityBackup
		e.Status, e.Parts = runWords(r.Status, name, "'s backup of ", r.Service)
	case store.ActivityRestore:
		e.Kind = ActivityRestore
		e.Status, e.Parts = runWords(r.Status, name, "'s restore of ", r.Service)
	case store.ActivitySchedule:
		e.Kind = ActivitySchedule
		e.Status, e.Parts = scheduleWords(r, name)
	case store.ActivityProblem:
		e.Kind = ActivityNeedsYou
		e.Status, e.Parts = problemWords(r)
	case store.ActivityHost:
		e.Kind = ActivityServer
		e.AppID, e.AppName = "", ""
		e.Status, e.Parts = hostEventWords(r)
	case store.ActivityUpdate:
		e.Kind = ActivityServer
		e.AppID, e.AppName = "", ""
		e.Status, e.Parts = updateRunWords(r)
	default:
		return ActivityEntry{}, false
	}
	e.Sentence = plainText(e.Parts)
	return e, true
}

// deployWords says a deployment's start or end. The who-words are the console's
// page 4 words, read from the deployment's own record (a deleted record falls
// back to how it was started).
func (s *Service) deployWords(ctx context.Context, r store.ActivityRow, name []TextPart) (status string, p []TextPart) {
	var version []TextPart
	if r.Version != nil {
		version = []TextPart{txt(" "), mono("v" + strconv.FormatInt(*r.Version, 10))}
	}
	who := "by " + r.Trigger
	errText := r.Error
	if rec, err := s.store.GetDeployment(ctx, r.RowID); err == nil {
		who = deployWhoWords(rec)
		errText = rec.Error
	}
	if r.Phase == "started" {
		return ActivityRolling, parts(name, version, " is rolling out, "+who+".")
	}
	switch store.Status(r.Status) {
	case store.StatusHealthy:
		return ActivityHealthy, parts(name, version, " is live, "+who+".")
	case store.StatusReverted:
		if n, ok := revertedToVersion(errText); ok {
			return ActivityFailed, parts(name, version, " failed its health checks and was rolled back to ", mono(fmt.Sprintf("v%d", n)), ".")
		}
		return ActivityFailed, parts(name, version, " failed its health checks and was rolled back.")
	case store.StatusFailed:
		if version == nil {
			return ActivityFailed, parts(name, "'s deploy failed before it started.")
		}
		return ActivityFailed, parts(name, version, " failed its health checks.")
	case store.StatusSuperseded:
		return ActivityIdle, parts(name, version, " was replaced by a newer deploy.")
	}
	return ActivityIdle, parts(name, version, " changed.")
}

// triggerLead says how an action was started, in words that follow a verb.
func triggerLead(trigger string) string {
	switch app.Trigger(trigger) {
	case app.TriggerConsole:
		return "from the console"
	case app.TriggerManual:
		return "through the API"
	case app.TriggerWebhook:
		return "by the webhook"
	}
	return "by " + trigger
}

// actionWords says an app action. A command names its program only: the
// arguments and the output were never recorded.
func actionWords(r store.ActivityRow, name []TextPart) (kind, status string, p []TextPart) {
	how := triggerLead(r.Trigger)
	state := func(done, doing, failed string) (string, []TextPart) {
		switch r.Status {
		case store.ActionFailed:
			return ActivityFailed, parts(name, " "+failed+".")
		case store.ActionRunning:
			return ActivityIdle, parts(name, " "+doing+" "+how+".")
		}
		return ActivityHealthy, parts(name, " "+done+" "+how+".")
	}
	switch r.Label {
	case store.ActionRestart:
		status, p = state("was restarted", "is being restarted", "could not be restarted")
		return ActivityRestart, status, p
	case store.ActionStop:
		status, p = state("was stopped", "is being stopped", "could not be stopped")
		return ActivityStop, status, p
	case store.ActionStart:
		status, p = state("was started", "is being started", "could not be started")
		return ActivityStart, status, p
	case store.ActionTerminal:
		sh := mono(cmp.Or(r.Program, "a shell"))
		if r.Program == "" {
			sh = txt("a shell")
		}
		if r.Status == store.ActionFailed {
			return ActivityCommand, ActivityWarning, parts("A terminal (", sh, ") in ", name, " ended with an error.")
		}
		return ActivityCommand, ActivityIdle, parts("A terminal (", sh, ") was opened in ", name, " "+how+".")
	}
	// A run.
	prog := mono(cmp.Or(r.Program, "a command"))
	if r.Program == "" {
		prog = txt("a command")
	}
	switch {
	case r.Status == store.ActionRunning:
		return ActivityCommand, ActivityIdle, parts(prog, " is running in ", name, " "+how+".")
	case r.ExitCode != nil && *r.ExitCode == 0:
		return ActivityCommand, ActivityHealthy, parts(prog, " ran in ", name, " "+how+", exit 0.")
	case r.ExitCode != nil:
		return ActivityCommand, ActivityWarning, parts(prog, " ran in ", name, " "+how+", exit "+strconv.Itoa(*r.ExitCode)+".")
	}
	return ActivityCommand, ActivityFailed, parts(prog, " didn't finish in ", name, " "+how+".")
}

// runWords says the outcome of a backup or restore: "<app>'s backup of <service> finished."
func runWords(status string, name []TextPart, link, service string) (string, []TextPart) {
	subject := mono(service)
	if service == "" {
		subject = txt("the app")
	}
	switch status {
	case store.BackupSucceeded:
		return ActivityHealthy, parts(name, link, subject, " finished.")
	case store.BackupFailed:
		return ActivityFailed, parts(name, link, subject, " failed.")
	}
	return ActivityIdle, parts(name, link, subject, " is running.")
}

// scheduleWords says a schedule run that failed or was stopped at its limit.
func scheduleWords(r store.ActivityRow, name []TextPart) (string, []TextPart) {
	sched := parts("Schedule ", mono(cmp.Or(r.Label, "run")))
	if r.Label == "" {
		sched = parts("Schedule run")
	}
	if r.Status == store.RunStoppedAtLimit {
		return ActivityWarning, parts(sched, " was stopped at its time limit in ", name, ".")
	}
	if r.ExitCode != nil {
		return ActivityFailed, parts(sched, " failed in ", name, ", exit "+strconv.Itoa(*r.ExitCode)+".")
	}
	return ActivityFailed, parts(sched, " failed in ", name, ".")
}

// problemWords says a change of the needs-you list with the needs-you list's own
// sentence, stored as parts when the event was written (the title that was sent
// for an event written before that). A resolved one is "Resolved: " and the sentence.
func problemWords(r store.ActivityRow) (string, []TextPart) {
	sentence, ok := decodeParts(r.Detail)
	if !ok {
		sentence = []TextPart{txt(strings.ReplaceAll(r.Label, "_", " "))}
	}
	switch r.Status {
	case store.ProblemResolved:
		return ActivityHealthy, parts("Resolved: ", sentence)
	case store.ProblemWorsened:
		return ActivityFailed, sentence
	}
	return ActivityWarning, sentence
}

// hostEventWords says a host event: a boot, a Caravana version change, a run of
// the OS's package manager. A version event is only in the feed when the version changed ("from" is
// set): the first one ever recorded is the baseline the next change is measured from, and a start
// on the same version is never recorded.
func hostEventWords(r store.ActivityRow) (string, []TextPart) {
	switch r.Label {
	case store.HostEventBoot:
		// The event's own time is the boot time: "The server started (up since 03:12 UTC)." (the page-2
		// contract). The row's time says the day.
		return ActivityIdle, parts("The server started (up since " + r.At.UTC().Format("15:04") + " UTC).")
	case store.HostEventVersion:
		var d struct{ From, To string }
		_ = json.Unmarshal([]byte(r.Detail), &d)
		switch {
		case d.To == "":
			return ActivityIdle, parts("Caravana's version changed.")
		case d.From == "":
			return ActivityIdle, parts("Caravana ", mono(d.To), " is running.") // not listed: the store leaves it out
		}
		return ActivityIdle, parts("Caravana was upgraded from ", mono(d.From), " to ", mono(d.To), ".")
	case store.HostEventOSPackages:
		var d osPackagesDetail
		_ = json.Unmarshal([]byte(r.Detail), &d)
		return osPackagesWords(d)
	}
	return ActivityIdle, parts("The server changed.")
}

// updateRunWords says an update of Caravana that finished (feature 030): the row's Label is the
// version it was for, Program the one it left, Error the updater's one-line reason and Trigger
// who asked.
func updateRunWords(r store.ActivityRow) (string, []TextPart) {
	to, from := r.Label, r.Program
	how := "by " + r.Trigger
	switch r.Trigger {
	case store.UpdateByConsole:
		how = "from the console"
	case store.UpdateByAPI:
		how = "through the API"
	case store.UpdateByAutomatic:
		how = "automatically"
	}
	reason := func() string {
		if x := strings.TrimRight(strings.TrimSpace(r.Error), "."); x != "" {
			return ": " + x + "."
		}
		return "."
	}
	switch r.Status {
	case store.UpdateUpdated:
		if from == "" {
			return ActivityHealthy, parts("caravana was updated to ", mono(to), " "+how+".")
		}
		return ActivityHealthy, parts("caravana was updated from ", mono(from), " to ", mono(to), " "+how+".")
	case store.UpdateRolledBack:
		if cause := RollbackCause(r.Error); cause != "" {
			return ActivityWarning, parts("The update to caravana ", mono(to), " was rolled back: "+cause)
		}
		return ActivityWarning, parts("The update to caravana ", mono(to), " was rolled back.")
	case store.UpdateFailed:
		return ActivityFailed, parts("The update to caravana ", mono(to), " failed"+reason())
	case store.UpdateRefused:
		return ActivityWarning, parts("The server's updater refused caravana ", mono(to), reason())
	}
	return ActivityWarning, parts("The update to caravana ", mono(to), " didn't finish"+reason())
}

// nodeEventEntry puts one of the node's events from Nomad in Caravana's words.
// The ID has the time to the nanosecond and the subsystem: Nomad gives events
// no identity of their own.
func nodeEventEntry(ev deploy.NodeEvent) ActivityEntry {
	msg := strings.ToLower(ev.Message)
	status, sentence := ActivityIdle, ""
	switch {
	case strings.Contains(msg, "heartbeat missed"):
		status, sentence = ActivityWarning, "The server missed its heartbeat: Nomad lost contact with it."
	case strings.Contains(msg, "re-registered"), strings.Contains(msg, "reregistered"):
		sentence = "The server registered with Nomad again."
	case strings.Contains(msg, "registered"):
		sentence = "The server registered with Nomad."
	case strings.Contains(msg, "drain complete"), strings.Contains(msg, "drain disabled"):
		sentence = "The server's drain ended."
	case strings.Contains(msg, "drain"):
		status, sentence = ActivityWarning, "The server started draining."
	case strings.Contains(msg, "eligib"):
		status, sentence = ActivityWarning, "The server's eligibility for new copies changed."
	default:
		sentence = "Nomad reported: " + strings.TrimRight(sentenceCase(ev.Message), ".") + "."
	}
	return ActivityEntry{
		ID:   fmt.Sprintf("node.%d.%s", ev.Time.UnixNano(), strings.ToLower(ev.Subsystem)),
		Time: ev.Time.UTC(), Kind: ActivityServer, Status: status, Parts: []TextPart{txt(sentence)}, Sentence: sentence,
	}
}

// sentenceCase upper-cases the first letter of s.
func sentenceCase(s string) string {
	s = strings.TrimSpace(s)
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// osPackagesWords says a run of the OS's package manager: "OS packages updated by Ubuntu's
// automatic updates: 14 packages." or "...by hand: 3 packages.", with ", a reboot is needed."
// when the run installed what the reboot marker names. A run that recorded no count (an event
// written without one) says only that packages were updated.
func osPackagesWords(d osPackagesDetail) (string, []TextPart) {
	by := "by hand"
	if d.By == "unattended" {
		by = "by Ubuntu's automatic updates"
	}
	out := "OS packages updated " + by
	if d.Packages > 0 {
		out += ": " + plural(d.Packages, "package")
	}
	if d.Reboot {
		return ActivityWarning, parts(out + ", a reboot is needed.")
	}
	return ActivityIdle, parts(out + ".")
}

// plural is "1 package" or "14 packages": the name with an s unless n is 1.
func plural(n int, name string) string {
	if n == 1 {
		return "1 " + name
	}
	return strconv.Itoa(n) + " " + name + "s"
}

// rowIsOfServer says a row of the server feed is about the server called name. local says it is the server
// Caravana runs on, whose host events, update runs and unnamed items (its disk when the cluster has one server,
// the OS kinds) are its own. A problem is about the server its key names: a remote disk's service place, a
// server kind's subject.
func rowIsOfServer(r store.ActivityRow, name string, local bool) bool {
	switch r.Source {
	case store.ActivityProblem:
		if on := problemServerOf(r.Program); on != "" {
			return on == name
		}
		return local
	case store.ActivityHost, store.ActivityUpdate:
		return local
	}
	return true
}

// problemServerOf is the server an item key names ("" for the server Caravana runs on, or no server): keys are
// kind|app|service|subject, and a disk of another server carries its name where the service goes (notifier.go).
func problemServerOf(key string) string {
	p := strings.Split(key, "|")
	if len(p) < 4 {
		return ""
	}
	switch AttentionKind(p[0]) {
	case AttentionDiskNearlyFull:
		return p[2]
	case AttentionServerDown, AttentionServerCertExpiring:
		return p[3]
	}
	return ""
}
