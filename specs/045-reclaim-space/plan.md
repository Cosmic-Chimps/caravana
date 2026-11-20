# Implementation Plan: Reclaim space (unused images and orphan volumes)

**Spec**: [spec.md](./spec.md) · **Research**: [research.md](./research.md) · **Contracts**: [contracts/agent.md](./contracts/agent.md), [contracts/api.md](./contracts/api.md) · **Tasks**: [tasks.md](./tasks.md)

## Summary

Each managed server's agent reports, with every sync, what Docker holds (images with their digests and whether a
container uses them, Docker's dangling volumes, the build cache) and how big each Nomad host volume directory is.
Caravana turns that into a **Space** read per server: unused images by its own keep rule (the digests of each
app's last N healthy deployments, every deployment under way, every stored definition, its helper images), Docker's
leftovers, the build cache and the **orphan volumes** (Nomad host volumes no stored app declares). A **clean-up**
is a record handed to the agent in its sync answer with the keep list; the agent removes what no container uses
and isn't kept, and reports the outcome in its next sync. One clean-up per managed server runs daily at 04:00 UTC
(a setting, on by default) and on "Clean up now". An orphan volume is deleted through Nomad only after its name
is typed, optionally archived to the backup destination first by a one-off batch job that mounts it. Migration
**0035**, API **0.50.0**, the sync protocol gains one report block and one answer block (both optional, so an
older agent or console still works), no new Go module, no runtime dependency, no policy change, constitution
unchanged (the archive job is a helper job named here: `<volume>_archive`).

## Constitution Check

- **I Small**: no module, no service; the agent that exists does the host work. ✓
- **II One model**: no change to `app.App`; one new helper job beside an app's jobs, `<app>_<volume>_archive`,
  pure in `nomadjob` with a golden. ✓ (This plan names it, as II asks.)
- **III Safe deploys**: untouched; the keep rule exists so a rollback never waits on a pull. ✓
- **IV Fail loudly**: the dropped-volume warning (016) now says where the data is and where to delete it; a
  deletion refused by Nomad is reported with the reason, never swallowed. ✓
- **V API-first**: every read and action is an API operation in the same commit; the console calls
  `internal/service` only. ✓
- **VI Nomad-native**: volumes are listed and deleted through `HostVolumes()`; the archive is a Nomad batch job;
  Docker is touched only by the agent on the host, never by a job with the socket (research R6). ✓
- **VII Secure**: the agent applies nothing but validated values (a request ID and digests); the destination's
  keys go in the archive job's own variable as the backup jobs do; no secret in any record. ✓
- **VIII Plain Go**: table tests for the pure rules (keep list, orphan classification, the agent's plan), a
  golden for the archive job, the fake Nomad, a console step, a gate step, an `install-test.sh` check. ✓

No Complexity Tracking entry.

## Decisions

**D1 Store (migration `internal/store/migrations/0035_reclaim_space.sql`; 0034 is taken by the unmerged 044
branch, and the runner applies by version so a gap is fine).**
- `cleanup_settings` (one row, `id = 1`): `automatic INTEGER NOT NULL DEFAULT 1`, `keep_deployments INTEGER NOT
  NULL DEFAULT 3 CHECK (keep_deployments BETWEEN 1 AND 10)`, `last_automatic_day TEXT NOT NULL DEFAULT ''` (the
  UTC date the daily run was last requested on, so a restart after 04:00 doesn't request twice).
- `image_cleanups`: `id TEXT PRIMARY KEY` (32 hex), `server_idx INTEGER NOT NULL`, `server_name TEXT NOT NULL`,
  `trigger TEXT NOT NULL CHECK (trigger IN ('owner','schedule'))`, `docker_volumes INTEGER NOT NULL DEFAULT 0`,
  `requested_at TEXT NOT NULL`, `started_at TEXT`, `finished_at TEXT` (nine fraction digits, NULL until set),
  `status TEXT NOT NULL CHECK (status IN ('requested','running','done','failed','lost'))`, `images_removed`,
  `bytes_freed`, `volumes_removed` (`INTEGER NOT NULL DEFAULT 0`), `error TEXT NOT NULL DEFAULT ''`. Index on
  `(server_idx, requested_at)`.
- `volume_deletions`: `id TEXT PRIMARY KEY`, `volume TEXT NOT NULL` (the Nomad name `<app>_<volume>`), `app_id
  TEXT NOT NULL DEFAULT ''` (the parsed app, which may no longer exist: no foreign key), `server_name TEXT NOT NULL
  DEFAULT ''`, `back_up_first INTEGER NOT NULL DEFAULT 0`, `trigger TEXT NOT NULL DEFAULT 'owner'`,
  `requested_at TEXT NOT NULL`, `finished_at TEXT`, `status TEXT NOT NULL CHECK (status IN
  ('archiving','deleting','done','failed'))`, `archive_object TEXT NOT NULL DEFAULT ''`, `bytes INTEGER NOT NULL
  DEFAULT 0` (the size last measured, 0 when unknown), `error TEXT NOT NULL DEFAULT ''`.
- Store functions (`internal/store/space.go`): `CleanupSettings`, `SaveCleanupSettings`, `CreateImageCleanup`,
  `OpenImageCleanup(serverIdx)` (requested or running), `StartImageCleanup(id, at)`, `SettleImageCleanup(id,
  status, at, removed, bytes, volumes, error)`, `ListImageCleanups(serverIdx, limit)`, `GetImageCleanup(id)`,
  `OpenImageCleanups()` (for the pass), `CreateVolumeDeletion`, `SettleVolumeDeletion`, `GetVolumeDeletion`,
  `OpenVolumeDeletions()` (settled as failed at start: "Caravana restarted during it").
- The feed (`store/activity.go`): two more `UNION ALL` legs: `'image_cleanups'` (rows that are done, failed or
  lost; `service` = server name, `status`, `trigger`, `exit_code` = images_removed, `version` = bytes_freed,
  `error`, `detail` = volumes_removed as text) and `'volume_deletions'` (rows done or failed; `app_id`, `service`
  = server name, `label` = volume, `version` = bytes, `error`, `detail` = archive_object). With `serverOnly`
  both are kept (they are about a server); `rowIsOfServer` matches their `Service` to the server's name.

**D2 The agent (`internal/nodeagent/space.go`, contract [agent.md](./contracts/agent.md)).**
- `Report.Space *SpaceReport` (omitempty): `images` (each: `id`, `digests` (the `sha256:…` parts of
  `RepoDigests`), `tags`, `bytes`, `inUse`), `imagesTruncated` (over 1000), `dockerVolumes {count, bytes}`,
  `buildCacheBytes`, `volumes` (each: `id` = directory name, `bytes`), `volumesMeasuredAt`, `lastClean`
  (`id`, `startedAt`, `finishedAt`, `imagesRemoved`, `bytesFreed`, `volumesRemoved`, `error`). Gathered by
  `a.space(ctx, st)` from the Runner (research R4); a Docker that doesn't answer leaves `Space` nil (the console
  then says "no inventory yet"). The volume directories are measured (`du -sk <root>/opt/nomad/data/host_volumes/<name>`,
  names checked against `^[A-Za-z0-9-]{1,64}$`) when `state.VolumesMeasuredAt` is over 15 minutes old or a
  clean just ran; the sizes are kept in `state.json` (`volumeSizes`, `volumesMeasuredAt`) between syncs.
- `Answer.Clean *CleanRequest` (`id`, `keep []string`, `dockerVolumes`), validated in `validateCommon`: `id`
  32 lowercase hex, `keep` at most 4096 entries each `^sha256:[0-9a-f]{64}$`; a bad one refuses the whole answer
  as §5 does. `applySync` step 4, last: when `ans.Clean != nil && ans.Clean.ID != st.LastClean.ID`, run
  `a.clean(ctx, ans.Clean)` under a 10-minute context, write the outcome to `state.LastClean` (the ID first, so a
  crash mid-run never repeats it; its outcome then reads "interrupted"), print one line.
- `clean`: `docker system df` before; the inventory; every image with `!inUse` and no digest in `keep` is
  removed by its references (or its ID when it has none); `docker builder prune -af`; `docker volume prune -f`
  when asked; `docker system df` after; `bytesFreed` is the sum of the per-type `Size` differences (research
  R4), `imagesRemoved` the count that succeeded, `error` the first failure's text through `clean()` cut to 200
  characters (a Docker message names an image at most, never a secret). The plan of what to remove is the pure
  `removable(images, keep) []image`, table-tested.
- `caravana node space` prints the report's space block as JSON (debugging on a host; no new unit).

**D3 Service (`internal/service/space.go`, `cleanups.go`, `orphans.go`).**
- `keepDigests(ctx)`: pure core `keepFrom(apps []store.AppRecord, deps map[string][]store.Deployment, helpers []string, n int) []string`:
  per app the newest `n` deployments with status healthy and a recorded definition (the definition's images are
  `repo@sha256:…`: take the part after `@`), every deployment pending or running, every stored app's current
  definition's pinned images (only those with a digest), plus `helpers` (the digests of `nomadjob.Settings`'
  names, backup and sqlite images). Sorted, unique. One store read per app (`ListDeployments`, newest first) is
  fine for hundreds of apps once a day. Capped at 4096: over it the clean-up is refused with `ErrKeepListTooLong`.
- `NodeSync`: after the report is stored, `s.cleanupHandoff(ctx, row, report)`: (a) a report whose
  `space.lastClean.id` equals this server's open cleanup settles it (`done`, or `failed` with the error;
  `finished_at` = the agent's `finishedAt` when it parses, else now) and publishes `ChangeServers`; (b) an open
  cleanup still `requested` is marked `running` now; (c) while a cleanup is `requested` or `running` and not
  yet reported, the answer carries `Clean{ID, Keep, DockerVolumes}` (the keep list is computed here, under the
  server lock, at most once per sync). The report's space block is validated in `validateReport` (counts and
  text sizes; a bad block is a field error like any other).
