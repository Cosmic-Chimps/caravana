package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/dns"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Operator › Console address (feature 051, specs/051-console-address/contracts/page.md): the domain the
// console answers on, what depends on it, the form that checks a new one and the panel that follows the
// change. No script is needed: the form, Switch and Cancel are plain posts that answer 303, and the
// panel reloads itself with a <noscript> refresh. With script the panel is read again every 3 seconds
// from /parts/ops/console-address while the change is checking or switching, and the part answers 286
// when it has settled, which htmx takes as "stop polling".

// The sentences of the page that more than one place draws.
const (
	consoleSigninAgain = "You sign in again at the new address."
	consoleFollowText  = "Notification links and joined servers follow by themselves."
	// consoleRefreshSeconds is how often a page without script reloads itself while a change is open.
	consoleRefreshSeconds = 5
)

// consoleDay is one choice of "Keep the old address for".
type consoleDay struct {
	Value    int
	Label    string
	Selected bool
}

// consoleForm is the change form with what was typed. Zone is set when a connected DNS zone covers the
// domain and it has no record: the page then offers a second button that lets Caravana make it.
type consoleForm struct {
	Domain formField
	Days   []consoleDay
	Hint   string
	Notice sentence
	Zone   string
}

// fillDays marks the chosen window; the default is 30 days.
func (f *consoleForm) fillDays(chosen int) {
	f.Days = nil
	for _, d := range []struct {
		n     int
		label string
	}{{0, "Don't keep it"}, {7, "7 days"}, {30, "30 days"}, {90, "90 days"}} {
		f.Days = append(f.Days, consoleDay{Value: d.n, Label: d.label, Selected: d.n == chosen})
	}
}

// consoleCheckRow is one check of the change panel.
type consoleCheckRow struct {
	Label   string
	Status  statusView
	Message string
}

// consoleChangeView is the change panel: the checks, one sentence of where it stands and the actions
// that fit.
type consoleChangeView struct {
	ID      string
	Domain  string
	Status  statusView
	Checks  []consoleCheckRow
	Summary sentence
	Reason  string // the one line of a change that didn't go through; "" for none
	Link    string // the new address, for a change that is switching or applied
	// Open is true while the panel is polled: the change is checking or switching.
	Open      bool
	CanSwitch bool
	CanCancel bool
	PartHref  string
	Refusal   sentence // why a post was turned down
}

// consoleNow is the address as the page opens with it.
type consoleNow struct {
	Domain, URL, Issuer string
	Old                 []consoleOld
}

type consoleOld struct {
	Domain string
	Until  clockTime
}

// consoleAddressPage is the page.
type consoleAddressPage struct {
	pageData
	Now          consoleNow
	Deps         []sentence
	CanChange    bool
	CannotChange string
	ShowForm     bool
	Form         consoleForm
	Change       *consoleChangeView
	Refusal      sentence
}

// consoleStatus is the status pill of a change.
func consoleStatus(status string) statusView {
	switch status {
	case store.ConsoleChecking:
		return statusView{Kind: "rolling", Word: "Checking"}
	case store.ConsoleReady:
		return statusView{Kind: "healthy", Word: "Ready to switch"}
	case store.ConsoleCheckFailed:
		return statusView{Kind: "failed", Word: "Check failed"}
	case store.ConsoleSwitching:
		return statusView{Kind: "rolling", Word: "Switching"}
	case store.ConsoleApplied:
		return statusView{Kind: "healthy", Word: "Switched"}
	case store.ConsoleFailed:
		return statusView{Kind: "failed", Word: "Failed"}
	case store.ConsoleRefused:
		return statusView{Kind: "failed", Word: "Refused"}
	case store.ConsoleLost:
		return statusView{Kind: "warning", Word: "No answer"}
	}
	return statusView{Kind: "idle", Word: "Cancelled"}
}

// consoleCheckStatus is the pill of one check; a waiting check of a change that has ended is idle.
func consoleCheckStatus(state string, open bool) statusView {
	switch state {
	case "ok":
		return statusView{Kind: "healthy", Word: "ok"}
	case "failed":
		return statusView{Kind: "failed", Word: "failed"}
	case "waiting":
		if open {
			return statusView{Kind: "rolling", Word: "waiting"}
		}
		return statusView{Kind: "idle", Word: "not reached"}
	}
	return statusView{Kind: "idle", Word: "skipped"}
}

var consoleCheckLabels = map[string]string{"hostname": "Host name", "dns": "DNS", "route": "Traefik route", "certificate": "Certificate"}

