# 063 Caravana backs itself up: its database and Nomad Variables, with a documented restore

CodeAlta task `qck4`. Caravana's own data is one SQLite file on the first server (`<data dir>/caravana.db`: apps and their
recorded deployments, people, tokens, settings, schedules, backup records). The values it never keeps in SQLite (the apps'
secret sets, registry, notification, DNS and destination credentials, uploaded certificates, the issuer) live in Nomad
Variables, in Nomad's raft state on the same servers. The installer copies the database only during an upgrade. If the first
server's disk dies, the apps' own backups survive in the bucket, but nothing says which apps existed, how they were made, or how
to reach those backups. This feature backs Caravana itself up to the default backup destination every day, encrypted with a
key the owner keeps, and restores it on a fresh server.

## What exists that this builds on

- `store.Open` applies the embedded migrations and refuses a store a newer Caravana migrated (`ErrStoreTooNew`).
- The sqlite backup engine (029) copies a live database with `VACUUM INTO` and checks it with `PRAGMA quick_check`.
- Backup destinations (052): `service.ListBackupDestinations`, the default one, `deploy.BackupDestination{Endpoint, Region, Bucket,
  Prefix, AccessKeyID, SecretAccessKey}`, secret key in a Nomad Variable. rclone reaches them as S3 provider `Other` with path-style
  addressing (`remoteKeys`), so every destination Caravana accepts speaks path-style S3 with Signature V4.
- `AdoptFirstServer` with a first server already in the store rotates its node token, key and endpoint: the installer's
  `register_first_server` on a fresh host adopts a restored store's server 1.
- `RedeployVersion` deploys a past healthy deployment again with its digests pinned (`CheckDigests` first).
- `pg_restore` runs with `--clean --if-exists --single-transaction`, so a dump restores into an empty database too.
- Caravana's Nomad policy reads, writes and lists variables under `nomad/jobs/*` and `caravana/*`. No policy change.

## Decisions

