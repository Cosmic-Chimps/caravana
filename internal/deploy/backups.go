package deploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// Backups (feature 010). The job shapes are in nomadjob; this file registers
// them, writes the variables they read, and reads what their runs did.

// BackupCredentials are what a dump logs in with.
type BackupCredentials struct {
	User, Password, Database string
}

// BackupDestination is where backups go: an S3-compatible bucket.
type BackupDestination struct {
	Endpoint, Region, Bucket, Prefix, AccessKeyID, SecretAccessKey string
}

// Statuses of a backup run or a restore, as Nomad's record of the job says.
const (
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
)

// BackupRun is one run of a backup job, as Nomad records it.
type BackupRun struct {
	JobID   string    // <job>/periodic-<ts>
	Started time.Time // from <ts>
	Status  string    // RunRunning, RunSucceeded or RunFailed
	Object  string    // bucket path of the dump, on success
	Bytes   int64
	Expired []string // dump files retention deleted, by name ("<ts>.dump")
	Message string   // why it failed
}

// destinationSecretPath is where the destination's secret key is kept: the
// control plane's own secrets live in Nomad Variables (constitution VII).
const destinationSecretPath = "caravana/backup-destination"

// BackupSecret returns the destination's secret key, or "" when none is
// kept.
func (d *Deployer) BackupSecret(ctx context.Context) (string, error) {
	v, _, err := d.nomad.Variables().Read(destinationSecretPath, (&api.QueryOptions{}).WithContext(ctx))
	if errors.Is(err, api.ErrVariablePathNotFound) {
		return "", nil
	}
	if err != nil {
		return "", d.volumeError("read the backup destination's secret", err)
	}
	return v.Items["secretAccessKey"], nil
}

// SaveBackupSecret keeps the destination's secret key.
func (d *Deployer) SaveBackupSecret(ctx context.Context, secret string) error {
	return d.writeVariable(ctx, destinationSecretPath, map[string]string{"secretAccessKey": secret})
}

// destinationItems are the variable items that point rclone at dest, for
// the dumps of one service.
func destinationItems(appID, service string, dest BackupDestination) map[string]string {
	path := dest.Bucket
	if p := strings.Trim(dest.Prefix, "/"); p != "" {
		path += "/" + p
	}
	return map[string]string{
		"RCLONE_CONFIG_DEST_TYPE":              "s3",
		"RCLONE_CONFIG_DEST_PROVIDER":          "Other",
		"RCLONE_CONFIG_DEST_ENDPOINT":          dest.Endpoint,
		"RCLONE_CONFIG_DEST_REGION":            dest.Region,
		"RCLONE_CONFIG_DEST_ACCESS_KEY_ID":     dest.AccessKeyID,
		"RCLONE_CONFIG_DEST_SECRET_ACCESS_KEY": dest.SecretAccessKey,
		"RCLONE_CONFIG_DEST_FORCE_PATH_STYLE":  "true",
		"BACKUP_PATH":                          path + "/" + appID + "/" + service,
	}
}

