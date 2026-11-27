package nomadjob

import (
	"strconv"
	"strings"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// The copy job (feature 052, plan D8). After a backup succeeds, Caravana copies
// it to another destination with a one-off batch job that runs rclone between
// two remotes of S3-compatible buckets: "src" (where the backup is) and "dest"
// (where it goes). Like the backup and restore jobs it reads everything from its
// own Nomad Variable, nomad/jobs/<job>, which only its task (and Caravana) can
// read: the keys of both destinations are in that variable and in no other place.
//
// Like Translate, these functions are pure.

// BackupCopyJobID is the copy job of one backup of a service. The ID starts with
// the app's job prefix (AppJobPrefix), so deleting the app purges its copies.
// id is the copy's own 12 lowercase hex characters.
func BackupCopyJobID(appID, service, id string) string {
	return appID + "_" + service + "_copy-" + id
}

// BackupCopyJobMarker is what every copy job's ID holds after its service:
// Caravana's event watcher tells copy jobs from the rest by it.
const BackupCopyJobMarker = "_copy-"

// BackupCopyMarker starts the lines the copy task prints:
//
//	caravana-copy: done key=<object> bytes=<n>
//	caravana-copy: expired <name>
//	caravana-copy: warning <why old copies were not removed>
const BackupCopyMarker = "caravana-copy:"

// BackupCopyTaskName is the job's one task, whose output carries the markers.
const BackupCopyTaskName = "copy"

// The kinds of object a copy job copies: one file (a dump, a SQLite database)
// or a directory (a volume backup's archives).
const (
	CopyKindFile = "file"
	CopyKindDir  = "dir"
)

// ObjectExt is the extension of the object a backup of the engine leaves in a
// destination (".dump", ".sql.gz", ".archive.gz", ".sqlite.gz", ".opensearch.tar.gz"), and "" for a
// volume backup, which leaves a directory.
func ObjectExt(engine string) string {
	switch engine {
	case app.BackupVolume:
		return ""
	case app.BackupSQLite:
		return ".sqlite.gz"
	case app.BackupOpenSearch:
		return OpenSearchObjectExt
	}
	return recipeFor(engine).ext
}

// BackupCopyScript is the copy task's command, run as /bin/sh -c in the backup
// image (BusyBox ash). It is POSIX sh, and uses $VAR and $(...) only: Nomad
// interpolates ${...} in task config before the shell sees it.
//
// The job's variable gives it SRC_OBJECT (where the backup is in the source
// destination), DEST_OBJECT (where it goes in the target one), DEST_PATH (the
// target's folder for the service's backups), KEEP (how many copies the target
// keeps) and PINNED (the timestamps of the pinned backups, digits separated by
// spaces), beside the two remotes' RCLONE_CONFIG_SRC_* and RCLONE_CONFIG_DEST_*.
// The task's env gives it KIND (file or dir) and EXT (the object's extension,
// "" for a directory): neither is a secret, and the job says what it copies.
//
// It copies, asks rclone the size of what arrived (a copy of nothing is a
// failure: the run's last line says "the copy is empty"), prints the done line,
// and then applies the target's retention as the upload script does it: the
// copies of this service beyond KEEP, oldest first, never a pinned one and never
// the one just made. The copy is safe by then, so a retention that can't be done
// doesn't fail the run: it prints a warning line, and deletes nothing when the
// list of pinned backups can't be trusted.
const BackupCopyScript = `set -eu
for name in KIND SRC_OBJECT DEST_OBJECT DEST_PATH; do
  [ -n "$(printenv "$name" || true)" ] || { echo "the copy job has no $name" >&2; exit 1; }
done
ext=$(printenv EXT || true)
keep=$(printenv KEEP || true)
pinned=$(printenv PINNED || true)
err=/tmp/caravana-copy.$$.err
case "$KIND" in
  ` + CopyKindFile + `) rclone copyto "src:$SRC_OBJECT" "dest:$DEST_OBJECT" ;;
  ` + CopyKindDir + `) rclone copy "src:$SRC_OBJECT" "dest:$DEST_OBJECT" ;;
  *) echo "the copy job's kind is neither file nor dir" >&2; exit 1 ;;
esac
size=$(rclone size --json "dest:$DEST_OBJECT")
count=$(printf '%s' "$size" | sed -n 's/.*"count": *\([0-9][0-9]*\).*/\1/p')
bytes=$(printf '%s' "$size" | sed -n 's/.*"bytes": *\([0-9][0-9]*\).*/\1/p')
if [ -z "$count" ] || [ "$count" -eq 0 ]; then
  echo "the copy is empty: nothing arrived in the destination" >&2
  exit 1
fi
[ -n "$bytes" ] || bytes=0
echo "` + BackupCopyMarker + ` done key=$DEST_OBJECT bytes=$bytes"

warn() { echo "` + BackupCopyMarker + ` warning the copy is kept, but old copies were not removed: $1"; exit 0; }
case "$keep" in ''|*[!0-9]*|0) exit 0 ;; esac
if printf '%s' "$pinned" | grep -q '[^0-9 ]'; then
  warn "the list of pinned backups is not valid"
fi
own=$(basename "$DEST_OBJECT")
if [ "$KIND" = ` + CopyKindDir + ` ]; then
  listing=$(rclone lsf --dirs-only "dest:$DEST_PATH/" 2> "$err") || warn "$(tail -n 1 "$err")"
  names=$(echo "$listing" | grep -E '^[0-9]+/$' | sort -n | while read -r d; do basename "$d"; done)
else
  listing=$(rclone lsf --files-only "dest:$DEST_PATH/" 2> "$err") || warn "$(tail -n 1 "$err")"
  names=$(echo "$listing" | sort)
fi
candidates=
for n in $names; do
  case "$n" in
    *"$ext") ;;
    *) continue ;;
  esac
  is_pinned=
  for p in $pinned; do
    [ "$n" != "$p$ext" ] || is_pinned=1
  done
  [ -n "$is_pinned" ] || candidates="$candidates $n"
done
excess=$(($(echo $candidates | wc -w) - keep))
[ "$excess" -gt 0 ] || exit 0
for n in $(echo $candidates | tr ' ' '\n' | head -n "$excess"); do
  [ "$n" != "$own" ] || continue
  if [ "$KIND" = ` + CopyKindDir + ` ]; then
    rclone purge "dest:$DEST_PATH/$n" 2> "$err" || warn "$(tail -n 1 "$err")"
  else
    rclone deletefile "dest:$DEST_PATH/$n" 2> "$err" || warn "$(tail -n 1 "$err")"
  fi
  echo "` + BackupCopyMarker + ` expired $n"
done
`

// BackupCopyJob is the one-off job that copies one backup of service of appID
// to another destination. id is the copy's own ID, the one BackupCopyJobID takes;
// kind is CopyKindFile or CopyKindDir and ext the engine's extension (".dump",
// ".sql.gz", ".archive.gz", ".sqlite.gz", "" for a directory). The script's
// other inputs and both destinations' keys are in the job's own variable, which
// the job doesn't hold.
func BackupCopyJob(appID, service, id, kind, ext string, s Settings) *api.Job {
	jobID := BackupCopyJobID(appID, service, id)
	j := batchJob(jobID, appID, service)
	j.TaskGroups = []*api.TaskGroup{batchGroup("copy", &api.Task{
		Name:      BackupCopyTaskName,
		Driver:    "docker",
		Config:    map[string]any{"image": s.backupImage(), "entrypoint": []string{"/bin/sh", "-c"}, "args": []string{BackupCopyScript}},
		Env:       map[string]string{"KIND": kind, "EXT": ext},
		Templates: []*api.Template{variableTemplate(jobID)},
		Resources: &api.Resources{CPU: new(200), MemoryMB: new(256)},
	})}
	return j
}

// BackupCopyOutput is what the copy task's output says.
type BackupCopyOutput struct {
	// Done is false when no done line was printed.
	Done bool
	// Object is the copy's path in the target destination and Bytes the size
	// rclone measured, when Done.
	Object string
	Bytes  int64
	// Expired lists what the target's retention deleted, by name ("1791.dump",
	// or "1791" for a directory).
	Expired []string
	// Warning is why retention couldn't finish, when it couldn't; the copy is
	// still good.
	Warning string
}

// ParseBackupCopyOutput reads the lines the copy task printed.
func ParseBackupCopyOutput(lines []string) BackupCopyOutput {
	var out BackupCopyOutput
	for _, line := range lines {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), BackupCopyMarker+" ")
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(rest, "done "):
			for _, f := range strings.Fields(strings.TrimPrefix(rest, "done ")) {
				k, v, _ := strings.Cut(f, "=")
				switch k {
				case "key":
					out.Object = v
				case "bytes":
					if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
						out.Bytes = n
					}
				}
			}
			out.Done = out.Object != ""
		case strings.HasPrefix(rest, "expired "):
			out.Expired = append(out.Expired, strings.TrimSpace(strings.TrimPrefix(rest, "expired ")))
		case strings.HasPrefix(rest, "warning "):
			out.Warning = strings.TrimSpace(strings.TrimPrefix(rest, "warning "))
		}
	}
	return out
}
