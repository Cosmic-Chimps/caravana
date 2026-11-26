# 061 OpenSearch snapshots: back up a search cluster without stopping it

CodeAlta task `pcqt` (a follow-up of 060, task `gwfq`). Stacked on 060 (`alta/copper-pelion-48z3`). The only engine that fit
an OpenSearch or Elasticsearch service was `volume` (016), which stops the service for the whole archive; Wazuh's indexer has no
backup at all. This feature adds the engine `opensearch`: the cluster takes a snapshot of itself through its own API while it
runs, and Caravana archives it to the backup destination with retention, pins, copies and restore like every other engine.

## Research (lab, 2026-11-26, Lima VM, aarch64)

- **R1** `repository-s3` is in neither `opensearchproject/opensearch:2.19.1` nor `wazuh/wazuh-indexer:4.14.11`
  (`opensearch-plugin list`, `ls plugins modules`). Installing it means `opensearch-plugin install` at the image's exact version,
  a download at start and a restart, and S3 keys in the node's keystore. So the S3 repository the task proposed is out.
- **R2** `curl`, `tar` and `gzip` are in the OpenSearch, Wazuh indexer and Elasticsearch 8.17 images (Amazon Linux 2023 and
  Ubuntu bases); they run as uid 1000.
- **R3** A shared-filesystem (`fs`) repository needs its location inside `path.repo`, a static node setting. The OpenSearch and
  Elasticsearch images take it as an environment variable named `path.repo` (their entrypoints turn dotted variables into
  settings); Wazuh's `/entrypoint.sh` doesn't, so there it goes in `opensearch.yml`. A location outside it is refused with
  `repository_exception` "location [...] doesn't match any of the locations specified by path.repo" (500). A location under a
  `path.repo` entry is fine, and OpenSearch makes the folder itself (volumes are mode 0777, 016).
- **R4** `PUT _snapshot/<repo>/<name>?wait_for_completion=true` with `{"indices":"*","include_global_state":false}` takes
  `.opendistro_security` and the plugins' dot indices too; restoring `.opendistro_security` as `admin` is refused (403
  "no permissions for []"). `"indices":"*,-.*"` takes every other index; a pattern that matches nothing is a SUCCESS with no
  indices.
- **R5** Restore over a live index needs it closed: `POST /<a,b>/_close?ignore_unavailable=true`, then
  `POST _snapshot/<repo>/<snap>/_restore?wait_for_completion=true` with `{"indices":"a,b","include_global_state":false}` brings
  the snapshot's data back and opens them; an index made since stays. Files archived and extracted by root (mode 664, owner 0)
  in a readonly repository restore fine.
- **R6** Elasticsearch 8.17 (`ELASTIC_PASSWORD`, https by default) behaves the same for R3–R5; its answers add `repository` and
  `feature_states` keys.
- Index names can't contain `"`, `,`, spaces, `\`, `/`, `*`, `?`, `<`, `>`, `|` or `#`, so a list of them can be cut on `,`
  and `"` in a shell script without a JSON parser (the images have no `jq`).

## Decisions

- **D1 Engine.** `app.BackupOpenSearch = "opensearch"`, for OpenSearch, its forks (Wazuh indexer) and Elasticsearch 7/8. It is
  not a dump engine (`DumpEngine` stays false): it runs inside the service's copy on Caravana's own schedule, like `volume` and
  `sqlite` (`runsInCopy`).
