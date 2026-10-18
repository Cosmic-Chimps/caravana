# Research: App actions and Run a command (020.5)

## R1. Run a command: Nomad alloc exec

`api.Allocations().Exec(ctx, alloc, task, tty=false, command []string, stdin, stdout, stderr,
terminalSizeCh, q) (exitCode int, err error)` opens Nomad's exec websocket with Caravana's own
token. The scoped policy (`deploy/vm/caravana-policy.hcl`, applied by the installer and by
`TestLab`) gains `alloc-exec` on namespace `default`. The task is the service's own task (task name
= service name; never the `names` sidecar or `caravana-volumes`). stdin is an empty reader; no TTY.

- **Timeout** 60 s: the context passed to Exec is cancelled; the answer says "timed out" (exit code
  absent).
- **Cap** 64 KiB per stream: a writer that keeps the first 64 KiB and counts the rest; `truncated`
  per stream.
- **Command shape**: API takes `command: [string]` (1–64 items, each ≤ 4 KiB, no NUL). The console
  takes one line split by a pure shell-words parser (single and double quotes, backslash escapes;
  no variables, globs or pipes); its help says to use `sh -c '…'` for a shell.
- **Errors**: the copy isn't running (409 `copy_not_running`); the copy isn't the app's (404); the
  program isn't found shows as Nomad's error text on stderr with the exit code Nomad gives (126/127),
  or as `failed to start` with the error, never as a 500.

## R2. Never store the output

The request log (`httpx.LogRequests`) records method, path and status only. The API answer and the
console page set `Cache-Control: no-store`. The console answers the POST with the page itself (200,
no redirect), since a redirect would need the output in a URL or in storage. No live notice carries
anything but "actions changed". Tests run `echo` with a sentinel and search every page, API answer,
the SQLite file and the captured log.

## R3. Stop and Start

- **Stop**: `Jobs().Deregister(appID, purge=false)`: Nomad keeps the job (status dead, `Stop:
  true`) with its versions, so Start can bring it back. The app's Postgres backup periodic jobs
  (`<app>_<svc>_backup`) are deregistered the same way; restore jobs aren't periodic and are left.
- **Start**: read the job (`Jobs().Info`), set `Stop=false`, `Jobs().Register` — what `nomad job
  start` does. That makes a new Nomad job version with the same definition and digests; no tag is
  resolved, secrets in Nomad Variables were never touched. Backup periodic jobs get the same.
  Caravana's own deployment records are not touched: Start is not a deploy (its own record in
  `app_actions`). The Nomad deployment Start causes isn't watched; the copies' health shows as for
  any app. **Lab-checked 2026-10-18** (Nomad 2.0.7, the lab's whoami with `Canary: 1,
  AutoPromote: true` as `Translate` makes): `job stop` without purge took it off Traefik (404);
  `job start` served 200 again within about 4 s, and its Nomad deployment ended `successful` on its
  own. A group without canaries (one with a volume, `Canary: 0`) has nothing to promote.
- **Stored state**: `apps.stopped_at` (NULL = not stopped), so lists, refusals and the scheduler
  don't ask Nomad. Stop writes it after Nomad accepted; Start clears it after Nomad accepted.
- **Refusals while stopped** (FR-004): one sentinel `ErrAppStopped` (409 `app_stopped`) checked in
  `saveAndDeploy`, `Redeploy` (so the webhook), `RedeployVersion`, `Promote` (target), and the
  console's define/configure/secret edits (they all go through those).
- **Scheduled volume backups** skip a stopped app (`service/backups.go` scheduler) without a
  record; "back up now" and restore on a stopped app are 409 `app_stopped`.
- **Status**: `AppStatus.State` gains `stopped`; the copy kinds of the needs-you list ignore a
  stopped app.

## R4. Restart

For each running copy of the app's current version, one at a time: `Allocations().RestartAllTasks`
(the restart Caravana already marks as requested, `requestedRestartMessage`, so it never counts as
a crash loop), then wait until the copy is running again and, when the service has a health check,
healthy (Nomad's alloc health / service checks as `Copies` reads them), up to 2 minutes per copy.
Runs in the background after the API answers 202; its outcome goes in `app_actions`.

## R5. Busy

Stop, Start and Restart take the app's per-app lock (the one deploys and volume backups hold) with
`TryLock`: if it's held, 409 `app_busy`. Restart holds it for its whole run, so a deploy that comes
during a restart waits, as it waits for a volume backup. Run a command takes no lock.

## R6. The record: `app_actions`

Migration `0012_app_actions.sql`:

```sql
ALTER TABLE apps ADD COLUMN stopped_at TEXT; -- NULL: not stopped
CREATE TABLE app_actions (
  id          TEXT PRIMARY KEY,
  app_id      TEXT NOT NULL,
  action      TEXT NOT NULL CHECK (action IN ('stop','start','restart','run')),
  trigger     TEXT NOT NULL,          -- console | manual
  status      TEXT NOT NULL CHECK (status IN ('running','succeeded','failed')),
  copy_id     TEXT NOT NULL DEFAULT '',
  service     TEXT NOT NULL DEFAULT '',
  program     TEXT NOT NULL DEFAULT '', -- run: the command's first word, nothing else
  exit_code   INTEGER,                  -- run: NULL when it timed out or didn't start
  error       TEXT NOT NULL DEFAULT '', -- never command output
  started_at  TEXT NOT NULL,
  finished_at TEXT
) STRICT;
CREATE INDEX app_actions_by_app ON app_actions(app_id, started_at);
```

Deleting an app deletes its actions. A restart left `running` at startup is marked `failed`
("Caravana restarted during it"). The app page lists the last 10.
