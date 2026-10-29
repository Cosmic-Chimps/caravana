package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Settings (feature 006, and 021 page 3): one page per entry under a grouped
// nav. Account is /settings; Registries, Backup storage, Notifications and API
// tokens have their own addresses. Every form posts to the path it always had
// and a success answers 303 to its entry's page, where the notice is drawn from
// a query flag. A shown-once secret (a new token, a new signing secret) is never
// in a redirect or an address: the post answers 200 with the entry's page and the
// secret drawn once, so a reload or a visit later doesn't have it.

// fieldError is a sentence about one form field, shown under it.
type fieldError struct{ Text string }

// tokenRow is one API token in the table. Never the token's value.
type tokenRow struct {
	ID       string
	Name     string
	State    string // active | expired | revoked
	Mark     string // healthy, warning or idle: tokensState's, the same the nav counts
	Made     when
	LastUsed when
	// Expiry is the Expiry cell's words ("expires in 80 days", "never expires",
	// "expired 3 days ago", "revoked 3 days ago"); ExpiryWarn says it is drawn in
	// the warning hue (a full token that never expires).
	Expiry     string
	ExpiryWarn bool
	Exact      string // the exact time behind Expiry, for a tooltip
	// CanDo is "Everything" for a full token, or "Deploy · app web of project shop".
	CanDo string
	// Notes are the sentences under a row marked with a warning (tokenNotes).
	Notes []tokenNote
}

// newToken is the token just created, shown once. ExampleID is the app a deploy
// token's example reads, "" for a full token.
type newToken struct {
	Name      string
	Value     string
	ExampleID string
}

// scopeForm is the "Only for one app" part of the new-token form: what was
// typed, and what is wrong with it. Projects lists the projects to choose from.
type scopeForm struct {
	Project  string
	App      string
	Projects []scopeProject
	AppErr   *fieldError
	ProjErr  *fieldError
}

// scopeProject is one project in the select.
type scopeProject struct {
	ID, Label string
	Selected  bool
}

// settingsData is every Settings page: Entry says which, and the fields of the
// other entries stay empty. The handler of a refused form sets the fields of
// its own entry; renderSettings fills in the rest.
type settingsData struct {
	pageData
	Entry           string
	Nav             []settingsNavGroup
	Email           string
	Origin          string // scheme://host, for the curl example
	PasswordChanged bool
	CurrentErr      *fieldError
	NewErr          *fieldError
	Tokens          []tokenRow
	OlderHidden     int    // expired or revoked tokens left out of the table, until "Show older"
	ShowOlder       bool   // ?older=1: every expired and revoked token is listed
	TokenName       string // kept after a refused create
	TokenExpires    string
	NameErr         *fieldError
	ExpiresErr      *fieldError
	Scope           scopeForm
	NewToken        *newToken
	Dest            destinationForm
	Registries      registriesData
	Notifications   notificationsData
	Certificates    certificatesData
}

// destinationForm is the Backup storage page: the saved destination (or what
// was typed, after a refusal), never the secret key.
type destinationForm struct {
	Endpoint, Region, Bucket, Prefix, AccessKeyID string
	HasSecret                                     bool // a secret key is kept: the field may stay empty
	Saved                                         bool
	Field                                         string // the field at fault, after a refusal
	Err                                           *fieldError
	// Row is the destination as the page's one connection row says it; nil when none is set.
	Row *destRow
	// Problem says no destination is set while a service has a backup policy.
	Problem bool
	// Open says the form under the row is shown open: it was refused.
	Open bool
}

// destRow is the backup destination's connection row. Mark is backupStorageState's.
type destRow struct {
	Mark, Name, Kind, State string
	StateFailed             bool
	UsedBy                  string
	// Failure is the note under a row whose last backup failed; nil otherwise.
	Failure *backupFailure
}

// backupFailure says which run failed and why: the service and app, the plain cause
// (page 2's table; "" when the message matches none), the raw message in mono and
// where to look.
type backupFailure struct {
	Service, App, Cause, Detail, Href string
}

// destFieldView is one text field of the destination form.
type destFieldView struct {
	Name, Label, Value, Placeholder, Err string
}

