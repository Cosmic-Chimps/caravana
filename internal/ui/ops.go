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
	Head opsHead
	// NeedsMeta is the count beside "Needs you" ("3" or "nothing").
	NeedsMeta string
	Server    opsServerRegion
	Apps      opsApps
	Needs     attentionRegion
	Activity  opsActivityRegion
}

// opsData is what one read of the host and its apps gives the page and its two parts.
type opsData struct {
	Head      opsHead
	NeedsMeta string
	Server    opsServerRegion
	Apps      opsApps
}

// opsHead is the headline and the stats line.
type opsHead struct {
	Headline sentence
	Stats    []string
}

// opsServerRegion is the Server section: its own live region, swapped on the notices that change
// the host and its copies. NomadDown says Nomad couldn't be read: the section says so and the
// panel and the copies are not drawn.
type opsServerRegion struct {
	NomadDown bool
	Room      *monRoom
	Node      opsNode
}

// opsServerPart is what /parts/ops/server answers: the region, and the headline and stats,
// which sit outside it and are swapped by id.
type opsServerPart struct {
	Server opsServerRegion
	Head   opsHead
}

// opsAppsPart is what /parts/ops/apps answers: the region, the headline and stats, and the
// Needs-you count, which sit outside it and are swapped by id.
type opsAppsPart struct {
	Apps      opsApps
	Head      opsHead
	NeedsMeta string
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
	// ID is the link's id, stable across a swap so that focus stays on it ("ops-copy-<copy id>").
	ID    string
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
	// LinkID is the app link's id, stable across a swap so that focus stays on it ("ops-app-<app id>").
	LinkID string
	// Marks are the copies, one mark each; NoMarks says why there are none to draw.
	Marks     []string
	MarksWord string // "2 copies", for a screen reader
	NoMarks   string
	CPU       opsCPU
	Mem       string
	Reach     string
	Change    string
	ChangeT   when
}

