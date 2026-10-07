package deploy

import (
	"context"
	"net/http"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// Restore loads the dump at object into svc of appID, as a one-off batch
// job running image (the service's current pinned image). The job gets its
// own copy of the backup job's variable first: a job can't read another
// job's variable, and its copy would wait for one forever instead of
// failing. An earlier restore job of the service is purged.
func (d *Deployer) Restore(ctx context.Context, appID string, svc app.Service, image, object string) error {
	items, err := d.readVariable(ctx, nomadjob.BackupVariablePath(nomadjob.BackupJobID(appID, svc.Name)))
	if err != nil {
		return err
	}
	id := nomadjob.RestoreJobID(appID, svc.Name)
	if err := d.purgeJob(ctx, id); err != nil {
		return err
	}
	if err := d.writeVariable(ctx, nomadjob.BackupVariablePath(id), items); err != nil {
		return err
	}
	job := nomadjob.RestoreJob(appID, svc, image, object, d.settings)
	if _, _, err := d.nomad.Jobs().Register(job, (&api.WriteOptions{}).WithContext(ctx)); err != nil {
		return d.registerError(id, err)
	}
	return nil
}

// RestoreStatus reads how service's restore job is doing: RunRunning until
// Nomad says it is dead, then RunSucceeded or RunFailed with the tool's last
// line. A job Nomad no longer has is RunFailed.
func (d *Deployer) RestoreStatus(ctx context.Context, appID, service string) (status, message string, err error) {
	id := nomadjob.RestoreJobID(appID, service)
	job, _, err := d.nomad.Jobs().Info(id, (&api.QueryOptions{}).WithContext(ctx))
	if statusOf(err) == http.StatusNotFound {
		return RunFailed, "Nomad has no record of this restore", nil
	}
	if err != nil {
		return "", "", d.callError("read job "+id, err)
	}
	if job.Status == nil || *job.Status != "dead" {
		return RunRunning, "", nil
	}
	o, err := d.batchOutcome(ctx, id, "restore")
	if err != nil {
		return "", "", err
	}
	return o.status, o.message, nil
}
