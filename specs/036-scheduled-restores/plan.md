# Implementation Plan: Restore a pinned backup on a schedule

**Spec**: [spec.md](./spec.md) · **Data model**: [data-model.md](./data-model.md) · **Contracts**:
[contracts/api.md](./contracts/api.md), [contracts/console.md](./contracts/console.md) · **Tasks**: [tasks.md](./tasks.md)

## Summary

Two pieces. **Pins**: a `pin` label on a backup row; every engine's retention gets the pinned timestamps and
skips them before counting `keep`. **Restore schedules**: `app.Backup.Restore{Schedule, From}`; the volume-backup
scheduler's pass also runs due restore slots, each one through the same internal restore the manual Restore
uses, recorded with trigger `schedule`. No new module, no runtime dependency, no Nomad policy change, no change
to `nomadjob.Translate`'s output (goldens untouched). Migration **0029** (0027 is left for spec 034, 0028 is
035's). API **0.39.0** (0.38.0 left for 034).

## Constitution Check

- **I Small**: no module, no service. ✓
- **II One model**: the policy is a field of `app.Backup`, so image, Compose and Aspire inputs all normalize into
  it; `Translate` doesn't read it (Caravana's clock does), so its goldens don't move. ✓
- **III Safe deploys**: a deploy is unchanged; restores are the existing per-engine restores, which put
  everything back on failure. ✓
- **IV Fail loudly**: a bad schedule or label is a field error naming the path; a label not yet pinned is a
  **warning** on the deploy; every due slot leaves a record (succeeded, failed with the reason, or skipped with
  the reason); the console's Compose form carries the field so a deploy from the form never drops it. ✓
- **V API-first**: pin/unpin and the policy field are in the API and `api/openapi.yaml` in the same change; the
  console calls the same service methods. ✓
- **VI Nomad-native**: the Postgres pin list is an item of the backup job's own variable; nothing new in jobs. ✓
- **VII Secure**: no token is held for scheduled restores (the clock is inside Caravana); labels and timestamps
  are not secrets; nothing new is logged. ✓
- **VIII Plain Go**: table tests; the shell retention is tested by running the scripts with a fake `rclone`. ✓

No Complexity Tracking entry.

## Decisions

**D1 Pins are labels on backup rows.** `backups.pin TEXT NULL`, `backups.pinned_at TEXT NULL`, unique index
`(app_id, service, pin) WHERE pin IS NOT NULL`. `store.PinBackup(ctx, appID, backupID, label)` clears the label
from any other backup of the same service and sets it on this one, in one transaction; `UnpinBackup` sets both
NULL. `BackupRecord.Pin`, `PinnedAt`. Identifier names: say **pin/label**, never `pinned` alone, because
`service/deploy.go` already has a `pinned` type for pinned image digests.

**D2 The pin list is timestamps.** Every engine names a backup `<service>-<unix seconds>` and its object by the
seconds (`<ts>.dump`, `<ts>/`, `<ts>.sqlite.gz`), so the pins reach retention as space-separated seconds:
- Postgres: item `BACKUP_PINNED` in the backup job's variable `nomad/jobs/<app>_<svc>_backup` (rendered into the
  upload task's env at its start by `variableTemplate`). `SyncBackups` writes it with the other items (it gets
  `pins map[service][]string`); `Deployer.SetBackupPins(ctx, appID, service, stamps)` rewrites only that item
  when the variable exists (no variable: no job yet, nothing to do). `SetBackupDestination` already keeps other items.
- Volume: request item `PINNED` (added to `volumeRequestKeys`), written with each **backup** request.
  `VolumeScript`'s `handle_request` resets `PINNED=` with the others.
