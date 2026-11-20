package ui

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Settings › Notifications (feature 018, and 021 page 3): where Caravana tells
// the owner what needs them and how deploys end, one connection row per channel.
// Every form is a plain post, so the page works without script. The secret parts of a channel (a Slack or Discord URL,
// an ntfy or Telegram bot token, an SMTP password, a webhook's signing secret) are write-only: no struct here has
// a field for one, except the notice that shows a signing secret once, and a
// refused form never gives a typed Slack or Discord URL, token or password back.

// notificationForm is what an add or edit form shows: the values as saved or as
// typed, and the field a refusal names (the name of its input). Never a token or
// a password, and never a Slack or Discord URL, which is itself a secret.
type notificationForm struct {
	Kind, Name, URL string
	// The settings of an email channel (inputs smtpHost, smtpPort, smtpSecurity,
	// smtpUsername, fromAddress and toAddresses, the last one as typed: addresses
	// separated by commas, spaces or line breaks) and of a Telegram one (chatId and
	// apiUrl, the Bot API server).
	SMTPHost, SMTPPort, SMTPSecurity, SMTPUsername, FromAddress, ToAddresses string
	ChatID, APIURL                                                           string
	Problems                                                                 bool
	DeployFailed                                                             bool
	DeploySucceeded                                                          bool
	Field                                                                    string // the field at fault, after a refusal
	Err                                                                      *fieldError
}

// emailSettings reads the email inputs as the service's settings. A port that
// isn't a number is refused here, since the service only has numbers.
func (f notificationForm) emailSettings() (*service.EmailSettings, error) {
	port := 0
	if f.SMTPPort != "" {
		var err error
		if port, err = strconv.Atoi(f.SMTPPort); err != nil {
			return nil, app.NewFieldError("email.port", "must be a number between 1 and 65535")
		}
	}
	to := strings.FieldsFunc(f.ToAddresses, func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) })
	return &service.EmailSettings{Host: f.SMTPHost, Port: port, Security: f.SMTPSecurity, Username: f.SMTPUsername, From: f.FromAddress, To: to}, nil
}

func (f notificationForm) telegramSettings() *service.TelegramSettings {
	return &service.TelegramSettings{ChatID: f.ChatID, APIURL: f.APIURL}
}

// notifyInput is one text input of an email or Telegram row, with the refusal
// that names it.
type notifyInput struct {
	ID, Name, Type, Value, Placeholder, Err string
}

// Input is the view of the input called name, filled from value.
func (v notifyFormView) Input(name, typ, value, placeholder string) notifyInput {
	return notifyInput{ID: v.Prefix + "-" + name, Name: name, Type: typ, Value: value, Placeholder: placeholder, Err: v.Error(name)}
}

// Securities lists the entries of the security select, with the form's selected
// (STARTTLS when it has none).
func (v notifyFormView) Securities() []kindOption {
	chosen := v.Form.SMTPSecurity
	if chosen == "" {
		chosen = notify.SecuritySTARTTLS
	}
	var out []kindOption
	for _, o := range []kindOption{
		{Value: notify.SecuritySTARTTLS, Label: "STARTTLS (port 587)"},
		{Value: notify.SecurityTLS, Label: "TLS (port 465)"},
		{Value: notify.SecurityNone, Label: "None, for a mail server on this host"},
	} {
		o.Selected = o.Value == chosen
		out = append(out, o)
	}
	return out
}

// PasswordHint is the hint under the SMTP password input.
func (v notifyFormView) PasswordHint() string {
	if v.HasToken {
		return "A password is set: leave it empty to keep it."
	}
	return "Leave empty when the server needs no login."
}

// BotTokenHint is the hint under the Telegram bot token input.
func (v notifyFormView) BotTokenHint() string {
	if v.Editing {
		return "A token is set: leave it empty to keep it."
	}
	return "The token BotFather gave, like 123456789:AA…"
}

// notifyFormView is a form together with what its template needs to draw it.
type notifyFormView struct {
	Prefix   string // keeps the ids of the add form and of each row's edit form apart
	Form     notificationForm
	Editing  bool   // an edit form: the kind is fixed
	Kind     string // the channel's kind, when editing
	HasToken bool   // an ntfy channel that has an access token, or an email channel with a password
}

