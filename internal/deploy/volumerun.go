package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// Volume runs (feature 016, research R1–R3, R9). A service's volumes are
// backed up and restored by the caravana-volumes task inside its own copy:
// Caravana leaves it a request in the task's Nomad Variable, waits until the
// copy has rendered it, restarts the copy's tasks, and reads the result file
// the task writes. This file is the Nomad side of that; package service decides
// when, and records the outcome.

// Errors callers can check with errors.Is.
var (
	// ErrRequestNotRendered: the copy never showed the request, so restarting
	// it would run nothing (or an older request).
	ErrRequestNotRendered = errors.New("the copy didn't pick up the request")
	// ErrCopyNotFound: Nomad has no copy with that ID (it was replaced, or
	// its node is gone).
	ErrCopyNotFound = errors.New("copy not found")
)

// The actions a request can ask for.
const (
	VolumeBackup  = "backup"
	VolumeRestore = "restore"
)

// DefaultVolumeLimit is the run's time limit while no request says another.
const DefaultVolumeLimit = 30 * time.Minute

// VolumeRequest is one run Caravana asks a copy's caravana-volumes task for.
type VolumeRequest struct {
	// ID names the run, and the result file: "<service>-<unix seconds>".
	ID string
	// Action is VolumeBackup or VolumeRestore.
	Action string
	// TS names the directory a backup's archives go into.
	TS string
	// Object is the backup a restore loads: its directory in the destination.
	Object string
	// NotAfter is when the task stops honoring the request: a later start of
	// the copy (a deploy, a reboot) must never repeat the run.
	NotAfter time.Time
	// Limit is how long the run may take. Zero means DefaultVolumeLimit.
	Limit time.Duration
}

// The variable's keys that make up a request. The rest are the destination,
// KEEP and LIMIT, which outlast any one request.
var volumeRequestKeys = []string{"REQUEST", "ACTION", "TS", "OBJECT", "NOT_AFTER"}

// isDestinationKey reports whether k is one of the variable's keys that hold
// the destination: what destinationItems makes, and KEEP.
func isDestinationKey(k string) bool {
	return strings.HasPrefix(k, "RCLONE_CONFIG_DEST_") || k == "BACKUP_PATH" || k == "KEEP"
}

// volumeItems builds the task variable's items. dest nil leaves the
// destination out, keep 0 leaves KEEP out, and req nil makes an empty
// request (REQUEST is always there, so the variable is never empty, which
// Nomad refuses).
func volumeItems(a app.App, svc app.Service, dest *BackupDestination, keep int, req *VolumeRequest) map[string]string {
	items := map[string]string{"REQUEST": "", "LIMIT": strconv.Itoa(int(DefaultVolumeLimit / time.Second))}
	if dest != nil {
		maps.Copy(items, destinationItems(a.ID, svc.Name, *dest))
	}
	if keep > 0 {
		items["KEEP"] = strconv.Itoa(keep)
	}
	if req != nil {
		items["REQUEST"], items["ACTION"] = req.ID, req.Action
		items["NOT_AFTER"] = strconv.FormatInt(req.NotAfter.Unix(), 10)
		if req.TS != "" {
			items["TS"] = req.TS
		}
		if req.Object != "" {
			items["OBJECT"] = req.Object
		}
		if req.Limit > 0 {
			items["LIMIT"] = strconv.Itoa(int(req.Limit / time.Second))
		}
	}
	return items
}

// WriteVolumeVariable writes svc's caravana-volumes variable whole: the
// destination (when dest is not nil), KEEP (when keep is positive), the time
// limit and the request (an empty one when req is nil). It replaces what was
// there, so a caller that only changes the request passes the destination
// again.
func (d *Deployer) WriteVolumeVariable(ctx context.Context, a app.App, svc app.Service,
	dest *BackupDestination, keep int, req *VolumeRequest) error {
	return d.writeVariable(ctx, nomadjob.VolumeVariablePath(a.ID, svc.Name), volumeItems(a, svc, dest, keep, req))
}

// ClearVolumeRequest removes the request from the service's variable and keeps
// the rest (the destination, KEEP, the limit). A variable that isn't there is
// left that way.
func (d *Deployer) ClearVolumeRequest(ctx context.Context, appID, service string) error {
	path := nomadjob.VolumeVariablePath(appID, service)
	items, found, err := d.readVariableIfAny(ctx, path)
	if err != nil || !found {
		return err
	}
	return d.writeVariable(ctx, path, withoutRequest(items))
}

// withoutRequest is items with an empty request, and everything else kept.
func withoutRequest(items map[string]string) map[string]string {
	out := maps.Clone(items)
	for _, k := range volumeRequestKeys {
		delete(out, k)
	}
	out["REQUEST"] = ""
	return out
}

