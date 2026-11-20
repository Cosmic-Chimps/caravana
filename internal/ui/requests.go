package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/accesslog"
	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Requests (feature 047): the workspace page /requests reads each HTTP request Traefik served for
// the apps of one project › environment from Traefik's access log, newest first, with a histogram
// of the range stacked by status class. Caravana stores no request: every view is a read
// (service.ReadRequests), and the page draws what the read returned.
//
// It is the Logs page's shape (logs.go, templates/requests.html next to logs.html): the filters are
// all in the address, the chips, bars and rail items are links, the filters are one GET form, and
// Live is a form button, so the page works without script. The live region (#requests-live: the
// histogram and the table) is drawn again from /parts/requests on the requests notice while Live is
// on; the head, the filter row and the rail sit outside it, so a typed path is never swapped away.
// An app's Requests tab (/apps/{id}/requests) is the same page filtered to the app, inside the app's
// shell, with "All requests" at the end of its sentence.
//
// A request is Traefik's text about an app's request and its path may hold what a person typed: it
// goes through html/template only, never into a logger, an error, a notice or a redirect
// (constitution VII, plan D9). The query string and every header never reach the page: Traefik
// drops them before the line exists.

// requestsPageLines is how many requests a view asks for: the service's default.
const requestsPageLines = 200

// ---- The status classes ------------------------------------------------------------------------

// rqChips are the classes the chip row offers, in order. 1xx and "other" (a client that went away)
// are counted in the histogram but have no chip: they are in the view until a chip narrows it.
var rqChips = []string{accesslog.Class2xx, accesslog.Class3xx, accesslog.Class4xx, accesslog.Class5xx}

// classSet says which status classes are shown, indexed as accesslog.Classes. A set with none on is
// no filter: every class is shown.
type classSet [6]bool

func classIndex(c string) int { return slices.Index(accesslog.Classes, c) }

// chipsAll is the set of the four chips: turning on the last chip leaves the filter.
var chipsAll = func() (s classSet) {
	for _, c := range rqChips {
		s[classIndex(c)] = true
	}
	return s
}()

func (s classSet) none() bool { return s == classSet{} }

// has says whether class c is shown.
func (s classSet) has(c string) bool { return s.none() || s[classIndex(c)] }

// list is the classes on, in accesslog.Classes' order.
func (s classSet) list() []string {
	var out []string
	for i, c := range accesslog.Classes {
		if s[i] {
			out = append(out, c)
		}
	}
	return out
}

func (s classSet) param() string { return strings.Join(s.list(), ",") }

// toggled flips one chip. From no filter every chip is on, so the first click turns that one off. A
// set can't be left empty (empty means no filter), so flipping the only class on leaves it as it
// was, and the four chips on again is no filter.
func (s classSet) toggled(c string) classSet {
	out := s
	if s.none() {
		out = chipsAll
	}
	out[classIndex(c)] = !out[classIndex(c)]
	if out.none() {
		return s
	}
	if out == chipsAll {
		return classSet{}
	}
	return out
}

// parseClasses reads a comma list of classes; ok is false for a word that isn't one.
func parseClasses(value string) (set classSet, ok bool) {
	for _, word := range strings.Split(value, ",") {
		i := classIndex(word)
		if i < 0 {
			return classSet{}, false
		}
		set[i] = true
	}
	return set, true
}

// ---- The query ---------------------------------------------------------------------------------

// requestsReq is what the address asks for, checked and with the defaults filled in.
type requestsReq struct {
	Project, Environment string
	App, Service         string // Service is "<app>/<service>", as the API writes it
	Status               classSet
	Method               string // an HTTP method in capitals
	Path                 string // a prefix, starting with /
	MinMs                int    // the least duration, in milliseconds; 0 for any
	Client               string
	Range                string // a logsRange key
	// From and To are a window the address gave (Monitoring's Errors chart links to one); both
	// zero for a preset Range. A page with a window is read, not followed.
	From, To time.Time
	Live     bool
	Before   time.Time // "Show older": the requests that started before it

	// tab is the app whose Requests tab the page is, or "" for /requests. It is never read from the
	// address: the tab's path says it, and its links stay on the tab.
	tab string
}

var methodWord = regexp.MustCompile(`^[A-Z]{1,20}$`)

