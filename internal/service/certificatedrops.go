package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nodeagent"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Dropping the earlier certificates an upload covers (feature 056). Traefik keeps serving an ACME
// certificate it already holds, and one for an exact name beats an uploaded wildcard (feature 034,
// research R5). Its store is a folder only root reads on the ingress server, so the console hands that
// server's agent a request in its sync answer (an ID and the names of the uploads, nothing else); the
// agent removes the certificates those names cover and tells the outcome in its next report; Caravana
// then restarts Traefik once and calls the drop done when Traefik's task has started again.

// The timing of a drop.
const (
	// certDropPickupDeadline is how long a requested drop may wait for the agent to be handed it (every
	// 30 s sync); certDropRunDeadline how long a running one may wait for its outcome; and
	// certDropRestartDeadline how long Traefik may take to start again after the store was rewritten.
	certDropPickupDeadline  = 10 * time.Minute
	certDropRunDeadline     = 10 * time.Minute
	certDropRestartDeadline = 10 * time.Minute
	// defaultCertDropGap is how often the pass looks for drops that never got going or never ended.
	defaultCertDropGap = time.Minute
	// defaultCertDropPoll and certDropWatch are how often, and for how long at most, one restart waits for
	// Traefik's task to start before it leaves the drop to the pass.
	defaultCertDropPoll = 2 * time.Second
	certDropWatch       = 2 * time.Minute
	// certDropHistory is how many drops are listed.
	certDropHistory = 20
	// maxDropNames is the most names a request carries (the agent's limit).
	maxDropNames = 1000
	// maxDropEntry is the longest entry an agent may report for one dropped certificate.
	maxDropEntry = 4096
)

// The sentences of a drop that didn't end well.
const (
	reasonDropNotPicked   = "The server's agent didn't pick it up (is caravana-node.timer running?)"
	reasonDropNoOutcome   = "The server's agent never told how it went."
	reasonDropNoUploads   = "No uploaded certificate is left: there is nothing to drop."
	reasonDropRefused     = "The server's agent refused the request."
	reasonDropAgentFailed = "The server's agent couldn't change Traefik's certificate store."
	reasonDropRestart     = "The earlier certificates were removed from Traefik's store, but Traefik couldn't be restarted: it still serves them, and writes them back when it next saves its store. Drop again once Traefik runs."
)

// The refusals of RequestCertificateDrop. A caller tests them with errors.Is.
var (
	// ErrNoUploadedCertificates: no uploaded certificate has a name an agent may be asked to drop.
	ErrNoUploadedCertificates = errors.New("no certificate is uploaded: there is nothing to drop")
	// ErrCertificateDropRunning: a drop is already requested, running or restarting.
	ErrCertificateDropRunning = errors.New("a drop of the earlier certificates is already under way")
	// ErrCertificateDropNotFound: no drop has that ID.
	ErrCertificateDropNotFound = errors.New("no such drop of the earlier certificates")
)

// DropNoAgentError is ErrNoAgent for a drop: the ingress server has no agent that holds Traefik's store
// and reports lately. Server is its name, "" when no managed server runs the ingress (the lab's node).
type DropNoAgentError struct{ Server string }

func (e *DropNoAgentError) Error() string {
	if e.Server == "" {
		return "No server's agent holds Traefik's certificate store: run the installer on the server."
	}
	return "Server " + e.Server + "'s agent doesn't report Traefik's certificate store: run the installer on server " + e.Server + "."
}

// Is makes errors.Is(err, ErrNoAgent) true.
func (e *DropNoAgentError) Is(target error) bool { return target == ErrNoAgent }

// NodeCertificateDrop is the certificateDrop block of a sync answer: drop the stored ACME certificates
// whose every name Names covers. Names are lowercase host names and wildcards; it never carries a path
// or a certificate.
type NodeCertificateDrop struct {
	ID    string   `json:"id"`
	Names []string `json:"names"`
}

