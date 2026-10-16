# Research: Notifications

No lab experiment is needed: nothing new runs in Nomad. The questions are where the events come
from, how sending stays exactly-once-ish across restarts, and how secrets are kept.

## R1. Where problem events come from: a notifier that diffs the needs-you list

**Decision**: a `notifier` goroutine in `service` (started in `Service.Start`, tracked like the
other watchers) computes `Service.Attention` on a timer: every 30 s, and at once when a
`ChangeAttention` notice is published. It compares the items with the `sent_problems` table:

- **new item**: insert it and queue "problem opened" to every channel that wants problems;
- **known item**: update its `last_seen_at`;
- **known item missing from this check**:
  - for 5 minutes or more: queue "problem resolved" and delete the row;
  - for less: keep it, so a flapping item doesn't resend.

An item's **identity** is kind, app, service and deployment (for `deploy_failed`), and is stored
as one key string. Copy IDs are left out, because a crash loop changes copies and is still one
problem.

**Rationale**: the needs-you list is derived on every read (feature 014), and change notices only
flow while a console is subscribed (`ensureWatcher`). A timer catches problems nobody is
watching, such as crash loops and unhealthy copies, which Nomad reports only when asked. The
table makes it survive restarts (FR-004). On start the first check sees everything:
- items already in the table are not resent;
- items that went while Caravana was down are resolved, once the 5 minutes since their
  `last_seen_at` have passed.

When Nomad can't be read (`Attention.CopiesUnknown`), the copy kinds are not judged in that
check: they are neither opened nor resolved, so a Nomad outage doesn't send a burst of
"resolved".

**Alternatives considered**: hooking each place a problem arises (deploy finish, backup run,
copy events). Copy problems have no single place, and the diff gives "resolved" for free.

## R2. Deploy outcomes: a direct hook

**Decision**: `finish` (rollout outcome) and `fail` (failed before register) in
internal/service/deploy.go call `s.notifyDeploy(rec)` after the record is written. It queues
"deploy succeeded" for healthy and "deploy failed" for failed, reverted or superseded, to the
channels that want them, carrying the trigger and the reason.

**Rationale**: every deployment passes through exactly one of the two, including webhook and
scheduled redeploys. A deploy failure also opens a `deploy_failed` problem through R1; a channel
that wants both gets both, and the spec allows it.

## R3. Delivery: a queue in SQLite, sent by one worker

**Decision**: queued messages are rows in `notification_deliveries`. Each holds the channel, the
event, the payload JSON, the status (`pending`, `sent` or `failed`), the attempts, `next_at` and
the error.

A sender goroutine:
- picks due pending rows;
- sends each with a 10-second timeout;
- retries after 30 s, then 2 min, which is 3 attempts in all;
- marks the row sent or failed.

Different channels are sent in parallel, at most 4 at once. Each channel keeps its last 20
delivered or failed rows, older ones are pruned, and pending rows survive a restart.

**Rationale**:
- Deploys and the notifier only insert a row, so they never wait on a receiver (FR-009, SC-004).
- A restart doesn't lose queued messages.
- The deliveries list the console shows (FR-010) is the same table.

## R4. Message formats per kind

**Decision**: one internal event struct, with these fields:
- `type`: `problem.opened`, `problem.resolved`, `deploy.succeeded`, `deploy.failed`, `test`;
- `kind` (the attention kind), `app`, `service`;
- `title` and `text` in the console's wording;
- `detail`, `deployment`, `version`, `outcome`, `trigger`, `url`, `at`.

Each channel kind renders it:

| Kind | Request |
| --- | --- |
| webhook | `POST <url>`, `Content-Type: application/json`. Body: the event as JSON, field names as above. Headers: `Caravana-Event: <type>`, `Caravana-Delivery: <id>`, `Caravana-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>`. |
| slack | `POST <url>` `{"text": "*<title>*\n<text>\n<url>"}` (mrkdwn) |
| discord | `POST <url>` `{"content": "**<title>**\n<text>\n<url>"}` |
| ntfy | `POST <topic url>` with the body as text. Headers: `Title: <title>`, `Tags` (`warning` for problems, `white_check_mark` for resolved and success), `Priority` (`high` for problems and failures, `default` otherwise), `Click: <url>` when known, and `Authorization: Bearer <token>` when set. |