// requestsReqOf reads the address. bad is "" or a sentence saying what is wrong with it: a value this
// page never writes is refused, not guessed at.
func requestsReqOf(v url.Values) (q requestsReq, bad string) {
	q = requestsReq{
		Project: v.Get("project"), Environment: v.Get("environment"),
		App: v.Get("app"), Service: v.Get("service"),
		Method: strings.ToUpper(v.Get("method")), Path: v.Get("path"), Client: v.Get("client"),
		Live: v.Get("live") != "0",
	}
	if q.Project == "" {
		q.Project = app.DefaultProject
	}
	if q.Environment == "" {
		q.Environment = app.DefaultEnvironment
	}
	var win string
	if q.Range, q.From, q.To, win = windowOf(v); win != "" {
		return q, win
	}
	if q.custom() {
		q.Live = false
	}
	if s := v.Get("status"); s != "" {
		var ok bool
		if q.Status, ok = parseClasses(s); !ok {
			return q, "The status value is a comma list of 1xx, 2xx, 3xx, 4xx, 5xx and other."
		}
	}
	if q.Method != "" && !methodWord.MatchString(q.Method) {
		return q, "The method value is an HTTP method such as GET or POST."
	}
	if q.Path != "" && (!strings.HasPrefix(q.Path, "/") || len(q.Path) > 1024) {
		return q, "The path value is a prefix that starts with / and is at most 1024 bytes."
	}
	if s := v.Get("minDuration"); s != "" {
		ms, err := strconv.Atoi(s)
		if err != nil || ms < 0 {
			return q, "The minDuration value is a whole number of milliseconds."
		}
		q.MinMs = ms
	}
	if len(q.Client) > 64 {
		return q, "The client value is at most 64 bytes."
	}
	if s := v.Get("before"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return q, "The \"before\" value is a time in RFC 3339, such as 2026-10-25T00:30:00Z."
		}
		q.Before = t.UTC()
	}
	return q, ""
}

// inTab fixes the query to an app's Requests tab: the app and its place come from the app, not the
// address.
func (q requestsReq) inTab(rec store.AppRecord) requestsReq {
	q.tab, q.App = rec.App.ID, rec.App.ID
	q.Project, q.Environment = rec.Place.Project, rec.Place.Environment
	return q
}

// path is the page's own path: /requests, or the app's tab.
func (q requestsReq) path() string {
	if q.tab != "" {
		return "/apps/" + url.PathEscape(q.tab) + "/requests"
	}
	return "/requests"
}

// custom says the address gave a window of its own, from and to, not a preset range.
func (q requestsReq) custom() bool { return !q.From.IsZero() }

// rangeOf is the range the page reads: the preset's, or the window's.
func (q requestsReq) rangeOf() logsRange {
	if q.custom() {
		return customRange(q.From, q.To)
	}
	rng, _ := logsRangeOf(q.Range)
	return rng
}

// values is the address's query string: only what differs from the defaults.
func (q requestsReq) values() url.Values {
	v := url.Values{}
	set := func(name, value string) {
		if value != "" {
			v.Set(name, value)
		}
	}
	if q.Project != app.DefaultProject {
		set("project", q.Project)
	}
	if q.Environment != app.DefaultEnvironment {
		set("environment", q.Environment)
	}
	set("app", q.App)
	set("service", q.Service)
	set("status", q.Status.param())
	set("method", q.Method)
	set("path", q.Path)
	if q.MinMs > 0 {
		v.Set("minDuration", strconv.Itoa(q.MinMs))
	}
	set("client", q.Client)
	if q.Range != logsRangeDefault {
		set("range", q.Range)
	}
	if q.custom() {
		set("from", q.From.UTC().Format(time.RFC3339))
		set("to", q.To.UTC().Format(time.RFC3339))
	}
	if !q.Live {
		v.Set("live", "0")
	}
	if !q.Before.IsZero() {
		set("before", q.Before.UTC().Format(time.RFC3339Nano))
	}
	return v
}

// pageValues are the values of the page's own address: the tab's path already says the app and its
// place, so they are left out of it. values() keeps them, for the part, which is the environment's route.
func (q requestsReq) pageValues() url.Values {
	v := q.values()
	if q.tab != "" {
		v.Del("project")
		v.Del("environment")
		v.Del("app")
	}
	return v
}

// href is the page's address for the query, after the changes: "/requests?status=5xx", or the app's
// tab with the same filter.
func (q requestsReq) href(changes ...func(*requestsReq)) string {
	for _, change := range changes {
		change(&q)
	}
	if enc := q.pageValues().Encode(); enc != "" {
		return q.path() + "?" + enc
	}
	return q.path()
}

// hidden is the query's values as hidden fields, for a form that carries the rest of the filter
// along. Names in skip are left out.
func (q requestsReq) hidden(skip ...string) []hiddenField {
	var out []hiddenField
	v := q.pageValues()
	for _, name := range []string{"project", "environment", "app", "service", "status", "method", "path", "minDuration", "client", "range", "from", "to", "live", "before"} {
		if slices.Contains(skip, name) || !v.Has(name) {
			continue
		}
		out = append(out, hiddenField{Name: name, Value: v.Get(name)})
	}
	return out
}

// The changes the page's links make. Each also drops what the change makes stale: a new range or
// filter starts at the newest requests.
func rqFresh(q *requestsReq) { q.Before = time.Time{} }

