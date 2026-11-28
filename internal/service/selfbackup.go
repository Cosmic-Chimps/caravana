package service

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/s3"
	"github.com/cosmic-chimps/caravana/internal/selfbackup"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Caravana backs itself up (feature 063): once a day, and when the owner asks, it writes its database and
// the Nomad Variables it owns into one encrypted file in the default backup destination. The file is
// encrypted to the public half of a recovery key the owner keeps; Caravana never holds the private half.
// See specs/063-self-backup/plan.md (D1 to D7).

// The timing of Caravana's own backup.
const (
	// defaultSelfBackupGap is how often the pass looks at whether today's backup is due.
	defaultSelfBackupGap = time.Minute
	// selfBackupLimit is the longest one backup may take.
	selfBackupLimit = 30 * time.Minute
	// selfBackupOldAfter is how long without a good backup makes the needs-you item (D7).
	selfBackupOldAfter = 48 * time.Hour
	// selfBackupHistory is how many runs are listed.
	selfBackupHistory = 30
)

// Why a backup can't be made now (SelfBackupUnavailableError.Reason).
const (
	SelfBackupNoDestination = "no_destination"
	SelfBackupNoKey         = "no_key"
)

// The refusals of the self-backup calls. A caller tests them with errors.Is.
var (
	// ErrSelfBackupRunning: a backup of Caravana is already running.
	ErrSelfBackupRunning = errors.New("a backup of Caravana is already running")
	// ErrRecoveryKeyExists: a recovery key was made already and replace wasn't asked for.
	ErrRecoveryKeyExists = errors.New("a recovery key already exists: making a new one means the old key can't open the backups made after it")
	// ErrSelfBackupNotFound: no run has that ID.
	ErrSelfBackupNotFound = errors.New("no such backup run")
)

// SelfBackupUnavailableError says why a backup can't be made now: there is no default backup destination to
// put it in, or no recovery key to encrypt it to.
type SelfBackupUnavailableError struct{ Reason string }

func (e *SelfBackupUnavailableError) Error() string {
	if e.Reason == SelfBackupNoDestination {
		return "Caravana can't back itself up without a backup destination: add one in Settings › Backup storage"
	}
	return "Caravana can't back itself up until you make a recovery key"
}

// selfBackupState is what the service keeps in memory about its own backup.
type selfBackupState struct {
	gap time.Duration
	// random is where the recovery key and each backup's ephemeral key come from: crypto/rand.
	random io.Reader
	// client makes the S3 client of a destination; a test may give its own.
	client func(deploy.BackupDestination) *s3.Client
	// startMu makes the choice of a run's time and its insert one step, so two starts can't take the same second.
	startMu sync.Mutex
	// keyMu makes the owner's key changes one at a time.
	keyMu sync.Mutex
}

func newSelfBackupState() selfBackupState {
	return selfBackupState{gap: defaultSelfBackupGap, random: crand.Reader, client: s3ClientOf}
}

// s3ClientOf is the S3 client of a destination.
func s3ClientOf(d deploy.BackupDestination) *s3.Client {
	return s3.New(s3.Config{Endpoint: d.Endpoint, Region: d.Region, Bucket: d.Bucket,
		Credentials: s3.Credentials{AccessKeyID: d.AccessKeyID, SecretAccessKey: d.SecretAccessKey}}, nil)
}

// SetSelfBackupPassGap sets how often the pass that starts the daily backup runs (a minute by default); one
// that isn't positive keeps the default. Call it before Start.
func (s *Service) SetSelfBackupPassGap(gap time.Duration) {
	if gap > 0 {
		s.selfBackup.gap = gap
	}
}

// SelfBackupSettings are the owner's choices about the daily backup.
type SelfBackupSettings struct {
	Enabled bool
	Hour    int // UTC, 0 to 23
	Keep    int // 1 to 90 backups
}

// SelfBackupSettingsInput is a change of the settings: a nil field stays as it is.
type SelfBackupSettingsInput struct {
	Enabled *bool
	Hour    *int
	Keep    *int
}

