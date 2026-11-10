package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Servers (feature 031): the machines of the cluster. A row is made when the owner asks for a join
// command, used up by the join call and kept while the server is in the cluster. The store holds
// only SHA-256 hashes of the join and node tokens.

// The states of a server row.
const (
	ServerWaiting = "waiting" // the join command was made; nobody ran it yet
	ServerJoining = "joining" // the join call was answered; the server isn't ready yet
	ServerReady   = "ready"   // its checks passed
)

// ErrClusterFull is InsertServer's refusal when every index, 1 to 63, is taken.
var ErrClusterFull = errors.New("the cluster has the most servers it can have")

// Server is one stored server. A zero time is "not yet".
type Server struct {
	Index        int // 1 to 63: every address of the server derives from it
	ID           string
	Name         string // "" while a join that leaves the name to the machine waits
	Pool         string
	State        string
	First        bool
	NomadServer  bool // the role Caravana wants for it
	PublicKey    string
	Endpoint     string // "ip:51820"
	JoinHash     string // SHA-256 hex of the join token; "" once used
	JoinExpires  time.Time
	TokenHash    string // SHA-256 hex of the node token
	CreatedAt    time.Time
	JoinedAt     time.Time
	ReadyAt      time.Time
	SeenAt       time.Time
	Report       string // the agent's last report, JSON
	CertNotAfter time.Time
}

const serverColumns = `idx, id, name, pool, state, first, nomad_server, public_key, endpoint, join_hash, join_expires,
	token_hash, created_at, joined_at, ready_at, seen_at, report, cert_not_after`

func scanServer(r interface{ Scan(...any) error }) (Server, error) {
	var v Server
	var first, nomad int
	var joinExp, created, joined, ready, seen, cert string
	if err := r.Scan(&v.Index, &v.ID, &v.Name, &v.Pool, &v.State, &first, &nomad, &v.PublicKey, &v.Endpoint,
		&v.JoinHash, &joinExp, &v.TokenHash, &created, &joined, &ready, &seen, &v.Report, &cert); err != nil {
		return Server{}, err
	}
	v.First, v.NomadServer = first == 1, nomad == 1
	var err error
	for _, f := range []struct {
		text string
		into *time.Time
	}{{joinExp, &v.JoinExpires}, {created, &v.CreatedAt}, {joined, &v.JoinedAt}, {ready, &v.ReadyAt},
		{seen, &v.SeenAt}, {cert, &v.CertNotAfter}} {
		if *f.into, err = parseTimeOrZero(f.text); err != nil {
			return Server{}, err
		}
	}
	return v, nil
}

// ListServers returns every server, by index.
func (s *Store) ListServers(ctx context.Context) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+serverColumns+` FROM servers ORDER BY idx`)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		v, err := scanServer(rows)
		if err != nil {
			return nil, fmt.Errorf("list servers: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	return out, nil
}

func (s *Store) serverBy(ctx context.Context, what, column string, value any) (Server, error) {
	v, err := scanServer(s.db.QueryRowContext(ctx, `SELECT `+serverColumns+` FROM servers WHERE `+column+` = ?`, value))
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, fmt.Errorf("server by %s: %w", what, ErrNotFound)
	}
	if err != nil {
		return Server{}, fmt.Errorf("server by %s: %w", what, err)
	}
	return v, nil
}

// ServerByName returns the server with this name, or ErrNotFound. A server that has no name yet
// is not found by "".
func (s *Store) ServerByName(ctx context.Context, name string) (Server, error) {
	if name == "" {
		return Server{}, fmt.Errorf("server by name: %w", ErrNotFound)
	}
	return s.serverBy(ctx, "name", "name", name)
}

// ServerByID returns the server with this ID (a join's handle), or ErrNotFound.
func (s *Store) ServerByID(ctx context.Context, id string) (Server, error) {
	return s.serverBy(ctx, "ID", "id", id)
}