- `RequestCleanup(ctx, serverName, trigger, dockerVolumes) (store.ImageCleanup, error)`: the server must be
  managed with a report that has `space` (`ErrNoAgent`), no open cleanup (`ErrCleanupRunning`), the keep list
  computable; writes `requested`. `cleanupPass` (every minute from `Service.Start`, `SetCleanupPassGap`):
  `requested` older than 10 minutes → `failed` "the server's agent didn't pick it up (is caravana-node.timer
  running?)"; `running` older than 30 minutes → `lost`; then, when `automatic` and the UTC hour is 4 or later and
  `last_automatic_day` isn't today, request one per ready managed server with a space report (skipping those
  with an open one) with trigger `schedule`, writing today's date first.
- `ServerSpace(ctx, name) (SpaceView, error)`: the server (managed or not; `ErrServerNotFound`), `Images` from
  the report's inventory against `keepDigests` (nil with `Reason` "no agent" / "no inventory yet"),
  `DockerVolumes`, `BuildCacheBytes`, `Orphans` on this server's node (`deploy.AllHostVolumes` lists every
  dynamic host volume, `HostVolumes().List` with no prefix; the stub has `NodeID`; `classifyOrphans(stubs,
  apps)` is pure: a volume is orphan when no stored app's `nomadjob.VolumeName(app, v)` equals it; the app is the
  part before the first `_` when `app.PlaceOf` reads it, else ""; for each orphan one `Get` for `Allocations`
  (in use) and the size from the report's `volumes` by ID; "last deployed with it" is the newest deployment of
  that app whose definition declares the volume, read from `ListDeployments`, or "its app was deleted"),
  `Reclaimable` = unused images + Docker volumes + build cache + orphans with a size, `LastCleanup` (the newest
  record), `Open` (the open one), `Settings`.
