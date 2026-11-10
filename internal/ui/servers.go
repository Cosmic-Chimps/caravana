package ui

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The Servers list (feature 031): /ops/servers, every server of the cluster, the waiting and the
// joining ones too, with its numbers and the button that adds one. It reads only internal/service.
// The meters are the server page's own meter template at the list's size; the words are
// contracts/page-servers.md's, letter for letter.

// ---- what the page draws ----------------------------------------------------------------------

// serversPage is the whole page: the region is #srv-list, which /parts/ops/servers draws alone.
type serversPage struct {
	pageData
	List serversRegion
}

// serversRegion is the head, the cluster sentence, the notice and the list.
type serversRegion struct {
	// Line is "3 servers · 6 GB · 6 vCPU", or "3 in default, 1 in build · …".
	Line string
	// Cluster is the one line about the cluster's resilience; nil draws none.
	Cluster *clusterLine
	// CanAdd is false when no server can be added yet; Reason says why, and the Add button stays a link.
	CanAdd bool
	Reason sentence
	// NomadDown says Nomad couldn't be read, so no server is judged.
	NomadDown bool
	Rows      []serverRow
}

// clusterLine is the cluster sentence with its 7px mark.
type clusterLine struct {
	Mark string
	Text string
}

// serverRow is one server of the list.
type serverRow struct {
	// ID is the row's element id, stable across a swap so that keyboard focus stays on the link.
	ID   string
	Name string
	// NoName says the name isn't known yet: a command that leaves it to the machine.
	NoName bool
	Href   string
	Spec   string
	// Marks are the roles the server has, as small facts: console, ingress, scheduler.
	Marks []string
	// Unmanaged says Nomad has the node and Caravana doesn't manage it.
	Unmanaged bool
	State     statusView
	Meters    []slMeter
	// Cell replaces the three meters: what a waiting or joining server is doing.
	Cell string
	// Cancel is the form's action for a waiting join; empty for any other row.
	Cancel string
	Copies string
	Down   bool
}

// slMeter is one meter of a row: the server page's meter without its fill when the server is down.
// None is a meter with no numbers yet: a dash.
type slMeter struct {
	Label       string
	Fill        string
	ResW        string
	HasReserved bool
	Read        string
	Title       string
	Warn        bool
	Grey        bool
	None        bool
}

// ---- handlers ---------------------------------------------------------------------------------

