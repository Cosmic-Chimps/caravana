package api

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The audit log (feature 057): listAuditEvents, exportAuditEvents, getAuditSettings and
// putAuditSettings. Owners and admins read and export, owners change the retention; deploy tokens are
// refused by the default deny. Every answer is Cache-Control: no-store. No route edits or deletes an entry.

// auditError makes a refused filter or setting what the contract says: 422 invalid_field naming the
// parameter. Everything else is the caller's to map.
func auditError(err error) error {
	var fe *app.FieldError
	if errors.As(err, &fe) {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
	}
	return err
}

// auditQuery reads the filters of listAuditEvents and exportAuditEvents. A value that doesn't parse is
// a 422 invalid_field naming the parameter; the service refuses the rest.
func auditQuery(r *http.Request) (service.AuditQuery, error) {
	v := r.URL.Query()
	q := service.AuditQuery{
		Actor: v.Get("actor"), Action: v.Get("action"), Kind: v.Get("kind"), Resource: v.Get("resource"),
		Project: v.Get("project"), Environment: v.Get("environment"),
	}
	for _, p := range []struct {
		name string
		into *time.Time
	}{{"from", &q.From}, {"to", &q.To}} {
		s := v.Get(p.name)
		if s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return q, invalidLogField(p.name, "must be a time in RFC 3339, such as 2026-11-25T00:30:00Z")
		}
		*p.into = t.UTC()
	}
	if s := v.Get("before"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 1 {
			return q, invalidLogField("before", "must be an entry ID: a whole number from 1")
		}
		q.Before = n
	}
	if v.Has("limit") {
		n, err := strconv.Atoi(v.Get("limit"))
		if err != nil || n < 1 || n > store.MaxAuditLimit {
			return q, invalidLogField("limit", "must be a whole number from 1 to %d", store.MaxAuditLimit)
		}
		q.Limit = n
	}
	return q, nil
}

// auditEventJSON is schema AuditEvent. changes is always a list; from and to are left out of a change
// of secret material.
type auditEventJSON struct {
	ID        int64           `json:"id"`
	At        time.Time       `json:"at"`
	Actor     audit.Actor     `json:"actor"`
	Door      audit.Door      `json:"door"`
	Address   string          `json:"address,omitempty"`
	Action    audit.Action    `json:"action"`
	Operation string          `json:"operation,omitempty"`
	Resource  audit.Resource  `json:"resource"`
	Changes   []audit.Change  `json:"changes"`
	Record    *audit.Record   `json:"record,omitempty"`
	Related   []audit.Related `json:"related,omitempty"`
	Fallback  bool            `json:"fallback"`
}

func toAuditEventJSON(e audit.Event) auditEventJSON {
	changes := e.Changes
	if changes == nil {
		changes = []audit.Change{}
	}
	return auditEventJSON{ID: e.ID, At: e.At.UTC(), Actor: e.Actor, Door: e.Door, Address: e.Address, Action: e.Action,
		Operation: e.Operation, Resource: e.Resource, Changes: changes, Record: e.Record, Related: e.Related, Fallback: e.Fallback}
}

// auditEventListJSON is schema AuditEventList. next is the before of the next page, left out on the last.
type auditEventListJSON struct {
	Events []auditEventJSON `json:"events"`
	Next   string           `json:"next,omitempty"`
}