// notifyFormFor builds the view of a form for the template.
func notifyFormFor(form notificationForm, prefix string, editing bool, kind string, hasToken bool) notifyFormView {
	return notifyFormView{Prefix: prefix, Form: form, Editing: editing, Kind: kind, HasToken: hasToken}
}

// Error is the sentence under the named field, or "" when it isn't at fault.
func (v notifyFormView) Error(field string) string {
	if v.Form.Field == field && v.Form.Err != nil {
		return v.Form.Err.Text
	}
	return ""
}

// kindOption is one entry of the kind select.
type kindOption struct {
	Value, Label string
	Selected     bool
}

// Kinds lists the entries of the kind select, with the form's kind selected.
func (v notifyFormView) Kinds() []kindOption {
	var out []kindOption
	for _, k := range notify.Kinds {
		out = append(out, kindOption{Value: k, Label: kindLabel(k), Selected: k == v.Form.Kind})
	}
	return out
}

// SlackLike says whether the channel's URL is a secret that is never shown: in
// an edit form that means the field starts empty and an empty field keeps it. An
// ntfy topic is one too (anyone who knows it can read and post to it); only a
// webhook's address is shown whole.
func (v notifyFormView) SlackLike() bool {
	return v.Kind == notify.KindSlack || v.Kind == notify.KindDiscord || v.Kind == notify.KindNtfy
}

// URLHint is the hint under the URL input: the whole story when adding, and in
// an edit form only what a Slack or Discord URL needs, since it is never shown.
func (v notifyFormView) URLHint() string {
	switch {
	case !v.Editing:
		return "ntfy: the topic URL, like https://ntfy.sh/my-topic. Slack and Discord: the incoming webhook URL. Webhook: where Caravana posts signed JSON."
	case v.SlackLike():
		return "Leave empty to keep it."
	}
	return ""
}

// TokenHint is the hint under the ntfy token input.
func (v notifyFormView) TokenHint() string {
	if v.HasToken {
		return "Only for a protected topic. A token is set: leave it empty to keep it."
	}
	return "Only for a protected topic."
}

// kindLabel is how the console writes a kind.
func kindLabel(kind string) string {
	switch kind {
	case notify.KindNtfy:
		return "ntfy"
	case notify.KindSlack:
		return "Slack"
	case notify.KindDiscord:
		return "Discord"
	case notify.KindWebhook:
		return "Webhook"
	case notify.KindEmail:
		return "Email"
	case notify.KindTelegram:
		return "Telegram"
	}
	return kind
}

// lastDelivery is a channel's newest finished delivery, for its "Last delivery"
// cell.
type lastDelivery struct {
	OK    bool
	Kind  string // "sent" or "failed"
	When  when
	Error string // why it failed
}

// deliveryRow is one line of a channel's recent deliveries.
type deliveryRow struct {
	When   when
	Title  string
	OK     bool
	Failed bool
	Error  string // why it failed; "" otherwise
	Detail string // "sent", "failed (3 attempts): <error>" or "queued"
}

// notificationRow is one channel, one connection row.
type notificationRow struct {
	ID, Name, Kind, KindLabel, Address string
	KindLine                           string // the kind, then what the channel sends when that isn't everything
	Icon                               string // the kind's icon: hash, message-square, bell, mail, send or webhook
	Mark                               string // notificationsState's: healthy, warning or idle
	Paused                             bool
	HasToken                           bool
	Sends                              string
	Last                               *lastDelivery // nil: nothing sent yet
	Deliveries                         []deliveryRow
	Edit                               notificationForm // what the row's edit form shows
	Open                               bool             // the edit form was refused, so it is shown open
}

// signingSecretNotice is the signing secret just made, shown once.
type signingSecretNotice struct {
	Name, Value string
}

// notificationNotice is a sentence under the section's heading.
type notificationNotice struct {
	Text string
	Bad  bool // a refusal: shown as an alert with the failed mark
}

// notificationsData is the whole section.
type notificationsData struct {
	Rows   []notificationRow
	Notice *notificationNotice
	Alert  *fieldError // a refusal that names no field, such as Nomad being down
	Secret *signingSecretNotice
	Add    notificationForm
	// EditID and Edit say which row's edit form was refused, and how.
	EditID string
	Edit   notificationForm
	// Saved and Notified come from the redirect after a save or a test:
	// Notified is the ID of the channel whose test result to announce.
	Saved    bool
	Notified string
}

