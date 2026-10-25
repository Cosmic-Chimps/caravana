package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/logline"
)

// "Lines around this" (feature 024, research R4): the lines near one line of
// one copy, whatever their level, and the other stream of that copy over the
// same stretch of time. A line is addressed by its id (copy, stream, file,
// offset), which stays good until Nomad purges its file; a time from
// elsewhere (an alert's) is turned into an id by LogsAroundTime.
//
// Like the page read, nothing here writes a line to a logger or an error.

const (
	// logAroundWindow is how many bytes before and after the anchor are read,
	// crossing into the file before or after when the window passes an edge.
	logAroundWindow = 16 << 10
	logAroundLines  = 50  // lines before and after the anchor, by default
	logAroundMax    = 200 // and at most
	// logProbe is the size of one readat of the search by time, and
	// logProbeSteps how many more chunks a probe that found no timed line
	// reads ahead before it gives up.
	logProbe      = 4 << 10
	logProbeSteps = 4
)

// ErrLogRotated is a line whose file Nomad has purged (the API's 404
// log_rotated). It is the deploy layer's error, so either matches.
var ErrLogRotated = deploy.ErrLogRotated

// LogAround is the answer of LogsAround: the lines, newest first, and the id
// of the line that was asked for.
type LogAround struct {
	LogRead
	Anchor logline.LineID
}

// LogsAround reads the n lines before and the n lines after the line at, in
// its stream of the copy, with every level and no filter, and merges into
// them the copy's other stream over the same span of time. n is 1 to 200; 0
// means 50. at.Copy is ignored: the copy is copyRef, an allocation ID or its
// first 8 characters, of any app.
//
// The anchor is the line at at.Offset, or the line that contains it when it
// isn't where a line starts. The bytes read are logAroundWindow before and
// after it (the lines of a window that holds fewer than n are all there are),
// continuing into the neighboring file when the window passes an edge.
//
// Errors: a *app.FieldError on at or lines for a bad address; ErrLogCopyNotFound
// when Nomad has no such copy; ErrLogRotated when the file was purged; Nomad
// not readable matches deploy.ErrNomadUnavailable.
func (s *Service) LogsAround(ctx context.Context, copyRef string, at logline.LineID, n int) (LogAround, error) {
	switch {
	case n < 0 || n > logAroundMax:
		return LogAround{}, app.NewFieldError("lines", "must be between 1 and %d", logAroundMax)
	case n == 0:
		n = logAroundLines
	}
	if err := checkAroundID(at); err != nil {
		return LogAround{}, err
	}
	c, err := s.resolveLogCopy(ctx, copyRef)
	if err != nil {
		return LogAround{}, err
	}
	byStream, err := s.copyStreamFiles(ctx, c)
	if err != nil {
		return LogAround{}, err
	}
	files := byStream[at.Stream]
	idx := slices.IndexFunc(files, func(f deploy.LogFile) bool { return f.Index == at.File })
	switch {
	case idx >= 0:
	case len(files) == 0 || at.File < files[len(files)-1].Index:
		return LogAround{}, fmt.Errorf("copy %s: %s file %d: %w", c.short, at.Stream, at.File, ErrLogRotated)
	default:
		return LogAround{}, app.NewFieldError("at", "the copy has no %s file %d yet", at.Stream, at.File)
	}
	f := files[idx]
	if at.Offset >= f.Size {
		return LogAround{}, app.NewFieldError("at", "is past the end of the file")
	}

	raws, err := s.aroundRaws(ctx, c, files, idx, at.Offset)
	if err != nil {
		return LogAround{}, err
	}
	lines, err := s.parseWindow(ctx, c, at.Stream, files, raws)
	if err != nil {
		return LogAround{}, err
	}
	anchor := -1
	for i, l := range lines {
		if l.ID.File == at.File && l.ID.Offset <= at.Offset {
			anchor = i
		}
	}
	if anchor < 0 {
		return LogAround{}, app.NewFieldError("at", "is not in the file's text")
	}
	sel := lines[max(0, anchor-n):min(len(lines), anchor+n+1)]
	span0, span1 := sel[0].Time, sel[0].Time
	for _, l := range sel {
		if l.Time.Before(span0) {
			span0 = l.Time
		}
		if l.Time.After(span1) {
			span1 = l.Time
		}
	}
	rows := entriesOf(c, sel)

	otherStream := logline.Stdout
	if at.Stream == logline.Stdout {
		otherStream = logline.Stderr
	}
	others, err := s.otherStreamLines(ctx, c, otherStream, byStream[otherStream], span0, span1)
	if err != nil {
		return LogAround{}, err
	}

	merged := mergeNewestFirst([][]LogEntry{rows, others}, 0)
	return LogAround{
		LogRead: LogRead{
			Lines:    merged,
			Coverage: LogCoverage{From: span0, Complete: true, Timed: true},
			Copies: []LogCopy{{ID: c.ID, Short: c.short, App: c.app, Service: c.Service,
				Gone: c.Gone, Lines: len(merged)}},
			From: span0, To: span1,
		},
		Anchor: lines[anchor].ID,
	}, nil
}