// consoleChangeOf is the panel of one stored change.
func consoleChangeOf(now time.Time, c store.ConsoleAddressChange) *consoleChangeView {
	open := c.Status == store.ConsoleChecking || c.Status == store.ConsoleSwitching
	v := &consoleChangeView{ID: c.ID, Domain: c.Domain, Status: consoleStatus(c.Status), Open: open, Reason: c.Reason,
		CanSwitch: c.Status == store.ConsoleReady,
		CanCancel: c.Status == store.ConsoleChecking || c.Status == store.ConsoleReady || c.Status == store.ConsoleCheckFailed,
		PartHref:  "/parts/ops/console-address?id=" + url.QueryEscape(c.ID)}
	for _, k := range c.Checks {
		label := consoleCheckLabels[k.Name]
		if label == "" {
			label = k.Name
		}
		v.Checks = append(v.Checks, consoleCheckRow{Label: label, Status: consoleCheckStatus(k.State, c.Status == store.ConsoleChecking), Message: k.Message})
	}
	link := "https://" + c.Domain + "/"
	switch c.Status {
	case store.ConsoleChecking:
		v.Summary = join(plainS("Checking "), sentence{mono(c.Domain)}, plainS(": Caravana is waiting for Traefik to route it and for its certificate, up to 5 minutes. Nothing else has changed."))
	case store.ConsoleReady:
		v.Summary = join(plainS("All checks passed for "), sentence{mono(c.Domain)}, plainS(". Switching restarts Caravana at the new address. "+consoleSigninAgain))
	case store.ConsoleCheckFailed:
		v.Summary = join(plainS("The checks for "), sentence{mono(c.Domain)}, plainS(" didn't pass, so nothing changed and the domain is out of Traefik's route. Fix what is missing, then check again."))
	case store.ConsoleSwitching:
		v.Link = link
		v.Summary = join(plainS("Waiting for the server agent (up to a minute). Caravana restarts at "), sentence{mono(link)}, plainS("; sign in there."))
	case store.ConsoleApplied:
		v.Link = link
		v.Summary = join(plainS("Caravana now answers at "), sentence{mono(c.Domain)}, plainS("."))
		if c.RedirectDays == 0 || c.RedirectUntil.IsZero() {
			v.Summary = join(v.Summary, plainS(" The old address is no longer served."))
		} else {
			v.Summary = join(v.Summary, plainS(" "), sentence{mono(c.OldDomain)}, plainS(" redirects its pages here, and still answers the API, until "+clockOf(now, c.RedirectUntil).Text+"."))
		}
	case store.ConsoleFailed:
		v.Summary = join(plainS("The server agent couldn't finish the change and put the old address back. Nothing changed."))
	case store.ConsoleRefused:
		v.Summary = join(plainS("The server agent refused the change, and changed nothing."))
	case store.ConsoleLost:
		v.Summary = join(plainS("The server agent reported no outcome in 5 minutes. Look at its log on the server (journalctl -u caravana-node); the old address is still routed."))
	default:
		v.Summary = join(plainS("The change was cancelled and "), sentence{mono(c.Domain)}, plainS(" is out of Traefik's route. Nothing else changed."))
	}
	return v
}

// consoleDeps are the "Depends on it" sentences, from the counts the service gives.
func consoleDeps(v service.ConsoleAddressView) []sentence {
	d := v.Dependents
	var out []sentence
	if d.Passkeys > 0 {
		out = append(out, join(plainS(pluralN(d.Passkeys, "passkey", "passkeys")+" belong to "), sentence{mono(v.Domain)},
			plainS(": they won't work at the new address. Password sign-in stays on there until you add a passkey.")))
	}
	out = append(out, plainS(consoleSigninAgain))
	if n := d.Webhooks + d.DeployTokens + d.APITokens; n > 0 {
		var held []string
		for _, c := range []struct {
			n         int
			one, many string
		}{{d.Webhooks, "webhook token", "webhook tokens"}, {d.DeployTokens, "deploy token", "deploy tokens"}, {d.APITokens, "API token", "API tokens"}} {
			if c.n > 0 {
				held = append(held, pluralN(c.n, c.one, c.many))
			}
		}
		out = append(out, join(plainS(strings.Join(held, ", ")+". Their senders (CI's CARAVANA_URL, Git hosts' webhook URLs) still hold "),
			sentence{mono(v.Domain)}, plainS(": update them before the old address stops answering.")))
	}
	if v.Issuer != "" {
		out = append(out, join(plainS("Anything that trusts the issuer "), sentence{mono(v.Issuer)},
			plainS(", such as Bella's Nomad attestor, needs the new one.")))
	}
	return append(out, plainS(consoleFollowText))
}

