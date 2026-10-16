# Implementation Plan: Notifications

**Branch**: `018-notifications` (stacked on `017-private-registries`) | **Date**: 2026-10-16 |
**Spec**: [notifications.spec.md](./notifications.spec.md)

**Scale note**: about 30 files in six packages. It has:
- a new pure package `internal/notify`;
- one migration;
- two settings;
- no Nomad change.

The risks are duplicates and floods:
- a problem resent after a restart;
- "resolved" sent during a Nomad outage;
- one bad receiver slowing the rest.

Research R1 and R3 address them with a persisted diff and a queue. Every one is table-tested.

## Summary

Owners add channels in Settings › Notifications or through `/v1/notifications/channels`
(API 0.15.0). The kinds are signed webhook, Slack, Discord and ntfy, and secrets are write-only in
Nomad Variables.

Two sources produce events:
- **A notifier** recomputes the "needs you" list every 30 s, and on every attention notice. It
  diffs the list against a `sent_problems` table, so each problem is sent once when it opens and
  once when it has been gone 5 minutes, across restarts.
- **Deploy outcomes** are sent from where deployments end.

Events go into a delivery queue in SQLite. One sender works through it with a 10 s timeout and
three attempts, and keeps the last 20 deliveries per channel for the console.

## Technical Context

**Language/Version**: Go 1.27, no CGO

**Primary Dependencies**: standard library (`net/http`, `crypto/hmac`, `crypto/sha256`). No new
module.

**Storage**: SQLite (`0010_notifications.sql`) and Nomad Variables (`caravana/notifications/<id>`).

**Testing**:
- pure diff, render and sign tests;
- `httptest` receivers for sending;
- the deploytest fakes;
- the console lab step `console-notifications`.

**Target Platform**: single-node Linux host

**Project Type**: web service

**Performance Goals**: a problem is sent within about a minute (30 s check plus sending); a dead
receiver costs only its own 10 s per attempt.

**Constraints**:
- exactly one "opened" and one "resolved" per problem, across restarts;
- no secret in any page, answer, log or message;
- nothing waits on a receiver.

**Scale/Scope**: a handful of channels; tens of events a day.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS. No new module or service. The notifier and sender are goroutines in Caravana; it costs one needs-you read every 30 s (one Nomad request), which the console already pays when open. |
| II. One Model, Many Inputs | PASS. Untouched: notifications read the app model and records, never change them. |
| III. Safe, Reversible Deploys | PASS. Deploys only enqueue a row after their record is written; a receiver can never block or fail a deploy. |
| IV. Fail Loudly | PASS. Failed deliveries are listed per channel with the receiver's answer. Invalid channel input names its field. |
| V. API-First | PASS. Seven operations in `api/openapi.yaml` 0.15.0 in the same commits; the parity table maps every console route. |
| VI. Nomad-Native | PASS. No Nomad code beyond the existing variable helpers in `deploy`. |
| VII. Secure by Default | PASS. Channel secrets are in Nomad Variables, write-only. Webhook messages are signed. Messages carry no secret (deploy errors never do). Outgoing requests go only to URLs the owner configured. |
| VIII. Plain, Tested Go | PASS. Pure diff, render and sign; table tests; a lab step with a real receiver. |

No violations. Re-checked after design: no change.

## Project Structure

```text
specs/018-notifications/{notifications.spec.md, plan.md, research.md, data-model.md, quickstart.md,
                         contracts/{api.md,console.md}, checklists/requirements.md, tasks.md}

internal/notify/notify.go, render.go, sign.go, send.go (+ tests, testdata goldens)
internal/store/migrations/0010_notifications.sql
internal/store/notifications.go (+ tests); apps.go (DeleteApp drops sent_problems)
internal/deploy/notifications.go (+ tests)         # caravana/notifications/<id> variable helpers
internal/service/notifications.go (+ tests)        # channels CRUD, test, rotate, deliveries
internal/service/notifier.go (+ tests)             # diffProblems, checkProblems, wording, notifyDeploy
internal/service/sender.go (+ tests)               # the delivery worker
internal/service/service.go, deploy.go             # start goroutines; hook finish/fail
internal/config/config.go (+ tests)                # CARAVANA_PUBLIC_URL, CARAVANA_NOTIFY_RESOLVE_AFTER
cmd/caravana/main.go                               # wiring
internal/api/notifications.go, api.go, errors.go (+ tests), parity_test.go
api/openapi.yaml                                   # 0.14.0 → 0.15.0
internal/ui/notifications.go, settings.go, ui.go, templates/settings.html (+ tests)
deploy/install/install.sh                          # CARAVANA_PUBLIC_URL from --domain
cmd/caravana/console_lab_test.go                   # console-notifications
CHANGELOG.md, CLAUDE.md, docs/plan.md
```

## Delivery order

1. **Foundation**: `internal/notify` (render, sign, verify, send), the migration and store, the
   variable helpers, the config settings.
2. **US3 channels (P1)**: service CRUD, test and rotate; API; Settings section. It is useful
   alone: a test message reaches a phone.
3. **US1 problems (P1)**: the notifier (diff, wording, summary), the sender, start/stop, DeleteApp
   cleanup.
4. **US2 deploy outcomes (P1)**: the hooks in `finish` and `fail`.
5. **Lab and docs**: the console lab step, the installer setting, CHANGELOG, CLAUDE.md, the plan.

## Complexity Tracking

None.
