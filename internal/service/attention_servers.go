package service

import (
	"context"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The server kinds of the needs-you list (feature 031), derived on every read like the rest:
//   - server_down: a ready server whose Nomad node is down, disconnected or gone. Judged only while
//     Nomad's nodes could be read.
//   - server_certificate_expiring: a joining or ready server whose Nomad certificate ends in under 14
//     days (its agent renews it at 30 days, so it couldn't). From the store alone.
//
// The key of both is kind|||<name>: the server's name is the subject (problemSubject).

// serverItems reads the rows and Nomad's nodes. known is false when Nomad's nodes couldn't be read,
// and server_down is then not judged either way.
func (s *Service) serverItems(ctx context.Context, now time.Time) (items []AttentionItem, known bool, err error) {
	rows, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, true, nil // no cluster: nothing to judge, and nothing missing
	}
	nodes, nodesErr := s.deployer.Nodes(ctx)
	if nodesErr != nil {
		s.log.Warn("can't read Nomad's nodes; the needs-you list leaves server_down out", "error", nodesErr)
	}
	return serverItemsOf(now, rows, nodes, nodesErr == nil), nodesErr == nil, nil
}

// serverItemsOf is the pure part. nomadOK is false when the nodes couldn't be read: nodes is then
// ignored and no server is down.
func serverItemsOf(now time.Time, rows []store.Server, nodes []deploy.Node, nomadOK bool) []AttentionItem {
	var items []AttentionItem
	for _, r := range rows {
		if r.State == store.ServerReady && nomadOK {
			if node := nodeOf(nodes, r.Index); node == nil || node.Down() {
				when := r.SeenAt
				if when.IsZero() {
					when = now
				}
				it := AttentionItem{Kind: AttentionServerDown, Server: r.Name, Level: metrics.DiskFailed, When: when}
				it.Detail = plainText(serverDownParts(it))
				items = append(items, it)
			}
		}
		if r.State != store.ServerWaiting && !r.CertNotAfter.IsZero() && r.CertNotAfter.Sub(now) < certWarnWithin {
			end := r.CertNotAfter
			it := AttentionItem{Kind: AttentionServerCertExpiring, Server: r.Name, Level: metrics.DiskWarning, When: now, ExpiresAt: &end}
			it.Detail = plainText(serverCertParts(it))
			items = append(items, it)
		}
	}
	return items
}

// serverDownParts is the item's one wording.
func serverDownParts(it AttentionItem) []TextPart {
	return parts(mono(it.Server), " is down. Its copies are being replaced on other servers; what has a volume there waits for it.")
}

// serverCertParts is the item's one wording: "fsn-2's Nomad certificate ends Nov 29. Its agent
// couldn't renew it; run the installer on it again."
func serverCertParts(it AttentionItem) []TextPart {
	end := "soon"
	if it.ExpiresAt != nil {
		end = it.ExpiresAt.UTC().Format("Jan 2")
	}
	return parts(mono(it.Server), "'s Nomad certificate ends ", end, ". Its agent couldn't renew it; run the installer on it again.")
}
