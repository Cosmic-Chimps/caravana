package ui

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Feature 055: network and disk I/O on the server page and on Monitoring.

func rowByLabel(rows []ioRow, label string) (ioRow, bool) {
	for _, r := range rows {
		if r.Label == label {
			return r, true
		}
	}
	return ioRow{}, false
}

func TestBytesRateText(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B/s", 1: "1 B/s", 1023: "1023 B/s", 1024: "1 KB/s", 102400: "100 KB/s", 524288: "512 KB/s",
		1 << 20: "1 MB/s", 1_572_864: "1.5 MB/s", 5 << 30: "5 GB/s"} {
		if got := bytesRateText(in); got != want {
			t.Errorf("bytesRateText(%d) = %q, want %q", in, got, want)
		}
	}
}

func knownIO() service.HostIO {
	at := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	return service.HostIO{
		At:       &at,
		Net:      []service.IONetRate{{Name: "eth0", Kind: "default", RxBps: 1 << 20, TxBps: 102400}, {Name: "caravana-mesh", Kind: "mesh", RxBps: 2048, TxBps: 1024}},
		Disks:    []service.IODiskRate{{Name: "vda1", Holds: []string{"docker", "nomad"}, ReadBps: 524288, WriteBps: 69905, BusyPercent: 50}},
		NetRxBps: i64(1 << 20), NetTxBps: i64(102400), DiskReadBps: i64(524288), DiskWriteBps: i64(69905), DiskBusyPercent: i64(50),
	}
}

func TestIOSectionKnown(t *testing.T) {
	now := time.Date(2026, 11, 24, 14, 0, 30, 0, time.UTC)
	s := ioSectionOf(now, knownIO())
	if len(s.Why) != 0 {
		t.Errorf("a known section says why: %v", s.Why)
	}
	want := map[string][2]string{
		"Network in":      {"1 MB/s", "eth0"},
		"Network out":     {"100 KB/s", "eth0"},
		"Between servers": {"in 2 KB/s · out 1 KB/s", "caravana-mesh · carried by the network above"},
		"Disk read":       {"512 KB/s", "vda1 · Nomad and Docker"},
		"Disk write":      {"68 KB/s", "vda1 · Nomad and Docker"},
		"Disk busy":       {"50 %", "vda1 · Nomad and Docker"},
	}
	if len(s.Rows) != len(want) {
		t.Fatalf("%d rows: %+v", len(s.Rows), s.Rows)
	}
	for label, w := range want {
		r, ok := rowByLabel(s.Rows, label)
		if !ok || r.Value != w[0] || r.Note.String() != w[1] || r.None {
			t.Errorf("%s = %+v, want %q with note %q", label, r, w[0], w[1])
		}
	}
	// The interface and device names are in mono.
	if r, _ := rowByLabel(s.Rows, "Network in"); len(r.Note) != 1 || !r.Note[0].Mono {
		t.Errorf("the interface isn't mono: %+v", r.Note)
	}
	if r, _ := rowByLabel(s.Rows, "Disk read"); !r.Note[0].Mono || r.Note[0].Text != "vda1" {
		t.Errorf("the device isn't mono: %+v", r.Note)
	}
}

func TestIOSectionWithTwoDevicesAndAResetInterface(t *testing.T) {
	now := time.Date(2026, 11, 24, 14, 0, 30, 0, time.UTC)
	io := knownIO()
	io.Disks = append(io.Disks, service.IODiskRate{Name: "sdb", Holds: []string{"docker"}, ReadBps: 10, WriteBps: 20, BusyPercent: 3})
	io.NetRxBps, io.NetTxBps = nil, nil // the default interface's counter reset
	s := ioSectionOf(now, io)
	if r, _ := rowByLabel(s.Rows, "Network in"); !r.None || r.Value != "—" || r.Note.String() == "" {
		t.Errorf("a reset interface = %+v: want a dash and a note", r)
	}
	if r, _ := rowByLabel(s.Rows, "Disk read"); r.Note.String() != "vda1 · sdb" {
		t.Errorf("with two devices the note names both: %q", r.Note.String())
	}
	n := 0
	for _, r := range s.Rows {
		if r.Label == "Disk" {
			n++
			if !strings.HasPrefix(r.Value, "read ") || !strings.Contains(r.Value, "busy") {
				t.Errorf("a device row = %+v", r)
			}
		}
	}
	if n != 2 {
		t.Errorf("%d device rows, want 2", n)
	}
}

