# Tasks: Notifications

**Input**: [plan.md](./plan.md), [notifications.spec.md](./notifications.spec.md),
[research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/),
[quickstart.md](./quickstart.md)

**Scale note**: 15 tasks. Run the phases in order. The risky tasks:
- **T008**: the diff must never resend or flood.
- **T009**: the sender must never block or lose a message.

**Tests are required** (constitution VIII):
- table-driven, with `testing` and `httptest` only;
- `go build ./...`, `go vet ./...`, `gofmt -l .` and `go test` for the touched packages stay
  green after every task;
- an API change edits `api/openapi.yaml` in the same commit;
- a template change runs `tools/ui-css.sh build` when it adds classes.

## Phase 1: Setup

Nothing.

## Phase 2: Foundational

- [X] T001 [P] Package `internal/notify` (research R4, data-model "Package"), in notify.go, render.go, sign.go and send.go:
  - `Event` and `Channel`.
  - `Render(ch, ev) (*http.Request, error)` for webhook, slack, discord and ntfy, with exactly the
    headers and bodies of research R4. `Render` also takes the delivery ID and a clock, for the
    webhook headers and signature timestamp.
  - `Sign(secret string, t int64, body []byte) string` and
    `Verify(secret, header string, body []byte, now time.Time, tolerance time.Duration) error`.
  - `Send(ctx, client *http.Client, req *http.Request) error`: 10 s timeout, 2xx ok, else an error
    with the status and up to 200 bytes of the body. The error must never contain the request URL
    (Slack and Discord URLs are secrets): use the host only.

  Tests: one golden per kind under internal/notify/testdata (method, URL, headers, body). Also
  `Sign`/`Verify` round trip with tolerance and tamper cases, `Send` against `httptest` (ok, 500,
  slow over the timeout), and a sentinel URL path absent from every error.
- [X] T002 [P] Store (data-model "Migration", "Store"):
  - internal/store/migrations/0010_notifications.sql, exactly as data-model.md says;
  - internal/store/notifications.go with channels CRUD, sent problems (list, upsert, touch,
    delete, delete by app), deliveries (enqueue, due, finish, retry, list, prune);
  - `DeleteApp` also deletes `sent_problems` rows of the app.

  Tests: round trips; due ordering by `next_at`; prune keeps 20 per channel and never prunes
  pending rows; cascade on channel delete.
- [X] T003 [P] Config and variable helpers:
  - `CARAVANA_PUBLIC_URL` (`-public-url`): optional, absolute http(s), trailing slash trimmed.
  - `CARAVANA_NOTIFY_RESOLVE_AFTER` (`-notify-resolve-after`): default 5m, minimum 10s.

  Both go in internal/config/config.go with tests, and are wired in cmd/caravana/main.go through
  setters on `Service`, like `SetVolumeBackupTimeout`.

  internal/deploy/notifications.go: `NotificationSecret(ctx, id) (map[string]string, error)`,
  `SaveNotificationSecret(ctx, id, items)` and `DeleteNotificationSecret(ctx, id)`, on
  `caravana/notifications/<id>`. Tests with the fake.

## Phase 3: User Story 3 - Manage channels (P1)

- [X] T004 [US3] Service, in internal/service/notifications.go:
  - `NotificationChannels`, `CreateNotificationChannel(ctx, in)`, `UpdateNotificationChannel`,
    `DeleteNotificationChannel`, `TestNotificationChannel` and `RotateNotificationSecret`, per
    data-model "Service".
  - Validation per contracts/api.md: kind; URL absolute http(s); at least one event; token only
    for ntfy.
  - The shown address masks Slack and Discord paths (`hooks.slack.com/services/T…/••••`).
  - A webhook's signing secret: 32 random bytes in hex, returned once on create and on rotate.
  - Secrets in the variable: Slack and Discord store `url`; ntfy stores `token`; webhook stores
    `signing_secret`.
  - `TestNotificationChannel` renders a `test` event, sends it at once, records a delivery (sent
    or failed) and returns it.
  - `NotificationDeliveries` returns the last 20.

  Tests: each validation; masking; secret round trip; no secret in any returned struct
  (sentinel); a test against `httptest` both ok and failing.
- [X] T005 [US3] API, in internal/api/notifications.go:
  - the seven operations of contracts/api.md;
  - `not_a_webhook` 409;
  - api/openapi.yaml 0.14.0 → 0.15.0, with the schemas and the webhook message shape documented
    as a component for receivers.

  Tests: each operation; `signingSecret` only in the create and rotate answers; no secret
  elsewhere (sentinel); the contract test.
- [X] T006 [US3] Console, Settings › Notifications, per contracts/console.md (exact copy):
  - internal/ui/notifications.go;
  - settings.go loads the channels and each channel's deliveries;
  - routes in ui.go with `protected(requireSession(...))`;
  - templates/settings.html;
  - parity entries in internal/api/parity_test.go.

  Tests: list, add (webhook shows the secret once), edit (empty keeps), pause/resume, test
  notices, remove, deliveries list, no secret in pages or logs (sentinel), forms work without JS.

## Phase 4: User Story 1 - Problems (P1) 🎯 MVP

- [X] T007 [P] [US1] Wording, in internal/service/notifier.go: for each attention kind and deploy outcome, a title and text in the console's wording (internal/ui/attention.go has the needs-you sentences: port their wording, without importing ui), and the link per research R6 when the public URL is set. Tests: a table of every kind.
  - Also: Slack, Discord and ntfy messages end with a `Detail: <raw detail>` line (one line, cut at 500 characters), in `notify.Render`, for FR-006.
  - The deploy `trigger` is the record's own value: `console`, `manual` or `webhook`.
