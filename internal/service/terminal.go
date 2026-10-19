package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Terminal into a copy (feature 020.6): an interactive shell in the service's
// task, over Nomad's exec with a terminal. OpenTerminal checks what RunCommand
// checks, records the session as an action and returns a TerminalSession the
// API bridges to a websocket.
//
// What is typed and what is printed pass through a session and nowhere else:
// never logged, never in a record, an error or a notice (research R2 of 020.5,
// carried over). The record holds the shell asked for (program), the shell's
// exit code, and when the session ended for another reason, that reason in
// fixed words. The tests search every row, the SQLite file and every log line
// for a sentinel that was typed and echoed back.

// Errors the API maps to their own codes.
var (
	// ErrTooManyTerminals is a terminal asked for while the most that may be
	// open at once already are.
	ErrTooManyTerminals = errors.New("too many terminals are open: close one first")
)

// The limits of a terminal.
const (
	defaultMaxTerminals  = 5
	defaultTerminalIdle  = 15 * time.Minute
	defaultTerminalLimit = 60 * time.Minute
	// maxTerminalSide is the most columns or rows a terminal may have.
	maxTerminalSide = 1000
	// terminalOutputChunks is how many pieces of output may wait for the
	// caller before the shell is held back (a slow browser slows the shell
	// down instead of growing memory).
	terminalOutputChunks = 32
	// noShellMessage is what a session says when Nomad couldn't start the shell.
	noShellMessage = "no shell found in this image"
)

// The shells a terminal can ask for.
const (
	ShellAuto = "auto"
	ShellBash = "bash"
	ShellSh   = "sh"
)

// The reasons a terminal ends other than the shell exiting, in
// TerminalEnd.Reason.
const (
	EndIdle   = "idle"   // no input for the idle time
	EndLimit  = "limit"  // open for the time limit
	EndError  = "error"  // Caravana or Nomad couldn't carry on
	EndGone   = "gone"   // the connection to the caller was lost
	EndClosed = "closed" // the caller closed it
)

// TerminalSize is the size of a terminal in characters. It is deploy's type,
// re-exported so the API never imports deploy.
type TerminalSize = deploy.TerminalSize

// TerminalEnd says how a session ended.
type TerminalEnd struct {
	// Code is the shell's exit code; nil when the session ended another way.
	Code *int
	// Reason is why a session ended when the shell didn't exit: one of the End
	// constants. Empty when it did.
	Reason string
	// Message is a fixed sentence about the end, or empty. It is never what was
	// typed or printed.
	Message string
}

// shellCommand is the program a shell choice runs. "auto" starts bash when the
// image has it and sh otherwise (the sh -c itself needs /bin/sh). Nomad's exec
// can't set the environment, so auto sets TERM in the script; the others get
// the runtime's own (xterm).
func shellCommand(shell string) ([]string, error) {
	switch shell {
	case ShellAuto:
		return []string{"/bin/sh", "-c",
			"export TERM=xterm-256color; if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"}, nil
	case ShellBash:
		return []string{"/bin/bash"}, nil
	case ShellSh:
		return []string{"/bin/sh"}, nil
	}
	return nil, app.NewFieldError("shell", "must be auto, bash or sh")
}

// checkTerminalSize is a *app.FieldError unless cols and rows are 1 to 1000.
func checkTerminalSize(size TerminalSize) error {
	switch {
	case size.Cols < 1 || size.Cols > maxTerminalSide:
		return app.NewFieldError("cols", "must be between 1 and %d", maxTerminalSide)
	case size.Rows < 1 || size.Rows > maxTerminalSide:
		return app.NewFieldError("rows", "must be between 1 and %d", maxTerminalSide)
	}
	return nil
}

// SetTerminalLimits sets how many terminals may be open at once, how long one
// may go without input and how long one may stay open (5, 15 minutes and 60
// minutes by default). Call it before Start; a value that isn't positive keeps
// the default.
func (s *Service) SetTerminalLimits(maxOpen int, idle, limit time.Duration) {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	if maxOpen > 0 {
		s.maxTerminals = maxOpen
	}
	if idle > 0 {
		s.terminalIdle = idle
	}
	if limit > 0 {
		s.terminalLimit = limit
	}
}

// TerminalSession is one open shell. The caller writes what the user types
// with Write and Resize, reads the shell's output from Output until Done is
// closed, and then reads End. Close ends it when the caller goes away.
type TerminalSession struct {
	svc      *Service
	appID    string
	actionID string
	ctx      context.Context
	cancel   context.CancelFunc

	stdin  *io.PipeWriter
	stdinR *io.PipeReader
	sizes  chan TerminalSize
	output chan []byte
	done   chan struct{}

	idleFor, limitFor time.Duration
	idle, limit       *time.Timer

	mu     sync.Mutex
	reason string // why Caravana ended it; empty while open or when the shell exited
	typed  bool   // anything was typed
	end    TerminalEnd
}

