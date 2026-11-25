package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Clean-ups of a server's unused images (feature 045, plan D3). A clean-up is a record the agent is
// handed in its sync answer, with the digests to keep; the agent removes the images no container uses and
// that list doesn't name, and tells the outcome in its next report. One per managed server runs daily
// (a setting, on by default) and when the owner asks. Caravana never touches Docker itself.

// The timing of a clean-up.
const (
	// cleanupPickupDeadline is how long a requested clean-up may wait for the agent to be handed it
	// (every 30 s sync) before it is given up.
	cleanupPickupDeadline = 10 * time.Minute
	// cleanupRunDeadline is how long a running clean-up may wait for its outcome: the agent's own budget
	// is 10 minutes, and its report comes with the sync after.
	cleanupRunDeadline = 30 * time.Minute
	// defaultCleanupGap is how often the pass looks for clean-ups that never got going or never reported.
	defaultCleanupGap = time.Minute
	// dailyCleanupHour is the UTC hour from which the daily clean-up is requested.
	dailyCleanupHour = 4
	// agentFresh is how recent a server's report must be for its agent to count as reporting.
	agentFresh = 10 * time.Minute
	// cleanupHistory is how many clean-ups of a server are listed.
	cleanupHistory = 20
)

// The sentences of a clean-up that didn't end well.
const (
	reasonCleanupNotPicked   = "The server's agent didn't pick it up (is caravana-node.timer running?)"
	reasonCleanupNoOutcome   = "The server's agent never told how it went."
	reasonCleanupInterrupted = "Interrupted on the server."
)

// The refusals of RequestCleanup. A caller tests them with errors.Is.
var (
	// ErrNoAgent: the server has no agent reporting its images (an unmanaged node, a server that hasn't
	// joined, an agent older than the feature, one that hasn't reported lately). A *NoAgentError.
	ErrNoAgent = errors.New("the server has no agent reporting its images")
	// ErrCleanupRunning: a clean-up of the server is already requested or running. A *CleanupRunningError.
	ErrCleanupRunning = errors.New("a clean-up of the server is already under way")
	// ErrCleanupNotFound: no clean-up has that ID on that server.
	ErrCleanupNotFound = errors.New("no such clean-up")
)

// NoAgentError is ErrNoAgent in the words the owner reads.
type NoAgentError struct{ Server string }

func (e *NoAgentError) Error() string {
	return "Server " + e.Server + " has no agent reporting its images: run the installer on it."
}

// Is makes errors.Is(err, ErrNoAgent) true.
func (e *NoAgentError) Is(target error) bool { return target == ErrNoAgent }

// CleanupRunningError is ErrCleanupRunning with the server's name.
type CleanupRunningError struct{ Server string }

func (e *CleanupRunningError) Error() string {
	return "A clean-up of server " + e.Server + " is already under way."
}

// Is makes errors.Is(err, ErrCleanupRunning) true.
func (e *CleanupRunningError) Is(target error) bool { return target == ErrCleanupRunning }

// SetCleanupPassGap sets how often the pass of clean-ups runs (a minute by default). Call it before
// Start; a value that isn't positive keeps the default.
func (s *Service) SetCleanupPassGap(d time.Duration) {
	if d > 0 {
		s.cleanupGap = d
	}
}

// newCleanupID is 32 lowercase hex characters: the agent's request ID.
func newCleanupID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate a clean-up ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// agentReporting says the server's last report came from an agent that tells its images: the report has
// a space block and is fresh. A server that isn't ready has none worth trusting.
func agentReporting(row store.Server, now time.Time) (NodeReport, bool) {
	if row.State != store.ServerReady || now.Sub(row.SeenAt) > agentFresh {
		return NodeReport{}, false
	}
	report, ok := parseReport(row.Report)
	return report, ok && report.Space != nil
}