// formEvents are the three event checkboxes of a form, in the order the
// service lists them.
func formEvents(f notificationForm) []string {
	var out []string
	if f.Problems {
		out = append(out, service.EventProblems)
	}
	if f.DeployFailed {
		out = append(out, service.EventDeployFailed)
	}
	if f.DeploySucceeded {
		out = append(out, service.EventDeploySucceeded)
	}
	return out
}

// formFromEvents sets the three event checkboxes from a list of events.
func formFromEvents(f notificationForm, events []string) notificationForm {
	f.Problems = slices.Contains(events, service.EventProblems)
	f.DeployFailed = slices.Contains(events, service.EventDeployFailed)
	f.DeploySucceeded = slices.Contains(events, service.EventDeploySucceeded)
	return f
}

// readNotificationForm reads an add or edit form. It keeps the typed URL only
// for a webhook or ntfy channel, whose address the console shows anyway.
func readNotificationForm(r *http.Request, kind string) notificationForm {
	field := func(name string) string { return strings.TrimSpace(r.PostFormValue(name)) }
	f := notificationForm{
		Kind: kind, Name: field("name"), URL: field("url"),
		SMTPHost: field("smtpHost"), SMTPPort: field("smtpPort"), SMTPSecurity: field("smtpSecurity"), SMTPUsername: field("smtpUsername"),
		FromAddress: field("fromAddress"), ToAddresses: field("toAddresses"),
		ChatID: field("chatId"), APIURL: field("apiUrl"),
	}
	return formFromEvents(f, r.PostForm["events"])
}

// echoable is the form to draw again after a refusal: a Slack, Discord or ntfy
// address is a secret and doesn't come back, so the owner types it again. (A
// password or token is never in the form at all.)
func echoable(f notificationForm) notificationForm {
	if f.Kind != notify.KindWebhook {
		f.URL = ""
	}
	return f
}

// notificationInputName is the input a service field error is drawn under: the
// service names fields as the API does (email.host, token), the console's inputs
// have their own names (smtpHost, botToken).
func notificationInputName(kind, field string) string {
	switch {
	case strings.HasPrefix(field, "email.to"):
		return "toAddresses"
	case field == "token" && kind == notify.KindTelegram:
		return "botToken"
	}
	if input, ok := map[string]string{
		"email.host": "smtpHost", "email.port": "smtpPort", "email.security": "smtpSecurity", "email.username": "smtpUsername",
		"email.from": "fromAddress", "telegram.chatId": "chatId", "telegram.apiUrl": "apiUrl",
	}[field]; ok {
		return input
	}
	return field
}

// notificationFieldMessage is a field error as a sentence for the input it
// names: the service's messages are written to follow the field's name ("is
// required"). The channel's kind tells the ntfy token from the Telegram bot token.
func notificationFieldMessage(fe *app.FieldError, kind string) string {
	if fe.Field == "events" {
		if strings.Contains(fe.Message, "at least one") {
			return "Pick at least one thing to send."
		}
		return "The events " + strings.TrimSuffix(fe.Message, ".") + "."
	}
	name := map[string]string{
		"name": "The name", "url": "The URL", "token": "The token", "kind": "The kind",
		"email.host": "The mail server", "email.port": "The port", "email.security": "The security", "email.username": "The user name",
		"email.from": "The from address", "password": "The password", "telegram.chatId": "The chat ID", "telegram.apiUrl": "The Bot API address",
	}[fe.Field]
	switch {
	case strings.HasPrefix(fe.Field, "email.to"):
		name = "The to addresses"
	case fe.Field == "token" && kind == notify.KindTelegram:
		name = "The bot token"
	}
	if name == "" {
		return fe.Message
	}
	return name + " " + strings.TrimSuffix(fe.Message, ".") + "."
}

// showNotifications answers GET /settings/notifications. The notice comes from
// the redirect after a save or a test.
func (s *server) showNotifications(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	s.renderSettings(w, r, entryNotifications, settingsData{Notifications: notificationsData{
		Saved: q.Get("notifications") == "saved", Notified: q.Get("notified"),
	}})
}

