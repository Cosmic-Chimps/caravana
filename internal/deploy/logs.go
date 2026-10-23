package deploy

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/hashicorp/nomad/api"
)

// LogStream is one of a task's two output streams.
type LogStream string

// The two streams (they are Nomad's log types).
const (
	LogStdout LogStream = "stdout"
	LogStderr LogStream = "stderr"
)

// LogLine is one line of a copy's output, without its newline. Nomad's log
// lines carry no time, so there is none here.
type LogLine struct {
	Copy   string // the Nomad allocation ID
	Stream LogStream
	Text   string // valid UTF-8; invalid bytes are replaced
}

// maxLogLine is the longest line Logs sends: a longer one is cut here, so one
// runaway line (a binary dump, a missing newline) can't grow without bound.
const maxLogLine = 64 * 1024

// Logs reads one copy's output of one stream, line by line, into out: the
// lines in the last tailBytes of it, then, when follow is set, each new line
// as the task writes it. (research R10)
//
// tailBytes 0 means no tail: only what is written after the call, with follow.
// A tail that starts inside a line leaves that line's cut-off beginning out,
// instead of showing half a line. A line is complete at its newline; a partial
// last line waits for the rest while following, and is sent as it is when the
// output ends (without follow).
//
// task is the name of the task in the copy: Caravana names it after the
// service. Logs sends on out, so a reader that stops reading holds it up, and
// it returns when the output ends (without follow), when ctx ends (nil: ending
// isn't a failure), or when Nomad fails (an error of 003's kinds). A copy
// Nomad no longer has, or has no output files for yet, has no output: nil.
//
// Nomad's client library tries the node's own address first and then the
// server's (NewNomadClient keeps the first try short).
func (d *Deployer) Logs(ctx context.Context, copyID, task string, stream LogStream,
	tailBytes int64, follow bool, out chan<- LogLine) error {
	return d.readLogs(ctx, copyID, task, stream, tailBytes, follow, out, nil)
}

// readLogs is Logs, and when cut isn't nil it also says, in *cut, whether the
// tail left out the beginning of the output: true when the output had more
// than tailBytes before its end, so what was sent is not all of it. It is set
// when the first of the output arrives, before any line is sent. A caller that
// keeps the last N bytes of a run's output (ScheduleRunOutput) uses it to say
// the first lines were dropped.
func (d *Deployer) readLogs(ctx context.Context, copyID, task string, stream LogStream,
	tailBytes int64, follow bool, out chan<- LogLine, cut *bool) error {
	if cut != nil {
		*cut = false
	}
	q := (&api.QueryOptions{}).WithContext(ctx)
	alloc, _, err := d.nomad.Allocations().Info(copyID, q)
	if err != nil {
		if ctx.Err() != nil || statusOf(err) == http.StatusNotFound {
			return nil
		}
		return d.callError("read copy "+copyID, err)
	}

	// One byte more than the tail asks for, to see what comes before it: when
	// that byte is a newline, the tail starts on a line; when not, its first
	// line is cut.
	offset := tailBytes
	if tailBytes > 0 {
		offset++
	}
	cancel := make(chan struct{})
	frames, errCh := d.nomad.AllocFS().Logs(alloc, follow, task, string(stream), "end", offset, cancel, q)
	if frames == nil { // the request itself failed
		return d.logsError(ctx, copyID, <-errCh)
	}
	// Closing cancel stops the library's reader once its next frame is read,
	// but it may be blocked sending the frame it holds to a reader that is
	// gone. Drain until it finishes; it does when the connection closes (the
	// request carries ctx) or its channel does.
	abandoned := true
	defer func() {
		close(cancel)
		if abandoned {
			go func() {
				for {
					select {
					case _, ok := <-frames:
						if !ok {
							return
						}
					case <-errCh:
						return
					}
				}
			}()
		}
	}()

	send := func(text string) bool {
		line := LogLine{Copy: copyID, Stream: stream, Text: strings.ToValidUTF8(strings.TrimSuffix(text, "\r"), "�")}
		select {
		case out <- line:
			return true
		case <-ctx.Done():
			return false
		}
	}

	var split lineSplitter
	first := true
	for {
		select {
		case f, ok := <-frames:
			if !ok { // the output ended: whatever is left is the last line
				abandoned = false
				if rest := split.rest(); len(rest) > 0 {
					send(string(rest))
				}
				return nil
			}
			data := f.Data
			if first {
				first = false
				// A frame's offset is where its data ends. A tail that starts
				// after the beginning of the file starts inside some line.
				split.skipToLine = tailBytes > 0 && f.Offset-int64(len(data)) > 0
				if cut != nil {
					*cut = split.skipToLine
				}
			}
			if !split.add(data, send) {
				return nil // ctx ended while sending
			}
		case err := <-errCh:
			abandoned = false
			return d.logsError(ctx, copyID, err)
		case <-ctx.Done():
			return nil
		}
	}
}

// logsError is the error of a failed read of output. A cancelled ctx is no
// failure; a missing copy or file has no output.
func (d *Deployer) logsError(ctx context.Context, copyID string, err error) error {
	if ctx.Err() != nil || statusOf(err) == http.StatusNotFound {
		return nil
	}
	return d.callError("read output of copy "+copyID, err)
}

// lineSplitter turns chunks of bytes into lines.
type lineSplitter struct {
	buf []byte
	// skipToLine drops bytes up to and including the next newline: the
	// cut-off beginning of the first line of a tail.
	skipToLine bool
}

// add takes the next chunk and calls send for each complete line, in order.
// It stops, and returns false, when send does.
func (l *lineSplitter) add(data []byte, send func(string) bool) bool {
	if l.skipToLine {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return true // all of it is still the cut line
		}
		data, l.skipToLine = data[i+1:], false
	}
	l.buf = append(l.buf, data...)
	for {
		if i := bytes.IndexByte(l.buf, '\n'); i >= 0 {
			line := l.buf[:i]
			l.buf = l.buf[i+1:]
			if !send(string(line)) {
				return false
			}
			continue
		}
		if len(l.buf) < maxLogLine {
			return true
		}
		// A line with no end in sight: cut it where a character begins.
		cut := maxLogLine
		if cut < len(l.buf) {
			for cut > 0 && !utf8.RuneStart(l.buf[cut]) {
				cut--
			}
			if cut == 0 { // not text at all
				cut = maxLogLine
			}
		}
		line := l.buf[:cut]
		l.buf = l.buf[cut:]
		if !send(string(line)) {
			return false
		}
	}
}

// rest is the partial line held back, or nil.
func (l *lineSplitter) rest() []byte {
	if l.skipToLine {
		return nil
	}
	return l.buf
}
