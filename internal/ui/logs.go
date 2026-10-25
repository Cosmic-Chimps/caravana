package ui

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/logline"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Logs (feature 024): the workspace page /logs reads the output of every copy of
// every service of one project › environment from Nomad's files, newest first,
// with the environment's deploys between the lines and a histogram of the range.
// Caravana stores no line: every view is a read, and the page draws what the read
// returned (service.ReadLogs). The filters are all in the address, so a reload or a
// shared link shows the same; the bars, chips and rail items are links, the search
// is a plain GET form, and Live is a form button, so the page works without script.
//
// The page is drawn in two parts. The live region (#logs-live: the histogram and the
// stream) is drawn again from /parts/logs on the logs notice while Live is on. The
// head, the filter row, the rail and the footer sit outside it, so a typed search and
// a selection are never swapped away (feature 023's rule). A row that is open, and the
// lines around one, are paused pages: the open row's strip is drawn under its row and
// nothing replaces it.
//
// An app's Logs tab (/apps/{id}/logs) is the same page filtered to the app, inside
// the app's shell: the query's tab names the app, its links stay on the tab, its rail
// lists the app's services and its deploy rows are the app's, there are no place
// bars, and its head's sentence ends with "All logs", the environment's page.
//
// A line is the app's output and may hold anything: it goes through html/template
// only, and never into a logger, an error, a notice or a URL (constitution VII). The
// values of the page are contracts/logs-screen.md's.

const (
	// logsPageLines is how many rows (after folding) a view asks for: the service's default.
	logsPageLines = 500
	// logsRangeDefault is the range when the address names none.
	logsRangeDefault = "15m"
)

// logsRange is one preset of the range bar.
type logsRange struct {
	Key   string        // the address's value, and the API's
	Label string        // the bar's item
	Words string        // "15 minutes", for the histogram's summary
	D     time.Duration // how far back
}

var logsRanges = []logsRange{
	{"15m", "15 min", "15 minutes", 15 * time.Minute},
	{"1h", "1 h", "hour", time.Hour},
	{"24h", "24 h", "24 hours", 24 * time.Hour},
	{"7d", "7 d", "7 days", 7 * 24 * time.Hour},
}

func logsRangeOf(key string) (logsRange, bool) {
	for _, r := range logsRanges {
		if r.Key == key {
			return r, true
		}
	}
	return logsRange{}, false
}

// ---- The query --------------------------------------------------------------------------

// levelOrder is the order the level chips and the levels value are written in.
var levelOrder = []logline.Level{logline.Error, logline.Warn, logline.Info, logline.Debug}

// levelSet says which levels are shown, indexed as levelOrder.
type levelSet [4]bool

// defaultLevels is what the page shows until a chip says otherwise: error, warn and info.
var defaultLevels = levelSet{true, true, true, false}

func levelIndex(l logline.Level) int { return slices.Index(levelOrder, l) }

func (s levelSet) has(l logline.Level) bool { return s[levelIndex(l)] }

func (s levelSet) list() []logline.Level {
	var out []logline.Level
	for i, l := range levelOrder {
		if s[i] {
			out = append(out, l)
		}
	}
	return out
}

func (s levelSet) param() string {
	words := make([]string, 0, 4)
	for _, l := range s.list() {
		words = append(words, string(l))
	}
	return strings.Join(words, ",")
}

// toggled flips one level. A set can't be left empty (an empty value means the default
// to the API), so flipping the only level that is on leaves the set as it was.
func (s levelSet) toggled(l logline.Level) levelSet {
	out := s
	out[levelIndex(l)] = !out[levelIndex(l)]
	if len(out.list()) == 0 {
		return s
	}
	return out
}

// parseLevels reads a comma list of levels; ok is false for a word that isn't one.
func parseLevels(value string) (set levelSet, ok bool) {
	for _, word := range strings.Split(value, ",") {
		l, err := logline.ParseLevel(word)
		if err != nil {
			return levelSet{}, false
		}
		set[levelIndex(l)] = true
	}
	return set, true
}

// logsReq is what the address asks for, checked and with the defaults filled in.
type logsReq struct {
	Project, Environment string
	App, Service, Copy   string // Service is "<app>/<service>", as the API writes it
	Q                    string
	Range                string // a logsRange key
	Levels               levelSet
	Live                 bool
	Before               time.Time // "Show older": the lines before it
	Open                 string    // a line's id: the row that is open
	At                   string    // a line's id: the "lines around this" view of Copy

	// tab is the app whose Logs tab the page is, or "" for /logs. It is never read from the
	// address: the tab's path says it, and its links stay on the tab.
	tab string
}

// inTab fixes the query to an app's Logs tab: the app and its place come from the app, not the
// address.
func (q logsReq) inTab(rec store.AppRecord) logsReq {
	q.tab, q.App = rec.App.ID, rec.App.ID
	q.Project, q.Environment = rec.Place.Project, rec.Place.Environment
	return q
}

// path is the page's own path: /logs, or the app's tab.
func (q logsReq) path() string {
	if q.tab != "" {
		return "/apps/" + url.PathEscape(q.tab) + "/logs"
	}
	return "/logs"
}

// pageValues are the values of the page's own address: the tab's path already says the app and
// its place, so they are left out of it. values() keeps them, for the part, the stream's count
// and Download, which are the environment's routes.
func (q logsReq) pageValues() url.Values {
	v := q.values()
	if q.tab != "" {
		v.Del("project")
		v.Del("environment")
		v.Del("app")
	}
	return v
}

// logsReqOf reads the address. bad is "" or a sentence saying what is wrong with it: a
// value this page never writes is refused, not guessed at.
func logsReqOf(v url.Values) (q logsReq, bad string) {
	q = logsReq{
		Project: v.Get("project"), Environment: v.Get("environment"),
		App: v.Get("app"), Service: v.Get("service"), Copy: v.Get("copy"),
		Q: v.Get("q"), Range: v.Get("range"), Levels: defaultLevels,
		Live: v.Get("live") != "0", Open: v.Get("open"), At: v.Get("at"),
	}
	if q.Project == "" {
		q.Project = app.DefaultProject
	}
	if q.Environment == "" {
		q.Environment = app.DefaultEnvironment
	}
	if q.Range == "" {
		q.Range = logsRangeDefault
	}
	if _, ok := logsRangeOf(q.Range); !ok {
		return q, "The range is 15m, 1h, 24h or 7d."
	}
	if s := v.Get("levels"); s != "" {
		var ok bool
		if q.Levels, ok = parseLevels(s); !ok {
			return q, "The levels value is a comma list of error, warn, info and debug."
		}
	}
	if s := v.Get("before"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return q, "The \"before\" value is a time in RFC 3339, such as 2026-10-25T00:30:00Z."
		}
		q.Before = t.UTC()
	}
	for _, id := range []struct{ name, value string }{{"open", q.Open}, {"at", q.At}} {
		if id.value == "" {
			continue
		}
		if _, err := logline.ParseLineID(id.value); err != nil {
			return q, "The \"" + id.name + "\" value is a line's id, written <copy>.<stream>.<file>.<offset>."
		}
	}
	if q.At != "" && q.Copy == "" {
		at, _ := logline.ParseLineID(q.At)
		q.Copy = at.Copy
	}
	return q, ""
}

