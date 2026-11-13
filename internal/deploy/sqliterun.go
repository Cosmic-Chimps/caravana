package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// SQLite runs (feature 029, research R1–R5). A service with a sqlite backup
// has two idle sidecars in its copy: caravana-sqlite, which makes and checks
// copies of the database, and caravana-upload, which uploads and downloads
// them. This file runs the scripts of package nomadjob in them with Nomad's
// alloc exec, one function per step. Package service decides when, holds the
// app's lock and records the outcome.
//
// The destination's credentials travel on the command's standard input and
// nowhere else: not in the job, not in a variable or file, never in an
// argument (the container's process list shows those). They are also kept out
// of every error text this file makes, whatever a tool printed.

// sqliteOutputLimit is how much of each stream of a script Caravana reads.
// The scripts print a line or two.
const sqliteOutputLimit = 16 << 10

// SQLiteError is a step of a sqlite run that failed: the script ran and said
// why. Its text is "<step>: <reason>", as the script put it on the last line
// of its standard error (the step is copy, check, upload, download or stage),
// shortened and free of control characters and of the destination's secret
// key. Anything else that goes wrong (the copy isn't there, Nomad refuses the
// exec) is the error Exec gives: ErrCopyNotFound, ErrCopyNotRunning,
// ErrExecFailed or ErrNomadUnavailable.
type SQLiteError struct {
	Message string
}

func (e *SQLiteError) Error() string { return e.Message }

// SQLiteCopy makes a consistent copy of the service's database in the copy's
// caravana-sqlite task and checks it, leaving it at nomadjob.SQLiteCopyPath
// (request). The output has the copy's size.
func (d *Deployer) SQLiteCopy(ctx context.Context, copyID string, svc app.Service, request string) (nomadjob.SQLiteOutput, error) {
	vol, inVolume, ok := svc.SQLiteFile()
	if !ok {
		return nomadjob.SQLiteOutput{}, fmt.Errorf("service %s has no sqlite database file to back up", svc.Name)
	}
	cmd := nomadjob.SQLiteCommand(nomadjob.SQLiteCopyScript,
		nomadjob.SQLiteDatabasePath(vol.Name, inVolume), svc.Backup.File, nomadjob.SQLiteCopyPath(request), seconds(ctx))
	out, err := d.runSQLite(ctx, copyID, app.SQLiteTaskName, "copy", cmd, nil, nil)
	if err != nil {
		return out, err
	}
	if out.Bytes <= 0 {
		return out, &SQLiteError{Message: "copy: the size of the copy is unreadable"}
	}
	return out, nil
}

// SQLiteUpload gzips the copy of the run named request and uploads it to the
// destination as <backup path>/<ts>.sqlite.gz, in the copy's caravana-upload
// task, then deletes the oldest backups beyond keep. The output has the
// object, its size, the timestamps retention deleted and, when retention
// failed after a good upload, the warning. The copy is removed whatever the
// outcome. pinned lists the timestamps of the service's pinned backups, which
// retention keeps and doesn't count against keep.
func (d *Deployer) SQLiteUpload(ctx context.Context, copyID, request, appID string, svc app.Service,
	dest BackupDestination, ts string, keep int, pinned []string) (nomadjob.SQLiteOutput, error) {
	stdin, err := destinationLines(dest)
	if err != nil {
		return nomadjob.SQLiteOutput{}, err
	}
	cmd := nomadjob.SQLiteCommand(nomadjob.SQLiteUploadScript,
		nomadjob.SQLiteCopyPath(request), destinationItems(appID, svc.Name, dest)["BACKUP_PATH"], ts, keepArg(keep), seconds(ctx), pinList(pinned))
	out, err := d.runSQLite(ctx, copyID, app.UploadTaskName, "upload", cmd, &dest, strings.NewReader(stdin))
	if err != nil {
		return out, err
	}
	if out.Object == "" {
		return out, &SQLiteError{Message: "upload: the object of the backup is unreadable"}
	}
	return out, nil
}

// SQLiteDownload downloads the backup at object (its key in the destination,
// as SQLiteUpload reported it) in the copy's caravana-upload task and
// gunzips it to nomadjob.SQLiteCopyPath(request), which must start like a
// SQLite database. The output has the file's size.
func (d *Deployer) SQLiteDownload(ctx context.Context, copyID, request, object string, dest BackupDestination) (nomadjob.SQLiteOutput, error) {
	stdin, err := destinationLines(dest)
	if err != nil {
		return nomadjob.SQLiteOutput{}, err
	}
	cmd := nomadjob.SQLiteCommand(nomadjob.SQLiteDownloadScript, object, nomadjob.SQLiteCopyPath(request), seconds(ctx))
	out, err := d.runSQLite(ctx, copyID, app.UploadTaskName, "download", cmd, &dest, strings.NewReader(stdin))
	if err != nil {
		return out, err
	}
	if out.Bytes <= 0 {
		return out, &SQLiteError{Message: "download: the size of the file is unreadable"}
	}
	return out, nil
}

