package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// Copies of backups and checks of destinations (feature 052). Both are one-off
// batch jobs that run rclone between S3-compatible destinations (nomadjob has
// their shapes). Each reads the keys of the destinations it uses from its own
// Nomad Variable, nomad/jobs/<job>, written before the job is registered and
// deleted with it: the keys are in no job, and in no variable that outlives the
// job.

// copyMessageMax is the most of a tool's own words a copy's or check's message
// repeats.
const copyMessageMax = 300

// BackupCopySpec is one copy of one backup to another destination. The service
// decides where the objects are: Source and Target are the two destinations, and
// SrcObject, DestObject and DestPath paths in them ("bucket/prefix/...", a
// directory's with a trailing "/").
type BackupCopySpec struct {
	AppID, Service string
	// ID names the copy: 12 lowercase hex characters, the job's own part.
	ID string
	// Source holds the backup, Target gets the copy.
	Source, Target BackupDestination
	// SrcObject is the backup's path in Source and DestObject where it goes in
	// Target. DestPath is Target's folder for the service's backups, where the
	// copy's retention looks.
	SrcObject, DestObject, DestPath string
	// Kind is nomadjob.CopyKindFile or CopyKindDir, and Ext the engine's
	// extension (".dump", ".sql.gz", ".archive.gz", ".sqlite.gz"; "" for a
	// directory).
	Kind, Ext string
	// Keep is how many copies Target keeps (0 deletes nothing), and Pinned the
	// timestamps of the pinned backups (unix seconds, as digits), which its
	// retention keeps and doesn't count.
	Keep   int
	Pinned []string
}

// JobID is the copy's Nomad job.
func (c BackupCopySpec) JobID() string { return nomadjob.BackupCopyJobID(c.AppID, c.Service, c.ID) }

// StartBackupCopy writes the copy job's variable (both destinations' keys, the
// objects, KEEP and the pins) and registers the job, and returns its ID. A job
// whose registration fails leaves no variable.
func (d *Deployer) StartBackupCopy(ctx context.Context, c BackupCopySpec) (jobID string, err error) {
	jobID = c.JobID()
	items := remoteKeys("SRC", c.Source)
	maps.Copy(items, remoteKeys("DEST", c.Target))
	items["SRC_OBJECT"], items["DEST_OBJECT"], items["DEST_PATH"] = c.SrcObject, c.DestObject, c.DestPath
	items["KEEP"] = strconv.Itoa(max(c.Keep, 0))
	if len(c.Pinned) > 0 {
		items["PINNED"] = pinList(c.Pinned)
	}
	path := nomadjob.BackupVariablePath(jobID)
	if err := d.writeVariable(ctx, path, items); err != nil {
		return "", redactError(err, &c.Source)
	}
	job := nomadjob.BackupCopyJob(c.AppID, c.Service, c.ID, c.Kind, c.Ext, d.settings)
	if _, _, err := d.nomad.Jobs().Register(job, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
		// The job isn't there, so nothing will read the variable: don't leave the keys.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = d.deleteVariable(cleanup, path)
		return "", d.registerError(jobID, err)
	}
	return jobID, nil
}

// BackupCopyResult is how a copy job is doing, as Nomad records it.
type BackupCopyResult struct {
	// Status is RunRunning, RunSucceeded or RunFailed.
	Status string
	// Object is the copy's path in the target destination and Bytes its size,
	// on success.
	Object string
	Bytes  int64
	// Expired lists what the target's retention deleted, by name.
	Expired []string
	// Message is why it failed; on success, why retention couldn't finish when
	// it couldn't (the copy is good).
	Message string
}