// destField is the template function that builds one: name is the input's
// name, which is also the field a refusal names.
func destField(f destinationForm, name, label, value, placeholder string) destFieldView {
	v := destFieldView{Name: name, Label: label, Value: value, Placeholder: placeholder}
	if f.Field == name && f.Err != nil {
		v.Err = f.Err.Text
	}
	return v
}

// setRow is the label and hint of a setting row, and the input the label is for
// ("" when it labels no single input).
type setRow struct{ Label, Hint, For string }

// srowFor is the template function {{srow "Label" "Hint" "input-id"}}.
func srowFor(label, hint, forID string) setRow { return setRow{Label: label, Hint: hint, For: forID} }

// showSettings answers GET /settings: Account.
func (s *server) showSettings(w http.ResponseWriter, r *http.Request, session string) {
	s.renderSettings(w, r, entryAccount, settingsData{PasswordChanged: r.URL.Query().Get("password") == "changed"})
}

// showBackupStorage answers GET /settings/backup-storage.
func (s *server) showBackupStorage(w http.ResponseWriter, r *http.Request, session string) {
	data := settingsData{}
	data.Dest.Saved = r.URL.Query().Get("backups") == "saved"
	s.renderSettings(w, r, entryBackupStorage, data)
}

// showTokens answers GET /settings/tokens. ?older=1 lists every expired and revoked token.
func (s *server) showTokens(w http.ResponseWriter, r *http.Request, session string) {
	s.renderSettings(w, r, entryTokens, settingsData{ShowOlder: r.URL.Query().Get("older") == "1"})
}

// setBackupDestination answers POST /settings/backups (feature 010). An
// empty secret key keeps the one saved before. A refusal draws the page
// again with what was typed, except the secret key.
func (s *server) setBackupDestination(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	in := service.DestinationInput{
		Endpoint: strings.TrimSpace(r.PostFormValue("endpoint")), Region: strings.TrimSpace(r.PostFormValue("region")),
		Bucket: strings.TrimSpace(r.PostFormValue("bucket")), Prefix: strings.TrimSpace(r.PostFormValue("prefix")),
		AccessKeyID: strings.TrimSpace(r.PostFormValue("accessKeyId")),
	}
	if v := r.PostFormValue("secretAccessKey"); v != "" {
		in.SecretAccessKey = &v
	}
	_, err := s.svc.SetBackupDestination(r.Context(), in)
	if err == nil {
		http.Redirect(w, r, "/settings/backup-storage?backups=saved", http.StatusSeeOther)
		return
	}
	form := destinationForm{Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix, AccessKeyID: in.AccessKeyID, Open: true}
	if _, err := s.svc.GetBackupDestination(r.Context()); err == nil {
		form.HasSecret = true
	}
	var fieldErr *app.FieldError
	switch {
	case errors.As(err, &fieldErr):
		form.Field, form.Err = fieldErr.Field, &fieldError{Text: fieldErr.Message}
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't save the backup destination", "error", err)
		form.Err = &fieldError{Text: "Nomad couldn't keep the secret key, so nothing was saved: " + err.Error()}
	default:
		s.serverError(w, "set backup destination", err)
		return
	}
	s.renderSettings(w, r, entryBackupStorage, settingsData{Dest: form})
}

