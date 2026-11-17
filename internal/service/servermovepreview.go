package service

import (
	"context"
	"fmt"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// MovePreviewItem is the copies of one service a move would move.
type MovePreviewItem struct {
	App, Service string
	Copies       int
}

// MovePreview is what MoveWorkOff would do now: the copies it would move, counted by app and service,
// and what would stay with its reason.
type MovePreview struct {
	Moves []MovePreviewItem
	Stays []MoveStay
}

// Copies is how many copies would move.
func (p MovePreview) Copies() int {
	n := 0
	for _, m := range p.Moves {
		n += m.Copies
	}
	return n
}

// PreviewMoveOff reads what moving work off the server would do, with the same classification
// MoveWorkOff uses (classifyCopies), so the confirmation page and the move never disagree. It changes
// nothing. It gives MoveWorkOff's refusals: ErrServerNotFound, ErrServerNotReady, ErrServerMoving and
// ErrNoOtherServer.
func (s *Service) PreviewMoveOff(ctx context.Context, name string) (MovePreview, error) {
	row, node, nodes, rows, err := s.moveSubject(ctx, name)
	if err != nil {
		return MovePreview{}, err
	}
	open, err := s.store.OpenServerMoves(ctx)
	if err != nil {
		return MovePreview{}, fmt.Errorf("preview moving work off %s: %w", name, err)
	}
	if slices.ContainsFunc(open, func(m store.ServerMove) bool { return m.ServerIdx == row.Index }) {
		return MovePreview{}, ErrServerMoving
	}
	others := moveOthers(rows, nodes, node.ID)
	if len(others) == 0 {
		return MovePreview{}, ErrNoOtherServer
	}
	movables, stays, err := s.classifyNode(ctx, node.Name, others)
	if err != nil {
		return MovePreview{}, err
	}
	p := MovePreview{}
	for _, st := range stays {
		p.Stays = append(p.Stays, MoveStay{App: st.App, Service: st.Service, Reason: st.Reason})
	}
	for _, m := range movables {
		i := slices.IndexFunc(p.Moves, func(it MovePreviewItem) bool { return it.App == m.AppID && it.Service == m.Service })
		if i < 0 {
			p.Moves = append(p.Moves, MovePreviewItem{App: m.AppID, Service: m.Service})
			i = len(p.Moves) - 1
		}
		p.Moves[i].Copies++
	}
	return p, nil
}