// checkAroundID checks the parts of an address a read can't make sense of.
func checkAroundID(at logline.LineID) error {
	if at.Stream != logline.Stdout && at.Stream != logline.Stderr {
		return app.NewFieldError("at", "the stream is stdout or stderr")
	}
	if at.File < 0 || at.Offset < 0 {
		return app.NewFieldError("at", "the file and the offset are not negative")
	}
	return nil
}

// entriesOf makes rows of lines (oldest first, one stream, one copy): no
// folding, and sort times made non-decreasing so a line never moves ahead of
// the one before it.
func entriesOf(c logCopy, lines []logline.Line) []LogEntry {
	rows := make([]LogEntry, 0, len(lines))
	var run time.Time
	for _, l := range lines {
		run = laterOf(run, l.Time)
		rows = append(rows, LogEntry{Line: l, App: c.app, Service: c.Service, Copy: c.short, sort: run})
	}
	return rows
}

// resolveLogCopy finds the copy, of any app, whose allocation ID is ref or
// starts with it (8 characters). One call lists the copies of every app to
// find it; the app is then the one with its service, asking each candidate's
// copies only when two apps have a service of that name.
func (s *Service) resolveLogCopy(ctx context.Context, ref string) (logCopy, error) {
	if ref == "" {
		return logCopy{}, app.NewFieldError("copy", "is required")
	}
	recs, err := s.store.ListApps(ctx)
	if err != nil {
		return logCopy{}, fmt.Errorf("read logs: %w", err)
	}
	ids := make([]string, len(recs))
	for i, r := range recs {
		ids[i] = r.App.ID
	}
	copies, err := s.deployer.LogCopies(ctx, ids)
	if err != nil {
		return logCopy{}, fmt.Errorf("read logs: %w", err)
	}
	i := slices.IndexFunc(copies, func(c deploy.Copy) bool { return c.ID == ref })
	if i < 0 {
		i = slices.IndexFunc(copies, func(c deploy.Copy) bool { return shortCopy(c.ID) == ref })
	}
	if i < 0 {
		return logCopy{}, fmt.Errorf("copy %q: %w", ref, ErrLogCopyNotFound)
	}
	c := copies[i]

	var candidates []string
	for _, r := range recs {
		if slices.ContainsFunc(r.App.Services, func(sv app.Service) bool { return sv.Name == c.Service }) {
			candidates = append(candidates, r.App.ID)
		}
	}
	switch len(candidates) {
	case 0:
		return logCopy{}, fmt.Errorf("copy %q: %w", ref, ErrLogCopyNotFound)
	case 1:
		return logCopy{Copy: c, app: candidates[0], short: shortCopy(c.ID)}, nil
	}
	for _, id := range candidates {
		own, err := s.deployer.LogCopies(ctx, []string{id})
		if err != nil {
			return logCopy{}, fmt.Errorf("read logs: %w", err)
		}
		if slices.ContainsFunc(own, func(o deploy.Copy) bool { return o.ID == c.ID }) {
			return logCopy{Copy: c, app: id, short: shortCopy(c.ID)}, nil
		}
	}
	return logCopy{}, fmt.Errorf("copy %q: %w", ref, ErrLogCopyNotFound)
}

