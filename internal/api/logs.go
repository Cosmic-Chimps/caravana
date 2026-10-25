package api

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/logline"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The environment's logs (feature 024): listLogs, getLogsAround and
// downloadLogs. Every answer, errors included, is Cache-Control: no-store (a
// line is the app's output and may hold anything), and none of them puts a
// line's text anywhere but in a 200 body: not in an error, not in the log.

// The codes only the logs give.
const (
	codeLogRotated   = "log_rotated"
	codeDownloadBusy = "download_busy"
)

// logsError maps the errors of the three log reads. Field errors are 422, as
// the contract says (getLogs, older, answers them as 400). The copy is checked
// before store.ErrNotFound, which an unknown copy also matches; a place error
// before both, since it too can wrap it. Other errors pass through for
// mapError (Nomad down is 502 nomad_unavailable).
func logsError(err error) error {
	if e := placeError(err); e != nil {
		return e
	}
	var fe *app.FieldError
	switch {
	case errors.As(err, &fe):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
	case errors.Is(err, service.ErrLogCopyNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeNotFound, Field: "copy", Message: err.Error()}
	case errors.Is(err, service.ErrLogRotated):
		return &Error{Status: http.StatusNotFound, Code: codeLogRotated, Message: err.Error()}
	case errors.Is(err, service.ErrDownloadBusy):
		return &Error{Status: http.StatusConflict, Code: codeDownloadBusy, Message: err.Error()}
	case errors.Is(err, store.ErrNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeAppNotFound, Field: "app", Message: err.Error()}
	}
	return err
}

func invalidLogField(field, format string, args ...any) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: field, Message: fmt.Sprintf(format, args...)}
}

// logRangePresets are the values of range.
var logRangePresets = map[string]time.Duration{
	"15m": 15 * time.Minute, "1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour,
}

// envLogQuery reads listLogs' query (and downloadLogs'). Every problem it can
// find is a 422 invalid_field naming the parameter; the rest (a q that is too
// long, from after to, range with from) is the service's Validate, which the
// read itself calls. An empty value counts as absent, except for lines.
func envLogQuery(r *http.Request) (service.EnvLogQuery, error) {
	v := r.URL.Query()
	q := service.EnvLogQuery{
		Project: v.Get("project"), Environment: v.Get("environment"),
		App: v.Get("app"), Service: v.Get("service"), Copy: v.Get("copy"),
		Stream: logline.Stream(v.Get("stream")), Q: v.Get("q"),
	}
	if s := v.Get("levels"); s != "" {
		for _, word := range strings.Split(s, ",") {
			l, err := logline.ParseLevel(word)
			if err != nil {
				return q, invalidLogField("levels", "use a comma list of error, warn, info and debug")
			}
			q.Levels = append(q.Levels, l)
		}
	}
	if s := v.Get("range"); s != "" {
		d, ok := logRangePresets[s]
		if !ok {
			return q, invalidLogField("range", "use 15m, 1h, 24h or 7d")
		}
		q.Range = d
	}
	for _, p := range []struct {
		name string
		into *time.Time
	}{{"from", &q.From}, {"to", &q.To}, {"before", &q.Before}} {
		s := v.Get(p.name)
		if s == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return q, invalidLogField(p.name, "must be a time in RFC 3339, such as 2026-10-25T00:30:00Z")
		}
		*p.into = t.UTC()
	}
	if v.Has("lines") {
		n, err := strconv.Atoi(v.Get("lines"))
		if err != nil || n < 1 || n > 1000 {
			return q, invalidLogField("lines", "must be a whole number from 1 to 1000")
		}
		q.Lines = n
	}
	return q, nil
}

// The JSON of a read (schemas LogEntry, LogDeploy, LogBin, LogHistogram,
// LogCoverage, LogCopy and LogRead).

type logEntryJSON struct {
	ID         string    `json:"id"`
	Time       time.Time `json:"time"`
	TimeSource string    `json:"timeSource"`
	App        string    `json:"app"`
	Service    string    `json:"service"`
	Copy       string    `json:"copy"`
	Stream     string    `json:"stream"`
	Level      string    `json:"level"`
	Text       string    `json:"text"`
	Raw        string    `json:"raw,omitempty"`
	Repeat     int       `json:"repeat"`
}

type logDeployJSON struct {
	DeploymentID string    `json:"deploymentId"`
	App          string    `json:"app"`
	Service      string    `json:"service,omitempty"`
	At           time.Time `json:"at"`
	Kind         string    `json:"kind"`
	JobVersion   int       `json:"jobVersion"`
	RevertedTo   int       `json:"revertedTo,omitzero"`
	Who          string    `json:"who"`
}