// ServerByIndex returns the server with this index, or ErrNotFound.
func (s *Store) ServerByIndex(ctx context.Context, index int) (Server, error) {
	return s.serverBy(ctx, "index", "idx", index)
}

// ServerByJoinHash returns the server a join token's hash belongs to, or ErrNotFound.
func (s *Store) ServerByJoinHash(ctx context.Context, hash string) (Server, error) {
	if hash == "" {
		return Server{}, fmt.Errorf("server by join token: %w", ErrNotFound)
	}
	return s.serverBy(ctx, "join token", "join_hash", hash)
}

// ServerByTokenHash returns the server a node token's hash belongs to, or ErrNotFound.
func (s *Store) ServerByTokenHash(ctx context.Context, hash string) (Server, error) {
	if hash == "" {
		return Server{}, fmt.Errorf("server by node token: %w", ErrNotFound)
	}
	return s.serverBy(ctx, "node token", "token_hash", hash)
}

// InsertServer stores a new server under the lowest free index, in one transaction, and returns it
// with the index. A zero CreatedAt is the store's clock. ErrClusterFull when every index is taken;
// a name or token hash that is in use is ErrAlreadyExists.
func (s *Store) InsertServer(ctx context.Context, v Server) (Server, error) {
	if v.CreatedAt.IsZero() {
		v.CreatedAt = s.now()
	}
	if v.Pool == "" {
		v.Pool = "default"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Server{}, fmt.Errorf("insert server: %w", err)
	}
	defer tx.Rollback()
	// The lowest free index: the first n that no row has.
	var idx int
	err = tx.QueryRowContext(ctx, `
		SELECT CASE WHEN NOT EXISTS (SELECT 1 FROM servers WHERE idx = 1) THEN 1
		       ELSE (SELECT MIN(idx) + 1 FROM servers a WHERE NOT EXISTS (SELECT 1 FROM servers b WHERE b.idx = a.idx + 1)) END`).
		Scan(&idx)
	if err != nil {
		return Server{}, fmt.Errorf("insert server: %w", err)
	}
	if idx > 63 {
		return Server{}, fmt.Errorf("insert server: %w", ErrClusterFull)
	}
	v.Index = idx
	_, err = tx.ExecContext(ctx, `INSERT INTO servers (`+serverColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.Index, v.ID, v.Name, v.Pool, v.State, boolInt(v.First), boolInt(v.NomadServer), v.PublicKey, v.Endpoint,
		v.JoinHash, zeroOrTime(v.JoinExpires), v.TokenHash, formatTime(v.CreatedAt), zeroOrTime(v.JoinedAt),
		zeroOrTime(v.ReadyAt), zeroOrTime(v.SeenAt), v.Report, zeroOrTime(v.CertNotAfter))
	if err != nil {
		if isUnique(err) {
			return Server{}, fmt.Errorf("insert server: %w", ErrAlreadyExists)
		}
		return Server{}, fmt.Errorf("insert server: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Server{}, fmt.Errorf("insert server: %w", err)
	}
	return v, nil
}

// ConsumeJoin spends a join token in one statement: the row whose join hash is hash, still
// waiting and not expired at now, gets the name, key, endpoint and node token hash, joined_at = now
// and state joining, and loses its join hash. Two calls with one token can't both win: the second
// finds no row and gets ErrNotFound. A name another server has is ErrAlreadyExists.
func (s *Store) ConsumeJoin(ctx context.Context, hash, name, publicKey, endpoint, tokenHash string, now time.Time) (Server, error) {
	if hash == "" {
		return Server{}, fmt.Errorf("consume join: %w", ErrNotFound)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE servers SET name = ?, public_key = ?, endpoint = ?, token_hash = ?, joined_at = ?, state = ?, join_hash = ''
		WHERE join_hash = ? AND state = ? AND join_expires > ?`,
		name, publicKey, endpoint, tokenHash, formatTime(now), ServerJoining,
		hash, ServerWaiting, formatTime(now))
	if err != nil {
		if isUnique(err) {
			return Server{}, fmt.Errorf("consume join: %w", ErrAlreadyExists)
		}
		return Server{}, fmt.Errorf("consume join: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return Server{}, fmt.Errorf("consume join: %w", err)
	} else if n == 0 {
		return Server{}, fmt.Errorf("consume join: %w", ErrNotFound)
	}
	return s.ServerByTokenHash(ctx, tokenHash)
}

// SaveServerReport records an agent's sync: its report (JSON), when it came and when its
// certificate ends (zero: unknown, kept as it was). An unknown server is ErrNotFound.
func (s *Store) SaveServerReport(ctx context.Context, index int, report string, seenAt, certNotAfter time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE servers SET report = ?, seen_at = ?, cert_not_after = CASE WHEN ? = '' THEN cert_not_after ELSE ? END
		WHERE idx = ?`, report, formatTime(seenAt), zeroOrTime(certNotAfter), zeroOrTime(certNotAfter), index)
	return oneRow(res, err, "save server report", index)
}

// SetServerReady marks a joining server ready at readyAt. A server already ready keeps its time.
func (s *Store) SetServerReady(ctx context.Context, index int, readyAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE servers SET state = ?, ready_at = CASE WHEN state = ? THEN ready_at ELSE ? END WHERE idx = ?`,
		ServerReady, ServerReady, formatTime(readyAt), index)
	return oneRow(res, err, "set server ready", index)
}

// SetNomadServer records whether Caravana wants the server to be a Nomad server.
func (s *Store) SetNomadServer(ctx context.Context, index int, on bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE servers SET nomad_server = ? WHERE idx = ?`, boolInt(on), index)
	return oneRow(res, err, "set nomad server", index)
}

