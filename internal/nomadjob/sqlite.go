package nomadjob

import (
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// The sqlite backup tasks (feature 029, research R1–R5). A service with a
// `sqlite` backup gets two poststart sidecars in its group, both idle until
// Caravana runs a command in them with Nomad's alloc exec:
//
//   - caravana-sqlite, the sqlite3 image, mounts the database's volume and
//     makes and checks the copies;
//   - caravana-upload, the backup image (rclone), has no volume and uploads
//     and downloads them.
//
// They share /alloc/data, where a copy waits between the two. The
// destination's credentials are never in the job: Caravana sends them on the
// standard input of each command that needs them (research R3).
//
// Like Translate, everything here is pure.

// Where the commands keep their files, as the sidecars see them.
const (
	// SQLiteWorkDir holds the copy that waits between the two sidecars. Each
	// run's first command empties it.
	SQLiteWorkDir = VolumeDataDir + "/sqlite"
)

// SQLiteCopyPath is where the copy of the run named request waits.
func SQLiteCopyPath(request string) string { return SQLiteWorkDir + "/" + request + ".db" }

// SQLiteVolumeDir is where caravana-sqlite (and caravana-volumes) see the
// volume of that name.
func SQLiteVolumeDir(volume string) string { return VolumeRoot + "/" + volume }

// SQLiteDatabasePath is the database as caravana-sqlite sees it: inVolume is
// the path inside the volume (app.Service.SQLiteFile).
func SQLiteDatabasePath(volume, inVolume string) string {
	return SQLiteVolumeDir(volume) + "/" + inVolume
}

// SQLiteStagePath is where a restore stages the checked file on the
// database's own volume, so the swap is a rename.
func SQLiteStagePath(volume, request string) string {
	return SQLiteVolumeDir(volume) + "/" + app.RestoreDir + "/" + request + ".db"
}

// SQLiteRestoreFile is the FILE of a restore-sqlite request for VolumeScript:
// the file relative to the root of the volumes, "<volume>/<path in volume>".
func SQLiteRestoreFile(volume, inVolume string) string { return volume + "/" + inVolume }

// SQLiteObject is the backup's object in the destination: backupPath is the
// service's BACKUP_PATH and ts the run's unix seconds.
func SQLiteObject(backupPath, ts string) string { return backupPath + "/" + ts + ".sqlite.gz" }

// SQLiteCommand is the argv that runs script with args: /bin/sh -c <script>
// sh <args...>. The arguments are positional ($1, $2, ...), never part of a
// shell line, so no value is ever read as shell syntax.
func SQLiteCommand(script string, args ...string) []string {
	return append([]string{"/bin/sh", "-c", script, "sh"}, args...)
}

// hasSQLiteBackup reports whether svc is backed up with the sqlite engine.
func hasSQLiteBackup(svc app.Service) bool {
	return svc.Backup != nil && svc.Backup.Engine == app.BackupSQLite
}

// holdsSQLiteFile reports whether v is the volume that holds svc's sqlite
// database.
func holdsSQLiteFile(svc app.Service, v app.Volume) bool {
	held, _, ok := svc.SQLiteFile()
	return ok && held.Name == v.Name
}

// Resources and stop time of the two sidecars. Like caravana-volumes, they
// are held for the copy's whole life, so the reservations stay small; the
// ceilings are the burst a run needs (rclone's multipart buffers are the
// larger) and need memory oversubscription (feature 013).
const (
	sqliteCPUMHz       = 20
	sqliteMemoryMB     = 16
	sqliteMemoryMaxMB  = 256
	uploadCPUMHz       = 20
	uploadMemoryMB     = 32
	uploadMemoryMaxMB  = 512
	sidecarKillTimeout = time.Second
)

// IdleScript is the sidecars' command: it does nothing until Nomad stops it
// (exit 0 on the stop signal, so the group ends cleanly). It uses $VAR and
// $(...) only: Nomad interpolates ${...} in task config.
const IdleScript = `trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done`

// sqliteTask is the caravana-sqlite sidecar of svc, which has a sqlite backup.
// It mounts the volume that holds the database, writable: it stages restores
// there, on the database's own disk. The guard against a read-only mount is
// explicit, though app.Validate already refuses a read-only database volume.
func sqliteTask(svc app.Service, s Settings) *api.Task {
	t := &api.Task{
		Name:      app.SQLiteTaskName,
		Driver:    "docker",
		Lifecycle: &api.TaskLifecycle{Hook: "poststart", Sidecar: true},
		Config: map[string]any{
			"image":      s.sqliteImage(),
			"entrypoint": []string{"/bin/sh", "-c"},
			"args":       []string{IdleScript},
		},
		Resources: &api.Resources{
			CPU:         new(sqliteCPUMHz),
			MemoryMB:    new(sqliteMemoryMB),
			MemoryMaxMB: new(sqliteMemoryMaxMB),
		},
		KillTimeout: new(sidecarKillTimeout),
	}
	if v, _, ok := svc.SQLiteFile(); ok {
		t.VolumeMounts = []*api.VolumeMount{{
			Volume:      new(v.Name),
			Destination: new(SQLiteVolumeDir(v.Name)),
			ReadOnly:    new(false),
		}}
	}
	return t
}

// uploadTask is the caravana-upload sidecar: the backup image, which has
// rclone, gzip and BusyBox, and no volume.
func uploadTask(s Settings) *api.Task {
	return &api.Task{
		Name:      app.UploadTaskName,
		Driver:    "docker",
		Lifecycle: &api.TaskLifecycle{Hook: "poststart", Sidecar: true},
		Config: map[string]any{
			"image":      s.backupImage(),
			"entrypoint": []string{"/bin/sh", "-c"},
			"args":       []string{IdleScript},
		},
		Resources: &api.Resources{
			CPU:         new(uploadCPUMHz),
			MemoryMB:    new(uploadMemoryMB),
			MemoryMaxMB: new(uploadMemoryMaxMB),
		},
		KillTimeout: new(sidecarKillTimeout),
	}
}

// The scripts below are run with SQLiteCommand. They are POSIX sh for BusyBox
// ash, use $VAR and $(...) only (never ${...}: one style for all of them, and
// the idle script is in the job, where Nomad interpolates it), and take their
// inputs as positional arguments.
//
// Each one that fails exits 1 with its reason, prefixed with the step (copy,
// check, upload, download, stage), as the last line of stderr; Caravana
// records "<step>: <reason>". Each one that succeeds prints the markers
// ParseSQLiteOutput reads.
//
// Every call to sqlite3, gzip and rclone runs under BusyBox timeout with what
// is left of the run's limit (the last argument), so one stuck call can't
// outlast the run.

// sqliteCommon is the start of every script below: the helpers they share.
// A script sets LIMIT (the seconds of the run) and calls set_deadline.
const sqliteCommon = `
# fail <message> says why on stderr, as its last line, and stops.
fail() {
  echo "$1" >&2
  exit 1
}

# set_deadline counts LIMIT from now. A LIMIT that is no number is the default
# of half an hour.
set_deadline() {
  case "$LIMIT" in ''|*[!0-9]*|0) LIMIT=1800 ;; esac
  DEADLINE=$(($(date +%s) + LIMIT))
}

# left prints the seconds of the limit that remain, at least 1: timeout 0 would
# mean no limit at all.
left() {
  remaining=$((DEADLINE - $(date +%s)))
  [ "$remaining" -ge 1 ] || remaining=1
  echo "$remaining"
}

# why <step> <stderr file> <exit status> prints "<step>: <reason>": the last
# line of the step's stderr, or that it ran out of time.
why() {
  line=$(tail -n 1 "$2" 2>/dev/null | tr -d '\r')
  case "$3" in
    124|143) echo "$1: timed out (the limit is $LIMIT seconds)" ;;
    *) if [ -n "$line" ]; then echo "$1: $line"; else echo "$1: failed (exit status $3)"; fi ;;
  esac
}

# work_dir_of <file> prints the directory the file is in, which must be a
# real directory of its own: the script empties it.
work_dir_of() {
  dir=$(dirname "$1")
  case "$dir" in /?*) echo "$dir" ;; *) return 1 ;; esac
}

# size_of <file> prints the file's size in bytes.
size_of() {
  wc -c < "$1" | tr -d ' '
}
`

// sqliteDestination reads the destination's credentials from the standard
// input: lines "RCLONE_CONFIG_DEST_<NAME>=<value>", which it exports for
// rclone. It reads nothing else and runs nothing: any other line is ignored,
// and a name with a character outside letters, digits and "_" too. After it
// the standard input is closed, so no later command reads it.
const sqliteDestination = `
read_destination() {
  while IFS= read -r line; do
    case "$line" in
      RCLONE_CONFIG_DEST_*=*)
        name=$(printf '%s' "$line" | cut -d= -f1)
        case "$name" in *[!A-Za-z0-9_]*) continue ;; esac
        value=$(printf '%s' "$line" | cut -d= -f2-)
        export "$name=$value"
        ;;
    esac
  done
  exec < /dev/null
}
`

// SQLiteCopyScript is run in caravana-sqlite as
//
//	sh -c <script> sh <db> <db as the service sees it> <out> <seconds>
//
// It copies the live database to <out> with VACUUM INTO, which reads the
// database in one transaction (consistent, and in WAL mode it never blocks the
// app's writers), then checks the copy with PRAGMA quick_check. The database
// must exist first: sqlite3 would create an empty one at a missing path. It
// runs as root, which is fine: SQLite gives the -wal and -shm files it creates
// to the database file's owner. It empties <out>'s directory first.
//
// On success it prints "caravana-sqlite-copy bytes=<n>", the copy's size.
const SQLiteCopyScript = `db=$1
shown=$2
out=$3
LIMIT=$4
` + sqliteCommon + `
set_deadline
case "$out" in ''|*\'*) fail "copy: the copy's path is not usable" ;; esac
work=$(work_dir_of "$out") || fail "copy: the copy's path is not usable"
trap 'rm -f "$out.err"' EXIT
rm -rf "$work" && mkdir -p "$work" || fail "copy: could not prepare $work"
[ -f "$db" ] || fail "copy: there is no database file at $shown"

timeout "$(left)" sqlite3 -bail -cmd ".timeout 60000" "$db" "VACUUM INTO '$out'" > /dev/null 2> "$out.err"
status=$?
if [ "$status" -ne 0 ]; then
  rm -f "$out"
  fail "$(why copy "$out.err" "$status")"
fi

check=$(timeout "$(left)" sqlite3 -bail "$out" "PRAGMA quick_check" 2> "$out.err")
status=$?
if [ "$status" -ne 0 ]; then
  rm -f "$out"
  fail "$(why check "$out.err" "$status")"
fi
if [ "$check" != ok ]; then
  rm -f "$out"
  fail "check: the copy is damaged: $(printf '%s\n' "$check" | head -n 1)"
fi
echo "` + sqliteCopyMarker + ` bytes=$(size_of "$out")"
`

// SQLiteUploadScript is run in caravana-upload as
//
//	sh -c <script> sh <copy> <backup path> <ts> <keep> <seconds> <pinned>
//
// with the destination's credentials on its standard input. <pinned> lists the
// timestamps of the pinned backups, separated by spaces, empty for none. It
// gzips the copy into "<backup path>/<ts>.sqlite.gz" in the destination (a pipe: no .gz on
// disk), with both exit codes checked (a pipeline's status is its last
// command's, and an archive cut short would otherwise upload as good), prints
// "caravana-backup key=<object> bytes=<n>", then deletes the oldest backups
// beyond <keep> and prints "caravana-expired <ts>.sqlite.gz" for each.
//
// A failed upload deletes the object it started. Retention comes after the
// upload is safe, so its failure doesn't fail the run: it prints
// "caravana-warning <text>" and exits 0. Only names like <digits>.sqlite.gz
// count, so nothing else under the path is touched, and this run's backup is
// never deleted. No <keep>, or a bad one, deletes nothing. The copy is always
// removed.
//
// A pinned backup is never deleted and doesn't count against <keep>: <keep> is
// how many unpinned backups stay. A <pinned> with anything but digits and
// spaces can't be trusted: nothing is deleted and a warning says so.
const SQLiteUploadScript = `copy=$1
path=$2
ts=$3
keep=$4
LIMIT=$5
pinned=$6
` + sqliteCommon + sqliteDestination + `
set_deadline
tmp="$copy.work"
trap 'rm -rf "$copy" "$tmp"' EXIT
read_destination
case "$ts" in ''|*[!0-9]*) fail "upload: the request has no timestamp" ;; esac
[ -n "$path" ] || fail "upload: the request has no backup destination"
[ -f "$copy" ] || fail "upload: there is no copy to upload at $copy"
mkdir -p "$tmp" || fail "upload: could not prepare $tmp"
object="$path/$ts.sqlite.gz"

# discard removes what the upload started. It gets its own minute: the run's
# own time may be spent.
discard() {
  timeout 60 rclone deletefile "dest:$object" > /dev/null 2>&1
  return 0
}

{
  timeout "$(left)" gzip -c "$copy" 2> "$tmp/gzip.err"
  echo $? > "$tmp/gzip.status"
} | timeout "$(left)" rclone rcat "dest:$object" 2> "$tmp/rclone.err"
uploaded=$?
zipped=$(cat "$tmp/gzip.status" 2>/dev/null)
# rclone's word comes first: when it dies, gzip fails with a broken pipe.
if [ "$uploaded" -ne 0 ]; then
  discard
  fail "$(why upload "$tmp/rclone.err" "$uploaded")"
fi
[ -n "$zipped" ] || zipped=1
if [ "$zipped" != 0 ]; then
  discard
  fail "$(why upload "$tmp/gzip.err" "$zipped")"
fi

sized=$(timeout "$(left)" rclone size --json "dest:$object" 2> "$tmp/rclone.err")
status=$?
if [ "$status" -ne 0 ]; then
  discard
  fail "$(why upload "$tmp/rclone.err" "$status")"
fi
bytes=$(printf '%s' "$sized" | sed -n 's/.*"bytes": *\([0-9][0-9]*\).*/\1/p')
if [ -z "$bytes" ]; then
  discard
  fail "upload: the size of the backup is unreadable"
fi
echo "` + BackupDoneMarker + ` key=$object bytes=$bytes"

# retain deletes the oldest unpinned backups beyond KEEP, as the volume engine does.
retain() {
  case "$keep" in ''|*[!0-9]*|0) return 0 ;; esac
  if printf '%s' "$pinned" | grep -q '[^0-9 ]'; then
    echo "` + sqliteWarningMarker + ` the backup is kept, but old backups were not removed: the list of pinned backups is not valid"
    return 0
  fi
  listing=$(timeout "$(left)" rclone lsf --files-only "dest:$path/" 2> "$tmp/rclone.err")
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "` + sqliteWarningMarker + ` the backup is kept, but old backups were not removed: $(why listing "$tmp/rclone.err" "$status")"
    return 0
  fi
  stamps=$(printf '%s\n' "$listing" | grep -E '^[0-9]+\.sqlite\.gz$' | sed 's/\.sqlite\.gz$//' | sort -n)
  candidates=
  for n in $stamps; do
    is_pinned=
    for p in $pinned; do
      [ "$n" != "$p" ] || is_pinned=1
    done
    [ -n "$is_pinned" ] || candidates="$candidates $n"
  done
  excess=$(($(echo $candidates | wc -w) - keep))
  [ "$excess" -gt 0 ] || return 0
  for n in $(echo $candidates | tr ' ' '\n' | head -n "$excess"); do
    [ "$n" != "$ts" ] || continue
    timeout "$(left)" rclone deletefile "dest:$path/$n.sqlite.gz" > /dev/null 2> "$tmp/rclone.err"
    status=$?
    if [ "$status" -ne 0 ]; then
      echo "` + sqliteWarningMarker + ` the backup is kept, but old backups were not removed: $(why "removing the backup $n" "$tmp/rclone.err" "$status")"
      return 0
    fi
    echo "` + BackupExpiredMarker + ` $n.sqlite.gz"
  done
}
retain
exit 0
`

// SQLiteDownloadScript is run in caravana-upload as
//
//	sh -c <script> sh <object> <out> <seconds>
//
// with the destination's credentials on its standard input. It streams the
// backup, gunzipped, into <out> (no .gz on disk), with both exit codes
// checked, and requires the file to start with "SQLite format 3". It empties
// <out>'s directory first. On success it prints
// "caravana-sqlite-download bytes=<n>", the file's size.
const SQLiteDownloadScript = `object=$1
out=$2
LIMIT=$3
` + sqliteCommon + sqliteDestination + `
set_deadline
read_destination
[ -n "$object" ] || fail "download: the request names no backup to restore"
work=$(work_dir_of "$out") || fail "download: the file's path is not usable"
tmp="$out.work"
trap 'rm -rf "$tmp"' EXIT
rm -rf "$work" && mkdir -p "$work" "$tmp" || fail "download: could not prepare $work"

{
  timeout "$(left)" rclone cat "dest:$object" 2> "$tmp/rclone.err"
  echo $? > "$tmp/rclone.status"
} | timeout "$(left)" gzip -dc > "$out" 2> "$tmp/gzip.err"
unzipped=$?
fetched=$(cat "$tmp/rclone.status" 2>/dev/null)
[ -n "$fetched" ] || fetched=1
# rclone's word comes first: when it dies, gzip fails on the cut-off input.
if [ "$fetched" -ne 0 ]; then
  rm -f "$out"
  fail "$(why download "$tmp/rclone.err" "$fetched")"
fi
if [ "$unzipped" -ne 0 ]; then
  rm -f "$out"
  fail "$(why download "$tmp/gzip.err" "$unzipped")"
fi
if [ "$(head -c 15 "$out")" != "SQLite format 3" ]; then
  rm -f "$out"
  fail "download: the backup is not a SQLite database"
fi
echo "` + sqliteDownloadMarker + ` bytes=$(size_of "$out")"
`

// SQLiteStageScript is run in caravana-sqlite as
//
//	sh -c <script> sh <downloaded> <volume dir> <request> <seconds>
//
// After the download, and while the service still runs, it checks the file
// with PRAGMA quick_check, refuses when an earlier restore left old files in
// <volume dir>/.caravana-restore/old (they may be the only copy of someone's
// data, and staging would delete them), and copies the file to
// <volume dir>/.caravana-restore/<request>.db: on the database's own disk,
// where the swap will be a rename. The download is always removed.
const SQLiteStageScript = `file=$1
dir=$2
request=$3
LIMIT=$4
` + sqliteCommon + `
set_deadline
trap 'rm -f "$file" "$file.err"' EXIT
case "$request" in ''|*[!A-Za-z0-9_-]*) fail "stage: the request ID is not usable" ;; esac
[ -f "$file" ] || fail "stage: there is no downloaded file at $file"
[ -d "$dir" ] || fail "stage: the volume is not mounted at $dir"

check=$(timeout "$(left)" sqlite3 -bail "$file" "PRAGMA quick_check" 2> "$file.err")
status=$?
[ "$status" -eq 0 ] || fail "$(why check "$file.err" "$status")"
[ "$check" = ok ] || fail "check: the backup is damaged: $(printf '%s\n' "$check" | head -n 1)"

if [ -e "$dir/.caravana-restore/old" ]; then
  fail "stage: $(basename "$dir") has files left in .caravana-restore/old by an earlier restore; check them, then delete that directory"
fi
rm -rf "$dir/.caravana-restore"
mkdir -p "$dir/.caravana-restore" 2> "$file.err"
status=$?
[ "$status" -eq 0 ] || fail "$(why stage "$file.err" "$status")"
cp "$file" "$dir/.caravana-restore/$request.db" 2> "$file.err"
status=$?
if [ "$status" -ne 0 ]; then
  rm -rf "$dir/.caravana-restore"
  fail "$(why stage "$file.err" "$status")"
fi
`

// Output markers of the sqlite scripts, besides the backup's own
// (BackupDoneMarker and BackupExpiredMarker).
const (
	// sqliteCopyMarker starts "caravana-sqlite-copy bytes=<n>".
	sqliteCopyMarker = "caravana-sqlite-copy"
	// sqliteDownloadMarker starts "caravana-sqlite-download bytes=<n>".
	sqliteDownloadMarker = "caravana-sqlite-download"
	// sqliteWarningMarker starts "caravana-warning <text>": the backup is good
	// but retention failed.
	sqliteWarningMarker = "caravana-warning"
)

// SQLiteOutput is what the markers of a script's standard output say. A field
// is zero when its marker is absent.
type SQLiteOutput struct {
	// Bytes is the copy's, the download's or the uploaded object's size.
	Bytes int64
	// Object is where the backup is in the destination
	// (<backup path>/<ts>.sqlite.gz).
	Object string
	// Expired lists the backups retention deleted, by timestamp.
	Expired []string
	// Warning says why retention failed after a good upload.
	Warning string
}

// ParseSQLiteOutput reads the markers of a script's standard output. Lines
// without a marker are skipped, and so is a marker whose number isn't one: a
// result Caravana can't read is a result it doesn't record, and the caller
// decides what a missing size means.
func ParseSQLiteOutput(stdout string) SQLiteOutput {
	var out SQLiteOutput
	for line := range strings.Lines(stdout) {
		line = strings.TrimRight(line, "\r\n")
		marker, rest, _ := strings.Cut(line, " ")
		switch marker {
		case sqliteCopyMarker, sqliteDownloadMarker:
			if n, ok := bytesOf(rest); ok {
				out.Bytes = n
			}
		case BackupDoneMarker:
			// "key=<object> bytes=<n>": the object may hold spaces, the size
			// comes last.
			key, size, ok := cutLast(strings.TrimPrefix(rest, "key="), " bytes=")
			if !ok || !strings.HasPrefix(rest, "key=") {
				continue
			}
			if n, err := strconv.ParseInt(size, 10, 64); err == nil && n >= 0 {
				out.Object, out.Bytes = key, n
			}
		case BackupExpiredMarker:
			if ts, ok := strings.CutSuffix(rest, ".sqlite.gz"); ok && ts != "" {
				out.Expired = append(out.Expired, ts)
			}
		case sqliteWarningMarker:
			out.Warning = rest
		}
	}
	return out
}

// bytesOf reads "bytes=<n>".
func bytesOf(s string) (int64, bool) {
	v, ok := strings.CutPrefix(s, "bytes=")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil && n >= 0
}