// Output delivers what the shell prints, in order, in pieces as they come. It
// is never closed: when Done is closed, take what is left in it and stop.
func (t *TerminalSession) Output() <-chan []byte { return t.output }

// Done is closed when the session has ended and its record is settled.
func (t *TerminalSession) Done() <-chan struct{} { return t.done }

// End says how the session ended. Call it after Done is closed.
func (t *TerminalSession) End() TerminalEnd {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.end
}

// Write sends what the user typed to the shell. A write is input: it keeps the
// session from being idle. It blocks until Nomad has taken the bytes, and
// fails once the session has ended.
func (t *TerminalSession) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if t.ctx.Err() != nil {
		return 0, io.ErrClosedPipe
	}
	t.mu.Lock()
	t.typed = true
	t.mu.Unlock()
	t.idle.Reset(t.idleFor)
	return t.stdin.Write(p)
}

// Resize tells the shell the terminal has a new size. The size must have been
// checked (cols and rows 1 to 1000). A resize isn't input.
func (t *TerminalSession) Resize(cols, rows int) {
	size := TerminalSize{Cols: cols, Rows: rows}
	for {
		select {
		case t.sizes <- size:
			return
		default:
		}
		// A full channel holds sizes nobody will need now: drop the oldest.
		select {
		case <-t.sizes:
		default:
		}
	}
}

// Close ends the session because the caller is done with it: reason is
// EndClosed when the user closed it and EndGone when the connection was lost.
// The first reason a session gets is the one kept. It returns at once; Done is
// closed when the session has ended.
func (t *TerminalSession) Close(reason string) { t.stop(reason) }

// stop records why the session is ending, unless it already has a reason, and
// cancels the exec.
func (t *TerminalSession) stop(reason string) {
	t.mu.Lock()
	if t.reason == "" {
		t.reason = reason
	}
	t.mu.Unlock()
	t.cancel()
}

// terminalWriter is where Nomad's client writes the shell's output.
type terminalWriter struct{ t *TerminalSession }

// Write passes a copy of p on and reports all of it written; the sender goes
// on whatever happens (a short write would be an error to it). After the
// session's context ends the output is dropped.
func (w terminalWriter) Write(p []byte) (int, error) {
	chunk := slices.Clone(p)
	select {
	case w.t.output <- chunk:
	case <-w.t.ctx.Done():
	}
	return len(p), nil
}

// OpenTerminal opens a shell in a copy of the app, as asked by shell ("auto",
// "bash" or "sh"), with the terminal at size. The session runs until the shell
// exits, the caller closes it, it has gone idle (no input for 15 minutes) or
// has been open for 60 minutes. At most five are open at once.
//
// Errors: an unknown app, or a copy that isn't the app's, is store.ErrNotFound;
// an app with no definition ErrAppNotDefined; a stopped one ErrAppStopped; a
// copy that has ended or isn't running ErrCopyNotRunning; a bad shell or size
// a *app.FieldError; too many sessions ErrTooManyTerminals. No lock of the app
// is taken: a terminal doesn't change the app.
//
// The action is recorded as running now and settled when the session ends:
// the shell asked for, the exit code, or the reason in fixed words.
func (s *Service) OpenTerminal(ctx context.Context, appID, copyID, shell string, size TerminalSize, trigger app.Trigger) (*TerminalSession, error) {
	command, err := shellCommand(shell)
	if err != nil {
		return nil, err
	}
	if err := checkTerminalSize(size); err != nil {
		return nil, err
	}
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	if !rec.App.Defined() {
		return nil, fmt.Errorf("app %q: %w", appID, ErrAppNotDefined)
	}
	if rec.StoppedAt != nil {
		return nil, fmt.Errorf("app %q: %w", appID, ErrAppStopped)
	}
	copies, err := s.deployer.Copies(ctx, appID)
	if err != nil {
		return nil, fmt.Errorf("list copies of %q: %w", appID, err)
	}
	i := slices.IndexFunc(copies, func(c Copy) bool { return c.ID == copyID })
	if i < 0 {
		return nil, fmt.Errorf("app %q has no copy %q: %w", appID, copyID, store.ErrNotFound)
	}
	c := copies[i]
	if c.Terminal || c.Stopping {
		return nil, fmt.Errorf("copy %q is %s: %w", copyID, c.Status, ErrCopyNotRunning)
	}
	id, err := newID("act_")
	if err != nil {
		return nil, err
	}

	s.termMu.Lock()
	if s.terminalsOpen >= s.maxTerminals {
		s.termMu.Unlock()
		return nil, ErrTooManyTerminals
	}
	s.terminalsOpen++
	idleFor, limitFor := s.terminalIdle, s.terminalLimit
	s.termMu.Unlock()

	action := store.ActionRecord{ID: id, AppID: appID, Action: store.ActionTerminal, Trigger: string(trigger),
		Status: store.ActionRunning, CopyID: copyID, Service: c.Service, Program: shell, StartedAt: time.Now().UTC()}
	if err := s.store.InsertAction(ctx, action); err != nil {
		s.releaseTerminal()
		return nil, err
	}
	s.actionsChanged(appID)

	// The session lives with the process, not with the request that opened it.
	runCtx, cancel := context.WithCancel(s.background())
	stdinR, stdin := io.Pipe()
	t := &TerminalSession{
		svc: s, appID: appID, actionID: id, ctx: runCtx, cancel: cancel,
		stdin: stdin, stdinR: stdinR,
		sizes:   make(chan TerminalSize, 8),
		output:  make(chan []byte, terminalOutputChunks),
		done:    make(chan struct{}),
		idleFor: idleFor, limitFor: limitFor,
	}
	t.sizes <- size
	t.idle = time.AfterFunc(idleFor, func() { t.stop(EndIdle) })
	t.limit = time.AfterFunc(limitFor, func() { t.stop(EndLimit) })

	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		s.runTerminal(t, c, command)
	}()
	return t, nil
}