// RotateFirstServer sets the first server's node token hash, public key and endpoint (the CLI's
// adopt-first on an installation that has one). ErrNotFound when there is no first server.
func (s *Store) RotateFirstServer(ctx context.Context, tokenHash, publicKey, endpoint string) (Server, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE servers SET token_hash = ?, public_key = ?, endpoint = ? WHERE first = 1`,
		tokenHash, publicKey, endpoint)
	if err != nil {
		return Server{}, fmt.Errorf("rotate first server: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Server{}, fmt.Errorf("rotate first server: %w", ErrNotFound)
	}
	return s.serverBy(ctx, "first", "first", 1)
}

// DeleteServer removes a server, so its node token stops working. An unknown one is ErrNotFound.
func (s *Store) DeleteServer(ctx context.Context, index int) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM servers WHERE idx = ?`, index)
	return oneRow(res, err, "delete server", index)
}

// DeleteExpiredJoins removes the rows still waiting whose join command ended before now, and says
// how many.
func (s *Store) DeleteExpiredJoins(ctx context.Context, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM servers WHERE state = ? AND join_expires != '' AND join_expires <= ?`,
		ServerWaiting, formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("delete expired joins: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func oneRow(res sql.Result, err error, what string, index int) error {
	if err != nil {
		return fmt.Errorf("%s %d: %w", what, index, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("%s %d: %w", what, index, err)
	} else if n == 0 {
		return fmt.Errorf("%s %d: %w", what, index, ErrNotFound)
	}
	return nil
}

// isUnique says err is SQLite refusing a duplicate value of a UNIQUE column or index.
func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// NewestHostOf returns the newest minute row of one Nomad node's host sample, or ErrNotFound when
// the node was never sampled. A server's numbers come from it.
func (s *Store) NewestHostOf(ctx context.Context, node string) (HostSample, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+hostColumns+`
		FROM metrics_host WHERE res = ? AND node = ? ORDER BY ts DESC LIMIT 1`, MinuteRes, node)
	h, err := scanHost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return HostSample{}, fmt.Errorf("newest host sample of %q: %w", node, ErrNotFound)
	}
	if err != nil {
		return HostSample{}, fmt.Errorf("newest host sample of %q: %w", node, err)
	}
	return h, nil
}
