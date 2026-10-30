package ui

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The Operator Overview (feature 027, page 1): /ops, the host and everything on
// it. It reads only internal/service. The headline, the Needs-you list and the
// "This host" meters are the Developer home's and Monitoring's own functions and
// templates, so the two views never disagree; what is new here is the server
// panel with its copies, the apps table across every place and the live
// activity.

// opsChipLimit is how many copies the server panel draws before "and N more".
const opsChipLimit = 40

// opsActivityLimit is how many entries the Live activity panel lists.
const opsActivityLimit = 8

// ---- what the page draws ----------------------------------------------------------------------

// opsPage is the whole Overview. Its regions are separate values so that a
// part draws one of them alone with the same templates.
type opsPage struct {
	pageData
	Main     opsMain
	Needs    attentionRegion
	Activity opsActivityRegion
	Capacity opsCapacity
}

// opsMain is what /parts/ops/overview draws: the headline and the stats (a
// slot outside the region, swapped with it), the server and the apps.
type opsMain struct {
	Head opsHead
	// NomadDown says Nomad couldn't be read: the page says so and the server
	// panel and the copies are not drawn.
	NomadDown bool
	Server    opsServer
	Apps      opsApps
	// NeedsMeta is the count beside "Needs you" ("3" or "nothing"), swapped with
	// the region because the list changes on the notices this region listens to.
	NeedsMeta string
}

// opsHead is the headline and the stats line.
type opsHead struct {
	Headline sentence
	Stats    []string
}

// opsServer is the Server section: the room sentence and the one node.
type opsServer struct {
	Room *monRoom
	Node opsNode
}

// opsNode is the host's panel. Message, when set, replaces the panel's body
// (Nomad can't be read, or nothing was read yet).
type opsNode struct {
	Message string
	Mark    string
	Name    string
	Href    string
	Pool    string
	// Copies is "18 copies" ("" when Nomad couldn't be read).
	Copies string
	// State is the pill drawn when the node isn't ready.
	State  *statusView
	Spec   string
	Meters []monMeter
	Chips  []opsChip
	// More is how many copies past the first opsChipLimit aren't drawn.
	More int
}

// opsChip is one copy of the server panel: the app's name and the copy's short
// ID, the copy's mark and a link to the app's Running tab.
type opsChip struct {
	Name  string
	Short string
	Title string
	Mark  string
	State string // "starting", "draining" or ""
	Href  string
	Role  string
}

// opsApps is the Apps section.
type opsApps struct {
	Meta  string
	Rows  []opsAppRow
	Empty bool
}

// opsAppRow is one app of the table: the Apps home's row, with the columns the
// Operator table has.
type opsAppRow struct {
	appRow
	// Where is the app's place when it isn't Default › production: "Shop › staging ›".
	Where string
	// Marks are the copies, one mark each; NoMarks says why there are none to draw.
	Marks     []string
	MarksWord string // "2 copies", for a screen reader
	NoMarks   string
	Spark     *monSpark
	Warn      bool // the app is unusual by 025's rules: the line is drawn in warn
	Mem       string
	Reach     string
	Change    string
	ChangeT   when
}

// opsActivityRegion is the Live activity panel.
type opsActivityRegion struct {
	Events []opsEvent
	// NomadDown adds the line that says the node's events couldn't be read.
	NomadDown bool
}

// opsEvent is one row of the Live activity panel.
type opsEvent struct {
	ID       string
	Time     string // "14:02:14", UTC
	ISO      string
	Title    string // "2026-10-30 14:02:14 UTC"
	Kind     string
	Mark     string
	Sentence sentence
	Href     string
}

// opsCapacity is the Capacity panel: 025's "This host" without the chart.
type opsCapacity struct {
	Lines  []monHostLine
	Meters []monMeter
	Room   *monRoom
}

// ---- handlers ---------------------------------------------------------------------------------