// copyStreamFiles lists the copy's output files, by stream, oldest first.
func (s *Service) copyStreamFiles(ctx context.Context, c logCopy) (map[logline.Stream][]deploy.LogFile, error) {
	all, err := s.deployer.LogFiles(ctx, c.ID, c.Service)
	if err != nil {
		if errors.Is(err, deploy.ErrCopyNotFound) {
			return nil, fmt.Errorf("copy %q: %w", c.short, ErrLogCopyNotFound)
		}
		return nil, fmt.Errorf("read logs of copy %s: %w", c.short, err)
	}
	out := map[logline.Stream][]deploy.LogFile{}
	for _, f := range all {
		out[f.Stream] = append(out[f.Stream], f)
	}
	return out, nil
}

// readLogBytes is deploy.ReadLogAt with its errors in the service's words.
func (s *Service) readLogBytes(ctx context.Context, c logCopy, f deploy.LogFile, from, limit int64) ([]byte, error) {
	data, err := s.deployer.ReadLogAt(ctx, c.ID, f.Path, from, limit)
	switch {
	case err == nil:
		return data, nil
	case errors.Is(err, deploy.ErrCopyNotFound):
		return nil, fmt.Errorf("copy %q: %w", c.short, ErrLogCopyNotFound)
	case errors.Is(err, deploy.ErrLogRotated):
		return nil, fmt.Errorf("copy %s: %s file %d: %w", c.short, f.Stream, f.Index, ErrLogRotated)
	case ctx.Err() != nil:
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("read logs of copy %s: %w", c.short, scrubReadError(err))
}

// aroundRaws reads the bytes around offset in files[idx] and, when the window
// passes the file's start or end, the tail of the file before or the head of
// the file after (if it is the next index). The lines come oldest first, with
// file as a position in files.
func (s *Service) aroundRaws(ctx context.Context, c logCopy, files []deploy.LogFile, idx int, offset int64) ([]rawLine, error) {
	f := files[idx]
	var out []rawLine

	if deficit := logAroundWindow - offset; deficit > 0 && idx > 0 && files[idx-1].Index == f.Index-1 {
		prev := files[idx-1]
		raws, err := s.windowLines(ctx, c, files, idx-1, max(0, prev.Size-deficit), prev.Size, -1)
		switch {
		case err == nil:
			out = append(out, raws...)
		case !errors.Is(err, ErrLogRotated): // a neighbor purged meanwhile is just not there
			return nil, err
		}
	}

	main, err := s.windowLines(ctx, c, files, idx, max(0, offset-logAroundWindow), min(f.Size, offset+logAroundWindow), offset)
	if err != nil {
		return nil, err
	}
	out = append(out, main...)

	if need := offset + logAroundWindow - f.Size; need > 0 && idx+1 < len(files) && files[idx+1].Index == f.Index+1 {
		next := files[idx+1]
		raws, err := s.windowLines(ctx, c, files, idx+1, 0, min(next.Size, need), -1)
		switch {
		case err == nil:
			out = append(out, raws...)
		case !errors.Is(err, ErrLogRotated):
			return nil, err
		}
	}
	return out, nil
}

// windowLines reads files[idx] from byte from to byte to and returns its whole
// lines. A line the window starts in the middle of is left out (the byte
// before from tells), and so is one it ends in the middle of, unless the file
// ends there. anchor is the offset of the line the caller asks for (or -1):
// when it lies in a first or last line too long for the window, that line is
// kept as it is, so the anchor always has a line.
func (s *Service) windowLines(ctx context.Context, c logCopy, files []deploy.LogFile, idx int, from, to, anchor int64) ([]rawLine, error) {
	f := files[idx]
	readFrom := from
	if from > 0 {
		readFrom = from - 1 // the byte before: a newline means from is where a line starts
	}
	data, err := s.readLogBytes(ctx, c, f, readFrom, to-readFrom)
	if err != nil {
		return nil, err
	}
	end := readFrom + int64(len(data))
	if len(data) == 0 {
		return nil, nil
	}

	start := 0
	if from > 0 {
		switch nl := bytes.IndexByte(data, '\n'); {
		case data[0] == '\n':
			start = 1
		case nl >= 0:
			start = nl + 1
		default:
			start = len(data)
		}
	}
	rel := int(anchor - readFrom)
	if anchor >= 0 && start > rel {
		start = max(rel, 0)
	}
	stop := len(data)
	if end < f.Size {
		if last := bytes.LastIndexByte(data, '\n'); last >= start && !(anchor >= 0 && last < rel) {
			stop = last + 1
		}
	}
	if start >= stop {
		return nil, nil
	}
	return splitRawLines(data[start:stop], readFrom+int64(start), idx, false), nil
}

// otherStreamLines reads the lines of the copy's other stream whose time is in
// [t0, t1]: the file whose last write is the first at or after t0 (a minute of
// slack), from the offset the search by time finds for t0, up to twice the
// window in bytes, on into the next file if the budget allows.
func (s *Service) otherStreamLines(ctx context.Context, c logCopy, stream logline.Stream, files []deploy.LogFile, t0, t1 time.Time) ([]LogEntry, error) {
	i := slices.IndexFunc(files, func(f deploy.LogFile) bool {
		return f.ModTime.IsZero() || !f.ModTime.Add(logline.Slack).Before(t0)
	})
	if i < 0 {
		return nil, nil
	}
	if i > 0 && !files[i-1].ModTime.IsZero() && files[i-1].ModTime.Add(-logline.Slack).After(t1) {
		return nil, nil // everything in this file is newer than the span
	}
	off, err := s.seekTime(ctx, c, files, i, t0)
	if err != nil {
		if errors.Is(err, ErrLogRotated) {
			return nil, nil
		}
		return nil, err
	}

	budget := int64(2 * logAroundWindow)
	var raws []rawLine
	for k := i; k < len(files) && budget > 0; k++ {
		from := int64(0)
		if k == i {
			from = off
		}
		to := min(files[k].Size, from+budget)
		if to <= from {
			continue
		}
		got, err := s.windowLines(ctx, c, files, k, from, to, -1)
		if err != nil {
			if errors.Is(err, ErrLogRotated) {
				break
			}
			return nil, err
		}
		raws = append(raws, got...)
		budget -= to - from
		if to < files[k].Size {
			break
		}
	}
	parsed, err := s.parseWindow(ctx, c, stream, files, raws)
	if err != nil {
		return nil, err
	}
	var kept []logline.Line
	for _, l := range parsed {
		if !l.Time.Before(t0) && !l.Time.After(t1) {
			kept = append(kept, l)
		}
	}
	return entriesOf(c, kept), nil
}

// LogsAroundTime turns a time into the address of a line: the first line of
// the copy's stream at or after t, so that LogsAround can read around it. It
// is the search of plan.md:
//
//   - The file is chosen from the listing, with no read: the newest file whose
//     previous file's last write is not after t.
//   - Probes of 4 KiB by offset then halve the file: each takes the time of
//     the first whole line with one, until the window is one probe, and the
//     window is read once to find the line. A 100 MiB file takes 12 probes.
//   - A file with no timed line in its first probe (an app that prints no
//     times) is placed by proportion: t between the file's start and last
//     write, as a share of its bytes.
//
// A copy Nomad doesn't have is ErrLogCopyNotFound; a stream with no output
// file is ErrLogRotated.
func (s *Service) LogsAroundTime(ctx context.Context, copyRef string, stream logline.Stream, t time.Time) (logline.LineID, error) {
	if stream != logline.Stdout && stream != logline.Stderr {
		return logline.LineID{}, app.NewFieldError("stream", "use stdout or stderr")
	}
	c, err := s.resolveLogCopy(ctx, copyRef)
	if err != nil {
		return logline.LineID{}, err
	}
	byStream, err := s.copyStreamFiles(ctx, c)
	if err != nil {
		return logline.LineID{}, err
	}
	files := byStream[stream]
	if len(files) == 0 {
		return logline.LineID{}, fmt.Errorf("copy %s: no %s output: %w", c.short, stream, ErrLogRotated)
	}
	i := len(files) - 1
	for i > 0 && files[i-1].ModTime.After(t) {
		i--
	}
	off, err := s.seekTime(ctx, c, files, i, t)
	if err != nil {
		return logline.LineID{}, err
	}
	return logline.LineID{Copy: c.short, Stream: stream, File: files[i].Index, Offset: off}, nil
}

// seekTime is the search above inside files[i]: the offset of the first line
// at or after t (the last line when every line is older).
func (s *Service) seekTime(ctx context.Context, c logCopy, files []deploy.LogFile, i int, t time.Time) (int64, error) {
	f := files[i]
	if f.Size == 0 {
		return 0, nil
	}
	bounds := fileBounds(files, i, c.StartedAt)

	_, first, ok, err := s.probeAhead(ctx, c, f, bounds, 0)
	if err != nil {
		return 0, err
	}
	if !ok {
		return s.placeByProportion(ctx, c, files, i, t)
	}
	if !first.Before(t) {
		return 0, nil
	}

	lo, hi := int64(0), f.Size // the answer is in [lo, hi]; lo's line is older than t
	for hi-lo > logProbe {
		mid := lo + (hi-lo)/2
		off, tm, ok, err := s.probeAhead(ctx, c, f, bounds, mid)
		if err != nil {
			return 0, err
		}
		switch {
		case !ok || !tm.Before(t):
			hi = mid
		default:
			lo = min(off, hi)
		}
	}

	data, err := s.readLogBytes(ctx, c, f, lo, min(f.Size, hi+logProbe)-lo)
	if err != nil {
		return 0, err
	}
	atEOF := lo+int64(len(data)) >= f.Size
	raws := splitRawLines(data, lo, i, false)
	if !atEOF && len(raws) > 0 && !bytes.HasSuffix(data, []byte{'\n'}) {
		raws = raws[:len(raws)-1] // cut by the read
	}
	lines, err := s.parseWindow(ctx, c, f.Stream, files, raws)
	if err != nil {
		return 0, err
	}
	for _, l := range lines {
		if !l.Time.Before(t) {
			return l.ID.Offset, nil
		}
	}
	if len(lines) > 0 {
		return lines[len(lines)-1].ID.Offset, nil
	}
	return lo, nil
}

// probeAhead is probeTimed at off and, when the probe holds no timed line, at
// the next chunks, up to logProbeSteps more.
func (s *Service) probeAhead(ctx context.Context, c logCopy, f deploy.LogFile, b logline.Bounds, off int64) (int64, time.Time, bool, error) {
	for k := int64(0); k <= logProbeSteps; k++ {
		at := off + k*logProbe
		if at >= f.Size {
			break
		}
		lineOff, tm, ok, err := s.probeTimed(ctx, c, f, b, at)
		if err != nil || ok {
			return lineOff, tm, ok, err
		}
	}
	return 0, time.Time{}, false, nil
}

// probeTimed reads logProbe bytes at off and returns the offset and time of
// the first whole line in them that carries a time. A line is whole when its
// first byte is known to start a line (the byte before off is a newline, or
// off is 0) and its end is in the probe (or the file ends).
func (s *Service) probeTimed(ctx context.Context, c logCopy, f deploy.LogFile, b logline.Bounds, off int64) (int64, time.Time, bool, error) {
	from, want := off, int64(logProbe)
	if off > 0 {
		from, want = off-1, logProbe+1
	}
	data, err := s.readLogBytes(ctx, c, f, from, want)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	atEOF := int64(len(data)) < want
	pos := 0
	if off > 0 {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			return 0, time.Time{}, false, nil
		}
		pos = nl + 1
	}
	for pos < len(data) {
		end := bytes.IndexByte(data[pos:], '\n')
		if end < 0 {
			if !atEOF {
				break // the probe cut this line
			}
			end = len(data) - pos
		}
		text := validLogText(bytes.TrimSuffix(data[pos:pos+end], []byte{'\r'}))
		if tm, _, ok := logline.TimeOf(logline.StripANSI(text), b); ok {
			return from + int64(pos), tm.UTC(), true, nil
		}
		pos += end + 1
	}
	return 0, time.Time{}, false, nil
}

