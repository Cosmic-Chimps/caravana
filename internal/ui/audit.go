package ui

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Settings › Audit log (feature 057): who did what, newest first, for owners and admins. The page
// is one GET form of filters, 100 entries a page with an "Older" link by cursor, each entry's
// changes in a <details>, a small export form (a range of days, CSV) and the retention form for
// owners. Nothing here holds a secret: the service's entries hold none (the diff redacts them), and
// every answer is no-store. No page edits or deletes an entry. Forms and links work without script.
//
// Every console form post runs inside an audit scope (auditScope below) so the service's notes
// become one entry per post. The console has no fallback entry: a post nothing noted (a search, a
// preview, a refused or unchanged save) leaves none.

// auditScope wraps a POST route's handler in an audit scope: the console door, the client's
// address and the operation. The scope has no fallback; the service's notes are the whole of the
// console's coverage. Finish runs once the handler returns, with the status it wrote.
func (s *server) auditScope(rt route, next http.HandlerFunc) http.HandlerFunc {
	if s.svc == nil {
		return next
	}
	operation := rt.method + " " + rt.path
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, sc := s.svc.BeginAudit(r.Context(), audit.DoorConsole, s.proxies.ClientAddress(r), operation)
		sc.NoFallback()
		sw := &statusCapture{ResponseWriter: w}
		defer func() { sc.Finish(sw.statusCode(), audit.Resource{}) }()
		next(sw, r.WithContext(ctx))
	}
}

// statusCapture remembers the status a handler wrote and passes Flush and Hijack through.
type statusCapture struct {
	http.ResponseWriter
	status int
}

func (w *statusCapture) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusCapture) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// statusCode is the status written; 200 for a handler that wrote nothing.
func (w *statusCapture) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *statusCapture) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

// Unwrap lets http.ResponseController reach the writer underneath.
func (w *statusCapture) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ---- the page's data -------------------------------------------------------------------------

// auditDay is the layout of a date input.
const auditDay = "2006-01-02"

// auditPageSize is how many entries a page lists.
const auditPageSize = 100

// auditData is the Audit log page.
type auditData struct {
	// Filters are what the form holds, by name; Errors the message of each refused one.
	Filters map[string]string
	Errors  map[string]string
	Actions []selectOption
	Kinds   []selectOption
	Rows    []auditRow
	// Older is the address of the next page, "" on the last; Filtered says a filter is set.
	Older    string
	Filtered bool
	// Export is the export form: the range and what refused it.
	ExportFrom, ExportTo string
	ExportErrors         map[string]string
	// Hidden carries the filters other than the range into the export form.
	Hidden []auditHidden
	// Retention is the days kept; CanSet says the viewer is an owner; RetentionValue is what the
	// field holds (what a refused post typed) and RetentionError its message.
	Retention      int
	CanSet         bool
	RetentionValue string
	RetentionError string
	Notice         sentence
}

// auditRow is one entry, ready to draw.
type auditRow struct {
	ID         int64
	At         clockTime
	Actor      string
	Door       string
	Action     string
	Kind       string // the resource's kind in words
	Name       string // its name (mono)
	Place      string // "project › environment"
	Href       string // the resource's page while it exists
	Address    string
	Fallback   bool
	Changes    []auditChangeRow
	Related    []string
	Record     string // "deployment 01J…"
	RecordHref string
}

// auditHidden is one filter the export form passes on.
type auditHidden struct{ Name, Value string }

// auditChangeRow is one line of an entry's changes: the path in mono, then the words.
type auditChangeRow struct {
	Path, Words string
}

// auditFilterNames are the form's filter inputs, in the order they show.
var auditFilterNames = []string{"actor", "action", "kind", "resource", "project", "environment", "from", "to"}

