package ui

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Operator › Caravana backup (feature 063, specs/063-self-backup/plan.md D14): whether Caravana's own daily
// backup works, the settings, the recovery key and the runs. The page is complete without script: Back up
// now, the settings and the key are plain posts. The state and the past runs are one live region (swapped
// on the selfbackup and attention notices, and polled every 3 s from /parts/ops/self-backup while a run goes,
// where the part answers 286 once nothing runs); the forms and the key section sit outside it, so a refresh
// never replaces what is being typed. The key is shown once, in the answer to the post that made it, a page
// of its own that is never redirected to, logged or drawn again.

// selfBackupRefreshSeconds is how often a visitor without script reloads the page while a backup runs.
const selfBackupRefreshSeconds = 5

// selfBackupGuideHref is where the restore guide is published (the site's address of docs/self-backup.md).
const selfBackupGuideHref = "https://cosmic-chimps.github.io/caravana/host/self-backup/"

// sbLink is a way out of a state: a link with its words.
type sbLink struct{ Text, Href string }

// sbLine is one line of the state: words, a clock time, and more words.
type sbLine struct {
	Lead string
	When *clockTime
	Tail sentence
}

// sbFact is one row of the last run's facts.
type sbFact struct {
	Label string
	When  *clockTime
	Mono  string
	Text  string
}

// sbRunRow is one row of the past runs.
type sbRunRow struct {
	Mark   string
	When   clockTime
	Text   sentence
	Detail sentence
}

// sbState is the region: the state in one pill and one or two lines, the way out, the last run's facts, the
// button, and the past runs (swapped by id beside it).
type sbState struct {
	Status    statusView
	Lines     []sbLine
	Way       *sbLink
	Warning   string
	Facts     []sbFact
	Running   bool
	CanBackUp bool // an admin, and a backup can be made now
	IsAdmin   bool
	Runs      []sbRunRow
	PartHref  string
}

// sbSettings is the settings form's state: what is stored, and the hours the select offers.
type sbSettings struct {
	Enabled bool
	Hour    int
	Keep    int
	Hours   []sbHour
	Summary string // the read-only line an admin sees
}

type sbHour struct {
	Value    int
	Label    string
	Selected bool
}

// sbKey is the recovery key section: the key's fingerprint and age, never the key.
type sbKey struct {
	Fingerprint string
	Made        *clockTime
}

// selfBackupPage is the page.
type selfBackupPage struct {
	pageData
	State     sbState
	Settings  sbSettings
	Key       sbKey
	IsOwner   bool
	Saved     bool
	Refusal   sentence // why a post was turned down
	GuideHref string
}

// selfBackupKeyPage is the key's page: the confirmation before a new key, or the key, once.
type selfBackupKeyPage struct {
	pageData
	Replace  bool   // the confirmation is for a new key, not the first one
	Made     bool   // the key is on the page
	Key      string // the recovery key itself, only on the page that follows the post that made it
	Print    string // its fingerprint
	Refusal  sentence
	HasKey   bool
	BackHref string
}

// sbHours is the select's options with hour chosen.
func sbHours(chosen int) []sbHour {
	out := make([]sbHour, 24)
	for h := range out {
		out[h] = sbHour{Value: h, Label: fmt.Sprintf("%02d:00 UTC", h), Selected: h == chosen}
	}
	return out
}

// sbTriggerWords says what started a run, to follow a lead: "on schedule", "by you", "by ana@example.com",
// "when the recovery key was made".
func sbTriggerWords(trigger, actor, viewer string) string {
	switch trigger {
	case store.SelfBackupBySchedule:
		return "on schedule"
	case store.SelfBackupByKey:
		if actor != "" {
			return "when the recovery key was made, by " + actorWords(actor, viewer)
		}
		return "when the recovery key was made"
	}
	if actor != "" {
		return "by " + actorWords(actor, viewer)
	}
	return "by an owner"
}

// sbSizeTo is "2.1 MB to main", the destination in mono.
func sbSizeTo(b int64, destination string) sentence {
	if destination == "" {
		return plainS(humanBytes(b))
	}
	return sentence{plain(humanBytes(b) + " to "), mono(destination)}
}

