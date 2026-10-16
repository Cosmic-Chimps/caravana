package nomadjob

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// The caravana-volumes task (feature 016, research R1–R4, R9). Every service
// with a volume gets a prestart task beside its own. It runs before the
// service's task on every start of the copy, with the service's volumes
// mounted, so nothing else is using them. It measures their sizes and, when
// Caravana has left it a request, backs the volumes up or restores them. To
// trigger a request, Caravana restarts the copy's tasks: the service stops
// cleanly, this task does the work, and the service starts again. The job
// doesn't change, so there is no new version and no Nomad deployment.
//
// Like Translate, everything here is pure.

// VolumeTaskName is the name of the task. app.Validate keeps a service from
// taking it.
const VolumeTaskName = app.ReservedServiceName

// VolumeVariablePath is the Nomad Variable the task reads its request and the
// destination from. It is under the service's own path, so Nomad's implicit
// policy lets this task, and no other service's, read it (like SecretsPath).
// Caravana writes it before every register, so it always exists: a template
// reading a missing variable blocks the task, and the copy never starts.
func VolumeVariablePath(appID, service string) string {
	return SecretsPath(appID, service) + "/" + VolumeTaskName
}

// Where the task keeps its files, as the task sees them. /alloc/data is shared
// by the group and lasts as long as the copy, which is how Caravana reads the
// results after the task's own output is gone (a restarted prestart task
// doesn't write to its log).
const (
	// VolumeRoot holds each volume of the service, at VolumeRoot/<volume>.
	VolumeRoot = "/caravana-volumes"
	// VolumeDataDir holds the sizes file and the result files.
	VolumeDataDir = "/alloc/data/caravana"
	// volumeRequestPath is where the template writes the request. The script
	// reads it when it runs, not Nomad's task env: env is captured when the
	// task is first set up, and a later restart would still see the old
	// request (lab finding L7).
	volumeRequestPath = "/local/request.env"
)

// The same files as Caravana reads them through the allocation's file
// system, whose paths start at the allocation directory.
const (
	// VolumeRequestFile is the rendered request, to check that the task will
	// see it before the copy is restarted.
	VolumeRequestFile = VolumeTaskName + volumeRequestPath
	// VolumeSizesFile has one "<volume> <kilobytes>" line per volume.
	VolumeSizesFile = "alloc/data/caravana/sizes"
)

// VolumeResultFile is where the task writes the outcome of request id.
func VolumeResultFile(request string) string {
	return "alloc/data/caravana/" + request + ".result"
}

// Resources of the task. Nomad adds a prestart task's reservation to the
// copy's for the copy's whole life, not only while the task runs, so the
// reservation stays small: it is held while the app runs. The ceiling is the
// burst a run needs for tar, gzip and rclone, taken only while the node has
// the memory spare (it needs memory oversubscription, which feature 013
// turns on; without it Nomad ignores the ceiling and says so).
const (
	volumeCPUMHz      = 50
	volumeMemoryMB    = 32
	volumeMemoryMaxMB = 512
)

// volumeTask is the caravana-volumes task of svc, which has volumes. The
// backup image is the task's image: it has rclone, tar and BusyBox.
//
// Each volume is mounted at VolumeRoot/<name>. It is writable only when the
// service's group asks for it writable, which is when the service has a
// volume backup (see volumeRequests); a group's mount can't be writable
// where its request is read-only, and a task that only measures a volume
// shouldn't be able to change it.
func volumeTask(appID string, svc app.Service, s Settings) *api.Task {
	t := &api.Task{
		Name:      VolumeTaskName,
		Driver:    "docker",
		Lifecycle: &api.TaskLifecycle{Hook: "prestart"},
		Config: map[string]any{
			"image":      s.backupImage(),
			"entrypoint": []string{"/bin/sh", "-c"},
			"args":       []string{VolumeScript},
		},
		Resources: &api.Resources{
			CPU:         new(volumeCPUMHz),
			MemoryMB:    new(volumeMemoryMB),
			MemoryMaxMB: new(volumeMemoryMaxMB),
		},
		Templates: []*api.Template{
			volumeTemplate(appID, svc.Name, volumeRequestDest, false),
			volumeTemplate(appID, svc.Name, volumeDestinationDest, true),
		},
	}
	for _, v := range svc.Volumes {
		t.VolumeMounts = append(t.VolumeMounts, &api.VolumeMount{
			Volume:      new(v.Name),
			Destination: new(VolumeRoot + "/" + v.Name),
			ReadOnly:    new(v.ReadOnly && !hasVolumeBackup(svc)),
		})
	}
	return t
}

