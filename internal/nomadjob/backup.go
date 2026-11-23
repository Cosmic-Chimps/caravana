package nomadjob

import (
	"fmt"
	"strconv"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// Backups (feature 010, research R1–R6). A backed-up service gets a Nomad
// periodic batch job beside the app's own: Nomad schedules periodic work only
// as a job of that type. Its first task dumps the database with the service's
// own image; its second uploads the dump with rclone and deletes the oldest
// ones. A restore is a one-off batch job that downloads a dump and loads it.
// Both read their credentials from their own Nomad Variable,
// nomad/jobs/<job>, which only their tasks (and Caravana) can read.
//
// Postgres, MySQL (and MariaDB) and MongoDB share this shape (feature 046): they
// differ in the dump and restore commands, the object's extension and whether
// the upload task compresses, which dumps.go holds as one recipe per engine.
//
// Like Translate, these functions are pure.

// BackupJobID is the periodic backup job of a service. App IDs and service
// names never contain "_", so it can't collide with an app's job or another
// service's, and every job of an app other than its own starts with
// AppJobPrefix.
func BackupJobID(appID, service string) string { return appID + "_" + service + "_backup" }

// RestoreJobID is the restore job of a service.
func RestoreJobID(appID, service string) string { return appID + "_" + service + "_restore" }

// AppJobPrefix starts every job of an app other than the app's own.
func AppJobPrefix(appID string) string { return appID + "_" }

// BackupVariablePath is where a backup or restore job reads its credentials.
func BackupVariablePath(jobID string) string { return "nomad/jobs/" + jobID }

// Output markers. The upload task prints them, and Caravana reads them from
// its output to record the run (deploy.BackupRuns).
const (
	// BackupDoneMarker starts the line "caravana-backup key=<object> bytes=<n>
	// dest=<name>" (dest is empty when the job's variable doesn't name the
	// destination).
	BackupDoneMarker = "caravana-backup"
	// BackupExpiredMarker starts each line "caravana-expired <file>".
	BackupExpiredMarker = "caravana-expired"
)

// dumpPath is where the dump waits between the two tasks: alloc/data is
// shared by every task of the group.
const dumpPath = "/alloc/data/backup.dump"

// uploadScript uploads the dump under the run's time, then deletes the
// oldest dumps beyond BACKUP_KEEP. The time is the end of the run's job ID
// (<job>/periodic-<unix seconds>), which a "back up now" shares with the
// schedule. It uses $VAR and $(...) only: Nomad interpolates ${...} in task
// config before the shell sees it.
//
// BACKUP_EXT is the object's extension (".dump", ".sql.gz", ".archive.gz"): the
// object is <ts><ext>, and retention looks only at the files that end in it.
// With BACKUP_GZIP=1 the dump is compressed first (a MySQL dump is plain SQL);
// a dump that compresses itself leaves it 0. The compressed copy is made in a
// file, not a pipe, because not every image's sh has pipefail and a failed gzip
// must fail the run.
//
// BACKUP_PINNED, an item of the job's variable, lists the timestamps of the
// pinned dumps (digits separated by spaces, or unset for none). A pinned dump
// is never deleted and doesn't count against BACKUP_KEEP: BACKUP_KEEP is how
// many unpinned dumps stay. A list with anything else in it can't be trusted,
// so nothing is deleted and the run fails saying so (the dump is already
// uploaded; a retention failure fails this engine's run today as well). This
// run's own dump is never deleted.
const uploadScript = `set -eu
ts=$(echo "$NOMAD_JOB_ID" | sed 's/.*periodic-//')
key="$BACKUP_PATH/$ts$BACKUP_EXT"
src=` + dumpPath + `
if [ "$BACKUP_GZIP" = 1 ]; then
  gzip -c "$src" > "$src.gz"
  src="$src.gz"
fi
rclone copyto "$src" "dest:$key"
echo "` + BackupDoneMarker + ` key=$key bytes=$(stat -c %s "$src") dest=$(printenv BACKUP_DESTINATION || true)"
pinned=$(printenv BACKUP_PINNED || true)
if printf '%s' "$pinned" | grep -q '[^0-9 ]'; then
  echo "old backups were not removed: the list of pinned backups is not valid" >&2
  exit 1
fi
files=$(rclone lsf --files-only "dest:$BACKUP_PATH/" | sort)
candidates=
for f in $files; do
  case "$f" in
    *"$BACKUP_EXT") ;;
    *) continue ;;
  esac
  is_pinned=
  for p in $pinned; do
    [ "$f" != "$p$BACKUP_EXT" ] || is_pinned=1
  done
  [ -n "$is_pinned" ] || candidates="$candidates $f"
done
n=$(echo $candidates | wc -w)
if [ "$n" -gt "$BACKUP_KEEP" ]; then
  echo $candidates | tr ' ' '\n' | head -n $((n - BACKUP_KEEP)) | while read -r f; do
    [ "$f" != "$ts$BACKUP_EXT" ] || continue
    rclone deletefile "dest:$BACKUP_PATH/$f"
    echo "` + BackupExpiredMarker + ` $f"
  done
fi
`

// withPullAuth adds the docker auth block to config when image comes from a
// registry in private (see pullAuth). The backup and restore variables already
// export every item as env, so the credential items reach the task through the
// template the task has.
func withPullAuth(config map[string]any, image string, private map[string]bool) map[string]any {
	if auth, ok := pullAuth(image, private); ok {
		config["auth"] = auth
	}
	return config
}

// neverSpec is the cron line of a Postgres backup job whose policy has schedule
// "never" (feature 039): the 30th of February, a day no month has, so Nomad
// never launches a run by itself. The job stays periodic and enabled on
// purpose: Back up now is Jobs().PeriodicForce, which Nomad refuses for a
// disabled periodic job. Lab, 2026-11-15 (Nomad 2.0.7): Nomad registers this
// spec, launches no child on its own, and PeriodicForce starts one.
const neverSpec = "0 0 30 2 *"

// BackupJob is the periodic job that backs up svc of appID, dumping with
// image (the service's pinned image) the way its engine does (dumps.go).
// private is as for Translate: when image comes from one of those registries,
// the dump task pulls it with the credentials in the job's variable.
func BackupJob(appID string, svc app.Service, image string, s Settings, private map[string]bool) *api.Job {
	id := BackupJobID(appID, svc.Name)
	b := *svc.Backup
	r := recipeFor(b.Engine)
	j := batchJob(id, appID, svc.Name)
	spec := b.ScheduleOrDefault()
	if !b.Automatic() {
		spec = neverSpec
	}
	j.Periodic = &api.PeriodicConfig{
		Enabled:         new(true),
		SpecType:        new("cron"),
		Specs:           []string{spec},
		ProhibitOverlap: new(true),
		TimeZone:        new("UTC"),
	}
	j.TaskGroups = []*api.TaskGroup{batchGroup("backup",
		&api.Task{
			Name:      "dump",
			Driver:    "docker",
			Lifecycle: &api.TaskLifecycle{Hook: "prestart"},
			Config:    withPullAuth(r.dumpConfig(image), image, private),
			Templates: []*api.Template{variableTemplate(id), databaseTemplate(appID, svc.Name, r)},
			Resources: smallResources(),
		},
		&api.Task{
			Name:   "upload",
			Driver: "docker",
			Config: map[string]any{"image": s.backupImage(), "entrypoint": []string{"/bin/sh", "-c"}, "args": []string{uploadScript}},
			Env: map[string]string{"BACKUP_KEEP": strconv.Itoa(b.KeepOrDefault()), "BACKUP_EXT": r.ext,
				"BACKUP_GZIP": r.gzipFlag()},
			Templates: []*api.Template{variableTemplate(id)},
			Resources: smallResources(),
		},
	)}
	return j
}

// RestoreJob is the one-off job that loads the dump at object (a path in
// the destination, as the backup run printed it) into svc of appID, with
// image (the service's current pinned image). private is as for BackupJob.
func RestoreJob(appID string, svc app.Service, image, object string, s Settings, private map[string]bool) *api.Job {
	id := RestoreJobID(appID, svc.Name)
	r := recipeFor("") // Postgres: a service without a policy has nothing else to go by
	if svc.Backup != nil {
		r = recipeFor(svc.Backup.Engine)
	}
	j := batchJob(id, appID, svc.Name)
	j.Meta["backup_key"] = object
	j.TaskGroups = []*api.TaskGroup{batchGroup("restore",
		&api.Task{
			Name:      "download",
			Driver:    "docker",
			Lifecycle: &api.TaskLifecycle{Hook: "prestart"},
			Config:    r.downloadConfig(s.backupImage()),
			Templates: []*api.Template{variableTemplate(id)},
			Resources: smallResources(),
		},
		&api.Task{
			Name:      "restore",
			Driver:    "docker",
			Config:    withPullAuth(r.restoreConfig(image), image, private),
			Templates: []*api.Template{variableTemplate(id), databaseTemplate(appID, svc.Name, r)},
			Resources: smallResources(),
		},
	)}
	return j
}

// batchJob is what the backup and restore jobs share.
func batchJob(id, appID, service string) *api.Job {
	return &api.Job{
		ID:          new(id),
		Name:        new(id),
		Type:        new("batch"),
		Datacenters: []string{"*"},
		Meta:        map[string]string{"caravana.app": appID, "caravana.service": service},
	}
}

// batchGroup runs tasks once: no restart, no reschedule. With Nomad's
// defaults a failed dump is tried about 8 times over minutes, and the run
// counts as running all that time, which blocks the next scheduled one.
func batchGroup(name string, tasks ...*api.Task) *api.TaskGroup {
	return &api.TaskGroup{
		Name:             new(name),
		Count:            new(1),
		Networks:         []*api.NetworkResource{{Mode: "bridge"}},
		RestartPolicy:    &api.RestartPolicy{Attempts: new(0), Mode: new("fail")},
		ReschedulePolicy: &api.ReschedulePolicy{Attempts: new(0), Unlimited: new(false)},
		Tasks:            tasks,
	}
}

// variableTemplate renders the job's own variable as env, each value
// JSON-quoted (see secretsTemplate).
func variableTemplate(jobID string) *api.Template {
	return &api.Template{
		EmbeddedTmpl: new(fmt.Sprintf(`{{ with nomadVar %q }}{{ range .Tuples }}{{ .K }}={{ printf "%%s" .V | toJSON }}`+"\n"+`{{ end }}{{ end }}`,
			BackupVariablePath(jobID))),
		DestPath:   new("secrets/backup.env"),
		Envvars:    new(true),
		ChangeMode: new("noop"),
	}
}

// databaseTemplate renders the engine's host and port variables (r.host and
// r.port: PGHOST and PGPORT, DB_HOST and DB_PORT) from the service's main
// registration (the node's address and the host port, which every service
// with ports has), skipping canaries.
func databaseTemplate(appID, service string, r dumpRecipe) *api.Template {
	return &api.Template{
		EmbeddedTmpl: new(fmt.Sprintf(`{{ range nomadService %q }}{{ if not (.Tags | contains %q) }}%s={{ .Address }}`+"\n"+`%s={{ .Port }}`+"\n"+`{{ end }}{{ end }}`,
			appID+"-"+service, canaryTag, r.host, r.port)),
		DestPath:   new("local/database.env"),
		Envvars:    new(true),
		ChangeMode: new("noop"),
	}
}

// smallResources is what a dump or an upload reserves.
func smallResources() *api.Resources {
	return &api.Resources{CPU: new(100), MemoryMB: new(128)}
}
