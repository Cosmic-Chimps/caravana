package ui

import (
	"cmp"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The Operator server page (feature 027, page 2): /ops/servers/{name}, the one host with its
// numbers, the copies on it, its facts and what happened to it lately. It reads only
// internal/service. The numbers and the room sentence are 025's own functions (the meters, the
// room), so this page and Monitoring never disagree; what is new here is the head, the copies
// with their use against their reservation, the facts and the server's own history.
//
// With one host there is no list of servers: /ops/servers redirects to the one, and Phase 3
// turns it into the list without moving this page (contracts/page2-server.md).

// srvRecentLimit is how many entries "Recent on this server" lists.
const srvRecentLimit = 8

// ---- what the page draws ----------------------------------------------------------------------

// opsServerPage is the whole page. Missing is set for a name that isn't the server's: the page
// then draws the 404 sentence instead of the head and the body.
type opsServerPage struct {
	pageData
	Name string
	// PartHref is the body's refresh route; empty for a host not read yet, whose name isn't known.
	PartHref string
	Head     srvHead
	Body     srvBody
	Missing  *srvMissing
}

// srvMissing is the 404 page: what was asked for and the one server there is.
type srvMissing struct {
	Asked string
	Real  string
	Href  string
}

// srvPart is what /parts/ops/servers/{name} answers: the body (swapped into its own element) and
// the head, which sits outside it and is swapped by id.
type srvPart struct {
	Head srvHead
	Body srvBody
}

// srvHead is the head: the name, the state in plain words, "reboot needed" when the host's files
// ask for it, and the spec line. It has no actions: a shell on the host and moving work off are
// not built (the contract says why).
type srvHead struct {
	Name   string
	State  *statusView // nil until the node is known
	Reboot bool
	// Spec is the spec line's items, each kept whole when the line wraps.
	Spec []srvItem
}

// srvItem is one item of a " · " list (the spec line, the agent, the os, the cpu). It is drawn in a
// span that doesn't break inside, so a narrow line wraps between items only, as page 1's stats line
// does. More is set on every item but the last: the separator goes with the item before it.
type srvItem struct {
	Text string
	More bool
}

// itemsOf makes the items of a list of texts.
func itemsOf(texts []string) []srvItem {
	out := make([]srvItem, len(texts))
	for i, t := range texts {
		out[i] = srvItem{Text: t, More: i < len(texts)-1}
	}
	return out
}

// srvBody is everything under the head, one live region: the notice, the banners, the numbers
// and the room line, and the two columns.
type srvBody struct {
	// NomadDown says Nomad couldn't be read: the notice is drawn, the numbers carry "as of", and
	// Running here says it can't read Nomad.
	NomadDown bool
	// Waiting is set before the sampler's first reading of the node: the body is this one sentence.
	Waiting string
	Banners []srvBanner
	Numbers srvNumbers
	Room    *monRoom
	Running srvRunning
	Facts   []srvFact
	Recent  []opsEvent
}

// srvBanner is a problem of the host as the contract draws it: the mark, the sentence, its
// explanation when it has one and its one action (none for a node that is down).
type srvBanner struct {
	Mark    string
	Line    sentence
	Explain sentence
	Action  string
	Href    string
}

// srvNumbers is the three numbers panel. Line is set instead of the cells when the host was never read.
type srvNumbers struct {
	Line  string
	Cells []srvCell
}

// srvCell is one cell: the label, the big value and its unit, what it is of, and the meter(s).
// Meters[0] reads the percentage; a second one (the other disk) reads its mount.
type srvCell struct {
	Label  string
	Value  string
	Unit   string
	Warn   bool
	Sub    monSentence
	Meters []monMeter
}

// srvRunning is the Running here panel. Message, when set, replaces the rows (Nomad can't be read).
// Count is the copies running, starting or stopping; Failed the failed ones listed as well.
type srvRunning struct {
	Count   int
	Failed  int
	Message string
	Rows    []srvCopy
}

// srvCopy is one row of Running here.
type srvCopy struct {
	// ID is the link's id, stable across a swap so that focus stays on it ("srv-copy-<copy id>").
	ID    string
	Href  string
	Label sentence
	Short string
	Title string
	Mark  string
	// State is "starting", "draining" (a copy being stopped) or "" for the chip's ground.
	State string
	Role  string
	CPU   srvMetric
	Mem   srvMetric
	Up    srvUp
}

// srvUp is the Up cell. Label is the fold's label, which follows the phase ("Up" for a copy that runs,
// "Starting", "Stopping", "Failed"), so the folded row never reads "Up: failed". Word is said before
// the text on a wide row only ("failed 3 min ago · exit 1"): the fold's label already says it. Crash is
// the needs-you item's signal for the copy's service, beside the phase and never in place of it.
type srvUp struct {
	Label string
	Word  string
	Text  string
	Crash *srvCrash
}

// srvCrash is the crash loop of a copy's service as the needs-you list says it: "restarted 4 times in
// 1 h" in the item's level (◆ warning for restarting, ■ failed for out of memory).
type srvCrash struct {
	Mark string
	Text string
}

// srvMetric is a LiveMetric: the value and its unit, and what it is of. Value is "—" when the
// copy has none (None is then set: it is drawn quiet, weight 400 in fg-3, never as a value). Warn
// puts the value in the warn hue (memory from 90 % of the limit).
type srvMetric struct {
	Value, Unit string
	Sub         string
	Warn        bool
	None        bool
	Title       string
}

// srvFact is one row of the facts panel. Value is the text; Clock replaces it for a time (up
// since), followed by After in the muted words. Note is a word after the value in fg-3 ("public").
// Second is the line under it, in fg-3, with its own SecondNote. Mark is drawn before the value (a
// host that is down).
type srvFact struct {
	Key   string
	Value string
	// Items replaces Value for a " · " list (agent, os, cpu): each item is kept whole.
	Items      []srvItem
	Note       string
	Clock      *clockTime
	After      string
	Second     string
	SecondNote string
	Mark       string
	Strong     bool
}

// ---- handlers ---------------------------------------------------------------------------------

// showOpsServers answers GET /ops/servers: one host, so it goes to the host's page. Before the
// sampler's first reading the node's name isn't known: the page says so instead.
func (s *server) showOpsServers(w http.ResponseWriter, r *http.Request, session string) {
	host, err := s.svc.Host(r.Context())
	if err != nil {
		s.serverError(w, "read the host", err)
		return
	}
	if host.Node == nil {
		s.drawOpsServer(w, r, "")
		return
	}
	http.Redirect(w, r, "/ops/servers/"+url.PathEscape(host.Node.Name), http.StatusFound)
}

// showOpsServer answers GET /ops/servers/{name}.
func (s *server) showOpsServer(w http.ResponseWriter, r *http.Request, session string) {
	s.drawOpsServer(w, r, r.PathValue("name"))
}

// drawOpsServer draws the page for name ("" for the page of a host not read yet).
func (s *server) drawOpsServer(w http.ResponseWriter, r *http.Request, name string) {
	data, err := s.loadOpsServer(r, name)
	if err != nil {
		s.serverError(w, "draw the server page", err)
		return
	}
	title := cmp.Or(name, "Server")
	crumbs := []crumb{{Label: "Overview", Href: "/ops"}, {Label: title}}
	page, err := s.shellPage(r, title, navOpsServer, crumbs...)
	if err != nil {
		s.serverError(w, "read admin", err)
		return
	}
	page.LiveURL = "/live?watch=ops"
	out := opsServerPage{pageData: page, Name: name}
	if name != "" {
		out.PartHref = "/parts/ops/servers/" + url.PathEscape(name)
	}
	status := http.StatusOK
	if data.missing != nil {
		out.Missing, status = data.missing, http.StatusNotFound
		out.Title, out.LiveURL = "Server not found", ""
		out.Shell.Crumbs = []crumb{{Label: "Overview", Href: "/ops"}, {Label: "Server not found"}}
	} else {
		out.Head, out.Body = data.head, data.body
	}
	s.render(w, status, "opsserver", out)
}

// partOpsServerBody answers GET /parts/ops/servers/{name}: the body, and the head by id.
func (s *server) partOpsServerBody(w http.ResponseWriter, r *http.Request, session string) {
	data, err := s.loadOpsServer(r, r.PathValue("name"))
	if err != nil {
		s.log.Error("draw the server page", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data.missing != nil {
		http.NotFound(w, r)
		return
	}
	s.renderPart(w, "opsserver", "srv-part", srvPart{Head: data.head, Body: data.body})
}

// ---- loaders ----------------------------------------------------------------------------------

// srvData is what one read gives the page and its part.
type srvData struct {
	missing *srvMissing
	head    srvHead
	body    srvBody
}

// loadOpsServer reads the host, its copies, the needs-you items about the host and the server's
// own history, and builds the head and the body. A name that isn't the node's is a missing page;
// before the node is known any name is the page of a host not read yet.
func (s *server) loadOpsServer(r *http.Request, name string) (srvData, error) {
	ctx := r.Context()
	host, err := s.svc.Host(ctx)
	if err != nil {
		return srvData{}, err
	}
	now := s.now().UTC()
	d := srvData{head: srvHead{Name: name}}
	d.body.NomadDown = nomadUnread(host)
	n := host.Node
	if n == nil {
		d.body.Waiting = "Waiting for the first reading of the server. It appears a minute after Caravana starts."
		return d, nil
	}
	if name != n.Name {
		d.missing = &srvMissing{Asked: name, Real: n.Name, Href: "/ops/servers/" + url.PathEscape(n.Name)}
		return d, nil
	}

	copies, copiesErr := s.svc.HostCopies(ctx)
	if copiesErr != nil && !errors.Is(copiesErr, deploy.ErrNomadUnavailable) {
		s.log.Warn("can't read the copies for the server page", "error", copiesErr)
	}
	d.body.NomadDown = d.body.NomadDown || copiesErr != nil

	sums, err := s.svc.ListApps(ctx)
	if err != nil {
		return srvData{}, err
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return srvData{}, err
	}
	att, err := s.svc.Attention(ctx)
	if err != nil {
		return srvData{}, err
	}
	items := s.hostItems(now, att)
	recent, err := s.svc.Activity(ctx, service.ActivityQuery{Limit: srvRecentLimit, Server: true})
	if err != nil {
		return srvData{}, err
	}

	d.head = srvHead{Name: n.Name, State: serverState(n.State), Reboot: host.Reboot != nil && host.Reboot.Required, Spec: itemsOf(nodeSpecItems(n))}
	coreMHz := coreMHzOf(host.CPU)
	if coreMHz == 0 && n.Cores > 0 && n.TotalMHz > 0 {
		coreMHz = float64(n.TotalMHz) / float64(n.Cores)
	}
	d.body.Banners = append(s.nodeBanners(now, n), items...)
	d.body.Numbers = serverNumbers(now, host, d.body.NomadDown, coreMHz)
	d.body.Room = roomOf(host)
	d.body.Running = s.serverCopies(pl, sums, copies, copiesErr, d.body.NomadDown, coreMHz, s.crashesOf(now, att))
	gap := s.svc.MetricsSampleGap()
	d.body.Facts = s.serverFacts(r, now, n, readAt(host, gap), gap)
	for _, e := range recent {
		ev := opsEventOf(e)
		ev.Time = shortTime(now, e.Time)
		d.body.Recent = append(d.body.Recent, ev)
	}
	return d, nil
}

// hostItems are the banners of the host's open needs-you items: a disk nearly full, and the OS's
// security updates and reboot (page 3's kinds). The row is the needs-you list's own, so the page
// and the list say the same words and offer the same action.
func (s *server) hostItems(now time.Time, att service.Attention) []srvBanner {
	var out []srvBanner
	for _, it := range att.Items {
		if !isHostItem(it.Kind) {
			continue
		}
		row := s.attentionRow(now, it, places{}, nil)
		b := srvBanner{Mark: row.Mark, Line: row.Text, Action: row.Action, Href: row.Href}
		if it.Kind == service.AttentionDiskNearlyFull {
			b.Explain = diskExplain(it.Holds)
		}
		out = append(out, b)
	}
	return out
}

// isHostItem says a needs-you kind is about the host itself. The OS kinds are page 3's; naming
// them here lets their banners appear as soon as the list has them.
func isHostItem(kind service.AttentionKind) bool {
	switch kind {
	case service.AttentionDiskNearlyFull, "os_reboot_required", "os_security_updates":
		return true
	}
	return false
}

// diskExplain says what is on a disk that is nearly full, but only when the item says it holds both
// (one filesystem for Nomad's data and Docker's images): "It holds Nomad's data and Docker's images."
// For one that holds only one of them the clause would be a guess, so there is none: the banner's
// own sentence names its mount ("The disk at /var/lib/docker ...").
func diskExplain(holds []string) sentence {
	if slices.Contains(holds, "nomad") && slices.Contains(holds, "docker") {
		return plainS("It holds Nomad's data and Docker's images.")
	}
	return nil
}

// nodeBanners is the banner of a node Nomad has lost: it can do nothing about it, so no action.
func (s *server) nodeBanners(now time.Time, n *service.HostNode) []srvBanner {
	if n.State != "down" {
		return nil
	}
	line := "Nomad lost this server's heartbeat"
	if lost := lostAt(n); !lost.IsZero() {
		line += " " + agoFull(now, lost)
	}
	return []srvBanner{{Mark: "failed", Line: plainS(line + ". Apps on it may be down.")}}
}

// lostAt is when Nomad marked the node down: its own status time. A node record without one falls back
// to the last time Caravana heard from it.
func lostAt(n *service.HostNode) time.Time {
	if !n.StatusAt.IsZero() {
		return n.StatusAt
	}
	return n.LastHeartbeat
}

// serverState is the node's state in the console's words: ready, draining, not taking new work
// (ineligible), down (Nomad lost its heartbeat) and starting (initializing).
func serverState(state string) *statusView {
	switch state {
	case "ready":
		return &statusView{Kind: "healthy", Word: "ready"}
	case "draining":
		return &statusView{Kind: "rolling", Word: "draining"}
	case "ineligible":
		return &statusView{Kind: "warning", Word: "not taking new work"}
	case "down":
		return &statusView{Kind: "failed", Word: "down"}
	case "initializing":
		return &statusView{Kind: "rolling", Word: "starting"}
	}
	return &statusView{Kind: "idle", Word: state}
}

// ---- the numbers ------------------------------------------------------------------------------

// serverNumbers builds the three cells from the newest sample, the one 025's meters show (one
// window, never a mean of the last minutes). When Nomad can't be read, or the sample is stale,
// each sub says "as of 14:02 UTC": the numbers are the last ones stored. CPU is warn from 90 %,
// the meter's own rule; memory and disk from 85 %, failed from 95 % (025).
func serverNumbers(now time.Time, h service.HostView, down bool, coreMHz float64) srvNumbers {
	if h.SampledAt == nil {
		return srvNumbers{Line: "The host hasn't been read yet. Its numbers appear a minute after Caravana starts."}
	}
	var asOf monSentence
	if down || h.Stale {
		asOf = monSentence{mtext(" · as of "), mclock(clockOf(now, *h.SampledAt))}
	}
	cells := []srvCell{cpuCell(h, coreMHz, asOf), memoryCell(h, asOf), diskCell(h, asOf)}
	return srvNumbers{Cells: cells}
}

// sizeInUnitOf writes mb in the unit sizeText gives total, so that "of 1.9 GB" is followed by
// "0.7 GB reserved" and never "736 MB reserved".
func sizeInUnitOf(mb, total int64) string {
	if total >= 1024 {
		return trim1(float64(mb)/1024) + " GB"
	}
	return sizeText(mb)
}

// splitUnit cuts "1.7 GB" into "1.7" and "GB".
func splitUnit(s string) (value, unit string) {
	v, u, _ := strings.Cut(s, " ")
	return v, u
}

// percentOf is used over total in whole percent.
func percentOf(used, total float64) string {
	return strconv.Itoa(int(used/total*100+0.5)) + "%"
}

// readPercent gives a meter the percentage readout of the numbers panel and no label.
func readPercent(m *monMeter, used, total float64) monMeter {
	out := *m
	out.Label, out.Read, out.Split = "", percentOf(used, total), false
	return out
}

func cpuCell(h service.HostView, coreMHz float64, asOf monSentence) srvCell {
	c := srvCell{Label: "CPU", Value: "—"}
	r := h.CPU
	m := cpuMeter(h)
	if m == nil {
		c.Sub = monSentence{mtext("Not read.")}
		return c
	}
	used, total := float64(*r.Used), float64(*r.Total)
	amount, unit := func(mhz float64) string { return strconv.Itoa(int(mhz + 0.5)) }, "MHz"
	if coreMHz > 0 {
		amount, unit = func(mhz float64) string { return vcpuText(mhz / coreMHz) }, "vCPU"
	}
	c.Value, c.Unit, c.Warn = amount(used), unit, m.Warn
	totalText := amount(total)
	if coreMHz > 0 {
		totalText = vcpuTotalText(total / coreMHz)
	}
	sub := "of " + totalText + " " + unit
	if r.Reserved != nil {
		sub += " · " + amount(float64(*r.Reserved)) + " reserved"
	}
	c.Sub = append(monSentence{mtext(sub)}, asOf...)
	c.Meters = []monMeter{readPercent(m, used, total)}
	return c
}

func memoryCell(h service.HostView, asOf monSentence) srvCell {
	c := srvCell{Label: "Memory", Value: "—"}
	m := memoryMeter(h)
	if m == nil {
		c.Sub = monSentence{mtext("Not read.")}
		return c
	}
	r := h.Memory
	c.Value, c.Unit = splitUnit(sizeText(*r.Used))
	c.Warn = m.Warn
	sub := "of " + sizeText(*r.Total)
	if r.Reserved != nil {
		sub += " · " + sizeInUnitOf(*r.Reserved, *r.Total) + " reserved"
	}
	c.Sub = append(monSentence{mtext(sub)}, asOf...)
	c.Meters = []monMeter{readPercent(m, float64(*r.Used), float64(*r.Total))}
	return c
}

// diskCell shows the fuller of the watched filesystems big and, when there are two (Nomad's data
// and Docker's images on different disks), the other as a second meter that reads its mount.
func diskCell(h service.HostView, asOf monSentence) srvCell {
	c := srvCell{Label: "Disk", Value: "—"}
	if len(h.Disks) == 0 {
		c.Sub = monSentence{mtext("Not read.")}
		return c
	}
	meters := diskMeters(h)
	first := 0
	for i, d := range h.Disks {
		if d.UsedPercent > h.Disks[first].UsedPercent {
			first = i
		}
	}
	d := h.Disks[first]
	c.Value, c.Unit = splitUnit(sizeText(d.UsedMB))
	c.Warn = meters[first].Warn
	c.Sub = append(monSentence{mtext("of " + sizeText(d.TotalMB) + " on "), mname(d.Mount)}, asOf...)
	c.Meters = []monMeter{readPercent(&meters[first], float64(d.UsedMB), float64(d.TotalMB))}
	for i, m := range meters {
		if i != first {
			// "on /var/lib/docker 41/80G": the readout diskMeters writes when there is more than one
			// ("/var/lib/docker 41/80G"), with the "on" of the first disk's sub in front of the mount.
			m.Label, m.Split, m.Read = "", false, "on "+m.Read
			c.Meters = append(c.Meters, m)
		}
	}
	return c
}

// ---- Running here -----------------------------------------------------------------------------

// serverCopies builds the Running here panel from the service's copies, in its order (problems
// first, then by app and service). The app's name is followed by the service only when the app
// has several; the place leads outside Default › production, as on page 1.
func (s *server) serverCopies(pl places, sums []service.AppSummary, copies []service.HostCopy, copiesErr error, down bool, coreMHz float64, crashes map[string]srvCrashPair) srvRunning {
	if copiesErr != nil {
		return srvRunning{Message: "Can't read Nomad right now."}
	}
	services := map[string]int{}
	for _, sum := range sums {
		services[sum.App.ID] = len(sum.App.Services)
	}
	out := srvRunning{}
	var ranks []int
	for _, c := range copies {
		if c.Phase == service.CopyPhaseFailed {
			out.Failed++
		} else {
			out.Count++
		}
		suffix := ""
		if services[c.AppID] > 1 {
			suffix = "/" + c.Service
		}
		row := srvCopy{
			ID: "srv-copy-" + c.ID, Href: "/apps/" + url.PathEscape(c.AppID),
			Label: pl.label(c.Place, c.AppID, suffix), Short: shortID(c.ID), Mark: rowMark(c),
			CPU: copyCPU(c.Usage, coreMHz), Mem: copyMemory(c.Usage), Up: copyUp(s.now(), c),
		}
		row.Up.Crash = crashes[c.AppID+"/"+c.Service].of(c)
		switch c.Phase {
		case service.CopyPhaseStopping:
			row.State, row.Role = "draining", "stopping"
		case service.CopyPhaseStarting:
			row.State, row.Role = "starting", "starting"
		default:
			row.Role = copyStatusView(c.Status).Word
		}
		row.Title = "copy " + c.ID + ", " + c.Service + ", " + row.Role + ", v" + strconv.FormatUint(c.Version, 10)
		out.Rows = append(out.Rows, row)
		ranks = append(ranks, rowRank(c.Phase, row.Up.Crash != nil))
	}
	// Problems first, and a crash loop is one even between its restarts, so the row never moves with the
	// cycle: failed, crash signal, starting, stopping, then running. Within a rank the service's order
	// (by place, app and service) stays.
	order := make([]int, len(out.Rows))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return ranks[a] - ranks[b] })
	sorted := make([]srvCopy, len(order))
	for i, j := range order {
		sorted[i] = out.Rows[j]
	}
	out.Rows = sorted
	return out
}

// rowRank is a copy's place in Running here: failed 0, a crash signal 1, starting 2, stopping 3,
// running 4.
func rowRank(phase string, crashing bool) int {
	switch {
	case phase == service.CopyPhaseFailed:
		return 0
	case crashing:
		return 1
	case phase == service.CopyPhaseStarting:
		return 2
	case phase == service.CopyPhaseStopping:
		return 3
	}
	return 4
}

// rowMark is the mark of a copy's chip: a failed copy is failed even though Nomad has told it to
// stop (copyMark would draw that one idle).
func rowMark(c service.HostCopy) string {
	if c.Phase == service.CopyPhaseFailed {
		return "failed"
	}
	return copyMark(c.Copy)
}

// copyCPU is the CPU of one copy: "0.19 vCPU" of "0.05 reserved", no sparkline (Caravana keeps a
// history per service, not per copy). A copy with no measurement shows "—".
func copyCPU(u *service.CopyUsage, coreMHz float64) srvMetric {
	amount, unit := func(mhz int) string { return strconv.Itoa(mhz) }, "MHz"
	if coreMHz > 0 {
		amount, unit = func(mhz int) string { return vcpuText(float64(mhz) / coreMHz) }, "vCPU"
	}
	m := srvMetric{Value: "—", None: true}
	if u == nil {
		return m
	}
	if u.CPUReservedMHz > 0 {
		m.Sub = "of " + amount(u.CPUReservedMHz) + " reserved"
	}
	switch {
	case u.Error != "":
		m.Title = u.Error
	case !u.None:
		m.Value, m.Unit, m.None = amount(u.CPUMHz), unit, false
	}
	return m
}

// copyMemory is "412 MB" of "512 MB limit", warn from 90 % of the limit.
func copyMemory(u *service.CopyUsage) srvMetric {
	m := srvMetric{Value: "—", None: true}
	if u == nil {
		return m
	}
	if u.MemoryCeilingMB > 0 {
		m.Sub = "of " + sizeText(int64(u.MemoryCeilingMB)) + " limit"
	}
	switch {
	case u.Error != "":
		m.Title = u.Error
	case !u.None:
		m.Value, m.Unit = splitUnit(sizeText(int64(u.MemoryMB)))
		m.Warn, m.None = u.NearCeiling, false
	}
	return m
}

// copyUp is the Up cell: how long a running copy has run ("3 d 4 h", "5 h 12 min", "12 min"), how long a
// starting one has been starting ("for 40 s"), how long a stopping one ran ("up 3 d 4 h"), and when a
// failed one failed and why ("failed 3 min ago · exit 1"). The words are short ("3 min"): this is a
// dense column. A copy that never started has "—".
func copyUp(now time.Time, c service.HostCopy) srvUp {
	switch c.Phase {
	case service.CopyPhaseFailed:
		up := srvUp{Label: "Failed", Word: "failed"}
		var parts []string
		if c.FinishedAt != nil {
			parts = append(parts, agoWords(now, *c.FinishedAt))
		}
		if why := failedWhy(c.Copy); why != "" {
			parts = append(parts, why)
		}
		up.Text = strings.Join(parts, " · ")
		return up
	case service.CopyPhaseStarting:
		up := srvUp{Label: "Starting", Text: "—"}
		if c.StartedAt != nil {
			up.Text = "for " + startingText(now.Sub(*c.StartedAt))
		}
		return up
	case service.CopyPhaseStopping:
		up := srvUp{Label: "Stopping", Text: "—"}
		if c.StartedAt != nil {
			up.Text = "up " + upText(now.Sub(*c.StartedAt))
		}
		return up
	}
	up := srvUp{Label: "Up", Text: "—"}
	if c.StartedAt != nil {
		up.Text = upText(now.Sub(*c.StartedAt))
	}
	return up
}

// failedWhy is why a copy failed, from the last event of its service's task, in the words the schedules'
// runs use: "out of memory", "exit 1", "couldn't start". Empty when Nomad says nothing (a lost node).
func failedWhy(c service.Copy) string {
	switch {
	case c.OutOfMemory:
		return "out of memory"
	case c.ExitCode != nil:
		return "exit " + strconv.Itoa(*c.ExitCode)
	case c.StartFailed:
		return "couldn't start"
	}
	return ""
}

// startingText is how long a copy has been starting, in seconds under a minute ("40 s"), else upText.
func startingText(d time.Duration) string {
	if d < time.Minute {
		return strconv.Itoa(max(int(d/time.Second), 0)) + " s"
	}
	return upText(d)
}

// srvCrashPair is the crash signals of one service: its restarting item and its out-of-memory item, each
// nil when it has none open.
type srvCrashPair struct {
	Restarting, OOM *srvCrash
}

// of is the signal for a copy: the out-of-memory item's when the copy was killed for memory, else the
// restarting item's, and the other when the copy has no reason of its own. Nil for none.
func (p srvCrashPair) of(c service.HostCopy) *srvCrash {
	switch {
	case c.OutOfMemory && p.OOM != nil:
		return p.OOM
	case p.Restarting != nil:
		return p.Restarting
	}
	return p.OOM
}

// crashesOf reads the crash loops of the needs-you list by "app/service". The count is the item's own
// (service.AttentionItem.Crashes, 025's crashesInHour), so the page never disagrees with the list or
// the notification; the level is the one the list draws the item in.
func (s *server) crashesOf(now time.Time, att service.Attention) map[string]srvCrashPair {
	out := map[string]srvCrashPair{}
	for _, it := range att.Items {
		var text string
		switch it.Kind {
		case service.AttentionRestarting:
			text = "restarted " + plural(it.Crashes, "time") + " in 1 h"
		case service.AttentionOutOfMemory:
			text = "out of memory " + plural(it.Crashes, "time") + " in 1 h"
		default:
			continue
		}
		// The item's own row says its level (the list's mark), so it is drawn the same here.
		sig := &srvCrash{Mark: s.attentionRow(now, it, places{}, nil).Mark, Text: text}
		key := it.AppID + "/" + it.Service
		pair := out[key]
		if it.Kind == service.AttentionRestarting {
			pair.Restarting = sig
		} else {
			pair.OOM = sig
		}
		out[key] = pair
	}
	return out
}

// upText writes a span of running time: days and hours from a day, hours and minutes from an
// hour, else minutes; under a minute it says so.
func upText(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under 1 min"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min"
	case d < 24*time.Hour:
		h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
		if m == 0 {
			return strconv.Itoa(h) + " h"
		}
		return strconv.Itoa(h) + " h " + strconv.Itoa(m) + " min"
	}
	days, h := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour)
	if h == 0 {
		return strconv.Itoa(days) + " d"
	}
	return strconv.Itoa(days) + " d " + strconv.Itoa(h) + " h"
}