- SQLite: a sixth argument of `SQLiteUploadScript` (`deploy.SQLiteUpload` gains `pinned []string`).
- Each script: if the list holds anything but digits and spaces, retention deletes nothing and says why (in the
  run's message / warning marker, as a retention failure is said today). Otherwise it removes the pinned
  stamps from the candidate list **before** `excess = count - KEEP`. The current run's own stamp stays protected
  as today.
- `uploadScript` (Postgres) keeps using `$VAR` and `$(...)` only (Nomad interpolates `${...}`).

**D3 Pin and unpin take the app's deploy lock with `TryLock`** (409 `app_busy`, as Stop/Start/Restart do) and
refuse while `backupBusy` (409 `backup_running`): so a retention run never starts with a pin list older than the
store's, and a deploy's `syncBackups` reads pins the store won't change under it. Order: check, write the store,
then `SetBackupPins` for a Postgres service; if the Nomad write fails, put the store back (re-pin the old holder
/ unpin) and return the error (502). A backup that isn't `succeeded` or is expired is `store.ErrNotFound` with
the reason (as `Restore` does). Label validation is an `*app.FieldError` on `label`.

**D4 The policy field.** `app.Backup.Restore *RestoreSchedule` (`json:"restore,omitempty"`),
`RestoreSchedule{Schedule string "schedule"; From string "from"}`; `app.ValidLabel(s)`. `validateBackup` checks
both for every engine (`<field>.restore.schedule`: five fields and `cronexpr.Parse`, as the volume schedule;
`<field>.restore.from`: `ValidLabel`). The compose overlay reuses `*app.Backup`, so it gets the field and its
errors at `services.<svc>.backup.restore.*` for free (check the path the overlay's validation names). `promoted`
keeps the target's backup policy as today. Revision: unchanged for apps without the field (omitempty).

**D5 The clock.** `runDueVolumeBackups`' loop (`schedule.go`) also calls `runDueRestores(ctx, noted)` each pass
(the same `scheduleGap`, 30 s; a new setter `SetBackupScanGap(d)` in the style of `SetScheduleScanGap` lets
the lab shorten it, and `startLab` sets 10 s). For each app not stopped, each service with `Backup.Restore`:
`base` = `CreatedAt` of the service's newest restore with trigger `schedule` (any status), else `rec.UpdatedAt`;
`slot, due, skipped := dueSlot(restore.Schedule, base, now)`; skipped only logs (noteOnce); due runs
`s.scheduledRestore(ctx, rec, svc)`:
1. pinned backup of the service with label `From` (`store.PinnedBackup`): none → refused; expired → refused
   ("the backup pinned as golden was deleted by retention"); engine ≠ service's → refused (engine message).
2. `backupBusy` → **skipped** record ("a backup or restore of <svc> was running at <slot>").
3. `s.restoreBackup(ctx, appID, b, store.RestoreTriggerSchedule, label)` — the body of today's `Restore` after it
   has found the backup, generalized with trigger and label. `ErrBackupRunning` from it (a claim lost to a race)
   → skipped record. `ErrAppStopped` → nothing recorded (it waits). Any other error → failed record with
   Caravana's words for it (`ErrNoRunningCopy`: "the service has no running copy"; `ErrNoBackupDestination`:
   "no backup destination is set"; else the error's text, which never holds a secret today).
A refused/skipped/failed-to-start record: `RestoreRecord{ID: lower(rand.Text()), BackupID: b.ID or "", Status:
failed|skipped, Message, Trigger: schedule, Pin: label, CreatedAt: now, FinishedAt: now, Engine: svc's}`.
Every record publishes `backupsChanged`. The manual `Restore` records trigger `owner`.

**D6 Restores table.** Migration 0029 rebuilds `restores` (CHECK changes need a rebuild, as 0022 did): status
adds `skipped`; new `trigger TEXT NOT NULL DEFAULT 'owner' CHECK (trigger IN ('owner','schedule'))` (every old
row was the owner's); new `pin TEXT` (the label a scheduled run asked for). `SaveRestore`'s upsert keeps its
`WHERE restores.status = 'running'` rule; a `skipped` row is final like `failed`. `LatestBackupRuns` picks the
newest restore **that isn't skipped** (so a skip never hides a failure). `RunningRestores` unchanged.
`store.LastScheduledRestore(ctx, appID, service) (time.Time, bool, error)`.

**D7 Deploy warning.** In `saveAndDeploy` beside `syncBackups` (deploy.go ~L414): for each service whose
policy has `Restore`, if no unexpired backup of that service is pinned with `From`, append
`app.Warning{Field: "services[i].backup.restore.from", Message: "no backup of <svc> is pinned as <label> yet:
each scheduled restore is refused until one is (pin one on the Backups tab, or PUT
/v1/apps/<id>/backups/<backupId>/pin)"}`. A store error is logged and adds no warning.

**D8 Needs-you and notifications** stay `restore_failed`; `attentionOf` gets the non-skipped newest restore from
the store. `ProblemParts` keeps its sentence; the item's `Detail` is the record's message, so "no backup of web is
pinned as golden" reaches the home and the notification. Nothing else changes in `notify`.

**D9 Console.** See [contracts/console.md](./contracts/console.md). Routes:
`POST /apps/{id}/backups/{backupId}/pin` (form `label`), `POST /apps/{id}/backups/{backupId}/unpin`,
`POST /apps/{id}/backups/restore-schedule` (form `service`, `schedule`, `from`, `revision`; an empty schedule
clears it), all 303 to `/apps/{id}/backups#svc-<service>` on success and the tab redrawn with the notice on a
refusal (`refuseBackup`, plus `ErrAppBusy` and field errors). The restore-schedule form calls
`SetBackupPolicy` with the service's current policy and `Restore` changed. The Compose form gains per service
`backupRestoreSchedule` and `backupRestoreFrom` (both or neither; refused without an engine). `parity_test.go`
lists the three console routes with their API operations (`pinBackup`, `unpinBackup`, `getApp`+`deployApp`).

**D10 Aspire.** In `../aspire-hosting-caravana` (its own repo, branch `scheduled-restore`, committed, not pushed):
`WithCaravanaScheduledRestore<T>(caravana, string from, string schedule)` adds `restore` to the service's overlay
backup; it requires a `WithCaravanaBackup` on the same service (an error at publish naming the service
otherwise); tests in `ResourceSettingsTests`; README and CHANGELOG lines.

## Lab

- `sqlite-backup-restore`: set `keep: 2`, back up, pin it as `golden`, five more backups, check the pinned row
  is not expired and its object is listed in the bucket, and two unpinned are kept; add a row after the pin;
  deploy `restore: {schedule: "<M> <H> * * *", from: golden}` with M/H = UTC now + 2 minutes; wait for a restore
  with trigger `schedule` to succeed; the added row is gone.
- `volume-backup-restore`: the same scheduled part (a file added after the pin is gone), without the five backups
  (each stops the service).
- `startLab` sets the scheduler gap short enough (10 s) for the slot to be seen promptly.
- Both steps remove the schedule (or the app) at the end so nothing keeps restoring.
