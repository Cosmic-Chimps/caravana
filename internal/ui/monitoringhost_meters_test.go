package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The readout splits what is used from what is reserved, and CPU is in vCPU everywhere.
func TestHostSplitReadouts(t *testing.T) {
	now := time.Date(2026, 10, 25, 14, 0, 0, 0, time.UTC)
	h := hostSample(now)
	h.Memory = service.HostResource{Used: i64(1740), Reserved: i64(1331), Total: i64(1946)}
	h.CPU = service.HostCPU{HostResource: service.HostResource{Used: i64(2880), Reserved: i64(1920), Total: i64(4800)}, Cores: i64(2)}
	mem, cpu := memoryMeter(h), cpuMeter(h)
	if mem.Read != "1.7G used · 1.3G reserved of 1.9G" || !mem.Split {
		t.Errorf("memory = %q (split %v)", mem.Read, mem.Split)
	}
	if cpu.Read != "1.2 used · 0.8 reserved of 2 vCPU" || cpu.Title != "1.2 vCPU in use, 0.8 vCPU reserved of 2 vCPU" || !cpu.Split {
		t.Errorf("cpu = %q, title %q", cpu.Read, cpu.Title)
	}
	h.Memory.Reserved, h.CPU.Reserved = nil, nil
	if got := memoryMeter(h).Read; got != "1.7G used of 1.9G" {
		t.Errorf("memory without a reservation = %q", got)
	}
	if got := cpuMeter(h).Read; got != "1.2 used of 2 vCPU" {
		t.Errorf("cpu without a reservation = %q", got)
	}
	for in, want := range map[float64]string{0.04: "0.04", 0.1: "0.1", 0.96: "0.96", 1.2: "1.2", 9.96: "10", 12: "12", 0: "0"} {
		if got := vcpuText(in); got != want {
			t.Errorf("vcpuText(%v) = %q, want %q", in, got, want)
		}
	}
}

// When memory in use is at 85 % or more, the room sentence says so after its count, because
// room counts reservations; the title is unchanged, and vCPU is the CPU unit.
func TestHostRoomSaysWhenMemoryIsInUse(t *testing.T) {
	room := service.HostRoom{Known: true, LikeAppName: "whoami", RoomResult: metrics.RoomResult{HasYardstick: true, Count: 20,
		LikeApp: "whoami", MemoryMB: 32, CPUMHz: 240}}
	at := func(used int64) service.HostView {
		return service.HostView{Room: room, Memory: service.HostResource{Used: i64(used), Total: i64(1000)},
			CPU: service.HostCPU{HostResource: service.HostResource{Total: i64(4800)}, Cores: i64(2)}}
	}
	for _, tc := range []struct {
		used int64
		want string
	}{
		{500, "Room for about 20 more apps like whoami (32 MB, 0.1 vCPU each)."},
		{849, "Room for about 20 more apps like whoami (32 MB, 0.1 vCPU each)."},
		{850, "Room for about 20 more apps like whoami (32 MB, 0.1 vCPU each), but memory in use is already at 85%."},
		{899, "Room for about 20 more apps like whoami (32 MB, 0.1 vCPU each), but memory in use is already at 89%."},
	} {
		got := roomOf(at(tc.used))
		if got.Text.String() != tc.want || got.Title != roomTitle || got.Warn {
			t.Errorf("used %d: room = %q (title %q), want %q", tc.used, got.Text.String(), got.Title, tc.want)
		}
	}
	// Without the cores the unit is MHz, and without a total there is nothing to add.
	if got := roomOf(service.HostView{Room: room}); got.Text.String() != "Room for about 20 more apps like whoami (32 MB, 240 MHz each)." {
		t.Errorf("without CPU cores = %q", got.Text.String())
	}
}

// The reserved mark is drawn after the fill, so a use above the reservation doesn't hide it; the
// split readout and the vCPU unit are on the page.
func TestHostReservedIsAboveTheFill(t *testing.T) {
	e := monHostEnvOf(t, 30*time.Second, 1740, 1331, 1946)
	_, body := e.get(t, "/parts/monitoring/host")
	meters := regexp.MustCompile(`<svg class="cv-mnmeter-svg"[^>]*>.*?</svg>`).FindAllString(body, -1)
	if len(meters) < 2 {
		t.Fatalf("meters = %d", len(meters))
	}
	for _, m := range meters[:2] {
		fill, reserved := strings.Index(m, `class="cv-mnfill`), strings.Index(m, `class="cv-mnreserved"`)
		if fill < 0 || reserved < 0 || reserved < fill {
			t.Errorf("the reserved line is not drawn after the fill: %s", m)
		}
	}
	words := text(body)
	for _, want := range []string{"1.7G used · 1.3G reserved of 1.9G", "1.2 used · 1 reserved of 4 vCPU", "vCPU each)"} {
		if !strings.Contains(words, want) {
			t.Errorf("the host lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(words, "MHz") {
		t.Errorf("a CPU amount is still in MHz:\n%s", words)
	}
	if !strings.Contains(body, `cv-mnmeter cv-mnmeter-split`) {
		t.Error("the split readouts have no class to sit under the bar")
	}
}