A 2xx answer is success; anything else, or a timeout, is a failure, with the status and the first
200 bytes of the body as the error.

**Summary** (FR-011): when one check opens more than 10 problems, each channel gets one
`problem.opened` event with `title` "<n> new problems" and the list in `text`.

**Rationale**: the signature follows the common `t=,v1=` scheme, so receivers can reuse known
verification code. ntfy's headers are its documented publishing API.

## R5. Where channel secrets live

**Decision**:
- **SQLite (`notification_channels`)**: id, kind, name, the shown address, events wanted,
  paused, created and updated times.
- **The Nomad Variable `caravana/notifications/<id>`**: the secret parts, write-only:
  - slack and discord: the whole URL, which is the secret;
  - ntfy: the topic URL in SQLite (shown) and the token, if any, in the variable;
  - webhook: the URL in SQLite and the signing secret in the variable.

**What the console shows**:
- for Slack and Discord, the host and a masked path (`hooks.slack.com/services/T…/••••`);
- for a webhook, the full URL. If a token is embedded in the URL, the owner chose that, so it
  stays visible like other non-secret settings.

The signing secret is generated by Caravana (32 random bytes, hex), shown once when the channel is
created, and can be regenerated.

**Rationale**: constitution VII keeps the control plane's secrets in Nomad Variables, as feature
010 keeps the backup destination's key and 017 the registry secrets. The `caravana/*` variables
are already in the policy.

## R6. The console's address for links

**Decision**: a new setting `CARAVANA_PUBLIC_URL` (flag `-public-url`, for example
`https://caravana.example.com`).
- **Installer**: writes it from `--domain` into caravana.env, so installs and reruns get it.
- **Without it**: messages have no links.
- **Link targets**:
  - deploy events: the deployment's entry, `<url>/apps/<id>/deployments#deployment-<dep>`;
  - backup and restore problems: `<url>/apps/<id>/backups`;
  - copy problems: `<url>/apps/<id>`;
  - no destination: `<url>/settings#backups`.

## R7. API, console, tests

**API** (0.14.0 → 0.15.0):

| Operation | Route | What |
| --- | --- | --- |
| `listNotificationChannels` | `GET /v1/notifications/channels` | list the channels |
| `createNotificationChannel` | `POST /v1/notifications/channels` | 201; a webhook channel's answer carries `signingSecret` this once |
| `updateNotificationChannel` | `PATCH /v1/notifications/channels/{id}` | name, address, secret parts, events, paused |
| `deleteNotificationChannel` | `DELETE /v1/notifications/channels/{id}` | remove a channel |
| `testNotificationChannel` | `POST /v1/notifications/channels/{id}/test` | 200 with the delivery's outcome, sent at once and not queued |
| `listNotificationDeliveries` | `GET /v1/notifications/channels/{id}/deliveries` | the last deliveries |
| `rotateNotificationSecret` | `POST /v1/notifications/channels/{id}/secret` | webhook only; the new secret, once |

**Console**: Settings › Notifications, with the add form, a list with Test, Edit, Pause and
Remove, and recent deliveries in each row's `<details>`. Parity entries for each route.

**Tests**:
- the notifier's diff is a pure function (`diffProblems(prev, now, at) (opened, resolved, kept)`),
  table-tested for new, kept, flapping, resolved, unknown copies and summary;
- the renderers are pure, one golden per kind; the signature is checked with a reference
  verifier;
- the sender is tested against an `httptest` receiver: ok, 500, timeout, retries, pruning;
- a sentinel secret is never in a page, an answer, a log or a message to another channel.

**Lab**: a console lab step `console-notifications` adds a webhook channel pointing at an
`httptest` server in the test process. Caravana runs on the Mac in the lab, so it reaches the
server. The step deploys an app with a broken health check, waits for `problem.opened` and
`deploy.failed`, checks the signature, fixes the app, and waits for `deploy.succeeded`. For
`problem.resolved`, the 5-minute delay is configurable (`CARAVANA_NOTIFY_RESOLVE_AFTER`, default
5m), and the lab sets 10 s.