// settingsFacts reads what the nav and the pages are decided from. Some tests
// serve the account pages without a deploy service: then only the tokens are known.
func (s *server) settingsFacts(ctx context.Context) (settingsFacts, error) {
	var f settingsFacts
	var err error
	if f.Tokens, err = s.auth.ListAPITokens(ctx); err != nil {
		return f, fmt.Errorf("list API tokens: %w", err)
	}
	if s.svc == nil {
		return f, nil
	}
	if f.Registries, err = s.svc.Registries(ctx); err != nil {
		return f, fmt.Errorf("list registries: %w", err)
	}
	if f.Channels, err = s.svc.NotificationChannels(ctx); err != nil {
		return f, fmt.Errorf("list notification channels: %w", err)
	}
	f.Deliveries = map[string][]store.Delivery{}
	for _, c := range f.Channels {
		if f.Deliveries[c.ID], err = s.svc.NotificationDeliveries(ctx, c.ID); err != nil {
			return f, fmt.Errorf("list deliveries of channel %q: %w", c.ID, err)
		}
	}
	if f.Certs, err = s.svc.Certificates(ctx); err != nil {
		return f, fmt.Errorf("list certificates: %w", err)
	}
	switch d, err := s.svc.GetBackupDestination(ctx); {
	case err == nil:
		f.Dest = &d
	case !errors.Is(err, store.ErrNotFound):
		return f, fmt.Errorf("read the backup destination: %w", err)
	}
	if f.LastBackup, f.FailingBackup, err = s.svc.BackupOutcome(ctx); err != nil {
		return f, fmt.Errorf("read the last backup outcome: %w", err)
	}
	if f.Apps, err = s.svc.StoredApps(ctx); err != nil {
		return f, fmt.Errorf("list apps: %w", err)
	}
	return f, nil
}

// renderSettings fills in what every Settings response shows (who, the nav with
// its counts and marks, and the entry's own rows) and writes the page of entry
// inside the shell, with Settings marked current.
func (s *server) renderSettings(w http.ResponseWriter, r *http.Request, entry string, data settingsData) {
	facts, err := s.settingsFacts(r.Context())
	if err != nil {
		s.serverError(w, "read settings", err)
		return
	}
	now := s.now()
	label := settingsLabel(entry)
	head, err := s.shellPage(r, "Settings · "+label, navSettings, crumb{Label: "Settings", Href: "/settings"}, crumb{Label: label})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	states := facts.states(now)
	data.pageData = head
	data.Entry = entry
	data.Nav = settingsNav(entry, states)
	data.Email = head.Shell.Email
	data.Origin = s.origin(r)

	switch entry {
	case entryRegistries:
		data.Registries = registriesSection(now, facts, states[entryRegistries], data.Registries)
	case entryBackupStorage:
		data.Dest = destinationSection(now, facts, states[entryBackupStorage], data.Dest)
	case entryNotifications:
		data.Notifications = notificationsSection(now, facts, states[entryNotifications], data.Notifications)
	case entryCertificates:
		cert, err := s.certificatesSection(r.Context(), now, facts, data.Certificates)
		if err != nil {
			s.serverError(w, "read certificate settings", err)
			return
		}
		data.Certificates = cert
	case entryTokens:
		data.Tokens, data.OlderHidden = tokenRows(now, facts.Tokens, states[entryTokens], data.ShowOlder)
		if s.svc != nil {
			pl, err := s.loadPlaces(r.Context())
			if err != nil {
				s.serverError(w, "list projects", err)
				return
			}
			data.Scope.Projects = nil
			for _, pr := range pl.Projects {
				data.Scope.Projects = append(data.Scope.Projects, scopeProject{ID: pr.ID, Label: pr.DisplayName, Selected: pr.ID == data.Scope.Project})
			}
		}
	}
	s.render(w, http.StatusOK, "settings", data)
}

// destinationSection fills in the Backup storage page: the connection row and the
// form's values. A refused form (Err set) keeps what was typed.
func destinationSection(now time.Time, f settingsFacts, st entryState, form destinationForm) destinationForm {
	if f.Dest == nil {
		form.Problem = policyServices(f.Apps) > 0
		return form
	}
	d := *f.Dest
	if form.Err == nil {
		saved := form.Saved
		form = destinationForm{Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket, Prefix: d.Prefix,
			AccessKeyID: d.AccessKeyID, HasSecret: true, Saved: saved}
	}
	row := &destRow{Mark: st.Rows[0], Name: endpointName(d), Kind: "S3-compatible · region " + d.Region,
		UsedBy: usedBy(policyServices(f.Apps), "service")}
	switch last := f.LastBackup; {
	case last == nil:
		row.State = "no backup yet"
	case last.Status == store.BackupSucceeded:
		row.State = "last backup " + backupWhen(now, last.StartedAt)
		if last.Bytes > 0 {
			row.State += " · " + humanBytes(last.Bytes)
		}
	default:
		at := last.StartedAt
		if last.FinishedAt != nil {
			at = *last.FinishedAt
		}
		row.State, row.StateFailed = "last backup failed "+relative(now, at), true
	}
	// The note names the failing service: the destination's own newest run when that failed, or
	// another service's newest run that failed while a later run of a service went well.
	if fail := f.FailingBackup; fail != nil {
		row.Failure = &backupFailure{Service: fail.Service, App: fail.AppID, Cause: causeOf(fail.Message), Detail: fail.Message,
			Href: "/apps/" + fail.AppID + "/backups"}
	}
	form.Row = row
	return form
}

