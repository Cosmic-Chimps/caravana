# Feature Specification: Notifications

**Feature Branch**: `018-notifications`

**Created**: 2026-10-16

**Status**: Implemented, v0.6.0

**Input**: "Notifications (docs/plan.md, Phase 2.5, 018). Push what the 'needs you' list derives
(failed or reverted deploys, out-of-memory kills, crash loops, unhealthy copies, failed backups and
restores, no backup destination) and deploy outcomes to the owner, without anyone having the
console open. Channels: outgoing webhooks with signed JSON, Slack and Discord incoming webhooks,
and ntfy. Email later. Each channel says which events it wants, can be tested, and shows its
recent deliveries. A problem is sent once when it starts and once when it is resolved, not again
while it lasts, and not again after Caravana restarts."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Hear about problems without watching the console (Priority: P1)

The owner adds their ntfy topic (or a Slack or Discord channel) in Settings › Notifications. At
03:00 Uptime Kuma's nightly volume backup fails. Within a minute their phone buzzes: "kuma: the
backup of uptime-kuma failed: <reason>", with a link to the app's Backups tab. The next night the
backup succeeds and a second message says it's resolved. Nothing is sent in between, even though
the problem was still on the needs-you list, and nothing is sent twice when Caravana restarts.

**Why this priority**: today a failure is only seen by opening the console; the point of a PaaS
the owner can "sleep well" with is that it tells them.

**Independent Test**: add a channel pointing at a local receiver. Deploy an app with a broken
health check: within a minute one "problem" message arrives for the failed deploy, with the app,
the reason and a link. Restart Caravana: nothing more arrives. Fix the app and deploy: one
"resolved" message arrives.

**Acceptance Scenarios**:

1. **Given** a channel that wants problems, **When** any item appears on the needs-you list,
   **Then** within a minute the channel receives one message naming the kind, the app (and
   service), what happened in words, the raw detail, and a link to the right console page.
2. **Given** a problem already sent, **When** it is still on the list a minute, an hour or a day
   later, **Then** nothing more is sent for it.
3. **Given** a problem already sent, **When** it leaves the list, **Then** the channel receives one
   "resolved" message for it.
4. **Given** problems already sent, **When** Caravana restarts, **Then** none of them is sent again,
   and a problem that started or ended while Caravana was down is sent once when it comes back.
5. **Given** nobody has the console open, **When** a copy starts crash-looping, **Then** it is still
   noticed and sent within a minute or two.

---

### User Story 2 - Deploy outcomes (Priority: P1)

The owner wants a message for every deploy that ends: "whoami: deployed v8 (healthy)" or "hollo:
the deploy was reverted to v6: <reason>", including deploys started by a webhook or a scheduled
redeploy. They can choose per channel whether healthy deploys are worth a message.