func rqWithRange(key string) func(*requestsReq) {
	return func(q *requestsReq) {
		rqFresh(q)
		if q.custom() {
			q.Live = true // a window is read, not followed; a preset range is followed again
		}
		q.Range, q.From, q.To = key, time.Time{}, time.Time{}
	}
}

func rqWithStatus(s classSet) func(*requestsReq) {
	return func(q *requestsReq) { rqFresh(q); q.Status = s }
}

func rqWithService(svc string) func(*requestsReq) {
	return func(q *requestsReq) { rqFresh(q); q.Service = svc }
}

func rqWithPlace(project, environment string) func(*requestsReq) {
	return func(q *requestsReq) {
		rqFresh(q)
		q.Project, q.Environment = project, environment
		q.App, q.Service = "", ""
	}
}

func rqPaused(q *requestsReq) { q.Live = false }

// filtered says the query narrows the requests by something but the range (the empty list's words).
func (q requestsReq) filtered() bool {
	return q.App != "" || q.Service != "" || !q.Status.none() || q.Method != "" || q.Path != "" || q.MinMs > 0 || q.Client != ""
}

// filterFields says one of the form's own fields is set (the Clear link's condition).
func (q requestsReq) filterFields() bool {
	return q.Method != "" || q.Path != "" || q.MinMs > 0 || q.Client != ""
}

// query is the read the address asks the service for.
func (q requestsReq) query() service.RequestQuery {
	rq := service.RequestQuery{
		Project: q.Project, Environment: q.Environment, App: q.App, Service: q.Service,
		Statuses: q.Status.list(), Method: q.Method, Path: q.Path,
		MinDuration: time.Duration(q.MinMs) * time.Millisecond, Client: q.Client,
		Before: q.Before, Lines: requestsPageLines,
	}
	if q.custom() {
		rq.From, rq.To = q.From, q.To
	} else {
		rq.Range = q.rangeOf().D
	}
	return rq
}

// ---- What the page draws -----------------------------------------------------------------------

// requestsPage is the template set "requests".
type requestsPage struct {
	pageData
	Requests *requestsView
}

// requestsView is the whole page.
type requestsView struct {
	// Action is where the page's forms go: /requests, or the app's tab.
	Action string
	// AllHref is set on an app's tab: "All requests", the environment's page.
	AllHref string
	// The head: the range bar and Live. Live is a form button: its value is what it flips to. A page
	// with a window of its own has none.
	HeadHidden []hiddenField
	Ranges     []logsBarItem
	Live       bool
	LiveFlip   string
	Custom     bool
	// The filter row: the place bars, the status chips and the form's fields.
	FilterHidden []hiddenField
	Places       logsPlaces
	Chips        []requestChip
	Methods      []rqOption
	Durations    []rqOption
	Path, Client string
	Filtered     bool   // a field of the form is set
	ClearHref    string // clears the form's fields, keeping the rest
	Rail         logsRail
	// Region is the live region. Following says the page draws it again on the requests notice,
	// from RegionHref.
	Region     *requestsRegion
	Following  bool
	RegionHref string
}

// requestChip is one status chip, a link that flips its class.
type requestChip struct {
	Class string
	Href  string
	On    bool
	Count string // 4xx and 5xx, when on
	Title string
}

// rqOption is one option of a select.
type rqOption struct {
	Value, Label string
	On           bool
}

// requestsRegion is the live region: the histogram and the table. /parts/requests draws it alone.
type requestsRegion struct {
	Hist    *rqHistView
	Head    requestsHead
	Rows    []requestRow
	Problem string // why there is nothing to read, when that is so
	Empty   string // the sentence for a read with no requests
	Dropped bool
	Older   string // "Show older"'s address, or ""

	// What the page needs of the read, and not the template.
	status   int
	services []service.RequestService
	classes  [6]int // the range's requests per class, as the histogram counts them
}

// pageStatus is the page's status: Traefik not running is a calm notice with a 200 (the part says
// 503, which htmx doesn't swap, so a refresh that can't read never empties the page).
func (r *requestsRegion) pageStatus() int {
	if r.status == http.StatusServiceUnavailable {
		return http.StatusOK
	}
	return r.status
}

// requestsHead is the line over the rows.
type requestsHead struct {
	Live   bool
	Paused bool
	Count  string // "1,204 requests"; "" when none or none read
	// Cap is how many rows the table holds when it holds the cap, 0 when it doesn't; CapHref is
	// "Show older"'s address beside it.
	Cap     int
	CapHref string
	// Older is the note of a view that shows requests before a time.
	Older *logsOlderNote
}

// requestRow is one request as the table draws it. The row is a link to the Logs around its time.
type requestRow struct {
	Href      string
	ISO       string
	Clock     string // "14:03:41.208"
	TimeTitle string
	Hue       int
	Service   string
	Method    string
	Host      string
	Path      string
	Status    int
	Class     string // "2xx" ... "other": the chip's class
	Duration  string
	Slow      bool
	Bytes     string
	Client    string
	Retries   int
	Title     string // the row's address, for the hover
}