type logBinJSON struct {
	From      time.Time `json:"from"`
	Count     int       `json:"count"`
	Errors    int       `json:"errors"`
	Estimated int       `json:"estimated"`
	Read      bool      `json:"read"`
}

type logHistogramJSON struct {
	From          time.Time    `json:"from"`
	To            time.Time    `json:"to"`
	BucketSeconds int          `json:"bucketSeconds"`
	Buckets       []logBinJSON `json:"buckets"`
}

type logCoverageJSON struct {
	From     time.Time `json:"from"`
	Complete bool      `json:"complete"`
}

type logCopyJSON struct {
	ID      string `json:"id"`
	Short   string `json:"short"`
	App     string `json:"app"`
	Service string `json:"service"`
	Gone    bool   `json:"gone"`
	Lines   int    `json:"lines"`
}

type logReadJSON struct {
	Lines     []logEntryJSON    `json:"lines"`
	Deploys   []logDeployJSON   `json:"deploys"`
	Histogram *logHistogramJSON `json:"histogram,omitempty"`
	Coverage  logCoverageJSON   `json:"coverage"`
	Copies    []logCopyJSON     `json:"copies"`
	Dropped   bool              `json:"dropped"`
	Anchor    string            `json:"anchor,omitempty"`
}

func toLogEntryJSON(e service.LogEntry) logEntryJSON {
	return logEntryJSON{
		ID: e.ID.String(), Time: e.Time.UTC(), TimeSource: string(e.TimeSource),
		App: e.App, Service: e.Service, Copy: e.Copy, Stream: string(e.Stream), Level: string(e.Level),
		Text: e.Text, Raw: e.Raw, Repeat: e.Repeat,
	}
}

func toLogReadJSON(read service.LogRead, deploys []service.DeployRow) logReadJSON {
	out := logReadJSON{
		Lines:    make([]logEntryJSON, 0, len(read.Lines)),
		Deploys:  make([]logDeployJSON, 0, len(deploys)),
		Coverage: logCoverageJSON{From: read.Coverage.From.UTC(), Complete: read.Coverage.Complete},
		Copies:   make([]logCopyJSON, 0, len(read.Copies)),
		Dropped:  read.Dropped,
	}
	for _, l := range read.Lines {
		out.Lines = append(out.Lines, toLogEntryJSON(l))
	}
	for _, d := range deploys { // oldest first, as LogDeploys sorts them
		out.Deploys = append(out.Deploys, logDeployJSON{DeploymentID: d.DeploymentID, App: d.App, Service: d.Service,
			At: d.At.UTC(), Kind: string(d.Kind), JobVersion: d.JobVersion, RevertedTo: d.RevertedTo, Who: d.Who})
	}
	for _, c := range read.Copies {
		out.Copies = append(out.Copies, logCopyJSON{ID: c.ID, Short: c.Short, App: c.App, Service: c.Service, Gone: c.Gone, Lines: c.Lines})
	}
	return out
}

func toLogHistogramJSON(h service.LogHistogram) *logHistogramJSON {
	out := &logHistogramJSON{From: h.From.UTC(), To: h.To.UTC(), BucketSeconds: h.BucketSeconds,
		Buckets: make([]logBinJSON, 0, len(h.Bins))}
	for _, b := range h.Bins {
		out.Buckets = append(out.Buckets, logBinJSON{From: b.From.UTC(), Count: b.Count, Errors: b.Errors, Estimated: b.Estimated, Read: b.Read})
	}
	return out
}

// noStore marks the answer, whatever it turns out to be, as not to be kept.
func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

// listLogs answers GET /v1/logs: the output of the environment's copies,
// newest first, with the deploys of the same range, the histogram and what the
// read covered.
func (s *server) listLogs(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	q, err := envLogQuery(r)
	if err != nil {
		return err
	}
	read, err := s.svc.ReadLogs(r.Context(), q)
	if err != nil {
		return logsError(err)
	}
	onlyApp := q.App
	if q.Service != "" {
		onlyApp, _, _ = strings.Cut(q.Service, "/")
	}
	deploys, err := s.svc.LogDeploys(r.Context(), q.Project, q.Environment, read.From, read.To, onlyApp)
	if err != nil {
		return logsError(err)
	}
	out := toLogReadJSON(read, deploys)
	out.Histogram = toLogHistogramJSON(read.Histogram())
	writeJSON(w, http.StatusOK, out)
	return nil
}