// olderTokensShown is how many expired or revoked tokens the table lists before "Show older".
const olderTokensShown = 5

// tokenRows builds the token table from the tokens (newest first) and
// tokensState: active ones first, those with a problem at the top of them, then
// expired, then revoked. Of the expired and revoked ones only the last
// olderTokensShown (by when they ended) are listed unless all is set; hidden says
// how many were left out.
func tokenRows(now time.Time, tokens []service.APITokenInfo, st entryState, all bool) (rows []tokenRow, hidden int) {
	type ended struct {
		row tokenRow
		at  time.Time
	}
	var inactive []ended
	for i, t := range tokens {
		row := tokenRow{
			ID: t.ID, Name: t.Name, State: t.State, Mark: st.Rows[i],
			Made:     whenOf(now, &t.CreatedAt, ""),
			LastUsed: whenOf(now, t.LastUsedAt, "never"),
			CanDo:    "Everything",
			Notes:    tokenNotes(now, t),
		}
		if t.Scope != nil {
			row.CanDo = "Deploy · app " + t.Scope.App + " of project " + t.Scope.Project
		}
		var at time.Time
		switch t.State {
		case service.TokenRevoked:
			w := whenOf(now, t.RevokedAt, "")
			row.Expiry, row.Exact, at = "revoked "+w.Text, w.Exact, *t.RevokedAt
		case service.TokenExpired:
			w := whenOf(now, t.ExpiresAt, "")
			row.Expiry, row.Exact, at = "expired "+w.Text, w.Exact, *t.ExpiresAt
		case service.TokenActive:
			if t.ExpiresAt == nil {
				row.Expiry = "never expires"
				// The warning hue is for the token the note is about.
				row.ExpiryWarn = t.Scope == nil
			} else {
				w := whenOf(now, t.ExpiresAt, "")
				row.Expiry, row.Exact = "expires "+w.Text, w.Exact
			}
		}
		if t.State == service.TokenActive {
			rows = append(rows, row)
		} else {
			inactive = append(inactive, ended{row, at})
		}
	}
	// The most recently ended first; the cut keeps those, and the table keeps its order.
	slices.SortStableFunc(inactive, func(a, b ended) int { return b.at.Compare(a.at) })
	if !all && len(inactive) > olderTokensShown {
		hidden, inactive = len(inactive)-olderTokensShown, inactive[:olderTokensShown]
	}
	for _, in := range inactive {
		rows = append(rows, in.row)
	}
	rank := func(r tokenRow) int {
		switch {
		case r.State == service.TokenActive && r.Mark == "warning":
			return 0
		case r.State == service.TokenActive:
			return 1
		case r.State == service.TokenExpired:
			return 2
		}
		return 3
	}
	sortStable(rows, rank)
	return rows, hidden
}

// changePassword answers POST /settings/password. Success goes back to the
// page with a confirmation (post/redirect/get); a refusal shows the page with
// a sentence under the field at fault.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	err := s.auth.ChangePassword(r.Context(), r.PostFormValue("currentPassword"), r.PostFormValue("newPassword"), session)
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings?password=changed", http.StatusSeeOther)
	case errors.Is(err, service.ErrWrongPassword):
		s.renderSettings(w, r, entryAccount, settingsData{CurrentErr: &fieldError{Text: "That isn't the current password."}})
	case errors.As(err, &fieldErr) && fieldErr.Field == "newPassword":
		s.renderSettings(w, r, entryAccount, settingsData{NewErr: &fieldError{Text: "The new password " + fieldErr.Message + "."}})
	default:
		s.serverError(w, "change password", err)
	}
}