// rqHistView is the histogram panel: an SVG of 30 stacked bars, a table of the same numbers for a
// screen reader, and the label row.
type rqHistView struct {
	Summary string
	Bars    []rqBar
	Table   []rqHistRow
	Start   string // "13:49 UTC"
	End     string // the window's end, when the address gave one; "" means now
	Read    string // "read from 14:21 UTC", when the read didn't reach the start of the range
	Empty   bool
}

// rqBar is one bin: its stacked segments, or a stub, or a hatched one for requests that weren't read.
type rqBar struct {
	X, W   string
	Title  string
	Unread bool
	Empty  bool
	Y, H   string // the stub's and the hatched bar's
	Segs   []rqSeg
}

// rqSeg is one class's part of a stack.
type rqSeg struct{ Class, Y, H string }

// rqHistRow is a bin in the hidden table.
type rqHistRow struct{ From, Requests, C2, C3, C4, C5 string }

// ---- Words -------------------------------------------------------------------------------------

const (
	requestsNoProxy     = "Traefik isn't running, so there are no requests to show."
	requestsNoAccessLog = "Traefik isn't writing its access log yet: rerun the installer, which restarts Traefik once."
	requestsNomadDown   = "Nomad can't be reached right now, so requests can't be read."
	requestsNoMatch     = "No requests match. Try a wider time range or fewer filters."
	requestsNone        = "No requests yet. New requests appear here."
	requestsNoneInRange = "No requests in this range."
	requestsUnreadTip   = "not read: older requests weren't loaded"
)

// ---- The histogram -----------------------------------------------------------------------------

// rqStack is the order a bar stacks its classes in, from the bottom: the quiet ones first, the ones
// that need a look on top.
var rqStack = []struct{ class, css string }{
	{accesslog.Class2xx, "cv-hbar"},
	{accesslog.Class3xx, "cv-hbar-3xx"},
	{accesslog.Class1xx, "cv-hbar-quiet"},
	{accesslog.ClassOther, "cv-hbar-quiet"},
	{accesslog.Class4xx, "cv-hbar-warn"},
	{accesslog.Class5xx, "cv-hbar-err"},
}

func binRequestsTitle(b service.RequestBin) string {
	out := plural(b.Count, "request")
	var parts []string
	for _, c := range []string{accesslog.Class4xx, accesslog.Class5xx} {
		if n := b.ClassCount(c); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, c))
		}
	}
	if len(parts) > 0 {
		out += ", " + strings.Join(parts, ", ")
	}
	return out
}

// rqHistOf builds the panel from the read's 30 bins. A bin the read never reached is hatched, never
// a zero. A stack is as high as the bin's share of the busiest bin; each class takes its share of
// the stack, and 4xx and 5xx never less than 1.5 units, so a few errors in a busy bin stay visible.
func rqHistOf(h service.RequestHistogram, cov service.LogCoverage, rng logsRange) *rqHistView {
	busiest, total, c4, c5 := 0, 0, 0, 0
	for _, b := range h.Bins {
		busiest = max(busiest, b.Count)
		total += b.Count
		c4 += b.ClassCount(accesslog.Class4xx)
		c5 += b.ClassCount(accesslog.Class5xx)
	}
	v := &rqHistView{Empty: total == 0}

	layout := "15:04 UTC"
	if rng.D >= 24*time.Hour {
		layout = "2006-01-02 15:04 UTC"
	}
	binLayout := "15:04"
	if h.BucketSeconds < 60 {
		binLayout = "15:04:05"
	}
	if rng.D >= 24*time.Hour {
		binLayout = "2006-01-02 " + binLayout
	}

	for i, b := range h.Bins {
		bar := rqBar{X: units(float64(i)*histWidth/float64(len(h.Bins)) + 1), W: units(histWidth/float64(len(h.Bins)) - 2)}
		row := rqHistRow{From: b.From.UTC().Format(binLayout), Requests: strconv.Itoa(b.Count),
			C2: strconv.Itoa(b.ClassCount(accesslog.Class2xx)), C3: strconv.Itoa(b.ClassCount(accesslog.Class3xx)),
			C4: strconv.Itoa(b.ClassCount(accesslog.Class4xx)), C5: strconv.Itoa(b.ClassCount(accesslog.Class5xx))}
		switch {
		case !b.Read:
			bar.Unread, bar.Y, bar.H, bar.Title = true, units(histHeight-histBarMax), units(histBarMax), requestsUnreadTip
			row = rqHistRow{From: row.From, Requests: "not read", C2: "not read", C3: "not read", C4: "not read", C5: "not read"}
		case b.Count == 0:
			bar.Empty, bar.Y, bar.H, bar.Title = true, units(histHeight-2), "2", "no requests"
		default:
			bar.Title = binRequestsTitle(b)
			bar.Segs = rqSegments(b, max(2, float64(b.Count)/float64(busiest)*histBarMax))
		}
		v.Bars = append(v.Bars, bar)
		v.Table = append(v.Table, row)
	}

	if total == 0 {
		v.Summary = "No requests " + rng.in()
	} else {
		v.Summary = plural(total, "request") + " " + rng.in()
		if c5 > 0 {
			v.Summary += ", " + strconv.Itoa(c5) + " 5xx"
		}
		if c4 > 0 {
			v.Summary += ", " + strconv.Itoa(c4) + " 4xx"
		}
	}
	v.Start = h.From.UTC().Format(layout)
	if rng.Key == "" {
		v.End = h.To.UTC().Format(layout)
	}
	if !cov.Complete {
		v.Read = "read from " + cov.From.UTC().Format(layout)
		if !cov.Timed {
			v.Read += " · not all read in time"
		}
	}
	return v
}