// SelfBackupStatus is what the backup page and the API show: the settings, the key's fingerprint, where
// backups go, whether one can be made, and the runs. It never has a key.
type SelfBackupStatus struct {
	Settings SelfBackupSettings
	// KeyFingerprint and KeyCreatedAt name the recovery key; both are empty or zero when none was made.
	KeyFingerprint string
	KeyCreatedAt   time.Time
	// Destination is the name of the default backup destination, "" when there is none.
	Destination string
	// Unavailable is why a backup can't be made now (SelfBackupNoDestination or SelfBackupNoKey), "" when it can.
	Unavailable string
	// Running is the run in progress, nil when none; Last is the newest run in any state and
	// LastSucceeded the newest that succeeded.
	Running, Last, LastSucceeded *store.SelfBackup
}

// RecoveryKeyResult is what making a recovery key answers, once: the key itself and its fingerprint.
type RecoveryKeyResult struct {
	Key         string
	Fingerprint string
}

// SelfBackups reads the state of Caravana's own backup. An admin only.
func (s *Service) SelfBackups(ctx context.Context) (SelfBackupStatus, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return SelfBackupStatus{}, err
	}
	return s.selfBackupStatus(ctx)
}

func (s *Service) selfBackupStatus(ctx context.Context) (SelfBackupStatus, error) {
	set, err := s.store.SelfBackupSettings(ctx)
	if err != nil {
		return SelfBackupStatus{}, err
	}
	out := SelfBackupStatus{Settings: SelfBackupSettings{Enabled: set.Enabled, Hour: set.Hour, Keep: set.Keep},
		KeyFingerprint: set.KeyFingerprint, KeyCreatedAt: set.KeyCreatedAt}
	switch dest, err := s.store.GetBackupDestination(ctx); {
	case err == nil:
		out.Destination = dest.Name
	case !errors.Is(err, store.ErrNotFound):
		return SelfBackupStatus{}, err
	}
	switch {
	case out.Destination == "":
		out.Unavailable = SelfBackupNoDestination
	case !set.HasKey():
		out.Unavailable = SelfBackupNoKey
	}
	for _, r := range []struct {
		get func(context.Context) (store.SelfBackup, bool, error)
		dst **store.SelfBackup
	}{{s.store.OpenSelfBackup, &out.Running}, {s.store.LatestSelfBackup, &out.Last}, {s.store.LatestSucceededSelfBackup, &out.LastSucceeded}} {
		b, ok, err := r.get(ctx)
		if err != nil {
			return SelfBackupStatus{}, err
		}
		if ok {
			*r.dst = &b
		}
	}
	return out, nil
}

// SetSelfBackupSettings changes the daily backup's settings: on or off, the UTC hour (0 to 23) and how many
// backups to keep (1 to 90). An owner only. A field outside its range is an *app.FieldError.
func (s *Service) SetSelfBackupSettings(ctx context.Context, in SelfBackupSettingsInput) (SelfBackupStatus, error) {
	if err := s.Allow(ctx, NeedOwner(), Target{}); err != nil {
		return SelfBackupStatus{}, err
	}
	before, err := s.store.SelfBackupSettings(ctx)
	if err != nil {
		return SelfBackupStatus{}, err
	}
	enabled, hour, keep := before.Enabled, before.Hour, before.Keep
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if in.Hour != nil {
		if *in.Hour < 0 || *in.Hour > 23 {
			return SelfBackupStatus{}, app.NewFieldError("hour", "must be from 0 to 23 (UTC)")
		}
		hour = *in.Hour
	}
	if in.Keep != nil {
		if *in.Keep < 1 || *in.Keep > 90 {
			return SelfBackupStatus{}, app.NewFieldError("keep", "must be from 1 to 90 backups")
		}
		keep = *in.Keep
	}
	if enabled == before.Enabled && hour == before.Hour && keep == before.Keep {
		s.noChange(ctx)
		return s.selfBackupStatus(ctx)
	}
	if err := s.store.SetSelfBackupSchedule(ctx, enabled, hour, keep); err != nil {
		return SelfBackupStatus{}, err
	}
	view := func(e bool, h, k int) map[string]any { return map[string]any{"enabled": e, "hour": h, "keep": k} }
	s.noteSetting(ctx, audit.ActionUpdated, settingsResource("self-backup"), nil,
		diffMaps(view(before.Enabled, before.Hour, before.Keep), view(enabled, hour, keep))...)
	s.publish(Change{What: ChangeSelfBackup})
	s.publish(Change{What: ChangeAttention})
	return s.selfBackupStatus(ctx)
}