// values is the address's query string: only what differs from the defaults.
func (q logsReq) values() url.Values {
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
	set("copy", q.Copy)
	set("q", q.Q)
	if q.Range != logsRangeDefault {
		set("range", q.Range)
	}
	if q.Levels != defaultLevels {
		set("levels", q.Levels.param())
	}
	if !q.Live {
		v.Set("live", "0")
	}
	if !q.Before.IsZero() {
		set("before", q.Before.UTC().Format(time.RFC3339Nano))
	}
	set("open", q.Open)
	set("at", q.At)
	return v
}

// href is the page's address for the query, after the changes: "/logs?service=…", or the app's
// tab with the same filter.
func (q logsReq) href(changes ...func(*logsReq)) string {
	for _, change := range changes {
		change(&q)
	}
	if enc := q.pageValues().Encode(); enc != "" {
		return q.path() + "?" + enc
	}
	return q.path()
}

// The changes the page's links make. Each also drops what the change makes stale: a new
// range or filter starts at the newest lines, with no row open.
func fresh(q *logsReq) { q.Before, q.Open, q.At = time.Time{}, "", "" }

func withRange(key string) func(*logsReq) {
	return func(q *logsReq) { fresh(q); q.Range = key }
}

func withLevels(l levelSet) func(*logsReq) {
	return func(q *logsReq) { fresh(q); q.Levels = l }
}

func withService(svc string) func(*logsReq) {
	return func(q *logsReq) { fresh(q); q.Service, q.Copy = svc, "" }
}

func withCopy(svc, short string) func(*logsReq) {
	return func(q *logsReq) { fresh(q); q.Service, q.Copy = svc, short }
}

func withPlace(project, environment string) func(*logsReq) {
	return func(q *logsReq) {
		fresh(q)
		q.Project, q.Environment = project, environment
		q.App, q.Service, q.Copy = "", "", ""
	}
}

// paused stops following: a page that is read, not watched.
func paused(q *logsReq) { q.Live = false }

// envQuery is the read the query asks the service for.
func (q logsReq) envQuery() service.EnvLogQuery {
	rng, _ := logsRangeOf(q.Range)
	return service.EnvLogQuery{
		Project: q.Project, Environment: q.Environment,
		App: q.App, Service: q.Service, Copy: q.Copy,
		Levels: q.Levels.list(), Q: q.Q, Range: rng.D, Before: q.Before, Lines: logsPageLines,
	}
}

// onlyApp is the app whose deploys the stream shows: the one the query names, or none for all.
func (q logsReq) onlyApp() string {
	if q.Service != "" {
		id, _, _ := strings.Cut(q.Service, "/")
		return id
	}
	return q.App
}

// filtered says the query narrows or filters the stream (the empty stream's words).
func (q logsReq) filtered() bool {
	return q.Q != "" || q.App != "" || q.Service != "" || q.Copy != "" || q.Levels != defaultLevels
}

// downloadHref is Download's address: the same filter, with no row, no paging and no Live.
func (q logsReq) downloadHref() string {
	q.Before, q.Open, q.At, q.Live = time.Time{}, "", "", true
	if enc := q.values().Encode(); enc != "" {
		return "/logs/download?" + enc
	}
	return "/logs/download"
}

// hidden is the query's values as hidden fields, for a form that carries the rest of the
// filter along. Names in skip are left out.
func (q logsReq) hidden(skip ...string) []hiddenField {
	var out []hiddenField
	v := q.pageValues()
	for _, name := range []string{"project", "environment", "app", "service", "copy", "q", "range", "levels", "live", "before", "open", "at"} {
		if slices.Contains(skip, name) || !v.Has(name) {
			continue
		}
		out = append(out, hiddenField{Name: name, Value: v.Get(name)})
	}
	return out
}

// hiddenField is one <input type="hidden">.
type hiddenField struct{ Name, Value string }

// ---- What the page draws ----------------------------------------------------------------

// logsPage is the template set "logs".
type logsPage struct {
	pageData
	Logs *logsView
}

// logsView is the whole page.
type logsView struct {
	// Action is where the page's forms go: /logs, or the app's tab.
	Action string
	// AllHref is set on an app's tab: "All logs", the environment's page.
	AllHref string
	// The head: the range bar and Live. Live is a form button: its value is what it flips to.
	HeadHidden []hiddenField
	Ranges     []logsBarItem
	Live       bool
	LiveFlip   string
	// The filter row.
	SearchHidden []hiddenField
	Places       logsPlaces
	Search       logsSearch
	Chips        []logsChip
	// The rail and, in the "lines around this" view, its notice.
	Rail   logsRail
	Around *logsAround
	// Region is the live region. Following says the page draws it again on the logs
	// notice, from RegionHref.
	Region       *logsRegion
	Following    bool
	RegionHref   string
	DownloadHref string
}

// logsBarItem is one item of a segmented bar, a link.
type logsBarItem struct {
	Label string
	Href  string
	On    bool
}

// logsPlaces are the project and environment bars: each is drawn only when it has more
// than one choice.
type logsPlaces struct {
	Projects     []logsBarItem
	Environments []logsBarItem
}

func (p logsPlaces) Any() bool { return len(p.Projects) > 0 || len(p.Environments) > 0 }

// logsSearch is the search field: what was typed, and "12 lines" once one is set.
type logsSearch struct {
	Value string
	N     string
}

// logsChip is one level chip, a link that flips its level.
type logsChip struct {
	Level logline.Level
	Href  string
	On    bool
	Count string // error and warn, when on
	Title string
}

// logsRail is the services rail.
type logsRail struct {
	All    logsRailItem
	Groups []logsRailGroup
}

// logsRailGroup is one app's services. Name is set only when the rail has more than one app.
type logsRailGroup struct {
	Name  string
	Items []logsRailItem
}

