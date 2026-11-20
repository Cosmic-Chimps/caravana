package nomadjob

import (
	"strconv"
	"strings"

	"github.com/hashicorp/nomad/api"
)

// The archive job (feature 045, plan D4). Before the owner deletes an orphan
// host volume (one no stored app declares), Caravana may archive its files to
// the backup destination with a one-off batch job that mounts the volume. Like
// the backup and restore jobs it reads the destination from its own Nomad
// Variable, nomad/jobs/<job>, which only its task (and Caravana) can read.
//
// Like Translate, these functions are pure.

// ArchiveJobID is the archive job of a host volume. A volume's name is
// <app>_<volume>, so the ID starts with the app's job prefix (AppJobPrefix)
// and goes with the app's other helper jobs when the app is deleted.
func ArchiveJobID(volume string) string { return volume + "_archive" }

// ArchiveMarker starts the last line the archive task prints:
// "caravana-archive: ok object=<path> bytes=<n>" or
// "caravana-archive: failed: <message>".
const ArchiveMarker = "caravana-archive:"

// ArchiveTaskName is the job's one task, whose output carries the marker.
const ArchiveTaskName = "archive"

// archiveVolumeName is the group's name for the volume and the task's mount
// point is archiveMount.
const (
	archiveVolumeName = "data"
	archiveMount      = "/data"
)

// ArchiveLimitSeconds is the time the script gives the whole run. Caravana
// waits half an hour for the job, so the script's own limit ends it first.
const (
	ArchiveLimitSeconds = 1700
	archiveLimit        = "1700" // ArchiveLimitSeconds as the script needs it, in a constant
)

// ArchiveScript is the task's command, run as /bin/sh -c in the backup image
// (BusyBox ash). It is POSIX sh, and uses $VAR and $(...) only: Nomad
// interpolates ${...} in task config before the shell sees it.
//
// It streams the volume as a gzipped tar into the destination at
// BACKUP_PATH/TS/<VOLUME>.tar.gz, nothing staged on disk, then asks rclone the
// object's size. Every call to tar and rclone runs under BusyBox timeout. A tar
// piped into rclone is wrapped on both sides and both exit codes are checked (a
// pipeline's status is its last command's, and an archive cut short would
// otherwise upload as good). On any failure the partial object is deleted, as
// well as it can be, and the last line says why. It exits 0 after the "ok"
// line and 1 after the "failed" one.
//
// ARCHIVE_ROOT (default /data), for tests, is where the volume is mounted.
const ArchiveScript = `set -u
work=$(mktemp -d)
root=$(printenv ARCHIVE_ROOT || true)
[ -n "$root" ] || root=/data

fail() {
  msg=$(printf '%s' "$1" | tr '\n' ' ' | tr -d '\r' | cut -c1-200)
  echo "` + ArchiveMarker + ` failed: $msg"
  rm -rf "$work"
  exit 1
}

# step_failed <what> <stderr file> <exit status> says why a step failed, from
# its last line of stderr, or that it ran out of time.
step_failed() {
  line=$(tail -n 1 "$2" 2>/dev/null | tr -d '\r')
  case "$3" in
    124|143) msg="$1 timed out" ;;
    *) if [ -n "$line" ]; then msg="$1: $line"; else msg="$1 failed (exit status $3)"; fi ;;
  esac
  discard
  fail "$msg"
}

# discard removes what the run uploaded, as well as it can.
discard() {
  timeout 60 rclone deletefile "dest:$object" > /dev/null 2>&1
  return 0
}

for name in BACKUP_PATH TS VOLUME; do
  [ -n "$(printenv "$name" || true)" ] || fail "the job has no $name"
done
case "$TS" in *[!0-9]*) fail "the job's timestamp is not a number" ;; esac
[ -d "$root" ] || fail "the volume is not mounted"
object="$BACKUP_PATH/$TS/$VOLUME.tar.gz"
limit=` + archiveLimit + `

{
  timeout "$limit" tar -czf - -C "$root" . 2> "$work/tar.err"
  echo $? > "$work/tar.status"
} | timeout "$limit" rclone rcat "dest:$object" 2> "$work/rclone.err"
uploaded=$?
archived=$(cat "$work/tar.status" 2>/dev/null)
# rclone's word comes first: when it dies, tar fails with a broken pipe.
[ "$uploaded" -eq 0 ] || step_failed "the upload" "$work/rclone.err" "$uploaded"
[ -n "$archived" ] || archived=1
[ "$archived" = 0 ] || step_failed "the archive" "$work/tar.err" "$archived"

out=$(timeout 120 rclone size --json "dest:$object" 2> "$work/rclone.err")
status=$?
[ "$status" -eq 0 ] || step_failed "reading the size of the archive" "$work/rclone.err" "$status"
bytes=$(printf '%s' "$out" | sed -n 's/.*"bytes": *\([0-9][0-9]*\).*/\1/p')
[ -n "$bytes" ] || step_failed "reading the size of the archive" /dev/null 1

rm -rf "$work"
echo "` + ArchiveMarker + ` ok object=$object bytes=$bytes"
`

