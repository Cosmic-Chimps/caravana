package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/logline"
)

// Download (feature 024, research R4): the filter of a page as a text file,
// oldest first, read with the same walk, parse, filter and fold as ReadLogs but
// under bigger budgets and written out line by line.
//
// As in logsread.go, nothing here logs, stores or puts a line in an error.

// The download's budgets and limits (data-model.md).
const (
	logDownloadStreamBudget = 8 << 20         // bytes read per stream
	logDownloadBudget       = 64 << 20        // bytes read per download, split evenly beyond 8 streams
	logDownloadDeadline     = 2 * time.Minute // how long the reading may take
	logDownloadTimeFormat   = "2006-01-02T15:04:05.000Z"
	logDownloadNameFormat   = "20060102T150405Z"
)

// ErrDownloadBusy is returned when a download is already running: at most one
// goes at a time per Caravana (the API's 409 download_busy).
var ErrDownloadBusy = errors.New("a download is already running")

// LogDownloadName is the file name of a download: no ':' in it.
func LogDownloadName(project, environment string, from, to time.Time) string {
	return "caravana-logs-" + project + "-" + environment + "-" +
		from.UTC().Format(logDownloadNameFormat) + "-" + to.UTC().Format(logDownloadNameFormat) + ".txt"
}

// downloadBudgets are the bytes a download may read.
type downloadBudgets struct {
	stream, total int64
	deadline      time.Duration
}

var defaultDownloadBudgets = downloadBudgets{logDownloadStreamBudget, logDownloadBudget, logDownloadDeadline}

// each is a stream's bytes when the download reads n streams: the stream
// budget, or the total split evenly (never less than one chunk).
func (b downloadBudgets) each(n int) int64 {
	if n > 0 && int64(n)*b.stream > b.total {
		return max(b.total/int64(n), logChunk)
	}
	return b.stream
}

// DownloadLogs writes the environment's output that q selects to w as text,
// oldest first. q is ReadLogs' query (its Lines and Before are ignored: a
// download is the whole range); its errors are ReadLogs'.
//
// The file is:
//
//	# newest last
//	# caravana logs <project>/<environment> <from> to <to>, read from <time> (<filters>)
//	<time>[~]<TAB><app>/<service><TAB><copy><TAB><stream><TAB><level><TAB><text>
//	# deploy <time> <app> v<N> <words>
//	# cut: the budget ended at <time>; older lines of copy <copy> weren't included
//
// A time ends in "~" when the line carried none and it was taken from an older
// line. A folded row is written as many times as it stands for. Deploy rows
// are merged in by time. The walk goes backwards from the newest line, so a
// cut means the older lines are the missing ones; one "# cut:" line is written
// per copy that was cut, last. Every text has its control sequences removed
// again and its tabs made spaces, so a file can't drive a terminal or move a
// column.
//
// One download runs at a time per Service: another call returns
// ErrDownloadBusy at once. Streams are read before the first byte is written
// (each under 8 MiB, 64 MiB in all), then written one line at a time, so an
// error of w ends the download. A write error is returned as it is.
func (s *Service) DownloadLogs(ctx context.Context, q EnvLogQuery, w io.Writer) error {
	return s.download(ctx, q, w, defaultDownloadBudgets)
}

func (s *Service) download(ctx context.Context, q EnvLogQuery, w io.Writer, b downloadBudgets) error {
	q.Lines, q.Before = 0, time.Time{}
	if err := q.Validate(); err != nil {
		return err
	}
	win, err := q.window(s.clock.Now().UTC())
	if err != nil {
		return err
	}
	apps, err := s.logApps(ctx, q)
	if err != nil {
		return err
	}
	copies, err := s.logCopiesOf(ctx, q, apps)
	if err != nil {
		return err
	}

	if !s.downloading.CompareAndSwap(false, true) {
		return ErrDownloadBusy
	}
	defer s.downloading.Store(false)

	readCtx, cancel := context.WithTimeout(ctx, b.deadline)
	defer cancel()
	streams, err := s.readLogStreams(ctx, readCtx, q, win, copies, b.each)
	if err != nil {
		return err
	}

	onlyApp := q.App
	if q.Service != "" {
		onlyApp, _, _ = strings.Cut(q.Service, "/")
	}
	deploys, err := s.LogDeploys(ctx, q.Project, q.Environment, win.from, win.to, onlyApp)
	if err != nil {
		return err
	}
	return writeDownload(ctx, w, q, win, streams, deploys)
}

// downloadCut is one copy the download didn't read back to the range's start.
type downloadCut struct {
	copy     string // short ID
	edge     time.Time
	deadline bool // the time ran out, not the bytes
}

