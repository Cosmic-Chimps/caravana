package deploy

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// ErrArchiveFailed means the archive of an orphan volume did not end well. The error's text says why, in
// Caravana's words or the script's, at most 200 characters of a tool's own.
var ErrArchiveFailed = errors.New("the archive failed")

// archiveMessageMax is the most of a tool's own words an archive error repeats.
const archiveMessageMax = 200

// ArchiveVolume archives the host volume (its Nomad name, <app>_<volume>) to dest, with a one-off batch job
// that mounts it (feature 045, plan D4): the files go to <bucket>/<prefix>/<app>/volumes/<ts>/<volume>.tar.gz,
// or <bucket>/<prefix>/volumes/<ts>/… when the volume's name doesn't read as an app's (appID is ""). It
// returns the object's path and the bytes rclone measured.
//
// The destination's keys are written to the job's own variable first and are in nothing else; the job and
// the variable are removed when this returns, whatever happened, and a partial archive is deleted by the
// script. A job that never gets a copy (the server that holds the volume isn't placing work) ends it with
// ErrNomadUnavailable after Timing.ArchivePlaceWait.
func (d *Deployer) ArchiveVolume(ctx context.Context, volume, appID string, dest BackupDestination) (object string, bytes int64, err error) {
	timing := d.Timing.withDefaults()
	id := nomadjob.ArchiveJobID(volume)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	backupPath := destinationBase(dest) + "/volumes"
	if appID != "" {
		backupPath = destinationBase(dest) + "/" + appID + "/volumes"
	}
	wantObject := backupPath + "/" + ts + "/" + volume + ".tar.gz"

	// An archive job left by an earlier run is gone first; the variable is rewritten.
	if err := d.purgeJob(ctx, id); err != nil {
		return "", 0, err
	}
	if err := d.writeVariable(ctx, nomadjob.BackupVariablePath(id), destinationKeys(dest)); err != nil {
		return "", 0, err
	}
	defer func() {
		// The caller's context may be over (the wait timed out): the clean-up gets its own minute.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if perr := d.purgeJob(cleanup, id); perr != nil && err == nil {
			err = perr
		}
		if derr := d.deleteVariable(cleanup, nomadjob.BackupVariablePath(id)); derr != nil && err == nil {
			err = derr
		}
	}()

	job := nomadjob.VolumeArchiveJob(volume, appID, backupPath, ts, d.settings)
	if _, _, err := d.nomad.Jobs().Register(job, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
		return "", 0, d.registerError(id, err)
	}

	alloc, err := d.awaitArchive(ctx, id, timing)
	if err != nil {
		return "", 0, err
	}
	lines, err := d.taskOutput(ctx, alloc.ID, nomadjob.ArchiveTaskName, LogStdout)
	if err != nil {
		return "", 0, err
	}
	out := nomadjob.ParseArchiveOutput(lines)
	switch {
	case out.OK && out.Object == wantObject:
		return out.Object, out.Bytes, nil
	case out.OK:
		return "", 0, fmt.Errorf("%w: the archive task wrote to a different place than asked", ErrArchiveFailed)
	case out.Found:
		return "", 0, fmt.Errorf("%w: %s", ErrArchiveFailed, shorten(out.Message, archiveMessageMax))
	case alloc.ClientStatus == api.AllocClientStatusComplete:
		return "", 0, fmt.Errorf("%w: the archive task ended without saying how it went", ErrArchiveFailed)
	}
	// The task never ran to its end (it couldn't start, or Nomad stopped it): Nomad's own account.
	o, err := d.batchOutcome(ctx, id, nomadjob.ArchiveTaskName)
	if err != nil {
		return "", 0, err
	}
	return "", 0, fmt.Errorf("%w: %s", ErrArchiveFailed, shorten(o.message, archiveMessageMax))
}

// awaitArchive polls the job's copies until its one copy has ended, and returns it. No copy after
// Timing.ArchivePlaceWait is ErrNomadUnavailable; an end that doesn't come within Timing.ArchiveWait too.
func (d *Deployer) awaitArchive(ctx context.Context, jobID string, timing Timing) (*api.AllocationListStub, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timing.ArchiveWait)
	defer cancel()
	for {
		allocs, _, err := d.nomad.Jobs().Allocations(jobID, true, (&api.QueryOptions{}).WithContext(ctx))
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return nil, d.callError("read the copies of "+jobID, err)
		}
		if len(allocs) == 0 && time.Since(started) > timing.ArchivePlaceWait {
			return nil, fmt.Errorf("%w: the archive didn't start: the server that holds the volume isn't placing work", ErrNomadUnavailable)
		}
		if len(allocs) > 0 {
			switch a := allocs[0]; a.ClientStatus {
			case api.AllocClientStatusComplete, api.AllocClientStatusFailed, api.AllocClientStatusLost:
				return a, nil
			}
		}
		if !sleep(ctx, timing.PollGap) {
			break
		}
	}
	if err := ctx.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, err // the caller gave up
	}
	return nil, fmt.Errorf("%w: the archive didn't finish in %s", ErrArchiveFailed, timing.ArchiveWait)
}

// shorten cuts s to at most n bytes, on a character boundary, with an ellipsis when it cut.
func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