// auditFilters reads the filters from a query or form. A date that doesn't parse is an error at its
// input; the day is UTC: From is its first instant, To its last.
func auditFilters(v url.Values) (service.AuditQuery, map[string]string, map[string]string) {
	vals := map[string]string{}
	for _, n := range auditFilterNames {
		vals[n] = strings.TrimSpace(v.Get(n))
	}
	errs := map[string]string{}
	q := service.AuditQuery{Actor: vals["actor"], Action: vals["action"], Kind: vals["kind"], Resource: vals["resource"],
		Project: vals["project"], Environment: vals["environment"], Limit: auditPageSize}
	if d := vals["from"]; d != "" {
		if t, err := time.Parse(auditDay, d); err != nil {
			errs["from"] = "Use a date like 2026-11-25."
		} else {
			q.From = t.UTC()
		}
	}
	if d := vals["to"]; d != "" {
		if t, err := time.Parse(auditDay, d); err != nil {
			errs["to"] = "Use a date like 2026-11-25."
		} else {
			q.To = t.UTC().Add(24*time.Hour - time.Nanosecond)
		}
	}
	if b := strings.TrimSpace(v.Get("before")); b != "" {
		if n, err := strconv.ParseInt(b, 10, 64); err != nil || n < 1 {
			errs["before"] = "That page isn't one."
		} else {
			q.Before = n
		}
	}
	return q, vals, errs
}

// fieldMessageFor words a service refusal of a filter for its input.
func auditFieldMessage(fe *app.FieldError) string {
	return capitalize(fe.Message) + "."
}

// auditOptions are the Action and Kind selects: any, then each value of the closed list.
func auditOptions(selected string, values []string) []selectOption {
	out := []selectOption{{Value: "", Label: "Any", Selected: selected == ""}}
	for _, v := range values {
		out = append(out, selectOption{Value: v, Label: strings.ReplaceAll(v, "_", " "), Selected: v == selected})
	}
	return out
}

// ---- the actor, resource and change words ---------------------------------------------------

// auditActorWords says who acted, as the contract's examples do.
func auditActorWords(a audit.Actor) string {
	switch a.Kind {
	case audit.ActorPerson:
		return orWord(a.Email, "someone")
	case audit.ActorToken:
		if a.Email == "" {
			return "token " + a.Token
		}
		return a.Email + " through token " + a.Token
	case audit.ActorDeployToken:
		if a.Email == "" {
			return "deploy token " + a.Token
		}
		return "deploy token " + a.Token + " (" + a.Email + ")"
	case audit.ActorWebhook:
		if a.Token == "" {
			return "a webhook"
		}
		return "the webhook of " + a.Token
	case audit.ActorCaravana:
		return "Caravana"
	case audit.ActorHost:
		return "the host's command line"
	case audit.ActorAnonymous:
		if a.Email != "" {
			return "someone who typed " + a.Email
		}
		return "someone"
	}
	return orWord(a.Email, string(a.Kind))
}

func orWord(s, none string) string {
	if s == "" {
		return none
	}
	return s
}

// auditKindWords is a resource kind in words.
func auditKindWords(k audit.Kind) string {
	switch k {
	case audit.KindWebhookToken:
		return "webhook token"
	case audit.KindInstallation:
		return "the installation"
	case audit.KindSettings:
		return "settings"
	}
	return strings.ReplaceAll(string(k), "_", " ")
}

// auditChangeWords says one change. A change of secret material has no from or to: it reads "set",
// "changed" or "removed", never a value. A change with values reads "added X", "removed X" or "X → Y".
func auditChangeWords(c audit.Change) string {
	hasFrom, hasTo := c.From != "", c.To != ""
	switch c.Op {
	case audit.OpAdded:
		if !hasTo {
			return "set"
		}
		return "added " + c.To
	case audit.OpRemoved:
		if !hasFrom {
			return "removed"
		}
		return "removed " + c.From
	}
	switch {
	case hasFrom && hasTo:
		return c.From + " → " + c.To
	case hasTo:
		return c.To
	case hasFrom:
		return c.From
	}
	return "changed"
}

// ---- links ----------------------------------------------------------------------------------