// addNotification answers POST /settings/notifications. A webhook's signing
// secret is shown once, in this very response, because a redirect would lose
// it (as a new API token is). Every other kind goes back to the page.
func (s *server) addNotification(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	form := readNotificationForm(r, r.PostFormValue("kind"))
	// The form draws every kind's inputs (it works without script), so only the
	// ones of the chosen kind are read; a stale URL typed before switching to
	// email is ignored, not refused.
	in := service.NotificationChannelInput{Kind: form.Kind, Name: form.Name, Events: formEvents(form)}
	var err error
	switch form.Kind {
	case notify.KindEmail:
		in.Email, err = form.emailSettings()
		in.Password = r.PostFormValue("password")
	case notify.KindTelegram:
		in.Telegram = form.telegramSettings()
		in.Token = r.PostFormValue("botToken")
	default:
		in.URL, in.Token = form.URL, r.PostFormValue("token")
	}
	var created service.CreatedNotificationChannel
	if err == nil {
		created, err = s.svc.CreateNotificationChannel(r.Context(), in)
	}
	switch {
	case err == nil && created.SigningSecret != "":
		s.renderSettings(w, r, entryNotifications, settingsData{Notifications: notificationsData{
			Secret: &signingSecretNotice{Name: created.Name, Value: created.SigningSecret},
		}})
	case err == nil:
		http.Redirect(w, r, "/settings/notifications?notifications=saved", http.StatusSeeOther)
	default:
		s.refuseNotification(w, r, err, "add notification channel", "", form)
	}
}

// updateNotification answers POST /settings/notifications/{id}, for a row's
// edit form (which posts a name) and for its Pause and Resume forms (which post
// only "paused"). An empty URL or token keeps the one stored.
func (s *server) updateNotification(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	id := r.PathValue("id")
	var patch service.NotificationChannelPatch
	form := notificationForm{}
	if r.PostForm.Has("name") {
		form = readNotificationForm(r, r.PostFormValue("kind"))
		patch.Name = &form.Name
		events := formEvents(form)
		patch.Events = &events
		switch form.Kind {
		case notify.KindEmail:
			email, err := form.emailSettings()
			if err != nil {
				s.refuseNotification(w, r, err, "update notification channel", id, form)
				return
			}
			patch.Email = email
			// An empty password keeps the one stored, unless the box says to remove it.
			if password := r.PostFormValue("password"); password != "" {
				patch.Password = &password
			} else if r.PostFormValue("removePassword") == "1" {
				none := ""
				patch.Password = &none
			}
		case notify.KindTelegram:
			patch.Telegram = form.telegramSettings()
			if token := r.PostFormValue("botToken"); token != "" {
				patch.Token = &token
			}
		default:
			if form.URL != "" {
				patch.URL = &form.URL
			}
			if token := r.PostFormValue("token"); token != "" {
				patch.Token = &token
			}
		}
	}
	if r.PostForm.Has("paused") {
		paused := r.PostFormValue("paused") == "1"
		patch.Paused = &paused
	}

	_, err := s.svc.UpdateNotificationChannel(r.Context(), id, patch)
	if err == nil {
		http.Redirect(w, r, "/settings/notifications?notifications=saved", http.StatusSeeOther)
		return
	}
	s.refuseNotification(w, r, err, "update notification channel", id, form)
}

// refuseNotification draws the page again after the service refused a form: a
// field error goes under its input with what was typed (minus any secret), a
// missing channel goes back to the page, and anything else is an alert or a
// server error. editID is the channel whose edit form was posted, "" for the
// add form.
func (s *server) refuseNotification(w http.ResponseWriter, r *http.Request, err error, what, editID string, form notificationForm) {
	var fieldErr *app.FieldError
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, service.ErrNotAWebhook):
		http.Redirect(w, r, "/settings/notifications", http.StatusSeeOther)
	case errors.As(err, &fieldErr):
		form = echoable(form)
		form.Field, form.Err = notificationInputName(form.Kind, fieldErr.Field), &fieldError{Text: notificationFieldMessage(fieldErr, form.Kind)}
		data := notificationsData{Add: form}
		if editID != "" {
			data = notificationsData{EditID: editID, Edit: form}
		}
		s.renderSettings(w, r, entryNotifications, settingsData{Notifications: data})
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't keep the notification channel's secrets", "error", err)
		s.renderSettings(w, r, entryNotifications, settingsData{Notifications: notificationsData{
			Alert: &fieldError{Text: "Nomad couldn't keep the channel's secret, so nothing was saved: " + err.Error()},
		}})
	default:
		s.serverError(w, r, what, err)
	}
}

