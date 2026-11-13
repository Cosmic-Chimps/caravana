# Console contract: scheduled restores

Everything sits on the app's Backups tab (`internal/ui/backups.go`, `templates/app.html` `backups`) and the
Compose form. Plain forms, 303 on success, the tab redrawn with one notice on a refusal. No new script, no new
colour, no new component: reuse `cv-confirm-row`'s `<details>`, `cv-btn-sm`, `cv-inline-mono`, the run row's
existing meta line and `cv-note`.

## A run row

- A succeeded run that is not pinned has, beside **Restore…**, a **Pin…** `<details>`: one input `label`
  (default `golden`, `pattern`, `maxlength="32"`, `autocomplete="off"`), the sentence "A pinned backup is never
  deleted by retention and isn't counted in the backups kept. Pinning a label another backup has moves it here.",
  and **Pin** → `POST /apps/{id}/backups/{backupId}/pin`.
- A pinned run shows its label in its meta line as `pinned golden` (mono label) and has **Unpin** (a plain post
  to `/apps/{id}/backups/{backupId}/unpin`, no confirmation: nothing is lost).
- Pinned runs are always listed in the service's runs, even past "Show older".

## The service row

- `Keep` reads "7 backups" as today, and "7 backups, plus 1 pinned" when there are pins.
- A **Restore schedule** line, when the policy has one: "Restores **golden** every day at 04:00 UTC"
  (`scheduleText`) and, after it, the newest scheduled restore: "last: 3 hours ago, succeeded" /
  "last: failed — no backup of web is pinned as golden" / "last: skipped — …". With a label nothing carries:
  a `cv-note` "No backup of web is pinned as golden yet: each restore is refused until one is."
- A **Restore on a schedule…** `<details>` on the row (shown when the tab is not stopped and the app is defined):
  inputs `schedule` (prefilled with the current one or `0 4 * * *`) and `from` (prefilled or `golden`), the
  sentence "At each time, the backup pinned under this label replaces what the service holds now. This deploys
  the app.", **Save and deploy**, and when one is set, **Stop restoring** (the same form with an empty
  schedule). → `POST /apps/{id}/backups/restore-schedule` with `service`, `schedule`, `from`, `revision`;
  success 303 to `/apps/{id}/deployments#deployment-<id>` (as "Back up daily" does); refusals as
  `setBackupPolicy`'s.

## Recent restores

Each restore says who started it: "by you" for `owner`, "on schedule from **golden**" for `schedule`. A skipped
one has the idle mark and its message.

## Compose form

Per service, after Keep: **Restore on a schedule** (`backupRestoreSchedule`) and **from the backup pinned as**
(`backupRestoreFrom`). Both or neither ("Give both a schedule and a label, or neither."); refused without an
engine as Schedule is. Filled from the kept overlay or the app (`draftOf`), so a deploy from the form keeps it.

## Tests

`internal/ui` table tests for the row (pins, the schedule line, the "not pinned yet" note), the three routes
(303s, a bad label 422 with the notice, `app_busy` 409), and the Compose form round trip keeping `restore`.
`TestConsoleLab` is not extended (the gate steps cover the runs); `TestNoShipWords` and the parity test pass.