- **D2 The repository folder.** The policy names `repository` (`backup: {engine: opensearch, repository: /usr/share/opensearch/snapshots}`):
  an absolute path as the service sees it, equal to or inside one of the service's writable volumes (refused naming
  `services[i].backup.repository` otherwise; only this engine takes the field, as only `sqlite` takes `file`).
  `Service.OpenSearchRepository()` gives the volume and the path inside it (`""` for the mount itself), like `SQLiteFile`. The
  owner puts it in `path.repo`; a deploy **warns** on `services[i].backup.repository` when the service has no env named
  `path.repo` and no file (060's `files`) whose content contains `path.repo`, and the first run fails with OpenSearch's own
  sentence (R3) otherwise. The service must have a port (refused on `services[i].backup` like the dump engines).
- **D3 One fresh folder per run.** Each run uses `<repository>/caravana-<request>` and the object is
  `<BACKUP_PATH>/<ts>.opensearch.tar.gz`, a self-contained full snapshot. Nothing is incremental, so retention, pins (036),
  copies (052, `nomadjob/backupcopy.go` gets the extension) and restore from any place work as for the other engines. Each
  run's first step removes the `caravana-*` folders an earlier run left in `<repository>` (only those: the docs say the folder
  is Caravana's). Needs free disk for one snapshot while it is archived (documented).
- **D4 What a backup holds.** Every index whose name doesn't start with a dot (`*,-.*`), without global state: index templates,
  persistent cluster settings, the security plugin's configuration and the plugins' and Dashboards' dot indices are not in it
  (R4; documented). Data streams are not covered. Restore: the snapshot's indices; each that exists is closed first and the
  snapshot restored over it (R5); indices made since stay. If the restore fails after the close, the script tries to open them
  again, and says so in its reason either way.
- **D5 Where each step runs.** The API calls run with Nomad's alloc exec **in the service's own task** (curl, R2) against
  `127.0.0.1:<container port of the service's first port>`: https first, then http when the TLS handshake fails, with `-k`
  (loopback inside the copy's own network namespace; the security plugin's certificates are self-signed). The folder is archived
  and extracted in the `caravana-upload` sidecar (rclone image), which for this engine mounts the repository's volume writable at
  `/caravana-volumes/<volume>` (as `caravana-sqlite` does for sqlite; no `caravana-sqlite` task here). Steps:
  - backup: `clean` (sidecar: removes the `caravana-*` folders earlier runs left) → `snapshot` (service task: register
    `caravana` at the run's folder, take `<ts>`, require `"state":"SUCCESS"` and no failed shard, unregister) → `upload`
    (sidecar: tar+gzip streamed to `rclone rcat`, both statuses checked, size, retention with pins, the folder always removed).
  - restore: `clean` (sidecar) → `download` (sidecar: `rclone cat | tar xzf -` into the folder, both statuses checked, requires
    `index.latest`) → `restore` (service task: register readonly, read the one snapshot's name and indices, close, restore,
    require no failed shard, unregister) → `clean` (sidecar, whatever happened).
  - Every script prints markers and writes them to a result file under `/alloc/data/caravana/opensearch/` (029's exec-output
    race), takes its inputs as positional arguments (`SQLiteCommand`'s shape) and counts every call against what is left of the
    run's limit (`curl --max-time`, BusyBox `timeout`).
  - No restart, no volume request: the service never stops.
- **D6 The login.** On the exec's standard input only, never an argument or a file: lines `OPENSEARCH_USER=` and
  `OPENSEARCH_PASSWORD=`, which the script reads into variables and feeds to each curl call as a config on its stdin
  (`printf 'user = "%s:%s"\n' ... | curl -K - ...`; `printf` is a shell builtin, so no process listing shows it; `"` and `\` are
  escaped, a line break is refused). `app.FindDumpLogin` gains the engine (one login rule): `OPENSEARCH_INITIAL_ADMIN_PASSWORD`
  (user `admin`), else `INDEXER_PASSWORD` with `INDEXER_USERNAME` (default `admin`; Wazuh's), else `ELASTIC_PASSWORD` (user
  `elastic`); none found means no login (a cluster with the security plugin off), so validation doesn't require one. A 401 fails
  the run with "OpenSearch refused the login: set OPENSEARCH_INITIAL_ADMIN_PASSWORD, INDEXER_PASSWORD or ELASTIC_PASSWORD"
  (`DumpLoginHint`). `BackupPasswordNames` lists the three, so a plain-env password warns as for the dump engines. The values
  come from the service's env, then its secret set, read at run time (the same lookup `syncBackups` does).
- **D7 Runs and records.** Share 016/029's claim, request IDs, scheduler, limits, `backupBusy`, pins and records
  (`service/opensearchbackup.go` beside `sqlitebackup.go`; `deploy/opensearchrun.go` beside `sqliterun.go`, reusing
  `destinationLines`, `redact`, `seconds`, the result-file read). At start, a running opensearch backup or restore is failed
  ("Caravana restarted during the run"; a restore adds "indices it closed may still be closed"), with nothing asked of Nomad. The
  job ID of a record is `<app>_<service>_opensearch/<ts>`.
- **D8 Store and API.** Migration `0048_opensearch_backups.sql` widens the engine CHECK of `backups` and `restores` (as 0036 did).
  API 0.67.0: `opensearch` in every engine enum (policy, backup, restore, overlay), `BackupPolicy.repository`; no route, no code.
- **D9 Console.** The Compose form's engine select ("OpenSearch or Elasticsearch snapshot (the service keeps running)") with the
  Repository input (beside Database file), the Backups tab's change-policy form, rows and built-in schedules saying "OpenSearch
  snapshot". No designer pass (existing parts).
- **D10 Wazuh template** (`caravana-templates` branch `wazuh-template`): the indexer gets a volume `indexer-snapshots` at
  `/var/lib/wazuh-indexer-snapshots`, `path.repo` in its `opensearch.yml`, the secret `OPENSEARCH_INITIAL_ADMIN_PASSWORD` = the admin password (not `INDEXER_PASSWORD`: Wazuh's entrypoint writes that one into a keystore; the image ignores this name),
  and `backup: {engine: opensearch, repository: /var/lib/wazuh-indexer-snapshots}`; the notes lose the "isn't backed up" sentence;
  `requires` follows the release that has 061.
- **D11 Lab.** Gate step `opensearch-snapshot`: single-node OpenSearch 2.19.1 with the security plugin on
  (`OPENSEARCH_INITIAL_ADMIN_PASSWORD` a secret holding a sentinel, `path.repo` env, a volume, TCP health check, 384 MB heap),
  index A → Back up now → change to B and add an index → restore → A again and the new index still there; a second backup with
  `keep: 1` and the first pinned keeps both; no sentinel in any answer, log, job, export or the SQLite file.

## Constitution check

- I: no new module, no runtime dependency. II: one more helper-task use (`caravana-upload` already named by 029); no new job.
- III/IV: a bad repository path or engine field is refused naming the field; a missing `path.repo` warns at deploy and fails the
  run loudly. VII: the login and the destination's keys travel on stdin only; tests search for sentinels. V: the API changes in the
  same commit as the model. VI: Nomad access stays in `deploy`/`nomadjob`. VIII: scripts tested under a real `sh` with fake `curl`
  and `rclone`, goldens for the sidecar.

## Phases

- [x] **A — backend**: model and validation (D1, D2, D6), `nomadjob/opensearch.go` (scripts, sidecar, markers, parser) with script
  tests and goldens, `deploy/opensearchrun.go`, `service/opensearchbackup.go` wired into Back up now, the scheduler, restore,
  restore schedules, resume, pins; migration 0048; `api/openapi.yaml` 0.67.0; the deploy warning; sentinel tests.
- [ ] **B — console and docs**: D9, `docs/database-backups.md` (or a section in `docs/opensearch-apps.md`), the guide's backups page,
  `changelog.d/opensearch-snapshots.md`, `CLAUDE.md`.
- [ ] **C — lab and template**: D11 gate step passing in the lab, D10 template change.

## Deviations (phase A)

- **Migration 0048 runs with foreign keys off.** `backup_copies` (052) references `backups` with `ON DELETE CASCADE`, so the
  rebuild 0036 did would delete every copy's row now that foreign keys are enforced (054), and a `backups` row whose app is
  gone (kept from before enforcement) could not even be copied. The runner (`migrate.go`, `apply`) now reads a first line
  `-- caravana: foreign_keys off` and runs that migration on one connection with the pragma off around its transaction, as
  SQLite's guide for rebuilding a table says; `backup_copies` keeps its reference by name. `TestUpgradeTo0048` checks the copy
  rows, an orphan of each table and the cascade after it; `TestOrphansFromBeforeForeignKeysAreKept` still passes unchanged.
- **Names.** The sqlite engine's shared pieces lost "sqlite" from their names because opensearch uses them too: `sidecarRun`,
  `prepareSidecarRun` (it takes the engine), `settleSidecarBackup`, `sidecarFailure`. `nomadjob.SQLiteOutput` gained `Indices`
  and `Files` and `ParseSQLiteOutput` reads the opensearch markers and both objects' extensions, instead of a second type;
  `deploy.runSQLite` delegates to `runStep`, which takes any list of secrets to keep out of error text.
- **Retention is shared.** `uploadRetention` in `sqlite.go` (the `retain` function, parameterized by `$ext`) serves both upload
  scripts; the sqlite goldens and tests are unchanged.
- **Scripts.** The clean script takes a mode (`fresh` empties the work directory of old results, `keep` doesn't: the last clean of
  a restore, and the clean after a failed snapshot, run when results of the run are still wanted) and makes the work directory
  world-writable, since the service's own task (another user) writes its result files there (a result it can't write is not an
  error: the markers are on stdout). The snapshot and restore scripts also unregister a repository an earlier run left
  (`DELETE _snapshot/caravana` first, its answer ignored) and again on exit. The restore script refuses an index name outside
  `[A-Za-z0-9._+@,-]` rather than put it in a URL unencoded. The upload script refuses a folder with no `index.latest`, and every
  script that removes or empties a folder refuses one whose last segment doesn't start with `caravana-`.
- **Record messages.** A restore that succeeds has no message. After a restart a running backup is failed with
  `restartedMessage` ("Caravana restarted during the run") and a restore with the same plus "; the indices it closed may still be
  closed", with nothing asked of Nomad (`settleOpenSearchRuns`, called by `resumeVolumeRuns` before the volume and sqlite ones).
- **Not done (phase B and C):** the console (Compose form engine select, Repository input, Backups tab rows and built-in schedule
  words), docs, changelog note, CLAUDE.md, the gate step, the template change.
