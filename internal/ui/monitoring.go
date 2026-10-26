package ui

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Monitoring (feature 025, page 8): the workspace page /monitoring draws what the sampler stored,
// compared with the same hour of the earlier days: the unusual services as sentences, four charts
// of the place's totals with the band, and a table of its services. It calls only service.Metrics
// and service.Host; it never reads Nomad or Traefik itself.
//
// The filters are all in the address (place, range, sort), so a reload or a shared link shows the
// same; the bars are links and the page is a GET, so it works without script. The live region
// (#mon-live: sources, issues, charts and the table) is drawn again from /parts/monitoring on the
// metrics notice, which comes once a minute; the head and the filter row sit outside it. The
// charts are inline SVG: every shape is an element with attributes and every colour a class, never
// a style attribute (the page's policy forbids one). monitoring.js only adds the hover readout.
// The values of the page are contracts/monitoring-screen.md's.

// ---- the query ------------------------------------------------------------------------------

// monSorts are the sort bar's items, in order.
var monSorts = []struct {
	Sort  service.MetricsSort
	Label string
}{
	{service.SortUnusual, "unusual first"},
	{service.SortTraffic, "most traffic"},
	{service.SortMemory, "most memory"},
}

// monReq is what the address asks for, checked and with the defaults filled in.
type monReq struct {
	Project, Environment string
	Range                metrics.Range
	Sort                 service.MetricsSort
}

// monReqOf reads the address. bad is "" or a sentence saying what is wrong with it: a value this
// page never writes is refused, not guessed at.
func monReqOf(v url.Values) (q monReq, bad string) {
	q = monReq{Project: v.Get("project"), Environment: v.Get("environment"), Range: metrics.Range6h, Sort: service.SortUnusual}
	if q.Project == "" {
		q.Project = app.DefaultProject
	}
	if q.Environment == "" {
		q.Environment = app.DefaultEnvironment
	}
	if name := v.Get("range"); name != "" {
		rng, ok := metrics.ParseRange(name)
		if !ok {
			return q, "The range is 1h, 6h, 24h or 7d."
		}
		q.Range = rng
	}
	if name := v.Get("sort"); name != "" {
		sort, ok := service.ParseMetricsSort(name)
		if !ok {
			return q, "The sort is unusual, traffic or memory."
		}
		q.Sort = sort
	}
	return q, ""
}

// values is the address's query string: only what differs from the defaults.
func (q monReq) values() url.Values {
	v := url.Values{}
	if q.Project != app.DefaultProject {
		v.Set("project", q.Project)
	}
	if q.Environment != app.DefaultEnvironment {
		v.Set("environment", q.Environment)
	}
	if q.Range.Name != metrics.Range6h.Name {
		v.Set("range", q.Range.Name)
	}
	if q.Sort != service.SortUnusual {
		v.Set("sort", string(q.Sort))
	}
	return v
}

// href is the page's address for the query, after the changes.
func (q monReq) href(changes ...func(*monReq)) string {
	for _, change := range changes {
		change(&q)
	}
	if enc := q.values().Encode(); enc != "" {
		return "/monitoring?" + enc
	}
	return "/monitoring"
}

func monWithRange(rng metrics.Range) func(*monReq) { return func(q *monReq) { q.Range = rng } }
func monWithSort(s service.MetricsSort) func(*monReq) {
	return func(q *monReq) { q.Sort = s }
}
func monWithPlace(project, environment string) func(*monReq) {
	return func(q *monReq) { q.Project, q.Environment = project, environment }
}

// ---- sentences with numbers -----------------------------------------------------------------

// monPart is a piece of a Monitoring sentence: plain text, a name in mono, a number in weight 500
// (cv-mnstrong), or a clock time (UTC first, the local time beside it from localtime.js).
type monPart struct {
	Text   string
	Mono   bool
	Strong bool
	Clock  *clockTime
}

// monSentence is prose with its names and numbers marked; the "mon-sentence" template draws it.
type monSentence []monPart

func mtext(s string) monPart   { return monPart{Text: s} }
func mname(s string) monPart   { return monPart{Text: s, Mono: true} }
func mstrong(s string) monPart { return monPart{Text: s, Strong: true} }
func mclock(c clockTime) monPart {
	return monPart{Clock: &c}
}

func msentence(parts ...monPart) monSentence { return parts }