// testNotification answers POST /settings/notifications/{id}/test. The message
// is sent before the answer; the page it goes back to announces the outcome.
func (s *server) testNotification(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	_, err := s.svc.TestNotificationChannel(r.Context(), id)
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/notifications?notified="+url.QueryEscape(id), http.StatusSeeOther)
	case errors.Is(err, store.ErrNotFound):
		http.Redirect(w, r, "/settings/notifications", http.StatusSeeOther)
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't read the notification channel's secrets", "error", err)
		s.renderSettings(w, r, entryNotifications, settingsData{Notifications: notificationsData{
			Alert: &fieldError{Text: "Nomad couldn't give the channel's secret, so no message was sent: " + err.Error()},
		}})
	default:
		s.serverError(w, r, "test notification channel", err)
	}
}

// rotateNotification answers POST /settings/notifications/{id}/secret. The new
// signing secret is shown once, in this response.
func (s *server) rotateNotification(w http.ResponseWriter, r *http.Request, session string) {
	id := r.PathValue("id")
	secret, err := s.svc.RotateNotificationSecret(r.Context(), id)
	switch {
	case err == nil:
		name := id
		if channels, lerr := s.svc.NotificationChannels(r.Context()); lerr == nil {
			for _, c := range channels {
				if c.ID == id {
					name = c.Name
				}
			}
		}
		s.renderSettings(w, r, entryNotifications, settingsData{Notifications: notificationsData{Secret: &signingSecretNotice{Name: name, Value: secret}}})
	case errors.Is(err, store.ErrNotFound), errors.Is(err, service.ErrNotAWebhook):
		http.Redirect(w, r, "/settings/notifications", http.StatusSeeOther)
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't keep the new signing secret", "error", err)
		s.renderSettings(w, r, entryNotifications, settingsData{Notifications: notificationsData{
			Alert: &fieldError{Text: "Nomad couldn't keep the new secret, so the old one still works: " + err.Error()},
		}})
	default:
		s.serverError(w, r, "rotate notification secret", err)
	}
}

// removeNotification answers POST /settings/notifications/{id}/remove. The page
// asks first (a <details>); this is the post it sends.
func (s *server) removeNotification(w http.ResponseWriter, r *http.Request, session string) {
	err := s.svc.DeleteNotificationChannel(r.Context(), r.PathValue("id"))
	switch {
	case err == nil, errors.Is(err, store.ErrNotFound):
		http.Redirect(w, r, "/settings/notifications", http.StatusSeeOther)
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't remove the notification channel's secrets", "error", err)
		s.renderSettings(w, r, entryNotifications, settingsData{Notifications: notificationsData{Alert: &fieldError{Text: err.Error()}}})
	default:
		s.serverError(w, r, "remove notification channel", err)
	}
}

// sendsLine is the "Sends" cell: "Problems, failed deploys", whichever are on.
func sendsLine(events []string) string {
	var parts []string
	if slices.Contains(events, service.EventProblems) {
		parts = append(parts, "Problems")
	}
	if slices.Contains(events, service.EventDeployFailed) {
		parts = append(parts, "failed deploys")
	}
	if slices.Contains(events, service.EventDeploySucceeded) {
		parts = append(parts, "successful deploys")
	}
	if len(parts) > 0 {
		parts[0] = strings.ToUpper(parts[0][:1]) + parts[0][1:]
	}
	return strings.Join(parts, ", ")
}

// eventsWords says in plain English what a channel sends when it doesn't ask for all
// three events; "" when it asks for all of them (the kind alone says enough).
func eventsWords(events []string) string {
	problems := slices.Contains(events, service.EventProblems)
	failed := slices.Contains(events, service.EventDeployFailed)
	succeeded := slices.Contains(events, service.EventDeploySucceeded)
	switch {
	case problems && failed && succeeded:
		return ""
	case problems && failed:
		return "deploy failures and problems"
	case problems && succeeded:
		return "deploy successes and problems"
	case failed && succeeded:
		return "deploy failures and successes"
	case problems:
		return "problems only"
	case failed:
		return "deploy failures only"
	case succeeded:
		return "deploy successes only"
	}
	return "" // none: the service refuses that, so there is nothing to say
}

