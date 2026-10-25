package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/logline"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The environment's logs (feature 024): the output of every copy of every
// service of one project › environment, read from Nomad's files on every view
// and never stored. ReadLogs is the page read; the pieces it is made of
// (walkLogStream, parseLogStream, finishLogStream, mergeNewestFirst) are what
// the around read, the download and the live notice reuse.
//
// Nothing here writes a line to a logger, an error or a notice: a line is the
// app's output and may hold anything (constitution VII). Errors carry copy
// IDs and Nomad's own words, never a line.

// The read's budgets and limits (data-model.md).
const (
	logStreamBudget = 1 << 20          // bytes read per stream for a page
	logPageBudget   = 16 << 20         // bytes read per page, split evenly beyond 16 streams
	logReadsAtOnce  = 2                // page reads going at once per Caravana
	logChunk        = 64 << 10         // bytes per readat
	logReadDeadline = 5 * time.Second  // how long a page read may take
	logWalkWorkers  = 8                // Nomad calls going at once inside one read
	logDefaultLines = 500              // rows after folding, by default
	logMaxLines     = 1000             // and at most
	logMaxQuery     = 200              // bytes of q
	logMaxLine      = 64 << 10         // a longer line is cut here, as deploy.Logs does
	logSkewLimit    = time.Minute      // how far a file's last timed line may be from its ModTime
	logProbeLines   = 64               // lines of a chunk looked at for a time, to know the walk went far enough
	logDefaultRange = 15 * time.Minute // when neither a range nor from and to are given
)

// logRanges are the presets of Range.
var logRanges = []time.Duration{15 * time.Minute, time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}

// ErrLogCopyNotFound is a copy that isn't one of the environment's copies for
// the query (the API's 404 not_found). It also matches store.ErrNotFound, as
// an unknown app does, so a caller that doesn't tell them apart needs only one
// check.
var ErrLogCopyNotFound = fmt.Errorf("copy %w", store.ErrNotFound)

// EnvLogQuery is what a page read of an environment's output is asked. It is
// the environment's counterpart of LogQuery, which reads one app's tail.
type EnvLogQuery struct {
	// Project and Environment name the place; empty means Default and
	// production.
	Project, Environment string
	// App is an app ID of the place, Service "<app>/<service>" and Copy an
	// allocation ID or its first 8 characters. Each narrows the copies read,
	// before anything is read.
	App, Service, Copy string
	// Stream is stdout or stderr; "" or "both" reads both.
	Stream logline.Stream
	// Levels are the levels shown; nil means error, warn and info.
	Levels []logline.Level
	// Q is a case-insensitive substring of the text plus "<app>/<service>",
	// at most 200 bytes.
	Q string
	// From and To bound the lines by their time. Or Range, one of 15m, 1h,
	// 24h and 7d, ending at To; not both. To defaults to now.
	From, To time.Time
	Range    time.Duration
	// Before is "Show older": only lines before it are shown.
	Before time.Time
	// Lines is how many rows after folding, 1 to 1000; 0 means 500.
	Lines int
}

