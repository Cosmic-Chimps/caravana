# Feature 039: a backup policy with no automatic backups (`schedule: "never"`)

**Status**: implemented and reviewed on branch `039-backup-schedule-never` | **Created**: 2026-11-15 | **Size**: small (one
spec file: spec, plan and tasks together)

**Input**: request from the Umbral session for the public Umbral demo (Umbral task `28zs`), approved by the
maintainer on 2026-11-15 ("do it now"; 0.21.0 is released after it).

## Why

The Umbral demo (app `umbral-demo`, service `umbral`, engine `volume`) resets every night at 04:00 UTC by
restoring a pinned golden backup with 036's `restore: {schedule, from}`. A backup policy always backs up on a
schedule (empty means `0 3 * * *`), so the demo would also back up the populated instance every night, and
retention would keep the day's visitors' data. The demo's privacy page promises only the empty instance is ever
copied. Today it dodges this with `"0 4 31 2 *"` (31 February), which works only by accident of the cron library
and the slot finder. With pinning, such a service needs no scheduled backups at all.

## What

**User story (P1).** As an owner, I declare that a service's backup policy takes no automatic backups, and keep
Back up now, pinning, retention of the backups I take, and the scheduled restore.

- **FR-001** `backup.schedule` accepts the word `never` (exact, lowercase) besides a five-field cron line, for
  every engine (`postgres`, `volume`, `sqlite`). Empty still means `0 3 * * *` (unchanged: an omitted field
  never changes meaning). Anything else that isn't a valid cron line is refused naming `…backup.schedule`, and
  the message mentions `never`.
- **FR-002** With `never`, Caravana never starts a backup by itself: the volume and sqlite scheduler skips the
  service, and a Postgres service's backup job never runs on its own.
- **FR-003** Back up now works as before for every engine; retention (`keep`) applies to the backups taken by
  hand; pinned backups are never deleted (036); `restore: {schedule, from}` runs as before.
- **FR-004** `restore.schedule` stays a cron line: `never` there is refused naming `…backup.restore.schedule`
  (a restore that never runs is a policy without `restore`).
- **FR-005** Reads say it plainly: the API returns `schedule: "never"` as stored; the built-in schedule for the
  backup (022's `backup:<app>:<service>`) has no next run; the console shows "No automatic backups (Back up now
  only)" wherever it shows the backup's schedule (Configure, the Backups tab, Schedules), and its backup forms
  offer it as a choice.
- **FR-006** Nothing judges a `never` service for not backing up on its own (no needs-you item, no
  notification about a missed or overdue scheduled backup).
- **FR-007** The Compose overlay and templates take the same value; `api/openapi.yaml` documents it on
  `BackupPolicy.schedule` (version 0.42.0); the Aspire package gets `CaravanaBackupSchedule.Never` (its own
  repository, separately).

**Success criteria.** A volume service with `{engine: volume, schedule: never, keep: 3, restore: {schedule,
from}}` deploys, takes no backup at its old 03:00 slot, backs up on Back up now, restores the pinned backup on
schedule; a Postgres service with `never` registers no run on its own and backs up on Back up now.

## Plan

- **Model** (`internal/app`): `BackupScheduleNever = "never"`, a `Backup.Automatic() bool`
  (`Schedule != "never"`), validation in `validateBackup` (and `validateRestoreSchedule` refusing `never`).
  `ScheduleOrDefault` keeps returning the stored word; every caller that turns it into a time checks
  `Automatic()` first.
- **Volume and sqlite scheduler** (`service/schedule.go`, `dueSlot` caller): skip a service whose backup isn't
  automatic. Built-in schedule (`service/schedules.go`): no `NextRunAt`, words "never".
- **Postgres** (`nomadjob.BackupJob`): Back up now is `Jobs().PeriodicForce`, which Nomad refuses for a
  disabled periodic job (`IsPeriodicActive`), so the job stays periodic and enabled with a cron spec that has no
  next time (a constant in `nomadjob`, e.g. `0 0 30 2 *`), hidden from the API. **Verify in the lab** before
  relying on it: Nomad registers such a job, never launches a child on its own, and `PeriodicForce` starts a
  run. If Nomad refuses it, stop and report; the fallback (a one-off job for Back up now) is a bigger change.
- **Console** (`ui/backups.go`, `configure.go`, the schedule words): the label above, and the choice in the
  backup forms.
- **Docs**: openapi 0.42.0, `changelog.d/backup-schedule-never.md`, the CLAUDE.md paragraph line.
- Constitution: III/IV unaffected (an explicit word, refused names the field); no new module; `Translate`
  unchanged (the backup job is `nomadjob.BackupJob`, pure, golden test).

## Tasks

- [x] T1 Model, validation and table tests (`internal/app`).
- [x] T2 Scheduler skip and built-in schedule (`internal/service`), tests.
- [x] T3 Postgres job spec for `never` (`internal/nomadjob`), golden test; lab check of register, no
  self-launch and `PeriodicForce`.
- [x] T4 Attention and notifications ignore `never` (FR-006), test.
- [x] T5 Console label and form choice, tests; `tools/ui-css.sh build` if styles change.
- [x] T6 openapi 0.42.0 description, release note, CLAUDE.md.
- [x] T7 CI checks: build, vet, staticcheck, gofmt, cross-compile, release check, `go test -count=1 ./...` (28
  packages, 2026-11-15). The race detector was left to CI: the machine's load (35–600) timed it out on
  three packages in earlier runs with no failing test.
