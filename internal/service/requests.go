package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/accesslog"
	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/logline"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Requests (feature 047): each HTTP request Traefik served for the apps of one
// project › environment, read from Traefik's stdout, which Nomad keeps in
// alloc/logs/traefik.stdout.<n>. ReadRequests walks those files backwards with
// the same walk the Logs page uses (walkLogStream) and never stores anything.
//
// A request line is Traefik's text about an app's request and may hold a path
// a person typed. Nothing here writes one to the server log, an error, a
// notice or the store (plan D9, constitution VII). Errors carry Nomad's own
// words and allocation IDs, never a line.

// The read's limits.
const (
	// requestStreamBudget is the bytes read from Traefik's stdout for a page:
	// about 20,000 requests at ~400 bytes a line.
	requestStreamBudget = 8 << 20
	// requestDefaultLines is how many requests a page answers by default.
	requestDefaultLines = 200
	// requestMaxPath, requestMaxClient and requestMaxMethod bound the filters.
	requestMaxPath   = 1024
	requestMaxClient = 64
	requestMaxMethod = 20
)

// ErrNoProxy is Traefik not running (the API's 503 proxy_unavailable).
var ErrNoProxy = deploy.ErrNoProxy

// RequestQuery is what a page read of an environment's requests is asked.
type RequestQuery struct {
	// Project and Environment name the place; empty means Default and
	// production.
	Project, Environment string
	// App is an app ID of the place and Service "<app>/<service>"; each
	// narrows the requests listed (not the rail's counts).
	App, Service string
	// Statuses are the classes shown (accesslog.Classes); nil means all.
	Statuses []string
	// Method is an HTTP method name (upper-cased by Validate's caller or
	// here); "" means any.
	Method string
	// Path is a prefix the request's path starts with; "" means any.
	Path string
	// MinDuration keeps the requests that took at least this long; 0 means any.
	MinDuration time.Duration
	// Client is an address, matched exactly; "" means any.
	Client string
	// From and To bound the requests by their start time. Or Range, one of
	// 15m, 1h, 24h and 7d, ending at To; not both. To defaults to now.
	From, To time.Time
	Range    time.Duration
	// Before is "Show older": only requests that started before it.
	Before time.Time
	// Lines is how many requests to answer, 1 to 1000; 0 means 200.
	Lines int
}

// logQuery is the part of the query the logs' own rules check and resolve: the
// place, the app and service, the range and the lines.
func (q RequestQuery) logQuery() EnvLogQuery {
	return EnvLogQuery{Project: q.Project, Environment: q.Environment, App: q.App, Service: q.Service,
		From: q.From, To: q.To, Range: q.Range, Before: q.Before, Lines: q.Lines}
}

// Validate checks the query's own fields, naming the field at fault as a
// *app.FieldError. Whether the place, app and service exist is checked by
// ReadRequests.
func (q RequestQuery) Validate() error {
	if err := q.logQuery().Validate(); err != nil {
		return err
	}
	for _, c := range q.Statuses {
		if !accesslog.ValidClass(c) {
			return app.NewFieldError("status", "use a comma list of 1xx, 2xx, 3xx, 4xx, 5xx and other")
		}
	}
	if q.Method != "" && !validMethod(q.Method) {
		return app.NewFieldError("method", "use an HTTP method such as GET or POST")
	}
	if q.Path != "" {
		if !strings.HasPrefix(q.Path, "/") {
			return app.NewFieldError("path", "is a path prefix and starts with /")
		}
		if len(q.Path) > requestMaxPath {
			return app.NewFieldError("path", "is longer than %d bytes", requestMaxPath)
		}
	}
	if q.MinDuration < 0 {
		return app.NewFieldError("minDuration", "must be a number of milliseconds, 0 or more")
	}
	if len(q.Client) > requestMaxClient {
		return app.NewFieldError("client", "is longer than %d bytes", requestMaxClient)
	}
	return nil
}