// Validate checks the query's own fields, naming the field at fault as a
// *app.FieldError. Whether the place, app, service and copy exist is checked
// by ReadLogs.
func (q EnvLogQuery) Validate() error {
	switch q.Stream {
	case "", "both", logline.Stdout, logline.Stderr:
	default:
		return app.NewFieldError("stream", "use stdout, stderr or both")
	}
	for _, l := range q.Levels {
		switch l {
		case logline.Error, logline.Warn, logline.Info, logline.Debug:
		default:
			return app.NewFieldError("levels", "use error, warn, info or debug")
		}
	}
	if len(q.Q) > logMaxQuery {
		return app.NewFieldError("q", "is longer than %d bytes", logMaxQuery)
	}
	if q.Service != "" {
		a, svc, ok := strings.Cut(q.Service, "/")
		if !ok || a == "" || svc == "" || strings.Contains(svc, "/") {
			return app.NewFieldError("service", "is written <app>/<service>")
		}
	}
	if q.Range != 0 {
		if !slices.Contains(logRanges, q.Range) {
			return app.NewFieldError("range", "use 15m, 1h, 24h or 7d")
		}
		if !q.From.IsZero() || !q.To.IsZero() {
			return app.NewFieldError("range", "give a range, or from and to, not both")
		}
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.From.After(q.To) {
		return app.NewFieldError("from", "is after to")
	}
	if q.Lines < 0 || q.Lines > logMaxLines {
		return app.NewFieldError("lines", "must be between 1 and %d", logMaxLines)
	}
	return nil
}

// logWindow is the resolved time range of a read.
type logWindow struct {
	from, to time.Time // both UTC; a line is in when from <= its time <= to
	before   time.Time // zero, or the lines must be before it
}

// has says whether a line at t is in the window.
func (w logWindow) has(t time.Time) bool {
	if t.Before(w.from) || t.After(w.to) {
		return false
	}
	return w.before.IsZero() || t.Before(w.before)
}

// upper is the newest time a line can have to be wanted.
func (w logWindow) upper() time.Time {
	if !w.before.IsZero() && w.before.Before(w.to) {
		return w.before
	}
	return w.to
}

// window resolves From, To and Range against now.
func (q EnvLogQuery) window(now time.Time) (logWindow, error) {
	to := q.To
	if to.IsZero() {
		to = now
	}
	from := q.From
	if from.IsZero() {
		r := q.Range
		if r == 0 {
			r = logDefaultRange
		}
		from = to.Add(-r)
	}
	w := logWindow{from: from.UTC(), to: to.UTC(), before: q.Before.UTC()}
	if w.from.After(w.to) {
		return logWindow{}, app.NewFieldError("from", "is after to")
	}
	if !q.Before.IsZero() && !q.Before.After(w.from) {
		return logWindow{}, app.NewFieldError("before", "is not after the start of the range")
	}
	return w, nil
}

// logFilter is the level and text filter of a query, ready to apply.
type logFilter struct {
	levels map[logline.Level]bool
	q      string // lower-cased; "" matches everything
}

func (q EnvLogQuery) filter() logFilter {
	levels := q.Levels
	if len(levels) == 0 {
		levels = []logline.Level{logline.Error, logline.Warn, logline.Info}
	}
	f := logFilter{levels: map[logline.Level]bool{}, q: strings.ToLower(q.Q)}
	for _, l := range levels {
		f.levels[l] = true
	}
	return f
}

// keeps says whether the line passes: its level is wanted and the query is in
// its text plus "<app>/<service>" (the handoff's search).
func (f logFilter) keeps(l logline.Line, app, service string) bool {
	if !f.levels[l.Level] {
		return false
	}
	if f.q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(l.Text+" "+app+"/"+service), f.q)
}

// LogEntry is one row of the stream: a line, read, with where it came from.
type LogEntry struct {
	logline.Line
	App     string // the app ID
	Service string
	Copy    string // the copy's short ID, the first 8 characters of its allocation ID

	// sort is where the row sits among the others: its time, made
	// non-decreasing along its stream so that a line a few milliseconds older
	// than the one before it (a multi-threaded app) never moves ahead of it.
	// The row keeps its own Time for display.
	sort time.Time
}

// LogCopy is a copy in the rail: one copy the query read.
type LogCopy struct {
	ID      string // the allocation ID
	Short   string
	App     string
	Service string
	Gone    bool // the copy has ended and Nomad still keeps its output
	Lines   int  // its lines in the read, before folding, after the level and text filters
}

// LogCoverage says how far back the read went.
type LogCoverage struct {
	// From is the oldest time every stream was read back to: nothing newer
	// than it is missing. When Complete, it is the range's start.
	From time.Time
	// Complete is true when every stream was read to the range's start or to
	// the oldest file Nomad keeps.
	Complete bool
	// Timed is false when the deadline, not the budget, ended a stream.
	Timed bool
}

// LogRead is one view's answer.
type LogRead struct {
	Lines    []LogEntry // newest first
	Coverage LogCoverage
	Copies   []LogCopy
	// Dropped is true when a stream was read back to the oldest file Nomad
	// keeps and older files were already purged (or one was purged during the
	// read): output before it is gone.
	Dropped bool
	// From and To are the range the read resolved.
	From, To time.Time

	// stamps is every line that passed the range, level and text filters,
	// unfolded and unsorted: what the histogram counts (T006).
	stamps []logStamp
}

// logStamp is what the histogram needs of a line.
type logStamp struct {
	Time      time.Time
	Level     logline.Level
	Estimated bool
}