// hasVolumeBackup reports whether svc is backed up with the volume engine.
func hasVolumeBackup(svc app.Service) bool {
	return svc.Backup != nil && svc.Backup.Engine == app.BackupVolume
}

// Where the two templates render, as the task sees them (relative to the
// task directory, as Nomad wants them). The request goes under local/, which
// Nomad's file system API shows, and Caravana reads it to know the copy has
// the request. The destination goes under secrets/, which the API never
// shows: it holds the S3 secret key, and nobody with read-fs on the token
// should be able to read that through a copy's files.
const (
	volumeRequestDest     = "local/request.env"
	volumeDestinationDest = "secrets/destination.env"
	// volumeDestinationPath is where the script finds the second file.
	volumeDestinationPath = "/secrets/destination.env"
	// destinationKeyPattern picks the variable's keys that hold the
	// destination (what deploy's destinationItems writes). BACKUP_PATH is not
	// one: it is a bucket path, not a credential.
	destinationKeyPattern = "^RCLONE_CONFIG_DEST_"
)

// volumeTemplate renders the task's variable as KEY='value' lines, for the
// script to source. destination picks which keys: false renders every key but
// the destination's (the request file), true renders only those (the
// destination file, which holds the secret key). Both read the same variable,
// so Caravana still writes one. It is not env: see volumeRequestPath. "noop"
// means a changed variable never restarts anything; Caravana restarts the copy
// itself, once the request file shows the new request.
//
// A value is put in single quotes, and each single quote inside it is written
// as quote, backslash, quote, quote (which closes the string, adds a literal
// quote and opens it again). The shell reads the file, and inside single
// quotes nothing is expanded, so a key with a $ or a backtick arrives as it is
// and runs nothing. (JSON quoting,
// which secretsTemplate uses for Nomad's own env-file parser, would leave
// both for the shell to expand.) The template text has no "${", which Nomad
// would interpolate.
func volumeTemplate(appID, service, destPath string, destination bool) *api.Template {
	filter := `.K | regexMatch "` + destinationKeyPattern + `"`
	if !destination {
		filter = "not (" + filter + ")"
	}
	tmpl := `{{ with nomadVar "` + VolumeVariablePath(appID, service) + `" }}` +
		`{{ range .Tuples }}{{ if ` + filter + ` }}{{ .K }}='{{ .V | replaceAll "'" "'\\''" }}'` + "\n" +
		`{{ end }}{{ end }}{{ end }}`
	return &api.Template{
		EmbeddedTmpl: new(tmpl),
		DestPath:     new(destPath),
		Envvars:      new(false),
		ChangeMode:   new("noop"),
	}
}

