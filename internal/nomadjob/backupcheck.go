package nomadjob

import (
	"strconv"
	"strings"

	"github.com/hashicorp/nomad/api"
)

// The check job (feature 052, plan D9). It asks the servers' network what
// Caravana can't ask from where it runs: whether a backup destination can be
// reached with its key (mode "test"), and which of a backup's places still holds
// its object (mode "locate"). It is a one-off batch job running rclone, with the
// keys of the destinations in its own Nomad Variable only, as the copy job has.
// It belongs to no app: its ID starts with "_" so no app's ID or prefix can match
// it, as for 044's scan jobs.
//
// Like Translate, these functions are pure.

// BackupCheckJobID is the check job with the given ID (12 lowercase hex characters).
func BackupCheckJobID(id string) string { return "_backup-check-" + id }

// BackupCheckMarker starts the lines the check task prints:
//
//	caravana-check: ok
//	caravana-check: failed <list|write|delete>
//	caravana-check: found <place>
//	caravana-check: missing <place>
//	caravana-check: unreachable <place>
const BackupCheckMarker = "caravana-check:"

// BackupCheckTaskName is the job's one task.
const BackupCheckTaskName = "check"

// The modes of a check job.
const (
	CheckModeTest   = "test"
	CheckModeLocate = "locate"
)

// MaxCheckPlaces is how many places a check job looks in: a backup has its own
// destination and at most two copies.
const MaxCheckPlaces = 3

// BackupCheckScript is the check task's command, run as /bin/sh -c in the backup
// image. It is POSIX sh, and uses $VAR and $(...) only (Nomad interpolates ${...}).
//
// The job's variable gives it, for each place i from 1 to 3, the remote r<i>
// (RCLONE_CONFIG_R<i>_*) and R<i>_PATH. The task's env gives it MODE and
// CHECK_ID, which are no secrets.
//
// In mode test, with one place, it lists the destination's folder (a folder that
// doesn't exist yet, rclone's exit status 3, still shows the bucket answers),
// writes a small object under it and deletes the object, and prints "ok", or
// "failed list", "failed write" or "failed delete" for the step that failed, with
// rclone's own words on standard error, and exits 1.
//
// In mode locate, R<i>_PATH is an object. For each place in order it asks rclone
// the size of the path: some objects there means "found i" and the script stops;
// none, or exit status 3 (not found), means "missing i"; any other failure means
// "unreachable i". It exits 0 after naming what it found of each place it looked
// in.
const BackupCheckScript = `set -eu
mode=$(printenv MODE || true)
id=$(printenv CHECK_ID || true)
[ -n "$id" ] || { echo "the check job has no CHECK_ID" >&2; exit 1; }
err=/tmp/caravana-check.$$.err
case "$mode" in
  ` + CheckModeTest + `)
    path=$(printenv R1_PATH || true)
    [ -n "$path" ] || { echo "the check job has no R1_PATH" >&2; exit 1; }
    probe="$path/.caravana-check-$id"
    status=0
    rclone lsf --max-depth 1 "r1:$path/" > /dev/null 2> "$err" || status=$?
    if [ "$status" -ne 0 ] && [ "$status" -ne 3 ]; then
      echo "` + BackupCheckMarker + ` failed list"
      cat "$err" >&2
      exit 1
    fi
    if ! printf 'caravana check %s\n' "$id" | rclone rcat "r1:$probe" 2> "$err"; then
      echo "` + BackupCheckMarker + ` failed write"
      cat "$err" >&2
      exit 1
    fi
    if ! rclone deletefile "r1:$probe" 2> "$err"; then
      echo "` + BackupCheckMarker + ` failed delete"
      cat "$err" >&2
      exit 1
    fi
    echo "` + BackupCheckMarker + ` ok"
    ;;
  ` + CheckModeLocate + `)
    for i in 1 2 3; do
      case $i in
        1) path=$(printenv R1_PATH || true) ;;
        2) path=$(printenv R2_PATH || true) ;;
        3) path=$(printenv R3_PATH || true) ;;
      esac
      [ -n "$path" ] || break
      status=0
      size=$(rclone size --json "r$i:$path" 2> /dev/null) || status=$?
      if [ "$status" -eq 0 ]; then
        count=$(printf '%s' "$size" | sed -n 's/.*"count": *\([0-9][0-9]*\).*/\1/p')
        if [ -n "$count" ] && [ "$count" -gt 0 ]; then
          echo "` + BackupCheckMarker + ` found $i"
          exit 0
        fi
        echo "` + BackupCheckMarker + ` missing $i"
      elif [ "$status" -eq 3 ]; then
        echo "` + BackupCheckMarker + ` missing $i"
      else
        echo "` + BackupCheckMarker + ` unreachable $i"
      fi
    done
    ;;
  *) echo "the check job's mode is neither test nor locate" >&2; exit 1 ;;
esac
`

// BackupCheckJob is the one-off job that checks backup destinations, with id
// the one BackupCheckJobID takes and mode CheckModeTest or CheckModeLocate. The
// places' paths and the destinations' keys are in the job's own variable, which
// the job doesn't hold.
func BackupCheckJob(id, mode string, s Settings) *api.Job {
	jobID := BackupCheckJobID(id)
	j := batchJob(jobID, "", "")
	delete(j.Meta, "caravana.app")
	delete(j.Meta, "caravana.service")
	j.TaskGroups = []*api.TaskGroup{batchGroup("check", &api.Task{
		Name:      BackupCheckTaskName,
		Driver:    "docker",
		Config:    map[string]any{"image": s.backupImage(), "entrypoint": []string{"/bin/sh", "-c"}, "args": []string{BackupCheckScript}},
		Env:       map[string]string{"MODE": mode, "CHECK_ID": id},
		Templates: []*api.Template{variableTemplate(jobID)},
		Resources: smallResources(),
	})}
	return j
}

// BackupCheckOutput is what the check task's output says.
type BackupCheckOutput struct {
	// OK is a test that passed.
	OK bool
	// Failed is the step a test failed at ("list", "write" or "delete"), "" if none.
	Failed string
	// Found is the place (1 to MaxCheckPlaces) that holds the object, 0 if none
	// was found. Missing and Unreachable are the places that don't, and the ones
	// that couldn't be asked, in order.
	Found       int
	Missing     []int
	Unreachable []int
}

// ParseBackupCheckOutput reads the lines the check task printed.
func ParseBackupCheckOutput(lines []string) BackupCheckOutput {
	var out BackupCheckOutput
	for _, line := range lines {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), BackupCheckMarker+" ")
		if !ok {
			continue
		}
		word, arg, _ := strings.Cut(rest, " ")
		switch word {
		case "ok":
			out.OK = true
		case "failed":
			out.Failed = strings.TrimSpace(arg)
		case "found", "missing", "unreachable":
			n, err := strconv.Atoi(strings.TrimSpace(arg))
			if err != nil || n < 1 || n > MaxCheckPlaces {
				continue
			}
			switch word {
			case "found":
				out.Found = n
			case "missing":
				out.Missing = append(out.Missing, n)
			default:
				out.Unreachable = append(out.Unreachable, n)
			}
		}
	}
	return out
}
