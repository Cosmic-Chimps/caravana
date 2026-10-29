# Implementation Plan: Volumes panel and volume backups

**Branch**: `016-volume-backups` | **Date**: 2026-10-15 | **Spec**: [volume-backups.spec.md](./volume-backups.spec.md)

**Input**: Feature specification from `specs/016-volume-backups/volume-backups.spec.md`

**Scale note**: about 35 files in seven packages, one migration, two new Nomad capabilities
(`alloc-lifecycle`, `read-fs`), one gate step. The design came from the lab (research L1–L11):
the backup runs **inside the app's own copy** as a prestart task, triggered by restarting the
copy's tasks, so no job version or Nomad deployment is ever added by a backup. The risks:
- a restore that replays later and overwrites newer data. Two guards stop it: a request expiry,
  and a result file per request.
- a run that leaves the service stopped. The script always exits 0, and the main task always
  starts after it.

## Summary

Every service with volumes gets a `caravana-volumes` prestart task (the pinned rclone image). At
each start of the copy it measures the volumes, which feeds the new volumes panel.

When Caravana leaves it a request in its own Nomad Variable and restarts the copy's tasks:
1. the service stops cleanly;
2. the task archives each volume to the backup destination, or restores the volumes from a
   backup;
3. it writes a result file;
4. the service starts again.

Caravana:
- schedules `volume` backups itself (cron, UTC, `hashicorp/cronexpr`);
- holds the app's deploy lock during a run, so deploys wait;
- reads the result and the stop duration from the copy;
- records them beside Postgres backups.

The Backups tab gains a Volumes section and stop durations. The API gains `listVolumes`, and
`engine` and `stoppedSeconds` on backups (0.13.0). Postgres keeps its Nomad periodic jobs.

## Technical Context

**Language/Version**: Go 1.27 (`go.mod`), no CGO

**Primary Dependencies**: standard library; `github.com/hashicorp/nomad/api` (`Allocations().Restart`,
`AllocFS().Cat`, `HostVolumes().Get`); `github.com/hashicorp/cronexpr` (already in `go.sum` through
the Nomad client, now direct). No new module download.

**Storage**: SQLite. Migration `0008_volume_backups.sql`: `engine`, `stopped_ms` and `volumes` on
`backups` and `restores`, and a `volume_sizes` table.

**Testing**:
- table-driven `testing`; golden job files for the new task;
- `deploytest` gains fakes for restart, file reads and host volumes;
- the scheduler is tested with an injected clock;
- `TestLab` gate step `volume-backup-restore` and console step `console-volumes`, both on the
  scoped policy token.

**Target Platform**: single-node Linux host (amd64/arm64) installed by `install.sh`.

**Project Type**: web service (control plane, API, server-rendered console).

**Performance Goals**: the service is stopped under 30 s per run for a volume under 100 MB
(SC-002). The panel read costs one host-volume list plus one file read per running copy (2 s
timeout).

**Constraints**:
- the service always starts again (SC-003);
- no secret in job definitions or pages (FR-017);
- no job version per backup;
- nothing new runs on the host;
- fits the 2 GB VM: the task reserves 64 MB only while it runs (checked in the lab, R12).

**Scale/Scope**: a handful of apps with one or two volumes each; archives up to a few GB.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS. No new runtime dependency or service. `cronexpr` was already in the module graph; it becomes direct, justified in the commit. The work runs in the app's own copy, with nothing extra running between runs. |
| II. One Model, Many Inputs | PASS. `volume` is a value of `Backup.Engine`, from the API or the Compose overlay alike. The new task comes from `nomadjob.Translate`, which stays pure and golden-tested. It is a helper task under II as amended in 1.1.0: the `caravana-volumes` prestart task, added to the group of each service with volumes, beside the service's one task. Nothing reads `Source`. |
| III. Safe, Reversible Deploys | PASS. Backups add no job version and touch no deploy. Deploys wait for a run (one lock). A restore keeps the old files until the new ones are in place. Auto-revert is unchanged. |
| IV. Fail Loudly | PASS. Every refusal names `services[i].backup.engine`: no volume, a shared volume, the reserved task name. A failed run is recorded with its reason and goes on the needs-you list. A missed scheduled slot is logged. |
| V. API-First | PASS. `listVolumes`, the new fields and the `no_running_copy` code are in `api/openapi.yaml` 0.13.0 in the same commits. The console reads only through `service`, and the parity table gains `listVolumes`. |
| VI. Nomad-Native, No Lock-In | PASS. Official client only. The task is plain jobspec, exported with its variable path in the header. Uninstalling Caravana leaves a task that only measures sizes and finds no request. Nomad code stays in `nomadjob` and `deploy`. |
| VII. Secure by Default | PASS. Destination credentials sit in the task's own variable, which only that task and Caravana can read, and never in the job or a page. The policy gains `alloc-lifecycle` and `read-fs`, not `alloc-exec`. A restore needs the typed service name. |
| VIII. Plain, Tested Go | PASS. Pure functions for the script's result parsing, the request guards, the schedule's due slots and the volumes view. Lab and gate steps cover the Nomad behaviour. |

