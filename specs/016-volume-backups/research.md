# Research: Volumes panel and volume backups

The spec left one question to the plan: how the service is stopped and started, and how the run
reaches the volume. It was settled in the Lima lab (Nomad 2.0.7) on 2026-10-15, with a scratch
app built like Caravana's: a `mkdir` dynamic host volume with `single-node-single-writer`
capability, and a group with `canary = 0` (stop-then-start).

## Lab findings

| # | Experiment | Result |
| --- | --- | --- |
| L1 | A batch job asks for the volume while the app's copy holds it | Not placed: "missing compatible host volumes". The job stays queued, not failed. |
| L2 | The app's group is scaled to 0 while L1 waits | The queued job is placed at once, mounts the volume, archives it, completes (~10 s). |
| L3 | `job scale` 1 → 0 → 1 | Each scale is a **new job version and a Nomad deployment** (v0 → v1 count 0 → v2 count 1). |
| L4 | A prestart task (not a sidecar) in the app's group, mounting the same volume | Runs before the main task on every start of the copy; shares the group's claim, no conflict. |
| L5 | `alloc restart -all-tasks` on that copy | The main task gets SIGTERM and stops cleanly, the prestart task runs again to completion, then the main task starts. **Same allocation, same job version, no deployment.** ~2 s for an empty volume. |
| L6 | A template with `nomadVar` for a variable that doesn't exist | The task blocks ("Missing: nomad.var.block"), the copy never starts, and the deploy fails at its deadline. |
| L7 | Template with `env = true`, variable changed, then L5 | The restarted task still sees the **old** value: env is captured at the first render. The rendered *file* has the new value. |
| L8 | Template file read by the script at run time, variable changed, file polled until it shows the change, then L5 | The task sees the new request and acts on it. |
| L9 | The prestart task's stdout after a restart | **Not captured**: the log file keeps the first run's output; later runs write nothing to it, though their work happened. |
| L10 | Files written to `/alloc/data` by the prestart task | Persist for the allocation's life; readable with `nomad alloc fs`. |
| L11 | The pinned backup image (`rclone/rclone@sha256:3103…`) | BusyBox 1.37 with `sh` and `tar` (`-z`, `--exclude`), plus rclone. |

## R1. Where a volume backup runs: inside the app's own copy

**Decision**: every service with volumes gets a prestart task, `caravana-volumes`, in its task
group. It uses the pinned backup image and mounts each of the service's volumes at
`/caravana-volumes/<volume>`. On every start of the copy it measures the volumes' sizes (R9). When
Caravana has left it a request, it also backs them up or restores them. Caravana triggers a request
by restarting the copy's tasks (`Allocations().Restart`, all tasks): the main task stops cleanly,
`caravana-volumes` does the work while nothing else uses the volume, and the main task starts
again (L4, L5).

**Rationale**: it's the only way found that stops the service, reaches its volume and starts it
again without changing the job:
- the job version doesn't change, and there's no Nomad deployment, so the app's history and
  version stay as deploys left them;
- the claim on the volume doesn't move between allocations, so there's no race with a
  replacement copy;
- auto-revert can't strand the app.

The stop is the image's own clean shutdown (SIGTERM, then the task's kill timeout), which is what
"stop-and-archive" was chosen for.

**Alternatives considered**:
- **Scale to 0, a batch job, scale to 1** (L1–L3). It works mechanically, but each backup adds
  two job versions and two Nomad deployments. Worse, if the copy is unhealthy after scaling back
  up, auto-revert rolls back to the last stable version, which is the `count = 0` one: the app
  would stay stopped. The header's version would also climb every night.
- **Stop the allocation (`alloc stop`)**. Nomad places a replacement at once, which races the
  backup job for the single-writer claim.
- **A multi-writer volume and a separate job**. The archive needs the service stopped anyway,
  and existing volumes were created single-writer.
- **Freeze the container (SIGSTOP) and copy**. That gives only a crash-consistent copy, and a
  clean stop was the agreed requirement.

## R2. Passing the request: a Nomad Variable, read as a file

**Decision**: the task's variable is `nomad/jobs/<app>/<service>/caravana-volumes`. Nomad's
implicit policy lets only that task (and Caravana) read it. Caravana writes it before every
register, so it always exists (L6). It holds:
- the destination's rclone items and the backup path, as in feature 010, when the service has a
  volume backup;
- `KEEP`;
- the request: `REQUEST` (an ID, empty for none), `ACTION` (`backup` or `restore`), `OBJECT` (the
  backup to restore) and `NOT_AFTER` (unix seconds).

