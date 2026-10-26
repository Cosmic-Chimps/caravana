package ui

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Feature 025: "This host" on /monitoring (#host), and the meters and room sentence the Apps home's
// Platform panel shares. All of it is built from a service.HostView.

func hostSample(now time.Time) service.HostView {
	at := now.Add(-30 * time.Second)
	return service.HostView{
		SampledAt: &at,
		Memory:    service.HostResource{Used: i64(1946), Reserved: i64(1024), Total: i64(3891)},
		CPU:       service.HostCPU{HostResource: service.HostResource{Used: i64(2880), Reserved: i64(1200), Total: i64(9600)}, Cores: i64(4)},
		Disks: []service.HostDisk{{Mount: "/", Holds: []string{"nomad", "docker"}, UsedMB: 18432, TotalMB: 40960, FreeMB: 22528,
			UsedPercent: 45, State: metrics.DiskOK}},
		Room:    service.HostRoom{Known: true, RoomResult: metrics.RoomResult{HasYardstick: true, Count: 4, LikeApp: "whoami", MemoryMB: 256, CPUMHz: 100}, LikeAppName: "whoami"},
		Sources: service.SamplerSources{Traefik: service.SamplerOK, Nomad: service.SamplerOK},
	}
}

func TestHostMeters(t *testing.T) {
	now := time.Date(2026, 10, 25, 14, 0, 0, 0, time.UTC)
	v := hostViewOf(now, hostSample(now))
	if v.Meta != "1 server" || len(v.Lines) != 0 || len(v.Meters) != 3 {
		t.Fatalf("view = %+v", v)
	}
	mem, cpu, disk := v.Meters[0], v.Meters[1], v.Meters[2]
	if mem.Label != "mem" || mem.Read != "1.9/3.8G" || !mem.HasReserved || mem.ResW != "26.3" || mem.Fill != "50" || mem.Warn {
		t.Errorf("memory = %+v", mem)
	}
	if mem.Title != "1.9 GB in use, 1 GB reserved of 3.8 GB" {
		t.Errorf("memory title = %q", mem.Title)
	}
	if cpu.Label != "cpu" || cpu.Read != "1.2/4 vCPU" || !cpu.HasReserved || cpu.ResW != "12.5" || cpu.Fill != "30" {
		t.Errorf("cpu = %+v", cpu)
	}
	if disk.Label != "disk" || disk.Read != "18/40G" || disk.Warn || disk.Mark != "" || disk.Fill != "45" {
		t.Errorf("disk = %+v", disk)
	}

	// Above 90 % the fill and the readout are warn.
	h := hostSample(now)
	h.Memory.Used = i64(3600)
	if m := hostViewOf(now, h).Meters[0]; !m.Warn {
		t.Errorf("memory at 92%% = %+v", m)
	}
	// A CPU with unknown cores reads in MHz; a number never read leaves its meter out.
	h = hostSample(now)
	h.CPU.Cores = nil
	h.Memory.Total = nil
	got := hostViewOf(now, h).Meters
	if len(got) != 2 || got[0].Label != "cpu" || got[0].Read != "2880/9600 MHz" {
		t.Errorf("meters = %+v", got)
	}
}

func TestHostDisks(t *testing.T) {
	now := time.Date(2026, 10, 25, 14, 0, 0, 0, time.UTC)
	h := hostSample(now)
	h.Disks = []service.HostDisk{
		{Mount: "/var/lib/nomad", UsedMB: 36864, TotalMB: 40960, FreeMB: 4096, UsedPercent: 90, State: metrics.DiskWarning},
		{Mount: "/var/lib/docker", UsedMB: 39936, TotalMB: 40960, FreeMB: 1024, UsedPercent: 97.5, State: metrics.DiskFailed},
		{Mount: "/srv", UsedMB: 1024, TotalMB: 40960, FreeMB: 39936, UsedPercent: 2.5, State: metrics.DiskOK},
	}
	var disks []monMeter
	for _, m := range hostViewOf(now, h).Meters {
		if m.Label == "disk" {
			disks = append(disks, m)
		}
	}
	if len(disks) != 3 {
		t.Fatalf("%d disk meters", len(disks))
	}
	if d := disks[0]; d.Read != "/var/lib/nomad 36/40G" || d.Mark != "warning" || !d.Warn || d.MarkWord != "nearly full" || d.Title != "/var/lib/nomad: 90% used, 4 GB left" {
		t.Errorf("warning disk = %+v", d)
	}
	if d := disks[1]; d.Read != "/var/lib/docker 39/40G" || d.Mark != "failed" || !d.Warn || d.MarkWord != "full" {
		t.Errorf("failed disk = %+v", d)
	}
	if d := disks[2]; d.Mark != "" || d.Warn || d.Read != "/srv 1/40G" {
		t.Errorf("ok disk = %+v", d)
	}
}

func TestHostRoomSentences(t *testing.T) {
	yard := func(count int, by metrics.Resource) service.HostRoom {
		return service.HostRoom{Known: true, LikeAppName: "whoami", RoomResult: metrics.RoomResult{HasYardstick: true, Count: count,
			LikeApp: "whoami", MemoryMB: 256, CPUMHz: 100, LimitedBy: by}}
	}
	tests := []struct {
		name string
		room service.HostRoom
		want string
		warn bool
	}{
		{"room", yard(4, metrics.ResourceMemory), "Room for about 4 more apps like whoami (256 MB, 100 MHz each).", false},
		{"no memory", yard(0, metrics.ResourceMemory), "No room for another app like whoami: memory is fully reserved.", true},
		{"no CPU", yard(0, metrics.ResourceCPU), "No room for another app like whoami: CPU is fully reserved.", true},
		{"nothing running", service.HostRoom{Known: true, RoomResult: metrics.RoomResult{FreeMemoryMB: 3686}}, "Nothing is running yet: 3.6 GB of memory is free to reserve.", false},
		{"nothing running, little", service.HostRoom{Known: true, RoomResult: metrics.RoomResult{FreeMemoryMB: 512}}, "Nothing is running yet: 512 MB of memory is free to reserve.", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := roomOf(tc.room)
			if got == nil || got.Text.String() != tc.want || got.Warn != tc.warn || got.Title != "Nomad's count of what fits. Caravana's and the host's own processes aren't reserved." {
				t.Errorf("room = %+v, want %q (warn %v)", got, tc.want, tc.warn)
			}
		})
	}
	if roomOf(service.HostRoom{}) != nil {
		t.Error("a room that couldn't be counted says nothing")
	}
}