// writeDownload writes the file: the two first lines, the merge, the cuts.
func writeDownload(ctx context.Context, w io.Writer, q EnvLogQuery, win logWindow, streams []*logStream, deploys []DeployRow) error {
	// Where the read reached, as assembleLogRead works it out: the newest of
	// the oldest times of the streams that stopped early.
	from := win.from
	var cuts []downloadCut
	var edgeNewest time.Time
	for _, ls := range streams {
		if ls.res.reason != stopBudget && ls.res.reason != stopDeadline {
			continue
		}
		edge := ls.oldest
		if edge.IsZero() {
			edge = win.upper() // nothing was read: nothing is known
		}
		edgeNewest = laterOf(edgeNewest, edge)
		i := -1
		for j := range cuts {
			if cuts[j].copy == ls.copy.short {
				i = j
			}
		}
		if i < 0 {
			cuts = append(cuts, downloadCut{copy: ls.copy.short})
			i = len(cuts) - 1
		}
		cuts[i].edge = laterOf(cuts[i].edge, edge)
		cuts[i].deadline = cuts[i].deadline || ls.res.reason == stopDeadline
	}
	if edgeNewest.After(from) {
		from = edgeNewest
	}

	bw := bufio.NewWriter(w)
	project, environment := q.Project, q.Environment
	if project == "" {
		project = app.DefaultProject
	}
	if environment == "" {
		environment = app.DefaultEnvironment
	}
	fmt.Fprintf(bw, "# newest last\n")
	fmt.Fprintf(bw, "# caravana logs %s/%s %s to %s, read from %s (%s)\n", project, environment,
		win.from.Format(time.RFC3339), win.to.Format(time.RFC3339), from.Format(time.RFC3339), downloadFilters(q))

	// The k-way merge, oldest first. A deploy row goes before the lines at the
	// same time, since its output follows it.
	next := make([]int, len(streams))
	nd := 0
	for n := 0; ; n++ {
		if n%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		best := -1
		for i, ls := range streams {
			if next[i] >= len(ls.rows) {
				continue
			}
			if best < 0 || ls.rows[next[i]].sort.Before(streams[best].rows[next[best]].sort) {
				best = i
			}
		}
		if nd < len(deploys) && (best < 0 || !deploys[nd].At.After(streams[best].rows[next[best]].sort)) {
			fmt.Fprintf(bw, "# deploy %s %s\n", deploys[nd].At.UTC().Format(time.RFC3339), downloadDeployWords(deploys[nd]))
			nd++
			continue
		}
		if best < 0 {
			break
		}
		e := streams[best].rows[next[best]]
		next[best]++
		line := downloadLine(e)
		for range e.Repeat + 1 {
			bw.WriteString(line)
		}
		if bw.Buffered() > 32<<10 {
			if err := bw.Flush(); err != nil {
				return err
			}
		}
	}

	for _, c := range cuts {
		what := "the budget"
		if c.deadline {
			what = "the time limit"
		}
		fmt.Fprintf(bw, "# cut: %s ended at %s; older lines of copy %s weren't included\n", what, c.edge.UTC().Format(time.RFC3339), c.copy)
	}
	return bw.Flush()
}

// downloadLine is one row's line.
func downloadLine(e LogEntry) string {
	ts := e.Time.UTC().Format(logDownloadTimeFormat)
	if e.TimeSource == logline.Estimated {
		ts += "~"
	}
	return ts + "\t" + e.App + "/" + e.Service + "\t" + e.Copy + "\t" + string(e.Stream) + "\t" + string(e.Level) + "\t" +
		downloadText(e.Text) + "\n"
}

var downloadSpaces = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")

// downloadText is text as the file holds it: control sequences removed once
// more (nothing from the app reaches a file unstripped), and no character that
// would move a field or end the line.
func downloadText(s string) string {
	return downloadSpaces.Replace(logline.StripANSI(s))
}

// downloadDeployWords is "<app> v<N> <words>" for a deploy row, as the
// console says it.
func downloadDeployWords(d DeployRow) string {
	var what string
	switch d.Kind {
	case DeployStarted:
		what = "started rolling out"
	case DeployLive:
		what = "is live"
	case DeployFailed:
		what = "failed its health checks"
	case DeployReverted:
		what = "failed its health checks"
		if d.RevertedTo > 0 {
			what += " · rolled back to v" + strconv.Itoa(d.RevertedTo)
		} else {
			what += " · rolled back"
		}
	case DeploySuperseded:
		what = "was replaced by a newer deploy"
	default:
		what = string(d.Kind)
	}
	if d.Who != "" {
		what += " · " + d.Who
	}
	return downloadText(d.App + " v" + strconv.Itoa(d.JobVersion) + " " + what)
}

// downloadFilters says what the query kept, for the header.
func downloadFilters(q EnvLogQuery) string {
	var parts []string
	appID, service := q.App, ""
	if q.Service != "" {
		appID, service, _ = strings.Cut(q.Service, "/")
	}
	if appID != "" {
		parts = append(parts, "app "+appID)
	}
	if service != "" {
		parts = append(parts, "service "+service)
	}
	if q.Copy != "" {
		parts = append(parts, "copy "+q.Copy)
	}
	if q.Stream == logline.Stdout || q.Stream == logline.Stderr {
		parts = append(parts, "stream "+string(q.Stream))
	}
	f := q.filter()
	var levels []string
	for _, l := range []logline.Level{logline.Error, logline.Warn, logline.Info, logline.Debug} {
		if f.levels[l] {
			levels = append(levels, string(l))
		}
	}
	parts = append(parts, "levels "+strings.Join(levels, ","))
	if q.Q != "" {
		parts = append(parts, "q "+strconv.Quote(q.Q))
	}
	return strings.Join(parts, ", ")
}
