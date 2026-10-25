package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/logline"
)

// The log files of a copy (feature 024): Nomad's logmon keeps a task's output
// in alloc/logs/<task>.<stdout|stderr>.<n>, n growing from 0 as each file
// fills, and purges the oldest. The service reads them backwards from the
// newest with the three calls here and parses the lines itself (package
// logline); nothing in this file looks at a line.

// ErrLogRotated: the file asked for is no longer there, because Nomad purged
// it as newer output pushed it out (or the copy's directory was collected).
// The address of a line in it stays well-formed but can't be read again.
var ErrLogRotated = errors.New("log file rotated away")

const (
	// logFilesDir is where a copy's task output is, inside its allocation.
	logFilesDir = "alloc/logs"
	// maxLogRead bounds one ReadLogAt, whatever the caller asks.
	maxLogRead = 16 << 20
	// maxFSMessage is how much of a body is read to find out that it is
	// Nomad's error message and not file content.
	maxFSMessage = 1024
)

// LogFile is one file of a copy's output.
type LogFile struct {
	Stream  logline.Stream
	Index   int       // the n of <task>.<stream>.<n>; higher is newer
	Size    int64     // bytes
	ModTime time.Time // when it was last written
	Path    string    // alloc/logs/<task>.<stream>.<n>, what ReadLogAt takes
}

// LogFiles lists the output files of task in the copy, ordered by stream
// (stdout, then stderr) and then index, oldest first. Files that aren't the
// task's output are left out: the hidden FIFOs logmon writes through
// (.<task>.<stream>.fifo) and the files of the copy's other tasks.
//
// A copy Nomad doesn't have is ErrCopyNotFound. A copy that has written
// nothing yet has no files: an empty list and nil. A token without the read-fs
// capability is ErrNomadUnavailable and says to run the installer again.
func (d *Deployer) LogFiles(ctx context.Context, copyID, task string) ([]LogFile, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	alloc, err := d.logAlloc(ctx, copyID)
	if err != nil {
		return nil, err
	}
	infos, _, err := d.nomad.AllocFS().List(alloc, logFilesDir, q)
	if err != nil {
		if statusOf(err) == http.StatusNotFound && !strings.Contains(errorBody(err), "Unknown allocation") {
			return nil, nil // the directory isn't there yet
		}
		return nil, d.logFSError(ctx, "list the output files of copy "+copyID, copyID, err)
	}
	var files []LogFile
	for _, fi := range infos {
		if fi == nil || fi.IsDir {
			continue
		}
		stream, index, ok := parseLogFileName(task, fi.Name)
		if !ok {
			continue
		}
		files = append(files, LogFile{Stream: stream, Index: index, Size: fi.Size, ModTime: fi.ModTime,
			Path: logFilesDir + "/" + fi.Name})
	}
	slices.SortFunc(files, func(a, b LogFile) int {
		if a.Stream != b.Stream {
			if a.Stream == logline.Stdout { // stdout before stderr
				return -1
			}
			return 1
		}
		return a.Index - b.Index
	})
	return files, nil
}

// parseLogFileName reads "<task>.<stdout|stderr>.<n>", with n only digits.
func parseLogFileName(task, name string) (logline.Stream, int, bool) {
	rest, ok := strings.CutPrefix(name, task+".")
	if !ok {
		return "", 0, false
	}
	stream, n, ok := strings.Cut(rest, ".")
	if !ok || n == "" || strings.Trim(n, "0123456789") != "" {
		return "", 0, false
	}
	if stream != string(logline.Stdout) && stream != string(logline.Stderr) {
		return "", 0, false
	}
	index, err := strconv.Atoi(n)
	if err != nil {
		return "", 0, false
	}
	return logline.Stream(stream), index, true
}