// SQLiteStage checks the downloaded file in the copy's caravana-sqlite task
// and stages it on the database's own volume, ready for the swap that a
// restore-sqlite request (VolumeRequest.File) makes while the service is
// stopped. The service is not touched. The download is removed.
func (d *Deployer) SQLiteStage(ctx context.Context, copyID string, svc app.Service, request string) error {
	vol, _, ok := svc.SQLiteFile()
	if !ok {
		return fmt.Errorf("service %s has no sqlite database file to restore", svc.Name)
	}
	cmd := nomadjob.SQLiteCommand(nomadjob.SQLiteStageScript,
		nomadjob.SQLiteCopyPath(request), nomadjob.SQLiteVolumeDir(vol.Name), request, seconds(ctx))
	_, err := d.runSQLite(ctx, copyID, app.SQLiteTaskName, "stage", cmd, nil, nil)
	return err
}

// runSQLite runs one script in task of the copy. A script that exits 0 gives
// its parsed markers. One that exits otherwise is a *SQLiteError with its
// reason; one that ran out of the context's time says so. dest, when not nil,
// is the destination to keep out of the error text.
func (d *Deployer) runSQLite(ctx context.Context, copyID, task, step string, cmd []string, dest *BackupDestination, stdin io.Reader) (nomadjob.SQLiteOutput, error) {
	res, err := d.Exec(ctx, copyID, task, cmd, stdin, sqliteOutputLimit)
	if err != nil {
		return nomadjob.SQLiteOutput{}, redactError(err, dest)
	}
	if res.TimedOut {
		return nomadjob.SQLiteOutput{}, &SQLiteError{Message: step + ": timed out"}
	}
	if res.ExitCode != nil && *res.ExitCode != 0 {
		return nomadjob.SQLiteOutput{}, &SQLiteError{Message: sqliteReason(step, res, *res.ExitCode, dest)}
	}
	return nomadjob.ParseSQLiteOutput(string(res.Stdout)), nil
}

// sqliteSteps are the steps the scripts name at the start of a failure's line.
var sqliteSteps = []string{"copy", "check", "upload", "download", "stage"}

// sqliteReason is the text of a failed step: the last line of the script's
// standard error, which already starts with the step ("copy: ..."). A script
// that said nothing, or whose line isn't usable, gets the exit status.
func sqliteReason(step string, res ExecResult, code int, dest *BackupDestination) string {
	lines := strings.Split(strings.TrimRight(string(res.Stderr), "\r\n"), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	// Control characters are dropped: the text goes to records and pages.
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, line)
	if dest != nil {
		line = redact(line, dest)
	}
	if line == "" {
		return fmt.Sprintf("%s: failed (exit status %d)", step, code)
	}
	// The scripts name their own step; a script has more than one (the copy
	// script also checks), so the step of the call is only added when the
	// line names none.
	if !slices.ContainsFunc(sqliteSteps, func(s string) bool { return strings.HasPrefix(line, s+":") }) {
		line = step + ": " + line
	}
	if runes := []rune(line); len(runes) > 300 {
		line = string(runes[:300]) + "…"
	}
	return line
}

// redact removes the destination's secret key from text.
func redact(text string, dest *BackupDestination) string {
	if dest == nil || dest.SecretAccessKey == "" {
		return text
	}
	return strings.ReplaceAll(text, dest.SecretAccessKey, "***")
}

// redactError returns err as it is, unless its text holds the secret key:
// then it is an error with the same text without it (and without the chain,
// which could carry the key too).
func redactError(err error, dest *BackupDestination) error {
	if dest == nil || dest.SecretAccessKey == "" || !strings.Contains(err.Error(), dest.SecretAccessKey) {
		return err
	}
	return errors.New(redact(err.Error(), dest))
}

// destinationLines is the standard input of the upload and download scripts:
// one "RCLONE_CONFIG_DEST_<NAME>=<value>" line per setting of dest, in a fixed
// order. Only those lines: the scripts export them and read nothing else. A
// value with a line break would end its line early and start a line of its
// own, so it is refused, without saying what the value was.
func destinationLines(dest BackupDestination) (string, error) {
	items := destinationItems("", "", dest) // BACKUP_PATH is the only item that uses the names
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(items)) {
		if !strings.HasPrefix(k, "RCLONE_CONFIG_DEST_") {
			continue
		}
		if strings.ContainsAny(items[k], "\r\n") {
			return "", fmt.Errorf("the backup destination's %s has a line break; fix it in Settings", strings.ToLower(strings.TrimPrefix(k, "RCLONE_CONFIG_DEST_")))
		}
		b.WriteString(k + "=" + items[k] + "\n")
	}
	return b.String(), nil
}

// seconds is what is left of the context's time, in whole seconds and at
// least 1, for the script to count its own calls against: the whole run's
// limit, not one step's. A context with no deadline gets the default limit.
func seconds(ctx context.Context) string {
	left := DefaultVolumeLimit
	if dl, ok := ctx.Deadline(); ok {
		left = time.Until(dl)
	}
	return fmt.Sprint(max(int(left.Seconds()), 1))
}

// keepArg is the script's KEEP: a positive number, or empty for none.
func keepArg(keep int) string {
	if keep <= 0 {
		return ""
	}
	return fmt.Sprint(keep)
}