- `DeleteOrphanVolume(ctx, name, confirm string, backUpFirst bool, trigger) (store.VolumeDeletion, error)`:
  `confirm != name` → `*app.FieldError` on `confirm`; the volume must exist (`ErrVolumeNotFound`), be orphan
  (`ErrVolumeNotOrphan`), have no allocations (`ErrVolumeInUse`); with `backUpFirst` the destination must be set
  (`ErrNoBackupDestination`, the existing sentinel). Without backup: delete through
  `deploy.DeleteHostVolume(ctx, id)` (a refusal "in use" is `ErrVolumeInUse`), record `done`. With backup:
  record `archiving`, answer, and a goroutine runs the archive (D4) then deletes; a failure settles `failed` with
  the volume untouched. One deletion at a time per volume (`ErrDeletionRunning`, a map under `spaceMu`).
- `CleanupSettings` / `PutCleanupSettings(automatic bool, keep int)`.
- Activity words: "Cleaned up server one: removed 7 images, freed 1.2 GB" (healthy; `failed`/`lost` are
  failed marks with the error), "Deleted the volume shop_uploads of app shop (2.3 GB), archived to
  shop/volumes/…" (`ActivityServer` kind for both; a deletion keeps its app for the link). `Service.Activity`'s
  `Server` filter keeps both.
- `unmountedVolumes` (016) now reads: `volume %q is no longer mounted: its data stays on server %s until you
  delete it under Operator › Servers` (the server from `HostVolumes` when known, else "its server").
- The disk item's link: `/ops/servers/<name>#space` when the item names a server, else the local server's page
  with `#space`.

