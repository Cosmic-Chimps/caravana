package nomadjob

import (
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// The opensearch backup scripts (feature 061, plan D3–D6). The cluster takes a
// snapshot of itself through its own API into a folder on a volume, with the
// service running; Caravana archives that folder to the backup destination.
//
// Two places run commands, both with Nomad's alloc exec:
//
//   - the service's own task runs the API calls (snapshot and restore): its
//     image has sh, curl, grep and sed, and the cluster is on 127.0.0.1;
//   - caravana-upload, the backup image, which for this engine mounts the
//     snapshot folder's volume (uploadTask), removes old folders, and archives
//     and extracts the folder (clean, upload and download).
//
// The scripts share the sqlite ones' shape and helpers (SQLiteCommand,
// sqliteCommon, sqliteDestination, uploadRetention): positional arguments, a
// result file under OpenSearchWorkDir that repeats the markers, a failure that
// is one last line of stderr starting with the step. The destination's keys
// and the cluster's login travel on the standard input only.
//
// Like Translate, everything here is pure.

// OpenSearchWorkDir holds the result files of the opensearch steps. The first
// clean of a run empties it.
const OpenSearchWorkDir = VolumeDataDir + "/opensearch"

// OpenSearchObjectExt is the extension of an opensearch backup's object in the
// destination: the snapshot folder, tarred and gzipped.
const OpenSearchObjectExt = ".opensearch.tar.gz"

// OpenSearchObject is the backup's object in the destination: backupPath is the
// service's BACKUP_PATH and ts the run's unix seconds.
func OpenSearchObject(backupPath, ts string) string {
	return backupPath + "/" + ts + OpenSearchObjectExt
}

// OpenSearchFolderName is the folder of the run named request inside the
// repository. Every run has a fresh one and the clean step removes only the
// folders that start with "caravana-".
func OpenSearchFolderName(request string) string { return "caravana-" + request }

// OpenSearchRepositoryDir is the repository folder as caravana-upload sees it:
// inVolume is the folder's path inside its volume (app.Service.OpenSearchRepository),
// "" for the mount itself.
func OpenSearchRepositoryDir(volume, inVolume string) string {
	if inVolume == "" {
		return SQLiteVolumeDir(volume)
	}
	return SQLiteVolumeDir(volume) + "/" + inVolume
}

// OpenSearchResultPath is where the step's script also writes the markers it
// prints, for the reason SQLiteResultPath says.
func OpenSearchResultPath(request, step string) string {
	return OpenSearchWorkDir + "/" + request + "." + step + ".result"
}

// OpenSearchResultFile is OpenSearchResultPath as Caravana reads it through the
// allocation's file system.
func OpenSearchResultFile(request, step string) string {
	return strings.TrimPrefix(OpenSearchResultPath(request, step), "/")
}

// OpenSearchCommand is the argv that runs script with args, as SQLiteCommand.
func OpenSearchCommand(script string, args ...string) []string { return SQLiteCommand(script, args...) }

// hasOpenSearchBackup reports whether svc is backed up with the opensearch engine.
func hasOpenSearchBackup(svc app.Service) bool {
	return svc.Backup != nil && svc.Backup.Engine == app.BackupOpenSearch
}

// Output markers of the opensearch scripts, besides the backup's own.
const (
	// opensearchSnapshotMarker starts "caravana-opensearch-snapshot indices=<n>".
	opensearchSnapshotMarker = "caravana-opensearch-snapshot"
	// opensearchRestoreMarker starts "caravana-opensearch-restore indices=<n>".
	opensearchRestoreMarker = "caravana-opensearch-restore"
	// opensearchDownloadMarker starts "caravana-opensearch-download files=<n>".
	opensearchDownloadMarker = "caravana-opensearch-download"
)

// OpenSearchLoginRefused is the reason of a run that OpenSearch answered 401. It
// names the variables app.FindDumpLogin reads (a test keeps the two equal).
const OpenSearchLoginRefused = "OpenSearch refused the login: set OPENSEARCH_INITIAL_ADMIN_PASSWORD, INDEXER_PASSWORD or ELASTIC_PASSWORD"

// openSearchCleanScript is run in caravana-upload as
//
//	sh -c <script> sh <repository> <work dir> <fresh|keep>
//
// It makes the work dir (world-writable: the service's own task, which runs as
// another user, writes its result files there), empties it when the mode is
// "fresh" (the first clean of a run), and removes the "caravana-*" folders
// earlier runs left in the repository. Only those: a folder of another name,
// and a link, is never touched. A repository folder that doesn't exist yet is
// made world-writable, like the volume it is in: this script runs as root, and
// the cluster, another user, makes each run's folder in it.
const OpenSearchCleanScript = `repo=$1
work=$2
mode=$3
` + sqliteCommon + `
case "$repo" in /?*) ;; *) fail "clean: the repository's path is not usable" ;; esac
case "$work" in /?*) ;; *) fail "clean: the work directory's path is not usable" ;; esac
mkdir -p "$work" || fail "clean: could not prepare $work"
chmod 777 "$work" 2>/dev/null
if [ "$mode" = fresh ]; then
  rm -f "$work"/*.result
  rm -rf "$work"/*.work
fi
if [ ! -d "$repo" ]; then
  mkdir -p "$repo" && chmod 777 "$repo" || fail "clean: could not make the repository folder $repo"
fi
for old in "$repo"/caravana-*; do
  [ -d "$old" ] || continue
  [ ! -L "$old" ] || continue
  rm -rf "$old" || fail "clean: could not remove $old"
done
exit 0
`

// openSearchAPI is the start of the two scripts that call the cluster's API: the
// login, the scheme, one call function and the way a failure is worded. They
// set PORT (the service's container port), LIMIT and RESULT, and call
// set_deadline, read_login, find_base and, before the first call, make tmp.
//
// The login comes on the standard input as lines "OPENSEARCH_USER=<user>" and
// "OPENSEARCH_PASSWORD=<password>" (neither or both may be there: a cluster with
// its security plugin off has none). It is held in a shell variable and given
// to each curl as a configuration on its own standard input, so it is in no
// argument, no file and no process listing (printf is a shell builtin).
// A value with a line break is refused. https is tried first with the
// certificate unchecked (loopback, inside the copy's own network namespace,
// where the security plugin's certificates are self-signed) and http only when
// the TLS handshake itself fails.
const openSearchAPI = sqliteCommon + `
# The service's task may not be able to write RESULT; the markers are on
# stdout as well.
say() {
  echo "$1"
  [ -z "$RESULT" ] || echo "$1" >> "$RESULT" 2>/dev/null || true
}

read_login() {
  OS_USER=
  OS_PASSWORD=
  while IFS= read -r line; do
    case "$line" in
      OPENSEARCH_USER=*) OS_USER=$(printf '%s' "$line" | cut -d= -f2-) ;;
      OPENSEARCH_PASSWORD=*) OS_PASSWORD=$(printf '%s' "$line" | cut -d= -f2-) ;;
    esac
  done
  exec < /dev/null
  cr=$(printf '\r')
  case "$OS_USER$OS_PASSWORD" in *"$cr"*) fail "$STEP: the login has a line break" ;; esac
  OS_CONFIG=
  if [ -n "$OS_USER$OS_PASSWORD" ]; then
    quoted_user=$(printf '%s' "$OS_USER" | sed 's/\\/\\\\/g; s/"/\\"/g')
    quoted_password=$(printf '%s' "$OS_PASSWORD" | sed 's/\\/\\\\/g; s/"/\\"/g')
    OS_CONFIG="user = \"$quoted_user:$quoted_password\""
  fi
}

# find_base chooses https or http for 127.0.0.1:$PORT. A refused connection or a
# timeout is the answer; only a failed TLS handshake (curl 35, or the server
# closing on it: 52, 56) tries the other scheme.
find_base() {
  for scheme in https http; do
    curl -sSk --max-time "$(left)" -o /dev/null "$scheme://127.0.0.1:$PORT/" 2> "$tmp/curl.err"
    probed=$?
    case "$probed" in
      0) BASE="$scheme://127.0.0.1:$PORT"; return 0 ;;
      35|52|56) continue ;;
      *) fail "$(why "$STEP" "$tmp/curl.err" "$probed")" ;;
    esac
  done
  fail "$STEP: could not reach OpenSearch on port $PORT: $(tail -n 1 "$tmp/curl.err" | tr -d '\r')"
}

# call <method> <path> [<json body>] makes one API call: the HTTP status is in
# CODE, the body in $tmp/body, curl's own exit status in CURL_STATUS. MAXTIME
# (seconds) replaces what is left of the run's limit.
call() {
  method=$1
  target=$2
  payload=$3
  budget=$(left)
  [ -z "$MAXTIME" ] || budget=$MAXTIME
  : > "$tmp/body"
  set -- -sSk --max-time "$budget" -X "$method" -o "$tmp/body" -w '%{http_code}'
  [ -z "$payload" ] || set -- "$@" -H 'Content-Type: application/json' --data-binary "$payload"
  if [ -n "$OS_CONFIG" ]; then
    CODE=$(printf '%s\n' "$OS_CONFIG" | curl -K - "$@" "$BASE$target" 2> "$tmp/curl.err")
  else
    CODE=$(curl "$@" "$BASE$target" 2> "$tmp/curl.err")
  fi
  CURL_STATUS=$?
}

# ok says whether the last call answered 2xx.
ok() {
  [ "$CURL_STATUS" -eq 0 ] || return 1
  case "$CODE" in 2??) return 0 ;; esac
  return 1
}

# problem_of <step> words why the last call failed: curl's own sentence, the
# login hint for a 401, OpenSearch's first reason, or the HTTP status.
problem_of() {
  if [ "$CURL_STATUS" -ne 0 ]; then
    why "$1" "$tmp/curl.err" "$CURL_STATUS"
    return 0
  fi
  if [ "$CODE" = 401 ]; then
    echo "$1: ` + OpenSearchLoginRefused + `"
    return 0
  fi
  reason=$(first_reason "$tmp/body")
  [ -n "$reason" ] || reason="OpenSearch answered HTTP $CODE"
  echo "$1: $reason"
}

# first_reason <body file> prints the first "reason" of an OpenSearch error, with
# its escapes undone, or nothing.
first_reason() {
  grep -oE '"reason" *: *"([^"\\]|\\.)*"' "$1" 2>/dev/null | head -n 1 | sed 's/^"reason" *: *"//; s/"$//; s/\\"/"/g; s/\\\\/\\/g' | tr -d '\r'
}

# check <step> fails with the reason of the last call unless it answered 2xx.
check() {
  ok || fail "$(problem_of "$1")"
}

# list_of <body file> prints the first "indices" list of an answer as
# "a,b,c": index names have no quote or comma in them.
list_of() {
  grep -oE '"indices" *: *\[[^]]*\]' "$1" 2>/dev/null | head -n 1 | sed 's/^[^[]*\[//; s/\]$//' | tr -d '" '
}

# count_of <a,b,c> prints how many names there are.
count_of() {
  [ -n "$1" ] || { echo 0; return 0; }
  printf '%s\n' "$1" | tr ',' '\n' | grep -c .
}

# shards_ok <body file> says no shard failed in an answer that has a shard count.
shards_ok() {
  grep -qE '"failed" *: *0([^0-9]|$)' "$1"
}
`

// OpenSearchSnapshotScript is run in the service's own task as
//
//	sh -c <script> sh <repository> <request> <ts> <port> <seconds> <result>
//
// with the cluster's login on its standard input. It registers a shared-filesystem
// repository named "caravana" at <repository>/caravana-<request>, takes the
// snapshot <ts> of every index whose name doesn't start with a dot, without
// the global state, waits for it, requires state SUCCESS and no failed shard,
// and unregisters the repository (also when it fails). Any OpenSearch error's
// first reason is the failure: "snapshot: <reason>". A repository location
// outside path.repo is refused by the cluster with its own sentence.
//
// On success it prints "caravana-opensearch-snapshot indices=<n>".
const OpenSearchSnapshotScript = `repo=$1
request=$2
ts=$3
PORT=$4
LIMIT=$5
RESULT=$6
STEP=snapshot
` + openSearchAPI + `
set_deadline
case "$ts" in ''|*[!0-9]*) fail "snapshot: the request has no timestamp" ;; esac
case "$request" in ''|*[!A-Za-z0-9_-]*) fail "snapshot: the request ID is not usable" ;; esac
case "$PORT" in ''|*[!0-9]*) fail "snapshot: the service's port is not usable" ;; esac
case "$repo" in /?*) ;; *) fail "snapshot: the repository's path is not usable" ;; esac
tmp=$(mktemp -d) || fail "snapshot: could not make a work directory"
registered=
cleanup() {
  if [ -n "$registered" ]; then
    MAXTIME=30
    call DELETE /_snapshot/caravana
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
read_login
find_base

# A repository an earlier run left registered would keep its old location.
call DELETE /_snapshot/caravana
folder="$repo/caravana-$request"
call PUT /_snapshot/caravana "{\"type\":\"fs\",\"settings\":{\"location\":\"$folder\"}}"
check snapshot
registered=1

call PUT "/_snapshot/caravana/$ts?wait_for_completion=true" '{"indices":"*,-.*","include_global_state":false}'
check snapshot
if ! grep -qE '"state" *: *"SUCCESS"' "$tmp/body" || ! shards_ok "$tmp/body"; then
  state=$(grep -oE '"state" *: *"[A-Z_]+"' "$tmp/body" | head -n 1 | cut -d'"' -f4)
  reason=$(first_reason "$tmp/body")
  [ -z "$reason" ] || reason=": $reason"
  [ -n "$state" ] || state=unreadable
  fail "snapshot: the snapshot ended $(echo "$state" | tr 'A-Z' 'a-z')$reason"
fi
indices=$(list_of "$tmp/body")

call DELETE /_snapshot/caravana
registered=
say "` + opensearchSnapshotMarker + ` indices=$(count_of "$indices")"
`

// OpenSearchRestoreScript is run in the service's own task as
//
//	sh -c <script> sh <repository> <request> <port> <seconds> <result>
//
// with the cluster's login on its standard input, after the backup's folder
// was extracted to <repository>/caravana-<request>. It registers it read-only
// as "caravana", reads the one snapshot in it, and for the snapshot's indices:
// closes those that exist (a snapshot is restored only over a closed index),
// restores them and lets the restore open them. Indices made since stay. When
// the restore fails after the close, it tries to open them again, and says in
// its reason whether that worked. The repository is unregistered either way.
//
// On success it prints "caravana-opensearch-restore indices=<n>".
const OpenSearchRestoreScript = `repo=$1
request=$2
PORT=$3
LIMIT=$4
RESULT=$5
STEP=restore
` + openSearchAPI + `
set_deadline
case "$request" in ''|*[!A-Za-z0-9_-]*) fail "restore: the request ID is not usable" ;; esac
case "$PORT" in ''|*[!0-9]*) fail "restore: the service's port is not usable" ;; esac
case "$repo" in /?*) ;; *) fail "restore: the repository's path is not usable" ;; esac
tmp=$(mktemp -d) || fail "restore: could not make a work directory"
registered=
cleanup() {
  if [ -n "$registered" ]; then
    MAXTIME=30
    call DELETE /_snapshot/caravana
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT
read_login
find_base

call DELETE /_snapshot/caravana
folder="$repo/caravana-$request"
call PUT /_snapshot/caravana "{\"type\":\"fs\",\"settings\":{\"location\":\"$folder\",\"readonly\":true}}"
check restore
registered=1

call GET /_snapshot/caravana/_all
check restore
snapshot=$(grep -oE '"snapshot" *: *"[^"]*"' "$tmp/body" | head -n 1 | cut -d'"' -f4)
case "$snapshot" in ''|*[!A-Za-z0-9._-]*) fail "restore: the backup holds no snapshot Caravana can read" ;; esac
if ! grep -qE '"state" *: *"SUCCESS"' "$tmp/body"; then
  state=$(grep -oE '"state" *: *"[A-Z_]+"' "$tmp/body" | head -n 1 | cut -d'"' -f4)
  [ -n "$state" ] || state=unreadable
  fail "restore: the snapshot in the backup is not complete (state $(echo "$state" | tr 'A-Z' 'a-z'))"
fi
indices=$(list_of "$tmp/body")
case "$indices" in *[!A-Za-z0-9._+@,-]*) fail "restore: the snapshot has an index whose name Caravana can't restore" ;; esac

# reopen says what became of the indices the restore closed, as a sentence for
# the end of the reason.
reopen() {
  MAXTIME=60
  call POST "/$indices/_open?ignore_unavailable=true"
  MAXTIME=
  if [ "$CURL_STATUS" -eq 0 ]; then
    case "$CODE" in 2??) echo "; the indices it closed were opened again"; return 0 ;; esac
  fi
  echo "; opening the indices it closed again failed, so they may still be closed"
}

if [ -n "$indices" ]; then
  call POST "/$indices/_close?ignore_unavailable=true"
  if ! ok; then
    problem=$(problem_of restore)
    fail "$problem$(reopen)"
  fi
  call POST "/_snapshot/caravana/$snapshot/_restore?wait_for_completion=true" "{\"indices\":\"$indices\",\"include_global_state\":false}"
  problem=
  if ! ok; then
    problem=$(problem_of restore)
  elif ! shards_ok "$tmp/body"; then
    problem="restore: some shards could not be restored"
    reason=$(first_reason "$tmp/body")
    [ -z "$reason" ] || problem="$problem: $reason"
  fi
  [ -z "$problem" ] || fail "$problem$(reopen)"
fi

call DELETE /_snapshot/caravana
registered=
say "` + opensearchRestoreMarker + ` indices=$(count_of "$indices")"
`

// OpenSearchUploadScript is run in caravana-upload as
//
//	sh -c <script> sh <folder> <backup path> <ts> <keep> <seconds> <pinned> <result>
//
// with the destination's credentials on its standard input. <folder> is the
// run's snapshot folder (OpenSearchRepositoryDir + OpenSearchFolderName). It
// tars and gzips the folder, a pipe with no archive on disk, into
// "<backup path>/<ts>.opensearch.tar.gz" in the destination, with both exit
// codes checked (a pipeline's status is its last command's), prints
// "caravana-backup key=<object> bytes=<n>", then applies retention as the sqlite
// upload does: the oldest unpinned backups beyond <keep>, only names like
// <digits>.opensearch.tar.gz, never this run's. A failed upload deletes the
// object it started; a failed retention is a warning. The folder is always
// removed.
const OpenSearchUploadScript = `folder=$1
path=$2
ts=$3
keep=$4
LIMIT=$5
pinned=$6
RESULT=$7
` + sqliteCommon + sqliteDestination + `
set_deadline
[ -n "$RESULT" ] || fail "upload: the request names no result file"
case "$(basename "$folder")" in caravana-?*) ;; *) fail "upload: the snapshot folder's path is not usable" ;; esac
tmp="$RESULT.work"
trap 'rm -rf "$folder" "$tmp"' EXIT
rm -f "$RESULT"
read_destination
case "$ts" in ''|*[!0-9]*) fail "upload: the request has no timestamp" ;; esac
[ -n "$path" ] || fail "upload: the request has no backup destination"
[ -d "$folder" ] || fail "upload: there is no snapshot folder at $folder"
[ -f "$folder/index.latest" ] || fail "upload: the snapshot folder has no snapshot in it"
mkdir -p "$tmp" || fail "upload: could not prepare $tmp"
ext=` + OpenSearchObjectExt + `
object="$path/$ts$ext"

# discard removes what the upload started. It gets its own minute: the run's
# own time may be spent.
discard() {
  timeout 60 rclone deletefile "dest:$object" > /dev/null 2>&1
  return 0
}

{
  timeout "$(left)" tar -C "$folder" -czf - . 2> "$tmp/tar.err"
  echo $? > "$tmp/tar.status"
} | timeout "$(left)" rclone rcat "dest:$object" 2> "$tmp/rclone.err"
uploaded=$?
archived=$(cat "$tmp/tar.status" 2>/dev/null)
# rclone's word comes first: when it dies, tar fails with a broken pipe.
if [ "$uploaded" -ne 0 ]; then
  discard
  fail "$(why upload "$tmp/rclone.err" "$uploaded")"
fi
[ -n "$archived" ] || archived=1
if [ "$archived" != 0 ]; then
  discard
  fail "$(why upload "$tmp/tar.err" "$archived")"
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
say "` + BackupDoneMarker + ` key=$object bytes=$bytes"
` + uploadRetention + `
retain
exit 0
`

// OpenSearchDownloadScript is run in caravana-upload as
//
//	sh -c <script> sh <object> <folder> <seconds> <result>
//
// with the destination's credentials on its standard input. It streams the
// backup into tar, which extracts it to <folder> (emptied first), with both
// exit codes checked, and requires the folder to hold a snapshot
// (index.latest). On success it prints "caravana-opensearch-download files=<n>".
// A failed download removes the folder.
const OpenSearchDownloadScript = `object=$1
folder=$2
LIMIT=$3
RESULT=$4
` + sqliteCommon + sqliteDestination + `
set_deadline
[ -n "$RESULT" ] || fail "download: the request names no result file"
read_destination
[ -n "$object" ] || fail "download: the request names no backup to restore"
case "$(basename "$folder")" in caravana-?*) ;; *) fail "download: the snapshot folder's path is not usable" ;; esac
tmp="$RESULT.work"
trap 'rm -rf "$tmp"' EXIT
rm -rf "$folder" && mkdir -p "$folder" "$tmp" || fail "download: could not prepare $folder"

{
  timeout "$(left)" rclone cat "dest:$object" 2> "$tmp/rclone.err"
  echo $? > "$tmp/rclone.status"
} | timeout "$(left)" tar -C "$folder" -xzf - 2> "$tmp/tar.err"
unpacked=$?
fetched=$(cat "$tmp/rclone.status" 2>/dev/null)
[ -n "$fetched" ] || fetched=1
# Whichever failed first has the reason. rclone's own words come first, unless they
# are only the broken pipe (or nothing, when it was killed by it) that a tar which
# stopped leaves behind.
if [ "$fetched" -ne 0 ] && { [ "$unpacked" -eq 0 ] || { [ -s "$tmp/rclone.err" ] && ! grep -qi pipe "$tmp/rclone.err"; }; }; then
  rm -rf "$folder"
  fail "$(why download "$tmp/rclone.err" "$fetched")"
fi
if [ "$unpacked" -ne 0 ]; then
  rm -rf "$folder"
  fail "$(why download "$tmp/tar.err" "$unpacked")"
fi
if [ ! -f "$folder/index.latest" ]; then
  rm -rf "$folder"
  fail "download: the backup holds no OpenSearch snapshot"
fi
say "` + opensearchDownloadMarker + ` files=$(find "$folder" -type f | wc -l | tr -d ' ')"
`