// NodeCertificateOutcome is the certificateDrop block of a report: how the last request the agent handled
// went. Dropped lists each certificate removed as its names joined with "+", main first.
type NodeCertificateOutcome struct {
	ID      string   `json:"id"`
	State   string   `json:"state"` // running, dropped, nothing, failed or refused
	Dropped []string `json:"dropped,omitempty"`
	Reason  string   `json:"reason,omitempty"`
}

// validateCertificateOutcome checks a report's certificateDrop block: an ID of 32 lowercase hex digits,
// one of the five states, at most 100 entries each of host names (the agent's rule) joined with "+", and
// a reason that is printable and short. A block that isn't is a field error naming the first thing wrong.
func validateCertificateOutcome(o *NodeCertificateOutcome) error {
	if o == nil {
		return nil
	}
	if !spaceCleanRE.MatchString(o.ID) {
		return app.NewFieldError("certificateDrop.id", "must be 32 lowercase hex digits")
	}
	if !slices.Contains([]string{"running", "dropped", "nothing", "failed", "refused"}, o.State) {
		return app.NewFieldError("certificateDrop.state", "must be running, dropped, nothing, failed or refused")
	}
	if len(o.Dropped) > 100 {
		return app.NewFieldError("certificateDrop.dropped", "has more than 100 entries")
	}
	for i, entry := range o.Dropped {
		field := fmt.Sprintf("certificateDrop.dropped[%d]", i)
		if len(entry) > maxDropEntry {
			return app.NewFieldError(field, "is longer than %d characters", maxDropEntry)
		}
		for _, name := range strings.Split(entry, "+") {
			if !nodeagent.ValidDropName(name) {
				return app.NewFieldError(field, "must be host names joined with +")
			}
		}
	}
	return checkTextMax("certificateDrop.reason", o.Reason, 300)
}

// SetCertificateDropPassGap sets how often the pass of certificate drops runs (a minute by default) and
// how often a restart looks at Traefik while it starts (two seconds by default); either that isn't
// positive keeps its default. Call it before Start.
func (s *Service) SetCertificateDropPassGap(gap, poll time.Duration) {
	if gap > 0 {
		s.certDrop.gap = gap
	}
	if poll > 0 {
		s.certDrop.poll = poll
	}
}

// certDropState is what the service keeps in memory about drops: the timing and the drops whose restart
// is being driven, so the hand-off and the pass never drive one twice.
type certDropState struct {
	gap, poll time.Duration
	mu        sync.Mutex
	driving   map[string]bool
}

func newCertDropState() certDropState {
	return certDropState{gap: defaultCertDropGap, poll: defaultCertDropPoll, driving: map[string]bool{}}
}

