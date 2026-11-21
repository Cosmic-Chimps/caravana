package ui

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/dns"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Settings › DNS providers (feature 048): the zones Caravana may make DNS
// records in, one connection row each (today only Cloudflare). Every form is a
// plain post, so the page works without script. The API token is write-only, as
// the secret providers' keys are: no struct here has a field for it, so no page
// can show it and a refused save never puts it back in a form.

// dnsForm is what a zone form shows: the settings as saved or as typed, and the
// field a refusal names. Never the token.
type dnsForm struct {
	Zone    string
	Proxied bool
	Editing bool   // the zone is fixed, and the token may stay empty
	Field   string // the field at fault, after a refusal
	Err     *fieldError
}

// dnsFormViewT is a zone form as the dns-fields template draws it: the Zone and
// token inputs, which the shared provider-fields template knows, and the proxy
// checkbox.
type dnsFormViewT struct {
	Form    providerFormView
	ProxyID string
	Proxied bool
}

// dnsFormView builds the inputs of a zone form. idPrefix keeps the ids of the
// add form and of each row's edit form apart.
func dnsFormView(f dnsForm, idPrefix string) dnsFormViewT {
	zone := providerFieldView{ID: idPrefix + "-zone", Name: "zone", Kind: "text", Label: "Zone", Value: f.Zone,
		Placeholder: "example.com", ReadOnly: f.Editing, Required: true,
		Hint: "The domain Cloudflare serves. Domains under it get their records from Caravana."}
	token := providerFieldView{ID: idPrefix + "-token", Name: "token", Kind: "password", Label: "Cloudflare API token",
		Required: !f.Editing, Hint: "Needs Zone › DNS › Edit for this zone, and nothing else."}
	if f.Editing {
		token.Hint = "Leave empty to keep the one stored."
	}
	if f.Err != nil {
		switch f.Field {
		case "zone":
			zone.Err = f.Err.Text
		case "token":
			token.Err = f.Err.Text
		}
	}
	return dnsFormViewT{Form: providerFormView{Fields: []providerFieldView{zone, token}}, ProxyID: idPrefix + "-proxied", Proxied: f.Proxied}
}

// dnsListedRecord is one record the last test listed.
type dnsListedRecord struct {
	Type, Name, Content string
	Proxied             string // "yes" or "no"
	Made                string // "made by Caravana for web", "" for a hand-made record
}

// dnsRow is one zone, one connection row.
type dnsRow struct {
	Zone string
	Mark string // dnsZonesState's: failed, healthy or idle
	Kind string // "Cloudflare · proxied"
	// Made is "3 records made by Caravana" or "no records yet".
	Made  string
	Count int
	// State and Event are the row's two lines: the last test's outcome in
	// Caravana's words, and when it happened.
	State, Event string
	StateFailed  bool
	Detail       string // the failed test's reason, under the row
	Test         *when
	Edit         dnsForm
	Open         bool // the edit form is shown open: it was refused, or asked for with ?edit=
	// Listed are the zone's records as the test just run found them; nil unless
	// this row was tested by the post that drew the page.
	Listed []dnsListedRecord
	Tested bool
}

// dnsData is the whole section.
type dnsData struct {
	Rows   []dnsRow
	Notice *providersNotice
	Alert  *fieldError // a refusal that names no field: Nomad being down
	Add    dnsForm
	// EditZone says which row's edit form is open; Edit is what it was refused
	// with (its Zone is empty when it wasn't refused).
	EditZone string
	Edit     dnsForm
	Saved    bool
	Removed  bool
	// Tested is the zone whose test result to announce, and Listed its records.
	Tested string
	Listed []dnsListedRecord
}

// showDNSZones answers GET /settings/dns. The notice comes from the redirect
// after a save or a removal; ?edit=<zone> opens that zone's edit form.
func (s *server) showDNSZones(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	s.renderSettings(w, r, entryDNS, settingsData{DNS: dnsData{
		Saved: q.Get("dns") == "saved", Removed: q.Get("dns") == "removed", EditZone: q.Get("edit"),
	}})
}

