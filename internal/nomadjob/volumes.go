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
		Templates: []*api.Template{volumeTemplate(appID, svc.Name)},
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

// volumeTemplate renders the task's variable as KEY='value' lines in
// local/request.env, for the script to source. It is not env: see
// volumeRequestPath. "noop" means a changed variable never restarts anything;
// Caravana restarts the copy itself, once the file shows the new request.
//
// A value is put in single quotes, and each single quote inside it is written
// as quote, backslash, quote, quote (which closes the string, adds a literal
// quote and opens it again). The shell reads the file, and inside single
// quotes nothing is expanded, so a key with a $ or a backtick arrives as it is
// and runs nothing. (JSON quoting,
// which secretsTemplate uses for Nomad's own env-file parser, would leave
// both for the shell to expand.) The template text has no "${", which Nomad
// would interpolate.
func volumeTemplate(appID, service string) *api.Template {
	tmpl := `{{ with nomadVar "` + VolumeVariablePath(appID, service) + `" }}` +
		`{{ range .Tuples }}{{ .K }}='{{ .V | replaceAll "'" "'\\''" }}'` + "\n" + `{{ end }}{{ end }}`
	return &api.Template{
		EmbeddedTmpl: new(tmpl),
		DestPath:     new("local/request.env"),
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
// ROOT, DATA and REQUEST_FILE can be set in the environment, for tests; the
// defaults are the task's real paths.
//
// The request (REQUEST, ACTION, OBJECT, NOT_AFTER, LIMIT, KEEP, TS and the
// destination's RCLONE_CONFIG_DEST_* and BACKUP_PATH) is sourced in a
// subshell, so a damaged file ends that subshell and nothing more.
const VolumeScript = `[ -n "$ROOT" ] || ROOT=` + VolumeRoot + `
[ -n "$DATA" ] || DATA=` + VolumeDataDir + `
[ -n "$REQUEST_FILE" ] || REQUEST_FILE=` + volumeRequestPath + `

# One "<volume> <kilobytes>" line for each volume, replaced as a whole so
# Caravana never reads half a file.
measure_sizes() {
  mkdir -p "$DATA" || return 0
  : > "$DATA/.sizes.tmp" || return 0
  for dir in "$ROOT"/*; do
    [ -d "$dir" ] || continue
    kb=$(du -sk "$dir" 2>/dev/null | cut -f1)
    [ -n "$kb" ] || kb=0
    echo "$(basename "$dir") $kb" >> "$DATA/.sizes.tmp"
  done
  mv "$DATA/.sizes.tmp" "$DATA/sizes"
}

# finish <status> writes the result file from MESSAGE and the detail lines the
# run left in $DETAIL, replacing nothing half-written.
finish() {
  message=$(printf '%s' "$MESSAGE" | tr '\n' ' ')
  {
    echo "status=$1"
    echo "message=$message"
    [ ! -f "$DETAIL" ] || cat "$DETAIL"
  } > "$DATA/.$REQUEST.tmp" && mv "$DATA/.$REQUEST.tmp" "$DATA/$REQUEST.result"
  rm -f "$DETAIL"
}

# --- The runs. Each returns 0 for success, or 1 with MESSAGE saying why. It
# --- appends "key=value" lines to $DETAIL for the result file.

do_backup() {
  MESSAGE="not implemented yet"
  return 1
}

do_restore() {
  MESSAGE="not implemented yet"
  return 1
}

# --- The request.

handle_request() {
  [ -f "$REQUEST_FILE" ] || return 0
  REQUEST= ACTION= OBJECT= NOT_AFTER=
  set -a
  . "$REQUEST_FILE"
  set +a
  # The ID names a file: letters, digits, "-" and "_" only.
  case "$REQUEST" in ''|*[!A-Za-z0-9_-]*) return 0 ;; esac
  case "$NOT_AFTER" in ''|*[!0-9]*) return 0 ;; esac
  [ "$NOT_AFTER" -ge "$(date +%s)" ] || return 0
  [ ! -e "$DATA/$REQUEST.result" ] || return 0

  mkdir -p "$DATA" || return 0
  DETAIL="$DATA/.$REQUEST.detail"
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