// RequestCleanup asks a managed server's agent to clean up its unused images: it writes a clean-up in
// state requested, which the next sync of the agent hands out with the keep list. The server must be
// known (ErrServerNotFound), report its images (ErrNoAgent), have no clean-up under way
// (ErrCleanupRunning), and the keep list must fit (ErrKeepListTooLong). trigger is store.CleanupByOwner
// or CleanupBySchedule; dockerVolumes also prunes Docker's leftover volumes.
func (s *Service) RequestCleanup(ctx context.Context, serverName, trigger string, dockerVolumes bool) (store.ImageCleanup, error) {
	s.serverMu.Lock()
	defer s.serverMu.Unlock()
	row, err := s.store.ServerByName(ctx, serverName)
	if errors.Is(err, store.ErrNotFound) {
		// The lab's one node has a name and no row: it exists, and has no agent.
		if v, verr := s.Server(ctx, serverName); verr == nil && !v.Managed {
			return store.ImageCleanup{}, &RefusalError{Field: "name", Err: &NoAgentError{Server: serverName}}
		}
		return store.ImageCleanup{}, &RefusalError{Field: "name", Err: ErrServerNotFound}
	}
	if err != nil {
		return store.ImageCleanup{}, fmt.Errorf("request a clean-up: %w", err)
	}
	return s.requestCleanup(ctx, row, trigger, dockerVolumes)
}

// requestCleanup is RequestCleanup for a server row already read; the caller holds serverMu.
func (s *Service) requestCleanup(ctx context.Context, row store.Server, trigger string, dockerVolumes bool) (store.ImageCleanup, error) {
	now := s.clock.Now().UTC()
	if _, ok := agentReporting(row, now); !ok {
		return store.ImageCleanup{}, &RefusalError{Field: "name", Err: &NoAgentError{Server: row.Name}}
	}
	if _, err := s.keepDigests(ctx); err != nil {
		return store.ImageCleanup{}, err
	}
	id, err := newCleanupID()
	if err != nil {
		return store.ImageCleanup{}, err
	}
	c, err := s.store.CreateImageCleanup(ctx, store.ImageCleanup{
		ID: id, ServerIdx: row.Index, ServerName: row.Name, Trigger: trigger, DockerVolumes: dockerVolumes, RequestedAt: now,
	})
	if errors.Is(err, store.ErrCleanupOpen) {
		return store.ImageCleanup{}, &RefusalError{Field: "name", Err: &CleanupRunningError{Server: row.Name}}
	}
	if err != nil {
		return store.ImageCleanup{}, fmt.Errorf("request a clean-up: %w", err)
	}
	s.noteSetting(ctx, audit.ActionRequested, serverResource(row.ID, row.Name), &audit.Record{Kind: audit.RecordCleanup, ID: c.ID},
		audit.Change{Path: "dockerVolumes", Op: audit.OpAdded, To: fmt.Sprint(dockerVolumes)})
	s.publish(Change{What: ChangeServers})
	return c, nil
}

// cleanupHandoff is what a sync does about clean-ups, called after the report is stored and with
// serverMu held (plan D3). (a) A report whose lastClean is this server's open clean-up settles it: done,
// or failed with the agent's error, or failed as interrupted when the agent began it and never ended.
// (b) A clean-up still requested is marked running. (c) While one is open and not reported, the answer
// carries it with the keep list. The sync never fails for it: what goes wrong is logged, and the answer
// simply has no clean block.
func (s *Service) cleanupHandoff(ctx context.Context, row store.Server, report NodeReport, now time.Time) *CleanRequest {
	open, ok, err := s.store.OpenImageCleanup(ctx, row.Index)
	if err != nil {
		s.log.Warn("clean-up: read the open clean-up", "server", row.Name, "error", err)
		return nil
	}
	if !ok {
		return nil
	}
	if last := lastCleanOf(report); last != nil && last.ID == open.ID {
		s.settleFromReport(ctx, open, last, now)
		return nil
	}
	if open.Status == store.CleanupRequested {
		if err := s.store.StartImageCleanup(ctx, open.ID, now); err != nil {
			s.log.Warn("clean-up: mark it running", "server", row.Name, "error", err)
			return nil
		}
		s.publish(Change{What: ChangeServers})
	}
	keep, err := s.keepDigests(ctx)
	if err != nil {
		// The list grew past what fits since it was requested: nothing is handed out, and it ends here.
		s.settleCleanup(ctx, open, store.CleanupFailed, now, 0, 0, 0, err.Error())
		return nil
	}
	return &CleanRequest{ID: open.ID, Keep: keep, DockerVolumes: open.DockerVolumes}
}

