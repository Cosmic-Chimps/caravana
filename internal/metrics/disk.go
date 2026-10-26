package metrics

import "time"

// The disk thresholds (ruling 8), in percent used.
const (
	// DiskWarnPercent opens the item (◆); a meter reads warning from here.
	DiskWarnPercent = 85.0
	// DiskFailPercent makes it failed (■).
	DiskFailPercent = 95.0
	// DiskResolvePercent: an open item stays open while the disk is at or
	// above this, and resolves on a sample under it.
	DiskResolvePercent = 83.0
	// DiskStaleAfter: a host sample older than this is not judged.
	DiskStaleAfter = 5 * time.Minute
)

// DiskLevel is the meter's state of a filesystem.
type DiskLevel string

const (
	DiskOK      DiskLevel = "ok"
	DiskWarning DiskLevel = "warning"
	DiskFailed  DiskLevel = "failed"
)

// DiskStatus is one sample's reading of a filesystem.
type DiskStatus struct {
	// State is for the meter and getHost: ok below 85 %, warning from 85 %,
	// failed from 95 %.
	State DiskLevel
	// Open says the needs-you item is open after this sample.
	Open bool
	// Level is the open item's level, by this sample's percent (failed from
	// 95 %, else warning); empty when the item is closed.
	Level DiskLevel
}

// DiskState reads one sample. The item opens at 85 % and stays open while the
// disk is at or above 83 % (prevOpen says it was open before this sample); a
// sample under 83 % closes it. The level follows the current percent.
func DiskState(usedPct float64, prevOpen bool) DiskStatus {
	st := DiskStatus{State: DiskOK}
	switch {
	case usedPct >= DiskFailPercent:
		st.State = DiskFailed
	case usedPct >= DiskWarnPercent:
		st.State = DiskWarning
	}
	if usedPct >= DiskWarnPercent || (prevOpen && usedPct >= DiskResolvePercent) {
		st.Open = true
		st.Level = DiskWarning
		if usedPct >= DiskFailPercent {
			st.Level = DiskFailed
		}
	}
	return st
}

// DiskSample is one stored sample of a filesystem.
type DiskSample struct {
	At          time.Time
	UsedPercent float64
}

// DiskRun reads a filesystem's samples, oldest first, and says whether its
// item is open after the newest one, and when the current run began: the
// first sample at or above 85 % since the item last closed. The hysteresis is
// DiskState's, applied along the history, so an item that opened at 86 % and
// has read 84 % since is open and began at the 86 % sample.
func DiskRun(samples []DiskSample) (open bool, when time.Time) {
	for _, s := range samples {
		st := DiskState(s.UsedPercent, open)
		switch {
		case st.Open && !open:
			when = s.At
		case !st.Open:
			when = time.Time{}
		}
		open = st.Open
	}
	return open, when
}

// UsedPercent is used / (used + free) × 100, what df prints: free is what a
// non-root writer gets, so the percent is Nomad's UsedPercent. No space at
// all reads 0.
func UsedPercent(usedMB, freeMB int64) float64 {
	if usedMB+freeMB <= 0 {
		return 0
	}
	return float64(usedMB) * 100 / float64(usedMB+freeMB)
}

// Stale says the host sample is too old to judge: older than 5 minutes (a
// sample exactly 5 minutes old is still fresh).
func Stale(sampleAt, now time.Time) bool {
	return now.Sub(sampleAt) > DiskStaleAfter
}