- **D1 Nomad side: an export of Caravana's variables, not `operator snapshot`.** A raft snapshot needs a management token (Caravana
  has a scoped one), is encrypted by the cluster's own keyring, and restoring it replaces the whole raft state of the new cluster
  (its nodes, ACL tokens, jobs, CA). The export reads each variable Caravana owns through its own token and keeps its items. What is
  exported is a pure rule, `selfbackup.Keep(path)`:
  - `caravana/*`, except `caravana/cluster/*` (the cluster's CA and gossip key belong to the cluster the installer makes; the
    restored store's servers join it again).
  - `nomad/jobs/<app>/<service>`: exactly two segments after `nomad/jobs/`, an app ID without `_` (helper jobs have `_`) that is
    not `traefik`, a service that is not `caravana` (`nomad/jobs/<app>/caravana/previous` belongs to a deployment in flight).
    These are the secret sets and secret files, the only copy of a kept secret value.
  - Not: registry copies (`…/<svc>/<svc>`), volume requests (`…/<svc>/caravana-volumes`), helper jobs' variables and Traefik's
    (`nomad/jobs/traefik…`): each deploy writes them again from the store and the variables above.
- **D2 The bundle and its encryption.** One object per backup, `<bucket>/<prefix>/_caravana/<ts>/caravana-<ts>.cvbackup`
  (`<ts>` is unix seconds; `_caravana` can't be an app ID, so it is never in an app's path). The file is a plain header and an
  encrypted body:
  - Header: the line `CARAVANA-BACKUP 1`, then one line of JSON `{"createdAt","version","schema","keyFingerprint","ephemeralKey"}`
    (`schema` is the store's newest migration; `ephemeralKey` base64), then `\n`. Readable without the key, so a listing says which
    version and which key made each backup.
  - Body: `gzip(tar)` of `caravana.db` (the `VACUUM INTO` copy), `variables.json` (`[{path, items}]`) and `manifest.json`
    (counts: apps, people, variables), encrypted. The key is X25519: Caravana keeps only the **public** key (in SQLite); each backup
    makes an ephemeral key pair, and HKDF-SHA256 over the shared secret (salt: ephemeral public key ‖ recipient public key; info:
    `caravana self-backup v1` ‖ SHA-256 of the header bytes, so a changed header can't be decrypted) gives an AES-256-GCM key. The body
    is cut in 64 KiB chunks, each sealed with nonce = 11-byte big-endian chunk counter ‖ one byte that is 1 on the last chunk and 0
    otherwise (age's STREAM), so a truncated or reordered file fails. Standard library only: `crypto/ecdh`, `crypto/hkdf`,
    `crypto/aes`, `crypto/cipher`, `archive/tar`, `compress/gzip`. No new module.
  - Nothing on the server can read a backup: a stolen bucket and a stolen server together still don't give old backups' secrets
    beyond what the server holds today.
- **D3 The recovery key.** `cvrk_` + lowercase base32 (no padding) of the 32-byte X25519 private key and the first 4 bytes of its
  SHA-256 (a mistyped key is refused as mistyped, not as wrong). Made by the owner (owner role only) with
  `POST /v1/self-backups/recovery-key`, shown once in that answer (`no-store`) and never again: the private key is not stored
  anywhere. The fingerprint (first 16 hex of SHA-256 of the public key) is stored and shown everywhere a key is named. Making a new
  key replaces the public key; backups made before still need the old key (each lists its fingerprint), and a backup runs at once
  with the new one. Until a key exists nothing is backed up.
- **D4 In-process upload with our own S3 client.** Caravana runs on the host, not in Nomad, and its database file is not
  something a job should mount. A Nomad job would also leave the restore (on a fresh server, before Caravana runs) without a way to
  download. So `internal/s3` is a small path-style S3 client over `net/http` with Signature V4: `Put` (a file of known length,
  payload SHA-256 signed), `Get`, `GetRange` (the header), `List` (ListObjectsV2 with continuation), `Delete`. No error holds a
  URL, a header, a key or a body: the operation, the HTTP status and S3's error `Code`. A test checks the signer against AWS's
  published Signature V4 example. Empty region is `us-east-1`. No new module (the AWS SDK is many).
- **D5 Making a backup** (`service/selfbackup.go`), one at a time (`ErrSelfBackupRunning`):
  1. a row `self_backups` `running`;
  2. `store.CopyTo(ctx, path)`: `VACUUM INTO` a temporary file in the data dir, then `PRAGMA quick_check` on it; refused when the
     data dir's free space is under twice the database's size (a sentence);
  3. read the variables (`deploy.ExportVariables`: `PrefixList` of both prefixes, `Keep`, then `Read` each);
  4. stream `tar → gzip → encrypt` into a temporary file in the data dir (the variables never reach the disk unencrypted), hash it;
  5. `Put` to the default destination; delete the temporary files whatever happened;
  6. retention: list `<base>/_caravana/`, delete objects beyond `keep` (newest kept; only names that read as `<ts>/caravana-<ts>.cvbackup`),
     a failed delete is a warning on the run, not a failure;
  7. the row `succeeded` (object, bytes, destination, key fingerprint, Caravana version, schema) or `failed` with a reason in Caravana's
     words (never a library's text with a URL in it).
  A run left `running` at start is failed with "Caravana restarted during the backup".
- **D6 Schedule and settings.** Daily at `hour` UTC (default 3, 0–23), on by default (`enabled`), `keep` 14 (1–90), stored in
  `self_backup_settings`. A daily pass (every minute, `SetSelfBackupPassGap`) runs when enabled, a key exists, a default destination
  exists and no succeeded or failed scheduled run exists for today's slot. `RequestSelfBackup` (admin) runs now (trigger `owner`).
- **D7 Needs you** (`service/attention_selfbackup.go`, host-level keys `kind|||`), only while a backup destination exists:
  - `self_backup_no_key` (warning): no recovery key, "Caravana can't back itself up until you make a recovery key";
  - `self_backup_failed` (failed): the newest run failed, with its reason;
  - `self_backup_old` (warning): enabled, a key, and no succeeded run in the last 48 hours (or none ever, once 48 hours have passed
    since the key was made). Not raised while turned off: turning it off is the owner's choice, said on the page.
  Wording in `ProblemParts` (one source). Notifications follow from Attention as for every kind.
- **D8 Restore: `caravana admin restore`.** On the host, as root, before Caravana starts:
  - The destination's settings as flags (`--endpoint --region --bucket --prefix --access-key-id`), the secret key and the recovery
    key from `CARAVANA_RESTORE_SECRET_KEY` and `CARAVANA_RECOVERY_KEY`, else asked on `/dev/tty` without echo. Never a flag.
  - `--list` prints each backup (time, size, version, key fingerprint, object) from the headers (ranged GETs); `--object <path>` or
    `--latest` picks one.
  - Download to a temporary file, check the header's fingerprint against the key's (a sentence naming both fingerprints on a
    mismatch), decrypt and unpack: the database to a temporary folder in the data dir, the variables in memory.
  - Open the database with `store.Open` (an older backup is migrated forward; `ErrStoreTooNew` says to install that version or
    newer), `quick_check`, fail every deployment `pending` or `running` with "interrupted: Caravana was restored from a backup", and
    insert a `self_restores` row `pending` (object, the backup's time and version).
  - Refuse when the data dir already holds a store with any app or person, unless `--replace` (a live install is never overwritten by
    mistake); refuse while Caravana answers on its listen address.
  - Write the variables with Caravana's token (`NOMAD_ADDR`, `NOMAD_TOKEN` from the environment, as the installer has in
    `caravana.env`), then move the database into place (its `-wal` and `-shm` removed), owned like the data dir.
  - Print what it did: apps, people, variables, and that Caravana redeploys the apps when it starts.
- **D9 Restore: `install.sh --restore`.** The first-server install, with `--restore latest|<object>` and the destination flags
  (`--restore-endpoint --restore-region --restore-bucket --restore-prefix --restore-access-key-id`; secrets from the same two
  variables or `/dev/tty`). It runs `caravana admin restore` after `configure_caravana` and before `start_version`, skips
  `ensure_admin`, and `register_first_server` then adopts the restored server 1. Refused on a host that already runs Caravana.
  `--email` keeps its meaning (the ACME email default).
- **D10 After the restore, at Caravana's start** (`service/selfrestore.go`): a `pending` restore becomes `running` and a goroutine
  redeploys, one app at a time, every app that isn't stopped and has a live deployment with a recorded definition, with
  `RedeployVersion` and the new trigger `restore` (`app.TriggerRestore`), waiting for each to end (15 minutes at most). Each app is
  a row `self_restore_items` (`redeploy`, status `waiting|running|done|failed|skipped`, the deployment, a reason): an app with no live
  version is skipped ("never deployed" or "no recorded definition"), a stopped app is skipped ("stopped: Start redeploys it").
  The restore is `done` when every item ended. A Caravana restart mid-way goes on with the items still waiting.
- **D11 Start of an app whose job Nomad doesn't have** (after a restore, or a job purged by hand) redeploys its live version pinned
  (`RedeployVersion`, trigger of the Start), instead of failing. Small, in `Service.StartApp`.
- **D12 Data is the owner's step, one button.** Volumes come up empty and databases fresh. The restore panel lists every service with
  a backup policy and its newest backup that isn't gone, and "Restore every service's newest backup" (`POST /v1/self-restores/{id}/data`,
  maintain on every app, so admin and owner) restores them one at a time with `Restore(app, backup, "")` once the app's redeploy item
  is `done`, each a `self_restore_items` row of kind `data`. Not automatic: the owner may want an older backup, and a restore of a
  database the app has started writing to is the owner's call. Each service's own Backups tab still restores any backup.
- **D13 API 0.70.0** (admin reads, owner for the key and settings; deploy tokens denied by the default deny; every answer `no-store`):
  `getSelfBackups` (`GET /v1/self-backups`: settings, key fingerprint and when it was made, destination, last run, `unavailable`
  reason: `no_destination`, `no_key`), `putSelfBackupSettings` (owner), `createRecoveryKey` (owner, 201, `{recoveryKey,
  fingerprint}` once; `replace: true` needed when a key exists, else 409 `recovery_key_exists`), `startSelfBackup` (`POST
  /v1/self-backups/runs`, 202, `Location`; 409 `self_backup_running`, `self_backup_unavailable` with the reason), `listSelfBackupRuns`,
  `getSelfBackupRun`, `getSelfRestore` (`GET /v1/self-restores/latest`, 404 `self_restore_not_found`), `restoreSelfRestoreData`
  (202). The attention kinds, `Deployment.trigger` `restore`. Audit: key made (the key never noted), settings, a run requested by
  someone, the data step; clock-driven runs add nothing (057 D14).
- **D14 Console** (`internal/ui/selfbackup.go`, `templates/opsselfbackup.html`): Operator › **Caravana backup** (`/ops/self-backup`, after
  Console address): the state in one sentence, the last run, Back up now, the settings form (owner; read-only for an admin), the
  recovery key section (Make a recovery key / Make a new key…, the key shown once on a 200 `no-store` page with what to do with it),
  past runs, a link to the restore guide, and, while a restore is running or ended within 30 days, the restore panel (items and the
  data button). Overview's server panel for the first server says "Caravana backup: <when>" or the problem. Forms work without
  JavaScript; the run panel refreshes from `/parts/ops/self-backup` while a run or a restore goes (286 when not). Existing parts only;
  the designer reviews after.
- **D15 Migration 0049** (`0049_self_backup.sql`): `self_backup_settings` (one row: enabled, hour, keep, public_key, key_fingerprint,
  key_created_at), `self_backups` (id, status, trigger `schedule|owner|key`, actor, started_at, ended_at, destination, object, bytes,
  key_fingerprint, version, schema, warning, error), `self_restores` (id, status `pending|running|done`, object, backup_created_at,
  backup_version, restored_at, ended_at), `self_restore_items` (restore_id, seq, kind `redeploy|data`, app, service, status, ref,
  reason, started_at, ended_at).
- **D16 Secrets rule.** No recovery key, private key, variable item, secret key or decrypted byte in any log, page (but the one key
  page), answer (but `createRecoveryKey`'s), error, audit entry or notification. Sentinel tests in `selfbackup`, `service`, `api`,
  `ui` and `cmd/caravana`.

## What the owner keeps outside the server

The recovery key, the backup destination's endpoint, bucket, prefix, access key ID and secret key, and the console's domain. The docs
say so on the page where the key is shown and in the restore guide.

## Not in 063

A raft snapshot, restoring onto a server that still runs an older install, servers other than the first coming back by themselves
(they show as down; remove them and add them again), DNS records made by 048 following the new address (update them, or remove and
add the zone), automatic data restores, a second destination for Caravana's own backups, encryption of the apps' backups.

## Constitution check

- I Small by default: no new module, no new runtime dependency; the copy and upload run in Caravana's process.
- III/IV: a restore redeploys recorded digests pinned; every refusal names its field or says why.
- V API-first: every console action has its API route; `openapi.yaml` 0.70.0 in the same change.
- VI: Nomad access stays in `deploy`; the export uses the official client.
- VII: secret values stay encrypted with a key the server doesn't hold; no secret in logs. The control plane's secrets stay in
  Nomad Variables; the bundle in the bucket is an encrypted copy, not a new home.
- VIII: table tests, the signer's known answer, the stream cipher's truncation, round trips, sentinels.

## Phases

- **A** `internal/s3`, `internal/selfbackup` (pure: `Keep`, recovery key, bundle write/read, stream cipher), `store` (migration 0049,
  `CopyTo`, records), `deploy.ExportVariables`/`ImportVariables`, `service` (runs, pass, retention, settings, key, attention, audit,
  start-up), API 0.70.0.
- **B** Console: Operator › Caravana backup, the key page, Overview line.
- **C** Restore: `caravana admin restore`, `service/selfrestore.go` (D10, D11, D12), the restore panel and its API, `install.sh --restore`,
  docs (`docs/self-backup.md`, the guide), gate step `self-backup`, changelog note.

## Deviations

### Phase A

- **`self_backups.schema_version`**, not `schema` (D15): the column is named for what it holds and keeps clear of SQL's own word. The API
  and the header still say `schema`.
- **Routes.** D13 names the operations, not every path. They are `GET /v1/self-backups`, `PUT /v1/self-backups/settings`, `POST
  /v1/self-backups/recovery-key`, `POST|GET /v1/self-backups/runs` and `GET /v1/self-backups/runs/{id}`. `getSelfBackups` also carries
  `lastSucceeded` and `running` beside `last` (the newest run in any state), which the page needs for "Last backup" and the run panel. Codes:
  `self_backup_running`, `self_backup_unavailable` (its message says which of the two reasons), `self_backup_not_found` (field `id`),
  `recovery_key_exists`. The recovery-key body is optional (`{"replace": true}`).
- **The destination's own secret key is in the backup.** `selfbackup.Keep` keeps `caravana/*` minus `caravana/cluster/*`, and the destination's
  key is at `caravana/backup-destinations/<name>`. That is wanted: a restored Caravana needs it to reach the backups of its apps. It is
  encrypted like every other variable, and the sentinel tests search for it.
- **`deploy` imports `selfbackup`** for the `Variable{Path, Items}` type, so there is one type from Nomad to the file. `ImportVariables` also
  refuses, before writing anything, a path `selfbackup.Keep` doesn't keep and a variable with no items: a backup that names Traefik's or the
  cluster's variables is not one of ours.
- **`store.CopyTo` holds the store's one connection** while `VACUUM INTO` runs (the store has one connection by design), so the console waits for a
  copy of a large database. A copy of a few MB takes milliseconds; a gigabyte would be seconds. Not worth a second connection yet.
- **The free-space rule is in `CopyTo`** (a `*store.SpaceError` with the sentence), where D5 step 2 puts it; it measures the folder the copy goes to.
- **A failed scheduled run is not tried again the same day** (D6 says so; the needs-you item says it failed). A Caravana that was down at the
  hour backs up when it starts, any time that day (there is no "too late" limit as the schedules have: a backup of the middle of the day is
  as good as one of the night).
- **Phase C must clean the restored copy.** The database in a backup was copied while its own run was `running`, so a restored store has a
  `self_backups` row that Start would settle as failed ("Caravana restarted during the backup") and then raise `self_backup_failed` about.
  `caravana admin restore` deletes `self_backups` rows of status `running` from the restored database, with the deployments it fails.
- **Feed and live notices.** The three needs-you kinds are in `store.OpenedProblemKinds` (the Activity feed has no row of its own for a run of
  Caravana's backup), `service.ChangeSelfBackup` ("selfbackup") is an admin-only notice, and `app.TriggerRestore` has its words ("after a
  restore") in the console, the feed, the Logs page's deploy rows and the notification text. `openapi.yaml` 0.70.0 adds `restore` to
  `Deployment.trigger`.
- **`self_backup_no_key`'s time** (the item's `at`) is when the default destination was last saved: the moment the owner could have made a key.