// ensureVolumeVariables makes sure every service of a with volumes has its
// variable, before the job is registered: a template reading a variable that
// isn't there blocks the task, and the copy never starts (lab finding L6). It
// shapes each to the app's definition without knowing the destination, which
// WriteVolumeVariable adds when the service has a backup:
//   - a request that has not expired is kept, so registering never loses a run;
//   - an expired one is cleared;
//   - KEEP follows the service's policy;
//   - the destination stays for a service with a volume backup, and goes for
//     one without, so the secret key isn't left where nothing uses it.
func (d *Deployer) ensureVolumeVariables(ctx context.Context, a app.App) error {
	for _, svc := range a.Services {
		if len(svc.Volumes) == 0 {
			continue
		}
		path := nomadjob.VolumeVariablePath(a.ID, svc.Name)
		have, found, err := d.readVariableIfAny(ctx, path)
		if err != nil {
			return err
		}
		want := maps.Clone(have)
		if want == nil {
			want = map[string]string{}
		}
		if until, err := strconv.ParseInt(want["NOT_AFTER"], 10, 64); want["REQUEST"] != "" && (err != nil || time.Now().Unix() > until) {
			want = withoutRequest(want)
		}
		if _, ok := want["REQUEST"]; !ok {
			want["REQUEST"] = ""
		}
		if _, ok := want["LIMIT"]; !ok {
			want["LIMIT"] = strconv.Itoa(int(DefaultVolumeLimit / time.Second))
		}
		if hasVolumeBackup(svc) {
			want["KEEP"] = strconv.Itoa(svc.Backup.KeepOrDefault())
		} else {
			for k := range want {
				if isDestinationKey(k) {
					delete(want, k)
				}
			}
		}
		if found && maps.Equal(have, want) {
			continue
		}
		if err := d.writeVariable(ctx, path, want); err != nil {
			return err
		}
	}
	return nil
}

// hasVolumeBackup reports whether svc is backed up with the volume engine.
func hasVolumeBackup(svc app.Service) bool {
	return svc.Backup != nil && svc.Backup.Engine == app.BackupVolume
}

// readVariableIfAny reads a variable's items; found is false when there is
// none.
func (d *Deployer) readVariableIfAny(ctx context.Context, path string) (items map[string]string, found bool, err error) {
	v, _, err := d.nomad.Variables().Read(path, (&api.QueryOptions{}).WithContext(ctx))
	if errors.Is(err, api.ErrVariablePathNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, d.volumeError("read "+path, err)
	}
	return v.Items, true, nil
}

// RunningCopy returns the ID of the service's copy that is running and not
// being replaced, the one a volume run goes through. ok is false when there
// is none: the service has no copy yet, it is still starting, or it has
// stopped. A service with a volume runs one copy.
func (d *Deployer) RunningCopy(ctx context.Context, appID, service string) (copyID string, ok bool, err error) {
	copies, err := d.Copies(ctx, appID) // newest version first
	if err != nil {
		return "", false, err
	}
	for _, c := range copies {
		if c.Service == service && (c.Status == CopyHealthy || c.Status == CopyUnhealthy) {
			return c.ID, true, nil
		}
	}
	return "", false, nil
}

// maxFileRead caps what Caravana reads from a copy's files: they are a few
// lines long.
const maxFileRead = 1 << 20

// readCopyFile reads a file of a copy's allocation directory. found is false
// when the file isn't there yet. A copy Nomad doesn't have is ErrCopyNotFound.
func (d *Deployer) readCopyFile(ctx context.Context, copyID, path string) (text string, found bool, err error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	alloc, _, err := d.nomad.Allocations().Info(copyID, q)
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return "", false, fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
		}
		return "", false, d.callError("read copy "+copyID, err)
	}
	r, err := d.nomad.AllocFS().Cat(alloc, path, q)
	if err != nil {
		if statusOf(err) == http.StatusNotFound || fileMissing(err) {
			return "", false, nil
		}
		return "", false, d.capabilityError("read "+path+" of copy "+copyID, "read-fs", err)
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, maxFileRead))
	if err != nil {
		return "", false, d.callError("read "+path+" of copy "+copyID, err)
	}
	return string(b), true, nil
}

// fileMissing recognizes the answer for a file that isn't there when it
// isn't a 404: Nomad's client words it as the operating system does.
func fileMissing(err error) bool {
	var ure api.UnexpectedResponseError
	return errors.As(err, &ure) && strings.Contains(ure.Body(), "no such file")
}

