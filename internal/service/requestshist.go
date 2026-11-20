package service

import (
	"math"
	"time"

	"github.com/cosmic-chimps/caravana/internal/accesslog"
)

// The histogram of a requests read (feature 047): the read's requests counted
// in 30 equal bins of the range, by status class. Like the logs', it is derived
// from the read and never stored.

// requestBins is how many bins the histogram has.
const requestBins = 30

// RequestBin is one bin of the histogram.
type RequestBin struct {
	From  time.Time // the bin's start
	Count int       // requests read in the bin
	// Classes is the bin's count per class, in accesslog.Classes' order.
	Classes [6]int
	// Read is false when the bin lies wholly before the coverage: its requests
	// weren't loaded, so its zero is not a zero.
	Read bool
}

// ClassCount is the bin's count of one class (accesslog.Class...); 0 for a name
// that isn't a class.
func (b RequestBin) ClassCount(class string) int {
	for i, c := range accesslog.Classes {
		if c == class {
			return b.Classes[i]
		}
	}
	return 0
}

// RequestHistogram is the 30 bins of a read's range.
type RequestHistogram struct {
	From, To time.Time
	// BucketSeconds is the width of a bin, rounded to whole seconds (at least 1).
	BucketSeconds int
	Bins          [requestBins]RequestBin
}

// Histogram counts the read's requests (every one that passed the filters,
// whatever Lines cut) into 30 bins over [From, To]. A request at To is in the
// last bin. When the coverage isn't complete, bins that end at or before
// Coverage.From have Read false.
func (r RequestRead) Histogram() RequestHistogram {
	h := RequestHistogram{From: r.From, To: r.To}
	width := r.To.Sub(r.From) / requestBins
	h.BucketSeconds = max(1, int(math.Round(width.Seconds())))

	for i := range h.Bins {
		h.Bins[i] = RequestBin{From: r.From.Add(time.Duration(i) * width), Read: true}
		end := r.From.Add(time.Duration(i+1) * width)
		if i == requestBins-1 {
			end = r.To
		}
		if !r.Coverage.Complete && !end.After(r.Coverage.From) {
			h.Bins[i].Read = false
		}
	}
	index := map[string]int{}
	for i, c := range accesslog.Classes {
		index[c] = i
	}
	for _, st := range r.stamps {
		if st.Time.Before(r.From) || st.Time.After(r.To) {
			continue
		}
		i := requestBins - 1
		if width > 0 {
			i = min(int(st.Time.Sub(r.From)/width), requestBins-1)
		}
		b := &h.Bins[i]
		b.Count++
		b.Classes[index[st.Class]]++
	}
	return h
}