// agoWords is how long ago t was, short: "just now", "2 s ago", "3 min ago", "5 h ago", "8 d ago".
func agoWords(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + " s ago"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " h ago"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + " d ago"
}

// agoFull is how long ago t was in full words, as the needs-you sentences say it: "3 minutes ago",
// "1 minute ago", "under a minute ago". Sentences use it; the dense columns use agoWords.
func agoFull(now, t time.Time) string {
	if now.Sub(t) < time.Minute {
		return "under a minute ago"
	}
	text := relative(now, t)
	if !strings.HasSuffix(text, " ago") { // relative gives a date past two months
		return "over 60 days ago"
	}
	return text
}

// shortTime is the time column of Recent: "14:02" for today (UTC), else the date as shortDate says it.
// The whole time, UTC, is in the title, with the local one added by the page's script.
func shortTime(now, t time.Time) string {
	t = t.UTC()
	if t.Format("2006-01-02") == now.UTC().Format("2006-01-02") {
		return t.Format("15:04")
	}
	return shortDate(now, t)
}

// shortDate is the console's one date format in dense places (UTC): "Oct 22", and "Oct 22, 2025" when
// the date is from another year than now.
func shortDate(now, t time.Time) string {
	t = t.UTC()
	if t.Year() != now.UTC().Year() {
		return t.Format("Jan 2, 2006")
	}
	return t.Format("Jan 2")
}