// logsRailItem is "all services" or one service, with its hue, copy count and, when it is
// the chosen one, its copies.
type logsRailItem struct {
	Name   string
	Href   string
	Hue    int
	Count  string // the service's copies; "" when Nomad couldn't say
	On     bool
	Copies []logsRailCopy
}

// logsRailCopy is a copy listed under the chosen service.
type logsRailCopy struct {
	Short string
	Href  string
	Gone  bool
	On    bool
}

// logsAround is the notice of the "lines around this" view.
type logsAround struct {
	At      clockTime
	Copy    string
	Service string
	Back    string
}

// logsRegion is the live region: the histogram and the stream. /parts/logs draws it alone.
type logsRegion struct {
	Hist    *histView // nil in the "lines around this" view
	Head    logsStreamHead
	Items   []logsItem
	Problem string // why there is nothing to read, when that is so
	Back    string // the "lines around this" view's way back to the stream, beside the Problem
	Empty   string // the sentence for a stream with no lines
	Dropped bool
	Older   string // "Show older"'s address, or ""

	// What the page needs of the read, and not the template.
	status int
	lines  []service.LogEntry
	copies []service.LogCopy
	hist   service.LogHistogram
	cover  service.LogCoverage
	anchor string // the line the around view is about
}

// logsStreamHead is the line over the rows.
type logsStreamHead struct {
	Live   bool
	Paused bool
	// Feed is the count of new lines a paused page shows when it has script; nil where
	// it makes no sense (the around view, an older page).
	Feed   *logsFeed
	Copies string // "8 copies", while Live
	// Older is the note of a view that shows lines before a time.
	Older *logsOlderNote
}

// logsFeed is the paused head's "· 3 new lines since Show": logs.js listens for the logs notice,
// reads CountHref (the same stream, from the newest) and counts the rows the page doesn't have.
type logsFeed struct {
	CountHref string
	ShowHref  string
}

type logsOlderNote struct {
	Before clockTime
	Back   string
}

// logsItem is one row of the stream: a line or a deploy.
type logsItem struct {
	Line   *logsLine
	Deploy *logsDeploy
	// Strip is the open row's details, drawn under its row (set by the page, never by /parts/logs).
	Strip *logsStrip
}

// logsLine is one line as the stream draws it.
type logsLine struct {
	Anchor    string // "line-<copy>-<stream>-<file>-<offset>", the row's stable id
	Href      string
	ISO       string
	Clock     string // "14:03:41.208", with a leading "~" when estimated
	TimeTitle string
	Estimated bool
	Level     string
	Hue       int
	Service   string
	Copy      string
	Text      string
	Times     int // N of the ×N chip; 0 for a line that wasn't folded
	Open      bool
	Anchored  bool
}

// logsDeploy is a deploy row.
type logsDeploy struct {
	Href     string
	ISO      string
	Clock    string
	Title    string
	Failed   bool
	Sentence sentence
}

// logsStrip is the open row's details.
type logsStrip struct {
	Stream     string
	Service    string
	Copy       string
	AroundHref string
	OnlyHref   string
	OnlyLabel  string
	CloseHref  string
	URL        string
	Raw        string
	Note       *firstSeenNote
}

// firstSeenNote is an error row's "First seen 09:12 UTC today, 3 times since."
type firstSeenNote struct {
	Lead  string
	Clock *clockTime
	Tail  string
}

// ---- Words -------------------------------------------------------------------------------

const (
	logsNomadDown    = "Nomad can't be reached right now, so logs can't be read."
	logsCopyGone     = "That copy isn't one of this environment's copies any more. A newer deploy may have replaced it. Pick another in the list."
	logsAroundGone   = "This copy's lines are gone."
	logsRotated      = "That part of the copy's output has been rotated away."
	logsNoMatch      = "No lines match. Try a wider time range or turn on more levels."
	logsNoOutput     = "No output yet. New lines appear here."
	logsEstimatedTip = "estimated: this line carried no time; taken from the line before it"
	logsUnreadTip    = "not read: older output wasn't loaded"
	logsDownloadBusy = "A download is already running; try again in a moment."
)

// logsAppName is an app as the stream names it: its name, which is its ID in Default › production.
func logsAppName(id string) string {
	if p, err := app.PlaceOf(id); err == nil {
		return p.Name
	}
	return id
}

// hueOf picks a service's hue, one of six, from a stable hash of "<app>/<service>": the
// same service has the same hue on every page, whatever else is listed.
func hueOf(appID, svc string) int {
	h := fnv.New32a()
	h.Write([]byte(appID + "/" + svc))
	return int(h.Sum32() % 6)
}

func lineAnchor(id logline.LineID) string {
	return "line-" + id.Copy + "-" + string(id.Stream) + "-" + strconv.Itoa(id.File) + "-" + strconv.FormatInt(id.Offset, 10)
}

// linesIn is how many lines the rows stand for: a folded row is several.
func linesIn(rows []service.LogEntry) int {
	n := 0
	for _, r := range rows {
		n += 1 + r.Repeat
	}
	return n
}

// ---- Building the region -----------------------------------------------------------------