// lastCleanOf is the outcome of the agent's last clean-up in a report, nil when it has none.
func lastCleanOf(r NodeReport) *NodeClean {
	if r.Space == nil {
		return nil
	}
	return r.Space.LastClean
}

// settleFromReport ends an open clean-up with the agent's outcome.
func (s *Service) settleFromReport(ctx context.Context, open store.ImageCleanup, last *NodeClean, now time.Time) {
	at := now
	if t, err := time.Parse(time.RFC3339, last.FinishedAt); err == nil {
		at = t.UTC()
	}
	switch {
	case last.FinishedAt == "":
		s.settleCleanup(ctx, open, store.CleanupFailed, now, 0, 0, 0, reasonCleanupInterrupted)
	case last.Error != "":
		s.settleCleanup(ctx, open, store.CleanupFailed, at, int(last.ImagesRemoved), last.BytesFreed, int(last.VolumesRemoved), last.Error)
	default:
		s.settleCleanup(ctx, open, store.CleanupDone, at, int(last.ImagesRemoved), last.BytesFreed, int(last.VolumesRemoved), "")
	}
}

// settleCleanup ends an open clean-up and tells the pages. A failure to write is logged: the next sync or pass
// finds the clean-up still open and tries again.
func (s *Service) settleCleanup(ctx context.Context, c store.ImageCleanup, status string, at time.Time, removed int, freed int64, volumes int, errText string) {
	if err := s.store.SettleImageCleanup(ctx, c.ID, status, at, removed, freed, volumes, errText); err != nil {
		s.log.Warn("clean-up: settle it", "server", c.ServerName, "status", status, "error", err)
		return
	}
	s.publish(Change{What: ChangeServers})
}