// ---- the facts --------------------------------------------------------------------------------

// serverFacts are the facts panel's rows: only what is known. The public address is the console's
// own host (Nomad doesn't know it), the private one is the node's caravana0 interface, left out
// when the node has none. Each address says which it is.
func (s *server) serverFacts(r *http.Request, now time.Time, n *service.HostNode, read time.Time, gap time.Duration) []srvFact {
	var out []srvFact
	if n.Pool != "" {
		out = append(out, srvFact{Key: "pool", Value: n.Pool})
	}
	if public := s.consoleHostOf(r); public != "" || n.Address != "" {
		f := srvFact{Key: "address", Value: public, Note: "public", Second: n.Address, SecondNote: "private"}
		if public == "" {
			f.Value, f.Note, f.Second, f.SecondNote = n.Address, "private", "", ""
		}
		out = append(out, f)
	}
	if !n.BootTime.IsZero() {
		boot := n.BootTime.UTC()
		// Non-breaking spaces: the time is one word to the line breaker, which may only break after it.
		c := clockAt(boot, strings.ReplaceAll(boot.Format("2006-01-02 15:04")+" UTC", " ", "\u00a0"), localDate)
		// The relative words are one word to the line breaker (non-breaking spaces), so "(8 d ago)" is never split.
		out = append(out, srvFact{Key: "up since", Clock: &c, After: strings.ReplaceAll(agoWords(now, boot), " ", "\u00a0")})
	}
	if agent := agentItems(n, s.version); len(agent) > 0 {
		out = append(out, srvFact{Key: "agent", Items: itemsOf(agent)})
	}
	if os := osItems(n); len(os) > 0 {
		out = append(out, srvFact{Key: "os", Items: itemsOf(os)})
	}
	if cpu := cpuItems(n); len(cpu) > 0 {
		out = append(out, srvFact{Key: "cpu", Items: itemsOf(cpu)})
	}
	if f, ok := lastSeenFact(now, n, read, gap); ok {
		out = append(out, f)
	}
	return out
}