// showOps answers GET /ops.
func (s *server) showOps(w http.ResponseWriter, r *http.Request, session string) {
	main, needs, err := s.loadOpsMain(r)
	if err != nil {
		s.serverError(w, "draw the Operator overview", err)
		return
	}
	activity, err := s.loadOpsActivity(r, main.NomadDown)
	if err != nil {
		s.serverError(w, "read activity", err)
		return
	}
	capacity, err := s.loadOpsCapacity(r)
	if err != nil {
		s.serverError(w, "read the host", err)
		return
	}
	head, err := s.shellPage(r, "Overview", navOpsOverview, crumb{Label: "Overview"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live?watch=ops"
	s.render(w, http.StatusOK, "ops", opsPage{pageData: head, Main: main, Needs: needs, Activity: activity, Capacity: capacity})
}

// opsOOB is what /parts/ops/overview answers: the region, and the headline and
// the Needs-you count, which sit outside it and are swapped by id.
type opsOOB struct {
	opsMain
}

// partOpsOverview answers GET /parts/ops/overview.
func (s *server) partOpsOverview(w http.ResponseWriter, r *http.Request, session string) {
	main, _, err := s.loadOpsMain(r)
	if err != nil {
		s.log.Error("draw the Operator overview", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "ops", "ops-main-oob", opsOOB{main})
}

// partOpsActivity answers GET /parts/ops/activity.
func (s *server) partOpsActivity(w http.ResponseWriter, r *http.Request, session string) {
	region, err := s.loadOpsActivity(r, s.nomadDown(r.Context()))
	if err != nil {
		s.log.Error("read activity", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "ops", "ops-activity-region", region)
}

// partOpsCapacity answers GET /parts/ops/capacity.
func (s *server) partOpsCapacity(w http.ResponseWriter, r *http.Request, session string) {
	capacity, err := s.loadOpsCapacity(r)
	if err != nil {
		s.log.Error("read the host", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "ops", "ops-capacity-region", capacity)
}

// nomadDown says Nomad can't be read right now, judged by the sampler's last
// reading of it: the cheap answer a part uses when it doesn't read the copies.
func (s *server) nomadDown(ctx context.Context) bool {
	h, err := s.svc.Host(ctx)
	return err == nil && nomadUnread(h)
}

// nomadUnread says the sampler's last tick couldn't read Nomad.
func nomadUnread(h service.HostView) bool {
	return h.Sources.Nomad == service.SamplerDenied || h.Sources.Nomad == service.SamplerUnreachable
}

// ---- loaders ----------------------------------------------------------------------------------

// loadOpsMain reads what the main region and the Needs-you list draw.
func (s *server) loadOpsMain(r *http.Request) (opsMain, attentionRegion, error) {
	ctx := r.Context()
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return opsMain{}, attentionRegion{}, err
	}
	sums, err := s.svc.ListApps(ctx)
	if err != nil {
		return opsMain{}, attentionRegion{}, err
	}
	needs, items, err := s.loadAttention(r, pl, sums)
	if err != nil {
		return opsMain{}, attentionRegion{}, err
	}
	host, err := s.svc.Host(ctx)
	if err != nil {
		return opsMain{}, attentionRegion{}, err
	}
	copies, copiesErr := s.svc.AllCopies(ctx)
	if copiesErr != nil && !errors.Is(copiesErr, deploy.ErrNomadUnavailable) {
		s.log.Warn("can't read the copies for the Operator overview", "error", copiesErr)
	}
	down := copiesErr != nil || nomadUnread(host)
	now := s.now()

	listed := map[string]bool{}
	for _, it := range items {
		listed[it.AppID] = true
	}
	rows := s.opsRows(ctx, now, pl, sums, listed, copies, copiesErr)

	m := opsMain{NomadDown: down}
	m.Head = opsHead{Headline: headlineOf(sums, items)}
	m.Apps = opsAppsOf(rows, sums, copies, copiesErr)
	m.Server = opsServer{Room: roomOf(host), Node: s.opsNode(host, rows, copies, down)}
	m.Head.Stats = opsStats(host, len(sums), copies, copiesErr, len(items))
	switch n := len(items); n {
	case 0:
		m.NeedsMeta = "nothing"
	default:
		m.NeedsMeta = strconv.Itoa(n)
	}
	return m, needs, nil
}

// opsStats is the stats line: "1 server · 13 apps · 18 copies · cpu 38% · mem 63% · 3 open items".
// Before the host's first reading it says so instead of the use and the items.
func opsStats(h service.HostView, apps int, copies map[string][]service.Copy, copiesErr error, open int) []string {
	out := []string{"1 server", appCount(apps)}
	if copiesErr == nil {
		out = append(out, copyCount(runningCopies(copies)))
	}
	if h.Recent == nil {
		return append(out, "waiting for the first reading")
	}
	if h.Recent.CPUPercent != nil {
		out = append(out, "cpu "+strconv.Itoa(int(*h.Recent.CPUPercent+0.5))+"%")
	}
	if h.Recent.MemoryPercent != nil {
		out = append(out, "mem "+strconv.Itoa(int(*h.Recent.MemoryPercent+0.5))+"%")
	}
	return append(out, plural(open, "open item"))
}

// runningCopies counts the copies that run or are starting, across apps.
func runningCopies(byApp map[string][]service.Copy) int {
	n := 0
	for _, copies := range byApp {
		for _, c := range copies {
			if !c.Terminal {
				n++
			}
		}
	}
	return n
}

// opsRows builds the apps table's rows, problems first, then by name.
func (s *server) opsRows(ctx context.Context, now time.Time, pl places, sums []service.AppSummary, listed map[string]bool,
	copies map[string][]service.Copy, copiesErr error) []opsAppRow {
	usage := s.opsUsage(ctx, pl, sums)
	rows := make([]opsAppRow, 0, len(sums))
	for _, sum := range sums {
		base := s.appRow(now, sum, needsYouOf(sum, listed[sum.App.ID]))
		row := opsAppRow{appRow: base}
		if !atHome(sum.Place) {
			row.Where = pl.display(sum.Place.Project) + " › " + sum.Place.Environment + " ›"
		}
		switch {
		case copiesErr != nil:
			row.NoMarks = "—"
		default:
			for _, c := range copies[sum.App.ID] {
				if c.Terminal && c.Status != deploy.CopyFailed {
					continue
				}
				row.Marks = append(row.Marks, copyMark(c))
			}
			if len(row.Marks) == 0 {
				row.NoMarks = "none"
			} else {
				row.MarksWord = copyCount(len(row.Marks))
			}
		}
		if u, ok := usage[sum.App.ID]; ok {
			row.Spark, row.Warn, row.Mem = u.Spark, u.Unusual, u.Mem
		}
		row.Reach = ""
		if len(base.Domains) > 0 {
			row.Reach = base.Domains[0].Host
		}
		if d := sum.LastDeployment; d != nil {
			row.Change = versionLabel(d.JobVersion)
			row.ChangeT = whenOf(now, &d.CreatedAt, "")
		}
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(a, b opsAppRow) int {
		return cmp.Or(statusRank[b.Sort]-statusRank[a.Sort], strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return rows
}

// copyMark is the mark kind of one copy, as the Running tab draws it.
func copyMark(c service.Copy) string {
	if c.Stopping {
		return "idle"
	}
	return copyStatusView(c.Status).Kind
}

// opsAppsOf is the Apps section: "13 running" is the apps with a running copy.
func opsAppsOf(rows []opsAppRow, sums []service.AppSummary, copies map[string][]service.Copy, copiesErr error) opsApps {
	running := 0
	for _, sum := range sums {
		switch {
		case copiesErr != nil:
			if sum.Status.HealthyCopies > 0 {
				running++
			}
		case slices.ContainsFunc(copies[sum.App.ID], func(c service.Copy) bool { return !c.Terminal }):
			running++
		}
	}
	return opsApps{Rows: rows, Empty: len(rows) == 0, Meta: strconv.Itoa(running) + " running"}
}

// opsUse is what 025 knows of an app: the CPU line of the last hour, whether
// the app is unusual, and the memory it uses now.
type opsUse struct {
	Spark   *monSpark
	Unusual bool
	Mem     string
}

// opsSparkW and opsSparkH are the table's sparkline box, 84 by 16.
const (
	opsSparkW = 84.0
	opsSparkH = 16.0
)

// opsUsage reads 025's numbers of every place that has apps and folds each
// app's services into one line and one memory figure. A place that can't be
// read leaves its apps without them ("—"): the table is still true.
func (s *server) opsUsage(ctx context.Context, pl places, sums []service.AppSummary) map[string]opsUse {
	have := map[app.Place]bool{}
	for _, sum := range sums {
		have[app.Place{Project: sum.Place.Project, Environment: sum.Place.Environment}] = true
	}
	out := map[string]opsUse{}
	coreMHz := 0.0
	if h, err := s.svc.Host(ctx); err == nil {
		coreMHz = coreMHzOf(h.CPU)
	}
	for place := range have {
		view, err := s.svc.Metrics(ctx, service.MetricsQuery{Project: place.Project, Environment: place.Environment, Range: metrics.Range1h})
		if err != nil {
			s.log.Warn("can't read the numbers of a place for the Operator overview", "project", place.Project, "environment", place.Environment, "error", err)
			continue
		}
		byApp := map[string][]service.ServiceMetrics{}
		for _, sv := range view.Services {
			byApp[sv.AppID] = append(byApp[sv.AppID], sv)
		}
		for id, svs := range byApp {
			out[id] = opsUseOf(svs, coreMHz)
		}
	}
	return out
}

// opsUseOf folds an app's services: the CPU series summed (a point is a gap
// only when no service has a number), the memory summed, and unusual when any
// service is.
func opsUseOf(svs []service.ServiceMetrics, coreMHz float64) opsUse {
	u := opsUse{}
	n := 0
	for _, sv := range svs {
		n = max(n, len(sv.CPU))
		u.Unusual = u.Unusual || sv.Unusual
	}
	series := make([]*float64, n)
	reserved, haveReserved := 0.0, false
	for _, sv := range svs {
		for i, v := range sv.CPU {
			if v == nil {
				continue
			}
			sum := *v
			if series[i] != nil {
				sum += *series[i]
			}
			series[i] = &sum
		}
		if sv.CPUReservedMHz != nil {
			reserved, haveReserved = reserved+float64(*sv.CPUReservedMHz), true
		}
	}
	top, last := 0.0, (*float64)(nil)
	if haveReserved {
		top = reserved
	}
	for _, v := range series {
		if v != nil {
			top, last = max(top, *v), v
		}
	}
	if last != nil {
		if top <= 0 {
			top = 1
		}
		x := func(i int) float64 {
			if n < 2 {
				return 0
			}
			return float64(i) / float64(n-1) * opsSparkW
		}
		y := func(v float64) float64 { return opsSparkH - 1.5 - v/top*(opsSparkH-3) }
		title := strconv.Itoa(int(*last+0.5)) + " MHz now"
		if coreMHz > 0 {
			title = vcpuText(*last/coreMHz) + " vCPU now"
		}
		u.Spark = &monSpark{Segments: segments(series, x, y), Title: "CPU, last hour: " + title}
	}
	total, known := int64(0), false
	for _, sv := range svs {
		if sv.MemoryMB != nil {
			total, known = total+*sv.MemoryMB, true
		}
	}
	if known {
		u.Mem = sizeText(total)
	}
	return u
}

// opsNode builds the host's panel. rows are the apps' rows in table order, so
// the chips are grouped by app as the table lists them.
func (s *server) opsNode(h service.HostView, rows []opsAppRow, copies map[string][]service.Copy, down bool) opsNode {
	n := h.Node
	switch {
	case down:
		return opsNode{Mark: "idle", Message: "Nomad can't be reached right now, so the server's state isn't shown."}
	case n == nil:
		return opsNode{Mark: "idle", Message: "Waiting for the first reading of the server."}
	}
	node := opsNode{Name: n.Name, Href: "/ops/servers/" + url.PathEscape(n.Name), Pool: n.Pool, Mark: nodeMark(n.State)}
	node.Copies = copyCount(runningCopies(copies))
	if n.State != "ready" {
		node.State = &statusView{Kind: nodeMark(n.State), Word: n.State}
	}
	node.Spec = nodeSpec(n)
	node.Meters = opsMeters(h)

	all := 0
	for _, row := range rows {
		recs := copies[row.ID]
		for _, c := range recs {
			if c.Terminal && c.Status != deploy.CopyFailed {
				continue
			}
			all++
			if len(node.Chips) >= opsChipLimit {
				continue
			}
			chip := opsChip{Name: row.Name, Short: shortID(c.ID), Href: "/apps/" + url.PathEscape(row.ID), Mark: copyMark(c)}
			word := copyStatusView(c.Status).Word
			switch {
			case c.Stopping:
				chip.State, chip.Role = "draining", "draining"
			case c.Status == deploy.CopyStarting:
				chip.State, chip.Role = "starting", word
			default:
				chip.Role = word
			}
			chip.Title = "copy " + c.ID + ", " + c.Service + ", " + chip.Role + ", v" + strconv.FormatUint(c.Version, 10)
			node.Chips = append(node.Chips, chip)
		}
	}
	node.More = all - len(node.Chips)
	return node
}

// nodeMark is the mark kind of a node's state: ready, draining (rolling),
// ineligible (warning), down (failed), anything else idle.
func nodeMark(state string) string {
	switch state {
	case "ready":
		return "healthy"
	case "draining":
		return "rolling"
	case "ineligible":
		return "warning"
	case "down":
		return "failed"
	}
	return "idle"
}

// nodeSpec is "4 vCPU · 7.8 GB · Ubuntu 24.04 · kernel 6.8.0-147-generic": only
// what Nomad reported.
func nodeSpec(n *service.HostNode) string {
	var parts []string
	if n.Cores > 0 {
		parts = append(parts, strconv.Itoa(n.Cores)+" vCPU")
	}
	if n.MemoryMB > 0 {
		parts = append(parts, gbText(float64(n.MemoryMB))+" GB")
	}
	if n.OS != "" {
		r, size := utf8.DecodeRuneInString(n.OS)
		os := string(unicode.ToUpper(r)) + n.OS[size:]
		parts = append(parts, strings.TrimSpace(os+" "+n.OSVersion))
	}
	if n.Kernel != "" {
		parts = append(parts, "kernel "+n.Kernel)
	}
	return strings.Join(parts, " · ")
}

// opsMeters are 025's meters of the host with the panel's short readouts:
// "1.2/2 vCPU", "1.7/1.9G", "5.8/18.3G". The fill, the hatched reservation and
// the warn rule are the same numbers as everywhere else.
func opsMeters(h service.HostView) []monMeter {
	if h.SampledAt == nil {
		return nil
	}
	meters := hostMeters(h)
	for i := range meters {
		m := &meters[i]
		switch m.Label {
		case "mem":
			if h.Memory.Used != nil && h.Memory.Total != nil {
				m.Read = gbText(float64(*h.Memory.Used)) + "/" + gbText(float64(*h.Memory.Total)) + "G"
				m.Split = false
			}
		case "cpu":
			if h.CPU.Used != nil && h.CPU.Total != nil {
				perCore := coreMHzOf(h.CPU)
				if perCore > 0 {
					m.Read = vcpuText(float64(*h.CPU.Used)/perCore) + "/" + vcpuText(float64(*h.CPU.Total)/perCore) + " vCPU"
				} else {
					m.Read = strconv.FormatInt(*h.CPU.Used, 10) + "/" + strconv.FormatInt(*h.CPU.Total, 10) + " MHz"
				}
				m.Split = false
			}
		}
	}
	return meters
}

// loadOpsActivity reads the last entries of the feed. nomadDown adds the line
// that says the node's own events couldn't be read.
func (s *server) loadOpsActivity(r *http.Request, nomadDown bool) (opsActivityRegion, error) {
	entries, err := s.svc.Activity(r.Context(), service.ActivityQuery{Limit: opsActivityLimit})
	if err != nil {
		return opsActivityRegion{}, err
	}
	region := opsActivityRegion{NomadDown: nomadDown}
	for _, e := range entries {
		region.Events = append(region.Events, opsEventOf(e))
	}
	return region, nil
}

// opsEventOf turns an entry into a row: the time to the second in UTC, the kind
// word, the mark and the sentence with the app's name in mono.
func opsEventOf(e service.ActivityEntry) opsEvent {
	t := e.Time.UTC()
	ev := opsEvent{
		ID: e.ID, Time: t.Format("15:04:05"), ISO: t.Format(time.RFC3339), Title: t.Format("2006-01-02 15:04:05 UTC"),
		Kind: e.Kind, Mark: e.Status,
	}
	if e.AppName != "" && strings.HasPrefix(e.Sentence, e.AppName) {
		ev.Sentence = sentence{mono(e.AppName), plain(strings.TrimPrefix(e.Sentence, e.AppName))}
	} else {
		ev.Sentence = plainS(e.Sentence)
	}
	if e.AppID != "" {
		ev.Href = "/apps/" + url.PathEscape(e.AppID)
	}
	return ev
}

// loadOpsCapacity reads 025's host numbers for the Capacity panel.
func (s *server) loadOpsCapacity(r *http.Request) (opsCapacity, error) {
	h, err := s.svc.Host(r.Context())
	if err != nil {
		return opsCapacity{}, err
	}
	v := hostViewOf(s.now().UTC(), h)
	return opsCapacity{Lines: v.Lines, Meters: v.Meters, Room: v.Room}, nil
}
