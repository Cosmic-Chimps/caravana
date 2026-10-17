// Package store keeps Caravana's state (apps and their deployments) in one
// SQLite file inside the data directory. Its schema migrations are embedded in
// the binary and applied when the store opens.
//
// Only one Caravana process may use a data directory at a time; a lock file
// enforces it.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	// Registers the pure-Go "sqlite" driver with database/sql.
	_ "modernc.org/sqlite"
)

// Errors callers can check with errors.Is. Returned errors wrap them with
// details (which ID, which version).
var (
	ErrNotFound          = errors.New("not found")
	ErrAlreadyExists     = errors.New("already exists")
	ErrInvalidTransition = errors.New("invalid status change")
	ErrSchemaTooNew      = errors.New("document schema is newer than this Caravana")
	ErrStoreTooNew       = errors.New("store was migrated by a newer Caravana")
	ErrLocked            = errors.New("data directory is in use by another Caravana process")
	ErrSchemaMismatch    = errors.New("store schema differs from this caravana's")
)

const (
	dbFile   = "caravana.db"
	lockFile = "caravana.lock"
)

// Store is the SQLite store. It is safe for concurrent use.
type Store struct {
	db      *sql.DB
	lock    *os.File
	now     func() time.Time // tests replace it with a fake clock
	applied int              // migrations applied by Open
}

// Open locks dataDir, opens (or creates) the store in it and applies any
// missing migrations.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory %q: %w", dataDir, err)
	}

	lock, err := os.OpenFile(filepath.Join(dataDir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file in %q: %w", dataDir, err)
	}
	// An advisory lock: the OS releases it when the process exits, even after
	// a crash, so a stale lock never blocks a restart.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %q", ErrLocked, dataDir)
		}
		return nil, fmt.Errorf("lock data directory %q: %w", dataDir, err)
	}

	s := &Store{lock: lock, now: time.Now}
	s.db, err = openDB(filepath.Join(dataDir, dbFile))
	if err == nil {
		err = s.db.PingContext(ctx)
	}
	if err == nil {
		err = s.migrate(ctx)
	}
	if err == nil {
		err = s.scrubSecretValues(ctx)
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// OpenShared opens a store that another process (the running server) holds
// the lock for, for the admin CLI: it takes no lock and applies no migrations.
// It fails with ErrSchemaMismatch unless the database is at exactly this
// binary's latest migration, and refuses a data directory with no database.
func OpenShared(ctx context.Context, dataDir string) (*Store, error) {
	path := filepath.Join(dataDir, dbFile)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("no caravana database in %q (has the server run here yet?): %w", dataDir, err)
	}
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, now: time.Now}
	if err := s.checkSchema(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// checkSchema returns ErrSchemaMismatch unless the applied migrations are
// exactly the embedded ones.
func (s *Store) checkSchema(ctx context.Context) error {
	fsys, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	known, err := readMigrations(fsys)
	if err != nil {
		return err
	}
	applied, err := appliedVersions(ctx, s.db)
	if err != nil {
		return err
	}
	newest := known[len(known)-1].version
	match := len(applied) == len(known)
	for _, m := range known {
		match = match && applied[m.version]
	}
	if !match {
		have := 0
		for v := range applied {
			have = max(have, v)
		}
		return fmt.Errorf("%w: the database is at migration %d, this binary at %d; stop caravana or run the same version",
			ErrSchemaMismatch, have, newest)
	}
	return nil
}

// openDB opens the SQLite file with the settings Caravana relies on.
func openDB(path string) (*sql.DB, error) {
	// _txlock=immediate: a transaction takes the write lock when it begins,
	// so busy_timeout covers it. A deferred one that reads first can fail at
	// once with "database is locked" when the CLI writes at the same moment.
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open store %q: %w", path, err)
	}
	// SQLite has one writer anyway. One connection serializes every query,
	// so there are no "database is locked" errors to handle.
	db.SetMaxOpenConns(1)
	return db, nil
}

func (s *Store) migrate(ctx context.Context) error {
	fsys, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	s.applied, err = migrate(ctx, s.db, fsys)
	return err
}

// MigrationsApplied is how many migrations Open applied: all of them on a
// new store, 0 when it was already up to date.
func (s *Store) MigrationsApplied() int {
	return s.applied
}

// Close closes the store and releases the data directory.
func (s *Store) Close() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.lock != nil {
		// Closing the file releases the lock.
		err = errors.Join(err, s.lock.Close())
		s.lock = nil
	}
	return err
}

// Ping reports whether the store is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}
