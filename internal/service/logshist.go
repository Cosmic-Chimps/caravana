package service

import (
	"math"
	"time"

	"github.com/cosmic-chimps/caravana/internal/logline"
)

// The histogram of a logs read (feature 024, research R6): the read's lines
// counted in 30 equal bins of the range. It is derived from the read and never
// stored; the API (and the console's page) call histogramOf on the LogRead
// ReadLogs returned.

// logBins is how many bins the histogram has.
const logBins = 30

// LogBin is one bin of the histogram.
type LogBin struct {
	From      time.Time // the bin's start
	Count     int       // lines read in the bin, estimated ones included
	Errors    int       // of those, error lines
	Estimated int       // of those, lines at an estimated time
	// Read is false when the bin lies wholly before the coverage: its output
	// wasn't loaded, so its zero is not a zero. A bin is never drawn as empty
	// when its minutes weren't read.
	Read bool
}

// LogHistogram is the 30 bins of a read's range.
type LogHistogram struct {
	From, To time.Time
	// BucketSeconds is the width of a bin, rounded to whole seconds (at least 1).
	BucketSeconds int
	Bins          [logBins]LogBin
}

// histogramOf counts the read's lines into 30 bins over [r.From, r.To]. The
// lines are the unfolded ones that passed the range and the filters
// (r.stamps). A line at To is in the last bin. When the coverage isn't
// complete, bins that end at or before Coverage.From have Read false.
func histogramOf(r LogRead) LogHistogram {
	h := LogHistogram{From: r.From, To: r.To}
	span := r.To.Sub(r.From)
	width := span / logBins
	h.BucketSeconds = max(1, int(math.Round(width.Seconds())))

	for i := range h.Bins {
		start := r.From.Add(time.Duration(i) * width)
		h.Bins[i] = LogBin{From: start, Read: true}
		end := r.From.Add(time.Duration(i+1) * width)
		if i == logBins-1 {
			end = r.To
		}
		if !r.Coverage.Complete && !end.After(r.Coverage.From) {
			h.Bins[i].Read = false
		}
	}
	for _, st := range r.stamps {
		if st.Time.Before(r.From) || st.Time.After(r.To) {
			continue
		}
		i := logBins - 1
		if width > 0 {
			i = min(int(st.Time.Sub(r.From)/width), logBins-1)
		}
		b := &h.Bins[i]
		b.Count++
		if st.Level == logline.Error {
			b.Errors++
		}
		if st.Estimated {
			b.Estimated++
		}
	}
	return h
}

// firstSeen is the open row's note, "first seen 09:12 UTC, 3 times since": how
// many lines of what was read have the same text as text (the text with its
// time cut out, logline.TextWithoutTime), and the oldest time among them. lines
// are newest first, as LogRead.Lines. A folded row counts for all the lines it
// replaced, and its time is its group's newest, so the first time can be a
// little later than the true first. n is 0 (and first the zero time) when no
// line has that text.
func firstSeen(lines []LogEntry, text string) (first time.Time, n int) {
	for _, l := range lines {
		if logline.TextWithoutTime(l.Line) != text {
			continue
		}
		n += 1 + l.Repeat
		first = l.Time // the last one met is the oldest
	}
	return first, n
}

// Histogram is the read's 30 bins over its range, for the API and the page.
func (r LogRead) Histogram() LogHistogram { return histogramOf(r) }

// LevelCounts is how many lines of the read's range there are at each level:
// the same stamps the histogram counts (every line that passed the range and
// the filters, folded or not), so a chip's count and the histogram's total
// say the same number. Levels with no lines are absent.
func (r LogRead) LevelCounts() map[logline.Level]int {
	out := map[logline.Level]int{}
	for _, st := range r.stamps {
		if st.Time.Before(r.From) || st.Time.After(r.To) {
			continue
		}
		out[st.Level]++
	}
	return out
}