// shortCopy is a copy's short ID: the first 8 characters of its allocation ID.
func shortCopy(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// ReadLogs reads the output of an environment's copies, newest first.
//
//   - The place must exist (*PlaceError, with the field project or
//     environment). App must be one of its apps (store.ErrNotFound), Service
//     one of that app's services (a *app.FieldError on service) and Copy one
//     of the copies left after those (ErrLogCopyNotFound).
//   - The copies are cut by App, Service and Copy before anything is read.
//   - At most two reads run at once per Caravana; another waits for its turn
//     (or for ctx). Once it has its turn the read has logReadDeadline.
//   - Each stream of each copy is walked backwards from its newest file under
//     its budget; see walkLogStream. A stream the deadline stopped is read as
//     far as it got, and Coverage.Timed says so.
//   - A copy Nomad no longer has when it is read is left out.
//
// Nomad can't be read: the error matches deploy.ErrNomadUnavailable.
func (s *Service) ReadLogs(ctx context.Context, q EnvLogQuery) (LogRead, error) {
	if err := q.Validate(); err != nil {
		return LogRead{}, err
	}
	win, err := q.window(s.clock.Now().UTC())
	if err != nil {
		return LogRead{}, err
	}
	apps, err := s.logApps(ctx, q)
	if err != nil {
		return LogRead{}, err
	}
	copies, err := s.logCopiesOf(ctx, q, apps)
	if err != nil {
		return LogRead{}, err
	}

	select {
	case s.logSem <- struct{}{}:
		defer func() { <-s.logSem }()
	case <-ctx.Done():
		return LogRead{}, ctx.Err()
	}
	readCtx, cancel := context.WithTimeout(ctx, s.logDeadline)
	defer cancel()

	streams, err := s.readLogStreams(ctx, readCtx, q, win, copies, streamBudget, true)
	if err != nil {
		return LogRead{}, err
	}
	return assembleLogRead(q, win, streams), nil
}

// logApps resolves the place and the apps the query may read.
func (s *Service) logApps(ctx context.Context, q EnvLogQuery) ([]store.AppRecord, error) {
	project, environment := q.Project, q.Environment
	if project == "" {
		project = app.DefaultProject
	}
	if environment == "" {
		environment = app.DefaultEnvironment
	}
	if err := s.requireEnvironment(ctx, project, environment); err != nil {
		field := "environment"
		if errors.Is(err, ErrProjectNotFound) {
			field = "project"
		}
		if errors.Is(err, ErrProjectNotFound) || errors.Is(err, ErrEnvironmentNotFound) {
			return nil, &PlaceError{Field: field, Err: err}
		}
		return nil, err
	}
	recs, err := s.store.ListAppsIn(ctx, project, environment)
	if err != nil {
		return nil, fmt.Errorf("read logs: %w", err)
	}

	appID, service := q.App, ""
	if q.Service != "" {
		var svcApp string
		svcApp, service, _ = strings.Cut(q.Service, "/")
		if appID != "" && appID != svcApp {
			return nil, app.NewFieldError("service", "is a service of app %q, not of %q", svcApp, appID)
		}
		appID = svcApp
	}
	if appID == "" {
		return recs, nil
	}
	i := slices.IndexFunc(recs, func(r store.AppRecord) bool { return r.App.ID == appID })
	if i < 0 {
		if q.Service != "" && q.App == "" {
			return nil, app.NewFieldError("service", "app %q is not in %s/%s", appID, project, environment)
		}
		return nil, fmt.Errorf("app %q is not in %s/%s: %w", appID, project, environment, store.ErrNotFound)
	}
	if service != "" && !slices.ContainsFunc(recs[i].App.Services, func(sv app.Service) bool { return sv.Name == service }) {
		return nil, app.NewFieldError("service", "app %q has no service %q", appID, service)
	}
	return recs[i : i+1], nil
}

// logCopy is one copy to read, with the app it belongs to.
type logCopy struct {
	deploy.Copy
	app   string
	short string
}

// logCopiesOf lists the copies of the apps, every one Nomad still lists, and
// cuts them by the query's service and copy.
func (s *Service) logCopiesOf(ctx context.Context, q EnvLogQuery, apps []store.AppRecord) ([]logCopy, error) {
	var service string
	if q.Service != "" {
		_, service, _ = strings.Cut(q.Service, "/")
	}
	var out []logCopy
	for _, rec := range apps {
		copies, err := s.deployer.LogCopies(ctx, []string{rec.App.ID})
		if err != nil {
			return nil, fmt.Errorf("read logs: %w", err)
		}
		for _, c := range copies {
			if service != "" && c.Service != service {
				continue
			}
			if q.Copy != "" && c.ID != q.Copy && shortCopy(c.ID) != q.Copy {
				continue
			}
			out = append(out, logCopy{Copy: c, app: rec.App.ID, short: shortCopy(c.ID)})
		}
	}
	if q.Copy != "" && len(out) == 0 {
		return nil, fmt.Errorf("copy %q: %w", q.Copy, ErrLogCopyNotFound)
	}
	return out, nil
}

// stopReason is why a stream's walk ended.
type stopReason int

const (
	stopEnd      stopReason = iota // no file is left: the oldest file Nomad keeps was read to its start
	stopRange                      // a timed line older than the range's start was read
	stopBudget                     // the stream's bytes were spent
	stopDeadline                   // the page's time ran out
)

// rawLine is a line as read, before it is parsed.
type rawLine struct {
	text   string // valid UTF-8, no newline, at most logMaxLine bytes
	file   int    // the position of its file in the stream's file list
	offset int64  // the byte offset of its first byte in that file
}

// walkLimits bound one stream's walk.
type walkLimits struct {
	budget int64     // bytes to read, give or take a chunk
	from   time.Time // stop once a timed line older than this was read; zero reads on
	upper  time.Time // zero, or files that can only hold lines newer than this are skipped
}

// streamRead is what a walk found.
type streamRead struct {
	raws    []rawLine // oldest first
	reason  stopReason
	read    int64 // bytes read
	dropped bool  // older output existed and Nomad purged it
}

// streamBudget is each stream's bytes for a page that reads n streams: 1 MiB,
// or the page's 16 MiB split evenly when there are more than 16 (never less
// than one chunk).
func streamBudget(n int) int64 {
	if n > logPageBudget/logStreamBudget {
		return max(int64(logPageBudget/n), logChunk)
	}
	return logStreamBudget
}

// fileBounds says when the lines of files[i] can have been written: after the
// previous file's last write (or the copy's start for the first file) and
// before this file's last write, each with a minute of slack.
func fileBounds(files []deploy.LogFile, i int, started *time.Time) logline.Bounds {
	var b logline.Bounds
	if m := files[i].ModTime; !m.IsZero() {
		b.NotAfter = m.Add(logline.Slack)
	}
	switch {
	case i > 0 && !files[i-1].ModTime.IsZero():
		b.NotBefore = files[i-1].ModTime.Add(-logline.Slack)
	case i == 0 && started != nil:
		b.NotBefore = started.Add(-logline.Slack)
	}
	return b
}

// writeSpan is when the lines of files[i] were written, as the listing says:
// from the previous file's last write (the copy's start for the first file) to
// this file's last write, without slack. Either end is zero when unknown.
func writeSpan(files []deploy.LogFile, i int, started *time.Time) (lo, hi time.Time) {
	switch {
	case i > 0:
		lo = files[i-1].ModTime
	case started != nil:
		lo = *started
	}
	return lo, files[i].ModTime
}

// between places byte offset off of a stretch of size bytes written from lo
// to hi: lo plus the same share of the time as off is of the bytes. With lo
// unknown it is hi, and with hi unknown (or not after lo) it is lo; both
// unknown give the zero time.
func between(lo, hi time.Time, off, size int64) time.Time {
	switch {
	case lo.IsZero():
		return hi
	case hi.IsZero() || size <= 0 || !hi.After(lo):
		return lo
	}
	off = min(max(off, 0), size)
	return lo.Add(time.Duration(float64(hi.Sub(lo)) * float64(off) / float64(size)))
}

// placeLines settles the times of one file's lines that carry none of their
// own (research R2 and its gate follow-up). group is what logline.Parse made
// of a run of files[fi]'s lines, oldest first, each with its offset in its ID;
// started is the copy's first start. Every line it places is Estimated.
//
//   - A file with no timed line in the group, or a skewed one (whose times
//     aren't believed), has every line placed by its byte offset between the
//     file's write bounds (writeSpan), as if its bytes were written at an even
//     pace. The newest file's upper bound is its last write, which for a live
//     stream is about now.
//   - Otherwise the lines before the first timed line are placed by offset
//     between the file's lower bound and that line's time, and the lines after
//     it keep what Parse gave them: the time of the timed line before them.
//   - A first timed line earlier than the lower bound is trusted (the
//     container's clock and Nomad's start event disagree by a second or two;
//     the bounds already refuse a time more than logline.Slack before it), and
//     the lower bound moves as far again before that line, never more than the
//     slack before the bound, so the lines before it stay before it.
//
// Nothing here uses the time of the read.
func placeLines(group []logline.Line, files []deploy.LogFile, fi int, started *time.Time, skewed bool) {
	if len(group) == 0 {
		return
	}
	f := files[fi]
	lo, hi := writeSpan(files, fi, started)
	first := -1
	if !skewed {
		first = slices.IndexFunc(group, func(l logline.Line) bool { return l.TimeSource == logline.FromLine })
	}
	if first < 0 {
		for i := range group {
			group[i].Time, group[i].TimeSource = between(lo, hi, group[i].ID.Offset, f.Size), logline.Estimated
		}
		return
	}
	t1, o1 := group[first].Time, group[first].ID.Offset
	if !lo.IsZero() && t1.Before(lo) {
		lo = laterOf(t1.Add(-lo.Sub(t1)), lo.Add(-logline.Slack))
	}
	for i := 0; i < first; i++ {
		group[i].Time, group[i].TimeSource = between(lo, t1, group[i].ID.Offset, o1), logline.Estimated
	}
}

// walkLogStream reads one stream of one copy backwards: from the end of its
// newest file in chunks of logChunk, into the file before when one is spent,
// as Nomad's own findClosest does for its end offset. files are the stream's
// files, oldest first, as LogFiles lists them. The walk stops at the first of:
//
//   - a chunk whose oldest line is older than lim.from (stopRange): the time
//     of the first timed line near its start or, while no timed line has been
//     read in its file, its first line's place by offset (placeLines);
//   - lim.budget bytes read (stopBudget);
//   - ctx ending (stopDeadline; the caller tells a deadline from a cancel);
//   - no file left (stopEnd), or a file Nomad purged under it (stopEnd with
//     dropped: it ends with what it has).
//
// A copy Nomad no longer has is deploy.ErrCopyNotFound; any other failure is
// returned wrapped. The lines it found are in the result either way a stop
// is not an error.
func (s *Service) walkLogStream(ctx context.Context, c deploy.Copy, files []deploy.LogFile, lim walkLimits) (streamRead, error) {
	res := streamRead{reason: stopEnd}
	var batches [][]rawLine // newest chunk first, each oldest first

	finished := true
walk:
	for i := len(files) - 1; i >= 0; i-- {
		f := files[i]
		// Every line of this file is newer than the previous file's last write,
		// so if that is past what the query wants, so is the whole file.
		if i > 0 && !lim.upper.IsZero() && files[i-1].ModTime.Add(-logline.Slack).After(lim.upper) {
			continue
		}
		bounds := fileBounds(files, i, c.StartedAt)
		lo, hi := writeSpan(files, i, c.StartedAt)
		sawTimed := false // a timed line was read in this file

		pos := f.Size
		var carry []byte    // the bytes from pos on that belong to a line begun before pos
		terminated := false // whether the end of what is read is known to be a line's end
		for pos > 0 {
			if res.read >= lim.budget {
				res.reason, finished = stopBudget, false
				break walk
			}
			start := max(int64(0), pos-logChunk)
			data, err := s.deployer.ReadLogAt(ctx, c.ID, f.Path, start, pos-start)
			if err != nil {
				switch {
				case errors.Is(err, deploy.ErrLogRotated):
					res.dropped, finished = true, false
					break walk
				case ctx.Err() != nil:
					res.reason, finished = stopDeadline, false
					break walk
				}
				return res, scrubReadError(err)
			}
			if int64(len(data)) < pos-start { // the file isn't as long as it was listed
				res.dropped, finished = true, false
				break walk
			}
			res.read += int64(len(data))

			buf := make([]byte, 0, len(data)+len(carry))
			buf = append(append(buf, data...), carry...)
			var batch []rawLine
			if start > 0 {
				// Up to the first newline is the end of a line that began
				// earlier: it waits for the next chunk.
				nl := bytes.IndexByte(buf, '\n')
				if nl < 0 {
					carry, pos = buf, start
					continue
				}
				batch = splitRawLines(buf[nl+1:], start+int64(nl)+1, i, terminated)
				carry, terminated = buf[:nl], true
			} else {
				batch = splitRawLines(buf, 0, i, terminated)
				carry = nil
			}
			pos = start
			batches = append(batches, batch)
			if lim.from.IsZero() || len(batch) == 0 {
				continue
			}
			oldest, ok := firstTimed(batch, bounds, logProbeLines)
			if ok {
				sawTimed = true
			} else if !sawTimed {
				// No time near the chunk's start, and none read in this file
				// yet: if the chunk has none at all, its lines are placed by
				// offset, and the walk goes by that.
				if _, timed := firstTimed(batch[min(len(batch), logProbeLines):], bounds, len(batch)); timed {
					sawTimed = true
				} else {
					oldest, ok = between(lo, hi, batch[0].offset, f.Size), true
				}
			}
			if ok && !oldest.IsZero() && oldest.Before(lim.from) {
				res.reason, finished = stopRange, false
				break walk
			}
		}
	}
	if finished && len(files) > 0 && files[0].Index > 0 {
		res.dropped = true // the files before the first one were purged
	}

	total := 0
	for _, b := range batches {
		total += len(b)
	}
	res.raws = make([]rawLine, 0, total)
	for i := len(batches) - 1; i >= 0; i-- {
		res.raws = append(res.raws, batches[i]...)
	}
	return res, nil
}

// firstTimed is the time of the first of the first n lines of batch that
// carries one, which the walk compares with the range's start.
func firstTimed(batch []rawLine, b logline.Bounds, n int) (time.Time, bool) {
	for i := 0; i < len(batch) && i < n; i++ {
		if t, _, ok := logline.TimeOf(logline.StripANSI(batch[i].text), b); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

// splitRawLines cuts seg, which begins at byte offset base of a file, into
// lines. The text after the last newline is a line when it isn't empty, or
// when endKnown says the end of seg is a line's end (its newline was cut
// off); otherwise it is nothing, because seg ended on a newline.
func splitRawLines(seg []byte, base int64, file int, endKnown bool) []rawLine {
	var out []rawLine
	off := 0
	for {
		i := bytes.IndexByte(seg[off:], '\n')
		if i < 0 {
			if rest := seg[off:]; len(rest) > 0 || endKnown {
				out = appendRawLine(out, rest, base+int64(off), file)
			}
			return out
		}
		out = appendRawLine(out, seg[off:off+i], base+int64(off), file)
		off += i + 1
	}
}

// appendRawLine adds one line: a trailing \r dropped, invalid UTF-8 replaced,
// and a line longer than logMaxLine cut where a character begins, into lines
// that each know their offset, as deploy.Logs cuts a runaway line.
func appendRawLine(out []rawLine, b []byte, offset int64, file int) []rawLine {
	b = bytes.TrimSuffix(b, []byte{'\r'})
	for len(b) > logMaxLine {
		cut := logMaxLine
		for cut > 0 && !utf8.RuneStart(b[cut]) {
			cut--
		}
		if cut == 0 { // not text at all
			cut = logMaxLine
		}
		out = append(out, rawLine{text: validLogText(b[:cut]), file: file, offset: offset})
		b, offset = b[cut:], offset+int64(cut)
	}
	return append(out, rawLine{text: validLogText(b), file: file, offset: offset})
}

func validLogText(b []byte) string {
	return strings.ToValidUTF8(string(b), "�")
}

// parseLogStream reads the raw lines of one stream, oldest first, into lines:
// logline.Parse for each, with the bounds of the line's file, then the skew
// check and placeLines per file. files are the stream's files and started the
// copy's first start, as walkLogStream got them.
//
// The skew check: the last timed line of a file was written at about the
// file's last write, so one that is more than a minute from the file's
// ModTime means the file's times can't be trusted (a container in a zone west
// of UTC, a clock set wrong), and every line of the file becomes Estimated,
// placed like the lines of a file with no time: by byte offset between the
// file's write bounds.
func parseLogStream(short string, stream logline.Stream, started *time.Time, files []deploy.LogFile, raws []rawLine) []logline.Line {
	lines := make([]logline.Line, 0, len(raws))
	var prev *logline.Line
	for g := 0; g < len(raws); {
		end := fileRunEnd(raws, g)
		fi := raws[g].file
		group := parseFileRun(short, stream, started, files, raws[g:end], prev)
		placeLines(group, files, fi, started, skewed(group, files[fi].ModTime))
		lines = append(lines, group...)
		last := lines[len(lines)-1]
		prev = &last
		g = end
	}
	return lines
}

// fileRunEnd is the end of the run of raws, from g on, that come from one file.
func fileRunEnd(raws []rawLine, g int) int {
	end := g
	for end < len(raws) && raws[end].file == raws[g].file {
		end++
	}
	return end
}

// parseFileRun is logline.Parse over a run of one file's lines, oldest first,
// after prev (the stream's line before them, or nil). The times of lines with
// none of their own are Parse's until placeLines settles them.
func parseFileRun(short string, stream logline.Stream, started *time.Time, files []deploy.LogFile, run []rawLine, prev *logline.Line) []logline.Line {
	fi := run[0].file
	f := files[fi]
	b := fileBounds(files, fi, started)
	out := make([]logline.Line, 0, len(run))
	for _, r := range run {
		id := logline.LineID{Copy: short, Stream: stream, File: f.Index, Offset: r.offset}
		l := logline.Parse(r.text, stream, id, b, prev)
		out = append(out, l)
		prev = &out[len(out)-1]
	}
	return out
}

// skewed says whether the last line of the group that carried its own time is
// more than logSkewLimit from the file's last write.
func skewed(group []logline.Line, mod time.Time) bool {
	if mod.IsZero() {
		return false
	}
	for i := len(group) - 1; i >= 0; i-- {
		if group[i].TimeSource == logline.FromLine {
			d := group[i].Time.Sub(mod)
			return d > logSkewLimit || d < -logSkewLimit
		}
	}
	return false
}

// logStream is one stream of one copy through the read.
type logStream struct {
	copy   logCopy
	stream logline.Stream
	files  []deploy.LogFile // this stream's, oldest first
	res    streamRead
	oldest time.Time // the time of the oldest line read, before the range cut; zero when none

	rows   []LogEntry // after the range, filters and folding; oldest first, sort non-decreasing
	stamps []logStamp
	lines  int // lines that passed the filters, before folding
}

// readLogStreams lists the files of every copy and walks every stream, a few
// at a time, then parses and filters each. parent is the request's context and
// ctx the one with the deadline; budget gives a stream's bytes for the number
// of streams read (the page's streamBudget, or a download's).
func (s *Service) readLogStreams(parent, ctx context.Context, q EnvLogQuery, win logWindow, copies []logCopy, budget func(streams int) int64, fold bool) ([]*logStream, error) {
	wanted := []logline.Stream{logline.Stdout, logline.Stderr}
	if q.Stream == logline.Stdout || q.Stream == logline.Stderr {
		wanted = []logline.Stream{q.Stream}
	}

	// The files of each copy: one listing holds both streams.
	type listing struct {
		files    []deploy.LogFile
		err      error
		deadline bool
	}
	listings := make([]listing, len(copies))
	slots := make(chan struct{}, logWalkWorkers)
	var wg sync.WaitGroup
	for i, c := range copies {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			files, err := s.deployer.LogFiles(ctx, c.ID, c.Service)
			switch {
			case err != nil && ctx.Err() != nil:
				listings[i].deadline = true
			case err != nil:
				listings[i].err = err
			default:
				listings[i].files = files
			}
		})
	}
	wg.Wait()
	if err := parent.Err(); err != nil {
		return nil, err
	}

	var streams []*logStream
	for i, c := range copies {
		l := listings[i]
		if l.err != nil {
			if errors.Is(l.err, deploy.ErrCopyNotFound) {
				continue // the copy went between the two calls
			}
			return nil, fmt.Errorf("read logs of copy %s: %w", c.short, l.err)
		}
		for _, st := range wanted {
			ls := &logStream{copy: c, stream: st}
			if l.deadline {
				ls.res.reason = stopDeadline
			}
			for _, f := range l.files {
				if f.Stream == st {
					ls.files = append(ls.files, f)
				}
			}
			streams = append(streams, ls)
		}
	}

	lim := walkLimits{budget: budget(len(streams)), from: win.from, upper: win.upper()}
	errs := make([]error, len(streams))
	for i, ls := range streams {
		if ls.res.reason == stopDeadline {
			continue
		}
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			res, err := s.walkLogStream(ctx, ls.copy.Copy, ls.files, lim)
			ls.res, errs[i] = res, err
		})
	}
	wg.Wait()
	if err := parent.Err(); err != nil {
		return nil, err
	}

	// A copy that went while its streams were walked is out of every stream.
	var hard error
	gone := map[string]bool{} // allocation IDs
	for i, ls := range streams {
		err := errs[i]
		switch {
		case err == nil:
		case errors.Is(err, deploy.ErrCopyNotFound):
			gone[ls.copy.ID] = true
		case hard == nil || errors.Is(err, deploy.ErrNomadUnavailable):
			hard = fmt.Errorf("read logs of copy %s: %w", ls.copy.short, err)
		}
	}
	if hard != nil {
		return nil, hard
	}
	out := streams[:0:0]
	for _, ls := range streams {
		if !gone[ls.copy.ID] {
			out = append(out, ls)
		}
	}

	filter := q.filter()
	for _, ls := range out {
		finishLogStream(ls, win, filter, fold)
	}
	return out, nil
}