// rqSegments divides a stack of height total (in the SVG's units) among the bin's classes, bottom to
// top.
func rqSegments(b service.RequestBin, total float64) []rqSeg {
	type part struct {
		css string
		h   float64
	}
	var parts []part
	for _, s := range rqStack {
		n := b.ClassCount(s.class)
		if n == 0 {
			continue
		}
		h := float64(n) / float64(b.Count) * total
		if (s.class == accesslog.Class4xx || s.class == accesslog.Class5xx) && h < 1.5 {
			h = 1.5
		}
		parts = append(parts, part{s.css, h})
	}
	// The minimums may have pushed the stack over its height: the first segment (the largest quiet
	// one) gives it back.
	sum := 0.0
	for _, p := range parts {
		sum += p.h
	}
	if sum > total && len(parts) > 1 {
		parts[0].h = max(0, parts[0].h-(sum-total))
	}
	segs := make([]rqSeg, 0, len(parts))
	top := histHeight
	for _, p := range parts {
		top -= p.h
		segs = append(segs, rqSeg{Class: p.css, Y: units(top), H: units(p.h)})
	}
	return segs
}

// ---- Rows --------------------------------------------------------------------------------------

// cleanText drops control characters from text Traefik wrote, so a path can't drive the page or a
// screen reader with them.
func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

// rqDurationText is how long a request took: "3.6 ms", "412 ms", "1.24 s", "12 s".
func rqDurationText(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1 ms"
	case d < 10*time.Millisecond:
		return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 1, 64) + " ms"
	case d < time.Second:
		return strconv.Itoa(int(d/time.Millisecond)) + " ms"
	case d < 10*time.Second:
		return strconv.FormatFloat(d.Seconds(), 'f', 2, 64) + " s"
	}
	return strconv.Itoa(int(d.Seconds())) + " s"
}

// logsAroundHref is Logs around a request's time for its service: a minute either side, in the same
// place.
func logsAroundHref(project, environment, appID, svc string, at time.Time) string {
	l := logsReq{Project: project, Environment: environment, Service: appID + "/" + svc, Range: logsRangeDefault, Levels: defaultLevels, Live: true,
		From: at.Add(-time.Minute).UTC().Truncate(time.Second), To: at.Add(time.Minute).UTC().Truncate(time.Second).Add(time.Second)}
	return l.href()
}

func (s *server) requestRowOf(q requestsReq, e service.RequestEntry, multi bool) requestRow {
	u := e.Time.UTC()
	host, path := cleanText(e.Host), cleanText(e.Path)
	return requestRow{
		Href: logsAroundHref(q.Project, q.Environment, e.App, e.Service, u),
		ISO:  u.Format(time.RFC3339Nano), Clock: u.Format("15:04:05.000"), TimeTitle: u.Format("2006-01-02 15:04:05.000 UTC"),
		Hue: hueOf(e.App, e.Service), Service: serviceLabel(multi, e.App, e.Service),
		Method: cleanText(e.Method), Host: host, Path: path,
		Status: e.Status, Class: e.Class, Duration: rqDurationText(e.Duration), Slow: e.Duration >= time.Second,
		Bytes: humanBytes(e.Bytes), Client: cleanText(e.Client), Retries: e.Retries,
		Title: host + path,
	}
}

// ---- Building the region -----------------------------------------------------------------------