// validMethod says whether m is an HTTP method token written in capitals (the
// caller upper-cases what a person typed).
func validMethod(m string) bool {
	if len(m) == 0 || len(m) > requestMaxMethod {
		return false
	}
	for _, r := range m {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// RequestEntry is one request of the answer.
type RequestEntry struct {
	Time     time.Time // when Traefik received it
	App      string    // the app ID
	Service  string    // the service's name in the app
	Router   string
	Method   string
	Host     string
	Path     string // the path alone: Traefik drops the query string before the line exists
	Status   int
	Class    string // accesslog.Class(Status)
	Duration time.Duration
	Bytes    int64
	Client   string
	TLS      string
	Retries  int
}

// RequestService is a service of the rail: how many requests it had in the read.
type RequestService struct {
	App, Service string
	Count        int
}

// RequestRead is one view's answer.
type RequestRead struct {
	// AccessLog is false when Traefik's job doesn't write its access log yet:
	// nothing was read, and the page says to run the installer again.
	AccessLog bool
	Requests  []RequestEntry // newest first, at most Lines
	// Total is how many requests the read found that passed every filter, before
	// Lines cut the list.
	Total    int
	Coverage LogCoverage
	// Dropped is true when the walk read back to the oldest file Nomad keeps and
	// older files were already purged.
	Dropped bool
	// From and To are the range the read resolved.
	From, To time.Time
	// Services counts the requests of each service of the place that passed the
	// filters but the app and service ones, so a rail can offer the others.
	Services []RequestService

	stamps []requestStamp // every request that passed every filter: what the histogram counts
}

// requestStamp is what the histogram needs of a request.
type requestStamp struct {
	Time  time.Time
	Class string
}

// ReadRequests reads the requests of an environment, newest first.
//
//   - The place must exist (*PlaceError, with the field project or
//     environment). App must be one of its apps (store.ErrNotFound) and Service
//     one of that app's services (a *app.FieldError on service).
//   - Traefik's job must exist and have a running allocation (ErrNoProxy). A
//     job that doesn't write the access log is not an error: the answer has
//     AccessLog false and nothing else.
//   - At most two page reads run at once per Caravana (the logs' own limit,
//     shared with them), and once it has its turn the read has logReadDeadline.
//   - The file is in completion order, and the read sorts by the time each
//     request started. A request that began before the range and ended inside
//     it may be missed by the walk's stop.
//
// Nomad can't be read: the error matches deploy.ErrNomadUnavailable.
func (s *Service) ReadRequests(ctx context.Context, q RequestQuery) (RequestRead, error) {
	q.Method = strings.ToUpper(q.Method)
	if err := q.Validate(); err != nil {
		return RequestRead{}, err
	}
	win, err := q.logQuery().window(s.clock.Now().UTC())
	if err != nil {
		return RequestRead{}, err
	}
	narrow, owners, err := s.requestOwners(ctx, q)
	if err != nil {
		return RequestRead{}, err
	}

	logged, err := s.deployer.ProxyAccessLog(ctx)
	if err != nil {
		return RequestRead{}, fmt.Errorf("read requests: %w", err)
	}
	read := RequestRead{AccessLog: logged, From: win.from, To: win.to,
		Coverage: LogCoverage{From: win.from, Complete: true, Timed: true}}
	if !logged {
		return read, nil
	}
	proxy, err := s.deployer.ProxyCopy(ctx)
	if err != nil {
		return RequestRead{}, fmt.Errorf("read requests: %w", err)
	}

	select {
	case s.logSem <- struct{}{}:
		defer func() { <-s.logSem }()
	case <-ctx.Done():
		return RequestRead{}, ctx.Err()
	}
	readCtx, cancel := context.WithTimeout(ctx, s.logDeadline)
	defer cancel()

	files, err := s.deployer.LogFiles(readCtx, proxy.ID, deploy.ProxyTask)
	switch {
	case err != nil && ctx.Err() != nil:
		return RequestRead{}, ctx.Err()
	case errors.Is(err, deploy.ErrCopyNotFound):
		return RequestRead{}, fmt.Errorf("read requests: %w", ErrNoProxy)
	case err != nil && readCtx.Err() == nil:
		return RequestRead{}, fmt.Errorf("read requests: %w", err)
	}
	var stdout []deploy.LogFile
	for _, f := range files {
		if f.Stream == logline.Stdout {
			stdout = append(stdout, f)
		}
	}

	res := streamRead{reason: stopDeadline}
	if err == nil {
		res, err = s.walkLogStream(readCtx, proxy, stdout, walkLimits{budget: requestStreamBudget, from: win.from, upper: win.upper()})
		if err != nil {
			return RequestRead{}, fmt.Errorf("read requests: %w", err)
		}
		if res.reason == stopDeadline && ctx.Err() != nil {
			return RequestRead{}, ctx.Err()
		}
	}
	return finishRequestRead(read, q, win, proxy, res, narrow, owners), nil
}

// requestOwners checks the query's place, app and service and returns the
// owners of the place's services (every app's, whatever q.App says: the rail
// counts them all) and the filter for the list (q.App and q.Service).
func (s *Service) requestOwners(ctx context.Context, q RequestQuery) (narrow requestNarrow, owners accesslog.Owners, err error) {
	narrowed, err := s.logApps(ctx, q.logQuery())
	if err != nil {
		return requestNarrow{}, nil, err
	}
	all := narrowed
	if q.App != "" || q.Service != "" {
		if all, err = s.logApps(ctx, EnvLogQuery{Project: q.Project, Environment: q.Environment}); err != nil {
			return requestNarrow{}, nil, err
		}
	}
	narrow.app = q.App
	if q.Service != "" {
		narrow.app, narrow.service, _ = strings.Cut(q.Service, "/")
	}
	return narrow, ownersOfRecords(all), nil
}

// ownersOfRecords is accesslog.OwnersOf the stored apps.
func ownersOfRecords(recs []store.AppRecord) accesslog.Owners {
	apps := make([]accesslog.OwnerApp, 0, len(recs))
	for _, r := range recs {
		a := accesslog.OwnerApp{ID: r.App.ID, Project: r.Place.Project, Environment: r.Place.Environment}
		for _, sv := range r.App.Services {
			a.Services = append(a.Services, sv.Name)
		}
		apps = append(apps, a)
	}
	return accesslog.OwnersOf(apps)
}

// requestNarrow is the app and service a list is cut to; zero values cut nothing.
type requestNarrow struct{ app, service string }

func (n requestNarrow) has(o accesslog.Owner) bool {
	return (n.app == "" || o.App == n.app) && (n.service == "" || o.Service == n.service)
}

// requestFilter is the status, method, path, duration and client filters.
type requestFilter struct {
	classes     map[string]bool
	method      string
	path        string
	minDuration time.Duration
	client      string
}

func (q RequestQuery) filter() requestFilter {
	f := requestFilter{method: q.Method, path: q.Path, minDuration: q.MinDuration, client: q.Client}
	if len(q.Statuses) > 0 {
		f.classes = map[string]bool{}
		for _, c := range q.Statuses {
			f.classes[c] = true
		}
	}
	return f
}

func (f requestFilter) keeps(r accesslog.Request) bool {
	switch {
	case f.classes != nil && !f.classes[accesslog.Class(r.Status)]:
		return false
	case f.method != "" && r.Method != f.method:
		return false
	case f.path != "" && !strings.HasPrefix(r.Path, f.path):
		return false
	case r.Duration < f.minDuration:
		return false
	case f.client != "" && r.Client != f.client:
		return false
	}
	return true
}

// finishRequestRead parses what the walk read, keeps what is in the window and
// passes the filters, sorts it newest first and fills the answer.
func finishRequestRead(read RequestRead, q RequestQuery, win logWindow, proxy deploy.Copy, res streamRead,
	narrow requestNarrow, owners accesslog.Owners) RequestRead {
	filter := q.filter()
	lines := q.Lines
	if lines == 0 {
		lines = requestDefaultLines
	}

	type railKey struct{ app, service string }
	rail := map[railKey]int{}
	var kept []RequestEntry // in file order, oldest first
	for _, raw := range res.raws {
		r, ok := accesslog.Parse(raw.text)
		if !ok || !win.has(r.Time) {
			continue
		}
		own, ok := owners.Of(r.Service)
		if !ok || !filter.keeps(r) {
			continue
		}
		rail[railKey{own.App, own.Service}]++
		if !narrow.has(own) {
			continue
		}
		kept = append(kept, RequestEntry{
			Time: r.Time, App: own.App, Service: own.Service, Router: r.Router, Method: r.Method, Host: r.Host,
			Path: r.Path, Status: r.Status, Class: accesslog.Class(r.Status), Duration: r.Duration, Bytes: r.Bytes,
			Client: r.Client, TLS: r.TLS, Retries: r.Retries,
		})
	}

	// The file is in completion order: a slow request lands after faster ones
	// that started later. Newest first by start; among equal starts the one
	// written later comes first.
	slices.Reverse(kept)
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Time.After(kept[j].Time) })

	read.Total = len(kept)
	read.stamps = make([]requestStamp, len(kept))
	for i, e := range kept {
		read.stamps[i] = requestStamp{Time: e.Time, Class: e.Class}
	}
	read.Requests = kept[:min(lines, len(kept))]
	read.Dropped = res.dropped
	for k, n := range rail {
		read.Services = append(read.Services, RequestService{App: k.app, Service: k.service, Count: n})
	}
	sort.Slice(read.Services, func(i, j int) bool {
		a, b := read.Services[i], read.Services[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.App != b.App {
			return a.App < b.App
		}
		return a.Service < b.Service
	})

	// How far back the read reached.
	switch res.reason {
	case stopBudget, stopDeadline:
		read.Coverage.Complete = false
		if res.reason == stopDeadline {
			read.Coverage.Timed = false
		}
		edge := win.upper() // nothing was read: nothing is known
		if len(res.raws) > 0 {
			edge = oldestRequestTime(res.raws, win.upper())
		}
		if edge.After(win.from) {
			read.Coverage.From = edge
		}
	case stopEnd:
		// The oldest file of this allocation was read to its start. Requests
		// before the allocation began are in an older allocation's files (a
		// Traefik restart), which this read doesn't open: say so.
		if proxy.CreatedAt != nil && proxy.CreatedAt.After(win.from) && !res.dropped {
			read.Coverage.Complete = false
			read.Coverage.From = proxy.CreatedAt.UTC()
			if read.Coverage.From.After(win.upper()) {
				read.Coverage.From = win.upper()
			}
		}
	}
	return read
}

// oldestRequestTime is the oldest start among the first requests the walk read,
// or fallback when none of its lines is one. The file is in completion order, so
// the oldest line isn't always the oldest start: a few lines are compared.
func oldestRequestTime(raws []rawLine, fallback time.Time) time.Time {
	const compared = 64
	oldest, seen := time.Time{}, 0
	for _, raw := range raws {
		r, ok := accesslog.Parse(raw.text)
		if !ok {
			continue
		}
		if oldest.IsZero() || r.Time.Before(oldest) {
			oldest = r.Time
		}
		if seen++; seen == compared {
			break
		}
	}
	if oldest.IsZero() {
		return fallback
	}
	return oldest
}
