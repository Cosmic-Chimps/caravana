package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Moving work off a server (feature 042). The server stops taking new work (its Nomad node is made
// ineligible) and each copy that can move is stopped with Nomad's allocation stop, one at a time, which
// makes the scheduler put a replacement on another server (research R4: Nomad's own node drain would
// stop what must stay). A copy is moved only after the app's lock is held, so no deploy or volume run
// moves under it, and the next one only when the replacement runs and passes its checks.
//
// What never moves is listed with its reason: a service with volumes (its data is on the server), one
// with a published port (the port is on this server's address), Traefik (the ingress), Caravana's own
// helper jobs (a run that ends on its own), anything Caravana didn't deploy, and a service whose
// placement rules allow no other server. A move that ends with nothing movable left is done, even when
// some of this stays.

// Errors of the placement and drain calls. The API maps each to its status and code.
var (
	// ErrServerNotReady: the server hasn't joined, or Nomad can't see it.
	ErrServerNotReady error = sentence("This server isn't ready: it hasn't joined, or Nomad can't see it.")
	// ErrServerMoving: work is already being moved off the server.
	ErrServerMoving error = sentence("Work is already being moved off this server.")
	// ErrNoOtherServer: no other server accepts work, so there is nowhere to move to.
	ErrNoOtherServer error = sentence("No other server accepts work: there is nowhere to move to.")
)

// The deadline of a move: how long it may take before it ends incomplete.
const (
	DefaultMoveDeadline = 15 * time.Minute
	minMoveDeadline     = time.Minute
	maxMoveDeadline     = 2 * time.Hour
)

// moveTiming holds the waits of a move, so tests can shorten them.
type moveTiming struct {
	minDeadline time.Duration // the shortest deadline asked for
	copyWait    time.Duration // how long one copy's replacement may take to run and pass its checks
	pollGap     time.Duration // between two rounds when nothing could be moved
}

func defaultMoveTiming() moveTiming {
	return moveTiming{minDeadline: minMoveDeadline, copyWait: 5 * time.Minute, pollGap: 5 * time.Second}
}

// SetMoveTimingForTests shortens the waits of a move: the shortest deadline MoveWorkOff accepts, how
// long one copy may take to move and the gap between rounds. A value that isn't positive keeps the
// default.
func (s *Service) SetMoveTimingForTests(minDeadline, copyWait, pollGap time.Duration) {
	if minDeadline > 0 {
		s.move.minDeadline = minDeadline
	}
	if copyWait > 0 {
		s.move.copyWait = copyWait
	}
	if pollGap > 0 {
		s.move.pollGap = pollGap
	}
}

// MoveStay is one thing a move never moves.
type MoveStay struct{ App, Service, Reason string }

// MoveView is a server's newest move as the API and the console show it.
type MoveView struct {
	ID         string
	State      string // store.MoveMoving, MoveDone, MoveIncomplete or MoveCancelled
	StartedAt  time.Time
	DeadlineAt time.Time
	EndedAt    time.Time // zero while it goes
	Moved      int       // copies that left the server
	Left       int       // movable copies still there
	Stays      []MoveStay
	Error      string
}

func moveViewOf(m store.ServerMove) *MoveView {
	v := &MoveView{ID: m.ID, State: m.State, StartedAt: m.StartedAt, DeadlineAt: m.DeadlineAt, EndedAt: m.EndedAt,
		Moved: m.Moved, Left: m.Left, Error: m.Error, Stays: []MoveStay{}}
	for _, st := range m.Stays {
		v.Stays = append(v.Stays, MoveStay{App: st.App, Service: st.Service, Reason: st.Reason})
	}
	return v
}

// moveRun is a move whose goroutine is going: its cancel function and a channel closed when it has
// settled the move's row.
type moveRun struct {
	cancel    context.CancelFunc
	cancelled atomic.Bool // the owner asked (AcceptWork), as against the process shutting down
	done      chan struct{}
}

// movable is one copy a move can move.
type movable struct {
	AppID, Service, CopyID, Group string
	Checked                       bool // the service has a health check, so the replacement must pass it
}

// moveTarget is a server a copy could land on, for the placement check of classifyCopies.
type moveTarget struct {
	Name    string
	Managed bool // has a row: its name is in the node's metadata, so "avoid" can name it
	Labels  map[string]string
}

// allowedOn says a placement rule lets a copy run on that server: it has every required label, and its
// name isn't avoided. Preferences never forbid anything. A server Caravana doesn't manage has no labels
// and no name in its metadata, so it only satisfies a service that requires nothing (research R2).
func allowedOn(p *app.Placement, o moveTarget) bool {
	if p == nil {
		return true
	}
	for name, value := range p.Require {
		if v, ok := o.Labels[name]; !ok || v != value {
			return false
		}
	}
	return !(o.Managed && slices.Contains(p.Avoid, o.Name))
}

// classifyCopies sorts the copies on a node into what can move and what stays (pure). byJob is
// deploy.AllCopies' answer, node the node's name, others the servers a copy could land on. Copies that
// have ended or are being stopped are left out. The result is in a fixed order (job ID, then the copies'
// order), and stays has one entry per app and service, or per job for what isn't an app.
func classifyCopies(apps []store.AppRecord, byJob map[string][]deploy.Copy, node string, others []moveTarget) (movables []movable, stays []store.MoveStay) {
	recs := map[string]store.AppRecord{}
	for _, r := range apps {
		recs[r.App.ID] = r
	}
	addStay := func(st store.MoveStay) {
		if !slices.Contains(stays, st) {
			stays = append(stays, st)
		}
	}
	for _, jobID := range slices.Sorted(maps.Keys(byJob)) {
		for _, c := range byJob[jobID] {
			// A disconnected copy is out of reach: stopping it would only tell Nomad's servers, and
			// the node isn't ready, so a move that reaches it is a cut that began meanwhile.
			if c.Node != node || c.Terminal || c.Stopping || c.Status == deploy.CopyDisconnected {
				continue
			}
			switch rec, isApp := recs[jobID]; {
			case jobID == "traefik":
				addStay(store.MoveStay{App: "traefik", Reason: "the proxy runs here"})
			case isApp:
				svcIdx := slices.IndexFunc(rec.App.Services, func(s app.Service) bool { return s.Name == c.Service })
				if svcIdx < 0 {
					addStay(store.MoveStay{App: jobID, Service: c.Service, Reason: "not deployed by Caravana"})
					continue
				}
				svc := rec.App.Services[svcIdx]
				switch {
				case len(svc.Volumes) > 0:
					addStay(store.MoveStay{App: jobID, Service: c.Service, Reason: "its data is on this server"})
				case svc.Publishes():
					addStay(store.MoveStay{App: jobID, Service: c.Service,
						Reason: fmt.Sprintf("its port %d is on this server's address", firstPublishedPort(svc))})
				case !anyAllowed(svc.Placement, others):
					addStay(store.MoveStay{App: jobID, Service: c.Service, Reason: "its placement rules allow no other server"})
				default:
					movables = append(movables, movable{AppID: jobID, Service: c.Service, CopyID: c.ID, Group: c.Service,
						Checked: svc.Health != nil})
				}
			case isHelperJob(jobID):
				addStay(store.MoveStay{App: helperApp(jobID), Reason: "a run that ends on its own"})
			default:
				addStay(store.MoveStay{App: jobID, Reason: "not deployed by Caravana"})
			}
		}
	}
	return movables, stays
}

func anyAllowed(p *app.Placement, others []moveTarget) bool {
	return slices.ContainsFunc(others, func(o moveTarget) bool { return allowedOn(p, o) })
}

func firstPublishedPort(svc app.Service) int {
	for _, p := range svc.Ports {
		if p.IsPublished() {
			return p.Published
		}
	}
	return 0
}

// isHelperJob says a job is one of Caravana's helpers beside an app's job (spec 010, 022): a backup or
// restore job, its periodic children, or a schedule run. App IDs hold no "_", so any job ID that does
// is one of them.
func isHelperJob(id string) bool { return strings.Contains(id, "_") }

// helperApp is the app a helper job belongs to: the job ID up to its first "_".
func helperApp(id string) string {
	appID, _, _ := strings.Cut(id, "_")
	return appID
}

// moveOthers are the servers a copy could land on: every Nomad node that is ready, accepts work and
// isn't the node that is being emptied, with its stored labels.
func moveOthers(rows []store.Server, nodes []deploy.Node, self string) []moveTarget {
	var out []moveTarget
	for _, n := range nodes {
		if n.ID == self || n.Status != "ready" || !n.Eligible || n.Draining {
			continue
		}
		o := moveTarget{Name: n.Name, Labels: map[string]string{}}
		for _, r := range rows {
			if node := nodeOf([]deploy.Node{n}, r.Index); node != nil {
				o.Name, o.Managed, o.Labels = r.Name, true, r.Labels
			}
		}
		out = append(out, o)
	}
	return out
}

// MoveWorkOff makes the server stop taking new work and starts moving its copies elsewhere, one at a
// time, until none that can move is left or the deadline passes. deadline is 1 minute to 2 hours, 0 for
// DefaultMoveDeadline (a field error on deadlineSeconds otherwise). It answers at once with the server's
// view, whose Move says how it goes; the work is in the background, and the server stays ineligible
// when it ends (AcceptWork lets it take work again).
//
// Refusals: an unknown or unmanaged server (ErrServerNotFound), one that isn't ready or that Nomad
// doesn't list or says is down (ErrServerNotReady), one with a move going (ErrServerMoving) and a
// cluster with no other server that accepts work (ErrNoOtherServer).
func (s *Service) MoveWorkOff(ctx context.Context, name string, deadline time.Duration) (ServerView, error) {
	if deadline == 0 {
		deadline = DefaultMoveDeadline
	}
	if deadline < s.move.minDeadline || deadline > maxMoveDeadline {
		return ServerView{}, app.NewFieldError("deadlineSeconds", "must be between %d and %d seconds", int(s.move.minDeadline/time.Second), int(maxMoveDeadline/time.Second))
	}
	s.moveMu.Lock()
	defer s.moveMu.Unlock()

	row, node, nodes, rows, err := s.moveSubject(ctx, name)
	if err != nil {
		return ServerView{}, err
	}
	if open, err := s.store.OpenServerMoves(ctx); err != nil {
		return ServerView{}, fmt.Errorf("move work off %s: %w", name, err)
	} else if slices.ContainsFunc(open, func(m store.ServerMove) bool { return m.ServerIdx == row.Index }) {
		return ServerView{}, ErrServerMoving
	}
	others := moveOthers(rows, nodes, node.ID)
	if len(others) == 0 {
		return ServerView{}, ErrNoOtherServer
	}
	movables, stays, err := s.classifyNode(ctx, node.Name, others)
	if err != nil {
		return ServerView{}, err
	}

	if err := s.deployer.SetNodeEligible(ctx, node.ID, false); err != nil {
		return ServerView{}, err
	}
	now := s.clock.Now().UTC()
	mv, err := s.store.InsertServerMove(ctx, store.ServerMove{
		ID: "mv_" + newTokenID(), ServerIdx: row.Index, State: store.MoveMoving,
		StartedAt: now, DeadlineAt: now.Add(deadline), Left: len(movables), Stays: stays,
	})
	if err != nil {
		// Without the row nobody would move anything: let the server take work again.
		if undoErr := s.deployer.SetNodeEligible(ctx, node.ID, true); undoErr != nil {
			s.log.Warn("couldn't make a server accept work again after a move that didn't start", "server", name, "error", undoErr)
		}
		return ServerView{}, fmt.Errorf("move work off %s: %w", name, err)
	}
	s.noteSetting(ctx, audit.ActionRequested, serverResource(row.ID, row.Name), nil,
		audit.Change{Path: "acceptsWork", Op: audit.OpChanged, From: "true", To: "false"})
	s.startMove(mv)
	s.publish(Change{What: ChangeServers})
	return s.Server(ctx, name)
}

// moveSubject reads the server a move or an accept is about: its row, its Nomad node (up) and what it
// read to find them.
func (s *Service) moveSubject(ctx context.Context, name string) (store.Server, deploy.Node, []deploy.Node, []store.Server, error) {
	row, err := s.store.ServerByName(ctx, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.Server{}, deploy.Node{}, nil, nil, &RefusalError{Field: "name", Err: ErrServerNotFound}
	case err != nil:
		return store.Server{}, deploy.Node{}, nil, nil, fmt.Errorf("server %s: %w", name, err)
	case row.State != store.ServerReady:
		return store.Server{}, deploy.Node{}, nil, nil, ErrServerNotReady
	}
	nodes, err := s.deployer.Nodes(ctx)
	if err != nil {
		return store.Server{}, deploy.Node{}, nil, nil, err
	}
	node := nodeOf(nodes, row.Index)
	if node == nil || node.Down() {
		return store.Server{}, deploy.Node{}, nil, nil, ErrServerNotReady
	}
	rows, err := s.store.ListServers(ctx)
	if err != nil {
		return store.Server{}, deploy.Node{}, nil, nil, fmt.Errorf("server %s: %w", name, err)
	}
	return row, *node, nodes, rows, nil
}

// classifyNode reads the apps and the copies and classifies what is on the node.
func (s *Service) classifyNode(ctx context.Context, nodeName string, others []moveTarget) ([]movable, []store.MoveStay, error) {
	apps, err := s.store.ListApps(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list apps: %w", err)
	}
	byJob, err := s.deployer.AllCopies(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list copies: %w", err)
	}
	m, st := classifyCopies(apps, byJob, nodeName, others)
	return m, st, nil
}

// startMove runs the move in the background and registers it so AcceptWork can cancel it. The
// goroutine settles the move's row, and closes done after.
func (s *Service) startMove(mv store.ServerMove) {
	ctx, cancel := context.WithCancel(s.background())
	run := &moveRun{cancel: cancel, done: make(chan struct{})}
	s.moveRunsMu.Lock()
	s.moveRuns[mv.ID] = run
	s.moveRunsMu.Unlock()
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		defer close(run.done)
		defer func() {
			s.moveRunsMu.Lock()
			delete(s.moveRuns, mv.ID)
			s.moveRunsMu.Unlock()
		}()
		s.runMove(ctx, run, mv)
	}()
}