// placeByProportion is the search for a file that carries no times: t as a
// share of the file's life, from the previous file's last write (or the
// copy's start) to its own, taken as a share of its bytes, moved back to where its
// line starts.
func (s *Service) placeByProportion(ctx context.Context, c logCopy, files []deploy.LogFile, i int, t time.Time) (int64, error) {
	f := files[i]
	var begin time.Time
	switch {
	case i > 0:
		begin = files[i-1].ModTime
	case c.StartedAt != nil:
		begin = *c.StartedAt
	}
	span := f.ModTime.Sub(begin)
	if begin.IsZero() || f.ModTime.IsZero() || span <= 0 {
		return 0, nil
	}
	share := float64(t.Sub(begin)) / float64(span)
	share = min(max(share, 0), 1)
	off := min(int64(share*float64(f.Size)), f.Size-1)
	if off <= 0 {
		return 0, nil
	}
	// The line that holds off starts after the last newline before it.
	lo := max(0, off-logProbe)
	data, err := s.readLogBytes(ctx, c, f, lo, off-lo)
	if err != nil {
		return 0, err
	}
	if nl := bytes.LastIndexByte(data, '\n'); nl >= 0 {
		return lo + int64(nl) + 1, nil
	}
	if lo == 0 {
		return 0, nil
	}
	return off, nil // a line longer than a probe: its middle, as good as any
}