Its template renders to `local/request.env` with `change_mode = "noop"` and **without**
`env = true`, and the script sources the file when it runs (L7, L8). Before restarting, Caravana
reads the rendered file through the allocation's file system and waits, up to 30 s, until it
shows the request ID.

The script ignores a request when:
- `NOT_AFTER` has passed, so a later ordinary start (a deploy, a node reboot) never repeats it;
- or its result file already exists.

Caravana clears the request after each run, and at startup for any run it finds unfinished.

**Rationale**: the variable is the one channel Nomad gives a task without a token, and it's
already how feature 008 hands secrets to tasks. The two guards matter most for restore: replaying
an old restore after a later change would lose data.

## R3. The run's outcome: a result file, not the task's output

**Decision**: the script writes `/alloc/data/caravana/<request>.result`, `key=value` lines:
- `status` (`succeeded` or `failed`) and `message`;
- per volume, `volume.<name>.bytes` (archive size) and `volume.<name>.size` (data size);
- `object` (the backup's path in the destination) and `expired` (backups deleted by retention).

Caravana reads the file with `AllocFS().Cat`. That needs the `read-fs` capability; restarting
needs `alloc-lifecycle`. Both are added to `deploy/install/caravana-policy.hcl`, so an existing
host reruns the installer.

**Rationale**: output after a restart isn't captured (L9). Files in `/alloc/data` are (L10).

## R4. The script

**Decision**: one shell script, a `const` in `nomadjob`. It never exits non-zero, because a
failed prestart task would keep the main task from starting (FR-002), and failures are reported
in the result file instead. It does this:

1. Write the sizes (R9).
2. Check the request against the guards (R2).
3. **Backup**: for each volume, run
   `tar -czf - -C /caravana-volumes/<v> --exclude ./.caravana-restore . | rclone rcat dest:$BACKUP_PATH/<ts>/<v>.tar.gz`,
   then record the archive's size. With every volume uploaded, delete the oldest `<ts>/`
   directories beyond `KEEP`, oldest first. A failure purges `<ts>/`, so no partial backup is
   left (FR-015).
4. **Restore**: for each volume, download `<v>.tar.gz` into `/caravana-volumes/<v>/.caravana-restore/`,
   on the volume's own disk, and check it with `tar -tzf`. Only when every archive checks out,
   move each volume's current entries into `.caravana-restore/old/` and extract. If an extract
   fails, move every volume's old entries back. Delete `.caravana-restore` at the end. A failed
   restore therefore leaves the volumes as they were (FR-008).
5. Wrap the work in BusyBox `timeout` with the run's limit (30 min, `CARAVANA_VOLUME_BACKUP_TIMEOUT`).

**Rationale**: streaming backups need no scratch space on the alloc's small ephemeral disk.
Restores stage on the volume's own disk, where space for the data already exists, and every step
before the swap is reversible.

**To verify while implementing**: that the BusyBox in the pinned image has `timeout`. If it
doesn't, the limit is enforced by Caravana restarting the copy again with no request.

## R5. The clock: Caravana schedules volume backups

**Decision**: a scheduler goroutine in `service` runs volume backups on their cron schedule (UTC),
using `github.com/hashicorp/cronexpr`. That module is already in `go.sum` and becomes a direct
dependency; the commit says so (constitution I). Every 30 s it looks for services whose latest
slot has passed with no run started since:
- a slot missed while Caravana was down is caught up if it is under an hour old, and skipped
  otherwise;
- skips are logged.

Postgres backups keep their Nomad periodic jobs (feature 010).

**Rationale**: a periodic job could only restart the app's copy with a Nomad ACL token bound to
it. Caravana's scoped token can't create ACL policies, and shouldn't be able to. Caravana already
runs under systemd beside Nomad, so a missed slot is the same risk as for any Caravana feature.

**Alternatives considered**: a periodic job that calls Caravana's API. Caravana listens on
loopback, which a bridge-mode container can't reach, and it would need an API token stored in
the job.

## R6. Ordering with deploys: one lock

**Decision**: a run holds the app's deploy lock (`appState.mu`) from writing the request until
the main task is running again, or the limit passes. So:
- a deploy that arrives during a run waits, then goes ahead (FR-016);
- a run never starts in the middle of a deploy;
- a second backup or restore of the service is refused, as today (`backupBusy`, FR-009).

**Rationale**: the lock already orders deploys and delete. A backup changes what runs for a few
seconds, so it has to take part in that ordering.

## R7. Records and resuming

**Decision**: migration `0008_volume_backups.sql`:

- `backups` and `restores` gain `engine` (`postgres` | `volume`, default `postgres`) and
  `stopped_ms`.
- `backups` gains `volumes`: JSON per volume, with archive bytes and data size.
- For volume runs, `job_id` holds `<app>_<service>_volumes/<ts>` and `object` the `<ts>/`
  prefix.
- `volume_sizes(app_id, volume, bytes, measured_at)`: the last measured size of each volume.

A run is recorded running before the restart. At startup, each volume run still running is read
from its copy's result file. When the file is missing and `NOT_AFTER` has passed, the run is
marked failed ("Caravana restarted during the run"). Either way its request is cleared.

**Stop duration**: from the main task's `Terminated` event to its next `Started` event, read from
the copy's task events. If the copy didn't come back within a minute, the run records how long
it waited and the needs-you list picks the copy up (spec edge case).

## R8. Validation

**Decision**: `app.Backup.Engine` accepts `volume`. A service with a `volume` backup:

- **must mount at least one volume**, else the backup is refused with "the service has no volume";
- **must not share a volume with another service of the app**, else it is refused naming the volume
  (FR-010);
- **needs a destination**, as feature 010 does;
- **needs no ports and no password** (those are Postgres rules).

Such a service's group asks for its volumes writable, even when the service mounts one read-only.
The main task's mount keeps `read_only`, so the app sees no change, and `caravana-volumes` can
still restore into the volume (spec edge case: read-only mounts are part of backup and restore).

## R9. Sizes for the panel

**Decision**: `caravana-volumes` runs `du -sk` on each volume every time the copy starts, and at
every backup, and writes `/alloc/data/caravana/sizes`. When the panel is read, Caravana reads
that file from each running copy (one `AllocFS` call per copy, 2 s timeout, four at a time) and
stores what it finds in `volume_sizes`. The panel then shows the stored size with its time:
"312 MB, measured 2 h ago".

A volume no service mounts keeps its last measured size. A volume that has never been measured
shows "not measured yet".

**Rationale**:
- Nomad doesn't measure volume usage.
- Caravana runs as the `caravana` user under `ProtectSystem=strict` and can't read
  `/opt/nomad/data`.
- `alloc exec` into app containers would need a capability that also opens every app's
  shell, and the image may lack `du`.

Measuring at start costs a `du`: seconds even for gigabytes.

**Consequence**: every service with volumes gains the `caravana-volumes` task, not only backed-up
ones, so turning on a backup later doesn't change the job's shape. Existing volume apps get it at
their next deploy. Until then their panel says "not measured yet".

## R10. Volumes panel and API

**Decision**: `GET /v1/apps/{id}/volumes` (`listVolumes`) returns, per volume:
- its name;
- its mounts (service, path, read-only);
- its host path (from Nomad's host volume, `HostPath`);
- its last size and when it was measured;
- whether it is backed up, and its last backup (ID, time, status).

It also lists volumes Nomad still has for the app but no service mounts (`mounted: false`).

The console draws a Volumes section at the top of the Backups tab (on every app, as today), when
the app has a volume. Backup rows gain "stopped N s". "Back up now" and "Restore" say "The service
stops while it runs", with the last stop's duration.

API 0.12.0 → 0.13.0:
- `BackupPolicy.engine` gains `volume`;
- `Backup` and `Restore` gain `engine` and `stoppedSeconds`, and `Backup` gains `volumes`;
- new code `no_running_copy` (409): back up now, or restore, when the service has no running
  copy.

## R11. Restarts that aren't crashes

**Decision**: feature 014's crash-loop rule ("restarts plus failed copies of the last hour reach
3") stops counting restarts that Caravana asked for. Their task event is `Restart Signaled` with
"User requested all tasks to restart" (L5). `deploy.Copy` gains `RequestedRestarts`, and the rule
subtracts it.

**Rationale**: three nightly backups would otherwise put a healthy app on the needs-you list.

## R12. Export, gate, lab

- **Export**: `nomadjob.HCL` already writes prestart tasks (the `names` task). The new task's
  template, mounts and resources are covered by the reflection test. The export header names the
  task's variable path.
- **Gate step `volume-backup-restore`** (`runGate`):
  1. deploy an app whose service writes a file to its volume;
  2. back it up to the `rclone serve s3` bucket that `backup-restore` uses;
  3. change the file, then restore;
  4. read the original file back;
  5. check the job version didn't move.

  It runs on the scoped policy token, which catches a missing `alloc-lifecycle` or `read-fs`.
- **Console lab step `console-volumes`**: the panel lists the volume with a size, and "Back up
  now" from the console records a run with a stop duration.
- **Reservation**: the new task asks for 100 MHz and 64 MB. That it doesn't add to a running
  copy's reservation (it ends before the main task starts) is checked in the lab with
  `nomad alloc status`; the plan's memory budget assumes it doesn't.
