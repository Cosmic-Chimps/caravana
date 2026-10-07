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
	// BackupDoneMarker starts the line "caravana-backup key=<object> bytes=<n>".
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
const uploadScript = `set -eu
ts=$(echo "$NOMAD_JOB_ID" | sed 's/.*periodic-//')
key="$BACKUP_PATH/$ts.dump"
rclone copyto ` + dumpPath + ` "dest:$key"
echo "` + BackupDoneMarker + ` key=$key bytes=$(stat -c %s ` + dumpPath + `)"
files=$(rclone lsf --files-only "dest:$BACKUP_PATH/" | grep '\.dump$' | sort)
n=$(echo "$files" | grep -c . || true)
if [ "$n" -gt "$BACKUP_KEEP" ]; then
  echo "$files" | head -n $((n - BACKUP_KEEP)) | while read -r f; do
    rclone deletefile "dest:$BACKUP_PATH/$f"
    echo "` + BackupExpiredMarker + ` $f"
  done
fi
`

// BackupJob is the periodic job that backs up svc of appID, dumping with
// image (the service's pinned image).
func BackupJob(appID string, svc app.Service, image string, s Settings) *api.Job {
	id := BackupJobID(appID, svc.Name)
	b := *svc.Backup
	j := batchJob(id, appID, svc.Name)
	j.Periodic = &api.PeriodicConfig{
		Enabled:         new(true),
		SpecType:        new("cron"),
		Specs:           []string{b.ScheduleOrDefault()},
		ProhibitOverlap: new(true),
		TimeZone:        new("UTC"),
	}
	j.TaskGroups = []*api.TaskGroup{batchGroup("backup",
		&api.Task{
			Name:      "dump",
			Driver:    "docker",
			Lifecycle: &api.TaskLifecycle{Hook: "prestart"},
			Config:    map[string]any{"image": image, "command": "pg_dump", "args": []string{"-Fc", "-f", dumpPath}},
			Templates: []*api.Template{variableTemplate(id), databaseTemplate(appID, svc.Name)},
			Resources: smallResources(),
		},
		&api.Task{
			Name:      "upload",
			Driver:    "docker",
			Config:    map[string]any{"image": s.backupImage(), "entrypoint": []string{"/bin/sh", "-c"}, "args": []string{uploadScript}},
			Env:       map[string]string{"BACKUP_KEEP": strconv.Itoa(b.KeepOrDefault())},
			Templates: []*api.Template{variableTemplate(id)},
			Resources: smallResources(),
		},
	)}
	return j
}

// RestoreJob is the one-off job that loads the dump at object (a path in
// the destination, as the backup run printed it) into svc of appID, with
// image (the service's current pinned image).
func RestoreJob(appID string, svc app.Service, image, object string, s Settings) *api.Job {
	id := RestoreJobID(appID, svc.Name)
	j := batchJob(id, appID, svc.Name)
	j.Meta["backup_key"] = object
	j.TaskGroups = []*api.TaskGroup{batchGroup("restore",
		&api.Task{
			Name:      "download",
			Driver:    "docker",
			Lifecycle: &api.TaskLifecycle{Hook: "prestart"},
			// Here Nomad's interpolation is wanted: it puts the job's meta in.
			Config:    map[string]any{"image": s.backupImage(), "args": []string{"copyto", "dest:${NOMAD_META_backup_key}", dumpPath}},
			Templates: []*api.Template{variableTemplate(id)},
			Resources: smallResources(),
		},
		&api.Task{
			Name:   "restore",
			Driver: "docker",
			Config: map[string]any{"image": image, "entrypoint": []string{"/bin/sh", "-c"},
				"args": []string{`pg_restore --clean --if-exists --no-owner -d "$PGDATABASE" ` + dumpPath}},
			Templates: []*api.Template{variableTemplate(id), databaseTemplate(appID, svc.Name)},
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

// databaseTemplate renders PGHOST and PGPORT from the service's main
// registration (the node's address and the host port, which every service
// with ports has), skipping canaries.
func databaseTemplate(appID, service string) *api.Template {
	return &api.Template{
		EmbeddedTmpl: new(fmt.Sprintf(`{{ range nomadService %q }}{{ if not (.Tags | contains %q) }}PGHOST={{ .Address }}`+"\n"+`PGPORT={{ .Port }}`+"\n"+`{{ end }}{{ end }}`,
			appID+"-"+service, canaryTag)),
		DestPath:   new("local/database.env"),
		Envvars:    new(true),
		ChangeMode: new("noop"),
	}
}

// smallResources is what a dump or an upload reserves.
func smallResources() *api.Resources {
	return &api.Resources{CPU: new(100), MemoryMB: new(128)}
}
