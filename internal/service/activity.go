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
}

// ActivityEntry is one line of the feed.
type ActivityEntry struct {
	// ID is stable for the entry (its source, row and phase), so a page can swap
	// the entry in place.
	ID   string
	Time time.Time
	Kind string
	// Status is the mark: healthy, warning, failed or idle.
	Status string
	// Sentence is the entry in Caravana's words, plain text.
	Sentence string
	// AppID, AppName and Place name the app the entry is about; empty for the
	// host's own. Sentence starts with AppName when the entry is about an app,
	// so a page can set it in mono.
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
	rows, err := s.store.ListActivity(ctx, limit, q.Before, q.Server)
	if err != nil {
		return nil, fmt.Errorf("read activity: %w", err)
	}
	var entries []ActivityEntry
	for _, r := range rows {
		if e, ok := s.activityEntry(ctx, r); ok {
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
// source this file doesn't know.
func (s *Service) activityEntry(ctx context.Context, r store.ActivityRow) (ActivityEntry, bool) {
	e := ActivityEntry{
		ID:   r.Source + "." + r.RowID + "." + r.Phase,
		Time: r.At.UTC(), AppID: r.AppID, Place: r.Place,
		AppName: cmp.Or(r.Place.Name, r.AppID),
	}
	name := e.AppName
	switch r.Source {
	case store.ActivityDeployment:
		e.Kind = ActivityDeploy
		e.Status, e.Sentence = s.deployWords(ctx, r, name)
	case store.ActivityAction:
		e.Kind, e.Status, e.Sentence = actionWords(r, name)
	case store.ActivityBackup:
		e.Kind = ActivityBackup
		e.Status, e.Sentence = runWords(r.Status, name+"'s backup of "+cmp.Or(r.Service, "the app"))
	case store.ActivityRestore:
		e.Kind = ActivityRestore
		e.Status, e.Sentence = runWords(r.Status, name+"'s restore of "+cmp.Or(r.Service, "the app"))
	case store.ActivitySchedule:
		e.Kind = ActivitySchedule
		e.Status, e.Sentence = scheduleWords(r, name)
	case store.ActivityProblem:
		e.Kind = ActivityNeedsYou
		e.Status, e.Sentence = problemWords(r)
	case store.ActivityHost:
		e.Kind = ActivityServer
		e.AppID, e.AppName = "", ""
		e.Status, e.Sentence = hostEventWords(r)
	default:
		return ActivityEntry{}, false
	}
	return e, true
}

// deployWords says a deployment's start or end. The who-words are the console's
// page 4 words, read from the deployment's own record (a deleted record falls
// back to how it was started).
func (s *Service) deployWords(ctx context.Context, r store.ActivityRow, name string) (status, sentence string) {
	version := ""
	if r.Version != nil {
		version = " v" + strconv.FormatInt(*r.Version, 10)
	}
	who := "by " + r.Trigger
	errText := r.Error
	if rec, err := s.store.GetDeployment(ctx, r.RowID); err == nil {
		who = deployWhoWords(rec)
		errText = rec.Error
	}
	if r.Phase == "started" {
		return ActivityIdle, name + version + " is rolling out, " + who + "."
	}
	switch store.Status(r.Status) {
	case store.StatusHealthy:
		return ActivityHealthy, name + version + " is live, " + who + "."
	case store.StatusReverted:
		if n, ok := revertedToVersion(errText); ok {
			return ActivityFailed, fmt.Sprintf("%s%s failed its health checks and was rolled back to v%d.", name, version, n)
		}
		return ActivityFailed, name + version + " failed its health checks and was rolled back."
	case store.StatusFailed:
		if version == "" {
			return ActivityFailed, name + "'s deploy failed before it started."
		}
		return ActivityFailed, name + version + " failed its health checks."
	case store.StatusSuperseded:
		return ActivityIdle, name + version + " was replaced by a newer deploy."
	}
	return ActivityIdle, name + version + " changed."
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
func actionWords(r store.ActivityRow, name string) (kind, status, sentence string) {
	how := triggerLead(r.Trigger)
	state := func(done, doing, failed string) (string, string) {
		switch r.Status {
		case store.ActionFailed:
			return ActivityFailed, name + " " + failed + "."
		case store.ActionRunning:
			return ActivityIdle, name + " " + doing + " " + how + "."
		}
		return ActivityHealthy, name + " " + done + " " + how + "."
	}
	switch r.Label {
	case store.ActionRestart:
		status, sentence = state("was restarted", "is being restarted", "could not be restarted")
		return ActivityRestart, status, sentence
	case store.ActionStop:
		status, sentence = state("was stopped", "is being stopped", "could not be stopped")
		return ActivityStop, status, sentence
	case store.ActionStart:
		status, sentence = state("was started", "is being started", "could not be started")
		return ActivityStart, status, sentence
	case store.ActionTerminal:
		sh := cmp.Or(r.Program, "a shell")
		if r.Status == store.ActionFailed {
			return ActivityCommand, ActivityWarning, "A terminal (" + sh + ") in " + name + " ended with an error."
		}
		return ActivityCommand, ActivityIdle, "A terminal (" + sh + ") was opened in " + name + " " + how + "."
	}
	// A run.
	prog := cmp.Or(r.Program, "a command")
	switch {
	case r.Status == store.ActionRunning:
		return ActivityCommand, ActivityIdle, prog + " is running in " + name + " " + how + "."
	case r.ExitCode != nil && *r.ExitCode == 0:
		return ActivityCommand, ActivityHealthy, prog + " ran in " + name + " " + how + ", exit 0."
	case r.ExitCode != nil:
		return ActivityCommand, ActivityWarning, prog + " ran in " + name + " " + how + ", exit " + strconv.Itoa(*r.ExitCode) + "."
	}
	return ActivityCommand, ActivityFailed, prog + " didn't finish in " + name + " " + how + "."
}

// runWords says the outcome of a backup or restore; subject is "x's backup of db".
func runWords(status, subject string) (string, string) {
	switch status {
	case store.BackupSucceeded:
		return ActivityHealthy, subject + " finished."
	case store.BackupFailed:
		return ActivityFailed, subject + " failed."
	}
	return ActivityIdle, subject + " is running."
}

// scheduleWords says a schedule run that failed or was stopped at its limit.
func scheduleWords(r store.ActivityRow, name string) (string, string) {
	sched := "Schedule " + cmp.Or(r.Label, "run")
	if r.Status == store.RunStoppedAtLimit {
		return ActivityWarning, sched + " was stopped at its time limit in " + name + "."
	}
	if r.ExitCode != nil {
		return ActivityFailed, sched + " failed in " + name + ", exit " + strconv.Itoa(*r.ExitCode) + "."
	}
	return ActivityFailed, sched + " failed in " + name + "."
}

// problemWords says a change of the needs-you list with the sentence that was
// sent when it changed.
func problemWords(r store.ActivityRow) (string, string) {
	summary := strings.TrimSpace(r.Detail)
	if summary == "" {
		summary = strings.ReplaceAll(r.Label, "_", " ")
	}
	switch r.Status {
	case store.ProblemResolved:
		return ActivityHealthy, "Resolved: " + summary
	case store.ProblemWorsened:
		return ActivityFailed, summary
	}
	return ActivityWarning, summary
}

// hostEventWords says a host event: a boot, a Caravana version change, a run of
// the OS's package manager.
func hostEventWords(r store.ActivityRow) (string, string) {
	switch r.Label {
	case store.HostEventBoot:
		return ActivityIdle, "The server started."
	case store.HostEventVersion:
		var d struct{ From, To string }
		_ = json.Unmarshal([]byte(r.Detail), &d)
		switch {
		case d.To == "":
			return ActivityIdle, "Caravana's version changed."
		case d.From == "":
			return ActivityIdle, "Caravana " + d.To + " is running."
		}
		return ActivityIdle, "Caravana was updated from " + d.From + " to " + d.To + "."
	case store.HostEventOSPackages:
		var d struct {
			Packages int
			By       string
			Reboot   bool
		}
		_ = json.Unmarshal([]byte(r.Detail), &d)
		out := "Ubuntu updates were installed"
		if d.Packages > 0 {
			out = fmt.Sprintf("%d Ubuntu packages were updated", d.Packages)
		}
		if d.Reboot {
			return ActivityWarning, out + "; a reboot is needed."
		}
		return ActivityIdle, out + "."
	}
	return ActivityIdle, "The server changed."
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
		Time: ev.Time.UTC(), Kind: ActivityServer, Status: status, Sentence: sentence,
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