func TestIOSectionReasons(t *testing.T) {
	now := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	since := now.Add(-7*time.Minute - 20*time.Second)
	for name, tc := range map[string]struct {
		io   service.HostIO
		want string
	}{
		"no agent":      {service.HostIO{Reason: service.IONoAgent}, "need Caravana's agent on the server"},
		"agent too old": {service.HostIO{Reason: service.IOAgentTooOld}, "The agent on this server is older"},
		"stale":         {service.HostIO{Reason: service.IOStale, Since: &since}, "No report from this server's agent for 7 minutes"},
		"stale 1 min":   {service.HostIO{Reason: service.IOStale, Since: new(now.Add(-time.Minute))}, "for 1 minute,"},
		"waiting":       {service.HostIO{Reason: service.IOWaiting}, "Waiting for the agent's next report"},
	} {
		s := ioSectionOf(now, tc.io)
		if !strings.Contains(s.Why.String(), tc.want) {
			t.Errorf("%s: why = %q, want %q", name, s.Why, tc.want)
		}
		if len(s.Rows) != 5 {
			t.Errorf("%s: %d rows", name, len(s.Rows))
		}
		for _, r := range s.Rows {
			if !r.None || r.Value != "—" {
				t.Errorf("%s: row %+v isn't a dash", name, r)
			}
		}
	}
}

// ---- the charts ------------------------------------------------------------------------------

func ioSeries(rng metrics.Range, n int, fill func(i int) (rx, tx, read, write *float64)) service.HostIOSeries {
	base := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	s := service.HostIOSeries{Range: rng, Points: make([]service.HostIOPoint, n)}
	for i := range s.Points {
		s.Points[i].T = base.Add(time.Duration(i-n+1) * rng.Step)
		rx, tx, rd, wr := fill(i)
		s.Points[i].NetRxBps, s.Points[i].NetTxBps, s.Points[i].DiskReadBps, s.Points[i].DiskWriteBps = rx, tx, rd, wr
		if rx != nil || tx != nil || rd != nil || wr != nil {
			s.Known = true
		}
	}
	return s
}

func TestHostIOCharts(t *testing.T) {
	now := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	host := service.HostView{IO: knownIO()}
	series := ioSeries(metrics.Range6h, 72, func(i int) (rx, tx, read, write *float64) {
		if i >= 30 && i <= 40 { // eleven points with no reading
			return nil, nil, nil, nil
		}
		return fl(2_500_000), fl(500_000), fl(2048), fl(0)
	})
	charts, line := hostIOChartsOf(now, host, series)
	if line != nil || len(charts) != 2 {
		t.Fatalf("charts = %d, line = %+v", len(charts), line)
	}
	net, disk := charts[0], charts[1]
	if net.Title != "Network, last 6 hours" || net.Solid != "in" || net.Dashed != "out" || disk.Title != "Disk, last 6 hours" || disk.Solid != "read" || disk.Dashed != "write" {
		t.Errorf("titles: %q (%s/%s), %q (%s/%s)", net.Title, net.Solid, net.Dashed, disk.Title, disk.Solid, disk.Dashed)
	}
	p := net.Plot
	if len(p.Lines) != 2 || p.Lines[0].Class != "cv-mnline" || p.Lines[1].Class != "cv-mnline-ref" {
		t.Fatalf("lines = %+v: the first is solid, the second dashed", p.Lines)
	}
	if len(p.Lines[0].Segments) != 2 || len(p.Gaps) != 1 {
		t.Errorf("the gap breaks the line: %d segments, %d gaps", len(p.Lines[0].Segments), len(p.Gaps))
	}
	if p.Lines[0].Title != "in: min 2.4 MB/s, max 2.4 MB/s, latest 2.4 MB/s" || p.Lines[1].Title != "out: min 488 KB/s, max 488 KB/s, latest 488 KB/s" {
		t.Errorf("titles = %q, %q", p.Lines[0].Title, p.Lines[1].Title)
	}
	// MB/s for the network chart (its largest is 2.4 MB/s), KB/s for the disk one (2 KB/s), the top gridline round.
	if !strings.HasSuffix(p.Scale, " MB/s") {
		t.Errorf("scale = %q", p.Scale)
	}
	if !strings.HasSuffix(disk.Plot.Scale, " KB/s") {
		t.Errorf("disk scale = %q", disk.Plot.Scale)
	}
	if p.Hover || p.Table.Head != "in" || p.Table.Head2 != "out" || len(p.Table.Rows) != 72 || p.Table.Rows[30].Value != "no reading" || p.Table.Rows[0].Value2 != "488 KB/s" {
		t.Errorf("table = %+v", p.Table.Rows[0])
	}
	if !strings.HasSuffix(net.AxisStart, "UTC") || net.AxisMid == "" {
		t.Errorf("axis = %q %q", net.AxisStart, net.AxisMid)
	}
}