// VolumeArchiveJob is the one-off job that archives the host volume to the
// backup destination, at backupPath/ts/<volume>.tar.gz (backupPath is a path
// in the bucket, ts the run's unix seconds; both are task env, since neither
// is a secret). The destination's keys are in the job's own variable, which
// the job doesn't hold. appID is the app the volume's name reads as, "" when
// it doesn't read as one.
//
// The group asks for the volume with its own capability, single-node-single-writer
// on a file system: an orphan has no writer, and the mkdir plugin declares no
// reader-only mode. The task mounts it read-only.
func VolumeArchiveJob(volume, appID, backupPath, ts string, s Settings) *api.Job {
	id := ArchiveJobID(volume)
	j := batchJob(id, appID, "")
	delete(j.Meta, "caravana.service")
	if appID == "" {
		delete(j.Meta, "caravana.app")
	}
	j.Meta["caravana.volume"] = volume
	g := batchGroup("archive", &api.Task{
		Name:   ArchiveTaskName,
		Driver: "docker",
		Config: map[string]any{"image": s.backupImage(), "entrypoint": []string{"/bin/sh", "-c"}, "args": []string{ArchiveScript}},
		Env:    map[string]string{"VOLUME": volume, "BACKUP_PATH": backupPath, "TS": ts},
		VolumeMounts: []*api.VolumeMount{{
			Volume:      new(archiveVolumeName),
			Destination: new(archiveMount),
			ReadOnly:    new(true),
		}},
		Templates: []*api.Template{variableTemplate(id)},
		Resources: &api.Resources{CPU: new(100), MemoryMB: new(128), MemoryMaxMB: new(512)},
	})
	g.Volumes = map[string]*api.VolumeRequest{archiveVolumeName: {
		Name:           archiveVolumeName,
		Type:           "host",
		Source:         volume,
		AccessMode:     "single-node-single-writer",
		AttachmentMode: "file-system",
	}}
	j.TaskGroups = []*api.TaskGroup{g}
	return j
}

// ArchiveOutcome is what the archive task's output says.
type ArchiveOutcome struct {
	// Found is false when no marker line was printed (the task never ran, or was killed).
	Found bool
	// OK says the archive is in the destination.
	OK bool
	// Object is its path in the destination and Bytes the size rclone measured, when OK.
	Object string
	Bytes  int64
	// Message is why it failed, in the script's words, when Found and not OK.
	Message string
}

// ParseArchiveOutput reads the lines the task printed. The last marker line
// decides; an "ok" line with no readable object or size is not trusted.
func ParseArchiveOutput(lines []string) ArchiveOutcome {
	for i := len(lines) - 1; i >= 0; i-- {
		rest, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), ArchiveMarker)
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		if msg, failed := strings.CutPrefix(rest, "failed:"); failed {
			return ArchiveOutcome{Found: true, Message: strings.TrimSpace(msg)}
		}
		fields, isOK := strings.CutPrefix(rest, "ok ")
		if !isOK {
			return ArchiveOutcome{Found: true, Message: "the archive task said something Caravana can't read"}
		}
		out := ArchiveOutcome{Found: true}
		var haveBytes bool
		for _, f := range strings.Fields(fields) {
			k, v, _ := strings.Cut(f, "=")
			switch k {
			case "object":
				out.Object = v
			case "bytes":
				n, err := strconv.ParseInt(v, 10, 64)
				if err == nil && n >= 0 {
					out.Bytes, haveBytes = n, true
				}
			}
		}
		if out.Object == "" || !haveBytes {
			return ArchiveOutcome{Found: true, Message: "the archive task's answer has no object or size"}
		}
		out.OK = true
		return out
	}
	return ArchiveOutcome{}
}
