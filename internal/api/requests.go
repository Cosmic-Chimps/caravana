package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/accesslog"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The environment's requests (feature 047): listRequests. Like the logs', every
// answer, errors included, is Cache-Control: no-store, and nothing here puts a
// request's path anywhere but in a 200 body: not in an error, not in the log.

// codeProxyUnavailable is Traefik not running.
const codeProxyUnavailable = "proxy_unavailable"

// requestsError maps the errors of the requests read. Traefik not running is
// 503 with a sentence that holds nothing of the request; every other error is
// the logs' (a field error is 422, a missing place or app 404, Nomad down 502).
func requestsError(err error) error {
	if errors.Is(err, service.ErrNoProxy) {
		return &Error{Status: http.StatusServiceUnavailable, Code: codeProxyUnavailable, Message: "Traefik isn't running"}
	}
	return logsError(err)
}

// requestQuery reads listRequests' query. Every problem it can find is a 422
// invalid_field naming the parameter; the rest (a path without its slash, a
// client over 64 bytes, a bad range) is the service's Validate, which the read
// itself calls. An empty value counts as absent, except for lines.
func requestQuery(r *http.Request) (service.RequestQuery, error) {
	v := r.URL.Query()
	q := service.RequestQuery{
		Project: v.Get("project"), Environment: v.Get("environment"),
		App: v.Get("app"), Service: v.Get("service"),
		Method: strings.ToUpper(v.Get("method")), Path: v.Get("path"), Client: v.Get("client"),
	}
	if s := v.Get("status"); s != "" {
		for _, word := range strings.Split(s, ",") {
			if !accesslog.ValidClass(word) {
				return q, invalidLogField("status", "use a comma list of 1xx, 2xx, 3xx, 4xx, 5xx and other")
			}
			q.Statuses = append(q.Statuses, word)
		}
	}
	if s := v.Get("minDuration"); s != "" {
		ms, err := strconv.Atoi(s)
		if err != nil || ms < 0 {
			return q, invalidLogField("minDuration", "must be a whole number of milliseconds, 0 or more")
		}
		q.MinDuration = time.Duration(ms) * time.Millisecond
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

// The JSON of a read (schemas RequestEntry, RequestBin, RequestHistogram,
// RequestService and RequestRead).

type requestEntryJSON struct {
	Time        time.Time `json:"time"`
	App         string    `json:"app"`
	Service     string    `json:"service"`
	Router      string    `json:"router"`
	Method      string    `json:"method"`
	Host        string    `json:"host"`
	Path        string    `json:"path"`
	Status      int       `json:"status"`
	StatusClass string    `json:"statusClass"`
	DurationMs  float64   `json:"durationMs"`
	Bytes       int64     `json:"bytes"`
	Client      string    `json:"client"`
	TLS         string    `json:"tls"`
	Retries     int       `json:"retries"`
}

type requestBinJSON struct {
	From    time.Time      `json:"from"`
	Count   int            `json:"count"`
	Classes map[string]int `json:"classes"`
	Read    bool           `json:"read"`
}

type requestHistogramJSON struct {
	From          time.Time        `json:"from"`
	To            time.Time        `json:"to"`
	BucketSeconds int              `json:"bucketSeconds"`
	Buckets       []requestBinJSON `json:"buckets"`
}

type requestServiceJSON struct {
	App     string `json:"app"`
	Service string `json:"service"`
	Count   int    `json:"count"`
}

type requestReadJSON struct {
	AccessLog bool                 `json:"accessLog"`
	Requests  []requestEntryJSON   `json:"requests"`
	Total     int                  `json:"total"`
	Histogram requestHistogramJSON `json:"histogram"`
	Coverage  logCoverageJSON      `json:"coverage"`
	Dropped   bool                 `json:"dropped"`
	Services  []requestServiceJSON `json:"services"`
}

func toRequestReadJSON(read service.RequestRead) requestReadJSON {
	out := requestReadJSON{
		AccessLog: read.AccessLog,
		Requests:  make([]requestEntryJSON, 0, len(read.Requests)),
		Total:     read.Total,
		Coverage:  logCoverageJSON{From: read.Coverage.From.UTC(), Complete: read.Coverage.Complete},
		Dropped:   read.Dropped,
		Services:  make([]requestServiceJSON, 0, len(read.Services)),
	}
	for _, e := range read.Requests {
		out.Requests = append(out.Requests, requestEntryJSON{
			Time: e.Time.UTC(), App: e.App, Service: e.Service, Router: e.Router, Method: e.Method, Host: e.Host, Path: e.Path,
			Status: e.Status, StatusClass: e.Class, DurationMs: float64(e.Duration) / float64(time.Millisecond),
			Bytes: e.Bytes, Client: e.Client, TLS: e.TLS, Retries: e.Retries,
		})
	}
	for _, s := range read.Services {
		out.Services = append(out.Services, requestServiceJSON{App: s.App, Service: s.Service, Count: s.Count})
	}
	h := read.Histogram()
	out.Histogram = requestHistogramJSON{From: h.From.UTC(), To: h.To.UTC(), BucketSeconds: h.BucketSeconds,
		Buckets: make([]requestBinJSON, 0, len(h.Bins))}
	for _, b := range h.Bins {
		classes := make(map[string]int, len(accesslog.Classes))
		for _, c := range accesslog.Classes {
			classes[c] = b.ClassCount(c)
		}
		out.Histogram.Buckets = append(out.Histogram.Buckets, requestBinJSON{From: b.From.UTC(), Count: b.Count, Classes: classes, Read: b.Read})
	}
	return out
}

// listRequests answers GET /v1/requests: the requests Traefik served for the
// environment's apps, newest first, with the histogram of the range and what
// the read covered. A host whose Traefik doesn't write the access log yet
// answers accessLog false and nothing else.
func (s *server) listRequests(w http.ResponseWriter, r *http.Request) error {
	noStore(w)
	q, err := requestQuery(r)
	if err != nil {
		return err
	}
	read, err := s.svc.ReadRequests(r.Context(), q)
	if err != nil {
		return requestsError(err)
	}
	writeJSON(w, http.StatusOK, toRequestReadJSON(read))
	return nil
}
