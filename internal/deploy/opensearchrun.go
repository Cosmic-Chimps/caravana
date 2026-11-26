package deploy

import (
	"context"
	"fmt"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// OpenSearch runs (feature 061). A service with an opensearch backup has the
// idle caravana-upload sidecar in its copy, with the snapshot folder's volume.
// This file runs the scripts of package nomadjob with Nomad's alloc exec, one
// function per step, as sqliterun.go does for sqlite: the API calls in the
// service's own task, the folder's cleaning, archiving and extracting in the
// sidecar. The service is never stopped or restarted. Package service decides
// when, holds the app's lock and records the outcome.
//
// The destination's credentials and the cluster's login travel on the command's
// standard input and nowhere else. Neither is in any error text this file makes.

// OpenSearchLogin is the account the snapshot and restore calls log in with.
// Both empty means no login: a cluster with its security plugin off.
type OpenSearchLogin struct {
	User, Password string
}

// lines is the standard input of the snapshot and restore scripts: one
// "OPENSEARCH_USER=" and one "OPENSEARCH_PASSWORD=" line, or nothing for no
// login. A value with a line break would end its line early, so it is refused,
// without saying what the value was.
func (l OpenSearchLogin) lines() (string, error) {
	if l.User == "" && l.Password == "" {
		return "", nil
	}
	if strings.ContainsAny(l.User+l.Password, "\r\n") {
		return "", fmt.Errorf("the OpenSearch login has a line break; fix it in the service's environment or secrets")
	}
	return "OPENSEARCH_USER=" + l.User + "\nOPENSEARCH_PASSWORD=" + l.Password + "\n", nil
}

// secrets are the login's values that must stay out of error text.
func (l OpenSearchLogin) secrets() []string {
	var out []string
	if l.Password != "" {
		out = append(out, l.Password)
	}
	return out
}

// openSearchRepository finds where the snapshot folder of svc is and the
// service's port, or says why the service can't be backed up this way.
func openSearchRepository(svc app.Service) (volume app.Volume, inVolume string, port int, err error) {
	vol, in, ok := svc.OpenSearchRepository()
	if !ok || len(svc.Ports) == 0 {
		return app.Volume{}, "", 0, fmt.Errorf("service %s has no opensearch snapshot folder and port to back up", svc.Name)
	}
	return vol, in, svc.Ports[0].ContainerPort, nil
}

// OpenSearchClean empties the "caravana-*" folders earlier runs left in the
// snapshot repository, in the copy's caravana-upload task. fresh is the first
// clean of a run, which also empties the work directory of its result files;
// the last clean of a restore passes false so the results stay readable.
func (d *Deployer) OpenSearchClean(ctx context.Context, copyID string, svc app.Service, fresh bool) error {
	vol, in, _, err := openSearchRepository(svc)
	if err != nil {
		return err
	}
	mode := "keep"
	if fresh {
		mode = "fresh"
	}
	cmd := nomadjob.OpenSearchCommand(nomadjob.OpenSearchCleanScript, nomadjob.OpenSearchRepositoryDir(vol.Name, in), nomadjob.OpenSearchWorkDir, mode)
	_, err = d.runStep(ctx, copyID, app.UploadTaskName, "clean", cmd, nil, nil)
	return err
}

// OpenSearchSnapshot takes the snapshot of the run named request, timestamped ts,
// in the service's own task: every index whose name doesn't start with a dot,
// into <repository>/caravana-<request>. The output has the number of indices.
func (d *Deployer) OpenSearchSnapshot(ctx context.Context, copyID string, svc app.Service, request, ts string, login OpenSearchLogin) (nomadjob.SQLiteOutput, error) {
	_, _, port, err := openSearchRepository(svc)
	if err != nil {
		return nomadjob.SQLiteOutput{}, err
	}
	stdin, err := login.lines()
	if err != nil {
		return nomadjob.SQLiteOutput{}, err
	}
	cmd := nomadjob.OpenSearchCommand(nomadjob.OpenSearchSnapshotScript, svc.Backup.Repository, request, ts, fmt.Sprint(port), seconds(ctx),
		nomadjob.OpenSearchResultPath(request, "snapshot"))
	out, err := d.runStep(ctx, copyID, svc.Name, "snapshot", cmd, strings.NewReader(stdin), login.secrets())
	if err != nil {
		return out, err
	}
	return d.indicesResult(ctx, copyID, nomadjob.OpenSearchResultFile(request, "snapshot"), out), nil
}

// OpenSearchUpload tars the run's snapshot folder and uploads it to the
// destination as <backup path>/<ts>.opensearch.tar.gz, in the copy's
// caravana-upload task, then deletes the oldest backups beyond keep, as
// SQLiteUpload does (pinned keeps the pinned ones). The output has the object,
// its size, what retention deleted and its warning. The folder is removed
// whatever the outcome.
func (d *Deployer) OpenSearchUpload(ctx context.Context, copyID, request, appID string, svc app.Service,
	dest BackupDestination, ts string, keep int, pinned []string) (nomadjob.SQLiteOutput, error) {
	vol, in, _, err := openSearchRepository(svc)
	if err != nil {
		return nomadjob.SQLiteOutput{}, err
	}
	stdin, err := destinationLines(dest)
	if err != nil {
		return nomadjob.SQLiteOutput{}, err
	}
	folder := nomadjob.OpenSearchRepositoryDir(vol.Name, in) + "/" + nomadjob.OpenSearchFolderName(request)
	cmd := nomadjob.OpenSearchCommand(nomadjob.OpenSearchUploadScript,
		folder, destinationItems(appID, svc.Name, dest)["BACKUP_PATH"], ts, keepArg(keep), seconds(ctx), pinList(pinned),
		nomadjob.OpenSearchResultPath(request, "upload"))
	out, err := d.runStep(ctx, copyID, app.UploadTaskName, "upload", cmd, strings.NewReader(stdin), destSecrets(&dest))
	if err != nil {
		return out, err
	}
	out, err = d.sqliteResult(ctx, copyID, nomadjob.OpenSearchResultFile(request, "upload"), out, hasObject)
	if err != nil {
		return out, err
	}
	if out.Object == "" {
		return out, &SQLiteError{Message: "upload: the object of the backup is unreadable"}
	}
	return out, nil
}

// OpenSearchDownload downloads the backup at object (its key in the
// destination, as OpenSearchUpload reported it) in the copy's caravana-upload
// task and extracts it to <repository>/caravana-<request>, which must hold a
// snapshot. The output has the number of files.
func (d *Deployer) OpenSearchDownload(ctx context.Context, copyID, request, object string, svc app.Service, dest BackupDestination) (nomadjob.SQLiteOutput, error) {
	vol, in, _, err := openSearchRepository(svc)
	if err != nil {
		return nomadjob.SQLiteOutput{}, err
	}
	stdin, err := destinationLines(dest)
	if err != nil {
		return nomadjob.SQLiteOutput{}, err
	}
	folder := nomadjob.OpenSearchRepositoryDir(vol.Name, in) + "/" + nomadjob.OpenSearchFolderName(request)
	cmd := nomadjob.OpenSearchCommand(nomadjob.OpenSearchDownloadScript, object, folder, seconds(ctx),
		nomadjob.OpenSearchResultPath(request, "download"))
	out, err := d.runStep(ctx, copyID, app.UploadTaskName, "download", cmd, strings.NewReader(stdin), destSecrets(&dest))
	if err != nil {
		return out, err
	}
	out, err = d.sqliteResult(ctx, copyID, nomadjob.OpenSearchResultFile(request, "download"), out, hasFiles)
	if err != nil {
		return out, err
	}
	if out.Files <= 0 {
		return out, &SQLiteError{Message: "download: the number of files extracted is unreadable"}
	}
	return out, nil
}

// OpenSearchRestore restores the snapshot in <repository>/caravana-<request>
// into the running cluster, in the service's own task: the snapshot's indices
// are closed where they exist, restored, and opened. The output has the number
// of indices.
func (d *Deployer) OpenSearchRestore(ctx context.Context, copyID string, svc app.Service, request string, login OpenSearchLogin) (nomadjob.SQLiteOutput, error) {
	_, _, port, err := openSearchRepository(svc)
	if err != nil {
		return nomadjob.SQLiteOutput{}, err
	}
	stdin, err := login.lines()
	if err != nil {
		return nomadjob.SQLiteOutput{}, err
	}
	cmd := nomadjob.OpenSearchCommand(nomadjob.OpenSearchRestoreScript, svc.Backup.Repository, request, fmt.Sprint(port), seconds(ctx),
		nomadjob.OpenSearchResultPath(request, "restore"))
	out, err := d.runStep(ctx, copyID, svc.Name, "restore", cmd, strings.NewReader(stdin), login.secrets())
	if err != nil {
		return out, err
	}
	return d.indicesResult(ctx, copyID, nomadjob.OpenSearchResultFile(request, "restore"), out), nil
}

// indicesResult is the output of a snapshot or restore that exited 0. Its marker
// only says how many indices there were, so a result file that can't be read
// loses that count and never fails a run that did its work.
func (d *Deployer) indicesResult(ctx context.Context, copyID, file string, stdout nomadjob.SQLiteOutput) nomadjob.SQLiteOutput {
	if out, err := d.sqliteResult(ctx, copyID, file, stdout, hasIndices); err == nil {
		return out
	}
	return stdout
}