// getLogsAround answers GET /v1/logs/around: the lines near one line of one
// copy, whatever their level, with the copy's other stream over the same
// stretch. The line is `at` (a line id) or `around` (a time, with `stream`).
func (s *server) getLogsAround(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	v := r.URL.Query()
	copyRef := v.Get("copy")
	if copyRef == "" {
		return invalidLogField("copy", "is required")
	}
	n := 0
	if v.Has("lines") {
		var convErr error
		if n, convErr = strconv.Atoi(v.Get("lines")); convErr != nil || n < 1 || n > 200 {
			return invalidLogField("lines", "must be a whole number from 1 to 200")
		}
	}
	at, around := v.Get("at"), v.Get("around")
	if (at == "") == (around == "") {
		return invalidLogField("at", "give at (a line id) or around (a time), one of them")
	}

	ctx := r.Context()
	var id logline.LineID
	if at != "" {
		var err error
		if id, err = logline.ParseLineID(at); err != nil {
			return invalidLogField("at", "%s", strings.TrimPrefix(err.Error(), "logline: "))
		}
	} else {
		t, err := time.Parse(time.RFC3339, around)
		if err != nil {
			return invalidLogField("around", "must be a time in RFC 3339, such as 2026-10-25T00:30:00Z")
		}
		stream := logline.Stdout
		if name := v.Get("stream"); name != "" {
			if stream, err = logline.ParseStream(name); err != nil {
				return invalidLogField("stream", "use stdout or stderr")
			}
		}
		if id, err = s.svc.LogsAroundTime(ctx, copyRef, stream, t); err != nil {
			return logsError(err)
		}
	}
	got, err := s.svc.LogsAround(ctx, copyRef, id, n)
	if err != nil {
		return logsError(err)
	}

	// The deploys of the stretch, for the copy's app and place.
	project, environment, appID := "", "", ""
	if len(got.Copies) > 0 {
		appID = got.Copies[0].App
		if p, err := app.PlaceOf(appID); err == nil {
			project, environment = p.Project, p.Environment
		}
	}
	deploys, err := s.svc.LogDeploys(ctx, project, environment, got.From, got.To, appID)
	if err != nil {
		return logsError(err)
	}
	out := toLogReadJSON(got.LogRead, deploys)
	out.Anchor = got.Anchor.String()
	writeJSON(w, http.StatusOK, out)
	return nil
}

// downloadLogs answers GET /v1/logs/download: listLogs' filter (lines and
// before aside) as a text file, oldest first. The service reads everything
// before it writes, so a refusal is still a JSON error with its own status;
// the headers go out with the first byte, and a failure after that is only
// logged, since the status is sent.
func (s *server) downloadLogs(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	q, err := envLogQuery(r)
	if err != nil {
		return err
	}
	// Reading can take a couple of minutes; the server's write deadline
	// shouldn't cut a download that is working.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downloadWriteDeadline))

	dw := &downloadWriter{w: w, name: func() string { return downloadName(q, time.Now()) }}
	err = s.svc.DownloadLogs(r.Context(), q, dw)
	switch {
	case err == nil:
		dw.start() // an empty file still has its headers
		return nil
	case !dw.started:
		return logsError(err)
	default:
		s.log.Warn("log download ended early", "error", err)
		return nil
	}
}

// downloadWriteDeadline is how long a download may take to the last byte.
const downloadWriteDeadline = 5 * time.Minute

// downloadWriter sends the headers with the first write.
type downloadWriter struct {
	w       http.ResponseWriter
	name    func() string
	started bool
}

func (d *downloadWriter) start() {
	if d.started {
		return
	}
	d.started = true
	h := d.w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.name()}))
	d.w.WriteHeader(http.StatusOK)
}

func (d *downloadWriter) Write(p []byte) (int, error) {
	d.start()
	return d.w.Write(p)
}

// downloadName is the file's name from the query's place and the range the
// service resolves from it (the same rules as the read's: to defaults to now,
// from to a range back from it, 15 minutes by default).
func downloadName(q service.EnvLogQuery, now time.Time) string {
	project, environment := q.Project, q.Environment
	if project == "" {
		project = app.DefaultProject
	}
	if environment == "" {
		environment = app.DefaultEnvironment
	}
	to := q.To
	if to.IsZero() {
		to = now
	}
	from := q.From
	if from.IsZero() {
		r := q.Range
		if r == 0 {
			r = 15 * time.Minute
		}
		from = to.Add(-r)
	}
	return service.LogDownloadName(project, environment, from, to)
}