func TestHostLines(t *testing.T) {
	now := time.Date(2026, 10, 25, 14, 0, 0, 0, time.UTC)
	ok := hostSample(now)

	denied := ok
	denied.Sources.Nomad = service.SamplerDenied
	lines := hostViewOf(now, denied).Lines
	if len(lines) != 1 || lines[0].Mark != "failed" || !strings.Contains(lines[0].Text.String(), "Caravana's Nomad token can't read the host") || !strings.Contains(lines[0].Text.String(), "node:read") {
		t.Errorf("denied = %+v", lines)
	}

	stale := ok
	old := now.Add(-12 * time.Minute)
	stale.SampledAt, stale.Stale = &old, true
	v := hostViewOf(now, stale)
	if len(v.Lines) != 1 || v.Lines[0].Mark != "failed" || v.Lines[0].Text.String() != "The newest reading of the host is from 13:48 UTC. The numbers here are the last known ones." {
		t.Errorf("stale = %+v", v.Lines)
	}
	if len(v.Meters) != 3 {
		t.Error("a stale host keeps its last known numbers drawn")
	}

	never := service.HostView{Sources: service.SamplerSources{Nomad: service.SamplerUnknown}}
	v = hostViewOf(now, never)
	if len(v.Lines) != 1 || v.Lines[0].Mark != "idle" || len(v.Meters) != 0 || v.Room != nil || v.Chart != nil {
		t.Errorf("never read = %+v", v)
	}

	unreachable := ok
	unreachable.Sources.Nomad = service.SamplerUnreachable
	if lines := hostViewOf(now, unreachable).Lines; len(lines) != 1 || lines[0].Mark != "failed" {
		t.Errorf("unreachable = %+v", lines)
	}
}