// listAuditEvents answers GET /v1/audit with a page of entries, newest first.
func (s *server) listAuditEvents(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	q, err := auditQuery(r)
	if err != nil {
		return err
	}
	page, err := s.svc.AuditEvents(r.Context(), q)
	if err != nil {
		return auditError(err)
	}
	out := auditEventListJSON{Events: make([]auditEventJSON, 0, len(page.Events))}
	for _, e := range page.Events {
		out.Events = append(out.Events, toAuditEventJSON(e))
	}
	if page.Next != 0 {
		out.Next = strconv.FormatInt(page.Next, 10)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// exportAuditEvents answers GET /v1/audit/export with the range as CSV, oldest first. from and to are
// required, before and limit are refused: an export has no page. The headers are set before the first
// row, so a refusal (a bad range, too many entries) takes them back; an error after rows were written
// can only end the file with the error's JSON, since the status is gone.
func (s *server) exportAuditEvents(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	q, err := auditQuery(r)
	if err != nil {
		return err
	}
	if q.Before != 0 || r.URL.Query().Has("limit") {
		return invalidLogField("limit", "an export has no page: leave before and limit out")
	}
	if q.From.IsZero() {
		return invalidLogField("from", "is required")
	}
	if q.To.IsZero() {
		return invalidLogField("to", "is required")
	}
	const stamp = "20060102T150405Z"
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="caravana-audit-`+q.From.UTC().Format(stamp)+"-"+q.To.UTC().Format(stamp)+`.csv"`)
	cw := audit.NewCSVWriter(w)
	err = s.svc.ExportAuditEvents(r.Context(), q, func(e audit.Event) error { return cw.Write(e) })
	if err != nil {
		h.Del("Content-Disposition")
		return auditError(err)
	}
	return cw.Close()
}

// auditSettingsJSON is schema AuditSettings.
type auditSettingsJSON struct {
	RetentionDays int `json:"retentionDays"`
}

// getAuditSettings answers GET /v1/audit/settings.
func (s *server) getAuditSettings(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	st, err := s.svc.AuditSettings(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, auditSettingsJSON{RetentionDays: st.RetentionDays})
	return nil
}

// putAuditSettings answers PUT /v1/audit/settings with the settings as they are now. The retention is
// required, 30 to 3650 days; the change is itself an entry.
func (s *server) putAuditSettings(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	var in struct {
		RetentionDays *int `json:"retentionDays"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.RetentionDays == nil {
		return invalidLogField("retentionDays", "is required")
	}
	st, err := s.svc.SetAuditSettings(r.Context(), service.AuditSettings{RetentionDays: *in.RetentionDays})
	if err != nil {
		return auditError(err)
	}
	writeJSON(w, http.StatusOK, auditSettingsJSON{RetentionDays: st.RetentionDays})
	return nil
}

// auditExempt are the write routes that leave no audit entry, each with the reason (plan D12): the
// agent's routine calls, every 30 seconds, and reads that are sent as POST because they carry a body.
// TestEveryWriteRouteLeavesOneEntry keeps its own copy of this table, so adding one takes two edits.
var auditExempt = map[string]string{
	"POST /v1/node/sync":                 "a server's agent reports and is answered every 30 seconds: routine, not a change",
	"POST /v1/node/certificate":          "a server's agent renews its certificate: routine, not a change",
	"POST /v1/apps/{id}/compose/preview": "shows the app a Compose file becomes and changes nothing",
	"POST /v1/templates/{name}/preview":  "shows the app a template becomes and changes nothing",
	"POST /v1/apps/{id}/promote/preview": "shows what a promotion would do and changes nothing",
}

// auditedReads are the GET routes that open an audit scope like a write: they hand out a file's content,
// which the service notes ("read a file"), and the entry needs the door and the client's address. Their
// scope writes no fallback: a read that noted nothing (a refusal) leaves no entry.
var auditedReads = map[string]bool{
	"GET /v1/apps/{id}/copies/{copyId}/files/download": true,
}

const webhookPath = "/v1/webhooks/{id}"

// audited wraps a write route's handler in an audit scope: the door (the API, MCP by its User-Agent,
// or the webhook), the client's address and the operation. The route's handler and authenticate fill the
// scope; this writes the entry once the status the handler wrote is known.
func (s *server) audited(rt route, next http.HandlerFunc) http.HandlerFunc {
	operation := rt.method + " " + rt.path
	if _, exempt := auditExempt[operation]; exempt {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		door := audit.DoorAPI
		switch {
		case rt.path == webhookPath:
			door = audit.DoorWebhook
		case strings.HasPrefix(r.UserAgent(), "caravana-mcp/"):
			door = audit.DoorMCP
		}
		ctx, sc := s.svc.BeginAudit(r.Context(), door, s.proxies.ClientAddress(r), operation)
		if rt.method == http.MethodGet {
			sc.NoFallback()
		}
		if rt.path == webhookPath {
			sc.SetFallback(fallbackResource(service.Target{AppID: r.PathValue("id")}))
		}
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			// Finish runs even if the handler panics past handle's recover: the entry of a change made
			// before the panic is kept.
			sc.Finish(sw.statusCode(), audit.Resource{})
		}()
		next(sw, r.WithContext(ctx))
	}
}

// fallbackResource says what a request is about from the target its route's access rule read: an app, an
// environment, a project, or nothing narrower, the installation.
func fallbackResource(t service.Target) audit.Resource {
	switch {
	case t.AppID != "":
		if p, err := app.PlaceOf(t.AppID); err == nil {
			return audit.Resource{Kind: audit.KindApp, ID: t.AppID, Name: p.Name, Project: p.Project, Environment: p.Environment}
		}
		return audit.Resource{Kind: audit.KindApp, ID: t.AppID, Name: t.AppID, Project: app.DefaultProject, Environment: app.DefaultEnvironment}
	case t.Project != "" && t.Environment != "":
		return audit.Resource{Kind: audit.KindEnvironment, ID: t.Environment, Name: t.Environment, Project: t.Project, Environment: t.Environment}
	case t.Project != "":
		return audit.Resource{Kind: audit.KindProject, ID: t.Project, Name: t.Project, Project: t.Project}
	}
	return audit.Resource{Kind: audit.KindInstallation}
}

// statusWriter remembers the status a handler wrote. It passes Flush and Hijack through, so a handler
// that streams or upgrades (a write route doesn't today) works as it would without it.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// statusCode is the status written, 200 for a handler that wrote nothing.
func (w *statusWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("the response writer can't be hijacked")
	}
	return h.Hijack()
}

// Unwrap lets http.ResponseController reach the writer underneath.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