// readAt is when the newest sample was taken: the end of its window (a sample's time is the start of
// the gap it covers). Zero when the host was never sampled.
func readAt(h service.HostView, gap time.Duration) time.Time {
	if h.SampledAt == nil {
		return time.Time{}
	}
	return h.SampledAt.Add(gap)
}

// lastSeenFact is the "last seen" row, only when it has something to say:
//   - a node Nomad lost: "lost 3 minutes ago" in fg-1 with the failed mark, at the time Nomad marked it
//     down (its own status time, not the sampler's);
//   - a ready node whose newest reading is older than two sampler ticks: "last reading 5 minutes ago";
//   - a ready node read lately: no row, since "2 s ago" on every page load says nothing.
func lastSeenFact(now time.Time, n *service.HostNode, read time.Time, gap time.Duration) (srvFact, bool) {
	if n.State == "down" {
		lost := lostAt(n)
		if lost.IsZero() {
			return srvFact{}, false
		}
		return srvFact{Key: "last seen", Value: "lost " + agoFull(now, lost), Mark: "failed", Strong: true}, true
	}
	if read.IsZero() || now.Sub(read) <= 2*gap {
		return srvFact{}, false
	}
	return srvFact{Key: "last reading", Value: agoFull(now, read)}, true
}

// consoleHostOf is the console's own host name, without a port: the one the owner set as the public
// address, else the host the console is served for, else the one this request came to.
func (s *server) consoleHostOf(r *http.Request) string {
	if u, err := url.Parse(s.svc.PublicURL()); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	host := s.host
	if host == "" {
		host = r.Host
	}
	if u, err := url.Parse("//" + host); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return host
}