// selfBackupStateOf turns the service's state into what the region draws.
func selfBackupStateOf(now time.Time, viewer string, admin bool, st service.SelfBackupStatus, runs []store.SelfBackup) sbState {
	v := sbState{IsAdmin: admin, PartHref: "/parts/ops/self-backup", Running: st.Running != nil}
	clock := func(t time.Time) *clockTime { c := clockOf(now, t); return &c }
	switch {
	case st.Running != nil:
		v.Status = statusView{Kind: "rolling", Word: "Running"}
		v.Lines = []sbLine{{Lead: "Backing up now. It started", When: clock(st.Running.StartedAt), Tail: plainS(".")}}
	case st.Unavailable == service.SelfBackupNoDestination:
		v.Status = statusView{Kind: "warning", Word: "Not set up"}
		v.Lines = []sbLine{{Lead: "Caravana has nowhere to put its backup yet."}}
		v.Way = &sbLink{Text: "Add a backup destination in Settings › Backup storage", Href: "/settings/backup-storage"}
	case st.Unavailable == service.SelfBackupNoKey:
		v.Status = statusView{Kind: "warning", Word: "Not set up"}
		v.Lines = []sbLine{{Lead: "Caravana can't back itself up until you make a recovery key."}}
		v.Way = &sbLink{Text: "Make a recovery key", Href: "#recovery-key"}
	case st.Last != nil && st.Last.Status == store.SelfBackupFailed:
		v.Status = statusView{Kind: "failed", Word: "Failed"}
		reason := strings.TrimSpace(st.Last.Error)
		if reason == "" {
			reason = "No reason was recorded."
		}
		v.Lines = []sbLine{{Lead: "The latest backup failed:", Tail: plainS(" " + reason)}}
		if st.LastSucceeded != nil {
			v.Lines = append(v.Lines, sbLine{Lead: "The last good one was", When: clock(runEnd(*st.LastSucceeded)), Tail: plainS(".")})
		}
	case st.Last != nil:
		v.Status = statusView{Kind: "healthy", Word: "Backed up"}
		last := *st.Last
		v.Lines = []sbLine{{Lead: "Backed up", When: clock(runEnd(last)), Tail: join(plainS(" to "), sentence{mono(last.Destination)}, plainS(", "+humanBytes(last.Bytes)+"."))}}
		v.Warning = last.Warning
	default:
		v.Status = statusView{Kind: "idle", Word: "Waiting"}
		if st.Settings.Enabled {
			v.Lines = []sbLine{{Lead: fmt.Sprintf("No backup yet. The first one runs at %02d:00 UTC, or press Back up now.", st.Settings.Hour)}}
		} else {
			v.Lines = []sbLine{{Lead: "No backup yet, and the daily backup is off. Press Back up now to make one."}}
		}
	}
	if !st.Settings.Enabled && st.Unavailable == "" && st.Running == nil && st.Last != nil {
		v.Lines = append(v.Lines, sbLine{Lead: "The daily backup is off."})
	}
	v.CanBackUp = admin && st.Unavailable == "" && st.Running == nil
	if st.Last != nil {
		l := *st.Last
		v.Facts = []sbFact{{Label: "Started", When: clock(l.StartedAt)}, {Label: "How", Text: sbTriggerWords(l.Trigger, l.Actor, viewer)}}
		if l.Destination != "" {
			v.Facts = append(v.Facts, sbFact{Label: "Where", Mono: l.Destination})
		}
		if l.Status == store.SelfBackupSucceeded {
			v.Facts = append(v.Facts, sbFact{Label: "Size", Text: humanBytes(l.Bytes)})
		}
		if l.KeyFingerprint != "" {
			v.Facts = append(v.Facts, sbFact{Label: "Key", Mono: l.KeyFingerprint})
		}
		if l.Version != "" {
			v.Facts = append(v.Facts, sbFact{Label: "Version", Mono: l.Version})
		}
	}
	for _, r := range runs {
		v.Runs = append(v.Runs, sbRunOf(now, viewer, r))
	}
	return v
}

// runEnd is when a run ended, or started while it hasn't.
func runEnd(r store.SelfBackup) time.Time {
	if r.EndedAt.IsZero() {
		return r.StartedAt
	}
	return r.EndedAt
}

