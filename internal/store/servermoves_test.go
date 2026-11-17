package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func moveFixture(t *testing.T) (*Store, Server) {
	t.Helper()
	s := openTestStore(t)
	v, err := s.InsertServer(context.Background(), Server{ID: "a", Name: "two", State: ServerReady})
	if err != nil {
		t.Fatal(err)
	}
	return s, v
}

func TestServerMoveLifecycle(t *testing.T) {
	ctx := context.Background()
	s, v := moveFixture(t)
	if _, err := s.NewestServerMove(ctx, v.Index); !errors.Is(err, ErrNotFound) {
		t.Fatalf("NewestServerMove with none = %v, want ErrNotFound", err)
	}
	deadline := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	stays := []MoveStay{{App: "db", Service: "pg", Reason: "its data is on this server"}}
	m, err := s.InsertServerMove(ctx, ServerMove{ID: "mv_1", ServerIdx: v.Index, DeadlineAt: deadline, Stays: stays})
	if err != nil {
		t.Fatalf("InsertServerMove: %v", err)
	}
	if m.State != MoveMoving || m.StartedAt.IsZero() {
		t.Errorf("a new move = %+v, want moving with a start time", m)
	}
	if _, err := s.InsertServerMove(ctx, ServerMove{ID: "mv_1", ServerIdx: v.Index, DeadlineAt: deadline}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("a second mv_1 = %v, want ErrAlreadyExists", err)
	}
	if _, err := s.InsertServerMove(ctx, ServerMove{ID: "mv_x", ServerIdx: 40, DeadlineAt: deadline}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a move of no server = %v, want ErrNotFound", err)
	}

	open, err := s.OpenServerMoves(ctx)
	if err != nil || len(open) != 1 || open[0].ID != "mv_1" {
		t.Fatalf("OpenServerMoves = %+v, %v", open, err)
	}
	if err := s.UpdateServerMove(ctx, "mv_1", 2, 3, stays); err != nil {
		t.Fatalf("UpdateServerMove: %v", err)
	}
	got, err := s.NewestServerMove(ctx, v.Index)
	if err != nil {
		t.Fatal(err)
	}
	if got.Moved != 2 || got.Left != 3 || len(got.Stays) != 1 || got.Stays[0] != stays[0] || !got.DeadlineAt.Equal(deadline) {
		t.Errorf("after an update: %+v", got)
	}
	if err := s.EndServerMove(ctx, "mv_1", MoveMoving, s.now(), 2, 0, ""); err == nil {
		t.Error("ending a move as moving should fail")
	}
	end := s.now()
	if err := s.EndServerMove(ctx, "mv_1", MoveIncomplete, end, 2, 1, "1 copy had nowhere to go within the deadline"); err != nil {
		t.Fatalf("EndServerMove: %v", err)
	}
	got, _ = s.NewestServerMove(ctx, v.Index)
	if got.State != MoveIncomplete || got.Left != 1 || got.Error == "" || !got.EndedAt.Equal(end) {
		t.Errorf("after the end: %+v", got)
	}
	// A settled move never changes again.
	if err := s.EndServerMove(ctx, "mv_1", MoveDone, s.now(), 3, 0, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("settling twice = %v, want ErrNotFound", err)
	}
	if err := s.UpdateServerMove(ctx, "mv_1", 9, 9, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("updating a settled move = %v, want ErrNotFound", err)
	}
	if open, _ := s.OpenServerMoves(ctx); len(open) != 0 {
		t.Errorf("open moves after the end = %+v", open)
	}
}

func TestNewestServerMoveIsTheLatest(t *testing.T) {
	ctx := context.Background()
	s, v := moveFixture(t)
	for _, id := range []string{"mv_a", "mv_b"} {
		if _, err := s.InsertServerMove(ctx, ServerMove{ID: id, ServerIdx: v.Index, DeadlineAt: s.now()}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.NewestServerMove(ctx, v.Index)
	if err != nil || got.ID != "mv_b" {
		t.Errorf("newest = %q, %v, want mv_b", got.ID, err)
	}
}

func TestServerMovesGoWithTheirServer(t *testing.T) {
	ctx := context.Background()
	s, v := moveFixture(t)
	if _, err := s.InsertServerMove(ctx, ServerMove{ID: "mv_1", ServerIdx: v.Index, DeadlineAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteServer(ctx, v.Index); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_moves`).Scan(&n); err != nil || n != 0 {
		t.Errorf("moves after the server was deleted = %d, %v, want 0", n, err)
	}
}