// finishLogStream parses a walked stream and takes the lines in the window
// through the level and text filters and folding. It fills rows (oldest
// first), stamps, lines and oldest.
//
// The order matters: the sort times are made non-decreasing over every line of
// the window, so a filter never moves a line; the filters come before
// folding, so a search for 502 folds the 502s and a line between two equal
// ones that the filter removes doesn't keep them apart. With fold false (the
// download) every kept line is its own row, with its own time and ID.
func finishLogStream(ls *logStream, win logWindow, filter logFilter, fold bool) {
	short := ls.copy.short
	lines := parseLogStream(short, ls.stream, ls.copy.StartedAt, ls.files, ls.res.raws)
	ls.res.raws = nil
	if len(lines) > 0 {
		ls.oldest = lines[0].Time
	}

	var kept []logline.Line
	var sorts []time.Time
	var run time.Time
	for _, l := range lines {
		if !win.has(l.Time) {
			continue
		}
		run = laterOf(run, l.Time)
		if !filter.keeps(l, ls.copy.app, ls.copy.Service) {
			continue
		}
		kept = append(kept, l)
		sorts = append(sorts, run)
		ls.stamps = append(ls.stamps, logStamp{Time: l.Time, Level: l.Level, Estimated: l.TimeSource == logline.Estimated})
	}
	ls.lines = len(kept)

	if !fold {
		ls.rows = make([]LogEntry, 0, len(kept))
		for j, l := range kept {
			ls.rows = append(ls.rows, LogEntry{Line: l, App: ls.copy.app, Service: ls.copy.Service, Copy: short, sort: sorts[j]})
		}
		return
	}
	folded := logline.Fold(kept)
	ls.rows = make([]LogEntry, 0, len(folded))
	j := 0
	for _, l := range folded {
		// A folded row is its group's newest line, whose ID is unique.
		for kept[j].ID != l.ID {
			j++
		}
		ls.rows = append(ls.rows, LogEntry{Line: l, App: ls.copy.app, Service: ls.copy.Service, Copy: short, sort: sorts[j]})
		j++
	}
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// mergeNewestFirst merges streams, each oldest first with non-decreasing sort
// times, into one list, newest first, of at most limit rows (limit <= 0: all).
// A stream's own order never changes; rows with equal sort times come from the
// earlier stream first.
func mergeNewestFirst(streams [][]LogEntry, limit int) []LogEntry {
	total := 0
	for _, s := range streams {
		total += len(s)
	}
	if limit <= 0 || limit > total {
		limit = total
	}
	next := make([]int, len(streams)) // each stream's newest row not yet taken
	for i, s := range streams {
		next[i] = len(s) - 1
	}
	out := make([]LogEntry, 0, limit)
	for len(out) < limit {
		best := -1
		for i, s := range streams {
			if next[i] < 0 {
				continue
			}
			if best < 0 || s[next[i]].sort.After(streams[best][next[best]].sort) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		out = append(out, streams[best][next[best]])
		next[best]--
	}
	return out
}

// assembleLogRead puts the streams together: the merged rows, the coverage,
// the rail and the stamps.
func assembleLogRead(q EnvLogQuery, win logWindow, streams []*logStream) LogRead {
	lines := q.Lines
	if lines == 0 {
		lines = logDefaultLines
	}
	rows := make([][]LogEntry, len(streams))
	read := LogRead{From: win.from, To: win.to}
	read.Coverage = LogCoverage{From: win.from, Complete: true, Timed: true}

	var newestEdge time.Time // the newest of the oldest times reached by streams that stopped early
	var copies []LogCopy
	at := map[string]int{} // allocation ID → position in copies
	for i, ls := range streams {
		rows[i] = ls.rows
		read.stamps = append(read.stamps, ls.stamps...)
		if ls.res.dropped {
			read.Dropped = true
		}
		switch ls.res.reason {
		case stopBudget, stopDeadline:
			read.Coverage.Complete = false
			if ls.res.reason == stopDeadline {
				read.Coverage.Timed = false
			}
			edge := ls.oldest
			if edge.IsZero() {
				edge = win.upper() // nothing was read: nothing is known
			}
			newestEdge = laterOf(newestEdge, edge)
		}
		n, ok := at[ls.copy.ID]
		if !ok {
			n = len(copies)
			at[ls.copy.ID] = n
			copies = append(copies, LogCopy{ID: ls.copy.ID, Short: ls.copy.short, App: ls.copy.app,
				Service: ls.copy.Service, Gone: ls.copy.Gone})
		}
		copies[n].Lines += ls.lines
	}
	if !read.Coverage.Complete && newestEdge.After(win.from) {
		read.Coverage.From = newestEdge
	}
	read.Lines = mergeNewestFirst(rows, lines)
	read.Copies = copies
	return read
}

// scrubReadError keeps what Nomad said out of an error when it might be
// content: deploy.ReadLogAt takes a body of the wrong length for Nomad's
// message and quotes its start, and a file cut short while it was read gives
// a body that is the app's own output.
func scrubReadError(err error) error {
	if !errors.Is(err, deploy.ErrNomadUnavailable) || !strings.Contains(err.Error(), "Nomad answered:") {
		return err
	}
	return fmt.Errorf("%w: Nomad answered something unexpected while its output was read", deploy.ErrNomadUnavailable)
}