// auditLinks resolves the links of the entries on a page against the apps that still exist: an
// entry's app, and for a backup or a restore (whose resource is the backup, with the service as its
// name and the app's place) the one app of that place that has such a service.
type auditLinks struct {
	apps    map[string]bool
	byPlace map[string][]string // "project/environment/service" -> app IDs
}

func newAuditLinks(recs []store.AppRecord) auditLinks {
	l := auditLinks{apps: map[string]bool{}, byPlace: map[string][]string{}}
	for _, r := range recs {
		l.apps[r.App.ID] = true
		for _, sv := range r.App.Services {
			k := r.Place.Project + "/" + r.Place.Environment + "/" + sv.Name
			l.byPlace[k] = append(l.byPlace[k], r.App.ID)
		}
	}
	return l
}

// appOf is the ID of the app an entry is about, "" when it is none or it no longer exists.
func (l auditLinks) appOf(e audit.Event) string {
	switch e.Resource.Kind {
	case audit.KindApp:
		if l.apps[e.Resource.ID] {
			return e.Resource.ID
		}
	case audit.KindBackup:
		ids := l.byPlace[e.Resource.Project+"/"+e.Resource.Environment+"/"+e.Resource.Name]
		if len(ids) == 1 {
			return ids[0]
		}
	}
	return ""
}

// hrefs are the page of the entry's resource and of its record, "" for none.
func (l auditLinks) hrefs(e audit.Event) (resource, record string) {
	id := l.appOf(e)
	if id != "" && e.Resource.Kind == audit.KindApp {
		resource = "/apps/" + url.PathEscape(id)
	}
	if e.Record == nil {
		return resource, ""
	}
	switch e.Record.Kind {
	case audit.RecordDeployment:
		if id != "" {
			record = "/apps/" + url.PathEscape(id) + "/deployments#deployment-" + url.PathEscape(e.Record.ID)
		}
	case audit.RecordBackup, audit.RecordRestore:
		if id != "" {
			record = "/apps/" + url.PathEscape(id) + "/backups"
		}
	case audit.RecordAppAction:
		if id != "" {
			record = "/apps/" + url.PathEscape(id)
		}
	case audit.RecordScheduleRun:
		if e.Resource.Kind == audit.KindSchedule && e.Resource.ID != "" {
			record = "/schedules/" + url.PathEscape(e.Resource.ID) + "/runs/" + url.PathEscape(e.Record.ID)
		}
	case audit.RecordCertificateDrop:
		record = "/settings/certificates" // the drops are listed there
	case audit.RecordSelfBackup, audit.RecordSelfRestore:
		record = "/ops/self-backup" // the runs and the restore are listed there
	}
	return resource, record
}

// auditRowOf is one entry, ready to draw.
func auditRowOf(now time.Time, links auditLinks, e audit.Event) auditRow {
	res, rec := links.hrefs(e)
	row := auditRow{
		ID: e.ID, At: clockSecondsOf(now, e.At), Actor: auditActorWords(e.Actor), Door: string(e.Door), Action: string(e.Action),
		Kind: auditKindWords(e.Resource.Kind), Name: e.Resource.Name, Href: res, Address: e.Address, Fallback: e.Fallback,
	}
	if row.Name == "" {
		row.Name = e.Resource.ID
	}
	if e.Resource.Kind == audit.KindInstallation {
		row.Name = ""
	}
	if e.Resource.Project != "" {
		row.Place = e.Resource.Project
		if e.Resource.Environment != "" {
			row.Place += " › " + e.Resource.Environment
		}
	}
	for _, c := range e.Changes {
		row.Changes = append(row.Changes, auditChangeRow{Path: c.Path, Words: auditChangeWords(c)})
	}
	for _, r := range e.Related {
		row.Related = append(row.Related, string(r.Action)+" "+auditKindWords(r.Kind)+orWordPrefix(r.ID))
	}
	if e.Record != nil {
		row.Record = strings.ReplaceAll(string(e.Record.Kind), "_", " ") + " " + e.Record.ID
		row.RecordHref = rec
	}
	return row
}