// CreateRecoveryKey makes the recovery key and returns it, once: only its public half is stored, and the key
// can't be read again. An owner only. With a key already made it needs replace (else ErrRecoveryKeyExists):
// the new key then encrypts every backup from now on, and the old one is still needed for those made before.
// When a backup destination exists a backup is made at once with the new key.
func (s *Service) CreateRecoveryKey(ctx context.Context, replace bool) (RecoveryKeyResult, error) {
	if err := s.Allow(ctx, NeedOwner(), Target{}); err != nil {
		return RecoveryKeyResult{}, err
	}
	s.selfBackup.keyMu.Lock()
	defer s.selfBackup.keyMu.Unlock()
	before, err := s.store.SelfBackupSettings(ctx)
	if err != nil {
		return RecoveryKeyResult{}, err
	}
	if before.HasKey() && !replace {
		return RecoveryKeyResult{}, ErrRecoveryKeyExists
	}
	key, err := selfbackup.NewKey(s.selfBackup.random)
	if err != nil {
		return RecoveryKeyResult{}, err
	}
	now := s.clock.Now().UTC()
	if err := s.store.SetSelfBackupKey(ctx, key.Public(), key.Fingerprint(), now); err != nil {
		return RecoveryKeyResult{}, err
	}
	// The key itself is never noted: only that one was made, and its fingerprint.
	action, change := audit.ActionCreated, audit.Change{Path: "recoveryKey", Op: audit.OpAdded, To: "fingerprint " + key.Fingerprint()}
	if before.HasKey() {
		action = audit.ActionRotated
		change = audit.Change{Path: "recoveryKey", Op: audit.OpChanged, From: "fingerprint " + before.KeyFingerprint, To: "fingerprint " + key.Fingerprint()}
	}
	s.noteSetting(ctx, action, settingsResource("self-backup"), nil, change)
	s.publish(Change{What: ChangeSelfBackup})
	s.publish(Change{What: ChangeAttention})
	if _, err := s.startSelfBackup(ctx, store.SelfBackupByKey, callerEmail(ctx)); err != nil &&
		!errors.Is(err, ErrSelfBackupRunning) && !isUnavailable(err) {
		s.log.Warn("self backup: the backup after a new recovery key didn't start", "error", err)
	}
	return RecoveryKeyResult{Key: key.String(), Fingerprint: key.Fingerprint()}, nil
}

func isUnavailable(err error) bool {
	var u *SelfBackupUnavailableError
	return errors.As(err, &u)
}

// RequestSelfBackup starts a backup now and returns its run, running; the backup goes on in the background. An
// admin only. It is refused with a *SelfBackupUnavailableError when there is no destination or no key, and with
// ErrSelfBackupRunning while another backup runs.
func (s *Service) RequestSelfBackup(ctx context.Context) (store.SelfBackup, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return store.SelfBackup{}, err
	}
	run, err := s.startSelfBackup(ctx, store.SelfBackupByOwner, callerEmail(ctx))
	if err != nil {
		return store.SelfBackup{}, err
	}
	s.noteSetting(ctx, audit.ActionRequested, settingsResource("self-backup"),
		&audit.Record{Kind: audit.RecordSelfBackup, ID: run.ID},
		audit.Change{Path: "backup", Op: audit.OpAdded, To: "now"})
	return run, nil
}

// SelfBackupRuns lists the runs, newest first, at most 30. An admin only.
func (s *Service) SelfBackupRuns(ctx context.Context) ([]store.SelfBackup, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return nil, err
	}
	return s.store.ListSelfBackups(ctx, selfBackupHistory)
}

// SelfBackupRun reads one run. An ID that isn't one is ErrSelfBackupNotFound. An admin only.
func (s *Service) SelfBackupRun(ctx context.Context, id string) (store.SelfBackup, error) {
	if err := s.Allow(ctx, NeedAdmin(), Target{}); err != nil {
		return store.SelfBackup{}, err
	}
	b, err := s.store.GetSelfBackup(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.SelfBackup{}, &RefusalError{Field: "id", Err: ErrSelfBackupNotFound}
	}
	return b, err
}