// runMove moves the server's movable copies until none is left or the deadline passes, and settles
// the row. It ends without settling when the process is shutting down (ctx cancelled and the owner
// didn't ask): the move stays open and the next start resumes it.
func (s *Service) runMove(ctx context.Context, run *moveRun, mv store.ServerMove) {
	moved, left := mv.Moved, mv.Left
	stays := mv.Stays
	// The time left is read off the service's clock once; the context counts it down.
	dctx, cancelDeadline := context.WithTimeout(ctx, mv.DeadlineAt.Sub(s.clock.Now()))
	defer cancelDeadline()

	settle := func(state string, errText string) {
		// Not the move's context: the row is settled even when it was the cancel that ended the move.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.store.EndServerMove(sctx, mv.ID, state, s.clock.Now().UTC(), moved, left, errText); err != nil {
			s.log.Error("settle a server move", "move", mv.ID, "state", state, "error", err)
		}
		s.publish(Change{What: ChangeServers})
		s.publish(Change{What: ChangeAttention})
	}
	progress := func() {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.store.UpdateServerMove(pctx, mv.ID, moved, left, stays); err != nil {
			s.log.Warn("record a server move's progress", "move", mv.ID, "error", err)
		}
		s.publish(Change{What: ChangeServers})
	}
	// ended settles the move and reports true when it must stop: the owner cancelled it, or its deadline
	// passed. A process shutting down stops it without settling.
	ended := func() bool {
		switch {
		case run.cancelled.Load():
			settle(store.MoveCancelled, "")
		case ctx.Err() != nil:
			// The process is shutting down: the row stays open for the next start.
		case dctx.Err() != nil:
			settle(store.MoveIncomplete, deadlineSentence(left))
		default:
			return false
		}
		return true
	}

	for !ended() {
		row, err := s.store.ServerByIndex(dctx, mv.ServerIdx)
		if err != nil {
			if dctx.Err() != nil {
				continue
			}
			if errors.Is(err, store.ErrNotFound) {
				settle(store.MoveIncomplete, "The server is no longer in the cluster.")
				return
			}
			s.log.Warn("server move: read the server", "move", mv.ID, "error", err)
			sleepCtx(dctx, s.move.pollGap)
			continue
		}
		nodes, err := s.deployer.Nodes(dctx)
		if err != nil {
			sleepCtx(dctx, s.move.pollGap) // Nomad not answering for a moment: the deadline bounds it
			continue
		}
		node := nodeOf(nodes, row.Index)
		if node == nil {
			settle(store.MoveIncomplete, "Nomad no longer lists the server.")
			return
		}
		rows, _ := s.store.ListServers(dctx)
		movables, nowStays, err := s.classifyNode(dctx, node.Name, moveOthers(rows, nodes, node.ID))
		if err != nil {
			sleepCtx(dctx, s.move.pollGap)
			continue
		}
		left, stays = len(movables), nowStays
		if left == 0 {
			progress()
			settle(store.MoveDone, "")
			return
		}
		progress()

		progressed := false
		for _, m := range movables {
			if ended() {
				return
			}
			if s.moveOne(dctx, *node, m) {
				moved++
				left--
				progressed = true
				progress()
			}
		}
		if !progressed {
			sleepCtx(dctx, s.move.pollGap)
		}
	}
}

