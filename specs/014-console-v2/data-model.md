# Data model: Console v2

No migration. Everything new is either computed from what is stored (revision, attention) or
read live from Nomad (usage, out of memory).

## App revision (store, computed)

`store.AppRecord` gains `Revision string`: the first 16 hex digits of SHA-256 over the stored
`apps.document` bytes. Same document, same revision; any saved change, a new one.

- `store.SaveAppIf(ctx, a, revision) (AppRecord, error)`: in one `BEGIN IMMEDIATE` transaction,
  reads the document, returns `store.ErrRevisionMismatch` when its revision differs (or
  `ErrNotFound`), else saves as `SaveApp` does (an identical document still changes nothing).
- `store.InsertApp` (existing) is the "only if new" save.

## Condition (service)

```go
type Condition struct {
    IfRevision string // save only if the stored app has this revision
    IfNew      bool   // save only if no app has this ID
}
```

Zero value: no condition. Both set: refused as a programming error. Errors:
`service.ErrAppChanged` (wraps `store.ErrRevisionMismatch`), `service.ErrAppExists` (existing).

## Edit form (ui, per service)

| Form field | Model field | Rule |
| --- | --- | --- |
| `svc.<name>.image` | `Image` | required |
| `svc.<name>.env` | `Env` | lines `NAME=value`; `#` and blank lines skipped; a line without `=` is an error at this field naming the line number |
| `svc.<name>.port` | `Ports[0].ContainerPort` | only when the service has one port; 1–65535 |
| `svc.<name>.domains` | `Domains` | one host per line, each on the only port's name |
| `svc.<name>.copies` | `Replicas` | whole number ≥ 1; empty means 1 |
| `svc.<name>.healthPath` | `Health.Path` | empty means TCP; only when the service has ports |
| `svc.<name>.healthInterval`, `…healthTimeout` | `Health.Interval`, `Health.Timeout` | seconds; empty means default |
| `svc.<name>.cpu` | `Resources.CPUMHz` | MHz; empty means default |
| `svc.<name>.memory`, `svc.<name>.memoryMax` | `Resources.MemoryMB`, `MemoryMaxMB` | MB; empty means default |
| `revision` | (condition) | hidden; the revision the form was drawn from |

Kept as stored, whatever the form says: `Command`, `Args`, `Secrets` (names only, so values are
kept), `Volumes`, `Backup`, ports beyond the first, the app's `Name` and `Source`, the service
list and its order.

Model path → form field, for `Validate` errors: `services[i].image` → `svc.<name>.image`,
`services[i].env…` → `.env`, `services[i].ports…` → `.port`, `services[i].domains…` →
`.domains`, `services[i].replicas` → `.copies`, `services[i].health.path|interval|timeout` →
the matching health field, `services[i].resources.cpuMHz|memoryMB|memoryMaxMB` → `.cpu`,
`.memory`, `.memoryMax`. Anything else → the form's notice, naming the path.

## Compose draft (ui)

Not stored. Fields: `name` (new only), `compose` (the file), `revision` (existing only), and per
service of the preview: `svc.<name>.domains`, `.healthPath`, `.healthInterval`,
`.healthTimeout`, `.backupEngine` (`""` or `postgres`), `.backupSchedule`, `.backupKeep`, and
`svc.<name>.secret.<SECRET>` (password inputs, never echoed). Becomes `compose.Request` with
`Services[name] = Overlay{Domains, Health, Secrets, Backup}`; an empty secret input is
`app.Secret{Name}` (keep the current value).

## Copy (deploy, extended)

`deploy.Copy` gains:

| Field | From |
| --- | --- |
| `LastRestart *time.Time` | newest non-zero `TaskState.LastRestart` over the copy's tasks |
| `OutOfMemory bool` | the service task's newest `Terminated` event has `Details["oom_killed"] == "true"` |

## Copy usage (service)

```go
type CopyUsage struct {
    CPUMHz           int    // used now
    MemoryMB         int    // used now
    CPUReservedMHz   int    // the service's reservation (defaults applied)
    MemoryReservedMB int
    MemoryCeilingMB  int
    NearCeiling      bool   // MemoryMB >= 90% of MemoryCeilingMB
    Error            string // set instead of the numbers when Nomad couldn't give them
}
```

`service.CopiesWithUsage(ctx, appID) ([]Copy, map[string]CopyUsage, error)`: the copies as
`Copies` returns them, and usage for each copy whose status is starting, healthy or unhealthy
(keyed by copy ID). Terminal copies have none.

## Attention item (service, derived)

```go
type AttentionKind string // deploy_failed, out_of_memory, restarting, unhealthy,
                          // backup_failed, restore_failed, no_backup_destination

type AttentionItem struct {
    Kind       AttentionKind
    AppID      string    // "" for no_backup_destination
    Service    string    // "" for deploy_failed and no_backup_destination
    Copies     []string  // the copy IDs it is about (copy kinds)
    Deployment string    // deploy_failed: the deployment ID
    Version    *uint64   // deploy_failed: its job version, when registered
    When       time.Time
    Detail     string    // the raw fact: Nomad's description, the deploy or backup error
}

type Attention struct {
    Items         []AttentionItem // newest first
    CopiesUnknown bool            // Nomad couldn't be read: copy kinds are missing
}
```

Rules (research R9), per app:

| Kind | True while | When | Fix |
| --- | --- | --- | --- |
| `deploy_failed` | latest deployment is `failed` or `reverted` | its `UpdatedAt` | `/apps/<app>/deployments#<deployment>` |
| `out_of_memory` | a non-terminal copy has `OutOfMemory` | `LastRestart` | `/apps/<app>/configure#svc-<service>-memory` |
| `restarting` | a non-terminal copy has ≥ 3 restarts, not out of memory | `LastRestart` | `/apps/<app>/logs?copy=<copy>` |
| `unhealthy` | a non-terminal copy is `unhealthy` and no rollout of the app runs | `StartedAt` | `/apps/<app>/logs?copy=<copy>` |
| `backup_failed` | a service's newest backup failed | its finish time | `/apps/<app>/backups` |
| `restore_failed` | a service's newest restore failed | its finish time | `/apps/<app>/backups` |
| `no_backup_destination` | some service has `Backup` and no destination is set | the newest such app's `UpdatedAt` | `/settings#backup-destination` |

- With a `deploy_failed` item, copies of that deployment's job version add no copy items.
- Copy items of one kind for one service merge into one item (`Copies` lists them; `When` is the
  newest).
- Sorted by `When`, newest first; ties by app ID then service.

The sentence each kind becomes is the console's (view.go), e.g. "blog's deploy v7 was reverted:
the new copies failed their health check." The API sends the facts, not the sentence.

## Change kind (service)

`ChangeAttention = "attention"`, published with `AppID` "" by `deploymentChanged`,
`copiesChanged`, `backupsChanged` and `DeleteApp`.

## New store read

`store.LatestBackupRuns(ctx) ([]BackupRecord, []RestoreRecord, error)`: per (app, service), the
newest backup and the newest restore, in one query each (window over `started_at`).
