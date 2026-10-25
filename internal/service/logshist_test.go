package service

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/logline"
)

func histRead(from time.Time, d time.Duration, complete bool, covFrom time.Time, stamps ...logStamp) LogRead {
	return LogRead{From: from, To: from.Add(d), stamps: stamps,
		Coverage: LogCoverage{From: covFrom, Complete: complete, Timed: true}}
}

func TestHistogramBins(t *testing.T) {
	from := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return from.Add(time.Duration(sec) * time.Second) }
	// 15 minutes: 30 s per bin.
	r := histRead(from, 15*time.Minute, true, from,
		logStamp{Time: at(0), Level: logline.Info},
		logStamp{Time: at(29), Level: logline.Error},
		logStamp{Time: at(30), Level: logline.Info, Estimated: true},
		logStamp{Time: at(31), Level: logline.Error, Estimated: true},
		logStamp{Time: at(32), Level: logline.Warn},
		logStamp{Time: at(100), Level: logline.Debug},
		logStamp{Time: at(-1), Level: logline.Info},  // before the range: not counted
		logStamp{Time: at(901), Level: logline.Info}, // after it
	)
	h := histogramOf(r)
	if h.BucketSeconds != 30 || !h.From.Equal(from) || !h.To.Equal(r.To) {
		t.Fatalf("bucket %d from %s to %s", h.BucketSeconds, h.From, h.To)
	}
	want := map[int]LogBin{
		0: {Count: 2, Errors: 1},
		1: {Count: 3, Errors: 1, Estimated: 2},
		3: {Count: 1},
	}
	for i, b := range h.Bins {
		if !b.From.Equal(from.Add(time.Duration(i) * 30 * time.Second)) {
			t.Errorf("bin %d starts at %s", i, b.From)
		}
		w := want[i]
		if b.Count != w.Count || b.Errors != w.Errors || b.Estimated != w.Estimated || !b.Read {
			t.Errorf("bin %d = %+v, want count %d errors %d estimated %d read", i, b, w.Count, w.Errors, w.Estimated)
		}
	}
}

func TestHistogramStampAtToIsInTheLastBin(t *testing.T) {
	from := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	r := histRead(from, time.Hour, true, from,
		logStamp{Time: from.Add(time.Hour), Level: logline.Error},
		logStamp{Time: from, Level: logline.Info})
	h := histogramOf(r)
	if h.Bins[29].Count != 1 || h.Bins[29].Errors != 1 || h.Bins[0].Count != 1 {
		t.Errorf("first %+v last %+v, want one line in each", h.Bins[0], h.Bins[29])
	}
	if h.BucketSeconds != 120 {
		t.Errorf("BucketSeconds = %d, want 120", h.BucketSeconds)
	}
}

func TestHistogramUnreadBins(t *testing.T) {
	from := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	// One hour, 2 minutes a bin; the coverage reaches back to 00:21, inside bin 10 (00:20-00:22).
	cov := from.Add(21 * time.Minute)
	r := histRead(from, time.Hour, false, cov, logStamp{Time: from.Add(25 * time.Minute), Level: logline.Info})
	h := histogramOf(r)
	for i, b := range h.Bins {
		if wantRead := i >= 10; b.Read != wantRead {
			t.Errorf("bin %d Read = %v, want %v", i, b.Read, wantRead)
		}
	}
	if h.Bins[12].Count != 1 {
		t.Errorf("bin 12 count = %d", h.Bins[12].Count)
	}

	// A coverage that falls exactly on a bin's end leaves that bin unread, the next one read.
	r = histRead(from, time.Hour, false, from.Add(20*time.Minute))
	h = histogramOf(r)
	if h.Bins[9].Read || !h.Bins[10].Read {
		t.Errorf("bins 9 and 10 Read = %v %v, want false true", h.Bins[9].Read, h.Bins[10].Read)
	}

	// Complete means every bin was read, whatever Coverage.From says.
	r = histRead(from, time.Hour, true, from.Add(30*time.Minute))
	for i, b := range histogramOf(r).Bins {
		if !b.Read {
			t.Errorf("complete read: bin %d is unread", i)
		}
	}
}

func TestHistogramEmptyRead(t *testing.T) {
	from := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	h := histogramOf(histRead(from, 15*time.Minute, true, from))
	for i, b := range h.Bins {
		if b.Count != 0 || b.Errors != 0 || b.Estimated != 0 || !b.Read {
			t.Errorf("bin %d = %+v, want zero and read", i, b)
		}
	}
	// A zero-length range puts everything in the last bin and doesn't divide by zero.
	h = histogramOf(histRead(from, 0, true, from, logStamp{Time: from, Level: logline.Info}))
	if h.Bins[29].Count != 1 || h.BucketSeconds != 1 {
		t.Errorf("zero range: last bin %+v, bucket %d", h.Bins[29], h.BucketSeconds)
	}
}

func TestFirstSeen(t *testing.T) {
	e := readEnv(t)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, []string{
		"2026-10-25T00:01:00Z cache miss for key a",
		"2026-10-25T00:02:00Z other",
		"2026-10-25T00:03:00Z cache miss for key a",
		"2026-10-25T00:04:00Z cache miss for key a",
		"2026-10-25T00:05:00Z cache miss for key a",
		"2026-10-25T00:06:00Z cache miss for key b",
	}, wrote("00:07:00"))
	read := mustRead(t, e, EnvLogQuery{App: "blog", Range: time.Hour})

	var text string
	for _, l := range read.Lines {
		if l.Repeat == 2 {
			text = logline.TextWithoutTime(l.Line)
		}
	}
	if text != "cache miss for key a" {
		t.Fatalf("no folded row with the text, got %q (rows %q)", text, entryTexts(read.Lines))
	}
	first, n := firstSeen(read.Lines, text)
	// 4 lines read: 00:01, and the folded 00:03-00:05 (its time is 00:05).
	if n != 4 || !first.Equal(readTime(t, "2026-10-25T00:01:00Z")) {
		t.Errorf("firstSeen = %s, %d, want 00:01 and 4", first, n)
	}
	if first, n := firstSeen(read.Lines, "never printed"); n != 0 || !first.IsZero() {
		t.Errorf("no match = %s, %d", first, n)
	}
}

// The chips' counts are the histogram's: the stamps of the range, per level.
func TestLevelCountsMatchTheHistogram(t *testing.T) {
	from := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return from.Add(time.Duration(sec) * time.Second) }
	r := histRead(from, 15*time.Minute, true, from,
		logStamp{Time: at(1), Level: logline.Error},
		logStamp{Time: at(2), Level: logline.Error},
		logStamp{Time: at(3), Level: logline.Warn},
		logStamp{Time: at(4), Level: logline.Info},
		logStamp{Time: at(-5), Level: logline.Error},       // before the range
		logStamp{Time: at(15*60 + 5), Level: logline.Warn}, // after it
	)
	got := r.LevelCounts()
	if got[logline.Error] != 2 || got[logline.Warn] != 1 || got[logline.Info] != 1 || got[logline.Debug] != 0 {
		t.Errorf("LevelCounts = %v, want error 2, warn 1, info 1", got)
	}
	errs := 0
	for _, b := range r.Histogram().Bins {
		errs += b.Errors
	}
	if errs != got[logline.Error] {
		t.Errorf("histogram has %d errors, the counts %d", errs, got[logline.Error])
	}
}