func TestHostIOChartsRangesAndLine(t *testing.T) {
	now := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	fill := func(int) (*float64, *float64, *float64, *float64) { return fl(10), fl(20), fl(30), fl(40) }
	for rng, want := range map[metrics.Range]string{metrics.Range1h: "Network, last hour", metrics.Range24h: "Network, last 24 hours", metrics.Range7d: "Network, last 7 days"} {
		charts, _ := hostIOChartsOf(now, service.HostView{IO: knownIO()}, ioSeries(rng, rng.N(), fill))
		if len(charts) != 2 || charts[0].Title != want {
			t.Errorf("%s: %+v", rng.Name, charts)
		}
		if rng.Name == "7d" && !strings.Contains(charts[0].Plot.Table.Rows[0].Time, "2026-") {
			t.Errorf("a 7 day table has dates: %q", charts[0].Plot.Table.Rows[0].Time)
		}
	}

	none := ioSeries(metrics.Range6h, 72, func(int) (*float64, *float64, *float64, *float64) { return nil, nil, nil, nil })
	for name, tc := range map[string]struct {
		io   service.HostIO
		want string
	}{
		"no agent":     {service.HostIO{Reason: service.IONoAgent}, "need Caravana's agent on the server"},
		"too old":      {service.HostIO{Reason: service.IOAgentTooOld}, "The agent on this server is older"},
		"waiting":      {service.HostIO{Reason: service.IOWaiting}, "Waiting for the agent's next report"},
		"known, early": {knownIO(), "appear a minute or two after the server's agent has reported twice"},
	} {
		charts, line := hostIOChartsOf(now, service.HostView{IO: tc.io}, none)
		if len(charts) != 0 || line == nil || line.Mark != "idle" || !strings.Contains(line.Text.String(), tc.want) {
			t.Errorf("%s: charts %d, line %+v, want %q", name, len(charts), line, tc.want)
		}
	}
}

// ---- the pages -------------------------------------------------------------------------------

// A node Caravana doesn't manage (the lab's) shows dashes and the reason, on the server page and on
// Monitoring.
func TestPagesSayWhyThereIsNoIOWithoutAnAgent(t *testing.T) {
	e := srvEnv(t)
	resp, body := e.get(t, "/ops/servers/lab")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ops/servers/lab = %d", resp.StatusCode)
	}
	words := text(body)
	for _, want := range []string{"Network and disk", "Network in", "Network out", "Disk read", "Disk write", "Disk busy",
		"Network and disk throughput need Caravana's agent on the server"} {
		if !strings.Contains(words, want) {
			t.Errorf("the server page lacks %q:\n%s", want, words)
		}
	}
	if !strings.Contains(body, `id="io"`) || strings.Count(body, `class="cv-spaceval cv-srvval-none"`) < 5 {
		t.Error("the section's rows aren't dashes")
	}
	if i, j := strings.Index(body, `id="io"`), strings.Index(body, `id="space"`); j >= 0 && i > j {
		t.Error("the section is after Space")
	}

	// The refresh part carries it too.
	_, part := e.get(t, "/parts/ops/servers/lab")
	if !strings.Contains(text(part), "need Caravana's agent on the server") {
		t.Error("the body's refresh lacks the section")
	}

	_, mon := e.get(t, "/parts/monitoring/host")
	if !strings.Contains(text(mon), "Network and disk throughput need Caravana's agent on the server") || strings.Contains(mon, `id="mon-io-net-title"`) {
		t.Errorf("Monitoring's host section:\n%s", text(mon))
	}
}