// releaseTerminal frees one of the sessions that may be open.
func (s *Service) releaseTerminal() {
	s.termMu.Lock()
	s.terminalsOpen--
	s.termMu.Unlock()
}

// runTerminal runs the exec until it ends, settles the record, and only then
// closes Done, so whoever sees a session end can read its record.
func (s *Service) runTerminal(t *TerminalSession, c Copy, command []string) {
	code, err := s.deployer.OpenTerminal(t.ctx, c.ID, c.Service, command, t.stdinR, terminalWriter{t}, t.sizes)
	t.idle.Stop()
	t.limit.Stop()
	// Nomad's client reads stdin until it ends, and a writer may be waiting
	// for it: both go.
	t.stdinR.Close()
	t.stdin.Close()

	end, status, exit, msg := s.settleTerminal(t, code, err)
	t.mu.Lock()
	t.end = end
	t.mu.Unlock()
	s.finishAction(t.actionID, t.appID, status, exit, msg)
	s.releaseTerminal()
	t.cancel()
	close(t.done)
}

// settleTerminal decides how a session ended, from what OpenTerminal returned
// and why Caravana stopped it, if it did: the end for the caller and the
// record's status, exit code and error. All of it is in words of ours.
func (s *Service) settleTerminal(t *TerminalSession, code int, err error) (end TerminalEnd, status string, exit *int, msg string) {
	t.mu.Lock()
	reason, typed := t.reason, t.typed
	t.mu.Unlock()

	if err == nil {
		exit = &code
		// Exec failing to find the program shows as 126 or 127 as well. Said
		// only when nothing was typed: after that it is the user's own exit.
		if (code == 126 || code == 127) && !typed {
			return TerminalEnd{Code: exit, Message: noShellMessage}, store.ActionFailed, exit, noShellMessage
		}
		return TerminalEnd{Code: exit}, store.ActionSucceeded, exit, ""
	}
	switch reason {
	case EndIdle:
		msg = "idle for " + describeDuration(t.idleFor)
		return TerminalEnd{Reason: EndIdle, Message: msg}, store.ActionFailed, nil, msg
	case EndLimit:
		msg = "open for " + describeDuration(t.limitFor)
		return TerminalEnd{Reason: EndLimit, Message: msg}, store.ActionFailed, nil, msg
	case EndGone:
		return TerminalEnd{Reason: EndGone, Message: "connection lost"}, store.ActionFailed, nil, "connection lost"
	case EndClosed:
		return TerminalEnd{Reason: EndClosed, Message: "closed"}, store.ActionSucceeded, nil, "closed"
	}
	switch {
	case errors.Is(err, deploy.ErrExecFailed):
		msg = noShellMessage
	case s.background().Err() != nil:
		msg = "Caravana is stopping"
	default:
		msg = execFailure(err)
	}
	return TerminalEnd{Reason: EndError, Message: msg}, store.ActionFailed, nil, msg
}

// describeDuration says d the way the record keeps it: "15 minutes" for whole
// minutes, Go's own text otherwise (tests use short times).
func describeDuration(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		n := int(d / time.Minute)
		if n == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", n)
	}
	return d.String()
}