// sbRunOf is one row of the past runs: when, who started it and what came of it, then the key.
func sbRunOf(now time.Time, viewer string, r store.SelfBackup) sbRunRow {
	how := sbTriggerWords(r.Trigger, r.Actor, viewer)
	row := sbRunRow{When: clockOf(now, r.StartedAt)}
	switch r.Status {
	case store.SelfBackupSucceeded:
		row.Mark = "healthy"
		row.Text = join(plainS(capitalize(how)+": "), sbSizeTo(r.Bytes, r.Destination))
	case store.SelfBackupFailed:
		row.Mark = "failed"
		reason := strings.TrimSpace(r.Error)
		if reason == "" {
			reason = "no reason was recorded."
		}
		row.Text = plainS(capitalize(how) + ": failed. " + reason)
	default:
		row.Mark = "rolling"
		row.Text = plainS(capitalize(how) + ": running.")
	}
	if r.KeyFingerprint != "" {
		row.Detail = sentence{plain("key "), mono(r.KeyFingerprint)}
	}
	if r.Warning != "" {
		row.Detail = join(row.Detail, plainS(" · "+r.Warning))
	}
	return row
}

// loadSelfBackup reads the service and builds the page's parts.
func (s *server) loadSelfBackup(r *http.Request) (sbState, sbSettings, sbKey, service.SelfBackupStatus, error) {
	ctx := r.Context()
	st, err := s.svc.SelfBackups(ctx)
	if err != nil {
		return sbState{}, sbSettings{}, sbKey{}, st, err
	}
	runs, err := s.svc.SelfBackupRuns(ctx)
	if err != nil {
		return sbState{}, sbSettings{}, sbKey{}, st, err
	}
	now := s.now().UTC()
	who, _ := service.PrincipalFrom(ctx)
	state := selfBackupStateOf(now, who.Email, who.Admin(), st, runs)
	settings := sbSettings{Enabled: st.Settings.Enabled, Hour: st.Settings.Hour, Keep: st.Settings.Keep, Hours: sbHours(st.Settings.Hour)}
	if st.Settings.Enabled {
		settings.Summary = fmt.Sprintf("Every day at %02d:00 UTC, keeping the last %d.", st.Settings.Hour, st.Settings.Keep)
	} else {
		settings.Summary = fmt.Sprintf("The daily backup is off. When it is on, it runs at %02d:00 UTC and keeps the last %d.", st.Settings.Hour, st.Settings.Keep)
	}
	key := sbKey{Fingerprint: st.KeyFingerprint}
	if !st.KeyCreatedAt.IsZero() {
		c := clockOf(now, st.KeyCreatedAt)
		key.Made = &c
	}
	return state, settings, key, st, nil
}