// saveDNSZone answers POST /settings/dns, for the add form and for a row's edit
// form (which also posts edit=1). An empty token keeps the one stored. A refusal
// draws the page again with what was typed, except the token.
func (s *server) saveDNSZone(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	edit := r.PostFormValue("edit") == "1"
	form := dnsForm{Zone: strings.ToLower(strings.TrimSpace(r.PostFormValue("zone"))), Proxied: r.PostFormValue("proxied") == "1", Editing: edit}
	in := service.DNSZoneInput{Zone: form.Zone, Provider: service.DNSProviderCloudflare, Proxied: form.Proxied}
	// An empty token keeps the stored one (editing) or is refused as missing
	// (adding): the service says which, so an empty form value is not sent.
	if v := r.PostFormValue("token"); v != "" {
		in.Token = &v
	}

	_, _, err := s.svc.SaveDNSZone(r.Context(), in)
	if err == nil {
		http.Redirect(w, r, "/settings/dns?dns=saved", http.StatusSeeOther)
		return
	}

	data := dnsData{Add: form}
	if edit {
		data = dnsData{EditZone: form.Zone, Edit: form}
	}
	var fieldErr *app.FieldError
	var provErr *service.DNSProviderError
	status := http.StatusUnprocessableEntity
	switch {
	case errors.As(err, &fieldErr) && (fieldErr.Field == "zone" || fieldErr.Field == "token"):
		form.Field, form.Err = fieldErr.Field, &fieldError{Text: dnsFieldMessage(fieldErr)}
		if edit {
			data.Edit = form
		} else {
			data.Add = form
		}
	case errors.As(err, &fieldErr):
		data.Alert = &fieldError{Text: fieldErr.Message}
	case errors.As(err, &provErr):
		// "saved, but the old setting stays on N records": the zone is saved, and the
		// page says what is left to do. A redirect would lose the sentence.
		data = dnsData{Notice: &providersNotice{Text: provErr.Words + ".", Bad: true}}
		if errors.Is(provErr, dns.ErrUnavailable) {
			status = http.StatusBadGateway
		}
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't save the DNS zone's token", "zone", form.Zone, "error", err)
		data.Alert = &fieldError{Text: "Nomad couldn't keep the token, so nothing was saved: " + err.Error()}
		status = http.StatusBadGateway
	default:
		s.serverError(w, r, "save DNS zone", err)
		return
	}
	s.renderSettings(w, r, entryDNS, settingsData{DNS: data, Status: status})
}

// dnsFieldMessage is a field error as a sentence for the input it names. The
// service's messages about the setting itself ("is required…") follow its name.
func dnsFieldMessage(fe *app.FieldError) string {
	if fe.Field == "token" && (strings.HasPrefix(fe.Message, "is ") || strings.HasPrefix(fe.Message, "must ")) {
		return "The API token " + fe.Message
	}
	return fe.Message
}

// testDNSZone answers POST /settings/dns/test, with the zone in the form's
// "zone" field. The outcome is stored, and the page it answers announces it and
// lists the records the test found under the zone's row: they are read now and
// kept nowhere, so they are drawn in this response only.
func (s *server) testDNSZone(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	zone := strings.TrimSpace(r.PostFormValue("zone"))
	res, err := s.svc.TestDNSZone(r.Context(), zone)
	switch {
	case err == nil:
		var listed []dnsListedRecord
		for _, rec := range res.Records {
			l := dnsListedRecord{Type: rec.Type, Name: rec.Name, Content: rec.Content, Proxied: "no"}
			if rec.Proxied {
				l.Proxied = "yes"
			}
			if rec.Managed {
				l.Made = "made by Caravana for " + rec.App
			}
			listed = append(listed, l)
		}
		slices.SortStableFunc(listed, func(a, b dnsListedRecord) int {
			return strings.Compare(a.Name+" "+a.Type, b.Name+" "+b.Type)
		})
		s.renderSettings(w, r, entryDNS, settingsData{DNS: dnsData{Tested: res.Zone.Zone, Listed: listed}})
	case errors.Is(err, store.ErrNotFound):
		http.Redirect(w, r, "/settings/dns", http.StatusSeeOther)
	default:
		s.serverError(w, r, "test DNS zone", err)
	}
}