No violations; Complexity Tracking is empty. **Re-checked after design**: no change.

## Project Structure

### Documentation (this feature)

```text
specs/016-volume-backups/
├── volume-backups.spec.md
├── plan.md
├── research.md          # lab findings L1–L11, decisions R1–R12
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── api.md
│   └── console.md
├── checklists/requirements.md
└── tasks.md             # /speckit-tasks
```

### Source Code

```text
internal/app/app.go, validate.go (+ tests)            # BackupVolume; engine rules; reserved task name
internal/nomadjob/volumes.go (+ tests, testdata)      # caravana-volumes task, VolumeScript, writable group request
internal/nomadjob/translate.go, hcl.go (+ goldens)    # wire the task; export header names the variable
internal/deploy/volumerun.go (+ tests)                # request variable, wait rendered, restart, result, sizes, stop duration
internal/deploy/volumes.go, copies.go (+ tests)       # HostVolumes; Copy.RequestedRestarts
internal/deploy/backups.go                            # SetBackupDestination also rewrites caravana-volumes variables
internal/deploy/deploytest/                           # fakes: restart, file system, host volumes
internal/store/migrations/0008_volume_backups.sql
internal/store/backups.go, volumes.go (+ tests)       # engine, stopped, volumes; volume_sizes; DeleteApp
internal/service/volumebackup.go (+ tests)            # BackUpNow/Restore for volume; the run; resume
internal/service/schedule.go (+ tests)                # the scheduler, injected clock
internal/service/volumes.go (+ tests)                 # Volumes view
internal/service/backups.go, attention.go, resume.go  # engine branch; RequestedRestarts; settle at start
internal/api/volumes.go, backups.go, errors.go (+ tests), parity_test.go
api/openapi.yaml                                      # 0.12.0 → 0.13.0
internal/ui/backups.go, edit.go, composeform.go (+ tests)
internal/ui/templates/{app,compose}.html
deploy/install/caravana-policy.hcl                    # alloc-lifecycle, read-fs
cmd/caravana/gate_test.go, console_lab_test.go        # volume-backup-restore, console-volumes
CHANGELOG.md, CLAUDE.md, docs/plan.md
```

**Structure Decision**:
- what a run does to Nomad is in `deploy`;
- the job shape and the script are in `nomadjob`, pure;
- the run's order (lock, request, restart, result, clear) and the scheduler are in `service`, so
  the API and the console start runs the same way;
- the store records the runs.

## Delivery order

1. **Foundation**:
   - the app model's `volume` engine and its validation;
   - the `caravana-volumes` task in `Translate` (sizes only at first) and its goldens;
   - `deploy` writes the task's variable before register;
   - migration 0008 and the store records;
   - the policy capabilities.

   Every volume app then measures its sizes.
2. **US1, back up (P1)**:
   - the script's backup path;
   - `deploy` volume run functions;
   - `service` run, lock and resume;
   - the scheduler;
   - `RequestedRestarts` in attention;
   - the API engine fields and `no_running_copy`;
   - the console's engine option, stop notes, back-up-now confirmation;
   - the gate step's backup half.
3. **US2, restore (P1)**: the script's restore path with rollback, the service restore branch,
   the console confirmation, the gate step's restore half.
4. **US3, volumes panel (P2)**: `HostVolumes`, the `service.Volumes` view, `listVolumes`, the
   Volumes section, the console lab step.
5. **Docs**: CHANGELOG (Unreleased), CLAUDE.md "Where we are", `docs/plan.md` (016 done), the
   upgrade note (rerun the installer, then redeploy each volume app once).

US2 needs US1's run machinery. US3 needs only the foundation's sizes, and can go in parallel with
US1/US2.

## Complexity Tracking

None.