**Why this priority**: deploys triggered by CI webhooks happen while nobody watches; knowing they
landed (or didn't) is the most common reason to want notifications.

**Independent Test**: with a channel that wants deploy outcomes, trigger the webhook of a working
app: one "deployed" message. Deploy a broken version: one "failed" or "reverted" message (and the
problem message of US1, unless the channel wants only one of the two).

**Acceptance Scenarios**:

1. **Given** a channel that wants deploy outcomes, **When** a deployment ends healthy, failed,
   reverted or superseded, **Then** one message names the app, the version, the outcome, what
   started it (console, API, webhook), and for a failure the reason.
2. **Given** a channel that only wants failures, **When** a deploy ends healthy, **Then** nothing is
   sent to it.
3. **Given** a deploy that fails before Nomad registers it (a missing image, refused registry
   credentials), **When** it ends, **Then** it is sent as a failed deploy with its reason.

---

### User Story 3 - Manage channels (Priority: P1)

In Settings › Notifications the owner adds a channel: its kind (webhook, Slack, Discord, ntfy), a
name, its address, and which events it wants. Addresses that hold a secret (a Slack or Discord URL,
an ntfy access token, a webhook's signing secret) are write-only like every secret. "Send a test"
sends a sample message right away and shows whether it was accepted. Each channel shows its last
deliveries (time, event, ok or the error). A channel can be paused or removed.

**Why this priority**: a channel that silently fails is worse than none; the owner has to see that
messages get through.

**Independent Test**: add a webhook channel, send a test (accepted, listed), point it at a URL that
answers 500, send a test (shown as failed with the status), remove it.

**Acceptance Scenarios**:

1. **Given** the Settings page, **When** the owner adds a channel, **Then** it is listed with its
   kind, name, the events it wants and its address with any secret part hidden.
2. **Given** a channel, **When** the owner sends a test, **Then** the page says whether the receiver
   accepted it, and the delivery is listed.
3. **Given** a webhook channel, **When** it is created, **Then** its signing secret is shown once,
   and every message to it carries a signature the receiver can check with that secret.
4. **Given** a receiver that is down, **When** a message is due, **Then** it is retried a few times
   over a few minutes and the failure is listed on the channel; other channels are not delayed.
5. **Given** a paused channel, **When** events happen, **Then** nothing is sent to it, and resuming
   it does not replay what was missed.
6. **Given** an API client with a token, **When** it manages channels, **Then** it can do everything
   the console can, and no answer contains a secret.

### Edge Cases

- **Many problems at once** (a node restart makes every app unhealthy for a minute): problems that
  start within the same check are sent as separate messages per app, at most one per item; a check
  that finds more than 10 new items sends one summary message instead, listing them.
- **Flapping**: an item that leaves and comes back within 5 minutes doesn't send "resolved" then
  "problem" again; resolution is only sent once the item has been gone for 5 minutes.
- **No backup destination**: an installation-wide problem, sent like any other, without an app.
- **An app deleted while it has problems**: its problems are dropped silently (no "resolved").
- **A receiver that answers slowly**: each delivery has a 10-second timeout.
- **Message text**: plain words in the console's own sentences, never a secret value (deploy errors
  already never contain one).
- **The console's own address** is needed for links; a fresh install without a public domain sends
  messages without links.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The owner MUST be able to add, list, edit, pause, resume, test and remove notification
  channels of the kinds: webhook (signed JSON), Slack incoming webhook, Discord webhook, ntfy topic.
- **FR-002**: Each channel MUST say which events it wants: problems (opened and resolved), deploy
  failures, deploy successes; any combination.
- **FR-003**: Every item the needs-you list derives MUST be sent once when it appears and once when
  it has been gone for 5 minutes, to every channel that wants problems, within about a minute of
  the change, whether or not anyone has the console open.
- **FR-004**: Which problems were already sent MUST survive a restart of Caravana: none is sent
  twice, and changes during downtime are sent once after it.
- **FR-005**: Every deployment that ends (healthy, failed, reverted, superseded, or failed before
  registration) MUST be sent to every channel that wants that outcome, once.
- **FR-006**: A message MUST name the event, the app and service when there is one, what happened in
  plain words, the raw detail, the time, and a link to the console page about it when the console's
  address is known.
- **FR-007**: Webhook messages MUST be JSON in a documented shape, signed with the channel's own
  secret so the receiver can check them; the secret is shown once at creation.
- **FR-008**: Secrets of a channel (Slack and Discord URLs, ntfy tokens, webhook signing secrets)
  MUST be write-only and kept where Caravana keeps its own secrets; never in a page, an API answer
  or a log.
- **FR-009**: Delivery MUST time out after 10 seconds, retry with backoff up to 3 attempts over a few
  minutes, and never hold up deploys, other channels, or the console.
- **FR-010**: Each channel MUST list its last 20 deliveries with time, event, outcome and the error
  when one failed.
- **FR-011**: More than 10 new problems in one check MUST be sent as one summary message per channel.
- **FR-012**: The API MUST offer everything the console does with channels and deliveries, with every
  new endpoint and field in `api/openapi.yaml` in the same change.

### Key Entities

- **Channel**: kind, name, address (the non-secret part shown), events wanted, paused or not,
  created time; its secret parts kept apart and write-only.
- **Delivery**: channel, event, time, outcome (sent, failed after retries), attempts, error.
- **Sent problem**: the identity of a needs-you item that was sent (kind, app, service, deployment),
  when it was first sent, and when it was last seen; kept until resolved.
- **Event**: problem opened, problem resolved, deploy succeeded, deploy failed (with reverted and
  superseded as outcomes), test.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A failed nightly backup reaches the owner's phone within 2 minutes, with nobody using
  the console.
- **SC-002**: A problem that lasts a day produces exactly 2 messages per channel (opened, resolved),
  across any number of Caravana restarts.
- **SC-003**: Setting up an ntfy or Slack channel and receiving a test takes under 2 minutes.
- **SC-004**: A receiver that is down never delays a deploy or another channel by more than its own
  10-second timeout.
- **SC-005**: No secret appears in any page, API answer, log or message.

## Assumptions

- One owner per installation; every channel belongs to the installation. Per-project channels come
  with 019.
- ntfy is reached by its topic URL (ntfy.sh or self-hosted), optionally with an access token.
- Slack and Discord are reached only through their incoming-webhook URLs; no bot or OAuth.
- Email (SMTP) is a later step; the event model doesn't change for it.
- Messages are in English, in the console's own wording.