// logsRegionOf reads what the query asks for and builds the live region. A read Nomad
// can't do, or one for a copy or app that isn't there, is a Problem of the region, not an
// error; an error is for what the page can't draw at all (an address the service refuses, a
// store failure).
func (s *server) logsRegionOf(ctx context.Context, q logsReq) (*logsRegion, error) {
	if q.At != "" {
		return s.logsAroundOf(ctx, q)
	}
	now := s.now().UTC()
	rng, _ := logsRangeOf(q.Range)
	reg := &logsRegion{status: http.StatusOK}
	read, err := s.svc.ReadLogs(ctx, q.envQuery())
	var pe *service.PlaceError
	switch {
	case err == nil:
	case errors.As(err, &pe):
		return nil, err
	case errors.Is(err, service.ErrLogCopyNotFound):
		reg.Problem, reg.status = logsCopyGone, http.StatusNotFound
	case errors.Is(err, store.ErrNotFound):
		reg.Problem, reg.status = "That app isn't in this environment.", http.StatusNotFound
	case isNomadDown(err):
		s.log.Warn("can't read logs from Nomad", "error", err)
		reg.Problem = logsNomadDown
	default:
		return nil, err
	}
	if reg.Problem != "" {
		read = service.LogRead{From: now.Add(-rng.D), To: now, Coverage: service.LogCoverage{From: now.Add(-rng.D), Complete: true, Timed: true}}
	}

	// The rows: newest first, capped; a deploy older than the oldest row shown would sit under
	// lines it came before, so it waits for "Show older".
	capped := len(read.Lines) >= logsPageLines
	floor := read.From
	if capped {
		floor = read.Lines[len(read.Lines)-1].Time
	}
	deploys, err := s.svc.LogDeploys(ctx, q.Project, q.Environment, read.From, read.To, q.onlyApp())
	if err != nil {
		return nil, err
	}
	// The histogram spans the whole range, so every deploy in it is a marker; only
	// the stream's rows take the cut, since a row older than the oldest line shown
	// would sit under lines it came before.
	rows := slices.DeleteFunc(slices.Clone(deploys), func(d service.DeployRow) bool {
		return d.At.Before(floor) || (!q.Before.IsZero() && !d.At.Before(q.Before))
	})

	reg.lines, reg.copies, reg.hist, reg.cover = read.Lines, read.Copies, read.Histogram(), read.Coverage
	reg.Dropped = read.Dropped
	reg.Hist = histOf(reg.hist, read.Coverage, deploys, rng)
	reg.Items = s.logsItems(q, read.Lines, rows, read.Copies)
	reg.Head = streamHead(q, read.Copies, reg.Problem != "")
	if !q.Before.IsZero() {
		reg.Head.Older = &logsOlderNote{Before: clockOf(now, q.Before), Back: q.href(fresh, func(q *logsReq) { q.Live = true })}
	}
	if reg.Problem == "" && len(read.Lines) == 0 {
		reg.Empty = logsNoOutput
		if q.filtered() {
			reg.Empty = logsNoMatch
		}
	}

	// "Show older": past the budget or past the 500 rows. The row's time plus a millisecond, so
	// the lines of the same instant aren't lost between two pages.
	before := read.Coverage.From
	if capped {
		before = floor.Add(time.Millisecond)
	}
	if reg.Problem == "" && (capped || !read.Coverage.Complete) && before.After(read.From) {
		reg.Older = q.href(fresh, paused, func(q *logsReq) { q.Before = before })
	}
	return reg, nil
}

// streamHead builds the line over the rows.
func streamHead(q logsReq, copies []service.LogCopy, problem bool) logsStreamHead {
	h := logsStreamHead{}
	if problem || q.At != "" {
		return h
	}
	if q.Live {
		running := 0
		for _, c := range copies {
			if !c.Gone {
				running++
			}
		}
		h.Live, h.Copies = true, copyCount(running)
		return h
	}
	h.Paused = true
	if q.At == "" && q.Before.IsZero() {
		newest := q
		fresh(&newest)
		h.Feed = &logsFeed{
			CountHref: "/parts/logs?" + newest.values().Encode(),
			ShowHref:  q.href(fresh, func(q *logsReq) { q.Live = true }),
		}
	}
	return h
}

// logsLabels says how rows name their service: the service alone, or "<app>/<service>" when
// the read has copies of more than one app.
func logsLabels(copies []service.LogCopy) (multi bool) {
	seen := map[string]bool{}
	for _, c := range copies {
		seen[c.App] = true
	}
	return len(seen) > 1
}

func serviceLabel(multi bool, appID, svc string) string {
	if multi {
		return logsAppName(appID) + "/" + svc
	}
	return svc
}

// logsItems merges the lines (newest first) and the deploy rows (oldest first) into the
// stream's rows, newest first; a deploy comes above the lines of its own instant.
func (s *server) logsItems(q logsReq, lines []service.LogEntry, deploys []service.DeployRow, copies []service.LogCopy) []logsItem {
	multi := logsLabels(copies)
	items := make([]logsItem, 0, len(lines)+len(deploys))
	d := len(deploys) - 1
	for _, l := range lines {
		for d >= 0 && !deploys[d].At.Before(l.Time) {
			items = append(items, logsItem{Deploy: deployRowOf(deploys[d])})
			d--
		}
		items = append(items, logsItem{Line: s.lineOf(q, l, multi)})
	}
	for ; d >= 0; d-- {
		items = append(items, logsItem{Deploy: deployRowOf(deploys[d])})
	}
	return items
}

// lineOf is a line as the stream draws it. The row is a link that opens it, or, when it is
// the open one, closes it again.
func (s *server) lineOf(q logsReq, l service.LogEntry, multi bool) *logsLine {
	id := l.ID.String()
	anchor := lineAnchor(l.ID)
	u := l.Time.UTC()
	row := &logsLine{
		Anchor: anchor, ISO: u.Format(time.RFC3339Nano), Clock: u.Format("15:04:05.000"),
		TimeTitle: u.Format("2006-01-02 15:04:05.000 UTC"),
		Level:     string(l.Level), Hue: hueOf(l.App, l.Service),
		Service: serviceLabel(multi, l.App, l.Service), Copy: l.Copy, Text: l.Text,
		Open: q.Open == id,
	}
	if l.TimeSource == logline.Estimated {
		row.Estimated, row.Clock, row.TimeTitle = true, "~"+row.Clock, logsEstimatedTip
	}
	if l.Repeat > 0 {
		row.Times = l.Repeat + 1
	}
	if row.Open {
		row.Href = q.href(func(q *logsReq) { q.Open = "" }) + "#" + anchor
	} else {
		row.Href = q.href(paused, func(q *logsReq) { q.Open = id }) + "#" + anchor
	}
	return row
}

// deployRowOf is a deploy row: "<app> v24 started rolling out · pushed to main by ana".
func deployRowOf(d service.DeployRow) *logsDeploy {
	u := d.At.UTC()
	version := ""
	if d.JobVersion > 0 {
		version = " v" + strconv.Itoa(d.JobVersion)
	}
	var words string
	switch d.Kind {
	case service.DeployStarted:
		words = " started rolling out"
		if d.Who != "" {
			words += " · " + d.Who
		}
	case service.DeployLive:
		words = " is live · rollout finished"
	case service.DeployFailed:
		words = " failed its health checks"
	case service.DeployReverted:
		words = " failed its health checks · rolled back"
		if d.RevertedTo > 0 {
			words += " to v" + strconv.Itoa(d.RevertedTo)
		}
	case service.DeploySuperseded:
		words = " was replaced by a newer deploy"
	default:
		words = " " + string(d.Kind)
	}
	return &logsDeploy{
		Href: "/apps/" + url.PathEscape(d.App) + "/deployments#deployment-" + d.DeploymentID,
		ISO:  u.Format(time.RFC3339Nano), Clock: u.Format("15:04:05"), Title: u.Format("2006-01-02 15:04:05 UTC"),
		Failed:   d.Kind == service.DeployFailed || d.Kind == service.DeployReverted,
		Sentence: sentence{mono(logsAppName(d.App)), plain(version + words)},
	}
}

// ---- The histogram -----------------------------------------------------------------------

