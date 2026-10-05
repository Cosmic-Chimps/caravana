package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// LogLine is one line of an app's output, from one copy's stream.
type LogLine struct {
	Copy    string // the copy's ID (a Nomad allocation ID)
	Service string // the service the copy belongs to
	Stream  deploy.LogStream
	Text    string     // the line without its newline; plain text, never markup
	Time    *time.Time // when Caravana received it; nil for the tail, which has no time
}

// LogQuery says which output to read.
type LogQuery struct {
	Copy   string           // one copy's ID; "" means every listed copy
	Stream deploy.LogStream // "stdout", "stderr", or "" or "both" for the two
	Lines  int              // how many lines of tail, 1 to 500; anything else means 500
	Follow bool             // keep going: send each new line as it is written
}

const (
	maxLogLines  = 500       // the most lines of tail, and the default
	logTailBytes = 64 * 1024 // how far back from the end of each stream the tail looks (R10)
)

// logTarget is one stream of one copy.
type logTarget struct {
	copy   Copy
	stream deploy.LogStream
}

// LogCopies lists the copies whose output a query would read: the one copy
// named, or every copy the app has, ordered by when they started (copies that
// haven't started last). A copy that isn't one of the app's is
// store.ErrNotFound, as is an unknown app. Callers that answer with an event
// stream use it first, so that these errors can still be a plain error response.
func (s *Service) LogCopies(ctx context.Context, appID, copyID string) ([]Copy, error) {
	copies, err := s.Copies(ctx, appID)
	if err != nil {
		return nil, err
	}
	if copyID != "" {
		i := slices.IndexFunc(copies, func(c Copy) bool { return c.ID == copyID })
		if i < 0 {
			return nil, fmt.Errorf("copy %q of app %q: %w", copyID, appID, store.ErrNotFound)
		}
		copies = []Copy{copies[i]}
	}
	slices.SortStableFunc(copies, func(a, b Copy) int {
		switch {
		case a.StartedAt != nil && b.StartedAt != nil:
			if c := a.StartedAt.Compare(*b.StartedAt); c != 0 {
				return c
			}
		case a.StartedAt != nil:
			return -1
		case b.StartedAt != nil:
			return 1
		}
		if c := strings.Compare(a.Service, b.Service); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return copies, nil
}

// Logs reads an app's output into out (research R10).
//
// First the tail: each chosen copy's recent output is read, copy by copy in
// start order (each copy's stdout, then its stderr), and the last q.Lines lines
// of all that are sent, oldest first. Nomad's lines carry no time, so this is
// the best order there is. Then, if q.Follow, each new line is sent as it
// arrives from any copy, with its Time set. Logs sends nothing else and closes
// nothing; it returns when the tail is sent (without follow), when ctx ends
// (nil), or when Nomad can't be read. A reader that stops reading holds Logs up.
//
// The tail and the follow are two reads, so a line written in the instant
// between them is missed; for a log viewer that is better than showing lines
// twice.
//
// A copy whose output can't be read doesn't stop the others: an error is
// returned only when no stream could be read (the usual cause is Nomad itself),
// or, while following, when every stream has ended with one.
//
// Line contents are never written to a logger: they are the app's output, and
// may hold anything the app prints (constitution VII).
func (s *Service) Logs(ctx context.Context, appID string, q LogQuery, out chan<- LogLine) error {
	copies, err := s.LogCopies(ctx, appID, q.Copy)
	if err != nil {
		return err
	}
	var streams []deploy.LogStream
	switch q.Stream {
	case "", "both":
		streams = []deploy.LogStream{deploy.LogStdout, deploy.LogStderr}
	default:
		streams = []deploy.LogStream{q.Stream}
	}
	var targets []logTarget
	for _, c := range copies {
		for _, st := range streams {
			targets = append(targets, logTarget{c, st})
		}
	}
	lines := q.Lines
	if lines < 1 || lines > maxLogLines {
		lines = maxLogLines
	}

	if err := s.tailLogs(ctx, targets, lines, out); err != nil {
		return err
	}
	if !q.Follow || ctx.Err() != nil {
		return nil
	}
	return s.followLogs(ctx, targets, out)
}

// tailLogs reads every target's tail at once, and sends the last lines of
// them, in target order.
func (s *Service) tailLogs(ctx context.Context, targets []logTarget, lines int, out chan<- LogLine) error {
	read := make([][]LogLine, len(targets))
	failed := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			failed[i] = s.readStream(ctx, t, logTailBytes, false, func(l deploy.LogLine) {
				read[i] = append(read[i], logLineOf(t.copy, l, nil))
			})
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if len(targets) > 0 {
		if err := firstErrorIfAll(failed); err != nil {
			return fmt.Errorf("read logs: %w", err)
		}
	}

	var all []LogLine
	for _, r := range read {
		all = append(all, r...)
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	for _, l := range all {
		select {
		case out <- l:
		case <-ctx.Done():
			return nil
		}
	}
	return nil
}

// readStream reads one target with deploy.Logs and calls got for each line, in
// order, until the read is over. It returns the read's error.
func (s *Service) readStream(ctx context.Context, t logTarget, tailBytes int64, follow bool, got func(deploy.LogLine)) error {
	ch := make(chan deploy.LogLine)
	done := make(chan error, 1)
	go func() { done <- s.deployer.Logs(ctx, t.copy.ID, t.copy.Service, t.stream, tailBytes, follow, ch) }()
	for {
		select {
		case l := <-ch:
			got(l)
		case err := <-done:
			return err
		}
	}
}

// firstErrorIfAll returns the first error when every entry is an error, else nil.
func firstErrorIfAll(errs []error) error {
	for _, err := range errs {
		if err == nil {
			return nil
		}
	}
	return errs[0]
}

// followLogs sends new lines from every target until ctx ends or every stream has
// ended. One goroutine per stream feeds one channel, which this goroutine
// forwards to out with the time of arrival.
func (s *Service) followLogs(ctx context.Context, targets []logTarget, out chan<- LogLine) error {
	type arrival struct {
		copy Copy
		line deploy.LogLine
	}
	inner := make(chan arrival)
	failed := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			failed[i] = s.readStream(ctx, t, 0, true, func(l deploy.LogLine) {
				select {
				case inner <- arrival{t.copy, l}:
				case <-ctx.Done():
				}
			})
		})
	}
	go func() {
		wg.Wait()
		close(inner)
	}()

	for item := range inner {
		now := time.Now().UTC()
		select {
		case out <- logLineOf(item.copy, item.line, &now):
		case <-ctx.Done():
			// Producers see the same cancellation and stop; wait for them.
			for range inner {
			}
		}
	}
	if ctx.Err() != nil || len(targets) == 0 {
		return nil
	}
	if err := firstErrorIfAll(failed); err != nil {
		return fmt.Errorf("follow logs: %w", err)
	}
	return nil
}

func logLineOf(c Copy, l deploy.LogLine, at *time.Time) LogLine {
	return LogLine{Copy: c.ID, Service: c.Service, Stream: l.Stream, Text: l.Text, Time: at}
}