// removeDNSZone answers POST /settings/dns/remove, with the zone in the form's
// "zone" field. The page asks first (a <details>); this is the post it sends.
// The records Caravana made stay at the provider.
func (s *server) removeDNSZone(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	zone := strings.TrimSpace(r.PostFormValue("zone"))
	err := s.svc.DeleteDNSZone(r.Context(), zone)
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/dns?dns=removed", http.StatusSeeOther)
	case errors.Is(err, store.ErrNotFound):
		http.Redirect(w, r, "/settings/dns", http.StatusSeeOther)
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't remove the DNS zone's token", "zone", zone, "error", err)
		s.renderSettings(w, r, entryDNS, settingsData{DNS: dnsData{Alert: &fieldError{Text: err.Error()}}, Status: http.StatusBadGateway})
	default:
		s.serverError(w, r, "remove DNS zone", err)
	}
}

// dnsKind is the line under a zone's name.
func dnsKind(z store.DNSZone) string {
	if z.Proxied {
		return "Cloudflare · proxied"
	}
	return "Cloudflare · direct"
}

// dnsMade says how many records Caravana made in a zone.
func dnsMade(n int) string {
	if n == 0 {
		return "no records yet"
	}
	return plural(n, "record") + " made by Caravana"
}

// zoneNames are the zones' names, for a sentence that lists them.
func zoneNames(zones []service.DNSZoneInfo) []string {
	out := make([]string, len(zones))
	for i, z := range zones {
		out[i] = z.Zone
	}
	return out
}

// dnsSection fills in the rows of the page and the notice the request carries,
// keeping what the handler set. The rows' marks are dnsZonesState's, the same
// function the nav counts with; failing zones come first, then the others by name.
func dnsSection(now time.Time, facts settingsFacts, st entryState, data dnsData) dnsData {
	data.Rows = make([]dnsRow, 0, len(facts.DNSZones))
	for i, z := range facts.DNSZones {
		row := dnsRow{Zone: z.Zone, Mark: st.Rows[i], Kind: dnsKind(z.DNSZone), Made: dnsMade(z.Records), Count: z.Records,
			Edit: dnsForm{Zone: z.Zone, Proxied: z.Proxied, Editing: true}}
		switch t := z.LastTest; {
		case t != nil && t.OK:
			w := whenOf(now, &t.At, "")
			row.State, row.Event, row.Test = t.Message, "tested "+w.Text, &w
		case t != nil:
			w := whenOf(now, &t.At, "")
			row.State, row.StateFailed, row.Event, row.Test, row.Detail = "can't be reached", true, "tested "+w.Text, &w, t.Message
		case z.UpdatedAt.After(z.CreatedAt):
			row.State, row.Event = "changed · not tested since", "saved "+whenOf(now, &z.UpdatedAt, "").Text
		default:
			row.State, row.Event = "not tested yet", "added "+whenOf(now, &z.CreatedAt, "").Text
		}
		if z.Zone == data.EditZone {
			row.Open = true
			if data.Edit.Zone != "" {
				row.Edit = data.Edit
			}
		}
		if z.Zone == data.Tested {
			row.Tested, row.Listed = true, data.Listed
		}
		data.Rows = append(data.Rows, row)
	}
	sortStable(data.Rows, func(r dnsRow) int { return markRank(r.Mark) })

	switch {
	case data.Saved:
		data.Notice = &providersNotice{Text: "Saved. Test it to see that Caravana can reach the zone."}
	case data.Removed:
		data.Notice = &providersNotice{Text: "Removed. The records Caravana made stay at Cloudflare."}
	case data.Tested != "":
		for _, row := range data.Rows {
			switch {
			case row.Zone != data.Tested || row.Test == nil:
			case !row.StateFailed:
				data.Notice = &providersNotice{Text: row.Zone + ": " + row.State + "."}
			default:
				data.Notice = &providersNotice{Text: row.Zone + " can't be reached: " + strings.TrimSuffix(row.Detail, ".") + ".", Bad: true}
			}
		}
	}
	return data
}