func newCertDropID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate a certificate drop ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// uploadedNames is every name of every uploaded certificate, lowercase, sorted, once each.
func (s *Service) uploadedNames(ctx context.Context) ([]string, error) {
	list, err := s.store.ListUploadedCertificates(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, c := range list {
		for _, n := range c.Names {
			names = append(names, strings.ToLower(strings.TrimSpace(n)))
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// dropNames is what an agent is asked to cover: the uploaded names the agent's rule accepts (an
// uploaded certificate may name an address or something odd, which no ACME certificate has), sorted,
// once each, at most 1000.
func (s *Service) dropNames(ctx context.Context) ([]string, error) {
	all, err := s.uploadedNames(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, n := range all {
		if nodeagent.ValidDropName(n) {
			out = append(out, n)
		}
	}
	if len(out) > maxDropNames {
		out = out[:maxDropNames]
	}
	return out, nil
}

// ingressAgent is the ingress server's row (the first server) and its last report when that is an agent
// that holds Traefik's store and reported lately; otherwise the refusal.
func ingressAgent(rows []store.Server, now time.Time) (store.Server, error) {
	i := slices.IndexFunc(rows, func(r store.Server) bool { return r.First })
	if i < 0 {
		return store.Server{}, &DropNoAgentError{}
	}
	row := rows[i]
	if row.State != store.ServerReady || now.Sub(row.SeenAt) > agentFresh {
		return store.Server{}, &DropNoAgentError{Server: row.Name}
	}
	if report, ok := parseReport(row.Report); !ok || !report.TraefikStore {
		return store.Server{}, &DropNoAgentError{Server: row.Name}
	}
	return row, nil
}

// RequestCertificateDrop asks the ingress server's agent to remove from Traefik's store the ACME
// certificates the uploaded ones cover: it writes a drop in state requested, which the next sync of the
// agent hands out with the names. An admin only. It is refused when no certificate is uploaded
// (ErrNoUploadedCertificates), when the ingress server has no agent reporting that it holds the store
// (ErrNoAgent: run the installer on it), and while another drop is open (ErrCertificateDropRunning).
func (s *Service) RequestCertificateDrop(ctx context.Context) (store.CertificateDrop, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return store.CertificateDrop{}, err
	}
	s.serverMu.Lock()
	defer s.serverMu.Unlock()
	names, err := s.dropNames(ctx)
	if err != nil {
		return store.CertificateDrop{}, fmt.Errorf("request a certificate drop: %w", err)
	}
	if len(names) == 0 {
		return store.CertificateDrop{}, ErrNoUploadedCertificates
	}
	rows, err := s.store.ListServers(ctx)
	if err != nil {
		return store.CertificateDrop{}, fmt.Errorf("request a certificate drop: %w", err)
	}
	now := s.clock.Now().UTC()
	row, err := ingressAgent(rows, now)
	if err != nil {
		return store.CertificateDrop{}, err
	}
	id, err := newCertDropID()
	if err != nil {
		return store.CertificateDrop{}, err
	}
	d, err := s.store.CreateCertificateDrop(ctx, store.CertificateDrop{
		ID: id, ServerIdx: row.Index, ServerName: row.Name, Actor: callerEmail(ctx), RequestedAt: now,
	})
	if errors.Is(err, store.ErrCertificateDropOpen) {
		return store.CertificateDrop{}, ErrCertificateDropRunning
	}
	if err != nil {
		return store.CertificateDrop{}, fmt.Errorf("request a certificate drop: %w", err)
	}
	s.publish(Change{What: ChangeServers})
	s.noteSetting(ctx, audit.ActionRequested, settingsResource("certificates"),
		&audit.Record{Kind: audit.RecordCertificateDrop, ID: d.ID},
		audit.Change{Path: "drop", Op: audit.OpAdded, To: "on server " + d.ServerName})
	return d, nil
}

// CertificateDrops lists the drops, newest first, at most 20. An admin only.
func (s *Service) CertificateDrops(ctx context.Context) ([]store.CertificateDrop, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return nil, err
	}
	return s.store.ListCertificateDrops(ctx, certDropHistory)
}

// CertificateDrop reads one drop. An ID that isn't one is ErrCertificateDropNotFound. An admin only.
func (s *Service) CertificateDrop(ctx context.Context, id string) (store.CertificateDrop, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return store.CertificateDrop{}, err
	}
	d, err := s.store.GetCertificateDrop(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.CertificateDrop{}, &RefusalError{Field: "id", Err: ErrCertificateDropNotFound}
	}
	return d, err
}

// certificateDropHandoff is what a sync does about drops, called after the report is stored and with
// serverMu held. Only the ingress server's agent is concerned: for any other the answer has no block.
//
//	(a) A report whose certificateDrop is the open drop's, and final, settles it: dropped (even with no
//	    names: the files may have been rewritten) goes on to restarting, nothing ends it done, failed and
//	    refused end it failed with the agent's sentence.
//	(b) A drop still requested is marked running.
//	(c) While one is requested or running, the answer carries it with the names of the uploaded
//	    certificates, read now. With none left the drop ends there.
//
// The sync never fails for it: what goes wrong is logged, and the answer has no block.
func (s *Service) certificateDropHandoff(ctx context.Context, row store.Server, report NodeReport, now time.Time) *NodeCertificateDrop {
	if !row.First {
		return nil
	}
	open, ok, err := s.store.OpenCertificateDrop(ctx)
	if err != nil {
		s.log.Warn("certificate drop: read the open drop", "server", row.Name, "error", err)
		return nil
	}
	if !ok || open.ServerIdx != row.Index || open.Status == store.DropRestarting {
		return nil
	}
	if o := report.CertificateDrop; o != nil && o.ID == open.ID && o.State != "running" {
		s.settleFromOutcome(ctx, open, o, now)
		return nil
	}
	names, err := s.dropNames(ctx)
	if err != nil {
		s.log.Warn("certificate drop: read the uploaded certificates", "error", err)
		return nil
	}
	if len(names) == 0 {
		s.settleDrop(ctx, open, store.DropFailed, now, reasonDropNoUploads)
		return nil
	}
	if open.Status == store.DropRequested {
		if err := s.store.StartCertificateDrop(ctx, open.ID, now); err != nil {
			s.log.Warn("certificate drop: mark it running", "server", row.Name, "error", err)
			return nil
		}
		s.publish(Change{What: ChangeServers})
	}
	return &NodeCertificateDrop{ID: open.ID, Names: names}
}

// settleFromOutcome ends or advances an open drop with the agent's final outcome.
func (s *Service) settleFromOutcome(ctx context.Context, open store.CertificateDrop, o *NodeCertificateOutcome, now time.Time) {
	switch o.State {
	case "dropped":
		if err := s.store.RestartCertificateDrop(ctx, open.ID, now, o.Dropped); err != nil {
			s.log.Warn("certificate drop: go on to restarting Traefik", "error", err)
			return
		}
		s.publish(Change{What: ChangeServers})
		open.Status, open.RewrittenAt, open.Dropped = store.DropRestarting, now, o.Dropped
		s.startDropRestart(open)
	case "nothing":
		s.settleDrop(ctx, open, store.DropDone, now, "")
	default: // failed, refused
		reason := o.Reason
		if reason == "" {
			reason = reasonDropAgentFailed
			if o.State == "refused" {
				reason = reasonDropRefused
			}
		}
		s.settleDrop(ctx, open, store.DropFailed, now, reason)
	}
}

// settleDrop ends an open drop and tells the pages. A failure to write is logged: the next sync or pass
// finds the drop still open and tries again.
func (s *Service) settleDrop(ctx context.Context, d store.CertificateDrop, status string, at time.Time, reason string) {
	if err := s.store.SettleCertificateDrop(ctx, d.ID, status, at, reason); err != nil {
		s.log.Warn("certificate drop: settle it", "status", status, "error", err)
		return
	}
	s.publish(Change{What: ChangeServers})
}

// startDropRestart restarts Traefik for a drop whose store is rewritten, in the background on the
// service's own context: a sync must not wait for it. At most one runs per drop.
func (s *Service) startDropRestart(d store.CertificateDrop) {
	s.certDrop.mu.Lock()
	if s.certDrop.driving[d.ID] {
		s.certDrop.mu.Unlock()
		return
	}
	s.certDrop.driving[d.ID] = true
	s.certDrop.mu.Unlock()
	poll := s.certDrop.poll
	s.backupWatcher.Go(func() {
		defer func() {
			s.certDrop.mu.Lock()
			delete(s.certDrop.driving, d.ID)
			s.certDrop.mu.Unlock()
		}()
		s.driveDropRestart(s.bg, d, poll)
	})
}

// driveDropRestart restarts Traefik's allocation (every task, in place) unless it started after the store
// was rewritten already, then waits for its task to start and settles the drop done. It gives up quietly
// after certDropWatch (the pass tries again) and when the restart can't be asked for.
func (s *Service) driveDropRestart(ctx context.Context, d store.CertificateDrop, poll time.Duration) {
	started, proxy, err := s.proxyStartedSince(ctx, d.RewrittenAt)
	if err != nil {
		s.log.Warn("certificate drop: look at Traefik", "error", err)
		return
	}
	if !started {
		if err := s.deployer.RestartCopy(ctx, proxy.ID); err != nil {
			s.log.Warn("certificate drop: restart Traefik; the pass tries again", "error", err)
			return
		}
	}
	deadline := time.NewTimer(certDropWatch)
	defer deadline.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for !started {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
		}
		if started, _, err = s.proxyStartedSince(ctx, d.RewrittenAt); err != nil {
			s.log.Warn("certificate drop: look at Traefik", "error", err)
		}
	}
	s.finishDrop(ctx, d)
}

// proxyStartedSince says Traefik's running copy started after t, and returns the copy.
func (s *Service) proxyStartedSince(ctx context.Context, t time.Time) (bool, deploy.Copy, error) {
	proxy, err := s.deployer.ProxyCopy(ctx)
	if err != nil {
		return false, deploy.Copy{}, err
	}
	return proxy.StartedAt != nil && proxy.StartedAt.After(t), proxy, nil
}

// finishDrop ends a restarting drop done and checks the hosts the uploads cover again, so the uploaded
// rows update without waiting for the watcher.
func (s *Service) finishDrop(ctx context.Context, d store.CertificateDrop) {
	s.settleDrop(ctx, d, store.DropDone, s.clock.Now().UTC(), "")
	names, err := s.uploadedNames(ctx)
	if err != nil {
		s.log.Warn("certificate drop: read the uploaded certificates", "error", err)
		return
	}
	if len(names) > 0 {
		s.scheduleCertRecheck(names)
	}
}

// runCertificateDropPass runs certificateDropPass at start and then every gap, until ctx ends.
func (s *Service) runCertificateDropPass(ctx context.Context, gap time.Duration) {
	t := time.NewTicker(gap)
	defer t.Stop()
	for {
		s.certificateDropPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// certificateDropPass gives up on the drops that never got going or never ended: a requested one the
// agent never picked up fails, a running one with no outcome is lost, and one restarting is done when
// Traefik has started since the rewrite, failed after ten minutes, and otherwise restarted again.
func (s *Service) certificateDropPass(ctx context.Context) {
	d, ok, err := s.store.OpenCertificateDrop(ctx)
	if err != nil {
		s.log.Warn("certificate drop pass: read the open drop", "error", err)
		return
	}
	if !ok {
		return
	}
	now := s.clock.Now().UTC()
	switch {
	case d.Status == store.DropRequested && now.Sub(d.RequestedAt) > certDropPickupDeadline:
		s.settleDrop(ctx, d, store.DropFailed, now, reasonDropNotPicked)
	case d.Status == store.DropRunning && now.Sub(dropStartOf(d)) > certDropRunDeadline:
		s.settleDrop(ctx, d, store.DropLost, now, reasonDropNoOutcome)
	case d.Status == store.DropRestarting:
		if started, _, err := s.proxyStartedSince(ctx, d.RewrittenAt); err == nil && started {
			s.finishDrop(ctx, d)
		} else if now.Sub(d.RewrittenAt) > certDropRestartDeadline {
			s.settleDrop(ctx, d, store.DropFailed, now, reasonDropRestart)
		} else {
			s.startDropRestart(d)
		}
	}
}

// dropStartOf is when a running drop was handed to its agent (its request time when that isn't known).
func dropStartOf(d store.CertificateDrop) time.Time {
	if d.StartedAt.IsZero() {
		return d.RequestedAt
	}
	return d.StartedAt
}
