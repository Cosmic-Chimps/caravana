package metrics

import "sort"

// AppReservation is a running stored app's reservation, summed over its
// services and copies (its sidecars included, by Nomad's flattening rule).
// Jobs Caravana didn't make and backup or schedule runs are not apps here;
// they only count in what is reserved.
type AppReservation struct {
	App      string
	MemoryMB int64
	CPUMHz   int64
}

// Resource names what ran out first.
type Resource string

const (
	ResourceMemory Resource = "memory"
	ResourceCPU    Resource = "cpu"
)

// RoomResult is "room for about N more apps like X".
type RoomResult struct {
	// HasYardstick is false when no stored app is running (or the yardstick
	// reserves nothing, so nothing could be counted): there is no Count, and
	// the page says how much memory is free to reserve instead.
	HasYardstick bool
	// Count is how many more apps like LikeApp fit; 0 reads "No room…".
	Count int
	// LikeApp, MemoryMB and CPUMHz are the yardstick X: the running app whose
	// summed memory reservation is the median.
	LikeApp  string
	MemoryMB int64
	CPUMHz   int64
	// LimitedBy is what the smaller count came from (memory on a tie).
	LimitedBy Resource
	// FreeMemoryMB and FreeCPUMHz are what is left to reserve (never negative).
	FreeMemoryMB int64
	FreeCPUMHz   int64
}

// Room counts what fits by reservation, since Nomad places by reservation and
// not by use. The yardstick X is the running app whose summed memory
// reservation is the median of the apps' (the lower middle when their count is
// even; of the apps that share that memory, the alphabetically first).
// Count is ⌊min(free memory / X.memory, free CPU / X.cpu)⌋. A side where X
// reserves nothing doesn't limit. Free amounts below zero (reserved above the
// total) count as 0.
func Room(apps []AppReservation, freeMemMB, freeCPUMHz int64) RoomResult {
	freeMemMB, freeCPUMHz = max(freeMemMB, 0), max(freeCPUMHz, 0)
	out := RoomResult{FreeMemoryMB: freeMemMB, FreeCPUMHz: freeCPUMHz}
	if len(apps) == 0 {
		return out
	}
	sorted := append([]AppReservation(nil), apps...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].MemoryMB != sorted[j].MemoryMB {
			return sorted[i].MemoryMB < sorted[j].MemoryMB
		}
		return sorted[i].App < sorted[j].App
	})
	mid := sorted[(len(sorted)-1)/2].MemoryMB
	var x AppReservation
	for _, a := range sorted { // sorted by name within the same memory
		if a.MemoryMB == mid {
			x = a
			break
		}
	}
	if x.MemoryMB <= 0 && x.CPUMHz <= 0 {
		return out
	}

	memN, cpuN := int64(-1), int64(-1)
	if x.MemoryMB > 0 {
		memN = freeMemMB / x.MemoryMB
	}
	if x.CPUMHz > 0 {
		cpuN = freeCPUMHz / x.CPUMHz
	}
	n, by := memN, ResourceMemory
	if memN < 0 || (cpuN >= 0 && cpuN < memN) {
		n, by = cpuN, ResourceCPU
	}
	out.HasYardstick = true
	out.Count = int(n)
	out.LikeApp, out.MemoryMB, out.CPUMHz, out.LimitedBy = x.App, x.MemoryMB, x.CPUMHz, by
	return out
}