// requestsRegionOf reads what the query asks for and builds the live region. A read Nomad can't do,
// Traefik not running, a host that doesn't write the log yet and an app that isn't there are a
// Problem of the region, not an error; an error is for what the page can't draw at all (an address
// the service refuses, a store failure).
func (s *server) requestsRegionOf(ctx context.Context, q requestsReq) (*requestsRegion, error) {
	reg := &requestsRegion{status: http.StatusOK}
	read, err := s.svc.ReadRequests(ctx, q.query())
	var pe *service.PlaceError
	var fe *app.FieldError
	switch {
	case err == nil:
	case errors.As(err, &pe), errors.As(err, &fe):
		return nil, err
	case errors.Is(err, service.ErrNoProxy):
		reg.Problem, reg.status = requestsNoProxy, http.StatusServiceUnavailable
	case errors.Is(err, store.ErrNotFound):
		reg.Problem, reg.status = "That app isn't in this environment.", http.StatusNotFound
	case isNomadDown(err):
		s.log.Warn("can't read requests from Nomad", "error", err)
		reg.Problem = requestsNomadDown
	default:
		return nil, err
	}
	if reg.Problem != "" {
		return reg, nil
	}
	reg.services = read.Services
	if !read.AccessLog {
		reg.Problem = requestsNoAccessLog
		return reg, nil
	}

	rng := q.rangeOf()
	hist := read.Histogram()
	for _, b := range hist.Bins {
		for i, c := range accesslog.Classes {
			reg.classes[i] += b.ClassCount(c)
		}
	}
	reg.Hist = rqHistOf(hist, read.Coverage, rng)
	reg.Dropped = read.Dropped

	apps := map[string]bool{}
	for _, e := range read.Requests {
		apps[e.App] = true
	}
	multi := len(apps) > 1
	for _, e := range read.Requests {
		reg.Rows = append(reg.Rows, s.requestRowOf(q, e, multi))
	}

	now := s.now().UTC()
	reg.Head = requestsHead{Live: q.Live, Paused: !q.Live}
	if read.Total > 0 {
		reg.Head.Count = thousands(read.Total) + " requests"
		if read.Total == 1 {
			reg.Head.Count = "1 request"
		}
	}
	if !q.Before.IsZero() {
		reg.Head.Older = &logsOlderNote{Before: clockOf(now, q.Before), Back: q.href(rqFresh, func(q *requestsReq) { q.Live = true })}
	}
	if len(read.Requests) == 0 {
		switch {
		case q.filtered():
			reg.Empty = requestsNoMatch
		case q.custom() || !q.Before.IsZero():
			reg.Empty = requestsNoneInRange
		default:
			reg.Empty = requestsNone
		}
	}

	// "Show older": past the 200 rows, or past what the read reached. A page of older requests is a
	// read of its own, so it is paused.
	capped := read.Total > len(read.Requests) && len(read.Requests) > 0
	before := read.Coverage.From
	if capped {
		before = read.Requests[len(read.Requests)-1].Time
	}
	if (capped || !read.Coverage.Complete) && before.After(read.From) {
		reg.Older = q.href(rqFresh, rqPaused, func(q *requestsReq) { q.Before = before })
		if capped {
			reg.Head.Cap, reg.Head.CapHref = len(read.Requests), reg.Older
		}
	}
	return reg, nil
}

// ---- The page ----------------------------------------------------------------------------------

