package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Server labels (feature 042). The owner's labels for a server are stored with its row and written to
// its Nomad node as dynamic node metadata, beside the server's name; a service's placement rules read
// them as constraints and affinities. Nomad's metadata is what the scheduler sees, so Caravana keeps it
// equal to the row: at every save, when a server becomes ready, at start and every labelReconcileGap.

// defaultLabelReconcileGap is how often every server's node metadata is read and made to match its row.
const defaultLabelReconcileGap = 5 * time.Minute

// SetLabelReconcileGap sets how often the node metadata is compared with the stored labels (5 minutes
// by default). Call it before Start; a value that isn't positive keeps the default.
func (s *Service) SetLabelReconcileGap(d time.Duration) {
	if d > 0 {
		s.labelGap = d
	}
}

// SetServerLabels replaces a managed server's labels with these and writes them to its Nomad node. The
// names and values are checked first (a field error on labels or labels.<name>). The labels are stored
// before Nomad is asked, so a Nomad that refuses (a token without node:write) or can't be reached
// leaves them stored for the reconcile to apply, and the view says LabelsApplied is false. A write
// Nomad's servers haven't shown yet after 15 seconds is no failure either (deploy.ErrLabelsPending): the
// view says so the same way.
//
// An unknown name, or a Nomad node Caravana doesn't manage, is ErrServerNotFound; a server that hasn't
// joined is ErrServerNotReady.
func (s *Service) SetServerLabels(ctx context.Context, name string, labels map[string]string) (ServerView, error) {
	if err := app.CheckLabels(labels, "labels"); err != nil {
		return ServerView{}, err
	}
	// One writer at a time: the reconcile reads the row and then writes Nomad, and must not write the
	// labels this call replaces after it.
	s.labelMu.Lock()
	row, err := s.store.ServerByName(ctx, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.labelMu.Unlock()
		return ServerView{}, &RefusalError{Field: "name", Err: ErrServerNotFound}
	case err != nil:
		s.labelMu.Unlock()
		return ServerView{}, fmt.Errorf("set server labels: %w", err)
	case row.State == store.ServerWaiting:
		s.labelMu.Unlock()
		return ServerView{}, ErrServerNotReady
	}
	if err := s.store.SetServerLabels(ctx, row.Index, labels); err != nil {
		s.labelMu.Unlock()
		return ServerView{}, fmt.Errorf("set server labels: %w", err)
	}
	row.Labels = labels
	applyErr := s.applyLabels(ctx, row)
	s.labelMu.Unlock()

	s.publish(Change{What: ChangeServers})
	if applyErr != nil && !errors.Is(applyErr, deploy.ErrLabelsPending) && !errors.Is(applyErr, errNoNode) {
		return ServerView{}, applyErr
	}
	return s.Server(ctx, name)
}

// errNoNode says the server has no Nomad node that is up: nothing to write now; the reconcile writes the
// labels when the node is there.
var errNoNode = errors.New("the server has no Nomad node that is up")

// applyLabels writes the row's name and labels to its node. The caller holds labelMu.
func (s *Service) applyLabels(ctx context.Context, row store.Server) error {
	nodes, err := s.deployer.Nodes(ctx)
	if err != nil {
		return err
	}
	node := nodeOf(nodes, row.Index)
	if node == nil || node.Down() {
		return errNoNode
	}
	return s.deployer.SetNodeLabels(ctx, node.ID, row.Name, row.Labels)
}

// reconcileNodeLabels makes the metadata of every managed server's node what its row says: the name and
// the labels, with no other label. A node that is down or that Nomad doesn't list is left for the next
// pass. Failures are logged, never fatal: a Nomad that is down, or a token without node:write, is not a
// reason to stop anything else.
func (s *Service) reconcileNodeLabels(ctx context.Context) {
	rows, err := s.store.ListServers(ctx)
	if err != nil {
		s.log.Warn("reconcile server labels: list servers", "error", err)
		return
	}
	for _, r := range rows {
		if r.State != store.ServerReady || r.Name == "" {
			continue
		}
		if err := s.reconcileServerLabels(ctx, r.Index); err != nil && ctx.Err() == nil {
			s.log.Warn("reconcile server labels", "server", r.Name, "error", err)
		}
	}
}

// reconcileServerLabels is reconcileNodeLabels for one server, read again under labelMu so it never
// writes what a save has just replaced.
func (s *Service) reconcileServerLabels(ctx context.Context, index int) error {
	s.labelMu.Lock()
	defer s.labelMu.Unlock()
	row, err := s.store.ServerByIndex(ctx, index)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil // removed since the list was read
		}
		return err
	}
	nodes, err := s.deployer.Nodes(ctx)
	if err != nil {
		return err
	}
	node := nodeOf(nodes, row.Index)
	if node == nil || node.Down() {
		return nil
	}
	server, labels, err := s.deployer.FreshNodeLabels(ctx, node.ID)
	if err != nil {
		return err
	}
	if server == row.Name && maps.Equal(labels, row.Labels) {
		return nil
	}
	if err := s.deployer.SetNodeLabels(ctx, node.ID, row.Name, row.Labels); err != nil && !errors.Is(err, deploy.ErrLabelsPending) {
		return err
	}
	return nil
}

// reconcileLabelsSoon starts one reconcile in the background, for a server that just became ready.
func (s *Service) reconcileLabelsSoon() {
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		ctx, cancel := context.WithTimeout(s.background(), time.Minute)
		defer cancel()
		s.reconcileNodeLabels(ctx)
	}()
}

// runLabelReconcile reconciles once at start and then every gap, until ctx ends.
func (s *Service) runLabelReconcile(ctx context.Context, gap time.Duration) {
	t := time.NewTicker(gap)
	defer t.Stop()
	for {
		s.reconcileNodeLabels(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// labelsApplied says the node's metadata holds the row's name and exactly its labels, as Nomad's servers
// show it now (the cached record: at most ten minutes old, fresh after a write).
func (s *Service) labelsApplied(ctx context.Context, r store.Server, node *deploy.Node) bool {
	if node == nil || node.Down() {
		return false
	}
	server, labels, err := s.deployer.NodeLabels(ctx, node.ID)
	return err == nil && server == r.Name && maps.Equal(labels, r.Labels)
}
