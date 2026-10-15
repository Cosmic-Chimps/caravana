# Research: Keep and edit the Compose file

No unknowns were left in the spec. The decisions below settle how the file is kept, how every
path that changes an app treats it, and how the console and the API show it.

## R1. Where the kept file lives

**Decision**: a new nullable column `compose` on both `apps` and `deployments` (migration
`0007_compose_file.sql`). It holds the JSON of `compose.Request` (`compose`, `name`, `services`
overlay) exactly as the API's `deployCompose` body takes it, with every secret value removed.
NULL means no kept file. In Go it is `Compose *compose.Request` on `store.AppRecord` and
`store.DeploymentRecord`.

**Rationale**: the overlay is kept in the request's own shape (spec assumption), so a client can
`GET` it and send it back unchanged, and the console can rebuild its form from it. A column per
table follows feature 011's `deployments.definition`: one row per thing, gone with the row, so
deleting an app (FR-011) deletes its files with no new code. The file text is a JSON string, so
comments, blank lines and key order are kept byte for byte.

**Alternatives considered**:
- Inside `app.App` (for example `Source.Compose`). Rollback would have carried it for free, but
  the raw input would then flow into `Translate`, export, every `App` in the API, and the
  revision of image apps' documents. `app` also can't import `compose` (compose imports app), so
  the overlay would need a second copy of its types. Constitution II wants the model to be the
  normalized app, not its input.