func TestHostChart(t *testing.T) {
	now := time.Date(2026, 10, 25, 14, 0, 0, 0, time.UTC)
	h := hostSample(now)
	n := 168
	h.History = service.HostHistory{Points: make([]time.Time, n), MemoryUsed: make([]*float64, n), MemoryReserved: make([]*float64, n)}
	for i := range h.History.Points {
		h.History.Points[i] = now.Truncate(time.Hour).Add(time.Duration(i-n+1) * time.Hour)
		if i < 100 || i > 104 { // five hours with no reading
			h.History.MemoryUsed[i], h.History.MemoryReserved[i] = fl(2000), fl(1024)
		}
	}
	c := hostViewOf(now, h).Chart
	if c == nil {
		t.Fatal("no chart")
	}
	p := c.Plot
	if len(p.Lines) != 2 || p.Lines[0].Class != "cv-mnline" || p.Lines[1].Class != "cv-mnline-ref" {
		t.Fatalf("lines = %+v: used is solid, reserved dashed", p.Lines)
	}
	if len(p.Lines[0].Segments) != 2 || len(p.Gaps) != 1 {
		t.Errorf("the gap breaks the line: %d segments, %d gaps", len(p.Lines[0].Segments), len(p.Gaps))
	}
	if p.Lines[0].Title != "used: min 2 GB, max 2 GB, latest 2 GB" || p.Lines[1].Title != "reserved: min 1 GB, max 1 GB, latest 1 GB" {
		t.Errorf("title = %q", p.Lines[0].Title)
	}
	if p.Hover || p.Table.Head2 != "Reserved" || len(p.Table.Rows) != n || p.Table.Rows[100].Value != "no reading" || p.Table.Rows[0].Value2 != "1 GB" {
		t.Errorf("table = %+v hover %v", p.Table.Rows[0], p.Hover)
	}
	if !strings.HasSuffix(c.AxisStart, "UTC") || c.AxisMid == "" {
		t.Errorf("axis = %q %q", c.AxisStart, c.AxisMid)
	}
}

// ---- the page --------------------------------------------------------------------------

// monHostEnv is monEnv with the host sampled: 1.9 of 3.8 GB used, 256 MB reserved (whoami's), a
// disk, and whoami's reservation on its rows so the yardstick is whoami. age is how old the
// host sample is.
func monHostEnv(t *testing.T, age time.Duration, disks ...store.DiskSample) *consoleEnv {
	t.Helper()
	return monHostEnvOf(t, age, 1946, 256, 3891, disks...)
}

// monHostEnvOf is monHostEnv with the host's memory (MB used, reserved, total) given.
func monHostEnvOf(t *testing.T, age time.Duration, used, reservedMB, total int64, disks ...store.DiskSample) *consoleEnv {
	t.Helper()
	e := monEnv(t)
	ctx := context.Background()
	series, err := e.Store.EnsureSeries(ctx, "whoami", "web")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	reserved := i64(256)
	res := []store.MetricsSample{{Series: series, TS: now.Add(-time.Minute), Minutes: 1, Copies: i64(1), MemoryReservedMB: reserved, CPUReservedMHz: i64(100), MemoryMB: i64(1000), MemoryLimitMB: i64(1024)}}
	at := now.Add(-age)
	tick := store.MetricsTick{Minute: store.MetricsRows{Samples: res, Hosts: []store.HostSample{{
		Node: "node-1", TS: at, MemoryUsedMB: i64(used), MemoryReservedMB: i64(reservedMB), MemoryTotalMB: i64(total),
		CPUUsedMHz: i64(2880), CPUReservedMHz: i64(2400), CPUTotalMHz: i64(9600), CPUCores: i64(4)}}, Disks: disks}}
	for i := range tick.Minute.Disks {
		tick.Minute.Disks[i].TS = at
	}
	if err := e.Store.WriteMetricsTick(ctx, tick); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestMonitoringHostSection(t *testing.T) {
	e := monHostEnv(t, 30*time.Second, store.DiskSample{Node: "node-1", Mount: "/", Holds: "nomad,docker", UsedMB: 18432, TotalMB: 40960, FreeMB: 22528})
	resp, body := e.get(t, "/monitoring")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /monitoring = %d", resp.StatusCode)
	}
	words := text(body)
	for _, want := range []string{"This host 1 server", "mem", "1.9/3.8G", "cpu", "1.2/4 vCPU", "disk", "18/40G",
		"Room for about 14 more apps like whoami (256 MB, 100 MHz each).", "Memory, last 7 days"} {
		if !strings.Contains(words, want) {
			t.Errorf("the host section lacks %q:\n%s", want, words)
		}
	}
	for _, want := range []string{`id="host"`, `hx-get="/parts/monitoring/host" hx-trigger="sse:metrics"`, `class="cv-mnroom" title="Nomad&#39;s count of what fits. Caravana&#39;s and the host&#39;s own processes aren&#39;t reserved."`,
		`class="cv-mnmeter-svg"`, `class="cv-mnreserved"`, `class="cv-mnkey-res"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if regexp.MustCompile(monNoStyle).MatchString(body) || regexp.MustCompile(monNoScript).MatchString(body) {
		t.Error("the host section has a style attribute or an inline script")
	}
	// The footer comes after the host, as the contract has it.
	if strings.Index(body, `id="host"`) > strings.Index(body, `class="cv-mnfoot"`) {
		t.Error("the footer is above the host")
	}

	resp, part := e.get(t, "/parts/monitoring/host")
	if resp.StatusCode != http.StatusOK || strings.Contains(part, "<html") || !strings.Contains(text(part), "This host 1 server") || !strings.Contains(text(part), "1.9/3.8G") {
		t.Errorf("part = %d:\n%s", resp.StatusCode, text(part))
	}
}

func TestMonitoringHostStaleAndFull(t *testing.T) {
	e := monHostEnv(t, 12*time.Minute, store.DiskSample{Node: "node-1", Mount: "/", Holds: "nomad,docker", UsedMB: 39000, TotalMB: 40960, FreeMB: 1960})
	_, body := e.get(t, "/monitoring")
	words := text(body)
	if !strings.Contains(words, "The newest reading of the host is from") || !strings.Contains(words, "The numbers here are the last known ones.") {
		t.Errorf("no stale line:\n%s", words)
	}
	if !strings.Contains(body, `cv-mnmeter-read cv-mnwarntext"><span class="cv-mark cv-mark-failed"`) {
		t.Error("the disk at 95% isn't a failed mark with a warn readout")
	}
}