// moveOne moves one copy: it takes the app's lock (a busy app is skipped until the next round), stops
// the copy, and waits for its replacement to run and pass its checks. It reports whether the copy moved.
// Nothing here is logged but IDs and Caravana's own words.
func (s *Service) moveOne(ctx context.Context, node deploy.Node, m movable) bool {
	state, err := s.tryLockApp(m.AppID)
	if err != nil {
		return false // busy, or deleted: the next round looks again
	}
	defer state.mu.Unlock()
	if err := s.deployer.MoveCopy(ctx, m.CopyID); err != nil {
		if ctx.Err() == nil && !errors.Is(err, deploy.ErrCopyNotFound) {
			s.log.Warn("server move: couldn't move a copy", "app", m.AppID, "service", m.Service, "copy", m.CopyID, "error", err)
		}
		return false
	}
	wctx, cancel := context.WithTimeout(ctx, s.move.copyWait)
	defer cancel()
	if _, err := s.deployer.WaitCopyMoved(wctx, m.AppID, m.Group, m.CopyID, node.ID, m.Checked); err != nil {
		if ctx.Err() == nil {
			s.log.Warn("server move: a copy's replacement didn't come in time", "app", m.AppID, "service", m.Service, "copy", m.CopyID)
		}
		return false
	}
	s.publish(Change{AppID: m.AppID, What: ChangeCopies})
	return true
}