// capabilityError is callError for a call that needs a capability the token's
// policy may lack: the installer's policy file has it, so rerunning the
// installer fixes an older host.
func (d *Deployer) capabilityError(action, capability string, err error) error {
	if statusOf(err) == http.StatusForbidden {
		return fmt.Errorf("%w: Nomad refused the token (%s); its policy needs the %s capability: run the installer again to update it",
			ErrNomadUnavailable, action, capability)
	}
	return d.callError(action, err)
}

// WaitRequestRendered waits until the copy's task has rendered the request
// into its file, so that restarting the copy runs this request and no other.
// It reads the file every Timing.RenderGap, for at most Timing.RenderWait,
// and then fails with ErrRequestNotRendered.
func (d *Deployer) WaitRequestRendered(ctx context.Context, copyID, requestID string) error {
	timing := d.Timing.withDefaults()
	waitCtx, cancel := context.WithTimeout(ctx, timing.RenderWait)
	defer cancel()
	want := "REQUEST='" + requestID + "'"
	for {
		text, found, err := d.readCopyFile(waitCtx, copyID, nomadjob.VolumeRequestFile)
		if err != nil {
			// A read cut short is the caller giving up, or the wait running
			// out; say which, not "Nomad is unreachable".
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case waitCtx.Err() != nil:
				return fmt.Errorf("%w: %s doesn't show request %s after %s", ErrRequestNotRendered,
					nomadjob.VolumeRequestFile, requestID, timing.RenderWait)
			}
			return err
		}
		if found && slices.Contains(strings.Split(text, "\n"), want) {
			return nil
		}
		if !sleep(waitCtx, timing.RenderGap) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: %s doesn't show request %s after %s", ErrRequestNotRendered,
				nomadjob.VolumeRequestFile, requestID, timing.RenderWait)
		}
	}
}

// RestartCopy restarts all the copy's tasks, whatever their lifecycle: the
// service stops cleanly, its caravana-volumes task runs again, then the
// service starts. It is the same copy and job version, so Nomad starts no
// deployment.
func (d *Deployer) RestartCopy(ctx context.Context, copyID string) error {
	err := d.nomad.Allocations().RestartAllTasks(&api.Allocation{ID: copyID}, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
		}
		return d.capabilityError("restart copy "+copyID, "alloc-lifecycle", err)
	}
	return nil
}

// VolumeResult reads the outcome of request requestID from the copy. ok is
// false until the task has written it. A file that can't be read as a result
// is an error.
func (d *Deployer) VolumeResult(ctx context.Context, copyID, requestID string) (res nomadjob.VolumeResult, ok bool, err error) {
	text, found, err := d.readCopyFile(ctx, copyID, nomadjob.VolumeResultFile(requestID))
	if err != nil || !found {
		return nomadjob.VolumeResult{}, false, err
	}
	res, err = nomadjob.ParseVolumeResult(text)
	if err != nil {
		return nomadjob.VolumeResult{}, false, fmt.Errorf("read the result of %s: %w", requestID, err)
	}
	return res, true, nil
}

// VolumeSizes reads the volumes' sizes, in kilobytes by volume name, as the
// copy's task measured them when the copy last started. A copy whose task has
// not measured yet (one made before the task existed) gives none.
func (d *Deployer) VolumeSizes(ctx context.Context, copyID string) (map[string]int64, error) {
	text, found, err := d.readCopyFile(ctx, copyID, nomadjob.VolumeSizesFile)
	if err != nil || !found {
		return map[string]int64{}, err
	}
	return nomadjob.ParseVolumeSizes(text), nil
}

// StopDuration is how long the service's task was stopped by a restart made at
// or after since: from its first "Terminated" event after since to the next
// "Started" one, as the copy's task events tell. ok is false when it hasn't
// both happened yet (the task is still stopped, or didn't restart).
func (d *Deployer) StopDuration(ctx context.Context, copyID, mainTask string, since time.Time) (time.Duration, bool, error) {
	alloc, _, err := d.nomad.Allocations().Info(copyID, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		if statusOf(err) == http.StatusNotFound {
			return 0, false, fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
		}
		return 0, false, d.callError("read copy "+copyID, err)
	}
	ts := alloc.TaskStates[mainTask]
	if ts == nil {
		return 0, false, nil
	}
	stopped, ok := stopDuration(ts.Events, since)
	return stopped, ok, nil
}

// stopDuration reads a task's events, oldest first, as Nomad keeps them.
func stopDuration(events []*api.TaskEvent, since time.Time) (time.Duration, bool) {
	var terminated time.Time
	for _, e := range events {
		if e == nil {
			continue
		}
		at := time.Unix(0, e.Time)
		switch {
		case e.Type == api.TaskTerminated && terminated.IsZero() && !at.Before(since):
			terminated = at
		case e.Type == api.TaskStarted && !terminated.IsZero() && !at.Before(terminated):
			return at.Sub(terminated), true
		}
	}
	return 0, false
}