// BackupCopyOutcome reads how the copy job is doing: RunRunning until Nomad says
// it is dead, then RunSucceeded or RunFailed with the task's last line of
// standard error. A job Nomad no longer has is RunFailed. Secret keys of
// the destinations given in redact are blanked out of any message.
func (d *Deployer) BackupCopyOutcome(ctx context.Context, jobID string, redactKeys ...BackupDestination) (BackupCopyResult, error) {
	job, _, err := d.nomad.Jobs().Info(jobID, (&api.QueryOptions{}).WithContext(ctx))
	if statusOf(err) == http.StatusNotFound {
		return BackupCopyResult{Status: RunFailed, Message: "Nomad has no record of this copy"}, nil
	}
	if err != nil {
		return BackupCopyResult{}, d.callError("read job "+jobID, err)
	}
	if job.Status == nil || *job.Status != "dead" {
		return BackupCopyResult{Status: RunRunning}, nil
	}
	o, err := d.batchOutcome(ctx, jobID, nomadjob.BackupCopyTaskName)
	if err != nil {
		return BackupCopyResult{}, err
	}
	if o.status != RunSucceeded {
		return BackupCopyResult{Status: RunFailed, Message: cleanMessage(o.message, redactKeys)}, nil
	}
	out := nomadjob.ParseBackupCopyOutput(o.stdout)
	if !out.Done {
		return BackupCopyResult{Status: RunFailed, Message: "the copy ended without saying how it went"}, nil
	}
	return BackupCopyResult{Status: RunSucceeded, Object: out.Object, Bytes: out.Bytes, Expired: out.Expired,
		Message: cleanMessage(out.Warning, redactKeys)}, nil
}

// PurgeBackupCopy removes the copy job and its variable (the destinations'
// keys). One that is already gone is done.
func (d *Deployer) PurgeBackupCopy(ctx context.Context, jobID string) error {
	perr := d.purgeJob(ctx, jobID)
	derr := d.deleteVariable(ctx, nomadjob.BackupVariablePath(jobID))
	return errors.Join(perr, derr)
}

// cleanMessage is a tool's words, shortened and without the secret keys of dests.
func cleanMessage(text string, dests []BackupDestination) string {
	for i := range dests {
		text = redact(text, &dests[i])
	}
	return shorten(text, copyMessageMax)
}

// CheckPlace is one place a check job looks in: the destination, and a path in
// it. For a test the path is the destination's own folder (DestinationBase); for
// a locate it is the object (or, for a volume backup, its directory) to look for.
type CheckPlace struct {
	Dest BackupDestination
	Path string
}

// CheckResult is what a check job found.
type CheckResult struct {
	// OK is a test that passed. Failed is the step it failed at ("list",
	// "write" or "delete"; "" when the job failed some other way), and Message
	// the tool's own words, without any secret key.
	OK      bool
	Failed  string
	Message string
	// Found is the place (1-based, as given) that holds the object, 0 when
	// none does. Missing and Unreachable are the places that don't, and the
	// ones that couldn't be asked, in order. A place after the one found isn't
	// asked.
	Found       int
	Missing     []int
	Unreachable []int
}