// SyncBackups makes a's backup jobs match its definition: for every service
// with a backup policy, its job's variable (credentials and destination)
// and its periodic job, dumping with the service's pinned image from
// digests; every other backup job of the app is purged with its runs and its
// variable. creds are by service name.
func (d *Deployer) SyncBackups(ctx context.Context, a app.App, digests map[string]string,
	creds map[string]BackupCredentials, dest BackupDestination) error {
	want := map[string]bool{}
	for _, svc := range a.Services {
		if svc.Backup == nil {
			continue
		}
		id := nomadjob.BackupJobID(a.ID, svc.Name)
		want[id] = true
		c := creds[svc.Name]
		items := destinationItems(a.ID, svc.Name, dest)
		items["PGUSER"], items["PGPASSWORD"], items["PGDATABASE"] = c.User, c.Password, c.Database
		if err := d.writeVariable(ctx, nomadjob.BackupVariablePath(id), items); err != nil {
			return err
		}
		job := nomadjob.BackupJob(a.ID, svc, digests[svc.Name], d.settings)
		if _, _, err := d.nomad.Jobs().Register(job, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
			return d.registerError(id, err)
		}
	}

	stubs, err := d.listJobs(ctx, nomadjob.AppJobPrefix(a.ID))
	if err != nil {
		return err
	}
	for _, s := range stubs {
		parent := s.ID
		if s.ParentID != "" {
			parent = s.ParentID
		}
		if strings.HasSuffix(parent, "_backup") && !want[parent] {
			if err := d.purgeJob(ctx, s.ID); err != nil {
				return err
			}
			if s.ParentID == "" {
				if err := d.deleteVariable(ctx, nomadjob.BackupVariablePath(s.ID)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// SetBackupDestination points every backup job at dest, from its next run:
// it rewrites the destination items of each backup job's variable, keeping
// its credentials. No job changes, so no app is deployed.
func (d *Deployer) SetBackupDestination(ctx context.Context, dest BackupDestination) error {
	metas, _, err := d.nomad.Variables().PrefixList("nomad/jobs/", (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return d.volumeError("list backup variables", err)
	}
	for _, m := range metas {
		jobID := strings.TrimPrefix(m.Path, "nomad/jobs/")
		appID, service, ok := splitBackupJobID(jobID)
		if !ok {
			continue
		}
		items, err := d.readVariable(ctx, m.Path)
		if err != nil {
			return err
		}
		maps.Copy(items, destinationItems(appID, service, dest))
		if err := d.writeVariable(ctx, m.Path, items); err != nil {
			return err
		}
	}
	return nil
}

// splitBackupJobID reads "<app>_<service>_backup" (or _restore) back.
func splitBackupJobID(jobID string) (appID, service string, ok bool) {
	parts := strings.Split(jobID, "_")
	if len(parts) != 3 || (parts[2] != "backup" && parts[2] != "restore") {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ForceBackup starts a run of service's backup job now and returns the
// run's job ID.
func (d *Deployer) ForceBackup(ctx context.Context, appID, service string) (string, error) {
	id := nomadjob.BackupJobID(appID, service)
	evalID, _, err := d.nomad.Jobs().PeriodicForce(id, (&api.WriteOptions{}).WithContext(ctx))
	if err != nil {
		return "", d.volumeError("start a backup of "+appID+"/"+service, err)
	}
	eval, _, err := d.nomad.Evaluations().Info(evalID, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return "", d.callError("read the backup's evaluation", err)
	}
	return eval.JobID, nil
}

// BackupRuns returns the runs of service's backup job that Nomad still
// remembers, with the outcome of the finished ones.
func (d *Deployer) BackupRuns(ctx context.Context, appID, service string) ([]BackupRun, error) {
	stubs, err := d.listJobs(ctx, nomadjob.BackupJobID(appID, service)+"/periodic-")
	if err != nil {
		return nil, err
	}
	var out []BackupRun
	for _, s := range stubs {
		ts, err := strconv.ParseInt(s.ID[strings.LastIndex(s.ID, "-")+1:], 10, 64)
		if err != nil {
			continue
		}
		run := BackupRun{JobID: s.ID, Started: time.Unix(ts, 0).UTC(), Status: RunRunning}
		if s.Status == "dead" {
			outcome, err := d.batchOutcome(ctx, s.ID, "upload")
			if err != nil {
				return nil, err
			}
			run.Status, run.Message = outcome.status, outcome.message
			for _, line := range outcome.stdout {
				switch {
				case strings.HasPrefix(line, nomadjob.BackupDoneMarker+" "):
					for _, field := range strings.Fields(line)[1:] {
						k, v, _ := strings.Cut(field, "=")
						switch k {
						case "key":
							run.Object = v
						case "bytes":
							run.Bytes, _ = strconv.ParseInt(v, 10, 64)
						}
					}
				case strings.HasPrefix(line, nomadjob.BackupExpiredMarker+" "):
					run.Expired = append(run.Expired, strings.TrimSpace(strings.TrimPrefix(line, nomadjob.BackupExpiredMarker)))
				}
			}
		}
		out = append(out, run)
	}
	return out, nil
}

// outcome is how a finished batch job ended.
type outcome struct {
	status  string
	message string
	stdout  []string // the main task's output, on success
}

// batchOutcome reads how a dead batch job ended: its copy complete means it
// succeeded (mainTask's stdout is kept); failed means the message is the
// last line the failed task wrote to stderr, else its last event.
func (d *Deployer) batchOutcome(ctx context.Context, jobID, mainTask string) (outcome, error) {
	allocs, _, err := d.nomad.Jobs().Allocations(jobID, true, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return outcome{}, d.callError("read the copies of "+jobID, err)
	}
	if len(allocs) == 0 {
		return outcome{status: RunFailed, message: "Nomad has no copy of this run: it never started"}, nil
	}
	a := allocs[0]
	if a.ClientStatus == "complete" {
		lines, err := d.taskOutput(ctx, a.ID, mainTask, LogStdout)
		return outcome{status: RunSucceeded, stdout: lines}, err
	}
	for name, st := range a.TaskStates {
		if st == nil || !st.Failed {
			continue
		}
		lines, err := d.taskOutput(ctx, a.ID, name, LogStderr)
		if err != nil {
			return outcome{}, err
		}
		for i := len(lines) - 1; i >= 0; i-- {
			if l := strings.TrimSpace(lines[i]); l != "" && !strings.Contains(l, "rclone.conf\" not found") {
				return outcome{status: RunFailed, message: name + ": " + l}, nil
			}
		}
		if n := len(st.Events); n > 0 {
			return outcome{status: RunFailed, message: name + ": " + st.Events[n-1].DisplayMessage}, nil
		}
		return outcome{status: RunFailed, message: name + " failed"}, nil
	}
	return outcome{status: RunFailed, message: "the run's copy ended " + a.ClientStatus + ": " + a.ClientDescription}, nil
}

// taskOutput is the last 16 KiB of one task's stream, as lines.
func (d *Deployer) taskOutput(ctx context.Context, copyID, task string, stream LogStream) ([]string, error) {
	lines := make(chan LogLine, 64)
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Logs(ctx, copyID, task, stream, 16<<10, false, lines)
		close(lines)
	}()
	var out []string
	for l := range lines {
		out = append(out, l.Text)
	}
	return out, <-errCh
}

// DeleteAppJobs purges every job of appID other than its own (backup jobs,
// their runs, restore jobs; purging a periodic job leaves its runs, so each
// goes by name), and deletes their variables.
func (d *Deployer) DeleteAppJobs(ctx context.Context, appID string) error {
	stubs, err := d.listJobs(ctx, nomadjob.AppJobPrefix(appID))
	if err != nil {
		return err
	}
	for _, s := range stubs {
		if err := d.purgeJob(ctx, s.ID); err != nil {
			return err
		}
	}
	metas, _, err := d.nomad.Variables().PrefixList("nomad/jobs/"+nomadjob.AppJobPrefix(appID), (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		return d.volumeError("list the variables of "+appID+"'s jobs", err)
	}
	for _, m := range metas {
		if err := d.deleteVariable(ctx, m.Path); err != nil {
			return err
		}
	}
	return nil
}

// listJobs lists the jobs whose IDs start with prefix.
func (d *Deployer) listJobs(ctx context.Context, prefix string) ([]*api.JobListStub, error) {
	stubs, _, err := d.nomad.Jobs().List((&api.QueryOptions{Prefix: prefix}).WithContext(ctx))
	if err != nil {
		return nil, d.callError("list jobs "+prefix+"*", err)
	}
	var out []*api.JobListStub
	for _, s := range stubs {
		if strings.HasPrefix(s.ID, prefix) {
			out = append(out, s)
		}
	}
	return out, nil
}

// purgeJob stops and erases one job; one Nomad no longer has is done.
func (d *Deployer) purgeJob(ctx context.Context, jobID string) error {
	_, _, err := d.nomad.Jobs().Deregister(jobID, true, (&api.WriteOptions{}).WithContext(ctx))
	if err != nil && statusOf(err) != http.StatusNotFound {
		return d.callError("purge job "+jobID, err)
	}
	return nil
}

func (d *Deployer) readVariable(ctx context.Context, path string) (map[string]string, error) {
	v, _, err := d.nomad.Variables().Read(path, (&api.QueryOptions{}).WithContext(ctx))
	if errors.Is(err, api.ErrVariablePathNotFound) {
		return nil, fmt.Errorf("%w: variable %s is gone", ErrNomadRejected, path)
	}
	if err != nil {
		return nil, d.volumeError("read "+path, err)
	}
	return v.Items, nil
}

func (d *Deployer) writeVariable(ctx context.Context, path string, items map[string]string) error {
	if _, _, err := d.nomad.Variables().Create(&api.Variable{Path: path, Items: items}, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
		return d.volumeError("write "+path, err)
	}
	return nil
}

func (d *Deployer) deleteVariable(ctx context.Context, path string) error {
	if _, err := d.nomad.Variables().Delete(path, (&api.WriteOptions{}).WithContext(ctx)); err != nil && statusOf(err) != http.StatusNotFound {
		return d.volumeError("delete "+path, err)
	}
	return nil
}