// ReadLogAt reads up to limit bytes of a log file of the copy from offset, as
// Nomad's readat does, and returns what it read: fewer than limit at the end
// of the file, and an empty slice (with nil) at or past it. path is one of
// LogFiles' Paths: anything outside alloc/logs is refused.
//
// A file Nomad no longer has is ErrLogRotated and a copy it doesn't have is
// ErrCopyNotFound. A token without read-fs is ErrNomadUnavailable.
//
// The lab showed (research R8) that Nomad answers readat's errors, a missing
// file among them, as the message in the body of a 200 and not as a status.
// A body can't tell them apart from content by itself, so the file is statted
// first (which answers a real 404) to know its size, the read is cut to what
// the file holds (it only grows), and a body of any other length is taken for
// Nomad's message.
func (d *Deployer) ReadLogAt(ctx context.Context, copyID, path string, offset, limit int64) ([]byte, error) {
	if !validLogPath(path) {
		return nil, fmt.Errorf("read log %q of copy %s: not a file in %s", path, copyID, logFilesDir)
	}
	if offset < 0 {
		return nil, fmt.Errorf("read log %q of copy %s: negative offset", path, copyID)
	}
	if limit <= 0 {
		return nil, nil
	}
	limit = min(limit, maxLogRead)

	q := (&api.QueryOptions{}).WithContext(ctx)
	alloc, err := d.logAlloc(ctx, copyID)
	if err != nil {
		return nil, err
	}
	action := "read output of copy " + copyID
	info, _, err := d.nomad.AllocFS().Stat(alloc, path, q)
	if err != nil {
		return nil, d.logFSError(ctx, action, copyID, err)
	}
	if offset >= info.Size {
		return nil, nil
	}
	want := min(limit, info.Size-offset)

	r, err := d.nomad.AllocFS().ReadAt(alloc, path, offset, want, q)
	if err != nil {
		return nil, d.logFSError(ctx, action, copyID, err)
	}
	defer r.Close()
	body, err := io.ReadAll(io.LimitReader(r, max(want+1, maxFSMessage)))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, d.callError(action, err)
	}
	if int64(len(body)) != want {
		return nil, d.logMessageError(action, copyID, string(body))
	}
	return body, nil
}

// validLogPath says whether path is a file directly in alloc/logs.
func validLogPath(path string) bool {
	name, ok := strings.CutPrefix(path, logFilesDir+"/")
	return ok && name != "" && !strings.ContainsAny(name, "/\\\x00") && name != ".." && name != "."
}

// logAlloc looks the allocation up. A copy Nomad doesn't have is
// ErrCopyNotFound.
func (d *Deployer) logAlloc(ctx context.Context, copyID string) (*api.Allocation, error) {
	alloc, _, err := d.nomad.Allocations().Info(copyID, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if statusOf(err) == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
		}
		return nil, d.callError("read copy "+copyID, err)
	}
	return alloc, nil
}

// logFSError maps a failed file call: a missing file is ErrLogRotated, an
// unknown copy ErrCopyNotFound, the rest as callError, naming read-fs when the
// token lacks it.
func (d *Deployer) logFSError(ctx context.Context, action, copyID string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if statusOf(err) == http.StatusNotFound {
		return d.logMessageError(action, copyID, errorBody(err))
	}
	return d.capabilityError(action, "read-fs", err)
}

// logMessageError maps the text Nomad's client gave instead of a file.
func (d *Deployer) logMessageError(action, copyID, msg string) error {
	switch {
	case strings.Contains(msg, "no such file"):
		return fmt.Errorf("%w: %s", ErrLogRotated, copyID)
	case strings.Contains(msg, "Unknown allocation"):
		return fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
	case strings.Contains(msg, "Permission denied"):
		return fmt.Errorf("%w: Nomad refused the token (%s); its policy needs the read-fs capability: run the installer again to update it",
			ErrNomadUnavailable, action)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	if len(first) > 200 {
		first = first[:200] + "…"
	}
	return fmt.Errorf("%w: %s: Nomad answered: %s", ErrNomadUnavailable, action, first)
}

// errorBody is the text of a Nomad error answer, or "".
func errorBody(err error) string {
	var ure api.UnexpectedResponseError
	if errors.As(err, &ure) {
		return ure.Body()
	}
	return ""
}

// LogCopies lists every copy Nomad still lists for the jobs, running or not,
// of any job version, in display order (service, newest version first, start
// time). Unlike Copies it hides none: a copy of an older version that has
// stopped is the one whose output is still on the node. Those terminal copies
// have Gone set. A job with no copies adds none; no jobs make no request.
func (d *Deployer) LogCopies(ctx context.Context, jobIDs []string) ([]Copy, error) {
	if len(jobIDs) == 0 {
		return nil, nil
	}
	stubs, _, err := d.nomad.Allocations().List((&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return nil, d.callError("list copies", err)
	}
	var copies []Copy
	for _, s := range stubs {
		if !slices.Contains(jobIDs, s.JobID) {
			continue
		}
		c := copyOf(s)
		c.Gone = c.Terminal
		copies = append(copies, c)
	}
	sortCopies(copies)
	return copies, nil
}
