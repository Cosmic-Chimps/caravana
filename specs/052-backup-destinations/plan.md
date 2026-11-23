# Plan 052: Several backup destinations and a second copy

Spec: `spec.md`. Task `mv6m`. Branch: the worktree branch `alta/robust-monsoon-cn34` (from `main` at 6bd3dd6, where
051 Phase A is merged). Numbers: API **0.58.0** (051 takes 0.57.0), migration **0042** (051 has 0041). API:
`contracts/api.md`.

## Constitution check

- I Small by default: no new Go module, no new runtime dependency. Copies and checks are Nomad batch jobs running the
  rclone image Caravana already pins (`settings.BackupImage`).
- II One model: `destination`, `copyTo` and `copyKeep` are fields of `app.Backup` (omitempty, so no revision moves).
  Two new helper jobs, named here and in constitution 1.2.3 (PATCH, its own commit): the copy job
  `<app>_<service>_copy-<id>` (belongs to the app, purged with it) and the check job `_backup-check-<id>` (belongs to
  no app, like 044's `_scan-<id>`). `Translate` is unchanged; `BackupCopyJob` and `BackupCheckJob` are new pure
  functions with goldens. The dump upload script gains one word in its done line (goldens change).
- III Safe deploys: nothing about deploys changes. Policy fields take effect through a normal deploy.
- IV Fail loudly: every refusal names its field (spec "Refusals"). A copy that fails is recorded failed with its
  message and raises a needs-you item; nothing is dropped silently.
- V API-first: every operation in `api/openapi.yaml` 0.58.0 in the same change; the console calls the same service
  methods; `GET/PUT /v1/backups/destination` keep their meaning (the default).
- VI Nomad-native: through the official client; jobs as JSON.
- VII Secure: each destination's secret key lives only in its Nomad Variable; a copy or check job gets the keys only in
  its own variable `nomad/jobs/<job>` (rendered into `secrets/`), purged with the job. Nothing in argv, job env, pages,
  answers, logs or SQLite. Sentinel tests.
- VIII Plain, tested Go: table tests, goldens, script tests under a real `sh` with a fake rclone, a lab gate step and
  a console step.

## Decisions

- **D1 Destinations in the store.** Migration `0042_backup_destinations.sql`:
  `backup_destinations(name TEXT PRIMARY KEY, endpoint, region, bucket, prefix, access_key_id, secret_path TEXT NOT
  NULL, is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1)), tested_at TEXT, test_ok INTEGER,
  test_message TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL) STRICT` with a partial unique index on `is_default`
  where it is 1. The old row of `backup_destination` becomes `main`, default, `secret_path =
  'caravana/backup-destination'` (no Nomad write: its key stays where it is, FR-001); the old table is dropped. New
  destinations' `secret_path` is `caravana/backup-destinations/<name>` (item `secretAccessKey`, as today).
  `backups.destination TEXT NOT NULL DEFAULT ''` and `restores.destination TEXT NOT NULL DEFAULT ''`, set to `'main'`
  on existing rows when `main` exists. `projects.backup_destination TEXT` (NULL: the installation's default).
  `backup_copies(app_id, backup_id, destination, status CHECK IN ('pending','running','succeeded','failed'), object
  TEXT NOT NULL DEFAULT '', bytes INTEGER NOT NULL DEFAULT 0, message TEXT NOT NULL DEFAULT '', expired INTEGER NOT NULL
  DEFAULT 0, job_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT, PRIMARY KEY
  (app_id, backup_id, destination), FOREIGN KEY (app_id, backup_id) REFERENCES backups (app_id, id) ON DELETE
  CASCADE) STRICT`, index on `status`. `DeleteApp` deletes the app's `backup_copies` before its `backups`.
  Store: `ListBackupDestinations`, `GetBackupDestinationNamed(name)`, `GetBackupDestination` (now: the default, or
  `ErrNotFound`), `SaveBackupDestination(d)` (insert or update by name; the first row is made the default),
  `SetDefaultBackupDestination(name)` (one transaction), `DeleteBackupDestination(name)`, `SaveBackupDestinationTest`,
  `SetProjectBackupDestination(project, name or "")`, the project's field on `Project`, `QueueBackupCopies(appID,
  backupID, names)` (insert or ignore, `pending`), `ListBackupCopies(appID)` (by backup), `OpenBackupCopies()` (pending
  and running, oldest first), `SaveBackupCopy`, `ExpireBackupCopies(appID, destination, ids)`, `LatestBackupCopies()`
  (newest copy per app, service and destination, for attention). `BackupRecord.Destination`, `BackupRecord.Copies`
  (filled by `ListBackups`/`GetBackup`), `RestoreRecord.Destination`.
- **D2 Destinations in the service** (`service/destinations.go`). `DestinationInput` gains `Default *bool`.
  `ListBackupDestinations(ctx) ([]DestinationView, error)`: the stored row plus `UsedBy` (each app and service whose
  resolved destination or copies name it, with role `destination` or `copy`; each project whose default it is).
  `SaveBackupDestination(ctx, name, in)`: the existing field checks (endpoint, region, bucket, prefix, access key;
  secret required the first time, kept when left out), `name` must be `app.ValidLabel`; writes the secret to
  `secret_path`, then the row; `Default` true makes it the default, false on the default is a field error `default`
  ("make another destination the default instead"); then D6's repoint. `DeleteBackupDestination(ctx, name)`:
  `store.ErrNotFound` → `ErrDestinationNotFound` (404 `destination_not_found`), the default →
  `ErrDestinationIsDefault` (409), used → `*DestinationInUseError{Users []string}` (409 `destination_in_use`, the
  message names up to 5 users and "and N more"); then the row, then the variable (a migrated `main` deletes
  `caravana/backup-destination`). `SetBackupDestination(ctx, in)` (the old single-destination call, kept for the old
  route and console callers) saves the default's name, or `main` when there is none. `backupDestination(ctx)` keeps
  meaning "the default, with its secret" for the callers that mean the installation's (045's archive, 035's
  templates, the no-destination checks). `destinationNamed(ctx, name) (deploy.BackupDestination, bool, error)`.
  Every change publishes `ChangeAttention` (no new notice: the settings page is a plain form page).
- **D3 The policy.** `app.Backup` gains `Destination string \`json:"destination,omitempty"\``, `CopyTo []string
  \`json:"copyTo,omitempty"\`` and `CopyKeep int \`json:"copyKeep,omitzero"\``. `validateBackup` (pure): each name
  `ValidLabel` (`.destination`, `.copyTo[k]`), at most 2 copies (`.copyTo`), no duplicate (`.copyTo[k]`), no copy equal
  to `Destination` (`.copyTo[k]`), `CopyKeep` 0 or 1–100 and only with copies (`.copyKeep`). `Backup.CopyKeepOrDefault()`.
  The Compose overlay reuses `*app.Backup`, so it takes them, with `services.<svc>.backup.*` errors. A promotion keeps
  the target's backup (unchanged rule); Copy to keeps the policy.
- **D4 Resolution.** `s.backupPlaces(ctx, appID string, b app.Backup) (primary string, copies []string, err error)`:
  `b.Destination`, else the app's project's `backup_destination`, else the default's name; copies are `b.CopyTo`.
  `checkBackups` (deploy flow, `SetBackupPolicy`, `SetRestoreSchedule`, Compose and templates go through it) refuses:
  no destination at all (today's message, on `services[i].backup`), an unknown `destination` or `copyTo[k]`
  ("no backup destination is named %q; add it in Settings › Backup storage"), a copy equal to the resolved
  destination (`copyTo[k]`, "is where this service's backups already go"). `UpdateProject` with `backupDestination`:
  unknown name is a field error `backupDestination`; a name some policy of the project copies to without naming its
  own destination is refused naming that service.
- **D5 Deploy-side destination items.** `deploy.BackupDestination` gains `Name`. `destinationKeys(dest)` becomes
  `remoteKeys(remote string, dest)` (`RCLONE_CONFIG_<REMOTE>_*`; `dest` and the existing callers use `DEST`).
  `destinationItems` adds `BACKUP_DESTINATION=<name>`; `isDestinationKey` covers it. `SyncBackups` takes
  `dests map[string]BackupDestination` (per service) instead of one. The dump upload script's done line becomes
  `<marker> key=$key bytes=… dest=$(printenv BACKUP_DESTINATION || true)`; `BackupRun.Destination` reads it; the
  watcher records it, or the service's current destination when empty (a job from before this change).
  `SQLiteUpload`/`SQLiteDownload` take the destination as today (its name is ignored by the script).
- **D6 Repoint.** `deploy.SetBackupDestination` (the scan of every variable) is replaced by
  `Deployer.PointBackups(ctx, appID, service string, dest BackupDestination) error`: rewrites the destination items of
  the service's dump job variable (when it exists) and of its caravana-volumes variable (when it holds `BACKUP_PATH`),
  keeping every other item. `s.repointBackups(ctx)` walks the stored apps and points each backed-up service at its
  resolved destination; it runs after a destination is saved, the default changes, or a project's default changes.
  A failure answers "saved, but the backup jobs still point at the old destination: …" as today.
- **D7 Copies** (`service/backupcopies.go`). When a backup is first recorded `succeeded` (the dump watcher's
  `recordApp`, the volume run's and the sqlite run's settle), `queueCopies` inserts one `pending` row per name of the
  service's current `copyTo` and wakes `backupsKey`. `copyPass(ctx)` runs at the end of every `recordBackups` pass:
  1. settle each `running` row: `deploy.BackupCopyOutcome(jobID)` (running, succeeded with object, bytes and the
     expired names, failed with a message); a final outcome saves the row, marks the expired copies
     (`ExpireBackupCopies`, ids by `expiredRecordID`), purges the job and its variable, and calls `backupsChanged`;
     a job Nomad doesn't have is failed "Nomad has no record of this copy"; running over 6 hours is failed "the copy
     didn't finish in 6h0m0s" and purged;
  2. start `pending` rows, oldest first, while fewer than 2 copies run: the backup must be succeeded with its primary
     not expired (else failed "the backup was deleted from <dest> by retention before it was copied"), the source and
     target destinations must exist (else failed "the destination <name> was removed"), the service must still have a
     backup policy (else failed "the service no longer has a backup policy"). `keep` is `CopyKeepOrDefault()`, the pins
     are `pinStamps(app, service)` (a pin that can't be read leaves the row pending for the next pass). Then
     `deploy.StartBackupCopy` → the row is `running` with its job ID.
  The object in the target is the source object with the source's base (`<bucket>/<prefix>`) replaced by the
  target's: `<target base>/<app>/<service>/<the rest>`. Kind `dir` for the volume engine (its object ends in `/`),
  `file` otherwise; `EXT` is the engine's (`.dump`, `.sql.gz`, `.archive.gz`, `.sqlite.gz`; `""` for a directory).
  Events: job IDs containing `_copy-` wake `backupsKey` (`service/events.go`).
- **D8 The copy job** (`nomadjob/backupcopy.go`, pure, goldens `backup-copy-file`, `backup-copy-dir`).
  `BackupCopyJobID(appID, service, id)` = `<app>_<service>_copy-<id>` (id: 12 lowercase hex). Batch, no restart and no
  reschedule, one group and one task `copy` running `settings.BackupImage` with `/bin/sh -c BackupCopyScript`, 200 MHz
  and 256 MB, `variableTemplate(id)` (env from `nomad/jobs/<id>`, in `secrets/`). The variable's items (written by
  deploy, never in the job): `RCLONE_CONFIG_SRC_*`, `RCLONE_CONFIG_DEST_*`, `SRC_OBJECT`, `DEST_OBJECT`, `DEST_PATH`
  (`<target base>/<app>/<service>`), `KIND`, `EXT`, `KEEP`, `PINNED`. The script: `rclone copyto src:$SRC_OBJECT
  dest:$DEST_OBJECT` (file) or `rclone copy src:$SRC_OBJECT dest:$DEST_OBJECT` (dir); `rclone size --json` of the
  target object; `caravana-copy: done key=$DEST_OBJECT bytes=<n>` (a size of 0 files fails the run: "the copy is
  empty"); then retention in `DEST_PATH` exactly as the upload script does it (files ending in `EXT`, or directories
  of digits; pinned stamps kept; this run's own never deleted; a `PINNED` that isn't digits and spaces deletes nothing
  and prints a warning, the copy still succeeds), printing `caravana-copy: expired <name>` per deletion. No
  `set -x`; rclone's own errors go to stderr, from which `batchOutcome` takes the last line.
- **D9 The check job** (`nomadjob/backupcheck.go`, pure, goldens `backup-check-test`, `backup-check-locate`).
  `_backup-check-<id>`, batch, one task `check`, same image and template. Items: `MODE` (`test` or `locate`), `CHECK_ID`,
  per place `i` = 1..3: `RCLONE_CONFIG_R<i>_*`, `R<i>_PATH` (test: the destination's base; locate: the object). Test
  (one place): `rclone lsf --max-depth 1 r1:$R1_PATH/` (exit 3, directory not found, counts as reachable), then a
  small object `$R1_PATH/.caravana-check-$CHECK_ID` written with `rcat` and removed with `deletefile`; prints
  `caravana-check: ok`, or `caravana-check: failed <list|write|delete>` and leaves rclone's line on stderr. Locate:
  for each place in order, `rclone size --json r<i>:$R<i>_PATH`; a count above 0 prints `caravana-check: found <i>`
  and stops; a count of 0 or exit 3 prints `caravana-check: missing <i>`; any other exit prints
  `caravana-check: unreachable <i>`. `deploy.CheckBackupPlaces(ctx, mode, places []CheckPlace) (CheckResult, error)`
  writes the variable, registers, follows the job until it is dead (at most 90 s), reads stdout and the failed
  line, and always purges the job and deletes the variable (with a fresh 30 s context). Messages pass through
  `redact` (no secret key in any text).
- **D10 Test.** `Service.TestBackupDestination(ctx, name) (store.DestinationTest, error)`: runs the check's `test`
  mode, stores the outcome on the row (`tested_at`, `test_ok`, `test_message`: "listed, wrote and removed a test
  object" or "could not <step>: <rclone's line>"), and answers it; an outcome is never an error (only an unknown name
  or Nomad's own failure is).
- **D11 Restore.** `Service.Restore(ctx, appID, backupID, from string)`. The places of a backup are its own
  destination (unless expired there) and then each succeeded, unexpired copy, in `copyTo` order. A backup with no
  place is not restorable (today's error). `from` set: it must be one of the places (else field error `from`). `from`
  empty and more than one place: the check's `locate` mode picks the first place that holds the object; none found is
  `ErrBackupMissing` (409 `backup_missing`, "the backup isn't in main or offsite any more"), and when a place is
  marked missing its record is marked expired there (the primary's `expired`, or the copy's). One place: no check
  (today's path, no extra job). `restoreBackup(ctx, appID, b, trigger, label, from)` then restores from that place:
  dump engines `deploy.Restore(..., source BackupDestination, object, …)` (the restore variable's destination items
  are replaced by the source's), volume `prepareVolumeRun` writes the source destination into the variable for the
  restore (`WriteVolumeVariable`; the next backup writes its own destination back), sqlite `SQLiteDownload` with the
  source. The restore record keeps `Destination`. The restore clock passes `from` empty. `ListBackups` keeps a backup
  while any place holds it; pinning (036) needs a place, not the primary.
- **D12 Needs-you and notifications.** `backup_failed`'s detail names the destination ("to main"). New kind
  `backup_copy_failed` (`AttentionBackupCopyFailed`, a warning; key `backup_copy_failed|<app>|<service>|<dest>`): the
  newest finished copy of the service to that destination failed. `ProblemParts` words it ("The copy of
  <service>'s backup to <dest> failed"), the home and the notifications use those words; `notify.Event` gains
  `destination`. `no_backup_destination` stays (a policy with no destination at all).
- **D13 API** (`contracts/api.md`). New: `listBackupDestinations`, `putBackupDestination`, `deleteBackupDestination`,
  `testBackupDestination`; `BackupPolicy.destination/copyTo/copyKeep`; `Backup.destination/copies`;
  `Restore.destination`; `restoreBackup` takes an optional body `{from}`; `Project.backupDestination` and
  `ProjectUpdate.backupDestination`; codes `destination_not_found`, `destination_is_default`, `destination_in_use`,
  `backup_missing`; the webhook notification's `destination`. Deploy tokens are denied the new routes by the default
  deny. MCP: no new tool (`list_backups` passes the new fields through).
- **D14 Console.** Settings › Backup storage lists the destinations (name, "default", endpoint and bucket/prefix,
  the last test, "used by N"), each with Edit, Test, Make default and Remove (a refusal is drawn on the row), and an
  Add destination form (name, the fields, "Make it the default"); the secret key input never holds a value. The project
  page has "Backups go to" (a select: "The installation's default (main)" and each destination). The Backups tab: the
  policy line says "to main, copied to offsite (keeps 14)"; a "Where backups go" form per backed-up service (destination
  select, "Also copy to" checkboxes, copy keep) posts to `/apps/{id}/backups/places` as a deploy on the page's revision
  through `Service.SetBackupPlaces` (like `SetRestoreSchedule`, kept-file apps through the overlay); each backup row
  says where it is ("in main · copied to offsite", "copying to offsite…", "copy to offsite failed: …"); the restore
  confirmation has "Restore from" (Automatic: the first place that has it, then each place). The Compose form's backup
  part carries destination and copies through the draft (like restore's fields). The attention row for
  `backup_copy_failed` links to the app's Backups tab. Forms work without JavaScript.
- **D15 Lab.** The gate's bucket job makes two buckets (`backups`, `offsite`). Gate step `backup-copies`
  (`gate_backup_copies_test.go`, after `sqlite-backup-restore`): add `offsite` through the API, Test both (ok) and a
  third with a wrong bucket (failed, no key in the message), a Postgres service with `copyTo: ["offsite"]`, Back up now,
  the backup and its copy succeeded and the object under `/data/offsite/`, change the data, delete the object under
  `/data/backups/`, restore without `from`: the restore's destination is `offsite` and the data is back; removing
  `offsite` while used is 409; the gate's secret key in no answer, log, job, export or the SQLite file; clean up.
  Console step `console-backup-destinations` (`console_backup_destinations_test.go`): add a destination through the
  form, Test, a busybox volume app whose Backups tab sets "Also copy to", Back up now, the row says "copied to
  offsite", the restore form offers it.

## Phases

- **A — model, store, destinations, resolution** (T001–T006): D1–D6; existing behaviour unchanged.
- **B — copies and checks** (T007–T011): D7–D11 and D12.
- **C — API** (T012–T013): D13.
- **D — console and docs** (T014–T017): D14, docs, changelog, constitution amendment.
- **E — lab** (T018–T019): D15.