func TestMonitoringHostNeverRead(t *testing.T) {
	e := monEnv(t)
	_, body := e.get(t, "/monitoring")
	words := text(body)
	if !strings.Contains(words, "The host hasn't been read yet.") || strings.Contains(words, "Room for about") {
		t.Errorf("never-read host:\n%s", words)
	}
	if strings.Contains(body, `class="cv-mnmeter"`) {
		t.Error("meters with nothing read")
	}
}

// The hatched line of the host meters runs from 0 to the reserved fraction of the total, for memory
// and for CPU (1331 of 1949 MB is 68.3 %, 2400 of 9600 MHz is 25 %), read from the page's markup.
func TestHostReservedLineIsDrawnToTheFraction(t *testing.T) {
	e := monHostEnvOf(t, 30*time.Second, 900, 1331, 1949)
	_, body := e.get(t, "/monitoring")
	lines := regexp.MustCompile(`<line class="cv-mnreserved" x1="0" x2="([0-9.]+)" y1="3" y2="3"/>`).FindAllStringSubmatch(body, -1)
	if len(lines) != 2 || lines[0][1] != "68.3" || lines[1][1] != "25" {
		t.Errorf("reserved lines = %v, want x2 68.3 (memory) and 25 (cpu)", lines)
	}
}

// Memory takes the warn hue from 85 % with a ◆ mark and a ■ mark from 95 %, as the disk does; under 85 %
// the fill is the plain one. The page's markup carries the classes.
func TestHostMemoryMeterStates(t *testing.T) {
	for _, tc := range []struct {
		used      int64
		mark      string
		fillClass string
		readClass string
	}{
		{1946, "", "cv-mnfill", "cv-mnmeter-read"},
		{8400, "", "cv-mnfill", "cv-mnmeter-read"},
		{8500, "warning", "cv-mnfill cv-mnfill-warn", "cv-mnmeter-read cv-mnwarntext"},
		{9400, "warning", "cv-mnfill cv-mnfill-warn", "cv-mnmeter-read cv-mnwarntext"},
		{9500, "failed", "cv-mnfill cv-mnfill-warn", "cv-mnmeter-read cv-mnwarntext"},
	} {
		e := monHostEnvOf(t, 30*time.Second, tc.used, 2000, 10000)
		_, body := e.get(t, "/parts/monitoring/host")
		meter := regexp.MustCompile(`(?s)<div class="cv-mnmeter">.*?</div>`).FindString(body)
		if !strings.Contains(meter, "mem") || !strings.Contains(meter, `class="`+tc.fillClass+`"`) || !strings.Contains(meter, `class="`+tc.readClass+`"`) {
			t.Errorf("memory at %d/10000 draws %s, want %q and %q", tc.used, meter, tc.fillClass, tc.readClass)
		}
		if tc.mark != "" && !strings.Contains(meter, "cv-mark-"+tc.mark) || tc.mark == "" && strings.Contains(meter, "cv-mark-") {
			t.Errorf("memory at %d/10000 mark = %s, want %q", tc.used, meter, tc.mark)
		}
	}
}
