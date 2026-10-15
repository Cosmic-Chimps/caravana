# Data model: Keep and edit the Compose file

One migration, two nullable columns. Everything else is a new field on existing records or a
pure function. Decisions and reasons are in [research.md](./research.md).

## Migration `0007_compose_file.sql`

```sql
-- Feature 015: the Compose file an app was made from, kept so it can be edited.

-- The app's kept file: the JSON of the deployCompose request (compose, name,
-- services overlay) as it was last deployed, secret values never stored.
-- NULL when the app wasn't made from a file, or the file was dropped.
ALTER TABLE apps ADD COLUMN compose TEXT;

-- The kept file the app had when this deployment ran, same format. NULL for
-- deployments not made from a file and for those recorded before this feature.
ALTER TABLE deployments ADD COLUMN compose TEXT;
```

Existing rows get NULL. Apps made from Compose before 015 are in the "not kept" state (US2).

## Kept file (`compose.Request`, existing type)

| Field | JSON | Kept as |
| --- | --- | --- |
| `Compose` | `compose` | the file text, byte for byte (comments, order, blank lines) |
| `Name` | `name` | as sent |
| `Services[name].Domains` | `services.<name>.domains` | as sent |
| `Services[name].Health` | `services.<name>.health` | as sent (nil stays nil) |
| `Services[name].Secrets` | `services.<name>.secrets` | **names only**: `Value` is always nil |
| `Services[name].Backup` | `services.<name>.backup` | as sent |

New method, beside the type in `internal/compose`:

```go
// WithoutSecretValues returns the request with every secret's value removed,
// names kept: what Caravana keeps and shows of a request.
func (r Request) WithoutSecretValues() Request
```

It copies the `Services` map and each `Secrets` slice, so the caller's request is never changed.

**Rules**
- Kept only for Compose deploys, and only after `compose.Translate` accepted the file, so a kept
  file is at most `MaxComposeBytes` (512 KiB).
- Secret values are removed twice: by the service before saving, and by the store before writing.
- A kept file is never rewritten by Caravana. Only its overlay's secret names change, on a secret
  edit.

## Store records

```go
type AppRecord struct {
    App       app.App
    Compose   *compose.Request // the kept file; nil when there is none (new)
    CreatedAt time.Time
    UpdatedAt time.Time
    Revision  string           // now covers Compose too (below)
}

type DeploymentRecord struct {
    // ... as today ...
    Definition *app.App
    Compose    *compose.Request // the app's kept file when it ran; nil when none (new)
    Source     string
}
```

**Revision** (changed): `revisionOf(document, compose)` is the first 16 hex digits of SHA-256 over
the document bytes. When `compose` is not NULL, a zero byte and the `compose` bytes follow. For an
app without a file the value equals today's revision.

**Write calls** (signatures change; `nil` means no file):

| Call | Change |
| --- | --- |
| `SaveApp(ctx, a, file)` | upsert writes `compose` too; "identical save changes nothing" compares document **and** compose |
| `SaveAppIf(ctx, a, file, revision)` | same, inside the existing transaction |
| `InsertApp(ctx, a, file)` | writes `compose` |
| `CreateDeployment(ctx, appID, d, definition, file, source)` | writes `deployments.compose` |

Reads (`GetApp`, `ListApps`, `GetDeployment`, `ListDeployments…`, `LatestHealthyDeployment`) scan
the new column. `DeleteApp` is unchanged: the column goes with its row.

## Service

`saveAndDeploy(ctx, a, cond, trigger, made, pin)`: `made` replaces the `warnings` argument and
says what the app was made from:

```go
// made is what an app was made from: the Compose file it keeps (nil for a
// definition) and the warnings that input brought (a Compose file's changed
// meanings), recorded first in the deployment.
type made struct {
    file     *compose.Request
    warnings []app.Warning
}
```

`deployWarned` takes the same `made` and passes `made.file` to `CreateDeployment`. `Redeploy`
passes the stored record's `Compose`. How each public call fills `made` is the table in
research R4.

New warning, added in `saveAndDeploy` when the stored app has a file and `made.file` is nil:

> This app's Compose file was dropped: it was deployed from a definition, not a file.

`GetApp` already returns `store.AppRecord`, so the kept file reaches the API and the console with
no new service read. `GetDeployment(ctx, appID, deploymentID)` (existing) returns the deployment
record with its `Compose`, which is what the download and `getDeploymentCompose` read.

## Console (`internal/ui`)

| Piece | What |
| --- | --- |
| `draftOf(req compose.Request) composeDraft` | pure; the kept request as the Compose form's draft (file, name, overlay, secrets as existing) |
| `configureView.Compose *composeView` | set when the app has a kept file, or is `Source.Kind == compose` without one (then `NotKept: true`, empty file, overlay from `draftFrom`) |
| `configureView.Edit.ReadOnly bool` | true when the app has a kept file |
| `historyEntry.ComposeHref string` | set only for deployments with a kept file |
| route `GET /apps/{id}/deployments/{deploymentId}/compose` | the file text as an attachment |

## State of an app's file

```text
            DeployCompose                    deployApp with a body
 (none) ────────────────────▶ kept(F) ──────────────────────────────▶ (none) + warning
   ▲                          │  ▲  │
   │ RedeployVersion of a     │  │  └─ DeployCompose(F') ─▶ kept(F')
   │ deployment without one   │  └──── SetSecret/RemoveSecret: kept(F, names updated)
   └──────────────────────────┘        Redeploy / webhook: kept(F), unchanged
                                       RedeployVersion of D: kept(D.Compose)
```

Legacy Compose apps start at "(none)" with `Source.Kind == compose`; pasting the file once is a
`DeployCompose`.