// String is the sentence as plain text, for tests.
func (s monSentence) String() string {
	var b strings.Builder
	for _, p := range s {
		if p.Clock != nil {
			b.WriteString(p.Clock.Text)
		} else {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// ---- number words ---------------------------------------------------------------------------

// trim1 writes v with at most one decimal and no trailing ".0".
func trim1(v float64) string {
	return strconv.FormatFloat(float64(int(v*10+0.5))/10, 'f', -1, 64)
}

// rateText is a rate of requests per second: no decimals from 10 up, else one ("0.4", "4", "96").
func rateText(v float64) string {
	if v >= 10 {
		return strconv.Itoa(int(v + 0.5))
	}
	return trim1(v)
}

// percentText is a share with one decimal: "2.4", "0.0".
func percentText(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }

// msText splits a latency in milliseconds into its number and unit: "820" "ms", "1.2" "s".
func msText(v float64) (num, unit string) {
	if v >= 1000 {
		return trim1(v / 1000), "s"
	}
	return strconv.Itoa(int(v + 0.5)), "ms"
}

// msWords is a latency with its unit: "820 ms", "1.2 s".
func msWords(v float64) string {
	n, u := msText(v)
	return n + " " + u
}

// sizeText is an amount of memory in megabytes as the page says it: "256 MB", "1 GB", "3.6 GB".
func sizeText(mb int64) string {
	if mb >= 1024 {
		return trim1(float64(mb)/1024) + " GB"
	}
	return strconv.FormatInt(mb, 10) + " MB"
}

// gbText is a number of megabytes in gigabytes with one decimal: "0.7", "3.5".
func gbText(mb float64) string { return strconv.FormatFloat(mb/1024, 'f', 1, 64) }

// ---- what the page draws --------------------------------------------------------------------

// monPage is the template set "monitoring".
type monPage struct {
	pageData
	Mon *monView
}

// monView is the whole page.
type monView struct {
	// The filter row: the place bars (each only with more than one choice) and the range bar.
	Projects, Environments []logsBarItem
	Ranges                 []logsBarItem
	// Region is the live region, drawn again from RegionHref on the metrics notice.
	Region     *monRegion
	RegionHref string
}

// monNotice is a line about a source the sampler couldn't read.
type monNotice struct {
	Mark string
	Text monSentence
}

// monIssue is one row of the issues panel.
type monIssue struct {
	Mark       string // warning or failed
	Text       monSentence
	Detail     monSentence
	Action     monSentence
	ActionHref string
}

// monCalm is the one line shown instead of the panel.
type monCalm struct {
	Mark string
	Text monSentence
}

// monRegion is what #mon-live draws.
type monRegion struct {
	Notices []monNotice
	Issues  []monIssue
	Calm    *monCalm
	Charts  []monChart
	Table   monTable
}

// monTable is the apps table.
type monTable struct {
	Meta  monSentence
	Sorts []logsBarItem
	Rows  []monRow
	Empty string
}

// monNum is one number cell: "—" (None) when there is none, in warn hue when Warn, with
// the quieter "usually 180" after it when Usual is set.
type monNum struct {
	Text  string
	None  bool
	Warn  bool
	Usual string
	Title string
}

// monRow is one service of the apps table.
type monRow struct {
	Anchor   string
	Mark     string
	MarkWord string
	Name     string
	Href     string
	Copies   string
	Requests monNum
	Errors   monNum
	P95      monNum
	Spark    *monSpark
	Mem      monMem
	Restarts monNum
	LogsHref string
	LogsName string
}

// monSpark is the CPU sparkline of a row: line segments in a 120 by 24 box.
type monSpark struct {
	Segments []string
	Title    string
}

// monMem is the table's memory cell: a bar and its readout.
type monMem struct {
	Known bool
	Fill  string // the bar's width in the SVG's 0-100 units
	Read  string
	Warn  bool
	Title string
}

// ---- handlers -------------------------------------------------------------------------------

func (s *server) showMonitoring(w http.ResponseWriter, r *http.Request, session string) {
	ctx := r.Context()
	q, bad := monReqOf(r.URL.Query())
	if bad != "" {
		s.message(w, http.StatusBadRequest, "Bad request", "That address isn't one caravana made.", bad+" Go back to Monitoring and use the choices there.")
		return
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		s.serverError(w, "list projects", err)
		return
	}
	if !pl.has(q.Project, q.Environment) {
		s.message(w, http.StatusNotFound, "Not found", "That project or environment doesn't exist.", "Go back to Monitoring and choose another.")
		return
	}
	view, err := s.monViewOf(ctx, q, pl)
	if err != nil {
		s.serverError(w, "draw monitoring", err)
		return
	}
	head, err := s.shellPage(r, "Monitoring", navMonitoring, crumb{Label: "Monitoring"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live?watch=monitoring"
	s.render(w, http.StatusOK, "monitoring", monPage{pageData: head, Mon: view})
}

// partMonitoring answers GET /parts/monitoring: the live region alone.
func (s *server) partMonitoring(w http.ResponseWriter, r *http.Request, session string) {
	q, bad := monReqOf(r.URL.Query())
	if bad != "" {
		http.Error(w, bad, http.StatusBadRequest)
		return
	}
	pl, err := s.loadPlaces(r.Context())
	if err != nil {
		s.log.Error("read projects", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !pl.has(q.Project, q.Environment) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	host, err := s.svc.Host(r.Context())
	if err != nil {
		s.log.Error("read the host", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	reg, err := s.monRegionOf(r.Context(), q, pl, host)
	if err != nil {
		s.partFailed(w, "read metrics", err)
		return
	}
	s.renderPart(w, "monitoring", "mon-region", reg)
}

// partFailed answers a part that couldn't be read: a place that vanished is 404, the rest 500
// (htmx swaps neither in, so the old content stays).
func (s *server) partFailed(w http.ResponseWriter, what string, err error) {
	var pe *service.PlaceError
	if errors.As(err, &pe) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.log.Error(what, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// monViewOf builds the page around its regions: the filter row, the live region and the host.
func (s *server) monViewOf(ctx context.Context, q monReq, pl places) (*monView, error) {
	host, err := s.svc.Host(ctx)
	if err != nil {
		return nil, err
	}
	reg, err := s.monRegionOf(ctx, q, pl, host)
	if err != nil {
		return nil, err
	}
	v := &monView{Region: reg, RegionHref: "/parts/monitoring"}
	if enc := q.values().Encode(); enc != "" {
		v.RegionHref += "?" + enc
	}
	for _, rng := range metrics.Ranges() {
		v.Ranges = append(v.Ranges, logsBarItem{Label: monRangeLabel(rng), On: rng.Name == q.Range.Name, Href: q.href(monWithRange(rng))})
	}
	if len(pl.Projects) > 1 {
		for _, pr := range pl.Projects {
			env := q.Environment
			if !pl.has(pr.ID, env) && len(pr.Environments) > 0 {
				env = pr.Environments[0].Name
			}
			v.Projects = append(v.Projects, logsBarItem{Label: pr.DisplayName, On: pr.ID == q.Project, Href: q.href(monWithPlace(pr.ID, env))})
		}
	}
	for _, pr := range pl.Projects {
		if pr.ID != q.Project || len(pr.Environments) < 2 {
			continue
		}
		for _, e := range pr.Environments {
			v.Environments = append(v.Environments, logsBarItem{Label: e.Name, On: e.Name == q.Environment, Href: q.href(monWithPlace(pr.ID, e.Name))})
		}
	}
	return v, nil
}

// monRangeLabel is a range bar's item: "1 h", "6 h", "24 h", "7 d".
func monRangeLabel(r metrics.Range) string {
	return strings.TrimRight(r.Name, "hd") + " " + r.Name[len(r.Name)-1:]
}

// monRegionOf reads the metrics of the place and builds the live region.
func (s *server) monRegionOf(ctx context.Context, q monReq, pl places, host service.HostView) (*monRegion, error) {
	view, err := s.svc.Metrics(ctx, service.MetricsQuery{Project: q.Project, Environment: q.Environment, Range: q.Range, Sort: q.Sort})
	if err != nil {
		return nil, err
	}
	b := monBuilder{
		now: view.At, q: q, pl: pl, view: view,
		multi: map[string]bool{}, kinds: map[serviceRef]map[metrics.Kind]bool{},
	}
	if host.CPU.Total != nil && host.CPU.Cores != nil && *host.CPU.Cores > 0 {
		b.coreMHz = float64(*host.CPU.Total) / float64(*host.CPU.Cores)
	}
	perApp := map[string]int{}
	for _, sv := range view.Services {
		perApp[sv.AppID]++
	}
	for id, n := range perApp {
		b.multi[id] = n > 1
	}
	return b.region(), nil
}

// serviceRef names a service of an app.
type serviceRef struct{ app, service string }

// monBuilder turns a MetricsView into the page's words and shapes.
type monBuilder struct {
	now     time.Time
	q       monReq
	pl      places
	view    service.MetricsView
	coreMHz float64                              // MHz of one core of the host, 0 when unknown
	multi   map[string]bool                      // apps that have several services, named "app / service"
	kinds   map[serviceRef]map[metrics.Kind]bool // the rules each service breaks
}

// where is "Shop / production" as two names, or empty with one place.
func (b *monBuilder) where() monSentence {
	if b.pl.single() {
		return nil
	}
	return msentence(mname(b.pl.display(b.q.Project)), mtext(" / "), mname(b.q.Environment))
}

// serviceName is what a sentence calls a service: the app, or "app/service" when the app has several.
func (b *monBuilder) serviceName(appName, appID, svc string) string {
	if b.multi[appID] {
		return appName + "/" + svc
	}
	return appName
}

func (b *monBuilder) region() *monRegion {
	reg := &monRegion{}
	reg.Notices = b.notices()
	for _, is := range b.view.Issues {
		ref := serviceRef{is.AppID, is.Service}
		if b.kinds[ref] == nil {
			b.kinds[ref] = map[metrics.Kind]bool{}
		}
		b.kinds[ref][is.Kind] = true
		reg.Issues = append(reg.Issues, b.issue(is))
	}
	if len(reg.Issues) == 0 {
		reg.Calm = b.calm()
	}
	reg.Charts = b.charts()
	reg.Table = b.table()
	return reg
}

// notices are the Nomad problems of the last tick: a line above the issues. Traefik's are said in
// the charts they affect.
func (b *monBuilder) notices() []monNotice {
	var out []monNotice
	switch b.view.Sources.Nomad {
	case service.SamplerDenied:
		out = append(out, monNotice{Mark: "warning", Text: nomadDeniedSentence()})
	case service.SamplerUnreachable:
		out = append(out, monNotice{Mark: "warning", Text: msentence(mtext("Can't read Nomad right now, so memory and CPU are the last numbers sampled."))})
	}
	return out
}

// nomadDeniedSentence says what to do when Caravana's Nomad token can't read the node.
func nomadDeniedSentence() monSentence {
	return msentence(mtext("Caravana's Nomad token can't read the host: it needs "), mname("node:read"), mtext(". Run the installer again to give it."))
}

// traefikSentence says why there are no request numbers; empty when Traefik answered.
func traefikSentence(st service.SamplerState) monSentence {
	switch st {
	case service.SamplerOff:
		return msentence(mtext("Traefik isn't serving its request numbers on this host. Run the installer again to turn them on."))
	case service.SamplerUnreachable:
		return msentence(mtext("Can't read Traefik's numbers right now."))
	case service.SamplerDisabled:
		return msentence(mtext("Request numbers are turned off: Caravana has no address for Traefik's metrics."))
	}
	return nil
}

// ---- the issues -----------------------------------------------------------------------------

// baselineStarted is "started 2026-10-25 UTC", or "" when nothing was sampled.
func (b *monBuilder) baselineStarted() string {
	if b.view.Baseline.Since == nil {
		return ""
	}
	return b.view.Baseline.Since.UTC().Format("2006-01-02") + " UTC"
}

// calm is the line shown when no service is unusual: nothing at all with no service.
func (b *monBuilder) calm() *monCalm {
	if len(b.view.Services) == 0 {
		return nil
	}
	if b.view.Baseline.State == metrics.Building {
		text := "The usual range is still being learned"
		if st := b.baselineStarted(); st != "" {
			text += ": started " + st + "."
		} else {
			text += ": nothing has been sampled yet."
		}
		return &monCalm{Mark: "idle", Text: msentence(mtext(text))}
	}
	if w := b.where(); w != nil {
		return &monCalm{Mark: "healthy", Text: append(msentence(mtext("Everything in ")), append(w, mtext(" is within its usual range."))...)}
	}
	return &monCalm{Mark: "healthy", Text: msentence(mtext("Every app is within its usual range."))}
}

// issue says one unusual service with the contract's fixed sentences.
func (b *monBuilder) issue(is service.MetricIssue) monIssue {
	name := b.serviceName(is.AppName, is.AppID, is.Service)
	out := monIssue{Mark: "warning"}
	if is.Level == metrics.LevelFailed {
		out.Mark = "failed"
	}
	started := b.startedClause(is)
	switch is.Kind {
	case metrics.Slower:
		text := msentence(mname(name), mtext(" is slower than usual. The slowest 5% of requests take "), mstrong(msWords(is.Value)))
		if is.Usual != nil {
			text = append(text, mtext(", usually "+msWords(*is.Usual)))
		}
		text = append(text, mtext(". "))
		out.Text = append(append(text, started...), b.deployClause(is)...)
		out.Detail = msentence(mtext(percentText(is.Window.ErrorPercent) + "% of requests return 5xx · " + rateText(is.Window.RequestsPerSec) + " req/s"))
		out.Action, out.ActionHref = b.showAction(name, "errors", is, true)
	case metrics.MoreErrors:
		text := msentence(mname(name), mtext(" returns more errors than usual: "), mstrong(percentText(is.Value)+"%"), mtext(" of requests"))
		if is.Usual != nil {
			text = append(text, mtext(", usually "+percentText(*is.Usual)+"%"))
		}
		text = append(text, mtext(". "))
		out.Text = append(append(text, started...), b.deployClause(is)...)
		out.Detail = msentence(mtext(plural(int(is.Window.Errors), "error") + " in the last 10 minutes"))
		out.Action, out.ActionHref = b.showAction(name, "errors", is, true)
	case metrics.Quieter:
		text := msentence(mname(name), mtext(" has much less traffic than usual: "), mstrong(rateText(is.Value)+" req/s"))
		if is.Usual != nil {
			text = append(text, mtext(", usually "+rateText(*is.Usual)))
		}
		text = append(text, mtext(". "))
		out.Text = append(text, started...)
		if len(is.Domains) > 0 {
			out.Detail = msentence(mtext("Its domains: " + strings.Join(is.Domains, ", ")))
		}
		out.Action, out.ActionHref = b.showAction(name, "logs", is, false)
	case metrics.Memory:
		limit := "memory limit"
		if is.LimitMB != nil {
			limit = sizeText(int64(*is.LimitMB+0.5)) + " memory limit"
		}
		out.Text = msentence(mname(name), mtext(" is using "), mstrong(strconv.Itoa(int(is.Value+0.5))+"%"), mtext(" of its "+limit+"."))
		if is.Restarts != nil && *is.Restarts > 0 {
			out.Text = append(out.Text, mtext(" It restarted "+plural(*is.Restarts, "time")+" in the last hour."))
		}
		out.Detail = b.exitDetail(is)
		out.Action, out.ActionHref = b.showAction(name, "logs", is, false)
	case metrics.Restarting:
		out.Text = msentence(mname(name), mtext(" restarted "+plural(int(is.Value+0.5), "time")+" in the last hour."))
		out.Detail = b.exitDetail(is)
		out.Action, out.ActionHref = b.showAction(name, "logs", is, false)
	}
	return out
}

// startedClause is "It started at 13:58 UTC." for an issue that has a run to walk back along.
func (b *monBuilder) startedClause(is service.MetricIssue) monSentence {
	if is.SinceCapped {
		return msentence(mtext("It started more than 6 hours ago."))
	}
	return msentence(mtext("It started at "), mclock(clockOf(b.now, is.Since)), mtext("."))
}

// deployClause says, as a fact, how the issue sits against a deploy of the environment: "It
// started 2 minutes after supplier-gw v24 rolled out." or "It started before ... rolled out."
func (b *monBuilder) deployClause(is service.MetricIssue) monSentence {
	d := is.DeployNear
	if d == nil {
		return nil
	}
	which := d.AppName + " v" + strconv.FormatUint(d.Version, 10)
	if d.Relation == metrics.StartedBefore {
		return msentence(mtext(" It started before "), mname(which), mtext(" rolled out."))
	}
	gap := "less than a minute"
	if d.Minutes >= 1 {
		gap = plural(d.Minutes, "minute")
	}
	return msentence(mtext(" It started "+gap+" after "), mname(which), mtext(" rolled out."))
}

// exitDetail is "copy e4d1 · exit 137 last at 13:56 UTC", or nothing when no exit is known.
func (b *monBuilder) exitDetail(is service.MetricIssue) monSentence {
	if is.LastExit == nil {
		return nil
	}
	return msentence(mtext("copy "+shortID(is.LastExit.Copy)+" · exit "+strconv.Itoa(is.LastExit.ExitCode)+" last at "), mclock(clockOf(b.now, is.LastExit.At)))
}

// showAction is the issue's action to the Logs page: the app's errors, or its logs.
func (b *monBuilder) showAction(name, what string, is service.MetricIssue, errorsOnly bool) (monSentence, string) {
	q := logsReq{Project: b.q.Project, Environment: b.q.Environment, App: is.AppID, Range: logsRangeDefault, Levels: defaultLevels, Live: true}
	if errorsOnly {
		q.Levels = levelSet{true, false, false, false}
	}
	return msentence(mtext("Show "), mname(name), mtext(" "+what)), q.href()
}

// ---- the apps table -------------------------------------------------------------------------

// table builds the apps table of the place.
func (b *monBuilder) table() monTable {
	t := monTable{}
	apps := map[string]bool{}
	for _, sv := range b.view.Services {
		apps[sv.AppID] = true
	}
	if w := b.where(); w != nil {
		t.Meta = append(msentence(mtext(strconv.Itoa(len(apps))+" in ")), w...)
	} else {
		t.Meta = msentence(mtext(appCount(len(apps))))
	}
	for _, it := range monSorts {
		t.Sorts = append(t.Sorts, logsBarItem{Label: it.Label, On: it.Sort == b.q.Sort, Href: b.q.href(monWithSort(it.Sort))})
	}
	if len(b.view.Services) == 0 {
		t.Empty = "No apps here yet. Their numbers appear a minute after they run."
		return t
	}
	for _, sv := range b.view.Services {
		t.Rows = append(t.Rows, b.row(sv))
	}
	return t
}

// row builds one service's row.
func (b *monBuilder) row(sv service.ServiceMetrics) monRow {
	name := b.serviceName(sv.AppName, sv.AppID, sv.Service)
	if b.multi[sv.AppID] {
		name = sv.AppName + " / " + sv.Service
	}
	kinds := b.kinds[serviceRef{sv.AppID, sv.Service}]
	row := monRow{
		Anchor: "mon-row-" + sv.AppID + "-" + sv.Service,
		Name:   name,
		Href:   "/apps/" + url.PathEscape(sv.AppID),
		Copies: copyCount(sv.Copies),
	}
	switch {
	case sv.State == service.ServiceStopped:
		row.Mark, row.MarkWord, row.Copies = "idle", "stopped", "stopped"
	case sv.State == service.ServiceUnusual && sv.Level == metrics.LevelFailed:
		row.Mark, row.MarkWord = "failed", "failing"
	case sv.State == service.ServiceUnusual:
		row.Mark, row.MarkWord = "warning", "unusual"
	case sv.State == service.ServiceRollingOut:
		row.Mark, row.MarkWord = "rolling", "rolling out"
	default:
		row.Mark, row.MarkWord = "healthy", "healthy"
	}
	if !sv.HasDomain {
		row.Copies += " · no domain"
	}

	none := func(why string) monNum { return monNum{Text: "—", None: true, Title: why} }
	if sv.Requests == nil || sv.Errors == nil || sv.P95 == nil {
		why := ""
		if sv.HTTPShared != "" {
			why = "Traefik names this service and " + sv.HTTPShared + " alike, so its numbers belong to neither."
		}
		row.Requests, row.Errors, row.P95 = none(why), none(why), none(why)
		if sv.Requests != nil {
			row.Requests = monNum{Text: rateText(*sv.Requests) + "/s", Warn: kinds[metrics.Quieter]}
		}
		if sv.Errors != nil {
			row.Errors = monNum{Text: percentText(*sv.Errors) + "%", Warn: kinds[metrics.MoreErrors]}
		}
	} else {
		row.Requests = monNum{Text: rateText(*sv.Requests) + "/s", Warn: kinds[metrics.Quieter]}
		row.Errors = monNum{Text: percentText(*sv.Errors) + "%", Warn: kinds[metrics.MoreErrors]}
		p := msWords(*sv.P95)
		if sv.P95Over {
			p = "over " + p
		}
		row.P95 = monNum{Text: p, Warn: kinds[metrics.Slower]}
		if kinds[metrics.Slower] && sv.UsualP95 != nil {
			row.P95.Usual = "usually " + rateText(*sv.UsualP95)
		}
	}
	if kinds[metrics.Quieter] && row.Requests.Text != "—" && sv.Requests != nil {
		row.Requests.Warn = true
	}

	row.Spark = b.spark(sv)
	row.Mem = b.memory(sv, kinds[metrics.Memory])
	if sv.Restarts == nil {
		row.Restarts = monNum{Text: "—", None: true}
	} else {
		row.Restarts = monNum{Text: strconv.FormatInt(*sv.Restarts, 10), Warn: *sv.Restarts > 0}
	}

	logs := logsReq{Project: b.q.Project, Environment: b.q.Environment, Service: sv.AppID + "/" + sv.Service, Range: logsRangeDefault, Levels: defaultLevels, Live: true}
	row.LogsHref, row.LogsName = logs.href(), "Logs of "+b.serviceName(sv.AppName, sv.AppID, sv.Service)
	return row
}

// memory is the memory cell: used against the limit.
func (b *monBuilder) memory(sv service.ServiceMetrics, issue bool) monMem {
	if sv.MemoryMB == nil || sv.MemoryLimitMB == nil || *sv.MemoryLimitMB <= 0 {
		return monMem{}
	}
	used, limit := float64(*sv.MemoryMB), float64(*sv.MemoryLimitMB)
	fill := min(100, used/limit*100)
	m := monMem{Known: true, Fill: units(fill), Warn: issue || used/limit*100 > metrics.MemoryPercent}
	if limit >= 1024 {
		m.Read = gbText(used) + " / " + gbText(limit) + " GB"
	} else {
		m.Read = strconv.FormatInt(*sv.MemoryMB, 10) + " / " + strconv.FormatInt(*sv.MemoryLimitMB, 10) + " MB"
	}
	m.Title = sizeText(*sv.MemoryMB) + " in use of a " + sizeText(*sv.MemoryLimitMB) + " limit"
	return m
}

// sparkW and sparkH are the CPU sparkline's box.
const (
	sparkW = 120.0
	sparkH = 24.0
)

// spark is the CPU line: scaled from zero to the larger of what is reserved and what was used, so
// a service using a fifth of its share draws a fifth of the way up.
func (b *monBuilder) spark(sv service.ServiceMetrics) *monSpark {
	top := 0.0
	if sv.CPUReservedMHz != nil {
		top = float64(*sv.CPUReservedMHz)
	}
	var last *float64
	for _, v := range sv.CPU {
		if v != nil {
			top = max(top, *v)
			last = v
		}
	}
	if last == nil {
		return nil
	}
	if top <= 0 {
		top = 1
	}
	n := len(sv.CPU)
	x := func(i int) float64 {
		if n < 2 {
			return 0
		}
		return float64(i) / float64(n-1) * sparkW
	}
	y := func(v float64) float64 { return sparkH - 2 - v/top*(sparkH-4) }
	return &monSpark{Segments: segments(sv.CPU, x, y), Title: b.cpuTitle(*last, sv.CPUReservedMHz)}
}

// cpuTitle is "0.4 of 1 core reserved, now" (in cores when the host's core speed is known, else
// MHz).
func (b *monBuilder) cpuTitle(now float64, reserved *int64) string {
	if reserved == nil {
		return trim1(now) + " MHz now"
	}
	if b.coreMHz > 0 {
		res := float64(*reserved) / b.coreMHz
		unit := "cores"
		if trim1(res) == "1" {
			unit = "core"
		}
		return trim1(now/b.coreMHz) + " of " + trim1(res) + " " + unit + " reserved, now"
	}
	return strconv.Itoa(int(now+0.5)) + " of " + strconv.FormatInt(*reserved, 10) + " MHz reserved, now"
}

// segments turns a series into the point lists of its polylines: a gap ends one and starts
// another, so a stretch nobody measured is never drawn as zero. A lone point is drawn as a short
// stroke so it shows.
func segments(vals []*float64, x func(int) float64, y func(float64) float64) []string {
	var out []string
	var cur []string
	flush := func() {
		switch len(cur) {
		case 0:
		case 1:
			out = append(out, cur[0]+" "+cur[0])
		default:
			out = append(out, strings.Join(cur, " "))
		}
		cur = nil
	}
	for i, v := range vals {
		if v == nil {
			flush()
			continue
		}
		cur = append(cur, units(x(i))+","+units(y(*v)))
	}
	flush()
	return out
}
