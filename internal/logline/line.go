// Package logline reads one line of an app's output: it strips terminal
// escape sequences, finds the time the line carries, decides its level, and
// folds runs of identical lines. It is pure: no I/O, no clock, and no
// knowledge of Nomad, HTTP or the rest of Caravana. The service builds the
// lines from what the deploy layer reads and calls Parse once per line.
package logline

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Level is how serious a line is. Every line has one; a line that says
// nothing about itself is Info, on either stream.
type Level string

// The four levels, from worst to least.
const (
	Error Level = "error"
	Warn  Level = "warn"
	Info  Level = "info"
	Debug Level = "debug"
)

// ErrBadLevel is returned by ParseLevel for a word that is not a level.
var ErrBadLevel = errors.New("logline: not a level")

// ParseLevel reads one of the four level names, in any case.
func ParseLevel(s string) (Level, error) {
	switch Level(strings.ToLower(strings.TrimSpace(s))) {
	case Error:
		return Error, nil
	case Warn:
		return Warn, nil
	case Info:
		return Info, nil
	case Debug:
		return Debug, nil
	}
	return "", fmt.Errorf("%w (use error, warn, info or debug)", ErrBadLevel)
}

// Stream is the output a line was written to.
type Stream string

// The two streams a task writes.
const (
	Stdout Stream = "stdout"
	Stderr Stream = "stderr"
)

// ErrBadStream is returned by ParseStream for a word that is not a stream.
var ErrBadStream = errors.New("logline: not a stream")

// ParseStream reads "stdout" or "stderr".
func ParseStream(s string) (Stream, error) {
	switch Stream(s) {
	case Stdout:
		return Stdout, nil
	case Stderr:
		return Stderr, nil
	}
	return "", fmt.Errorf("%w (use stdout or stderr)", ErrBadStream)
}

// TimeSource says where a line's time came from.
type TimeSource string

const (
	// FromLine: the line carried its own time.
	FromLine TimeSource = "line"
	// Estimated: the line had none, so it took the time of the older line
	// before it in the same stream (or the copy's start).
	Estimated TimeSource = "estimated"
	// Received: a live line, timed when it arrived. Parse never sets it.
	Received TimeSource = "received"
)

// LineID names one line for as long as Nomad keeps its file: the copy, the
// stream, the file's index in alloc/logs/<task>.<stream>.<file> and the byte
// offset of the line's first byte in that file.
type LineID struct {
	Copy   string // an allocation id, or its first 8 characters
	Stream Stream
	File   int
	Offset int64
}

// String writes "<copy>.<stream>.<file>.<offset>".
func (id LineID) String() string {
	return id.Copy + "." + string(id.Stream) + "." + strconv.Itoa(id.File) + "." + strconv.FormatInt(id.Offset, 10)
}

// ErrBadLineID is returned by ParseLineID for text that is not a line id.
var ErrBadLineID = errors.New("logline: not a line id")

// ParseLineID reads what LineID.String writes. Each part is checked: the copy
// is 8 hex characters or a full allocation id, the stream is stdout or stderr,
// the file and the offset are plain non-negative numbers.
func ParseLineID(s string) (LineID, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return LineID{}, fmt.Errorf("%w: want <copy>.<stream>.<file>.<offset>", ErrBadLineID)
	}
	if !validCopy(parts[0]) {
		return LineID{}, fmt.Errorf("%w: the copy is 8 hex characters or an allocation id", ErrBadLineID)
	}
	stream, err := ParseStream(parts[1])
	if err != nil {
		return LineID{}, fmt.Errorf("%w: the stream is stdout or stderr", ErrBadLineID)
	}
	file, ok := plainNumber(parts[2], 31)
	if !ok {
		return LineID{}, fmt.Errorf("%w: the file is a number", ErrBadLineID)
	}
	offset, ok := plainNumber(parts[3], 63)
	if !ok {
		return LineID{}, fmt.Errorf("%w: the offset is a number", ErrBadLineID)
	}
	return LineID{Copy: parts[0], Stream: stream, File: int(file), Offset: offset}, nil
}

// plainNumber reads digits only (no sign, no leading zero but "0" itself)
// that fit in the given number of bits, so a string and its number are one to
// one.
func plainNumber(s string, bits int) (int64, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, bits+1)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// validCopy accepts the first 8 characters of an allocation id (lowercase
// hex) or a whole one (8-4-4-4-12).
func validCopy(s string) bool {
	switch len(s) {
	case 8:
		return isHex(s)
	case 36:
		return s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' &&
			isHex(s[:8]) && isHex(s[9:13]) && isHex(s[14:18]) && isHex(s[19:23]) && isHex(s[24:])
	}
	return false
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Span is the byte range [Start, End) of a line's time inside its stripped
// text. The zero Span means the line has no time to cut out.
type Span struct{ Start, End int }

// Slack is how far before a copy's start a line's time may be, and so how far
// NotBefore sits before the start.
const Slack = time.Minute

// Bounds say when a line can have been written. The caller sets NotBefore to
// the copy's start minus Slack (or, for a later file, the previous file's
// ModTime minus Slack) and NotAfter to the file's ModTime. A zero value means
// that side is not checked.
type Bounds struct {
	NotBefore, NotAfter time.Time
}

func (b Bounds) contains(t time.Time) bool {
	if !b.NotBefore.IsZero() && t.Before(b.NotBefore) {
		return false
	}
	if !b.NotAfter.IsZero() && t.After(b.NotAfter) {
		return false
	}
	return true
}

// Line is one line of a copy's output, read.
type Line struct {
	ID         LineID
	Time       time.Time // UTC
	TimeSource TimeSource
	Stream     Stream
	Level      Level
	// Text is the line without escape sequences, time included, as shown. For
	// a JSON line it is the message field (or the whole line when there is
	// none) and Raw holds the line itself.
	Text string
	// Raw is the stripped line, set only when it was a JSON object.
	Raw string
	// Repeat is N-1 for a folded row of N lines, 0 otherwise.
	Repeat int

	span Span // where Text holds the line's time; folding ignores it
}

// TextWithoutTime is Text with the time cut out, which is what Fold compares:
// two health checks that differ only in their time are the same text.
func TextWithoutTime(l Line) string {
	return cut(l.Text, l.span)
}

// cut removes a span. A time at the very start also takes the spaces after
// it, so the text reads as if the line began with the rest.
func cut(s string, sp Span) string {
	if sp.End <= sp.Start || sp.Start < 0 || sp.End > len(s) {
		return s
	}
	rest := s[:sp.Start] + s[sp.End:]
	if sp.Start == 0 {
		rest = strings.TrimLeft(rest, " \t")
	}
	return rest
}