// deadlineSentence is the incomplete move's one sentence.
func deadlineSentence(left int) string {
	return copyCount(left) + " had nowhere to go, or their apps were busy, within the deadline."
}

// sleepCtx waits for d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// resumeMoves goes on with every move a previous process left open: the server is still ineligible,
// so it only needs its copies moved again, with the stored deadline. A move whose deadline has passed
// is settled incomplete.
func (s *Service) resumeMoves(ctx context.Context) error {
	open, err := s.store.OpenServerMoves(ctx)
	if err != nil {
		return fmt.Errorf("resume server moves: %w", err)
	}
	for _, mv := range open {
		if !mv.DeadlineAt.After(s.clock.Now()) {
			if err := s.store.EndServerMove(ctx, mv.ID, store.MoveIncomplete, s.clock.Now().UTC(), mv.Moved, mv.Left, "Caravana restarted during it."); err != nil {
				return fmt.Errorf("resume server moves: %w", err)
			}
			continue
		}
		s.startMove(mv)
	}
	return nil
}

// AcceptWork makes a server take work again: a move that is going is cancelled (the copies already
// moved stay where they are) and the node is made eligible. For a server with no move going it only
// makes the node eligible, as after a move that ended or for a server somebody made ineligible
// outside Caravana. Refusals: an unknown or unmanaged server (ErrServerNotFound) and one that isn't
// ready or that Nomad can't see (ErrServerNotReady).
func (s *Service) AcceptWork(ctx context.Context, name string) (ServerView, error) {
	row, node, _, _, err := s.moveSubject(ctx, name)
	if err != nil {
		return ServerView{}, err
	}
	// The cancel and the eligibility change are one act: wait for the move to settle its row before the
	// node takes work, or a copy being moved could be put back on the server.
	s.moveRunsMu.Lock()
	var runs []*moveRun
	open, err := s.store.OpenServerMoves(ctx)
	if err != nil {
		s.moveRunsMu.Unlock()
		return ServerView{}, fmt.Errorf("accept work on %s: %w", name, err)
	}
	for _, mv := range open {
		if mv.ServerIdx != row.Index {
			continue
		}
		if run, ok := s.moveRuns[mv.ID]; ok {
			run.cancelled.Store(true)
			run.cancel()
			runs = append(runs, run)
		} else {
			// No goroutine holds it (it just ended, or never started): settle the row here.
			if err := s.store.EndServerMove(ctx, mv.ID, store.MoveCancelled, s.clock.Now().UTC(), mv.Moved, mv.Left, ""); err != nil && !errors.Is(err, store.ErrNotFound) {
				s.moveRunsMu.Unlock()
				return ServerView{}, fmt.Errorf("accept work on %s: %w", name, err)
			}
		}
	}
	s.moveRunsMu.Unlock()
	for _, run := range runs {
		select {
		case <-run.done:
		case <-ctx.Done():
			return ServerView{}, ctx.Err()
		}
	}
	if err := s.deployer.SetNodeEligible(ctx, node.ID, true); err != nil {
		return ServerView{}, err
	}
	s.noteSetting(ctx, audit.ActionUpdated, serverResource(row.ID, row.Name), nil,
		audit.Change{Path: "acceptsWork", Op: audit.OpChanged, From: "false", To: "true"})
	s.publish(Change{What: ChangeServers})
	s.publish(Change{What: ChangeAttention})
	return s.Server(ctx, name)
}
