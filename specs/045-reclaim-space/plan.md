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
unchanged (the archive job is a helper job named here: `<volume>_archive`). Other facts:
- The archive job, `<app>_<volume>_archive` (the volume's own Nomad name plus `_archive`), is a one-off batch job
  with one task, `archive`, on the backup image: it mounts the volume at `/data` read-only, streams `tar | rclone rcat`
  to `<bucket>/<prefix>/<app>/volumes/<ts>/<volume>.tar.gz` (`…/volumes/<ts>/…` when the name doesn't read as an
  app's) and prints `caravana-archive: ok object=… bytes=…` or `caravana-archive: failed: …`. The destination's keys
  are only in the job's own Nomad Variable `nomad/jobs/<job>`; the job and the variable are removed when the archive
  ends, whatever happened.

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

**Phase A (agent, store, service core), 2026-11-20.**

- **Agent answer limit.** `clean.keep` of 4096 digests is about 300 KiB of JSON, over 031's 256 KiB limit on what the
  agent reads of the console's answer, so `maxAnswer` is now 512 KiB (`internal/nodeagent/validate.go`; 031's `node.md` §5
  says so). The 256 KiB body test now sends 600 KiB.
- **Sync body limit.** The report names up to 1000 images, which the console's node calls (64 KiB) and its generic JSON
  reader (1 MiB) refused, so the whole sync would have failed on a busy host. `internal/api` is otherwise Phase B's, but two
  small edits were needed for the protocol to work at all: `POST /v1/node/sync` accepts 4 MiB (`decodeNodeJSONMax`,
  `decodeJSONLimit` in `decode.go`; join and certificate keep 64 KiB), and its answer carries `clean`
  (`nodecalls.go`). The agent also keeps its own space block under 2 MiB, dropping the smallest images (`maxSpaceJSON`).
  `api/openapi.yaml` still says "bodies at most 64 KiB" and has no `space` or `clean`: T008 must add both
  (`NodeReport.space`, `NodeSyncAnswer.clean`, the 4 MiB), since `NodeReport` is `additionalProperties: false`.
- **Agent details.** The method is `a.clean` as planned, beside agent.go's text cleaner `clean` (a method and a function
  don't clash). `ExecRunner.WithTimeout` gives `du` (2 min) and a clean-up's Docker commands (5 min each) more than the
  30 s of an ordinary call; a fake runner just ignores it. `bytesFreed` sums the Images and Build Cache sizes (and Local
  Volumes only when Docker's volumes were pruned), not Containers, which grow on their own. Step 4 runs after a failed
  renewal (its error is returned after), and not after a promotion (that sync returns first; the answer repeats until
  it runs). `docker image inspect` tolerates an image that vanished between the list and the inspect; the container
  reads are strict, since an unread container would make its image look unused. `caravana node space` doesn't write
  state.json (it measures the volume directories into memory only).
- **Store.** `image_cleanups.server_idx` references `servers (idx) ON DELETE CASCADE`, as `server_moves` does, so a
  removed server's history goes with it and a reused index never inherits it; a partial unique index
  (`image_cleanups_one_open`) makes "one open per server" atomic (`store.ErrCleanupOpen`). Added beyond D1's list:
  `SetLastAutomaticDay`, `SetVolumeDeletionDeleting`, `ListHealthyDeployments` (the newest healthy deployments with a
  definition, so the keep rule doesn't read every deployment of an app). `TestUpgradeTo0033` ran "the rest of the
  migrations" and counted them; it now stops at 33.
- **Service.** `keepFrom` takes `map[string][]store.DeploymentRecord` (there is no `store.Deployment`) and returns
  `([]string, error)`, `ErrKeepListTooLong` when over 4096; it reads each deployment's recorded `Digests` as well as its
  definition's images. A report older than 10 minutes counts as "no agent" for `RequestCleanup` and the daily run
  (`agentReporting`). `cleanupHandoff` never fails the sync: what goes wrong is logged and the answer has no `clean`.
  `DeleteOrphanVolume` takes the trigger as `app.Trigger` and stores its word; it records the row as `deleting` before
  asking Nomad and settles it after, so a crash leaves a row `failInterruptedDeletions` closes at start. With `backUpFirst`
  it returns `ErrNoBackupDestination` for now, marked `// Phase D` (T012). `deploy.HostVolumeDetail` returns a
  `VolumeDetail`; `DeleteHostVolume` counts a volume that is already gone as deleted, as `DeleteVolumes` does.
  `humanSize` in `activity.go` repeats the console's `humanBytes` rule, which this package can't import.
- **Disk item's link.** It reads "Free up space" and goes to `/ops/servers/<name>#space` (the item's server, else
  `Service.LocalServerName()`, a new accessor over the sampler's directory); with no local name known yet it keeps the
  old "Open monitoring" link. `platform_test.go` and `opsserver_states_test.go` were updated. The `#space` anchor is
  Phase C's.

**Phase B (API and MCP), 2026-11-20.**

- **Router conflict.** `GET /v1/servers/{name}/space` and `GET /v1/servers/{name}/cleanups` conflict in Go's
  `ServeMux` with 031's `GET /v1/servers/joins/{id}` (both match `/v1/servers/joins/space`, neither is more
  specific, and registering them panics; the console met the same thing at `/ops/servers/joins/…`). The
  routes table and `api/openapi.yaml` keep each operation at its contract path (so the contract test and the
  scope walk are untouched); `handler` registers the two under one pattern, `GET /v1/servers/{name}/{sub}`
  (`serverSubRoutes`, `serverSubPattern` in `api.go`), of which `joins/{id}` is the more specific, and
  dispatches on the last segment. Any other sub-path answers the usual 404 `not_found`; `/v1/servers/joins/space`
  still reaches `getServerJoin` (404 `server_not_found`). Tests: `TestServerSubroutesKeepTheirAnswers`.
- **Extra fields beyond `contracts/api.md`**, all additive: `ServerSpace.orphanVolumesUnavailable` (Nomad couldn't
  be read, so the empty list isn't an answer) and `SpaceImages.truncated` (the agent named only the largest 1000
  images). `OrphanVolume.project` and `.environment` are the slugs of the app's place (`default`, `production`),
  not display names, and are `""` when `app` is; `measuredAt` is `null` while `bytes` is. An orphan has no
  `server` field (it is listed under its server's page); `VolumeDeletion.server` names the server that held it.
- **Error fields.** `server_not_found` has no `field` (as 031's other server calls); `no_agent`,
  `cleanup_running`, `volume_not_orphan`, `volume_in_use`, `deletion_running` and the two `*_not_found` codes carry
  the service's field (`name`, or `id` for an unknown clean-up or deletion). `no_backup_destination` is the
  existing code. `putCleanupSettings` requires both fields (a missing one is 400 `invalid_field`, not "off").
  `deleteOrphanVolume` records trigger `manual`; the API has no console cookie to tell otherwise.
- **`api/openapi.yaml` 0.50.0**: the eight operations (tag `servers`), schemas `ServerSpace`, `SpaceImages`,
  `OrphanVolume`, `Cleanup`, `VolumeDeletion`, `CleanupSettings`, and for the node protocol `NodeSpace`
  (`NodeReport.space`) and `NodeClean` (`NodeSyncAnswer.clean`), with the sync's body limit corrected to 4 MiB
  (413 text too). The join and certificate calls keep 64 KiB.
- **Test fake.** `spaceHost` (cleanup_agent_test.go) now answers `docker volume prune`, for the clean-up with
  `dockerVolumes: true`.
- **MCP.** `get_server_space` takes `server` (a server's name, the same lowercase-label rule as the agent's, with
  its own `serverPattern`: the shared `namePattern` refuses a name with `--`, which a host name can make).

**Phase C (console), 2026-11-20.**

- **Router conflict, again.** `GET /parts/ops/servers/{name}/space` conflicts with 031's `GET /parts/ops/servers/joins/{id}`
  (both match `/parts/ops/servers/joins/space`), as Phase B's API routes and 031's pages did. One pattern,
  `GET /parts/ops/servers/{kind}/{rest}`, takes both and `partServerSub` dispatches (`joins` is the join's part, `space` the
  section); the clean-up page joins `showServerSub` (`.../{name}/clean`). The parity table lists the new pattern in place of
  `GET /parts/ops/servers/joins/{id}`. The delete page (`/ops/servers/{name}/volumes/{volume}/delete`, four segments) and
  the posts don't conflict.
- **The section is drawn inside the body.** `id="space"` is in `srv-body`, between the numbers (and the room line) and
  Running here, from the same loader as the page, so a load and a refresh can't differ; the section also carries its own
  `hx-get` of `/parts/ops/servers/{name}/space` on `sse:servers`, swapped as `outerHTML`. The body refreshes on that
  notice too, so a clean-up's end reaches the page either way. The settings form has `hx-preserve` and an id, like the Labels
  form. A server that is down or disconnected has no section (Docker can't be asked), and a Nomad node that isn't a server
  row (the lab's) has one, with "—" and the agent sentence.
- **Redirects carry `#space`.** The three posts answer 303 to `?cleaning=<id>#space`, `?deleted=<volume>#space` and
  `?saved=1#space` (the contract has the query only), so the browser lands on the section. `?deleting=<id>` also carries
  `volume=<name>`: `GetVolumeDeletion` is addressed by volume and ID. The notices are drawn only while true: `?cleaning` while
  that clean-up is open, `?deleted` while the volume isn't listed (and Nomad could be read), `?deleting` while the deletion is
  archiving or deleting, and `?saved` always.
- **What the settings post says.** `POST /ops/cleanup/settings` takes `keep`, `automatic` and a hidden `server` (the server
  page it came from, so the 303 goes back there; none goes to `/ops/servers`). A keep outside 1–10 is a 422 page, not a redirect.
- **Overview's line and the Volumes panel.** The panel of each server of several has "Reclaimable 3.3 GB" (a link to `#space`)
  when `ServerSpace` says there is something; a server that is down or disconnected has none. `service.VolumeView` gained
  `NomadName` (`<app>_<volume>`, set by `Volumes`, not by the pure `volumeViews`) because the unmounted rows' `Name` is the
  definition's short name and the delete page is addressed by Nomad's; a row links to `/ops/servers/<server>/volumes/<name>/delete`
  when Nomad says which server holds it, and the note under the panel now ends "or you delete them".
- **Sizes read in the console's units.** `humanBytes` uses 1024 (91,050,000 bytes is "87 MB", 2.3e9 is "2.1 GB"); the contract's
  decimal examples are only examples.
- **The back-up-first box** is drawn when the backup destination is set (`GetBackupDestination`), as D6 says, though
  `DeleteOrphanVolume` still refuses `backUpFirst` until Phase D (T012): ticking it today answers 409 "No backup destination is
  set…", which is the wrong words for a destination that is set. T012 replaces that refusal.
- **A refused delete** maps: a wrong name 422 at the field (the page stays as it was), `volume_not_found` 404, not orphan, in
  use, deletion running and no destination 409 on the page's place of the form, Nomad down 502, anything else the error page.
  The delete page itself is a 404 for a volume that isn't among the server's orphans and 409 for one a copy mounts.
- **Lab.** `console-space` (`console_space_test.go`) runs after `console-servers`; the lab's node has no agent, so it checks the
  no-agent words and the refused clean-up, and the orphan flow; a real clean-up is `install-test.sh`'s (T015).

**Phase D (back up first, Docker's volumes), 2026-11-20.**

- **`ArchiveVolume` takes the destination.** `deploy.ArchiveVolume(ctx, volume, appID, dest)` (the plan's signature
  had no `dest`): the Deployer doesn't know the store, so `DeleteOrphanVolume` reads `backupDestination` (store row
  plus the secret from its Nomad Variable) before anything is recorded and refuses `ErrNoBackupDestination` without
  one. `destinationItems` was split: `destinationKeys` (the `RCLONE_CONFIG_DEST_*` credentials, which the archive's
  variable holds) and `destinationBase` (`bucket/prefix`); `destinationItems` is those plus the backup jobs'
  `BACKUP_PATH`, unchanged.
- **The job holds the path and the time, the variable holds only keys.** `VolumeArchiveJob(volume, appID, backupPath, ts, s)`
  sets `VOLUME`, `BACKUP_PATH` and `TS` as task env (none is a secret); the variable has no `BACKUP_PATH`, so the golden
  has no key and the two can't disagree. `BACKUP_PATH` is `<bucket>/<prefix>/<app>/volumes`, with the bucket (D4 said
  "`<app>/volumes`"), so the object path the script prints is complete, as a backup's is; Caravana checks the script's
  object equals the one it asked for. Group volume request: `single-node-single-writer`, `file-system`, as D4; the
  task mounts it read-only. Resources 100 MHz, 128 MB, ceiling 512 MB (rclone's chunk buffers; the ceiling needs
  013's oversubscription).
- **The script.** `ArchiveScript` is POSIX sh like `VolumeScript`, every `tar`/`rclone` call under `timeout` (1700 s for
  the archive, 120 s for the size, 60 s to delete a partial object; Caravana's wait is 30 minutes, so the script ends
  first). It doesn't leave out `.caravana-restore` (a backup does; an orphan's old files may be the only copy of
  someone's data). On any failure it deletes the partial object (`rclone deletefile`, best effort) and prints the
  failed line, at most 200 characters of a tool's words, and exits 1; `ARCHIVE_ROOT` (default `/data`) is for the
  script test. The shared fake `rclone` of the script tests gained `deletefile`.
- **Reading the outcome.** `nomadjob.ParseArchiveOutput` reads the last marker line of the task's stdout, also from a
  copy that failed (a failed `batchOutcome` would only read stderr). A copy that never ran to its end falls back to
  `batchOutcome`'s words, cut to 200 characters (`ErrArchiveFailed`). Only a job with **no copy at all** for
  `Timing.ArchivePlaceWait` (5 minutes) is "the server that holds the volume isn't placing work"
  (`ErrNomadUnavailable`); a copy that exists but hasn't started (an image being pulled) waits for
  `Timing.ArchiveWait` (30 minutes), then "the archive didn't finish". A leftover archive job of the volume is
  purged first, and the job and variable are removed in a deferred clean-up with a context of its own, so a shutdown
  or a timeout leaves neither.
- **The goroutine.** `DeleteOrphanVolume` with `backUpFirst` records the deletion `archiving` (with `back_up_first`
  set; `bytes` is the volume's measured size), answers it (so the API says 202 and the console 303 with `?deleting=`)
  and runs `archiveThenDelete` in a goroutine counted in `s.watchers`, under `s.background()`, holding the volume's
  `s.deleting` slot until it ends. After the archive: `SetVolumeDeletionDeleting`, `DeleteHostVolume`, settle `done`.
  A failed archive settles `failed` "<reason>; the volume was not deleted" and touches nothing; a delete that fails
  after the archive settles `failed` "the archive was made at <object>, but the volume was not deleted: <why>" with the
  object kept. A shutdown mid-archive leaves the record open (as a move does) and `failInterruptedDeletions` settles it
  at the next start. The record's `bytes` is the volume's size when the agent measured it, the archive's (compressed)
  size otherwise.
- **A crash leaves the job.** If the process is killed (not shut down) mid-archive, its deferred clean-up never runs: the
  archive job runs on to its end in Nomad and its variable (the destination's keys, readable only by that job's task)
  stays until the next archive of the same volume overwrites it, or the app's `DeleteAppJobs` removes it. Nothing
  sweeps it at start; `failInterruptedDeletions` only settles the record.
- **Test fake.** `deploytest.FinishOnRegister(jobID, ok, failedTask, output)` ends a job the test can't register itself
  as soon as it is registered, with an output function that sees the registered job (so a test answers with the object
  the job's own env names). Without it the job has no copy: a node that isn't placing work.
- **T013 (Docker's leftover volumes) was mostly built in Phase A–C**: the box, the form's `dockerVolumes`, the API body,
  `RequestCleanup`, the hand-off, the agent's `docker volume prune -f` and `volumesRemoved`, and a daily run that never
  sets it (`TestDailyCleanups` checks `DockerVolumes` is false). What had no test was the whole path through the real
  agent: `spaceHost` of `cleanup_agent_test.go` now holds local volumes and counts prunes, and `TestServerCleanupOperations`
  checks 3 leftovers read, a clean-up with `dockerVolumes: true` pruning once and answering `volumesRemoved: 3`, and a
  clean-up that doesn't ask leaving the volumes (and pruning nothing). The console's page names "removed 154 Docker
  volumes" (`TestCleanUpWithDockerVolumesShowsWhatWasRemoved`).
- **Not run.** The archive on a real Nomad (does a batch job claim a dynamic host volume with an orphan's capability, does
  the mount read-only work, does purging the job release the claim before the delete) is Phase E's gate step `space`.

**Phase E (lab, docs, release note), 2026-11-20.**

- **The archive works on a real Nomad, unchanged.** The gate step `space` (`gate_space_test.go`) ran the whole path in the
  lab (Nomad 2.0.7): the archive batch job claims a dynamic host volume with the orphan's capability
  (`single-node-single-writer`, `file-system`), the read-only mount at `/data` works, the object is in the bucket
  (`backups/gate/<app>/volumes/<ts>/<volume>.tar.gz`, `tar tzf` lists the volume's file), and purging the job released the claim
  before `DeleteHostVolume`: no "in use" at the delete, so `nomadjob/archive.go` and `ArchiveVolume` needed no change.
- **The step's shape.** The plan's "a reverted deploy that dropped a volume" needs Nomad's 10 minute progress deadline: a copy that
  exits or never passes its check is rescheduled until then (as `brokenReverts` finds), so the step takes about 11½ minutes (692 s
  on 2026-11-20, `-run 'TestLab/^(api-needs-token|space)$'`; a command that exits made no difference, 8 minutes was not enough).
  The deployment that dropped `scratch` carries the "its data stays on server X" warning, not the healthy one after it (that one
  drops nothing new from the stored app), so the step reads the warning on the broken deployment. `/v1/volumes/{name}/deletions`
  has no list: the step checks `volume_not_found` on a POST for an unknown volume and `deletion_not_found` on a read of an unknown
  ID (`GET /v1/volumes/{name}/deletions/{id}`). A killed run leaves its app's job and volumes in the lab (the next run's
  `remove()` can't know the app in a fresh data dir): `lab-nomad job stop -purge gate-space` and `volume delete` clear it.
  `console-space` passed too (81 s with `console-sign-in`).
- **A bug the install-test found, fixed in the agent.** On Docker 29 (the VM's), `docker image rm busybox:1.37 busybox@sha256:…`
  removes the image with its first name and fails on the second ("No such image: busybox@sha256:…", exit 1), so a clean-up
  that had removed the image recorded `failed`. `clean` now checks after a failed `image rm` whether Docker still has the image
  (`Agent.imageGone`: `image inspect <id>` answering "No such image") and counts it removed when it doesn't; any other answer is
  still a failure. Test: `TestCleanUpCountsAnImageThatTheFirstNameTookAway` (the fake Docker's `vanishOn`/`vanishErr`).
- **`failInterruptedDeletions` also removes the archive's leftovers.** For each interrupted deletion that was to back up first it
  purges `<volume>_archive` and deletes its Nomad Variable (the destination's keys), through the new `Deployer.RemoveArchive`
  (the same two calls `ArchiveVolume`'s deferred clean-up makes, which now uses it); a Nomad that refuses is logged, and the
  record is settled either way. This closes Phase D's "a crash leaves the job". Tests: `TestRemoveArchive`,
  `TestFailInterruptedDeletions`.
- **`install-test.sh` `check_space_host`** (run after `check_published_host`, before the upgrade): four deployments of `spaceapp`
  with `alpine:3.19`, `3.20`, `3.21` and `3.22` (checked: four distinct digests; a `sleep 1d`, 16 MB, no port); Nomad restarted;
  `docker image ls --digests` has all four; the agent's report shows `unusedCount` ≥ 1 within 3 minutes; Clean up now → 202,
  `done` within 3 minutes with `imagesRemoved` ≥ 1 and `bytesFreed` > 0; the first image gone, the three newest, Traefik's and
  whoami's present; the next report says `unusedCount` 0 and names the clean-up; redeploying the second version is healthy within
  90 s (the task said 60; a blue/green rollout waits 10 s of min-healthy time plus the start) with the image IDs unchanged (no
  pull); the feed has "Cleaned up server …"; the setting round-trips. **It passed on 2026-11-20 (Ubuntu 24.04, 1501 s whole
  script, the lab VM running beside it).** The first run failed on the agent bug above; the second on a variable the script's own
  loop had overwritten (a script bug).
- **Docs.** `docs/reclaim-space.md`, `changelog.d/045-reclaim-space.md` (Added ×3, Upgrade: rerun the installer), `CLAUDE.md`'s
  paragraph, a row in `docs/plan.md`'s Phase 3 table (Reclaim space), spec status "Implemented, unreleased".
- **A test that took minutes under `-race`.** `TestServerSpace/a keep list that doesn't fit…` (Phase A) saved 4096 apps one commit
  at a time and passed the 25 minute `-race` limit on a loaded Mac; it now stores one app with 4097 services (7 s for the whole
  test under `-race`). Even so, `internal/service` takes about 21 minutes and `internal/ui` about 25 minutes under `-race` alone on this
  Mac (no ui test is over 14 s without it), and `internal/api` took 25 minutes in a whole-repo run with every package in parallel:
  all passed, and it is load, not a hang. The whole-repo `go test -race ./...` run needs `-timeout 40m` here; CI's runners decide
  whether theirs does.