func orWordPrefix(id string) string {
	if id == "" {
		return ""
	}
	return " " + id
}

// ---- the handlers ---------------------------------------------------------------------------

// auditPage builds the page for the request's filters. refused, when set, is a refused retention
// post: what it typed and why. It answers 422 when a filter or the retention is refused.
func (s *server) auditPage(r *http.Request, retentionTyped, retentionErr string, exportErrs map[string]string) (auditData, int, error) {
	ctx := r.Context()
	q, vals, errs := auditFilters(r.URL.Query())
	data := auditData{Filters: vals, Errors: errs, ExportErrors: exportErrs, RetentionValue: retentionTyped, RetentionError: retentionErr}
	for _, v := range vals {
		if v != "" {
			data.Filtered = true
		}
	}
	for _, n := range auditFilterNames {
		if n != "from" && n != "to" && vals[n] != "" {
			data.Hidden = append(data.Hidden, auditHidden{n, vals[n]})
		}
	}
	data.Actions = auditOptions(vals["action"], actionNames())
	data.Kinds = auditOptions(vals["kind"], kindNames())
	now := s.now()
	data.ExportFrom = now.UTC().AddDate(0, 0, -7).Format(auditDay)
	data.ExportTo = now.UTC().Format(auditDay)
	if v := r.URL.Query().Get("exportFrom"); v != "" {
		data.ExportFrom = v
	}
	if v := r.URL.Query().Get("exportTo"); v != "" {
		data.ExportTo = v
	}

	status := http.StatusOK
	if len(errs) > 0 || len(exportErrs) > 0 || retentionErr != "" {
		status = http.StatusUnprocessableEntity
	}
	if len(errs) == 0 {
		page, err := s.svc.AuditEvents(ctx, q)
		var fe *app.FieldError
		switch {
		case err == nil:
			recs, err := s.svc.StoredApps(ctx)
			if err != nil {
				return data, 0, err
			}
			links := newAuditLinks(recs)
			for _, e := range page.Events {
				data.Rows = append(data.Rows, auditRowOf(now, links, e))
			}
			if page.Next != 0 {
				v := url.Values{}
				for _, n := range auditFilterNames {
					if vals[n] != "" {
						v.Set(n, vals[n])
					}
				}
				v.Set("before", strconv.FormatInt(page.Next, 10))
				data.Older = "/settings/audit?" + v.Encode()
			}
		case errors.As(err, &fe):
			errs[fe.Field] = auditFieldMessage(fe)
			status = http.StatusUnprocessableEntity
		default:
			return data, 0, err
		}
	}
	st, err := s.svc.AuditSettings(ctx)
	if err != nil {
		return data, 0, err
	}
	data.Retention = st.RetentionDays
	if who, ok := service.PrincipalFrom(ctx); ok {
		data.CanSet = who.Owner()
	}
	if data.RetentionValue == "" {
		data.RetentionValue = strconv.Itoa(st.RetentionDays)
	}
	return data, status, nil
}

func actionNames() []string {
	var out []string
	for _, a := range audit.Actions() {
		out = append(out, string(a))
	}
	return out
}

func kindNames() []string {
	var out []string
	for _, k := range audit.Kinds() {
		out = append(out, string(k))
	}
	slices.Sort(out)
	return out
}

// showAudit answers GET /settings/audit: the filters, a page of entries, the export and the retention.
func (s *server) showAudit(w http.ResponseWriter, r *http.Request, session string) {
	data, status, err := s.auditPage(r, "", "", nil)
	if err != nil {
		s.serverError(w, r, "read the audit log", err)
		return
	}
	if r.URL.Query().Get("saved") == "1" {
		data.Notice = plainS("Saved. Entries older than " + strconv.Itoa(data.Retention) + " days are removed once a day.")
	}
	w.Header().Set("Cache-Control", "no-store")
	s.renderSettings(w, r, entryAudit, settingsData{Audit: data, Status: status})
}