// runCleanupPass runs cleanupPass at start and then every gap, until ctx ends.
func (s *Service) runCleanupPass(ctx context.Context, gap time.Duration) {
	t := time.NewTicker(gap)
	defer t.Stop()
	for {
		s.cleanupPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// cleanupPass is the clock of clean-ups: it gives up on the ones that never got going or never reported,
// then requests the day's clean-up of every server when it is due.
func (s *Service) cleanupPass(ctx context.Context) {
	now := s.clock.Now().UTC()
	open, err := s.store.OpenImageCleanups(ctx)
	if err != nil {
		s.log.Warn("clean-up pass: list the open clean-ups", "error", err)
		return
	}
	for _, c := range open {
		switch {
		case c.Status == store.CleanupRequested && now.Sub(c.RequestedAt) > cleanupPickupDeadline:
			s.settleCleanup(ctx, c, store.CleanupFailed, now, 0, 0, 0, reasonCleanupNotPicked)
		case c.Status == store.CleanupRunning && now.Sub(startOf(c)) > cleanupRunDeadline:
			s.settleCleanup(ctx, c, store.CleanupLost, now, 0, 0, 0, reasonCleanupNoOutcome)
		}
	}
	s.dailyCleanups(ctx, now)
}

// startOf is when a running clean-up was handed to its agent (its request time when that isn't known).
func startOf(c store.ImageCleanup) time.Time {
	if c.StartedAt.IsZero() {
		return c.RequestedAt
	}
	return c.StartedAt
}

// dailyCleanups requests a clean-up of every ready server that reports its images, once a day: from 04:00
// UTC, when the setting is on and today's requests weren't made yet. Today's date is written first, so a
// restart after 04:00 never requests twice; a server that can't be asked (a clean-up under way, no agent)
// is skipped, and tomorrow brings another round. The trigger is schedule and Docker's volumes are never
// part of it.
func (s *Service) dailyCleanups(ctx context.Context, now time.Time) {
	settings, err := s.store.CleanupSettings(ctx)
	if err != nil {
		s.log.Warn("clean-up pass: read the settings", "error", err)
		return
	}
	day := now.Format(time.DateOnly)
	if !settings.Automatic || now.Hour() < dailyCleanupHour || settings.LastAutomaticDay == day {
		return
	}
	if err := s.store.SetLastAutomaticDay(ctx, day); err != nil {
		s.log.Warn("clean-up pass: record the day", "error", err)
		return
	}
	s.serverMu.Lock()
	defer s.serverMu.Unlock()
	rows, err := s.store.ListServers(ctx)
	if err != nil {
		s.log.Warn("clean-up pass: list the servers", "error", err)
		return
	}
	for _, row := range rows {
		if _, ok := agentReporting(row, now); !ok {
			continue
		}
		if _, err := s.requestCleanup(ctx, row, store.CleanupBySchedule, false); err != nil &&
			!errors.Is(err, ErrCleanupRunning) {
			s.log.Warn("clean-up pass: request the daily clean-up", "server", row.Name, "error", err)
		}
	}
}

// ServerCleanups lists a server's clean-ups, newest first, at most 20. An unknown or unmanaged server is
// ErrServerNotFound (an unmanaged node has none).
func (s *Service) ServerCleanups(ctx context.Context, serverName string) ([]store.ImageCleanup, error) {
	row, err := s.store.ServerByName(ctx, serverName)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &RefusalError{Field: "name", Err: ErrServerNotFound}
	}
	if err != nil {
		return nil, fmt.Errorf("list clean-ups: %w", err)
	}
	return s.store.ListImageCleanups(ctx, row.Index, cleanupHistory)
}

// ServerCleanup reads one clean-up of a server. One that doesn't exist, or belongs to another server, is
// ErrCleanupNotFound.
func (s *Service) ServerCleanup(ctx context.Context, serverName, id string) (store.ImageCleanup, error) {
	row, err := s.store.ServerByName(ctx, serverName)
	if errors.Is(err, store.ErrNotFound) {
		return store.ImageCleanup{}, &RefusalError{Field: "name", Err: ErrServerNotFound}
	}
	if err != nil {
		return store.ImageCleanup{}, fmt.Errorf("read a clean-up: %w", err)
	}
	c, err := s.store.GetImageCleanup(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && c.ServerIdx != row.Index) {
		return store.ImageCleanup{}, &RefusalError{Field: "id", Err: ErrCleanupNotFound}
	}
	return c, err
}

// CleanupSettings reads the owner's clean-up settings.
func (s *Service) CleanupSettings(ctx context.Context) (store.CleanupSettings, error) {
	return s.store.CleanupSettings(ctx)
}

// PutCleanupSettings stores whether the daily clean-up is on and how many healthy deployments of each app
// keep their images (1 to 10, a field error on keepDeployments otherwise).
func (s *Service) PutCleanupSettings(ctx context.Context, automatic bool, keepDeployments int) (store.CleanupSettings, error) {
	if keepDeployments < 1 || keepDeployments > 10 {
		return store.CleanupSettings{}, app.NewFieldError("keepDeployments", "must be 1 to 10")
	}
	was, _ := s.store.CleanupSettings(ctx)
	if err := s.store.SaveCleanupSettings(ctx, automatic, keepDeployments); err != nil {
		return store.CleanupSettings{}, err
	}
	if changes := audit.Diff(map[string]any{"automatic": was.Automatic, "keepDeployments": was.KeepDeployments},
		map[string]any{"automatic": automatic, "keepDeployments": keepDeployments}); len(changes) > 0 {
		s.noteSetting(ctx, audit.ActionUpdated, settingsResource("cleanup"), nil, changes...)
	} else {
		s.noChange(ctx)
	}
	return s.store.CleanupSettings(ctx)
}

// sortedByBytes orders unused images largest first, then by ID: the order the pages list what a
// clean-up would remove.
func sortedByBytes(images []SpaceImage) {
	sort.SliceStable(images, func(i, j int) bool {
		if images[i].Bytes != images[j].Bytes {
			return images[i].Bytes > images[j].Bytes
		}
		return images[i].ID < images[j].ID
	})
}
