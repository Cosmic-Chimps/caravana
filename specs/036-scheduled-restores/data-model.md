# Data model: scheduled restores

## App model (`internal/app`)

```go
type Backup struct {
    Engine, File, Schedule string
    Keep int
    // Restore, when set, restores the backup pinned under Restore.From on Restore.Schedule (feature 036).
    Restore *RestoreSchedule `json:"restore,omitempty"`
}

type RestoreSchedule struct {
    Schedule string `json:"schedule"` // five-field cron, UTC; required
    From     string `json:"from"`     // a label (ValidLabel); required
}

func ValidLabel(s string) bool // ^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$
```

Field errors: `services[i].backup.restore` (both empty? no: when present, both are required),
`services[i].backup.restore.schedule`, `services[i].backup.restore.from`.

## Store (migration `0029_restore_schedules.sql`)

`backups`: `ALTER TABLE` adds `pin TEXT` and `pinned_at TEXT` (NULL when not pinned), and
`CREATE UNIQUE INDEX backups_pin ON backups (app_id, service, pin) WHERE pin IS NOT NULL`.

`restores`: rebuilt (as 0022) with every column it has, plus:
- `status` CHECK `IN ('running', 'succeeded', 'failed', 'skipped')`
- `trigger TEXT NOT NULL DEFAULT 'owner' CHECK (trigger IN ('owner', 'schedule'))` — old rows read `owner`
- `pin TEXT` — the label a scheduled run asked for; NULL for the owner's
- `backup_id` stays `NOT NULL`; a refused run without a backup writes `''`

Go:

```go
const (
    RestoreTriggerOwner    = "owner"
    RestoreTriggerSchedule = "schedule"
    RestoreSkipped         = "skipped" // a restore status
)
type BackupRecord struct { /* … */ Pin string; PinnedAt *time.Time }
type RestoreRecord struct { /* … */ Trigger string /* "" saves as owner */; Pin string }

func (s *Store) PinBackup(ctx, appID, backupID, label string, at time.Time) (prev string, err error)
    // prev: the ID of the backup that held the label before ("" for none), so the caller can put it back
func (s *Store) UnpinBackup(ctx, appID, backupID string) error
func (s *Store) PinnedBackup(ctx, appID, service, label string) (BackupRecord, error) // ErrNotFound
func (s *Store) LastScheduledRestore(ctx, appID, service string) (time.Time, bool, error)
```

`SaveBackup`'s upsert never touches `pin`/`pinned_at`. `LatestBackupRuns` ignores skipped restores.

## Engines

| Engine | Where the pins go | Read by |
| --- | --- | --- |
| postgres | item `BACKUP_PINNED` of `nomad/jobs/<app>_<svc>_backup` | `uploadScript` (env, at task start) |
| volume | request item `PINNED` of `nomad/jobs/<app>/<svc>/caravana-volumes` | `VolumeScript` `apply_retention` |
| sqlite | 6th argument of `SQLiteUploadScript` | `retain` |

Values: unix seconds separated by one space, ascending; empty for none.

## Restore record outcomes of a due slot

| Case | status | message (Caravana's words) |
| --- | --- | --- |
| no pin | failed | `no backup of <svc> is pinned as <label>` |
| pin expired | failed | `the backup pinned as <label> was deleted by retention` |
| engine changed | failed | `the backup pinned as <label> was made by the <x> engine; <svc> now backs up with <y>` |
| busy | skipped | `a backup or restore of <svc> was running` |
| app stopped | — (nothing) | |
| no running copy / no destination / Nomad error | failed | the error in words |
| started | running → succeeded/failed | as the manual restore of the engine |