func newSelfBackupID() (string, error) {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return "", fmt.Errorf("generate a backup run ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// startSelfBackup checks that a backup can be made, writes the run (running) and starts it in the background.
// It does no permission check: the callers do.
func (s *Service) startSelfBackup(ctx context.Context, trigger, actor string) (store.SelfBackup, error) {
	status, err := s.selfBackupStatus(ctx)
	if err != nil {
		return store.SelfBackup{}, err
	}
	if status.Unavailable != "" {
		return store.SelfBackup{}, &SelfBackupUnavailableError{Reason: status.Unavailable}
	}
	id, err := newSelfBackupID()
	if err != nil {
		return store.SelfBackup{}, err
	}
	s.selfBackup.startMu.Lock()
	// The object's name has the second it started in it: a run never takes the second of the one before.
	startedAt := s.clock.Now().UTC().Truncate(time.Second)
	if prev, ok, err := s.store.LatestSelfBackup(ctx); err != nil {
		s.selfBackup.startMu.Unlock()
		return store.SelfBackup{}, err
	} else if ok && !startedAt.After(prev.StartedAt) {
		startedAt = prev.StartedAt.UTC().Truncate(time.Second).Add(time.Second)
	}
	run, err := s.store.InsertSelfBackup(ctx, store.SelfBackup{ID: id, Trigger: trigger, Actor: actor, StartedAt: startedAt,
		Destination: status.Destination, KeyFingerprint: status.KeyFingerprint})
	s.selfBackup.startMu.Unlock()
	if errors.Is(err, store.ErrSelfBackupOpen) {
		return store.SelfBackup{}, ErrSelfBackupRunning
	}
	if err != nil {
		return store.SelfBackup{}, err
	}
	s.publish(Change{What: ChangeSelfBackup})
	s.backupWatcher.Go(func() { s.executeSelfBackup(s.bg, run) })
	return run, nil
}

// executeSelfBackup makes the backup and settles the run, succeeded or failed.
func (s *Service) executeSelfBackup(ctx context.Context, run store.SelfBackup) {
	res, err := s.makeSelfBackup(ctx, run)
	status := store.SelfBackupSucceeded
	if err != nil {
		status = store.SelfBackupFailed
		res.Error = selfBackupReason(err)
		// The log gets the cause; neither it nor the reason holds a key, a variable or a byte of the backup.
		s.log.Warn("self backup failed", "run", run.ID, "reason", res.Error, "error", err)
	}
	if res.Destination == "" {
		res.Destination = run.Destination
	}
	if res.KeyFingerprint == "" {
		res.KeyFingerprint = run.KeyFingerprint
	}
	// A run cut off by a shutdown is still settled.
	if err := s.store.FinishSelfBackup(context.WithoutCancel(ctx), run.ID, status, s.clock.Now().UTC(), res); err != nil {
		s.log.Error("self backup: the run could not be settled", "run", run.ID, "error", err)
	}
	s.publish(Change{What: ChangeSelfBackup})
	s.publish(Change{What: ChangeAttention})
}

// selfBackupStep is an error of one step of a backup, with the sentence the run will say. The cause stays in
// the log.
type selfBackupStep struct {
	say string
	err error
}

func (e *selfBackupStep) Error() string { return e.say + ": " + e.err.Error() }
func (e *selfBackupStep) Unwrap() error { return e.err }

// selfBackupReason is the run's sentence for err, in Caravana's words: never a library's text with a URL in it.
func selfBackupReason(err error) string {
	var (
		space *store.SpaceError
		step  *selfBackupStep
		s3err *s3.Error
	)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "The backup took longer than " + selfBackupLimit.String() + " and was stopped."
	case errors.Is(err, context.Canceled):
		return "Caravana was stopped during the backup."
	case errors.As(err, &space):
		return "The backup was not made: " + space.Error() + "."
	case errors.As(err, &s3err):
		return "The backup destination didn't take the backup (" + s3err.Error() + ")."
	case errors.As(err, &step):
		return step.say + " (see Caravana's log)."
	}
	return "The backup failed (see Caravana's log)."
}

// makeSelfBackup does D5's steps 2 to 6 for a run: the database copy, the variables, the encrypted file, the
// upload and the retention. It returns what the run records; a failure after the upload (retention) is a
// warning, never an error.
func (s *Service) makeSelfBackup(ctx context.Context, run store.SelfBackup) (store.SelfBackupResult, error) {
	ctx, cancel := context.WithTimeout(ctx, selfBackupLimit)
	defer cancel()
	res := store.SelfBackupResult{Destination: run.Destination, KeyFingerprint: run.KeyFingerprint,
		Version: strings.TrimPrefix(s.runningVersion, "v")}

	set, err := s.store.SelfBackupSettings(ctx)
	if err != nil {
		return res, err
	}
	if !set.HasKey() {
		return res, &SelfBackupUnavailableError{Reason: SelfBackupNoKey}
	}
	res.KeyFingerprint = set.KeyFingerprint
	dest, ok, err := s.backupDestination(ctx)
	if err != nil {
		return res, &selfBackupStep{say: "Caravana couldn't read the backup destination's settings", err: err}
	}
	if !ok {
		return res, &SelfBackupUnavailableError{Reason: SelfBackupNoDestination}
	}
	res.Destination = dest.Name
	if res.Schema, err = s.store.SchemaVersion(ctx); err != nil {
		return res, err
	}

	// Everything the backup needs on disk lives in a folder of its own in the data folder (where the
	// database is, so a restore can't run out of room in some other place), removed whatever happens.
	dir, err := os.MkdirTemp(s.store.DataDir(), "self-backup-")
	if err != nil {
		return res, &selfBackupStep{say: "Caravana couldn't make a temporary folder in its data folder", err: err}
	}
	defer os.RemoveAll(dir)

	dbPath := filepath.Join(dir, selfbackup.DatabaseFile)
	if err := s.store.CopyTo(ctx, dbPath); err != nil {
		var space *store.SpaceError
		if errors.As(err, &space) {
			return res, err
		}
		return res, &selfBackupStep{say: "Caravana couldn't copy its database", err: err}
	}
	vars, err := s.deployer.ExportVariables(ctx, selfbackup.Keep)
	if err != nil {
		return res, &selfBackupStep{say: "Caravana couldn't read its variables from Nomad", err: err}
	}
	apps, people, err := s.store.Counts(ctx)
	if err != nil {
		return res, err
	}
	varsJSON, err := json.Marshal(vars)
	if err != nil {
		return res, &selfBackupStep{say: "Caravana couldn't write its variables", err: err}
	}
	manifest, _ := json.Marshal(selfbackup.Manifest{Apps: apps, People: people, Variables: len(vars)})

	db, err := os.Open(dbPath)
	if err != nil {
		return res, &selfBackupStep{say: "Caravana couldn't read its database copy", err: err}
	}
	defer db.Close()
	dbInfo, err := db.Stat()
	if err != nil {
		return res, &selfBackupStep{say: "Caravana couldn't read its database copy", err: err}
	}
	// The encrypted file goes to the disk; the variables never do unencrypted.
	bundle, err := os.OpenFile(filepath.Join(dir, "backup"+selfbackup.Extension), os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		return res, &selfBackupStep{say: "Caravana couldn't make the backup file in its data folder", err: err}
	}
	defer bundle.Close()
	meta := selfbackup.Meta{CreatedAt: run.StartedAt, Version: res.Version, Schema: res.Schema, KeyFingerprint: set.KeyFingerprint}
	if err := selfbackup.Write(bundle, s.selfBackup.random, set.PublicKey, meta, []selfbackup.File{
		{Name: selfbackup.DatabaseFile, Size: dbInfo.Size(), Body: db},
		{Name: selfbackup.VariablesFile, Size: int64(len(varsJSON)), Body: bytes.NewReader(varsJSON)},
		{Name: selfbackup.ManifestFile, Size: int64(len(manifest)), Body: bytes.NewReader(manifest)},
	}); err != nil {
		return res, &selfBackupStep{say: "Caravana couldn't write the backup file", err: err}
	}
	size, err := bundle.Seek(0, io.SeekCurrent)
	if err != nil {
		return res, &selfBackupStep{say: "Caravana couldn't write the backup file", err: err}
	}

	client := s.selfBackup.client(dest)
	key := selfbackup.ObjectKey(dest.Prefix, run.StartedAt.Unix())
	if err := client.Put(ctx, key, bundle, size); err != nil {
		return res, err
	}
	res.Object, res.Bytes = key, size
	res.Warning = s.pruneSelfBackups(ctx, client, dest.Prefix, set.Keep)
	return res, nil
}

// pruneSelfBackups deletes the backups beyond keep, the newest kept, and returns a warning in Caravana's
// words for what it couldn't do ("" when all went well). It deletes only keys that read as a backup's.
func (s *Service) pruneSelfBackups(ctx context.Context, client *s3.Client, prefix string, keep int) string {
	objs, err := client.List(ctx, selfbackup.Base(prefix))
	if err != nil {
		s.log.Warn("self backup: the old backups could not be listed", "error", err)
		return "The new backup is safe, but the old ones could not be listed to delete the extra ones."
	}
	type found struct {
		ts  int64
		key string
	}
	var backups []found
	for _, o := range objs {
		if ts, ok := selfbackup.ParseObjectKey(prefix, o.Key); ok {
			backups = append(backups, found{ts, o.Key})
		}
	}
	slices.SortFunc(backups, func(a, b found) int { return int(b.ts - a.ts) }) // newest first
	failed := 0
	for _, b := range backups[min(keep, len(backups)):] {
		if err := client.Delete(ctx, b.key); err != nil {
			failed++
			s.log.Warn("self backup: an old backup could not be deleted", "error", err)
		}
	}
	if failed > 0 {
		return "The new backup is safe, but " + plural(failed, "old backup") + " could not be deleted."
	}
	return ""
}

// failInterruptedSelfBackups settles a run the last process left running: it has nobody to finish it.
func (s *Service) failInterruptedSelfBackups(ctx context.Context) error {
	n, err := s.store.FailRunningSelfBackups(ctx, s.clock.Now().UTC(), "Caravana restarted during the backup.")
	if err != nil {
		return err
	}
	if n > 0 {
		s.publish(Change{What: ChangeSelfBackup})
		s.publish(Change{What: ChangeAttention})
	}
	return nil
}

// runSelfBackupPass starts the daily backup: one gap after Start and then every gap it asks whether today's
// is due. Starting it is the clock's work, not a change anyone made, so it notes nothing (057 D14).
func (s *Service) runSelfBackupPass(ctx context.Context, gap time.Duration) {
	timer := time.NewTimer(gap)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		s.selfBackupTick(ctx)
		timer.Reset(gap)
	}
}