// VolumeScript is the task's command, run as /bin/sh -c in the backup image
// (BusyBox ash). It is POSIX sh, and uses $VAR and $(...) only: Nomad
// interpolates ${...} in task config before the shell sees it.
//
// On every start it measures the volumes. When the rendered request names a
// run it should do, it does it and writes the outcome to a result file.
//
// It always exits 0. A prestart task that fails keeps the service from
// starting, and a backup is never worth that: failures go in the result file,
// which Caravana reads.
//
// It ignores a request that is empty, whose NOT_AFTER has passed, or whose
// result file exists. A later ordinary start (a deploy, a node reboot) must
// never repeat a run, least of all a restore, which would replace newer data
// with an old backup.
//
// ROOT, DATA, REQUEST_FILE and DEST_FILE can be set in the environment, for
// tests; the defaults are the task's real paths.
//
// The request (REQUEST, ACTION, OBJECT, NOT_AFTER, LIMIT, KEEP, TS and
// BACKUP_PATH) is sourced from REQUEST_FILE, and the destination's
// RCLONE_CONFIG_DEST_* from DEST_FILE, in a subshell, so a damaged file ends
// that subshell and nothing more. They are two files because only one of them
// may be readable through Nomad's file system API (see volumeDestinationDest).
//
// Every call to tar or rclone runs under BusyBox timeout, with the seconds the
// run has left of its LIMIT, so one stuck upload can't hold the service
// stopped for longer than the limit. A tar piped into rclone is wrapped on both
// sides, and its two exit codes are both checked (a pipeline's status is its
// last command's, and an archive cut short would otherwise upload as good).
const VolumeScript = `[ -n "$ROOT" ] || ROOT=` + VolumeRoot + `
[ -n "$DATA" ] || DATA=` + VolumeDataDir + `
[ -n "$REQUEST_FILE" ] || REQUEST_FILE=` + volumeRequestPath + `
[ -n "$DEST_FILE" ] || DEST_FILE=` + volumeDestinationPath + `

# One "<volume> <kilobytes>" line for each volume, replaced as a whole so
# Caravana never reads half a file.
measure_sizes() {
  mkdir -p "$DATA" || return 0
  : > "$DATA/.sizes.tmp" || return 0
  for dir in "$ROOT"/*; do
    [ -d "$dir" ] || continue
    echo "$(basename "$dir") $(kb_of "$dir")" >> "$DATA/.sizes.tmp"
  done
  mv "$DATA/.sizes.tmp" "$DATA/sizes"
}

# kb_of <dir> prints the kilobytes the directory holds.
kb_of() {
  kb=$(du -sk "$1" 2>/dev/null | cut -f1)
  [ -n "$kb" ] || kb=0
  echo "$kb"
}

# finish <status> writes the result file from MESSAGE and the detail lines the
# run left in $DETAIL, replacing nothing half-written, and removes the run's
# scratch files.
finish() {
  message=$(printf '%s' "$MESSAGE" | tr '\n' ' ')
  {
    echo "status=$1"
    echo "message=$message"
    [ ! -f "$DETAIL" ] || cat "$DETAIL"
  } > "$DATA/.$REQUEST.tmp" && mv "$DATA/.$REQUEST.tmp" "$DATA/$REQUEST.result"
  rm -rf "$DETAIL" "$WORK"
}

# seconds_left prints the seconds of the run's time limit that remain, at
# least 1: timeout 0 would mean no limit at all.
seconds_left() {
  now=$(date +%s)
  left=$((DEADLINE - now))
  [ "$left" -ge 1 ] || left=1
  echo "$left"
}

# step_failed <what> <stderr file> <exit status> sets MESSAGE from the step's
# last line of stderr, or says it ran out of time, and returns 1.
step_failed() {
  line=$(tail -n 1 "$2" 2>/dev/null | tr -d '\r')
  case "$3" in
    124|143) MESSAGE="$1 timed out (the limit is $LIMIT seconds)" ;;
    *) if [ -n "$line" ]; then MESSAGE="$1: $line"; else MESSAGE="$1 failed (exit status $3)"; fi ;;
  esac
  return 1
}

# --- The runs. Each returns 0 for success, or 1 with MESSAGE saying why. It
# --- appends "key=value" lines to $DETAIL for the result file.

# do_backup streams each volume as a gzipped tar into the destination, at
# BACKUP_PATH/TS/<volume>.tar.gz. Nothing is staged on disk: the copy's own
# disk is small. The first failure purges what the run uploaded, so no partial
# backup is left behind.
do_backup() {
  case "$TS" in ''|*[!0-9]*) MESSAGE="the request has no timestamp"; return 1 ;; esac
  [ -n "$BACKUP_PATH" ] || { MESSAGE="the request has no backup destination"; return 1; }
  found=
  for dir in "$ROOT"/*; do
    [ -d "$dir" ] || continue
    found=1
    if ! upload_volume "$dir" "$(basename "$dir")"; then
      discard_backup
      return 1
    fi
  done
  [ -n "$found" ] || { MESSAGE="the service has no volume mounted"; return 1; }
  echo "object=$BACKUP_PATH/$TS/" >> "$DETAIL"
  apply_retention
  return 0
}

# upload_volume <dir> <name> archives one volume and records its sizes.
upload_volume() {
  rm -f "$WORK/tar.status"
  {
    timeout "$(seconds_left)" tar -czf - -C "$1" --exclude ./.caravana-restore . 2> "$WORK/tar.err"
    echo $? > "$WORK/tar.status"
  } | timeout "$(seconds_left)" rclone rcat "dest:$BACKUP_PATH/$TS/$2.tar.gz" 2> "$WORK/rclone.err"
  uploaded=$?
  archived=$(cat "$WORK/tar.status" 2>/dev/null)
  # rclone's word comes first: when it dies, tar fails with a broken pipe.
  [ "$uploaded" -eq 0 ] || { step_failed "upload of $2" "$WORK/rclone.err" "$uploaded"; return 1; }
  [ -n "$archived" ] || archived=1
  [ "$archived" = 0 ] || { step_failed "archive of $2" "$WORK/tar.err" "$archived"; return 1; }

  out=$(timeout "$(seconds_left)" rclone size --json "dest:$BACKUP_PATH/$TS/$2.tar.gz" 2> "$WORK/rclone.err")
  status=$?
  [ "$status" -eq 0 ] || { step_failed "size of the archive of $2" "$WORK/rclone.err" "$status"; return 1; }
  bytes=$(printf '%s' "$out" | sed -n 's/.*"bytes": *\([0-9][0-9]*\).*/\1/p')
  [ -n "$bytes" ] || { MESSAGE="the size of the archive of $2 is unreadable: $out"; return 1; }
  {
    echo "volume.$2.bytes=$bytes"
    echo "volume.$2.size=$(kb_of "$1")"
  } >> "$DETAIL"
}

# discard_backup removes this run's directory from the destination, as well as
# it can, and forgets the volumes the run had recorded. It gets its own minute:
# the run's own time may be spent.
discard_backup() {
  : > "$DETAIL"
  timeout 60 rclone purge "dest:$BACKUP_PATH/$TS" > /dev/null 2>&1
  return 0
}

# apply_retention deletes the oldest backups beyond KEEP and lists their
# timestamps in expired=. The new backup is already safe, so a failure here
# doesn't fail the run: it is said in the message of a run that succeeded, and
# what was deleted before the failure is still listed. Only directories named
# with digits count, so nothing else under BACKUP_PATH is touched, and the new
# backup is never deleted. No KEEP, or a bad one, deletes nothing.
apply_retention() {
  case "$KEEP" in ''|*[!0-9]*|0) return 0 ;; esac
  dirs=$(timeout "$(seconds_left)" rclone lsf --dirs-only "dest:$BACKUP_PATH/" 2> "$WORK/rclone.err")
  status=$?
  if [ "$status" -ne 0 ]; then
    step_failed "listing the old backups" "$WORK/rclone.err" "$status"
    MESSAGE="the backup is kept, but old backups were not removed: $MESSAGE"
    return 0
  fi
  old=$(echo "$dirs" | grep -E '^[0-9]+/$' | sort -n)
  excess=$(($(echo "$old" | grep -c .) - KEEP))
  [ "$excess" -gt 0 ] || return 0
  expired=
  for d in $(echo "$old" | head -n "$excess"); do
    d=$(basename "$d")
    [ "$d" != "$TS" ] || continue
    timeout "$(seconds_left)" rclone purge "dest:$BACKUP_PATH/$d" > /dev/null 2> "$WORK/rclone.err"
    status=$?
    if [ "$status" -ne 0 ]; then
      step_failed "removing the backup $d" "$WORK/rclone.err" "$status"
      MESSAGE="the backup is kept, but old backups were not removed: $MESSAGE"
      break
    fi
    expired="$expired $d"
  done
  [ -z "$expired" ] || echo "expired=$(echo $expired)" >> "$DETAIL"
}

# do_restore replaces every volume's files with the archives of the backup at
# OBJECT. It must either restore every volume or leave every volume as it was,
# so it works in three steps:
#   1. stage: download each volume's archive into <volume>/.caravana-restore
#      (on the volume's own disk, where the room for the data already is) and
#      check that tar can read it. Nothing of the volume is touched yet, so a
#      missing or damaged archive, even the last one's, changes nothing.
#   2. swap: volume by volume, move the current files into
#      .caravana-restore/old (a rename, no copying) and extract the archive.
#   3. If any extract fails, every volume swapped so far, and that one, is
#      emptied of what was extracted and gets its old files back.
# At the end the staging directories are removed, old files with them. The
# backup path leaves .caravana-restore out of its archives, so nothing of this
# is ever backed up.
do_restore() {
  [ -n "$OBJECT" ] || { MESSAGE="the request names no backup to restore"; return 1; }
  case "$OBJECT" in */) ;; *) OBJECT="$OBJECT/" ;; esac
  # Old files left by an earlier restore that was cut off or could not put
  # them back may be the only copy of someone's data: staging again would
  # delete them, so refuse until a person has looked.
  for dir in "$ROOT"/*; do
    if [ -d "$dir/.caravana-restore/old" ]; then
      MESSAGE="$(basename "$dir") has files left in .caravana-restore/old by an earlier restore; check them, then delete that directory"
      return 1
    fi
  done
  ROLLBACK_FAILED=
  found=
  for dir in "$ROOT"/*; do
    [ -d "$dir" ] || continue
    found=1
    if ! stage_archive "$dir" "$(basename "$dir")"; then
      discard_stages
      return 1
    fi
  done
  [ -n "$found" ] || { MESSAGE="the service has no volume mounted"; return 1; }

  SWAPPED=
  for dir in "$ROOT"/*; do
    [ -d "$dir" ] || continue
    name=$(basename "$dir")
    if ! swap_volume "$dir" "$name"; then
      undo_swaps
      if [ -n "$ROLLBACK_FAILED" ]; then
        MESSAGE="$MESSAGE; the old files could not all be put back, they are in the .caravana-restore/old directory of each volume"
      else
        discard_stages
      fi
      return 1
    fi
    SWAPPED="$SWAPPED $name"
  done
  if ! discard_stages; then
    MESSAGE="the volumes are restored, but the old files could not be removed from their .caravana-restore directories"
  fi
  return 0
}

# stage_archive <dir> <name> downloads the volume's archive and checks it.
stage_archive() {
  rm -rf "$1/.caravana-restore"
  mkdir -p "$1/.caravana-restore" 2> "$WORK/mkdir.err"
  status=$?
  [ "$status" -eq 0 ] || { step_failed "preparing $2 for the restore" "$WORK/mkdir.err" "$status"; return 1; }
  timeout "$(seconds_left)" rclone copyto "dest:$OBJECT$2.tar.gz" "$1/.caravana-restore/$2.tar.gz" 2> "$WORK/rclone.err"
  status=$?
  [ "$status" -eq 0 ] || { step_failed "download of $2" "$WORK/rclone.err" "$status"; return 1; }
  timeout "$(seconds_left)" tar -tzf "$1/.caravana-restore/$2.tar.gz" > /dev/null 2> "$WORK/tar.err"
  status=$?
  [ "$status" -eq 0 ] || { step_failed "check of the archive of $2" "$WORK/tar.err" "$status"; return 1; }
}

# swap_volume <dir> <name> moves the volume's files aside and extracts its
# archive. When it fails it puts this volume back as it was, and says so with
# ROLLBACK_FAILED if it couldn't.
swap_volume() {
  mkdir "$1/.caravana-restore/old" 2> "$WORK/mkdir.err"
  status=$?
  [ "$status" -eq 0 ] || { step_failed "preparing $2 for the restore" "$WORK/mkdir.err" "$status"; return 1; }
  if ! move_entries "$1" "$1/.caravana-restore/old"; then
    MESSAGE="the files of $2 could not be moved aside"
    put_back "$1" || ROLLBACK_FAILED=1
    return 1
  fi
  timeout "$(seconds_left)" tar -xzf "$1/.caravana-restore/$2.tar.gz" -C "$1" 2> "$WORK/tar.err"
  status=$?
  if [ "$status" -ne 0 ]; then
    step_failed "extract of the archive of $2" "$WORK/tar.err" "$status"
    clear_entries "$1"
    put_back "$1" || ROLLBACK_FAILED=1
    return 1
  fi
}

# undo_swaps puts back the volumes that were swapped before a later one failed.
undo_swaps() {
  for swapped in $SWAPPED; do
    clear_entries "$ROOT/$swapped"
    put_back "$ROOT/$swapped" || ROLLBACK_FAILED=1
  done
}

# put_back <dir> moves a volume's old files back from .caravana-restore/old.
put_back() {
  move_entries "$1/.caravana-restore/old" "$1"
}

# discard_stages removes every volume's staging directory, the old files in it
# included. It returns 1 when one could not be removed.
discard_stages() {
  discarded=0
  for stage in "$ROOT"/*; do
    [ -d "$stage" ] || continue
    rm -rf "$stage/.caravana-restore" || discarded=1
  done
  return $discarded
}

# move_entries <from> <to> moves everything in <from>, hidden files too (a
# plain * skips them), except a .caravana-restore. It tries every entry and
# returns 1 if any failed.
move_entries() {
  moved=0
  for entry in "$1"/* "$1"/.[!.]* "$1"/..?*; do
    [ -e "$entry" ] || [ -L "$entry" ] || continue
    [ "$(basename "$entry")" != .caravana-restore ] || continue
    mv "$entry" "$2/" || moved=1
  done
  return $moved
}

# clear_entries <dir> deletes everything in <dir>, hidden files too, except a
# .caravana-restore: what a failed extract left behind.
clear_entries() {
  for entry in "$1"/* "$1"/.[!.]* "$1"/..?*; do
    [ -e "$entry" ] || [ -L "$entry" ] || continue
    [ "$(basename "$entry")" != .caravana-restore ] || continue
    rm -rf "$entry"
  done
}

# --- The request.

handle_request() {
  [ -f "$REQUEST_FILE" ] || return 0
  REQUEST= ACTION= OBJECT= NOT_AFTER= LIMIT= KEEP= TS= BACKUP_PATH=
  set -a
  . "$REQUEST_FILE"
  [ ! -f "$DEST_FILE" ] || . "$DEST_FILE"
  set +a
  # The ID names a file: letters, digits, "-" and "_" only.
  case "$REQUEST" in ''|*[!A-Za-z0-9_-]*) return 0 ;; esac
  case "$NOT_AFTER" in ''|*[!0-9]*) return 0 ;; esac
  [ "$NOT_AFTER" -ge "$(date +%s)" ] || return 0
  [ ! -e "$DATA/$REQUEST.result" ] || return 0

  # The run's time limit, counted from now. A LIMIT that is no number is
  # the default of half an hour.
  case "$LIMIT" in ''|*[!0-9]*|0) LIMIT=1800 ;; esac
  DEADLINE=$(($(date +%s) + LIMIT))

  WORK="$DATA/.$REQUEST.work"
  DETAIL="$DATA/.$REQUEST.detail"
  mkdir -p "$WORK" || return 0
  rm -f "$DETAIL"
  MESSAGE=
  case "$ACTION" in
    backup) if do_backup; then finish succeeded; else finish failed; fi ;;
    restore) if do_restore; then finish succeeded; else finish failed; fi ;;
    *) MESSAGE="unknown action"; finish failed ;;
  esac
}

measure_sizes
( handle_request )
exit 0
`