// showOpsServers answers GET /ops/servers.
func (s *server) showOpsServers(w http.ResponseWriter, r *http.Request, session string) {
	region, err := s.loadServers(r)
	if err != nil {
		s.serverError(w, r, "draw the servers", err)
		return
	}
	head, err := s.shellPage(r, "Servers", navOpsServer, crumb{Label: "Servers"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	head.LiveURL = "/live?watch=ops"
	s.render(w, http.StatusOK, "servers", serversPage{pageData: head, List: region})
}

// partOpsServersList answers GET /parts/ops/servers: the region alone.
func (s *server) partOpsServersList(w http.ResponseWriter, r *http.Request, session string) {
	region, err := s.loadServers(r)
	if err != nil {
		s.log.Error("draw the servers", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "servers", "sl-region", region)
}

// ---- loader -----------------------------------------------------------------------------------

// loadServers reads the cluster and builds the region.
func (s *server) loadServers(r *http.Request) (serversRegion, error) {
	ctx := r.Context()
	view, err := s.svc.ListServers(ctx)
	if err != nil {
		return serversRegion{}, err
	}
	// The arch is known for the server Caravana runs on only: it is what its node reports.
	local, arch := "", ""
	if h, err := s.svc.Host(ctx, ""); err == nil {
		local = h.Server
		if h.Node != nil {
			arch = h.Node.Arch
		}
	}
	return serversRegionOf(s.now().UTC(), view, local, arch), nil
}

// serversRegionOf builds the region from the cluster's view. local is the server Caravana runs on and
// arch its architecture, "" when unknown.
func serversRegionOf(now time.Time, v service.ServersView, local, arch string) serversRegion {
	region := serversRegion{CanAdd: v.Cluster.CanAdd, NomadDown: v.Cluster.Nomad == "unreachable"}
	if !v.Cluster.CanAdd {
		region.Reason = plainS(v.Cluster.CannotAddReason)
	}
	joined := 0
	for _, sv := range v.Servers {
		if sv.State == "waiting" && !sv.ExpiresAt.After(now) {
			continue // an expired command isn't listed
		}
		if sv.State != "waiting" && sv.State != "joining" {
			joined++
		}
		region.Rows = append(region.Rows, serverRowOf(now, sv, sv.Name == local && local != "", arch))
	}
	region.Line = clusterHead(v, joined)
	region.Cluster = clusterSentence(v, joined)
	return region
}

// clusterHead is the line beside the title: how many servers, and what they add up to.
func clusterHead(v service.ServersView, joined int) string {
	counts := map[string]int{}
	named := false
	for _, sv := range v.Servers {
		if sv.State == "waiting" || sv.State == "joining" {
			continue
		}
		pool := cmp.Or(sv.Pool, "default")
		counts[pool]++
		named = named || pool != "default"
	}
	count := plural(joined, "server")
	if named {
		pools := make([]string, 0, len(counts))
		for p := range counts {
			pools = append(pools, p)
		}
		// default first, then by name, so the line never reorders between reads.
		slices.SortFunc(pools, func(a, b string) int {
			switch {
			case a == "default" && b != "default":
				return -1
			case b == "default" && a != "default":
				return 1
			}
			return strings.Compare(a, b)
		})
		parts := make([]string, len(pools))
		for i, p := range pools {
			parts[i] = strconv.Itoa(counts[p]) + " in " + p
		}
		count = strings.Join(parts, ", ")
	}
	// The sums are of the ready servers' own numbers, the ones the rows' spec lines say, so the head and the rows
	// agree: a server with no numbers yet, a node Caravana doesn't manage and has no sample of, and one that is
	// down add nothing.
	var memoryMB, cores int64
	for _, sv := range v.Servers {
		if sv.State != "ready" {
			continue
		}
		if sv.Memory != nil {
			memoryMB += sv.Memory.Total
		}
		if sv.CPU != nil {
			cores += sv.CPU.Cores
		}
	}
	out := []string{count}
	if memoryMB > 0 {
		out = append(out, gbText(float64(memoryMB))+" GB")
	}
	if cores > 0 {
		out = append(out, strconv.FormatInt(cores, 10)+" vCPU")
	}
	return strings.Join(out, " · ")
}

// clusterSentence is the cluster's one line (the contract's table), nil for one server and for a
// cluster with no ready server.
func clusterSentence(v service.ServersView, joined int) *clusterLine {
	c := v.Cluster
	if c.NomadServers >= 3 {
		for _, sv := range v.Servers {
			if sv.NomadServer && sv.State == "down" {
				return &clusterLine{Mark: "warning", Text: "One of the three servers that run the scheduler is down. The cluster works, and can't lose another."}
			}
		}
	}
	switch {
	case c.SurvivesLoss:
		return &clusterLine{Mark: "healthy", Text: "The cluster survives the loss of one server."}
	case c.Ready >= 3:
		// Promotion is under way: the schedulers still to start, until three run.
		more := "Two more servers are"
		if c.NomadServers >= 2 {
			more = "One more server is"
		}
		return &clusterLine{Mark: "rolling", Text: more + " starting to run the scheduler. Once three do, the cluster survives the loss of one."}
	case c.Ready == 2 && joined >= 2:
		return &clusterLine{Mark: "idle", Text: "With one more server the cluster survives the loss of one."}
	}
	return nil
}

// serverRowOf builds one row. isLocal says it is the server Caravana runs on, which has the arch.
func serverRowOf(now time.Time, sv service.ServerView, isLocal bool, arch string) serverRow {
	row := serverRow{ID: "srv-row-" + sv.ID, Name: sv.Name, NoName: sv.Name == "", Unmanaged: !sv.Managed,
		Href: serverHrefOf(sv), State: serverRowState(sv), Down: sv.State == "down"}
	if row.NoName {
		row.Name = "the machine's own name"
	}
	if !isLocal {
		arch = ""
	}
	row.Spec = strings.Join(serverSpecItems(sv, arch), " · ")
	if sv.ControlPlane {
		row.Marks = append(row.Marks, "console")
	}
	if sv.Ingress {
		row.Marks = append(row.Marks, "ingress")
	}
	if sv.NomadServer {
		row.Marks = append(row.Marks, "scheduler")
	}
	switch sv.State {
	case "waiting":
		row.Cell = "Waiting for the server to call in · the command works for " + minutesLeft(now, sv.ExpiresAt)
		row.Cancel = "/ops/servers/joins/" + url.PathEscape(sv.ID) + "/cancel"
		return row
	case "joining":
		row.Cell = firstOpenCheck(sv.Checks)
		return row
	}
	row.Meters = serverMeters(sv, row.Down)
	if row.Down {
		row.Copies = copyCount(0)
	} else {
		row.Copies = copyCount(sv.Copies)
	}
	return row
}

// serverHrefOf is where a row leads: a server's page, or the join's page while it hasn't finished.
func serverHrefOf(sv service.ServerView) string {
	if sv.State == "waiting" || sv.State == "joining" {
		return "/ops/servers/joins/" + url.PathEscape(sv.ID)
	}
	return "/ops/servers/" + url.PathEscape(sv.Name)
}

// serverRowState is the state pill of a row: down, disk filling, not taking work, ready, joining or
// waiting.
func serverRowState(sv service.ServerView) statusView {
	switch {
	case sv.State == "waiting":
		return statusView{Kind: "idle", Word: "waiting"}
	case sv.State == "joining":
		return statusView{Kind: "rolling", Word: "joining"}
	case sv.State == "down":
		return statusView{Kind: "failed", Word: "down"}
	}
	for _, d := range sv.Disks {
		if d.State == metrics.DiskWarning || d.State == metrics.DiskFailed {
			return statusView{Kind: "warning", Word: "disk filling"}
		}
	}
	if !sv.AcceptsWork {
		return statusView{Kind: "idle", Word: "not taking work"}
	}
	return statusView{Kind: "healthy", Word: "ready"}
}

// serverSpecItems is "2 vCPU", "2 GB", then the architecture when it is known. Only what was read.
func serverSpecItems(sv service.ServerView, arch string) []string {
	var out []string
	if sv.CPU != nil && sv.CPU.Cores > 0 {
		out = append(out, strconv.FormatInt(sv.CPU.Cores, 10)+" vCPU")
	}
	if sv.Memory != nil && sv.Memory.Total > 0 {
		out = append(out, gbText(float64(sv.Memory.Total))+" GB")
	}
	if arch != "" {
		out = append(out, arch)
	}
	return out
}

// firstOpenCheck is the sentence of the first check that isn't done yet.
func firstOpenCheck(checks []service.ServerCheck) string {
	for _, c := range checks {
		if !c.OK {
			return c.Detail
		}
	}
	return "Almost ready"
}

// minutesLeft is "43 more minutes", "1 more minute" or "under a minute".
func minutesLeft(now, end time.Time) string {
	m := int(end.Sub(now) / time.Minute)
	switch {
	case m < 1:
		return "under a minute more"
	case m == 1:
		return "1 more minute"
	}
	return strconv.Itoa(m) + " more minutes"
}

// ---- the meters -------------------------------------------------------------------------------

// serverMeters are cpu, mem and disk of a server from its newest sample. A number that wasn't read is
// a dash. A down server keeps its last numbers, greyed and with no fill.
func serverMeters(sv service.ServerView, grey bool) []slMeter {
	return []slMeter{serverCPUMeter(sv, grey), serverMemoryMeter(sv, grey), serverDiskMeter(sv, grey)}
}

func serverCPUMeter(sv service.ServerView, grey bool) slMeter {
	m := slMeter{Label: "cpu", None: true}
	c := sv.CPU
	if c == nil || c.Total <= 0 {
		return m
	}
	used, total := float64(c.Used), float64(c.Total)
	amount, unit := func(mhz float64) string { return strconv.Itoa(int(mhz + 0.5)) }, " MHz"
	totalText := amount(total)
	if c.Cores > 0 {
		core := total / float64(c.Cores)
		amount, unit = func(mhz float64) string { return vcpuText(mhz / core) }, " vCPU"
		totalText = vcpuTotalText(total / core)
	}
	m = slMeter{Label: "cpu", Grey: grey, Fill: units(meterFill(used, total)),
		Read: amount(used) + " / " + totalText + unit, Warn: !grey && used/total*100 > metrics.MemoryPercent}
	m.Title = amount(used) + unit + " in use"
	if c.Reserved > 0 {
		m.HasReserved, m.ResW = true, units(meterFill(float64(c.Reserved), total))
		m.Title += ", " + amount(float64(c.Reserved)) + unit + " reserved"
	}
	m.Title += " of " + totalText + unit
	return m
}

func serverMemoryMeter(sv service.ServerView, grey bool) slMeter {
	m := slMeter{Label: "mem", None: true}
	r := sv.Memory
	if r == nil || r.Total <= 0 {
		return m
	}
	used, total := float64(r.Used), float64(r.Total)
	m = slMeter{Label: "mem", Grey: grey, Fill: units(meterFill(used, total)),
		Read: gbText(used) + " / " + gbText(total) + " GB", Warn: !grey && used/total*100 >= meterWarnPercent}
	m.Title = sizeText(r.Used) + " in use"
	if r.Reserved > 0 {
		m.HasReserved, m.ResW = true, units(meterFill(float64(r.Reserved), total))
		m.Title += ", " + sizeText(r.Reserved) + " reserved"
	}
	m.Title += " of " + sizeText(r.Total)
	return m
}

// serverDiskMeter is the fuller of the watched filesystems.
func serverDiskMeter(sv service.ServerView, grey bool) slMeter {
	m := slMeter{Label: "disk", None: true}
	if len(sv.Disks) == 0 {
		return m
	}
	d := sv.Disks[0]
	for _, other := range sv.Disks[1:] {
		if other.UsedPercent > d.UsedPercent {
			d = other
		}
	}
	m = slMeter{Label: "disk", Grey: grey, Fill: units(meterFill(d.UsedPercent, 100)),
		Read: trim1(float64(d.UsedMB)/1024) + " / " + trim1(float64(d.TotalMB)/1024) + " GB",
		Warn: !grey && (d.State == metrics.DiskWarning || d.State == metrics.DiskFailed)}
	m.Title = d.Mount + ": " + strconv.Itoa(int(d.UsedPercent)) + "% used, " + sizeText(d.FreeMB) + " left"
	return m
}

// multiServer says the cluster has more than one server that has joined, so a page that says where
// something is (a copy, a volume, the numbers) names the server. A cluster that can't be read counts as
// one: the pages then look as they did.
func (s *server) multiServer(ctx context.Context) bool {
	v, err := s.svc.ListServers(ctx)
	if err != nil {
		return false
	}
	return joinedServers(v) > 1
}

// joinedServers counts the servers that are part of the cluster: not a command waiting to be used and not a
// server still going through its checks.
func joinedServers(v service.ServersView) int {
	n := 0
	for _, sv := range v.Servers {
		if sv.State != "waiting" && sv.State != "joining" {
			n++
		}
	}
	return n
}