**D4 The archive job (`nomadjob/archive.go`, pure, golden `volume-archive.golden.json`).**
`VolumeArchiveJob(volume, appID, backupPath, ts string, s Settings) *api.Job`: batch job `<volume>_archive`,
one group `archive`, `Volumes: {"data": {Type: "host", Source: volume}}` with the volume's own capability
(single-node-single-writer, file-system: an orphan has no writer, and the plugin declares no reader-only mode),
one task `archive` with `BackupImage`, the mount at `/data`, `variableTemplate(jobID)` for the destination
(`RCLONE_CONFIG_DEST_*` and `BACKUP_PATH`, the items `destinationItems` writes), and `ArchiveScript`: `tar -czf -
-C /data . | rclone rcat dest:$BACKUP_PATH/$TS/<volume>.tar.gz`, then `rclone size --json` for the bytes, printing
`caravana-archive: ok object=… bytes=…` or `caravana-archive: failed: …` (the markers `batchOutcome` reads;
reuse its pattern with the task name). `BACKUP_PATH` is `<app>/volumes` for a parsed app, else `volumes`. In
`deploy`: `ArchiveVolume(ctx, volume, appID string) (object string, bytes int64, err error)`: write the job's
variable (destination items with `BACKUP_PATH`), register, wait for the single allocation to end (the
`PollGap`, 30 minutes at most), read the markers from the task's output, purge the job and delete the variable,
return. A volume on a down node: Nomad leaves the job pending; the wait ends `ErrNomadUnavailable` with "the
server that holds the volume isn't placing work".

**D5 API (contract [api.md](./contracts/api.md)), 0.50.0.** `getServerSpace`, `cleanServer` (202),
`listServerCleanups`, `getServerCleanup`, `deleteOrphanVolume` (`POST /v1/volumes/{name}/deletions`, 200
done or 202 archiving), `getVolumeDeletion`, `getCleanupSettings`, `putCleanupSettings`. Codes `no_agent`,
`cleanup_running`, `keep_list_too_long`, `volume_not_found`, `volume_not_orphan`, `volume_in_use`,
`deletion_running`; `no_backup_destination` exists. Deploy tokens: nothing added to `scopeRules`. MCP: one read
tool `get_server_space`.

**D6 Console.** The server page gains a section **Space** (`id="space"`, after the numbers panel, before Running
here): a line per kind (unused images with count and size, Docker's leftover volumes, build cache, each "—" with
the reason without an agent), the reclaimable total in the section's label, the last clean-up's sentence
("Cleaned up 2 hours ago: removed 7 images, freed 1.2 GB" / its failure) and "Clean up now…", which goes to
`GET /ops/servers/{name}/clean` (what will be removed, the "Also remove Docker's leftover volumes" box, Confirm
→ `POST /ops/servers/{name}/clean` → 303 to the page with `?cleaning=<id>`; a refusal draws the page again with
the reason); the orphan volumes as rows (name in mono, "from app shop (production)" or "its app was deleted",
size or "not measured yet", "last deployed with it Oct 12", "mounted by a running copy" instead of the action,
"Delete…" → `GET /ops/servers/{name}/volumes/{volume}/delete`: the name typed, the "Back up first" box when the
destination is set, Confirm → `POST` → 303 with `?deleted=<volume>` or `?deleting=<id>`); and the settings form
(keep 1–10, automatic on/off, Save → `POST /ops/cleanup/settings` → 303 back). The section is its own part,
`/parts/ops/servers/{name}/space`, refreshed on the `servers` notice (the sync publishes it), so a clean-up's
end appears by itself. Overview's server panels get one line "Reclaimable 1.5 GB" (link to `#space`) when the
space read has a total. The Volumes panel of an app (016) links its unmounted rows to the same delete page.
Forms work without JavaScript.

**D7 Tests and the lab.**
- Unit: `removable` and the inventory parsing with recorded Docker outputs (`nodeagent/testdata/docker/*`),
  `ParseSync` refusals for `clean`; `keepFrom`, `classifyOrphans`, the pass's three rules with a fake clock;
  the store round-trips; the activity words; the archive job's golden; the API's answers and refusals; the
  scope walk (the new routes denied); the console pages.
- `TestConsoleLab` step `console-space` (`cmd/caravana/console_space_test.go`): an app with two volumes, a
  redeploy with one, the warning names the server, the server page's Space section lists the orphan, the delete
  page refuses a wrong name and deletes with the right one, the feed has the entry, the other volume's data is
  still served, the settings form.
- Gate step `space` (`gate_space_test.go`): the same through the API plus `backUpFirst` against the gate's
  bucket job (the object listed afterwards), the refusals (`volume_not_orphan`, `volume_in_use` after a reverted
  deploy that dropped it, `no_agent` for Clean up now, `volume_not_found`).
- `install-test.sh` `check_space_host`: after the host exists, four deploys of busybox by tag (four tags the
  registry has; a sleep command), Nomad restarted (`systemctl restart nomad`), the first image present
  (`docker image ls --digests`), `POST …/cleanups`, the run `done` within 3 minutes, the first image gone, the
  other three and traefik present, `GET …/space` says 0 unused images; `redeployDeployment` of the second
  version healthy with the image list unchanged (no pull happened).

## Deviations

Recorded here as the work finds them.