// opsCPU is the CPU cell: the line of the last hour and, only when 025's rules call the app
// unusual, a warn mark before it with the line, both one link to the app on /monitoring.
type opsCPU struct {
	Spark *monSpark
	// Stopped says the app is stopped: the line is what it used while it ran and the cell reads
	// "—" beside it, never a "now" value.
	Stopped bool
	// Issue is 025's sentence for the app and ", open monitoring"; Href and ID are the link's. All
	// three are empty unless 025 has an issue for the app.
	Issue, Href, ID string
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

// ---- handlers ---------------------------------------------------------------------------------

// showOps answers GET /ops.
func (s *server) showOps(w http.ResponseWriter, r *http.Request, session string) {
	data, needs, err := s.loadOps(r)
	if err != nil {
		s.serverError(w, "draw the Operator overview", err)
		return
	}
	activity, err := s.loadOpsActivity(r, data.Server.NomadDown)
	if err != nil {
		s.serverError(w, "read activity", err)
		return
	}
	head, err := s.shellPage(r, "Overview", navOpsOverview, crumb{Label: "Overview"})
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	head.LiveURL = "/live?watch=ops"
	s.render(w, http.StatusOK, "ops", opsPage{pageData: head, Head: data.Head, NeedsMeta: data.NeedsMeta,
		Server: data.Server, Apps: data.Apps, Needs: needs, Activity: activity})
}

// partOpsServer answers GET /parts/ops/server: the Server section, and the headline and stats.
func (s *server) partOpsServer(w http.ResponseWriter, r *http.Request, session string) {
	data, _, err := s.loadOps(r)
	if err != nil {
		s.log.Error("draw the Operator overview", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "ops", "ops-server-part", opsServerPart{Server: data.Server, Head: data.Head})
}

// partOpsApps answers GET /parts/ops/apps: the Apps section, the headline and stats, and the
// Needs-you count.
func (s *server) partOpsApps(w http.ResponseWriter, r *http.Request, session string) {
	data, _, err := s.loadOps(r)
	if err != nil {
		s.log.Error("draw the Operator overview", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "ops", "ops-apps-part", opsAppsPart{Apps: data.Apps, Head: data.Head, NeedsMeta: data.NeedsMeta})
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

// loadOps reads what the headline, the Server and Apps sections and the Needs-you list draw.
func (s *server) loadOps(r *http.Request) (opsData, attentionRegion, error) {
	ctx := r.Context()
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return opsData{}, attentionRegion{}, err
	}
	sums, err := s.svc.ListApps(ctx)
	if err != nil {
		return opsData{}, attentionRegion{}, err
	}
	needs, items, err := s.loadAttention(r, pl, sums)
	if err != nil {
		return opsData{}, attentionRegion{}, err
	}
	host, err := s.svc.Host(ctx)
	if err != nil {
		return opsData{}, attentionRegion{}, err
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
	rows := s.opsRows(ctx, now, pl, host, sums, listed, copies, copiesErr)

	d := opsData{
		Head:   opsHead{Headline: headlineOf(sums, items), Stats: opsStats(host, len(sums), copies, copiesErr, len(items))},
		Apps:   opsAppsOf(rows, sums, copies, copiesErr),
		Server: opsServerRegion{NomadDown: down, Room: roomOf(host), Node: s.opsNode(host, rows, copies, down)},
	}
	switch n := len(items); n {
	case 0:
		d.NeedsMeta = "nothing"
	default:
		d.NeedsMeta = strconv.Itoa(n)
	}
	return d, needs, nil
}

// opsStats is the stats line: "1 server · 13 apps · 18 copies · cpu 38% · mem 63% · 3 open items".
// The use is the newest sample's, the one the Server panel's meters show (one window, never a mean
// of the last minutes). Before the host's first reading it says so instead of the use and the items.
func opsStats(h service.HostView, apps int, copies map[string][]service.Copy, copiesErr error, open int) []string {
	out := []string{"1 server", appCount(apps)}
	if copiesErr == nil {
		out = append(out, copyCount(runningCopies(copies)))
	}
	if h.SampledAt == nil {
		return append(out, "waiting for the first reading")
	}
	if p, ok := usedPercent(h.CPU.Used, h.CPU.Total); ok {
		out = append(out, "cpu "+strconv.Itoa(p)+"%")
	}
	if p, ok := usedPercent(h.Memory.Used, h.Memory.Total); ok {
		out = append(out, "mem "+strconv.Itoa(p)+"%")
	}
	return append(out, plural(open, "open item"))
}

// usedPercent is used over total in whole percent; ok is false when either wasn't read.
func usedPercent(used, total *int64) (int, bool) {
	if used == nil || total == nil || *total <= 0 {
		return 0, false
	}
	return int(float64(*used)/float64(*total)*100 + 0.5), true
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
func (s *server) opsRows(ctx context.Context, now time.Time, pl places, host service.HostView, sums []service.AppSummary, listed map[string]bool,
	copies map[string][]service.Copy, copiesErr error) []opsAppRow {
	usage := s.opsUsage(ctx, pl, host, sums)
	rows := make([]opsAppRow, 0, len(sums))
	for _, sum := range sums {
		base := s.appRow(now, sum, needsYouOf(sum, listed[sum.App.ID]))
		row := opsAppRow{appRow: base, LinkID: "ops-app-" + sum.App.ID}
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
		stopped := sum.StoppedAt != nil
		if u, ok := usage[sum.App.ID]; ok {
			row.CPU.Spark, row.Mem = u.Spark, u.Mem
			stopped = stopped || u.Stopped
			if u.Issue != "" && !stopped {
				row.CPU.Issue, row.CPU.Href, row.CPU.ID = u.Issue, u.Href, "ops-cpu-"+sum.App.ID
			}
		}
		if stopped {
			// A stopped app has no "now": the line is what it used while it ran, as Monitoring says.
			row.CPU.Stopped, row.Mem = true, ""
			if row.CPU.Spark != nil {
				row.CPU.Spark.Title = monStoppedCPUTitle
			}
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

// opsUse is what 025 knows of an app: the CPU line of the last hour, the memory it uses now, whether
// it is stopped, and, when 025's rules call it unusual, their sentence and the link to say it.
type opsUse struct {
	Spark   *monSpark
	Mem     string
	Stopped bool
	// Issue is 025's sentence for the app's worst issue (and how many more), ", open monitoring"
	// after it; Href is the app's row on /monitoring.
	Issue, Href string
}

// opsSparkW and opsSparkH are the table's sparkline box, 84 by 16.
const (
	opsSparkW = 84.0
	opsSparkH = 16.0
)

// opsUsage reads 025's numbers of every place that has apps and folds each
// app's services into one line and one memory figure. A place that can't be
// read leaves its apps without them ("—"): the table is still true.
func (s *server) opsUsage(ctx context.Context, pl places, host service.HostView, sums []service.AppSummary) map[string]opsUse {
	have := map[app.Place]bool{}
	for _, sum := range sums {
		have[app.Place{Project: sum.Place.Project, Environment: sum.Place.Environment}] = true
	}
	out := map[string]opsUse{}
	coreMHz := coreMHzOf(host.CPU)
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
		q := monReq{Project: cmp.Or(place.Project, app.DefaultProject), Environment: cmp.Or(place.Environment, app.DefaultEnvironment),
			Range: metrics.Range6h, Sort: service.SortUnusual}
		b := newMonBuilder(q, pl, view, host)
		issues := map[string][]service.MetricIssue{}
		for _, is := range view.Issues {
			issues[is.AppID] = append(issues[is.AppID], is)
		}
		for id, svs := range byApp {
			u := opsUseOf(svs, coreMHz)
			if list := issues[id]; len(list) > 0 {
				// The worst first, as 025 lists them; the link is that service's row on /monitoring.
				u.Issue = issueLabel(b.issue(list[0]).Text.String(), len(list)-1)
				u.Href = q.href() + "#mon-row-" + id + "-" + list[0].Service
			}
			out[id] = u
		}
	}
	return out
}

// issueLabel is 025's sentence for an app's worst issue, its closing full stop dropped, with how
// many other issues the app has, and ", open monitoring": the link's name and title.
func issueLabel(sentence string, more int) string {
	sentence = strings.TrimRight(strings.TrimSpace(sentence), ".")
	if more > 0 {
		sentence += " (and " + plural(more, "more issue") + ")"
	}
	return sentence + ", open monitoring"
}

// opsUseOf folds an app's services: the CPU series summed (a point is a gap
// only when no service has a number), the memory summed. The app is stopped
// when every service is; it has no memory then.
func opsUseOf(svs []service.ServiceMetrics, coreMHz float64) opsUse {
	u := opsUse{Stopped: len(svs) > 0}
	n := 0
	for _, sv := range svs {
		n = max(n, len(sv.CPU))
		u.Stopped = u.Stopped && sv.State == service.ServiceStopped
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
	if known && !u.Stopped {
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
			chip := opsChip{ID: "ops-copy-" + c.ID, Name: row.Name, Short: shortID(c.ID), Href: "/apps/" + url.PathEscape(row.ID), Mark: copyMark(c)}
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

// opsMeters are 025's meters of the host with Monitoring's own split readouts ("1.5G used · 0.7G
// reserved of 1.9G", "0.1 used · 0.72 reserved of 2 vCPU"): the same function, so the panel and
// the Monitoring page never disagree. Nothing before the host's first sample.
func opsMeters(h service.HostView) []monMeter {
	if h.SampledAt == nil {
		return nil
	}
	meters := hostMeters(h)
	// The disk's readout goes under its bar like memory's and CPU's: "5.5G used of 18.3G · /". The
	// disk meters are the last ones, one per watched filesystem.
	for i, d := range h.Disks {
		if at := len(meters) - len(h.Disks) + i; at >= 0 {
			meters[at].Read = gbText(float64(d.UsedMB)) + "G used of " + gbText(float64(d.TotalMB)) + "G · " + d.Mount
			meters[at].Split = true
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
// word, the mark and the sentence, whose parts the service marked mono (program, app,
// service, schedule and version names), drawn by the console's "sentence" template.
func opsEventOf(e service.ActivityEntry) opsEvent {
	t := e.Time.UTC()
	ev := opsEvent{
		ID: e.ID, Time: t.Format("15:04:05"), ISO: t.Format(time.RFC3339), Title: t.Format("2006-01-02 15:04:05 UTC"),
		Kind: e.Kind, Mark: e.Status,
	}
	for _, p := range e.Parts {
		ev.Sentence = append(ev.Sentence, part{Text: p.Text, Mono: p.Mono, Muted: p.Muted})
	}
	if len(ev.Sentence) == 0 {
		ev.Sentence = plainS(e.Sentence)
	}
	if e.AppID != "" {
		ev.Href = "/apps/" + url.PathEscape(e.AppID)
	}
	return ev
}