// histView is the histogram panel: an SVG of 30 bars and the deploy markers, a table of the
// same numbers for a screen reader, and the label row.
type histView struct {
	Summary string
	Bars    []histBar
	Marks   []histMark
	Table   []histRow
	Start   string // "13:49 UTC"
	Read    string // "read from 14:21 UTC", when the read didn't reach the start of the range
	Deploy  string // "┆ blog v24", the newest deploy in the range
	Empty   bool
}

// histBar is one bin: a rect of its count (or a stub, or a hatched one for output that
// wasn't read) and, over its top, a rect of its errors.
type histBar struct {
	Class      string
	X, Y, W, H string
	Title      string
	Err        bool
	ErrY, ErrH string
	ErrTitle   string
}

// histMark is a deploy's vertical line.
type histMark struct {
	X      string
	Title  string
	Failed bool
}

// histRow is a bin in the hidden table.
type histRow struct {
	From   string
	Lines  string
	Errors string
}

// The SVG's box: 300 by 48, in the SVG's own units; a bar is at most 44 high.
const (
	histWidth  = 300.0
	histHeight = 48.0
	histBarMax = 44.0
)

// units writes a length in the SVG's units, with at most one decimal.
func units(v float64) string {
	return strconv.FormatFloat(float64(int(v*10+0.5))/10, 'f', -1, 64)
}

func binTitle(b service.LogBin) string {
	out := plural(b.Count, "line")
	if b.Errors > 0 {
		out += ", " + plural(b.Errors, "error")
	}
	if b.Estimated > 0 {
		out += ", " + strconv.Itoa(b.Estimated) + " at an estimated time"
	}
	return out
}

// histOf builds the panel from the read's 30 bins. A bin the read never reached is hatched,
// never a zero.
func histOf(h service.LogHistogram, cov service.LogCoverage, deploys []service.DeployRow, rng logsRange) *histView {
	busiest := 0
	total, errs := 0, 0
	for _, b := range h.Bins {
		busiest = max(busiest, b.Count)
		total += b.Count
		errs += b.Errors
	}
	v := &histView{Empty: total == 0}

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
		bar := histBar{X: units(float64(i)*histWidth/float64(len(h.Bins)) + 1), W: units(histWidth/float64(len(h.Bins)) - 2)}
		switch {
		case !b.Read:
			bar.Class, bar.Y, bar.H, bar.Title = "cv-hbar-unread", units(histHeight-histBarMax), units(histBarMax), logsUnreadTip
		case b.Count == 0:
			bar.Class, bar.Y, bar.H, bar.Title = "cv-hbar-empty", units(histHeight-2), "2", binTitle(b)
		default:
			height := max(2, float64(b.Count)/float64(busiest)*histBarMax)
			bar.Class, bar.Y, bar.H, bar.Title = "cv-hbar", units(histHeight-height), units(height), binTitle(b)
			if b.Errors > 0 {
				eh := min(height, max(1, float64(b.Errors)/float64(b.Count)*height))
				bar.Err, bar.ErrY, bar.ErrH, bar.ErrTitle = true, units(histHeight-height), units(eh), plural(b.Errors, "error")
			}
		}
		v.Bars = append(v.Bars, bar)
		row := histRow{From: b.From.UTC().Format(binLayout), Lines: strconv.Itoa(b.Count), Errors: strconv.Itoa(b.Errors)}
		if !b.Read {
			row.Lines, row.Errors = "not read", "not read"
		}
		v.Table = append(v.Table, row)
	}

	span := h.To.Sub(h.From)
	multi := false
	for _, d := range deploys {
		if d.App != deploys[0].App {
			multi = true
		}
	}
	for _, d := range deploys {
		if d.At.Before(h.From) || d.At.After(h.To) || span <= 0 {
			continue
		}
		title := ""
		if d.JobVersion > 0 {
			title = "v" + strconv.Itoa(d.JobVersion) + " "
		}
		switch d.Kind {
		case service.DeployStarted:
			title += "rolling out"
		case service.DeployLive:
			title += "live"
		case service.DeployFailed:
			title += "failed"
		case service.DeployReverted:
			title += "failed, rolled back"
		default:
			title += "replaced"
		}
		if multi {
			title = logsAppName(d.App) + " " + title
		}
		v.Marks = append(v.Marks, histMark{
			X:      units(float64(d.At.Sub(h.From)) / float64(span) * histWidth),
			Title:  title,
			Failed: d.Kind == service.DeployFailed || d.Kind == service.DeployReverted,
		})
	}
	if n := len(v.Marks); n > 0 {
		newest := deploys[len(deploys)-1]
		label := "┆ " + logsAppName(newest.App)
		if newest.JobVersion > 0 {
			label += " v" + strconv.Itoa(newest.JobVersion)
		}
		if n > 1 {
			label += " and " + strconv.Itoa(n-1) + " more"
		}
		v.Deploy = label
	}

	if total == 0 {
		v.Summary = "No lines in the last " + rng.Words
	} else {
		v.Summary = plural(total, "line") + " in the last " + rng.Words
		if errs > 0 {
			v.Summary += ", " + plural(errs, "error")
		}
	}
	if n := len(v.Marks); n > 0 {
		v.Summary += ", " + plural(n, "deploy")
	}
	v.Start = h.From.UTC().Format(layout)
	if !cov.Complete {
		v.Read = "read from " + cov.From.UTC().Format(layout)
		if !cov.Timed {
			v.Read += " · not all read in time"
		}
	}
	return v
}

// ---- The "lines around this" view ---------------------------------------------------------

// logsAroundOf reads the lines around the line the query names, in its copy, every level,
// and draws them as a stream with that line marked.
func (s *server) logsAroundOf(ctx context.Context, q logsReq) (*logsRegion, error) {
	at, _ := logline.ParseLineID(q.At)
	reg := &logsRegion{status: http.StatusOK}
	got, err := s.svc.LogsAround(ctx, q.Copy, at, 0)
	var fe *app.FieldError
	switch {
	case err == nil:
	case errors.As(err, &fe):
		return nil, err
	case errors.Is(err, service.ErrLogRotated):
		reg.Problem, reg.status = logsRotated, http.StatusNotFound
	case errors.Is(err, service.ErrLogCopyNotFound):
		reg.Problem, reg.status = logsAroundGone, http.StatusNotFound
	case isNomadDown(err):
		s.log.Warn("can't read logs from Nomad", "error", err)
		reg.Problem = logsNomadDown
	default:
		return nil, err
	}
	if reg.Problem != "" {
		reg.Back = q.streamBack()
		return reg, nil
	}
	reg.lines, reg.copies, reg.anchor = got.Lines, got.Copies, got.Anchor.String()
	multi := logsLabels(got.Copies)
	for _, l := range got.Lines {
		row := s.lineOf(q, l, multi)
		row.Anchored = l.ID.String() == reg.anchor
		reg.Items = append(reg.Items, logsItem{Line: row})
	}
	return reg, nil
}