- [X] T008 [US1] The notifier (research R1), in internal/service/notifier.go:
  - Done as built, with these differences from the text below:
    - `diffProblems` also takes the IDs of the apps that exist and returns a `problemChanges` struct (opened, resolved, seen, dropped).
    - `store.ApplyProblemCheck` writes the table and the queue in one transaction.
    - More than 10 resolved in one check are also one summary.
    - The notifier is woken by a 30 s timer alone, because `Subscribe` would keep the copies watcher running for as long as Caravana does. The first check comes one gap after start.
  - `problemKey(item)`.
  - The pure `diffProblems(prev, now, copiesKnown, at, resolveAfter)`:
    - new items are opened;
    - present items are seen;
    - items absent for at least `resolveAfter` since `last_seen_at` are resolved;
    - absent but younger items are kept;
    - when `copiesKnown` is false, copy kinds (`out_of_memory`, `restarting`, `unhealthy`) are
      neither opened nor resolved;
    - items of apps that no longer exist are dropped without "resolved".
  - `checkProblems(ctx)`:
    1. Attention;
    2. the diff;
    3. a transaction-safe table update;
    4. enqueue one delivery per opened or resolved problem per channel that wants `problems` and
       isn't paused;
    5. more than 10 opened in one check becomes one summary per channel (FR-011).
  - Started in `Service.Start`: a 30 s timer plus a `ChangeAttention` subscription. Use the hub
    without keeping copy watching alive needlessly; the timer alone is acceptable if subscribing
    is awkward. Tracked like the other watchers and stopped on shutdown.

  Tests: the diff table (new, seen, flapping under and over the delay, unknown copies, deleted
  app, restart with existing rows); `checkProblems` with fakes (one opened, then nothing on the
  next check, then resolved after the delay); summary above 10.
- [X] T009 [US1] The sender (research R3), in internal/service/sender.go:
  - Done as built: one worker goroutine per channel with something due, so the loop never waits on a receiver; the loop is woken by a 5 s timer, by `poke` after a message is queued, and when a worker finishes.
  - a goroutine that every 5 s, and when woken by enqueue, takes due pending deliveries (limit 20)
    and sends them, at most 4 at once, one channel at a time per channel;
  - it reads the channel and its secret, then `Render` and `Send`;
  - on failure it retries after 30 s, then 2 min, then marks the delivery failed;
  - on success it marks it sent;
  - after each finish it prunes to 20;
  - a deleted or paused channel's pending rows are marked failed ("channel removed" or "channel
    paused") without sending.

  Tests: `httptest` receivers (ok; 500 twice then ok; always 500 gives failed after 3 attempts
  with the error kept; slow receiver doesn't delay another channel); pending rows survive a
  restart (new `Service` on the same store sends them).

## Phase 5: User Story 2 - Deploy outcomes (P1)

- [X] T010 [US2] `notifyDeploy(rec)` in internal/service/notifier.go, called from `finish` (rollout outcome) and `fail` (before register) in internal/service/deploy.go after the record write. It is also called where a deploy is superseded before it registers, which is the third place a deployment ends. It enqueues `deploy.succeeded` (healthy) or `deploy.failed` (failed, reverted, superseded) to channels wanting `deploy_succeeded` or `deploy_failed`, with version, outcome, trigger and reason (the record's error). It never blocks: an enqueue error is logged. Tests: each outcome, each event filter, webhook and redeploy triggers, failure before register (image not found).

## Phase 6: Polish

- [X] T011 [P] Installer: `deploy/install/install.sh` writes `CARAVANA_PUBLIC_URL=https://$domain` into caravana.env (the console domain the installer already knows). Shellcheck clean.
- [ ] T012 Console lab step `console-notifications`, in cmd/caravana/console_lab_test.go, after `console-volumes`:
  1. Start an `httptest` server in the test that records requests.
  2. Add a webhook channel through the console form (all three events), and keep the shown
     secret.
  3. Send a test, and expect a delivery with a valid signature (`notify.Verify`).
  4. Deploy an app with a health path that 404s, and expect `deploy.failed` (or reverted) and
     `problem.opened` within 2 minutes, each once.
  5. Fix the app and deploy, and expect `deploy.succeeded`.
  6. With `CARAVANA_NOTIFY_RESOLVE_AFTER=10s` set by startLab for the in-process server, expect
     `problem.resolved` within 2 minutes.
  7. Remove the channel and the app.
  - `go vet ./cmd/caravana`.
- [ ] T013 [P] Docs:
  - CHANGELOG "Unreleased": Added (notifications, channels, signed webhooks, `/v1/notifications`,
    API 0.15.0) and Upgrade (rerun the installer to set the console address used in links).
  - A CLAUDE.md "Where we are" paragraph for 018.
  - docs/plan.md marks 018 done.
- [ ] T014 Full check (CLAUDE.md "Commands"): `go test -race ./...` and the rest; shellcheck on install.sh.
- [ ] T015 Lab: full `TestLab` alone in the lab; quickstart with ntfy on the maintainer's host after the release.

## Dependencies

- T001 ∥ T002 ∥ T003.
- Then T004, T005 and T006.
- T007 ∥ T004.
- T008 needs T002, T004 and T007.
- T009 needs T001, T002 and T004.
- T010 needs T007 and T009.
- Then T011–T015.

## Implementation strategy

- **MVP**: T001–T010 (channels, problems, deploy outcomes).
- **Delegation**: one Sonnet agent per phase group, reviewed and lab-checked by Opus.
