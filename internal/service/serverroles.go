package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// maxNomadServers is how many Nomad servers a cluster wants: three survive the loss of one.
const maxNomadServers = 3

// ServerFacts is what the choice of Nomad servers reads about one server.
type ServerFacts struct {
	Index    int
	First    bool
	Ready    bool      // the server's checks passed
	Wanted   bool      // Caravana already wants it as a Nomad server
	Running  bool      // its last report says its Nomad server is a raft peer
	JoinedAt time.Time // when it joined: the older ones are promoted first
}

// wantedServers is the set of Nomad servers Caravana wants, by index, sorted. It only ever adds:
//   - the servers already wanted stay (a removed server isn't in the list, so its place is free);
//   - the first server is always one;
//   - with fewer than three ready servers nothing is added;
//   - with three or more, while fewer than three are wanted and every wanted one reports its Nomad
//     server running, the oldest ready server that isn't one is added: one per call, so servers are
//     promoted one at a time and raft never has two new voters at once.
func wantedServers(servers []ServerFacts) []int {
	var wanted []int
	running := true
	ready := 0
	for _, s := range servers {
		if s.Ready {
			ready++
		}
		if s.Wanted || s.First {
			wanted = append(wanted, s.Index)
			if !s.Running {
				running = false
			}
		}
	}
	if ready >= maxNomadServers && len(wanted) < maxNomadServers && running {
		var next *ServerFacts
		for i := range servers {
			s := &servers[i]
			if !s.Ready || s.Wanted || s.First {
				continue
			}
			if next == nil || s.JoinedAt.Before(next.JoinedAt) || (s.JoinedAt.Equal(next.JoinedAt) && s.Index < next.Index) {
				next = s
			}
		}
		if next != nil {
			wanted = append(wanted, next.Index)
		}
	}
	slices.Sort(wanted)
	return wanted
}

// reconcileRoles makes the stored roles what wantedServers says. The caller holds serverMu.
func (s *Service) reconcileRoles(ctx context.Context) error {
	rows, err := s.store.ListServers(ctx)
	if err != nil {
		return fmt.Errorf("reconcile roles: %w", err)
	}
	facts := make([]ServerFacts, 0, len(rows))
	for _, r := range rows {
		f := ServerFacts{Index: r.Index, First: r.First, Ready: r.State == store.ServerReady, Wanted: r.NomadServer, JoinedAt: r.JoinedAt}
		if rep, ok := parseReport(r.Report); ok {
			f.Running = rep.ServerRunning
		}
		facts = append(facts, f)
	}
	changed := false
	for _, idx := range wantedServers(facts) {
		i := slices.IndexFunc(rows, func(r store.Server) bool { return r.Index == idx })
		if i < 0 || rows[i].NomadServer {
			continue
		}
		if err := s.store.SetNomadServer(ctx, idx, true); err != nil {
			return fmt.Errorf("reconcile roles: %w", err)
		}
		changed = true
	}
	if changed {
		s.publish(Change{What: ChangeServers})
	}
	return nil
}