// parseWindow is parseLogStream for lines read from the middle of files. The
// skew check of parseLogStream compares a file's last timed line with its last
// write, which a window that stops short of the end can't do, so here a file is
// checked by its own tail: a skewed file (research R2) has every line of the
// window estimated, as in the page read.
func (s *Service) parseWindow(ctx context.Context, c logCopy, stream logline.Stream, files []deploy.LogFile, raws []rawLine) ([]logline.Line, error) {
	lines := make([]logline.Line, 0, len(raws))
	var prev logline.Line
	havePrev := false
	for g := 0; g < len(raws); {
		fi := raws[g].file
		end := g
		for end < len(raws) && raws[end].file == fi {
			end++
		}
		f := files[fi]
		b := fileBounds(files, fi, c.StartedAt)
		fallback := f.ModTime
		if !b.NotBefore.IsZero() {
			fallback = b.NotBefore.Add(logline.Slack)
		}
		first := len(lines)
		for _, r := range raws[g:end] {
			id := logline.LineID{Copy: c.short, Stream: stream, File: f.Index, Offset: r.offset}
			var p *logline.Line
			if havePrev {
				p = &prev
			}
			l := logline.Parse(r.text, stream, id, b, p)
			if l.Time.IsZero() {
				l.Time = fallback
			}
			lines = append(lines, l)
			prev, havePrev = l, true
		}

		group := lines[first:]
		last := raws[end-1]
		var skew bool
		if last.offset+int64(len(last.text)) >= f.Size-2 { // the window reaches the file's end
			skew = skewed(group, f.ModTime)
		} else {
			var err error
			if skew, err = s.tailSkewed(ctx, c, f, b); err != nil {
				return nil, err
			}
		}
		if skew {
			anchor := fallback
			if first > 0 {
				anchor = lines[first-1].Time
			}
			for i := range group {
				group[i].Time, group[i].TimeSource = anchor, logline.Estimated
			}
			prev = lines[len(lines)-1]
		}
		g = end
	}
	return lines, nil
}

// tailSkewed reads the last probe of a file and says whether its last timed
// line is more than logSkewLimit from the file's last write.
func (s *Service) tailSkewed(ctx context.Context, c logCopy, f deploy.LogFile, b logline.Bounds) (bool, error) {
	if f.ModTime.IsZero() || f.Size == 0 {
		return false, nil
	}
	from := max(0, f.Size-logProbe)
	data, err := s.readLogBytes(ctx, c, f, from, f.Size-from)
	if err != nil {
		if errors.Is(err, ErrLogRotated) {
			return false, nil
		}
		return false, err
	}
	pos := 0
	if from > 0 {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			return false, nil
		}
		pos = nl + 1
	}
	var group []logline.Line
	for _, raw := range splitRawLines(data[pos:], from+int64(pos), 0, false) {
		if tm, _, ok := logline.TimeOf(logline.StripANSI(raw.text), b); ok {
			group = append(group, logline.Line{Time: tm.UTC(), TimeSource: logline.FromLine})
		}
	}
	return skewed(group, f.ModTime), nil
}