// showRequestsPage answers GET /requests.
func (s *server) showRequestsPage(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	q, bad := requestsReqOf(r.URL.Query())
	if bad != "" {
		s.message(w, r, http.StatusBadRequest, "Bad request", "That address isn't one caravana made.", bad+" Go back to Requests and use the choices there.")
		return
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		s.serverError(w, r, "list projects", err)
		return
	}
	if !pl.has(q.Project, q.Environment) {
		s.message(w, r, http.StatusNotFound, "Not found", "That project or environment doesn't exist.", "Go back to Requests and choose another.")
		return
	}
	reg, err := s.requestsRegionOf(ctx, q)
	if !s.requestsRead(w, r, err) {
		return
	}
	view, err := s.requestsViewOf(ctx, pl, q, reg)
	if err != nil {
		s.serverError(w, r, "draw the requests", err)
		return
	}
	head, err := s.shellPage(r, "Requests", navRequests, crumb{Label: "Requests"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	if view.Following {
		head.LiveURL = "/live?" + url.Values{"watch": {"requests"}, "project": {q.Project}, "environment": {q.Environment}, "live": {"1"}}.Encode()
	}
	s.render(w, reg.pageStatus(), "requests", requestsPage{pageData: head, Requests: view})
}

// showAppRequests answers GET /apps/{id}/requests: the Requests page filtered to the app, inside the
// app's shell. The address takes the page's filters; the app and its place are the app's whatever it
// says. The stream is the app's own (/live/apps/{id}, which the header needs) with ?watch=requests
// for the requests notice and, while Live is on, live=1: the app's place is followed.
func (s *server) showAppRequests(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	sum, err := s.svc.AppSummary(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, r, "read app", err)
		return
	}
	q, bad := requestsReqOf(r.URL.Query())
	if bad != "" {
		s.message(w, r, http.StatusBadRequest, "Bad request", "That address isn't one caravana made.", bad+" Go back to the Requests tab and use the choices there.")
		return
	}
	q = q.inTab(sum.AppRecord)
	reg, err := s.requestsRegionOf(ctx, q)
	if !s.requestsRead(w, r, err) {
		return
	}
	view, err := s.requestsViewOf(ctx, places{}, q, reg)
	if err != nil {
		s.serverError(w, r, "draw the requests", err)
		return
	}
	head, err := s.shellPage(r, id+" requests", navProjects, s.appCrumbs(ctx, id)...)
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id + "?watch=requests"
	if view.Following {
		head.LiveURL += "&live=1"
	}
	s.render(w, reg.pageStatus(), "app", appPage{pageData: head, appHeader: s.header(ctx, sum, "requests"), RequestsTab: view})
}

// appRequestsWatch is the WatchRequests of an app's stream with ?watch=requests&live=1: it follows the
// app's place, which its ID says (an old ID that isn't a place's is in Default › production).
func (s *server) appRequestsWatch(id string) func() func() {
	watch := s.live.watchRequests
	if watch == nil {
		watch = s.svc.WatchRequests
	}
	return func() func() {
		p, err := app.PlaceOf(id)
		if err != nil {
			p = app.Place{Project: app.DefaultProject, Environment: app.DefaultEnvironment}
		}
		return watch(p.Project, p.Environment)
	}
}

// requestsRead says whether reading the requests worked. A refused address is answered with a 400
// page, a place that went away with a 404 and anything else with a 500, and it returns false.
func (s *server) requestsRead(w http.ResponseWriter, r *http.Request, err error) bool {
	var fe *app.FieldError
	var pe *service.PlaceError
	switch {
	case err == nil:
		return true
	case errors.As(err, &fe):
		s.message(w, r, http.StatusBadRequest, "Bad request", "That address isn't one caravana made.",
			fe.Field+" "+fe.Message+". Go back to Requests and use the choices there.")
	case errors.As(err, &pe):
		s.message(w, r, http.StatusNotFound, "Not found", "That project or environment doesn't exist.", "Go back to Requests and choose another.")
	default:
		s.serverError(w, r, "read requests", err)
	}
	return false
}

// partRequests answers GET /parts/requests: the histogram and the table, drawn again on the requests
// notice. A problem with the read is a sentence inside it; Traefik not running is a 503, which htmx
// doesn't swap, so a refresh that can't read never empties the page.
func (s *server) partRequests(w http.ResponseWriter, r *http.Request, session string) {
	q, bad := requestsReqOf(r.URL.Query())
	if bad != "" {
		http.Error(w, bad, http.StatusBadRequest)
		return
	}
	reg, err := s.requestsRegionOf(r.Context(), q)
	var fe *app.FieldError
	var pe *service.PlaceError
	switch {
	case err == nil:
	case errors.As(err, &fe):
		http.Error(w, fe.Field+" "+fe.Message, http.StatusBadRequest)
		return
	case errors.As(err, &pe):
		http.Error(w, "not found", http.StatusNotFound)
		return
	default:
		s.log.Error("read requests", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if reg.status == http.StatusServiceUnavailable {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	s.renderPart(w, "requests", "requests-region", reg)
}

// liveRequests answers GET /live?watch=requests&project=&environment=&live=1: the stream of a
// Requests page with Live on. It carries the requests notice only (never a request), and while it is
// open the service follows Traefik's log for the place's apps. A page that isn't live opens no such
// stream, and one for a place that doesn't exist follows nothing.
func (s *server) liveRequests(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	project, environment := v.Get("project"), v.Get("environment")
	if project == "" {
		project = app.DefaultProject
	}
	if environment == "" {
		environment = app.DefaultEnvironment
	}
	route := liveRoute{Kinds: []service.ChangeKind{service.ChangeRequests}}
	if v.Get("live") == "1" {
		watch := s.live.watchRequests
		if watch == nil {
			watch = s.svc.WatchRequests
		}
		route.WatchRequests = func() func() {
			if pl, err := s.loadPlaces(r.Context()); err != nil || !pl.has(project, environment) {
				return func() {}
			}
			return watch(project, environment)
		}
	}
	s.stream(w, r, route)
}

// requestsViewOf builds the page around a region: the head, the filter row and the rail.
func (s *server) requestsViewOf(ctx context.Context, pl places, q requestsReq, reg *requestsRegion) (*requestsView, error) {
	v := &requestsView{
		Action: q.path(), Region: reg, Live: q.Live, LiveFlip: "1", Custom: q.custom(),
		Following:    q.Live,
		HeadHidden:   q.hidden("live"),
		FilterHidden: q.hidden("method", "path", "minDuration", "client", "before"),
		Path:         q.Path, Client: q.Client, Filtered: q.filterFields(),
	}
	if q.Live {
		v.LiveFlip = "0"
	}
	if v.Following {
		rq := q
		rq.Live = true
		v.RegionHref = "/parts/requests"
		if enc := rq.values().Encode(); enc != "" {
			v.RegionHref += "?" + enc
		}
	}
	for _, rng := range logsRanges {
		v.Ranges = append(v.Ranges, logsBarItem{Label: rng.Label, On: !q.custom() && rng.Key == q.Range, Href: q.href(rqWithRange(rng.Key))})
	}
	if v.Filtered {
		v.ClearHref = q.href(rqFresh, func(q *requestsReq) { q.Method, q.Path, q.MinMs, q.Client = "", "", 0, "" })
	}

	// The place bars: each only when it has more than one choice, and never on an app's tab, whose
	// place is the app's.
	if q.tab != "" {
		v.AllHref = "/requests?project=" + url.QueryEscape(q.Project) + "&environment=" + url.QueryEscape(q.Environment)
		pl = places{}
	}
	if len(pl.Projects) > 1 {
		for _, pr := range pl.Projects {
			env := q.Environment
			if !pl.has(pr.ID, env) && len(pr.Environments) > 0 {
				env = pr.Environments[0].Name
			}
			v.Places.Projects = append(v.Places.Projects, logsBarItem{Label: pr.DisplayName, On: pr.ID == q.Project, Href: q.href(rqWithPlace(pr.ID, env))})
		}
	}
	for _, pr := range pl.Projects {
		if pr.ID != q.Project || len(pr.Environments) < 2 {
			continue
		}
		for _, e := range pr.Environments {
			v.Places.Environments = append(v.Places.Environments, logsBarItem{Label: e.Name, On: e.Name == q.Environment, Href: q.href(rqWithPlace(pr.ID, e.Name))})
		}
	}

	// The status chips count the range's 4xx and 5xx, as the histogram counts them.
	for _, c := range rqChips {
		chip := requestChip{Class: c, On: q.Status.has(c), Href: q.href(rqWithStatus(q.Status.toggled(c)))}
		chip.Title = "Show " + c + " requests"
		if chip.On {
			chip.Title = "Hide " + c + " requests"
		}
		if chip.On && (c == accesslog.Class4xx || c == accesslog.Class5xx) && reg.Problem == "" {
			chip.Count = strconv.Itoa(reg.classes[classIndex(c)])
		}
		v.Chips = append(v.Chips, chip)
	}

	v.Methods = []rqOption{{Value: "", Label: "any method", On: q.Method == ""}}
	methods := []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	if q.Method != "" && !slices.Contains(methods, q.Method) {
		methods = append(methods, q.Method)
	}
	for _, m := range methods {
		v.Methods = append(v.Methods, rqOption{Value: m, Label: m, On: q.Method == m})
	}
	durations := []int{0, 100, 500, 1000, 5000}
	if !slices.Contains(durations, q.MinMs) {
		durations = append(durations, q.MinMs)
		slices.Sort(durations)
	}
	for _, ms := range durations {
		o := rqOption{Value: strconv.Itoa(ms), Label: "any duration", On: q.MinMs == ms}
		switch {
		case ms == 0:
			o.Value = ""
		case ms%1000 == 0:
			o.Label = strconv.Itoa(ms/1000) + " s or more"
		default:
			o.Label = strconv.Itoa(ms) + " ms or more"
		}
		v.Durations = append(v.Durations, o)
	}

	rail, err := s.requestsRailOf(ctx, q, reg)
	if err != nil {
		return nil, err
	}
	v.Rail = rail
	return v, nil
}

// requestsRailOf builds the services rail: "all services", then each app's services that have a
// domain or had requests in the read, with their hue and request count. The counts are the read's, after the
// filters but the app and service ones.
func (s *server) requestsRailOf(ctx context.Context, q requestsReq, reg *requestsRegion) (logsRail, error) {
	sums, err := s.svc.ListAppsIn(ctx, q.Project, q.Environment)
	if err != nil {
		return logsRail{}, err
	}
	if q.App != "" {
		sums = slices.DeleteFunc(sums, func(a service.AppSummary) bool { return a.App.ID != q.App })
	}
	counts := map[string]int{}
	for _, c := range reg.services {
		counts[c.App+"/"+c.Service] = c.Count
	}
	rail := logsRail{All: logsRailItem{Name: "all services", Href: q.href(rqWithService("")), On: q.Service == ""}}
	var groups []logsRailGroup
	for _, sum := range sums {
		group := logsRailGroup{}
		for _, svc := range sum.App.Services {
			ref := sum.App.ID + "/" + svc.Name
			n, seen := counts[ref]
			if len(svc.Domains) == 0 && !seen {
				continue
			}
			item := logsRailItem{Name: svc.Name, Hue: hueOf(sum.App.ID, svc.Name), On: q.Service == ref, Href: q.href(rqWithService(ref))}
			if reg.Problem == "" {
				item.Count = strconv.Itoa(n)
			}
			group.Items = append(group.Items, item)
		}
		if len(group.Items) > 0 {
			group.Name = logsAppName(sum.App.ID)
			groups = append(groups, group)
		}
	}
	// The app names head the groups only when there is more than one app.
	if len(groups) == 1 {
		groups[0].Name = ""
	}
	rail.Groups = groups
	return rail, nil
}