// saveAudit answers POST /settings/audit (owners): the retention in days. A refused value draws the
// page again at 422 with the message at its input; a saved one is a 303 to ?saved=1.
func (s *server) saveAudit(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	typed := strings.TrimSpace(r.PostForm.Get("retentionDays"))
	refuse := func(msg string) {
		data, _, err := s.auditPage(r, typed, msg, nil)
		if err != nil {
			s.serverError(w, r, "read the audit log", err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		s.renderSettings(w, r, entryAudit, settingsData{Audit: data, Status: http.StatusUnprocessableEntity})
	}
	n, err := strconv.Atoi(typed)
	if err != nil {
		refuse("Enter a whole number of days.")
		return
	}
	_, err = s.svc.SetAuditSettings(r.Context(), service.AuditSettings{RetentionDays: n})
	var fe *app.FieldError
	switch {
	case err == nil:
		http.Redirect(w, r, "/settings/audit?saved=1", http.StatusSeeOther)
	case errors.As(err, &fe):
		refuse(auditFieldMessage(fe))
	default:
		s.serverError(w, r, "save the audit retention", err)
	}
}

// exportAudit answers GET /settings/audit/export: the range's entries as CSV, oldest first. The
// export form sends exportFrom and exportTo (UTC days); the filters of the page may come along. A
// missing or refused range draws the page again at 422 with the message at the export form.
func (s *server) exportAudit(w http.ResponseWriter, r *http.Request, session string) {
	v := r.URL.Query()
	// The export form names its range exportFrom and exportTo, so the page's own from and to filters
	// are not mistaken for it; the other filters narrow the export.
	merged := url.Values{}
	for k, vs := range v {
		merged[k] = vs
	}
	merged.Set("from", v.Get("exportFrom"))
	merged.Set("to", v.Get("exportTo"))
	q, _, errs := auditFilters(merged)
	exportErrs := map[string]string{}
	for k, msg := range errs {
		switch k {
		case "from":
			exportErrs["exportFrom"] = msg
		case "to":
			exportErrs["exportTo"] = msg
		}
	}
	if q.From.IsZero() && exportErrs["exportFrom"] == "" {
		exportErrs["exportFrom"] = "Choose the first day."
	}
	if q.To.IsZero() && exportErrs["exportTo"] == "" {
		exportErrs["exportTo"] = "Choose the last day."
	}
	if len(exportErrs) == 0 && q.To.Before(q.From) {
		exportErrs["exportTo"] = "The last day is before the first."
	}
	w.Header().Set("Cache-Control", "no-store")
	if len(exportErrs) > 0 {
		s.exportRefused(w, r, exportErrs)
		return
	}
	q.Limit, q.Before = 0, 0
	const stamp = "20060102"
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="caravana-audit-`+q.From.Format(stamp)+"-"+q.To.Format(stamp)+`.csv"`)
	cw := audit.NewCSVWriter(w)
	err := s.svc.ExportAuditEvents(r.Context(), q, func(e audit.Event) error { return cw.Write(e) })
	if err != nil {
		h.Del("Content-Disposition")
		var fe *app.FieldError
		if errors.As(err, &fe) {
			key := "exportFrom"
			if fe.Field == "to" {
				key = "exportTo"
			}
			s.exportRefused(w, r, map[string]string{key: auditFieldMessage(fe)})
			return
		}
		s.serverError(w, r, "export the audit log", err)
		return
	}
	if err := cw.Close(); err != nil {
		s.log.Error("end the audit export", "error", err)
	}
}

// exportRefused draws the page again at 422 with what the export form got wrong.
func (s *server) exportRefused(w http.ResponseWriter, r *http.Request, errs map[string]string) {
	// The page reads the filters from the query; the range typed stays in the export form.
	data, _, err := s.auditPage(r, "", "", errs)
	if err != nil {
		s.serverError(w, r, "read the audit log", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.renderSettings(w, r, entryAudit, settingsData{Audit: data, Status: http.StatusUnprocessableEntity})
}
