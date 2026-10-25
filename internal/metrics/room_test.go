package metrics_test

import (
	"testing"

	"github.com/cosmic-chimps/caravana/internal/metrics"
)

func apps(kv ...any) []metrics.AppReservation {
	var out []metrics.AppReservation
	for i := 0; i < len(kv); i += 3 {
		out = append(out, metrics.AppReservation{App: kv[i].(string), MemoryMB: int64(kv[i+1].(int)), CPUMHz: int64(kv[i+2].(int))})
	}
	return out
}

func TestRoomYardstick(t *testing.T) {
	tests := []struct {
		name string
		apps []metrics.AppReservation
		want string
	}{
		{"one app", apps("only", 256, 100), "only"},
		{"odd count: the middle", apps("a", 512, 100, "b", 128, 100, "c", 256, 100), "c"},
		{"even count: the lower middle", apps("a", 400, 100, "b", 100, 100, "c", 300, 100, "d", 200, 100), "d"},
		{"two apps: the smaller", apps("big", 1024, 100, "small", 64, 100), "small"},
		{"ties: the alphabetically first", apps("c", 32, 100, "a", 32, 100, "b", 32, 100, "z", 544, 100), "a"},
		{"ties at the median only", apps("m", 32, 100, "k", 64, 100, "b", 64, 100, "a", 64, 100, "z", 544, 100), "a"},
		{"the lab's: ten at 32 MB, two at 96, one at 544", apps(
			"blog", 32, 100, "a1", 32, 100, "a2", 32, 100, "a3", 32, 100, "a4", 32, 100, "a5", 32, 100,
			"a6", 32, 100, "a7", 32, 100, "a8", 32, 100, "hollo", 96, 300, "db", 96, 300, "nginx", 544, 500), "a1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := metrics.Room(tc.apps, 10000, 10000)
			if !got.HasYardstick || got.LikeApp != tc.want {
				t.Errorf("yardstick = %q (has %v), want %q", got.LikeApp, got.HasYardstick, tc.want)
			}
		})
	}
	// The input is not reordered.
	in := apps("b", 2, 1, "a", 1, 1)
	metrics.Room(in, 10, 10)
	if in[0].App != "b" {
		t.Error("Room sorted its input")
	}
}

func TestRoomCount(t *testing.T) {
	x := apps("whoami", 256, 100)
	tests := []struct {
		name          string
		mem, cpu      int64
		want          int
		by            metrics.Resource
		freeMem, cpu2 int64
	}{
		{"memory exactly 4 fits", 1024, 100000, 4, metrics.ResourceMemory, 1024, 100000},
		{"memory just under 4", 1023, 100000, 3, metrics.ResourceMemory, 1023, 100000},
		{"memory just over 4", 1025, 100000, 4, metrics.ResourceMemory, 1025, 100000},
		{"memory just under one", 255, 100000, 0, metrics.ResourceMemory, 255, 100000},
		{"memory exactly one", 256, 100000, 1, metrics.ResourceMemory, 256, 100000},
		{"cpu exactly 3", 100000, 300, 3, metrics.ResourceCPU, 100000, 300},
		{"cpu just under 3", 100000, 299, 2, metrics.ResourceCPU, 100000, 299},
		{"cpu just over 3", 100000, 301, 3, metrics.ResourceCPU, 100000, 301},
		{"cpu ran out", 100000, 99, 0, metrics.ResourceCPU, 100000, 99},
		{"a tie names memory", 512, 200, 2, metrics.ResourceMemory, 512, 200},
		{"cpu smaller than memory", 1024, 200, 2, metrics.ResourceCPU, 1024, 200},
		{"both out: memory", 0, 0, 0, metrics.ResourceMemory, 0, 0},
		{"over-reserved clamps to 0", -300, -5, 0, metrics.ResourceMemory, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := metrics.Room(x, tc.mem, tc.cpu)
			if !got.HasYardstick || got.Count != tc.want || got.LimitedBy != tc.by {
				t.Errorf("Room = %+v, want %d limited by %s", got, tc.want, tc.by)
			}
			if got.FreeMemoryMB != tc.freeMem || got.FreeCPUMHz != tc.cpu2 {
				t.Errorf("free = %d MB %d MHz, want %d %d", got.FreeMemoryMB, got.FreeCPUMHz, tc.freeMem, tc.cpu2)
			}
			if got.LikeApp != "whoami" || got.MemoryMB != 256 || got.CPUMHz != 100 {
				t.Errorf("yardstick facts = %+v", got)
			}
		})
	}
}

func TestRoomTheLabsNumbers(t *testing.T) {
	// 10 apps of 32 MB, 2 of 96 MB and nginx 544 MB; free 1949 − 1312 MB of
	// memory and 4000 − 1730 MHz: min(19, 22) = 19, limited by memory.
	var list []metrics.AppReservation
	for _, n := range []string{"blog", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		list = append(list, metrics.AppReservation{App: n, MemoryMB: 32, CPUMHz: 100})
	}
	list = append(list, metrics.AppReservation{App: "hollo", MemoryMB: 96, CPUMHz: 300},
		metrics.AppReservation{App: "db", MemoryMB: 96, CPUMHz: 300},
		metrics.AppReservation{App: "nginx", MemoryMB: 544, CPUMHz: 500})
	got := metrics.Room(list, 1949-1312, 4000-1730)
	if got.Count != 19 || got.LimitedBy != metrics.ResourceMemory || got.LikeApp != "b" {
		t.Errorf("Room = %+v", got)
	}
}

func TestRoomWithoutApps(t *testing.T) {
	got := metrics.Room(nil, 3600, 2000)
	if got.HasYardstick || got.Count != 0 || got.LikeApp != "" || got.LimitedBy != "" {
		t.Errorf("Room(nil) = %+v", got)
	}
	if got.FreeMemoryMB != 3600 || got.FreeCPUMHz != 2000 {
		t.Errorf("the free memory is still reported: %+v", got)
	}
}

func TestRoomYardstickThatReservesNothingOnOneSide(t *testing.T) {
	got := metrics.Room(apps("m", 256, 0), 1024, 0)
	if !got.HasYardstick || got.Count != 4 || got.LimitedBy != metrics.ResourceMemory {
		t.Errorf("cpu 0 doesn't limit: %+v", got)
	}
	got = metrics.Room(apps("c", 0, 100), 0, 350)
	if !got.HasYardstick || got.Count != 3 || got.LimitedBy != metrics.ResourceCPU {
		t.Errorf("memory 0 doesn't limit: %+v", got)
	}
	if got := metrics.Room(apps("z", 0, 0), 1000, 1000); got.HasYardstick {
		t.Errorf("a yardstick that reserves nothing can't size anything: %+v", got)
	}
}