// With an agent that reports, the server page shows the rates and Monitoring draws both charts once the
// sampler has written a host row with them.
func TestPagesShowTheIORatesOfAManagedServer(t *testing.T) {
	e := sampledServersEnv(t)
	first := e.firstToken(t)
	sync := func(at time.Time, rx, tx, read, write, busy int64) {
		t.Helper()
		r := healthy(2)
		r.ServerRunning = true
		r.IO = &service.NodeIO{At: at.UnixMilli(),
			Net:   []service.NodeIONet{{Name: "eth0", Kind: "default", RxBytes: rx, TxBytes: tx}, {Name: "caravana-mesh", Kind: "mesh", RxBytes: 100, TxBytes: 200}},
			Disks: []service.NodeIODisk{{Name: "vda1", Holds: []string{"nomad", "docker"}, ReadBytes: read, WriteBytes: write, BusyMs: busy}}}
		if _, err := e.Svc.NodeSync(t.Context(), first, r); err != nil {
			t.Fatalf("NodeSync: %v", err)
		}
	}

	// Before any report with counters: waiting.
	_, body := e.get(t, "/ops/servers/hel-1")
	if !strings.Contains(text(body), "Waiting for the agent's next report") {
		t.Errorf("before the first report:\n%s", text(body))
	}

	now := time.Now()
	sync(now.Add(-50*time.Second), 0, 0, 0, 0, 0)
	sync(now.Add(-20*time.Second), 31_457_280, 3_072_000, 15_728_640, 2_097_152, 15_000)

	_, body = e.get(t, "/ops/servers/hel-1")
	words := text(body)
	for _, want := range []string{"Network in 1 MB/s eth0", "Network out 100 KB/s eth0", "Between servers in 0 B/s · out 0 B/s caravana-mesh", "Disk read 512 KB/s vda1 · Nomad and Docker",
		"Disk write 68 KB/s", "Disk busy 50 % vda1"} {
		if !strings.Contains(words, want) {
			t.Errorf("the server page lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(words, "need Caravana's agent") || strings.Contains(words, "Waiting for the agent's next report") {
		t.Error("a known server also says why it is unknown")
	}
	if !regexp.MustCompile(`class="cv-inline-mono[^"]*">eth0<|<span class="[^"]*mono[^"]*">eth0</span>`).MatchString(body) {
		t.Errorf("eth0 isn't in a mono span:\n%s", body[strings.Index(body, `id="io"`):])
	}

	// The sampler writes the rates into its next host row; then Monitoring has the charts.
	deadline := time.Now().Add(15 * time.Second)
	var part string
	for {
		_, part = e.get(t, "/parts/monitoring/host?server=hel-1")
		if strings.Contains(part, `id="mon-io-net-title"`) || time.Now().After(deadline) {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	for _, want := range []string{"Network, last 6 hours", "Disk, last 6 hours", `class="cv-mnkey"`, "no reading", `<span class="cv-mnkey-res">out</span>`, `<span class="cv-mnkey-res">write</span>`} {
		if !strings.Contains(part+text(part), want) {
			t.Errorf("Monitoring's host section lacks %q:\n%s", want, text(part))
		}
	}
	if regexp.MustCompile(monNoStyle).MatchString(part) || regexp.MustCompile(monNoScript).MatchString(part) {
		t.Error("the I/O charts have a style attribute or an inline script")
	}
	if n := strings.Count(part, `class="cv-mnsvg"`); n < 2 { // the network and the disk (and memory, once an hour has a reading)
		t.Errorf("%d charts in the host section", n)
	}

	// The agent that stops reporting: after no more syncs the page doesn't keep showing old rates as now's
	// (the service's clock is real here, so only the reasons that don't need time are checked).
	sync2 := healthy(2)
	if _, err := e.Svc.NodeSync(t.Context(), first, sync2); err != nil {
		t.Fatal(err)
	}
	_, body = e.get(t, "/ops/servers/hel-1")
	if !strings.Contains(text(body), "The agent on this server is older") {
		t.Errorf("a report with no io block:\n%s", text(body))
	}
}