// drawSelfBackup draws the page with a status and the refusal of a post that was turned down.
func (s *server) drawSelfBackup(w http.ResponseWriter, r *http.Request, status int, refusal sentence, saved bool) {
	state, settings, key, _, err := s.loadSelfBackup(r)
	if err != nil {
		s.serverError(w, r, "read Caravana's backup", err)
		return
	}
	head, err := s.shellPage(r, "Caravana backup", navOpsSelfBackup, crumb{Label: "Caravana backup"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	head.LiveURL = "/live?watch=selfbackup"
	if state.Running {
		head.Refresh = selfBackupRefreshSeconds // without script the page reloads itself while a backup runs
	}
	s.render(w, status, "opsselfbackup", selfBackupPage{pageData: head, State: state, Settings: settings, Key: key,
		IsOwner: head.Who.Owner(), Saved: saved, Refusal: refusal, GuideHref: selfBackupGuideHref})
}

// showSelfBackup answers GET /ops/self-backup.
func (s *server) showSelfBackup(w http.ResponseWriter, r *http.Request, session string) {
	s.drawSelfBackup(w, r, http.StatusOK, nil, r.URL.Query().Get("saved") == "1")
}

// partSelfBackup answers GET /parts/ops/self-backup: the state, and by id the past runs. While a backup
// runs it is the plain part; once nothing runs it answers 286, which htmx takes as "stop polling" and still
// swaps in.
func (s *server) partSelfBackup(w http.ResponseWriter, r *http.Request, session string) {
	state, _, _, _, err := s.loadSelfBackup(r)
	if err != nil {
		s.log.Error("read Caravana's backup", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if state.Running {
		s.renderPart(w, "opsselfbackup", "sb-part", state)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var buf strings.Builder
	if err := s.pages["opsselfbackup"].ExecuteTemplate(&buf, "sb-part", state); err != nil {
		s.log.Error("render part", "part", "sb-part", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(286)
	_, _ = w.Write([]byte(buf.String()))
}

// startSelfBackupNow answers POST /ops/self-backup/runs: 303 to the page, whose state follows the run. A
// refusal draws the page again with its reason (409).
func (s *server) startSelfBackupNow(w http.ResponseWriter, r *http.Request, session string) {
	if _, err := s.svc.RequestSelfBackup(r.Context()); err != nil {
		var un *service.SelfBackupUnavailableError
		if errors.As(err, &un) || errors.Is(err, service.ErrSelfBackupRunning) {
			s.drawSelfBackup(w, r, http.StatusConflict, plainS(refusalWords(err)), false)
			return
		}
		s.serverError(w, r, "start a backup of Caravana", err)
		return
	}
	http.Redirect(w, r, "/ops/self-backup", http.StatusSeeOther)
}

// saveSelfBackupSettings answers POST /ops/self-backup/settings (owners): 303 to the page with ?saved=1. A
// number the service refuses draws the page again with its sentence (422).
func (s *server) saveSelfBackupSettings(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	enabled := r.PostFormValue("enabled") != ""
	in := service.SelfBackupSettingsInput{Enabled: &enabled}
	for _, f := range []struct {
		name string
		into **int
		say  string
	}{{"hour", &in.Hour, "Choose an hour from 00:00 UTC to 23:00 UTC."}, {"keep", &in.Keep, "Keep a number of backups from 1 to 90."}} {
		raw := strings.TrimSpace(r.PostFormValue(f.name))
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			s.drawSelfBackup(w, r, http.StatusUnprocessableEntity, plainS(f.say), false)
			return
		}
		*f.into = &n
	}
	_, err := s.svc.SetSelfBackupSettings(r.Context(), in)
	var fe *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, "/ops/self-backup?saved=1#settings", http.StatusSeeOther)
	case errors.As(err, &fe):
		s.drawSelfBackup(w, r, http.StatusUnprocessableEntity, plainS(refusalWords(errors.New(fe.Field+" "+fe.Message))), false)
	default:
		s.serverError(w, r, "save the backup settings of Caravana", err)
	}
}

// keyPage starts the data of the key's pages.
func (s *server) keyPage(r *http.Request) (selfBackupKeyPage, error) {
	head, err := s.shellPage(r, "Recovery key", navOpsSelfBackup, crumb{Label: "Caravana backup", Href: "/ops/self-backup"}, crumb{Label: "Recovery key"})
	if err != nil {
		return selfBackupKeyPage{}, err
	}
	st, err := s.svc.SelfBackups(r.Context())
	if err != nil {
		return selfBackupKeyPage{}, err
	}
	return selfBackupKeyPage{pageData: head, HasKey: st.KeyFingerprint != "", BackHref: "/ops/self-backup"}, nil
}

// showRecoveryKeyConfirm answers GET /ops/self-backup/key (owners): what a new key means, and the one
// button that makes it.
func (s *server) showRecoveryKeyConfirm(w http.ResponseWriter, r *http.Request, session string) {
	p, err := s.keyPage(r)
	if err != nil {
		s.serverError(w, r, "read Caravana's backup", err)
		return
	}
	p.Replace = p.HasKey
	s.render(w, http.StatusOK, "opsselfbackupkey", p)
}

// makeRecoveryKey answers POST /ops/self-backup/key (owners) with 200 and the key, once, on a page of its
// own: never a redirect (the key would be in an address), never logged, and nothing in the page's address,
// the live stream or a later page holds it. With a key already made it needs the confirmation's replace.
func (s *server) makeRecoveryKey(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	res, err := s.svc.CreateRecoveryKey(r.Context(), r.PostFormValue("replace") != "")
	if err != nil {
		if errors.Is(err, service.ErrRecoveryKeyExists) {
			p, perr := s.keyPage(r)
			if perr != nil {
				s.serverError(w, r, "read Caravana's backup", perr)
				return
			}
			p.Replace, p.Refusal = true, plainS("A recovery key was made already. Making a new one is a choice: confirm it here.")
			s.render(w, http.StatusConflict, "opsselfbackupkey", p)
			return
		}
		s.serverError(w, r, "make a recovery key", err)
		return
	}
	p, err := s.keyPage(r)
	if err != nil {
		// The key was made and can't be shown again: say so plainly rather than a bare error page.
		s.serverError(w, r, "draw the recovery key page", err)
		return
	}
	p.Made, p.Key, p.Print = true, res.Key, res.Fingerprint
	s.render(w, http.StatusOK, "opsselfbackupkey", p)
}