// VolumeResult is what a run wrote to its result file.
type VolumeResult struct {
	// Succeeded is the run's status.
	Succeeded bool
	// Message says why a failed run failed.
	Message string
	// Object is where the backup is in the destination, BACKUP_PATH/<ts>/.
	Object string
	// Volumes has one entry for each volume the run archived.
	Volumes []VolumeOutcome
	// Expired lists the backups retention deleted, by timestamp.
	Expired []string
}

// VolumeOutcome is one volume's part of a run.
type VolumeOutcome struct {
	Name string
	// Bytes is the size of the volume's archive in the destination.
	Bytes int64
	// SizeKB is the size of the volume's data, in kilobytes.
	SizeKB int64
}

// ParseVolumeResult reads a result file: "key=value" lines, as the script
// writes them. Keys it doesn't know are skipped, so a newer script's file
// still reads; a file with no status, or a number that isn't one, is an
// error, because a result Caravana can't trust shouldn't be recorded.
func ParseVolumeResult(text string) (VolumeResult, error) {
	var r VolumeResult
	var status string
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch {
		case key == "status":
			status = value
		case key == "message":
			r.Message = value
		case key == "object":
			r.Object = value
		case key == "expired":
			r.Expired = strings.Fields(value)
		case strings.HasPrefix(key, "volume."):
			rest := strings.TrimPrefix(key, "volume.")
			name, field, ok := cutLast(rest, ".")
			if !ok || (field != "bytes" && field != "size") {
				continue
			}
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				return VolumeResult{}, fmt.Errorf("volume result: %s is %q, not a size", key, value)
			}
			v := findOutcome(&r, name)
			if field == "bytes" {
				v.Bytes = n
			} else {
				v.SizeKB = n
			}
		}
	}
	switch status {
	case "succeeded":
		r.Succeeded = true
	case "failed":
	default:
		return VolumeResult{}, fmt.Errorf("volume result: status is %q, not succeeded or failed", status)
	}
	return r, nil
}

// findOutcome returns the entry for the named volume, adding it at the end
// when the file names it for the first time, so entries keep the file's order.
func findOutcome(r *VolumeResult, name string) *VolumeOutcome {
	for i := range r.Volumes {
		if r.Volumes[i].Name == name {
			return &r.Volumes[i]
		}
	}
	r.Volumes = append(r.Volumes, VolumeOutcome{Name: name})
	return &r.Volumes[len(r.Volumes)-1]
}

// cutLast splits s around the last sep, like strings.Cut around the first.
func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// ParseVolumeSizes reads the sizes file: one "<volume> <kilobytes>" line per
// volume. A line that isn't one is skipped: the sizes are only for the panel,
// and one bad line shouldn't hide the others.
func ParseVolumeSizes(text string) map[string]int64 {
	sizes := map[string]int64{}
	for line := range strings.Lines(text) {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kb < 0 {
			continue
		}
		sizes[fields[0]] = kb
	}
	return sizes
}