// kindLine is "Webhook", or "Webhook · problems only" when the channel sends less than everything.
func kindLine(kind string, events []string) string {
	if words := eventsWords(events); words != "" {
		return kindLabel(kind) + " · " + words
	}
	return kindLabel(kind)
}

// kindIcon is the icon a channel's kind is drawn with.
func kindIcon(kind string) string {
	switch kind {
	case notify.KindSlack:
		return "hash"
	case notify.KindDiscord:
		return "message-square"
	case notify.KindNtfy:
		return "bell"
	case notify.KindEmail:
		return "mail"
	case notify.KindTelegram:
		return "send"
	}
	return "webhook"
}

// notificationsSection fills in the rows of the page and the notice the request
// carries, keeping what the handler set. The rows' marks are
// notificationsState's, the same function the nav counts with; channels with a
// problem come first, then the others by name.
func notificationsSection(now time.Time, facts settingsFacts, st entryState, data notificationsData) notificationsData {
	data.Rows = make([]notificationRow, 0, len(facts.Channels))
	for i, c := range facts.Channels {
		row := notificationRow{
			ID: c.ID, Name: c.Name, Kind: c.Kind, KindLabel: kindLabel(c.Kind), KindLine: kindLine(c.Kind, c.Events), Address: c.Address, Icon: kindIcon(c.Kind),
			Mark: st.Rows[i], Paused: c.Paused, HasToken: c.HasToken, Sends: sendsLine(c.Events),
		}
		// A webhook's URL is shown, so it fills the edit form; a Slack, Discord or
		// ntfy address is a secret, so that field starts empty.
		row.Edit = formFromEvents(notificationForm{Kind: c.Kind, Name: c.Name}, c.Events)
		if c.Kind == notify.KindWebhook {
			row.Edit.URL = c.Address
		}
		if e := c.Email; e != nil {
			row.Edit.SMTPHost, row.Edit.SMTPSecurity, row.Edit.SMTPUsername, row.Edit.FromAddress = e.Host, e.Security, e.Username, e.From
			row.Edit.SMTPPort, row.Edit.ToAddresses = strconv.Itoa(e.Port), strings.Join(e.To, ", ")
		}
		if t := c.Telegram; t != nil {
			row.Edit.ChatID, row.Edit.APIURL = t.ChatID, t.APIURL
		}
		if c.ID == data.EditID {
			row.Edit, row.Open = data.Edit, true
			row.Edit.Kind = c.Kind
		}

		for _, d := range facts.Deliveries[c.ID] {
			at := d.CreatedAt
			if d.FinishedAt != nil {
				at = *d.FinishedAt
			}
			line := deliveryRow{When: whenOf(now, &at, ""), Title: d.Title}
			switch d.Status {
			case store.DeliverySent:
				line.OK, line.Detail = true, "sent"
			case store.DeliveryFailed:
				line.Failed, line.Error, line.Detail = true, d.Error, "failed ("+plural(d.Attempts, "attempt")+")"
				if d.Error != "" {
					line.Detail += ": " + d.Error
				}
			default:
				line.Detail = "queued"
			}
			row.Deliveries = append(row.Deliveries, line)
		}
		row.Last = lastOf(now, facts.Deliveries[c.ID])
		data.Rows = append(data.Rows, row)
	}
	slices.SortStableFunc(data.Rows, func(a, b notificationRow) int {
		if d := markRank(a.Mark) - markRank(b.Mark); d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})

	// The notice the redirect after a save or a test asks for.
	switch {
	case data.Saved:
		data.Notice = &notificationNotice{Text: "Saved."}
	case data.Notified != "":
		for _, row := range data.Rows {
			if row.ID != data.Notified || len(row.Deliveries) == 0 {
				continue
			}
			latest := row.Deliveries[0]
			if latest.OK {
				data.Notice = &notificationNotice{Text: row.Name + " accepted the test message."}
			} else {
				data.Notice = &notificationNotice{Text: row.Name + " didn't accept the test message: " + strings.TrimSuffix(latest.Error, ".") + ".", Bad: true}
			}
		}
	}

	// A fresh add form starts with ntfy, problems and failed deploys on.
	if data.Add.Kind == "" {
		data.Add = notificationForm{Kind: notify.KindNtfy, Problems: true, DeployFailed: true}
	}
	return data
}
