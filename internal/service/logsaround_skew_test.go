package service

import (
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/logline"
)

// A file whose times are hours behind its last write (a container in a zone
// west of UTC) is estimated whole, even when the window stops short of the
// file's end, so the file's tail is read to know.
func TestLogsAroundSkewedFileIsEstimated(t *testing.T) {
	e := readEnv(t)
	started := readTime(t, "2026-10-24T20:00:00Z")
	e.nomad.SetCopies("blog", copyStub(copyBlogWeb, "web", "running", started))
	f := makeAroundFile("skew", readTime(t, "2026-10-24T21:00:00Z"), 600, time.Second, 90)
	e.nomad.AddLogFile(copyBlogWeb, "stdout", 0, f.lines, wrote("00:30:00"))

	got := mustAround(t, e, "9a4d13ff", logline.LineID{Stream: logline.Stdout, Offset: f.offsets[100]}, 3)
	if len(got.Lines) != 7 {
		t.Fatalf("%d lines, want 7", len(got.Lines))
	}
	for _, l := range got.Lines {
		if l.TimeSource != logline.Estimated {
			t.Errorf("%q: time source %s, want estimated", l.Text[:30], l.TimeSource)
		}
	}
}