- A separate `compose_files` table keyed by content hash, deduplicated across deployments.
  Smaller on disk when a file is redeployed often, but it needs reference counting on delete.
  Files are small (Hollo's is 2 KiB, the cap is 512 KiB), so this is a cost with no benefit yet.

## R2. The revision covers the file

**Decision**: `revisionOf` hashes the document, and, when there is a kept file, a zero byte and the
`compose` column after it. For an app with no kept file the revision is exactly what it is today.
`SaveApp`'s "an identical save changes nothing" check, and `SaveAppIf`'s `UPDATE … WHERE`,
compare both columns.

**Rationale**: an edit that only changes a comment is a real change of what the owner edits (spec
edge case: "the stored file keeps the new text"), so a second tab holding the old file must get
the stale refusal (US1-4). Apps without a file keep their revision across the upgrade, so no open
form and no API client gets a surprise 412.

**Alternatives considered**: a revision over the document only. A comment-only edit from one tab
would then be silently lost to another tab's save (SC-004).

## R3. Store API

**Decision**: the write calls take the kept file next to the app:

- `SaveApp(ctx, a, file *compose.Request)`
- `SaveAppIf(ctx, a, file, revision)`
- `InsertApp(ctx, a, file)`
- `CreateDeployment(ctx, appID, d, definition, file, source)`

`nil` means no file. The store drops secret values again before writing
(`file.WithoutSecretValues()`), as `CreateDeployment` already does for the definition. The store
imports `internal/compose` only for the type.

**Rationale**: the app and its file are written in one statement, so the revision always matches
what was saved, and no path can save one and forget the other. The signature change makes the
compiler find every caller: about 70 test call sites that pass `nil`.

**Alternatives considered**: a separate `SetAppCompose` call. That is a second write that can
fail or race between the app save and the deploy, and every caller would have to remember it.

## R4. What every path does with the kept file

The service decides; the store only writes what it is given. `saveAndDeploy` gains the file the
app is made from, and `deployWarned` passes it to `CreateDeployment`.

| Path | Kept file afterwards | Deployment's file |
| --- | --- | --- |
| `DeployCompose` / `DeployComposeIf` (console and API) | the request, secret values removed | same |
| `Deploy` / `DeployIf` with a definition (`deployApp` with a body, the console's per-field form) | none; warning if there was one (R5) | none |
| `CreateApp` (image) | none | none |
| `SetSecret` / `RemoveSecret` | the stored file, with that service's overlay `secrets` set to the new names | same |
| `Redeploy` (webhook, Deploy button, `deployApp` without a body) | unchanged (the app isn't saved) | the stored file |
| `RedeployVersion` of a deployment with a file | that deployment's file | same |
| `RedeployVersion` of a deployment without one | none; warning if there was one | none |
| Resume after restart, backups, restores | unchanged | untouched |

Each deployment records the app's kept file at the time it ran. So a webhook redeploy of a
Compose app also records the file, and rolling back to it brings the file back (US3).

**Rationale**: a kept file is never out of step with the app (SC-004). Every way of changing the
app either writes a matching file or writes none with a warning. Secret edits are the one case
where the app changes and the file must follow, and only the overlay's names change, never the
file text.

**Secret edit detail**: `editSecrets` reads the record, changes the app as today, and, when there
is a kept file, sets `file.Services[service].Secrets` to the new list (names only), adding the
service's overlay entry if it had none. It then deploys through `saveAndDeploy` with that file
instead of `Deploy`.

## R5. The "file dropped" warning

**Decision**: `saveAndDeploy` compares the stored record with the incoming file. When the stored
app has a file and the incoming one is nil, it appends
`app.Warning{Message: "This app's Compose file was dropped: it was deployed from a definition, not a file."}`,
next to the existing `RemovedSecrets` and `unmountedVolumes` warnings. A rollback to a version
without a file gets the same sentence (FR-006 points at FR-009's warning).

**Rationale**: one sentence, one place, and it covers both FR-006 and FR-009. The warning has no
`field`: no request field is at fault.

## R6. Configure, per kind of app

**Decision**:

| App | Configure shows |
| --- | --- |
| Has a kept file | A "Compose file" section on top: the `compose-form` template from 014 (file textarea, name, overlay per service, Preview, Deploy), prefilled from the kept file (R7) and carrying the revision. Below it, the definition as read-only panels with the line "This app is edited through its Compose file." Secrets, webhook, export and delete stay as they are. |
| Made from Compose (`Source.Kind == compose`), no kept file | The note "This app's Compose file wasn't kept. Paste it once to edit it here from now on." with an empty file textarea and the overlay prefilled from the app (`draftFrom`, 014). The per-field form stays editable (US2-3). |
| Image app | Unchanged from 014 (per-field form, link to "Deploy from Compose"). |

Preview and Deploy post to the existing `POST /apps/{id}/compose/preview` and
`POST /apps/{id}/compose`. Their answer is the existing Deploy from Compose page, with the draft
as typed. `GET /apps/{id}/compose` is prefilled the same way, so either entry point shows the
same form. `POST /apps/{id}/configure` on an app with a kept file answers 409 with the
"edited through its Compose file" notice and deploys nothing. Only a hand-made post can reach it:
a stale tab opened before the file was kept already fails the revision check.

**Rationale**: no new form code: 014's draft, preview, refusals, stale handling and the "services
added and removed" sentences all work as they are. Plain HTML posts, no JavaScript (FR-012).

**Editor**: the existing `<textarea>` with `font-mono`, `spellcheck="false"`, `wrap="off"`,
`autocapitalize="off"`, and rows sized to the file (bounded 12–40). The hint under it says to put
passwords and keys in secrets, not in the file. No syntax highlighting (no Node build step).

## R7. Prefilling the form from a kept file

**Decision**: a new pure `draftOf(req compose.Request) composeDraft`, the inverse of
`composeDraft.request()`. It sets the file text, the name and each service's overlay inputs, and
lists secrets as existing with empty values. `keepStored(stored app.App)` then runs as in 014, so
any secret the app has and the overlay lacks is still shown and kept.

A round-trip test checks that `request(draftOf(r))` equals `r` (with secret values removed) for
each example pair in `internal/compose/testdata/` (`*.compose.yaml` plus `*.overlay.json`), for
Hollo (`deploy/examples/hollo/compose.yaml` plus `overlay.example.json`), and for a request built
by the form itself. Durations already show
as decimal seconds (`secondsText`), so a sub-second API value round-trips.

**Rationale**: the owner sees what they (or their client) sent, not what Caravana derived. For
example, no health check stays empty instead of showing default intervals. `draftFrom(app)` stays
for apps without a file.

## R8. History: downloading a deployment's file

**Decision**: console route `GET /apps/{id}/deployments/{deploymentId}/compose` answers the
deployment's file text, byte for byte, as
`Content-Type: application/yaml; charset=utf-8` with
`Content-Disposition: attachment; filename="<app>-<deployment>.compose.yaml"`. A deployment
without a file is 404. Each history entry with a kept file shows "Download Compose file" next to
"Export jobspec"; other entries don't (FR-010). The overlay isn't in the download: it lives in the
API answer (R9) and comes back on rollback.

**Rationale**: "the file, byte for byte as deployed" (US3-2) is a YAML file the owner can run with
`docker compose`. Export's download pattern (feature 011) already does this for jobspecs.

## R9. API

**Decision** (`api/openapi.yaml` 0.11.0 → 0.12.0, additions only):

- `GET /v1/apps/{id}/compose` (`getCompose`): the kept `ComposeRequest` (the `deployCompose` body
  schema), secret values absent, with `ETag: "<revision>"`, so a client can GET, edit and POST
  with `If-Match`. App without a file: 404 `compose_not_kept`.
- `GET /v1/apps/{id}/deployments/{deploymentId}/compose` (`getDeploymentCompose`): that
  deployment's `ComposeRequest`, or 404 `compose_not_kept`.
- `AppRecord` and `AppSummary` gain `composeKept` (boolean). `Deployment` gains `composeKept`.
- `deployApp` with a body documents the "file dropped" warning, and `redeployDeployment`
  documents that the version's file comes back.

The parity table in `internal/api/parity_test.go` adds `getCompose` to `GET /apps/{id}/configure`
and `GET /apps/{id}/compose`, and maps the new download route to `getDeploymentCompose`.

**Rationale**: JSON in the request's own shape is the one representation a client needs: the file
is a field of it, and so is the overlay. A `text/plain` variant would be a second way to read the
same thing. `GET` on the path that already takes `POST` follows REST and generated clients.

**Alternatives considered**: putting the file inside `getApp`. That makes every app read up to
512 KiB heavier, including `listApps` through `AppSummary`.

## R10. Size

**Decision**: no new limit. `compose.Translate` already refuses files over `MaxComposeBytes`
(512 KiB), and the request body cap is 1 MiB (`maxBodyBytes`). The spec's "at most 1 MiB" is the
body; the file inside it is at most 512 KiB, so that is the largest kept file.

## R11. Secret values never kept or shown

**Decision**: `compose.Request.WithoutSecretValues()` (new, beside the type) is applied by the
service before saving and again by the store before writing. The views reach a kept file only
through `store` records, so they never see a value. One test per path that shows a file posts a
sentinel secret value and asserts the bytes never appear: the Configure page, the Deploy from
Compose page, the download, `getCompose`, `getDeploymentCompose` and `listDeployments`
(SC-003, FR-002).

Plain env values written in the file are shown, as the per-field form shows env since 014. The
editor hint points passwords at secrets.

## R12. Lab and gate

**Decision**: `TestConsoleLab` gains a step "compose-file-edit": create a one-service Compose app
with a comment in the file, open Configure (comment present), change the image tag, preview,
deploy, wait healthy, and check that Configure shows the edited file. Then roll back to the first
deployment, check that Configure shows the first file, and download it. No new gate step and no
Nomad capability: this is store and console only, and the deploy path is the gate's
`compose-app`.
