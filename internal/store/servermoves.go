package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Server moves (feature 042): one row per "move work off this server". A row is written when
// the move starts and settled when it ends. It holds counts, the app and service names of what
// stays and Caravana's own sentences, never anything an app wrote.

// The states of a server move. moving is the open one; the others are final.
const (
	MoveMoving     = "moving"
	MoveDone       = "done"       // nothing movable is left on the server
	MoveIncomplete = "incomplete" // the deadline passed with movable copies still there
	MoveCancelled  = "cancelled"  // the owner accepted work again
)

// MoveStay is one thing a move never moves, with the reason in Caravana's words.
type MoveStay struct {
	App     string `json:"app"`
	Service string `json:"service,omitempty"`
	Reason  string `json:"reason"`
}

// ServerMove is one stored move. A zero EndedAt is "still going".
type ServerMove struct {
	ID         string // "mv_" and 26 lowercase base32 characters
	ServerIdx  int
	State      string
	StartedAt  time.Time
	DeadlineAt time.Time
	EndedAt    time.Time
	Moved      int // copies that left the server
	Left       int // movable copies still there when it ended
	Stays      []MoveStay
	Error      string // one sentence; "" when there is nothing to say
}

const serverMoveColumns = `id, server_idx, state, started_at, deadline_at, ended_at, moved, left, stays, error`

func scanServerMove(r interface{ Scan(...any) error }) (ServerMove, error) {
	var m ServerMove
	var started, deadline, ended, stays string
	if err := r.Scan(&m.ID, &m.ServerIdx, &m.State, &started, &deadline, &ended, &m.Moved, &m.Left, &stays, &m.Error); err != nil {
		return ServerMove{}, err
	}
	var err error
	for _, f := range []struct {
		text string
		into *time.Time
	}{{started, &m.StartedAt}, {deadline, &m.DeadlineAt}, {ended, &m.EndedAt}} {
		if *f.into, err = parseTimeOrZero(f.text); err != nil {
			return ServerMove{}, err
		}
	}
	if err := json.Unmarshal([]byte(stays), &m.Stays); err != nil {
		return ServerMove{}, fmt.Errorf("move %s stays: %w", m.ID, err)
	}
	return m, nil
}

// InsertServerMove stores a new move. A zero StartedAt is the store's clock and an empty State
// is "moving". An ID in use is ErrAlreadyExists; a server that doesn't exist is ErrNotFound.
func (s *Store) InsertServerMove(ctx context.Context, m ServerMove) (ServerMove, error) {
	if m.StartedAt.IsZero() {
		m.StartedAt = s.now()
	}
	if m.State == "" {
		m.State = MoveMoving
	}
	stays, err := json.Marshal(nonNilStays(m.Stays))
	if err != nil {
		return ServerMove{}, fmt.Errorf("insert server move: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO server_moves (`+serverMoveColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ServerIdx, m.State, formatTime(m.StartedAt), formatTime(m.DeadlineAt), zeroOrTime(m.EndedAt),
		m.Moved, m.Left, string(stays), m.Error)
	if err != nil {
		if isUnique(err) {
			return ServerMove{}, fmt.Errorf("server move %q: %w", m.ID, ErrAlreadyExists)
		}
		if isForeignKey(err) {
			return ServerMove{}, fmt.Errorf("insert server move: server %d: %w", m.ServerIdx, ErrNotFound)
		}
		return ServerMove{}, fmt.Errorf("insert server move %q: %w", m.ID, err)
	}
	return m, nil
}

func nonNilStays(s []MoveStay) []MoveStay {
	if s == nil {
		return []MoveStay{}
	}
	return s
}

// UpdateServerMove records the progress of an open move: the copies moved so far and the
// movable copies still there, and what stays. A move that isn't open (or doesn't exist) is
// ErrNotFound.
func (s *Store) UpdateServerMove(ctx context.Context, id string, moved, left int, stays []MoveStay) error {
	text, err := json.Marshal(nonNilStays(stays))
	if err != nil {
		return fmt.Errorf("update server move %q: %w", id, err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE server_moves SET moved = ?, left = ?, stays = ? WHERE id = ? AND state = ?`,
		moved, left, string(text), id, MoveMoving)
	return oneMove(res, err, "update server move", id)
}

// EndServerMove settles an open move: its final state, when it ended, the counts and, for an
// incomplete one, the sentence. A move that isn't open is ErrNotFound, so a settled move never
// changes again.
func (s *Store) EndServerMove(ctx context.Context, id, state string, endedAt time.Time, moved, left int, errText string) error {
	if state == MoveMoving {
		return fmt.Errorf("end server move %q: %q is not a final state", id, state)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE server_moves SET state = ?, ended_at = ?, moved = ?, left = ?, error = ? WHERE id = ? AND state = ?`,
		state, formatTime(endedAt), moved, left, errText, id, MoveMoving)
	return oneMove(res, err, "end server move", id)
}

func oneMove(res sql.Result, err error, what, id string) error {
	if err != nil {
		return fmt.Errorf("%s %q: %w", what, id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("%s %q: %w", what, id, err)
	} else if n == 0 {
		return fmt.Errorf("%s %q: %w", what, id, ErrNotFound)
	}
	return nil
}

// NewestServerMove returns the server's most recent move, or ErrNotFound when it never had one.
func (s *Store) NewestServerMove(ctx context.Context, index int) (ServerMove, error) {
	m, err := scanServerMove(s.db.QueryRowContext(ctx, `SELECT `+serverMoveColumns+` FROM server_moves
		WHERE server_idx = ? ORDER BY started_at DESC, id DESC LIMIT 1`, index))
	if errors.Is(err, sql.ErrNoRows) {
		return ServerMove{}, fmt.Errorf("newest move of server %d: %w", index, ErrNotFound)
	}
	if err != nil {
		return ServerMove{}, fmt.Errorf("newest move of server %d: %w", index, err)
	}
	return m, nil
}

// OpenServerMoves returns every move that is still going, oldest first. At most one per server
// is open; Caravana resumes them at start.
func (s *Store) OpenServerMoves(ctx context.Context) ([]ServerMove, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+serverMoveColumns+` FROM server_moves
		WHERE state = ? ORDER BY started_at, id`, MoveMoving)
	if err != nil {
		return nil, fmt.Errorf("list open server moves: %w", err)
	}
	defer rows.Close()
	var out []ServerMove
	for rows.Next() {
		m, err := scanServerMove(rows)
		if err != nil {
			return nil, fmt.Errorf("list open server moves: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list open server moves: %w", err)
	}
	return out, nil
}