func pluralN(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// loadConsoleAddress reads the service and draws what the page needs.
func (s *server) loadConsoleAddress(r *http.Request) (consoleAddressPage, error) {
	v, err := s.svc.ConsoleAddress(r.Context())
	if err != nil {
		return consoleAddressPage{}, err
	}
	now := s.now().UTC()
	p := consoleAddressPage{CanChange: v.CanChange, CannotChange: v.CannotChange, Deps: consoleDeps(v),
		Now: consoleNow{Domain: v.Domain, URL: v.PublicURL, Issuer: v.Issuer}}
	for _, o := range v.OldDomains {
		p.Now.Old = append(p.Now.Old, consoleOld{Domain: o.Domain, Until: clockOf(now, o.Until)})
	}
	if v.Change != nil {
		p.Change = consoleChangeOf(now, *v.Change)
	}
	p.Form = consoleForm{Domain: formField{ID: "console-domain"}, Hint: v.SSLIPHint}
	p.Form.fillDays(service.DefaultConsoleRedirectDays)
	p.ShowForm = v.CanChange && (p.Change == nil || !store.ConsoleChangeOpen(v.Change.Status))
	return p, nil
}

// drawConsoleAddress draws the page with a status, the form as it was posted (nil for a fresh one) and
// the refusal of an action that was turned down.
func (s *server) drawConsoleAddress(w http.ResponseWriter, r *http.Request, status int, form *consoleForm, refusal sentence) {
	p, err := s.loadConsoleAddress(r)
	if err != nil {
		s.serverError(w, r, "read the console's address", err)
		return
	}
	if form != nil {
		form.Hint = p.Form.Hint
		p.Form = *form
	}
	p.Refusal = refusal
	head, err := s.shellPage(r, "Console address", navOpsConsoleAddress, crumb{Label: "Console address"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	if p.Change != nil && p.Change.Open {
		head.Refresh = consoleRefreshSeconds
	}
	p.pageData = head
	s.render(w, status, "opsconsoleaddress", p)
}

// showConsoleAddress answers GET /ops/console-address.
func (s *server) showConsoleAddress(w http.ResponseWriter, r *http.Request, session string) {
	s.drawConsoleAddress(w, r, http.StatusOK, nil, nil)
}

// partConsoleAddress answers GET /parts/ops/console-address?id=: the change panel alone. Once the change
// is no longer checking or switching it answers 286, which htmx takes as "stop polling" and still swaps in.
func (s *server) partConsoleAddress(w http.ResponseWriter, r *http.Request, session string) {
	c, err := s.svc.ConsoleAddressChange(r.Context(), r.URL.Query().Get("id"))
	if err != nil && !errors.Is(err, service.ErrConsoleChangeNotFound) {
		s.log.Error("read the console address change", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var view *consoleChangeView
	if err == nil {
		view = consoleChangeOf(s.now().UTC(), c)
	} else {
		view = &consoleChangeView{Summary: plainS("This change is gone."), Status: statusView{Kind: "idle", Word: "Gone"}}
	}
	if view.Open {
		s.renderPart(w, "opsconsoleaddress", "console-change", view)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var buf strings.Builder
	if err := s.pages["opsconsoleaddress"].ExecuteTemplate(&buf, "console-change", view); err != nil {
		s.log.Error("render part", "part", "console-change", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(286)
	_, _ = w.Write([]byte(buf.String()))
}

// startConsoleChange answers POST /ops/console-address/changes: the name and its DNS are checked before
// anything is recorded; a refusal draws the page again with what was typed (400, 409, 422 or 502) and
// success is 303 to the page, whose panel follows the checks.
func (s *server) startConsoleChange(w http.ResponseWriter, r *http.Request, session string) {
	form := consoleForm{Domain: formField{ID: "console-domain", Value: strings.TrimSpace(r.PostFormValue("domain"))}}
	days := service.DefaultConsoleRedirectDays
	if raw := strings.TrimSpace(r.PostFormValue("redirectDays")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			n = -1 // refused by the service, naming redirectDays
		}
		days = n
	}
	form.fillDays(days)
	_, err := s.svc.StartConsoleAddressChange(r.Context(), form.Domain.Value, days, r.PostFormValue("createDNSRecord") != "")
	if err == nil {
		http.Redirect(w, r, "/ops/console-address", http.StatusSeeOther)
		return
	}
	status, refusal, ok := consoleStartRefusal(&form, err)
	if !ok {
		s.serverError(w, r, "start the console address change", err)
		return
	}
	s.drawConsoleAddress(w, r, status, &form, refusal)
}

// consoleStartRefusal puts a refusal of the start where it belongs: at the Domain field, at the window's
// select or as a notice above the form. ok is false for an error that isn't a refusal.
func consoleStartRefusal(form *consoleForm, err error) (status int, notice sentence, ok bool) {
	var fe *app.FieldError
	var taken *service.DomainTakenError
	var notHere *service.DomainNotHereError
	var prov *service.DNSProviderError
	onDomain := func(words string) {
		form.Domain.Error, form.Domain.Alert = plainS(words), true
	}
	switch {
	case errors.As(err, &fe):
		if fe.Field == "domain" {
			onDomain(refusalWords(errors.New(fe.Message)))
			return http.StatusBadRequest, nil, true
		}
		form.Notice = plainS(refusalWords(errors.New(fe.Message)))
		return http.StatusBadRequest, nil, true
	case errors.As(err, &taken):
		onDomain(taken.Host + " is already a domain of app " + taken.App + ". Remove it there first.")
		return http.StatusConflict, nil, true
	case errors.As(err, &notHere):
		onDomain(refusalWords(errors.New(notHere.Words)))
		form.Zone = notHere.Zone
		return http.StatusUnprocessableEntity, nil, true
	case errors.As(err, &prov):
		status = http.StatusUnprocessableEntity
		if errors.Is(prov, dns.ErrUnavailable) {
			status = http.StatusBadGateway
		}
		onDomain(refusalWords(errors.New(prov.Words)))
		return status, nil, true
	case errors.Is(err, service.ErrConsoleChangeOpen), errors.Is(err, service.ErrConsoleAddressUnsupported):
		return http.StatusConflict, plainS(refusalWords(err)), true
	}
	return 0, nil, false
}

// switchConsoleChange answers POST /ops/console-address/changes/{id}/switch: 303 to the page, which shows
// the change switching. A refusal draws the page with its reason: 409 for a change that isn't ready or no
// agent, 422 for a check that failed just now (the change stays ready), 404 for an unknown change.
func (s *server) switchConsoleChange(w http.ResponseWriter, r *http.Request, session string) {
	_, err := s.svc.SwitchConsoleAddress(r.Context(), r.PathValue("id"))
	if err == nil {
		http.Redirect(w, r, "/ops/console-address", http.StatusSeeOther)
		return
	}
	s.consoleActionRefusal(w, r, err)
}

// cancelConsoleChange answers POST /ops/console-address/changes/{id}/cancel: 303 to the page.
func (s *server) cancelConsoleChange(w http.ResponseWriter, r *http.Request, session string) {
	_, err := s.svc.CancelConsoleAddressChange(r.Context(), r.PathValue("id"))
	if err == nil {
		http.Redirect(w, r, "/ops/console-address", http.StatusSeeOther)
		return
	}
	s.consoleActionRefusal(w, r, err)
}

// consoleActionRefusal draws the page again with the reason a switch or a cancel was turned down.
func (s *server) consoleActionRefusal(w http.ResponseWriter, r *http.Request, err error) {
	var notHere *service.DomainNotHereError
	var check *service.ConsoleCheckError
	switch {
	case errors.Is(err, service.ErrConsoleChangeNotFound):
		s.drawConsoleAddress(w, r, http.StatusNotFound, nil, plainS("No such change of the console's address: it may have been removed."))
	case errors.As(err, &notHere):
		s.drawConsoleAddress(w, r, http.StatusUnprocessableEntity, nil, plainS(refusalWords(errors.New(notHere.Words))))
	case errors.As(err, &check):
		s.drawConsoleAddress(w, r, http.StatusUnprocessableEntity, nil, plainS(refusalWords(errors.New(check.Words))))
	case errors.Is(err, service.ErrConsoleChangeNotReady), errors.Is(err, service.ErrConsoleChangeNotCancellable), errors.Is(err, service.ErrNoAgent):
		s.drawConsoleAddress(w, r, http.StatusConflict, nil, plainS(refusalWords(err)))
	default:
		s.serverError(w, r, "change the console's address", err)
	}
}