func (s *Service) selfBackupTick(ctx context.Context) {
	due, err := s.selfBackupDue(ctx, s.clock.Now().UTC())
	if err != nil {
		s.log.Warn("self backup: could not tell whether a backup is due", "error", err)
		return
	}
	if !due {
		return
	}
	if _, err := s.startSelfBackup(ctx, store.SelfBackupBySchedule, ""); err != nil && !errors.Is(err, ErrSelfBackupRunning) && !isUnavailable(err) {
		s.log.Warn("self backup: the daily backup did not start", "error", err)
	}
}

// selfBackupDue says today's backup should start now: it is on, there is a key and a default destination, the
// hour has come, and no scheduled run, succeeded or failed, has ended since today's slot began (D6). A failed
// run is not tried again the same day: the needs-you item says so.
func (s *Service) selfBackupDue(ctx context.Context, now time.Time) (bool, error) {
	status, err := s.selfBackupStatus(ctx)
	if err != nil {
		return false, err
	}
	if !status.Settings.Enabled || status.Unavailable != "" || status.Running != nil {
		return false, nil
	}
	slot := time.Date(now.Year(), now.Month(), now.Day(), status.Settings.Hour, 0, 0, 0, time.UTC)
	if now.Before(slot) {
		return false, nil
	}
	spent, err := s.store.ScheduledSelfBackupSince(ctx, slot)
	return !spent, err
}