// createToken answers POST /settings/tokens. The new token is shown once, in
// this very response: a redirect would lose it (research R12).
func (s *server) createToken(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	name, expires := strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("expires")
	data := settingsData{TokenName: name, TokenExpires: expires}
	// "Only for one app" (a deploy token): an app name makes it one, in the chosen
	// project (Default's when the form had no select).
	var scope *service.TokenScope
	data.Scope = scopeForm{Project: r.PostFormValue("scopeProject"), App: strings.TrimSpace(r.PostFormValue("scopeApp"))}
	if data.Scope.App != "" {
		project := data.Scope.Project
		if project == "" {
			project = app.DefaultProject
		}
		scope = &service.TokenScope{Project: project, App: data.Scope.App}
	}

	var expiresAt *time.Time
	if expires != "" {
		// A date, taken as the end of that day in UTC.
		day, err := time.Parse("2006-01-02", expires)
		if err != nil {
			data.ExpiresErr = &fieldError{Text: "The expiry must be a date."}
			s.renderSettings(w, r, entryTokens, data)
			return
		}
		expiresAt = new(day.Add(24*time.Hour - time.Second))
	}

	created, err := s.auth.CreateScopedAPIToken(r.Context(), name, expiresAt, scope)
	var fieldErr *app.FieldError
	switch {
	case err == nil:
		nt := &newToken{Name: created.Info.Name, Value: created.Token}
		if scope != nil {
			nt.ExampleID = app.Place{Project: scope.Project, Environment: app.DefaultEnvironment, Name: scope.App}.ID()
		}
		s.renderSettings(w, r, entryTokens, settingsData{NewToken: nt})
	case errors.Is(err, service.ErrTokenNameTaken):
		data.NameErr = &fieldError{Text: "An active token already has that name."}
		s.renderSettings(w, r, entryTokens, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "name":
		data.NameErr = &fieldError{Text: "The name " + fieldErr.Message + "."}
		s.renderSettings(w, r, entryTokens, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "expiresAt":
		data.ExpiresErr = &fieldError{Text: "The expiry " + fieldErr.Message + "."}
		s.renderSettings(w, r, entryTokens, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "scope.project":
		data.Scope.ProjErr = &fieldError{Text: "The project " + strings.TrimPrefix(fieldErr.Message, "project ") + "."}
		s.renderSettings(w, r, entryTokens, data)
	case errors.As(err, &fieldErr) && fieldErr.Field == "scope.app":
		data.Scope.AppErr = &fieldError{Text: "The app name " + strings.TrimPrefix(fieldErr.Message, `"`+data.Scope.App+`" `) + "."}
		s.renderSettings(w, r, entryTokens, data)
	default:
		s.serverError(w, "create api token", err)
	}
}

// revokeToken answers POST /settings/tokens/{tokenId}/revoke. The page asks
// for a confirmation first (a <details>, research R16); this is the post it
// sends. Revoking can't touch deployed apps, and a new token replaces it.
func (s *server) revokeToken(w http.ResponseWriter, r *http.Request, session string) {
	err := s.auth.RevokeAPIToken(r.Context(), r.PathValue("tokenId"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "revoke api token", err)
		return
	}
	http.Redirect(w, r, "/settings/tokens", http.StatusSeeOther)
}

// readForm parses a posted form with a size limit. It answers 400 itself and
// returns false when the form can't be read.
func (s *server) readForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		s.message(w, http.StatusBadRequest, "Bad request", "That form couldn't be read.", "Go back and try again.")
		return false
	}
	return true
}

// showAccount answers GET /account, the address 006 had: it is Settings now. A
// permanent redirect, so bookmarks and old links move over. It needs no
// session; /settings asks for one.
func (s *server) showAccount(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings", http.StatusMovedPermanently)
}

// showSettingsAccount answers GET /settings/account, the address Account had
// for a moment in 021: it is /settings, the first entry. Permanent, no session.
func (s *server) showSettingsAccount(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings", http.StatusMovedPermanently)
}