// agentLine is "nomad 1.9.4 · docker 27.3 · traefik 3.7.1 · caravana 0.13.0": the programs
// Nomad and the Traefik job report, then the one running. Only the versions known are named.
func agentLine(n *service.HostNode, caravana string) string {
	return strings.Join(agentItems(n, caravana), " · ")
}

func agentItems(n *service.HostNode, caravana string) []string {
	var parts []string
	for _, p := range []struct{ name, version string }{
		{"nomad", n.NomadVersion}, {"docker", n.DockerVersion},
		{"traefik", strings.TrimPrefix(n.TraefikVersion, "v")}, {"caravana", caravana},
	} {
		if p.version != "" {
			parts = append(parts, p.name+" "+p.version)
		}
	}
	return parts
}

// osLine is "Ubuntu 24.04 · kernel 6.8.0-147-generic".
func osLine(n *service.HostNode) string { return strings.Join(osItems(n), " · ") }

func osItems(n *service.HostNode) []string {
	var parts []string
	if n.OS != "" {
		r, size := utf8.DecodeRuneInString(n.OS)
		parts = append(parts, strings.TrimSpace(string(unicode.ToUpper(r))+n.OS[size:]+" "+n.OSVersion))
	}
	if n.Kernel != "" {
		parts = append(parts, "kernel "+n.Kernel)
	}
	return parts
}

// cpuLine is "AMD EPYC-Rome · 2 cores · 2,445 MHz" or, where Nomad gives no model or per-core
// speed (a virtual machine on arm), "2 cores · 4,890 MHz in total".
func cpuLine(n *service.HostNode) string { return strings.Join(cpuItems(n), " · ") }

func cpuItems(n *service.HostNode) []string {
	var parts []string
	if n.CPUModel != "" {
		parts = append(parts, n.CPUModel)
	}
	if n.Cores > 0 {
		parts = append(parts, plural(n.Cores, "core"))
	}
	switch {
	case n.CoreMHz > 0:
		parts = append(parts, thousands(n.CoreMHz)+" MHz")
	case n.TotalMHz > 0:
		parts = append(parts, thousands(n.TotalMHz)+" MHz in total")
	}
	return parts
}

// thousands writes n with a comma between each group of three digits: 4890 is "4,890".
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