// ---- The page ----------------------------------------------------------------------------

// showLogsPage answers GET /logs.
func (s *server) showLogsPage(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	q, bad := logsReqOf(r.URL.Query())
	if bad != "" {
		s.message(w, http.StatusBadRequest, "Bad request", "That address isn't one caravana made.", bad+" Go back to Logs and use the choices there.")
		return
	}
	if q.At != "" || q.Open != "" {
		q.Live = false // an open row, or the lines around one, is a page that is read, not followed
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		s.serverError(w, "list projects", err)
		return
	}
	if !pl.has(q.Project, q.Environment) {
		s.message(w, http.StatusNotFound, "Not found", "That project or environment doesn't exist.", "Go back to Logs and choose another.")
		return
	}
	reg, err := s.logsRegionOf(ctx, q)
	if !s.logsRead(w, err) {
		return
	}
	view, err := s.logsViewOf(ctx, r, pl, q, reg)
	if err != nil {
		s.serverError(w, "draw the logs", err)
		return
	}
	head, err := s.shellPage(r, "Logs", navLogs, crumb{Label: "Logs"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	if view.Following || reg.Head.Feed != nil {
		// A paused page with script keeps the stream too, to count the lines that arrive.
		head.LiveURL = "/live?" + url.Values{"watch": {"logs"}, "project": {q.Project}, "environment": {q.Environment}, "live": {"1"}}.Encode()
	}
	s.render(w, reg.status, "logs", logsPage{pageData: head, Logs: view})
}

// isNomadDown says the error is Nomad's: it couldn't be reached, or it refused.
func isNomadDown(err error) bool {
	return errors.Is(err, deploy.ErrNomadUnavailable) || errors.Is(err, deploy.ErrNomadRejected)
}

// showAppLogs answers GET /apps/{id}/logs: the Logs page filtered to the app, inside the app's shell.
// The address takes the page's filters; the app and its place are the app's whatever it says. The
// stream is the app's own (/live/apps/{id}, which the header needs) with ?watch=logs for the logs
// notice and, while Live is on or a paused page counts new lines, live=1: the app's place is followed.
func (s *server) showAppLogs(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	id := r.PathValue("id")
	sum, err := s.svc.AppSummary(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	}
	if err != nil {
		s.serverError(w, "read app", err)
		return
	}
	q, bad := logsReqOf(r.URL.Query())
	if bad != "" {
		s.message(w, http.StatusBadRequest, "Bad request", "That address isn't one caravana made.", bad+" Go back to the Logs tab and use the choices there.")
		return
	}
	q = q.inTab(sum.AppRecord)
	if q.At != "" || q.Open != "" {
		q.Live = false // an open row, or the lines around one, is a page that is read, not followed
	}
	reg, err := s.logsRegionOf(ctx, q)
	if !s.logsRead(w, err) {
		return
	}
	view, err := s.logsViewOf(ctx, r, places{}, q, reg)
	if err != nil {
		s.serverError(w, "draw the logs", err)
		return
	}
	head, err := s.shellPage(r, id+" logs", navProjects, s.appCrumbs(ctx, id)...)
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live/apps/" + id + "?watch=logs"
	if view.Following || reg.Head.Feed != nil {
		head.LiveURL += "&live=1"
	}
	s.render(w, reg.status, "app", appPage{pageData: head, appHeader: s.header(ctx, sum, "logs"), LogsTab: view})
}

// appLogsWatch is the WatchLogs of an app's stream with ?watch=logs&live=1: it follows the app's
// place, which its ID says (an old ID that isn't a place's is in Default › production).
func (s *server) appLogsWatch(id string) func() func() {
	watch := s.live.watchLogs
	if watch == nil {
		watch = s.svc.WatchLogs
	}
	return func() func() {
		p, err := app.PlaceOf(id)
		if err != nil {
			p = app.Place{Project: app.DefaultProject, Environment: app.DefaultEnvironment}
		}
		return watch(p.Project, p.Environment)
	}
}

// logsRead says whether reading the logs worked. A refused address is answered with a 400 page, a
// place that went away with a 404 and anything else with a 500, and it returns false.
func (s *server) logsRead(w http.ResponseWriter, err error) bool {
	var fe *app.FieldError
	var pe *service.PlaceError
	switch {
	case err == nil:
		return true
	case errors.As(err, &fe):
		s.message(w, http.StatusBadRequest, "Bad request", "That address isn't one caravana made.",
			fe.Field+" "+fe.Message+". Go back to Logs and use the choices there.")
	case errors.As(err, &pe):
		s.message(w, http.StatusNotFound, "Not found", "That project or environment doesn't exist.", "Go back to Logs and choose another.")
	default:
		s.serverError(w, "read logs", err)
	}
	return false
}

// partLogs answers GET /parts/logs: the histogram and the stream, drawn again on the logs
// notice. A problem with the read is a sentence inside it, never an error status, so a refresh
// that can't read never empties the page.
func (s *server) partLogs(w http.ResponseWriter, r *http.Request, session string) {
	q, bad := logsReqOf(r.URL.Query())
	if bad != "" {
		http.Error(w, bad, http.StatusBadRequest)
		return
	}
	reg, err := s.logsRegionOf(r.Context(), q)
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
		s.log.Error("read logs", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "logs", "logs-region", reg)
}

// liveLogs answers GET /live?watch=logs&project=&environment=&live=1: the stream of a Logs page
// with Live on. It carries the logs notice only (never a line), and while it is open the service
// follows the place's copies. A page that isn't live opens no such stream, and one for a place
// that doesn't exist follows nothing.
func (s *server) liveLogs(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	project, environment := v.Get("project"), v.Get("environment")
	if project == "" {
		project = app.DefaultProject
	}
	if environment == "" {
		environment = app.DefaultEnvironment
	}
	route := liveRoute{Kinds: []service.ChangeKind{service.ChangeLogs}}
	if v.Get("live") == "1" {
		watch := s.live.watchLogs
		if watch == nil {
			watch = s.svc.WatchLogs
		}
		route.WatchLogs = func() func() {
			if pl, err := s.loadPlaces(r.Context()); err != nil || !pl.has(project, environment) {
				return func() {}
			}
			return watch(project, environment)
		}
	}
	s.stream(w, r, route)
}

// logsViewOf builds the page around a region: the head, the filter row, the rail, the open
// row's strip and the footer.
func (s *server) logsViewOf(ctx context.Context, r *http.Request, pl places, q logsReq, reg *logsRegion) (*logsView, error) {
	now := s.now().UTC()
	v := &logsView{
		Action: q.path(),
		Region: reg, Live: q.Live, LiveFlip: "1", Following: q.Live && q.At == "",
		DownloadHref: q.downloadHref(),
		HeadHidden:   q.hidden("live"),
		SearchHidden: q.hidden("q", "before", "open", "at"),
	}
	if q.Live {
		v.LiveFlip = "0"
	}
	if v.Following {
		rq := q
		rq.Live = true
		v.RegionHref = "/parts/logs?" + rq.values().Encode()
	}
	for _, rng := range logsRanges {
		v.Ranges = append(v.Ranges, logsBarItem{Label: rng.Label, On: rng.Key == q.Range, Href: q.href(withRange(rng.Key))})
	}

	// The place bars: each only when it has more than one choice, and never on an app's tab, whose
	// place is the app's.
	if q.tab != "" {
		v.AllHref = "/logs?project=" + url.QueryEscape(q.Project) + "&environment=" + url.QueryEscape(q.Environment)
		pl = places{}
	}
	if len(pl.Projects) > 1 {
		for _, pr := range pl.Projects {
			env := q.Environment
			if !pl.has(pr.ID, env) && len(pr.Environments) > 0 {
				env = pr.Environments[0].Name
			}
			v.Places.Projects = append(v.Places.Projects, logsBarItem{Label: pr.DisplayName, On: pr.ID == q.Project, Href: q.href(withPlace(pr.ID, env))})
		}
	}
	for _, pr := range pl.Projects {
		if pr.ID != q.Project || len(pr.Environments) < 2 {
			continue
		}
		for _, e := range pr.Environments {
			v.Places.Environments = append(v.Places.Environments, logsBarItem{Label: e.Name, On: e.Name == q.Environment, Href: q.href(withPlace(pr.ID, e.Name))})
		}
	}

	// The search and the chips count the lines the read found.
	v.Search.Value = q.Q
	if q.Q != "" && reg.Problem == "" {
		v.Search.N = plural(linesIn(reg.lines), "line")
	}
	counts := map[logline.Level]int{}
	for _, l := range reg.lines {
		counts[l.Level] += 1 + l.Repeat
	}
	for _, l := range levelOrder {
		chip := logsChip{Level: l, On: q.Levels.has(l), Href: q.href(withLevels(q.Levels.toggled(l)))}
		chip.Title = "Show " + string(l) + " lines"
		if chip.On {
			chip.Title = "Hide " + string(l) + " lines"
		}
		if chip.On && (l == logline.Error || l == logline.Warn) && q.At == "" && reg.Problem == "" {
			chip.Count = strconv.Itoa(counts[l])
		}
		v.Chips = append(v.Chips, chip)
	}

	rail, err := s.logsRailOf(ctx, q, reg)
	if err != nil {
		return nil, err
	}
	v.Rail = rail

	if q.At != "" {
		v.Around = s.aroundNotice(q, reg, now)
	}
	if strip := s.stripOf(r, q, reg, now); strip != nil {
		for i := range reg.Items {
			if l := reg.Items[i].Line; l != nil && l.Open {
				reg.Items[i].Strip = strip
			}
		}
	}
	return v, nil
}

// streamBack is the address the "lines around this" view's way back leads to: the stream with the
// filters it had, and no copy (the view's copy is its own), live again.
func (q logsReq) streamBack() string {
	return q.href(func(q *logsReq) { fresh(q); q.Copy = ""; q.Live = true })
}

// aroundNotice is "Around 14:03:39 UTC in copy 9a4d of blog/web. Every level, this copy only."
// It is nil when the read found nothing to be around: the region's problem says why, with its own
// way back.
func (s *server) aroundNotice(q logsReq, reg *logsRegion, now time.Time) *logsAround {
	if reg.Problem != "" {
		return nil
	}
	n := &logsAround{Back: q.streamBack(), Copy: q.Copy}
	for _, c := range reg.copies {
		n.Copy, n.Service = c.Short, logsAppName(c.App)+"/"+c.Service
	}
	for _, l := range reg.lines {
		if l.ID.String() == reg.anchor {
			n.At = clockSecondsOf(now, l.Time)
		}
	}
	return n
}

// logsRailOf builds the services rail: "all services", then each app's services with their
// hue and copy count. The copy counts come from Nomad's list of copies, not the read, which a
// service filter has narrowed; a Nomad that can't answer leaves the counts out.
func (s *server) logsRailOf(ctx context.Context, q logsReq, reg *logsRegion) (logsRail, error) {
	sums, err := s.svc.ListAppsIn(ctx, q.Project, q.Environment)
	if err != nil {
		return logsRail{}, err
	}
	if q.App != "" {
		sums = slices.DeleteFunc(sums, func(a service.AppSummary) bool { return a.App.ID != q.App })
	}
	rail := logsRail{All: logsRailItem{Name: "all services", Href: q.href(withService("")), On: q.Service == ""}}
	multi := 0
	for _, sum := range sums {
		if len(sum.App.Services) > 0 {
			multi++
		}
	}
	for _, sum := range sums {
		if len(sum.App.Services) == 0 {
			continue
		}
		counts := map[string]int{}
		copies, err := s.svc.Copies(ctx, sum.App.ID)
		known := err == nil
		for _, c := range copies {
			if !c.Terminal {
				counts[c.Service]++
			}
		}
		group := logsRailGroup{}
		if multi > 1 {
			group.Name = logsAppName(sum.App.ID)
		}
		for _, svc := range sum.App.Services {
			ref := sum.App.ID + "/" + svc.Name
			item := logsRailItem{Name: svc.Name, Hue: hueOf(sum.App.ID, svc.Name), On: q.Service == ref, Href: q.href(withService(ref))}
			if known {
				item.Count = strconv.Itoa(counts[svc.Name])
			}
			if item.On {
				item.Copies = railCopies(q, ref, sum.App.ID, svc.Name, reg.copies)
			}
			group.Items = append(group.Items, item)
		}
		rail.Groups = append(rail.Groups, group)
	}
	return rail, nil
}

// railCopies lists the copies of the chosen service that the read found, live ones first; the
// chosen copy links back to the service.
func railCopies(q logsReq, ref, appID, svc string, found []service.LogCopy) []logsRailCopy {
	var out []logsRailCopy
	for _, c := range found {
		if c.App != appID || c.Service != svc {
			continue
		}
		on := q.Copy == c.Short || q.Copy == c.ID
		href := q.href(withCopy(ref, c.Short))
		if on {
			href = q.href(withService(ref))
		}
		out = append(out, logsRailCopy{Short: c.Short, Href: href, Gone: c.Gone, On: on})
	}
	slices.SortStableFunc(out, func(a, b logsRailCopy) int {
		if a.Gone != b.Gone {
			if a.Gone {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Short, b.Short)
	})
	return out
}

// stripOf builds the open row's details, or nil when no row is open or it isn't among the
// lines read.
func (s *server) stripOf(r *http.Request, q logsReq, reg *logsRegion, now time.Time) *logsStrip {
	if q.Open == "" {
		return nil
	}
	var line *service.LogEntry
	for i := range reg.lines {
		if reg.lines[i].ID.String() == q.Open {
			line = &reg.lines[i]
			break
		}
	}
	if line == nil {
		return nil
	}
	multi := logsLabels(reg.copies)
	label := serviceLabel(multi, line.App, line.Service)
	ref := line.App + "/" + line.Service
	id := line.ID.String()
	strip := &logsStrip{
		Stream: string(line.ID.Stream), Service: label, Copy: line.Copy, Raw: line.Raw,
		AroundHref: q.href(paused, func(q *logsReq) { fresh(q); q.Copy, q.At = line.Copy, id }),
		CloseHref:  q.href(func(q *logsReq) { q.Open = "" }),
		URL:        s.origin(r) + q.href(paused) + "#" + lineAnchor(line.ID),
	}
	if q.Service != ref {
		strip.OnlyHref, strip.OnlyLabel = q.href(withService(ref)), label
	}
	if line.Level == logline.Error {
		strip.Note = firstSeenOf(reg.lines, *line, now, reg.cover.Complete)
	}
	return strip
}

// firstSeenOf says when a line's text was first seen in what was read, and how many times: the
// open error row's note. A folded row counts for all the lines it replaced. complete is false
// when the read didn't reach the start of the range.
func firstSeenOf(lines []service.LogEntry, line service.LogEntry, now time.Time, complete bool) *firstSeenNote {
	want := logline.TextWithoutTime(line.Line)
	var first time.Time
	n := 0
	for _, l := range lines { // newest first: the last one met is the oldest
		if logline.TextWithoutTime(l.Line) != want {
			continue
		}
		n += 1 + l.Repeat
		first = l.Time
	}
	scope := "in this range"
	if !complete {
		scope = "in what was read"
	}
	if n <= 1 {
		return &firstSeenNote{Lead: "Seen once " + scope + "."}
	}
	c := clockOf(now, first)
	tail := fmt.Sprintf(", %d times since", n)
	if first.UTC().Format("2006-01-02") == now.UTC().Format("2006-01-02") {
		tail = " today" + tail
	}
	if !complete {
		tail += " (in what was read)"
	}
	return &firstSeenNote{Lead: "First seen ", Clock: &c, Tail: tail + "."}
}

// ---- Download -------------------------------------------------------------------------------

// logsDownloadDeadline is how long a download may take to its last byte: the service reads for up to
// two minutes before it writes, longer than the server's own write timeout allows.
const logsDownloadDeadline = 5 * time.Minute

// downloadLogsFile answers GET /logs/download?<the page's query>: the same filter as a text file, oldest
// first (service.DownloadLogs). The page's paging, open row, around view and Live mean nothing to a file,
// so before, open, at and live are dropped before the query is read. The service reads every stream
// before it writes a byte, so a refusal still gets its own page and status (409 for a download already
// running, 502 for a Nomad that can't be read, 404 for a place, app or copy that isn't there); the
// headers go out with the first byte, and a failure after that is only logged, since the status is
// sent. The file holds the app's output: it is never logged, and the answer is never cached.
func (s *server) downloadLogsFile(w http.ResponseWriter, r *http.Request, session string) {
	v := r.URL.Query()
	for _, name := range []string{"before", "open", "at", "live"} {
		v.Del(name)
	}
	q, bad := logsReqOf(v)
	if bad != "" {
		s.message(w, http.StatusBadRequest, "Bad request", "That address isn't one caravana made.", bad+" Go back to Logs and use the choices there.")
		return
	}
	// The service reads for up to two minutes before the first byte; the server's write timeout
	// shouldn't cut a download that is working.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(logsDownloadDeadline))

	now := s.now().UTC()
	rng, _ := logsRangeOf(q.Range)
	dw := &logsDownloadWriter{w: w, name: service.LogDownloadName(q.Project, q.Environment, now.Add(-rng.D), now)}
	err := s.svc.DownloadLogs(r.Context(), q.envQuery(), dw)
	var fe *app.FieldError
	var pe *service.PlaceError
	switch {
	case err == nil:
		dw.start() // an empty file still has its headers
	case dw.started:
		s.log.Warn("log download ended early", "error", err)
	case errors.Is(err, service.ErrDownloadBusy):
		s.message(w, http.StatusConflict, "Download", logsDownloadBusy, "Only one download of logs runs at a time. Go back to Logs and press Download again.")
	case errors.As(err, &fe):
		s.message(w, http.StatusBadRequest, "Bad request", "That address isn't one caravana made.",
			fe.Field+" "+fe.Message+". Go back to Logs and use the choices there.")
	case errors.As(err, &pe):
		s.message(w, http.StatusNotFound, "Not found", "That project or environment doesn't exist.", "Go back to Logs and choose another.")
	case errors.Is(err, service.ErrLogCopyNotFound):
		s.message(w, http.StatusNotFound, "Not found", "That copy isn't there any more.", logsCopyGone)
	case errors.Is(err, store.ErrNotFound):
		s.message(w, http.StatusNotFound, "Not found", "That app isn't in this environment.", "Go back to Logs and choose another.")
	case isNomadDown(err):
		s.log.Warn("can't download logs from Nomad", "error", err)
		s.message(w, http.StatusBadGateway, "Logs", "Nomad can't be reached.", logsNomadDown)
	default:
		s.serverError(w, "download logs", err)
	}
}

// logsDownloadWriter sends the file's headers with its first write, so that nothing is sent while the
// service may still refuse.
type logsDownloadWriter struct {
	w       http.ResponseWriter
	name    string
	started bool
}

func (d *logsDownloadWriter) start() {
	if d.started {
		return
	}
	d.started = true
	h := d.w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.name}))
	h.Set("Cache-Control", "no-store")
	d.w.WriteHeader(http.StatusOK)
}

func (d *logsDownloadWriter) Write(p []byte) (int, error) {
	d.start()
	return d.w.Write(p)
}