// CheckBackupPlaces runs a check job and waits for it (Timing.CheckWait, 90 s by
// default): mode nomadjob.CheckModeTest checks the one place it is given (list,
// write and delete a small object), nomadjob.CheckModeLocate looks for the
// object in each place in order and stops at the first that has it. The
// destinations' keys are in the job's own variable only. The job and the variable
// are removed when this returns, whatever happened, within a fresh 30 s.
//
// A check that ran and said no is a CheckResult; an error is Nomad's: it
// couldn't run the check, or it didn't end in time (ErrNomadUnavailable).
func (d *Deployer) CheckBackupPlaces(ctx context.Context, mode string, places []CheckPlace) (res CheckResult, err error) {
	switch {
	case mode != nomadjob.CheckModeTest && mode != nomadjob.CheckModeLocate:
		return CheckResult{}, fmt.Errorf("check mode %q is neither test nor locate", mode)
	case len(places) == 0 || len(places) > nomadjob.MaxCheckPlaces:
		return CheckResult{}, fmt.Errorf("a check looks in 1 to %d places, got %d", nomadjob.MaxCheckPlaces, len(places))
	case mode == nomadjob.CheckModeTest && len(places) != 1:
		return CheckResult{}, errors.New("a test checks one destination")
	}
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return CheckResult{}, fmt.Errorf("make a check ID: %w", err)
	}
	id := hex.EncodeToString(raw[:])
	jobID := nomadjob.BackupCheckJobID(id)
	dests := make([]BackupDestination, len(places))
	items := map[string]string{}
	for i, p := range places {
		dests[i] = p.Dest
		maps.Copy(items, remoteKeys("R"+strconv.Itoa(i+1), p.Dest))
		items["R"+strconv.Itoa(i+1)+"_PATH"] = p.Path
	}

	defer func() {
		// The caller's context may be over (the wait timed out): the clean-up gets its own time.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		perr := errors.Join(d.purgeJob(cleanup, jobID), d.deleteVariable(cleanup, nomadjob.BackupVariablePath(jobID)))
		if perr != nil && err == nil {
			err = perr
		}
		if err != nil {
			err = redactErrors(err, dests)
		}
	}()

	if err := d.writeVariable(ctx, nomadjob.BackupVariablePath(jobID), items); err != nil {
		return CheckResult{}, err
	}
	job := nomadjob.BackupCheckJob(id, mode, d.settings)
	if _, _, err := d.nomad.Jobs().Register(job, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
		return CheckResult{}, d.registerError(jobID, err)
	}
	alloc, err := d.awaitCheck(ctx, jobID, d.Timing.withDefaults())
	if err != nil {
		return CheckResult{}, err
	}
	stdout, err := d.taskOutput(ctx, alloc.ID, nomadjob.BackupCheckTaskName, LogStdout)
	if err != nil {
		return CheckResult{}, err
	}
	out := nomadjob.ParseBackupCheckOutput(stdout)
	res = CheckResult{OK: out.OK, Failed: out.Failed, Found: out.Found, Missing: out.Missing, Unreachable: out.Unreachable}

	if !out.OK && out.Failed == "" && out.Found == 0 && len(out.Missing)+len(out.Unreachable) == 0 {
		// The job said nothing: it couldn't start, or was killed. Nomad's account.
		o, err := d.batchOutcome(ctx, jobID, nomadjob.BackupCheckTaskName)
		if err != nil {
			return CheckResult{}, err
		}
		res.Message = cleanMessage(o.message, dests)
	} else if out.Failed != "" {
		// rclone's own words, from the end of the task's standard error.
		lines, err := d.taskOutput(ctx, alloc.ID, nomadjob.BackupCheckTaskName, LogStderr)
		if err != nil {
			return CheckResult{}, err
		}
		for i := len(lines) - 1; i >= 0; i-- {
			if l := strings.TrimSpace(lines[i]); l != "" {
				res.Message = cleanMessage(l, dests)
				break
			}
		}
	}
	if mode == nomadjob.CheckModeLocate && res.Found == 0 {
		// A place the job didn't get to say anything about couldn't be asked.
		reported := map[int]bool{}
		for _, n := range append(append([]int(nil), res.Missing...), res.Unreachable...) {
			reported[n] = true
		}
		for n := 1; n <= len(places); n++ {
			if !reported[n] {
				res.Unreachable = append(res.Unreachable, n)
			}
		}
	}
	return res, nil
}

// awaitCheck polls the job's copy until it has ended and returns it. The job
// must end within Timing.CheckWait, copy or not.
func (d *Deployer) awaitCheck(ctx context.Context, jobID string, timing Timing) (*api.AllocationListStub, error) {
	ctx, cancel := context.WithTimeout(ctx, timing.CheckWait)
	defer cancel()
	for {
		allocs, _, err := d.nomad.Jobs().Allocations(jobID, true, (&api.QueryOptions{}).WithContext(ctx))
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return nil, d.callError("read the copies of "+jobID, err)
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
	return nil, fmt.Errorf("%w: the check didn't finish in %s", ErrNomadUnavailable, timing.CheckWait)
}

// redactErrors is err without the secret keys of dests: an error whose text holds
// one becomes a plain error with the same text, blanked.
func redactErrors(err error, dests []BackupDestination) error {
	for i := range dests {
		err = redactError(err, &dests[i])
	}
	return err
}

// DestinationBase is the bucket and the owner's prefix in it, "bucket/prefix":
// where every path Caravana writes in the destination starts.
func DestinationBase(dest BackupDestination) string { return destinationBase(dest) }

// BackupPath is the folder in the destination that holds a service's backups,
// "bucket/prefix/<app>/<service>".
func BackupPath(appID, service string, dest BackupDestination) string {
	return destinationBase(dest) + "/" + appID + "/" + service
}
