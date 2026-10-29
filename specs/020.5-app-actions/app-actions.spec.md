# Feature Specification: App actions and Run a command

**Feature Branch**: `020.5-app-actions` (stacked on `019.5-create-without-deploy`)

**Created**: 2026-10-18

**Status**: Implemented, v0.8.0

**Input**: docs/plan.md, Phase 2.6, 020.5: "Stop, Start and Restart per app. Run a command in a
running copy: one non-interactive command in the live copy, its stdout, stderr and exit code shown
once on the page that answers the form, with a timeout and an output cap. The output may hold a
secret (Umbral's `reset-credential` prints a new operator credential), so it never lands in a log,
a record, a notice or the live stream; what is recorded is that a command ran, its first word and
its exit code."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Run a command in the running app (Priority: P1)

The owner forgot Umbral's operator credential. On Umbral's Running tab they pick its copy, type
`/app/Umbral reset-credential` and press Run. The page answers with what the command printed (the
new credential, shown this once), what it wrote to stderr, and its exit code. Nothing of the output
is kept anywhere: reloading the page, the app's history, Caravana's logs and notifications don't
have it. The tab's list of recent commands shows "Umbral, exit 0, 2 minutes ago".

**Why this priority**: today this needs SSH to the host and Nomad's CLI with the management token.

**Independent Test**: run `echo s3cret-sentinel` in a running copy: the answer shows it with exit 0;
no page, API answer, record, log line or live notice holds the sentinel afterwards; the recent
commands list has `echo` with exit 0. Run `sh -c 'exit 3'`: exit 3. Run `sleep 120`: stopped at
the timeout and said so.

**Acceptance Scenarios**:

1. **Given** a running copy, **When** the owner runs a command, **Then** they see its stdout, stderr
   and exit code once, on the answer to that request.
2. **Given** a command that prints more than the cap, **When** it runs, **Then** the output is cut
   at the cap and the answer says it was cut.
3. **Given** a command that runs longer than the timeout, **When** the timeout passes, **Then** it
   is stopped and the answer says it timed out.
4. **Given** any command, **When** it has run, **Then** the app keeps a record of when, in which
   copy, the command's first word, the exit code and how it was started (console or API) — never
   the arguments or the output.
5. **Given** a copy that isn't running, or a command that doesn't exist in the image, **When** the
   owner runs it, **Then** the answer says so plainly.

---

### User Story 2 - Stop and start an app (Priority: P1)

The owner wants an app off for a while without deleting it (a staging copy over the weekend, an app
misbehaving at night). Stop shows what will happen ("its 2 copies stop; deploys, the webhook and
scheduled backups wait until you start it") and stops it: the app shows as stopped (○), it serves
nothing, and nothing about it is lost. Start runs again exactly what was running, with no new image
lookup.

**Why this priority**: today the only way to stop an app is to delete it.

**Independent Test**: stop a deployed app: no copies run, its domain stops answering, it is listed
as stopped, its webhook and a deploy are refused with "stopped", its volume and secrets stay. Start
it: the same version runs again and its domain answers.

**Acceptance Scenarios**:

1. **Given** a running app, **When** the owner stops it, **Then** all its copies stop, it is listed
   as stopped, and its definition, secrets, volumes, backups, webhook token and history stay.
2. **Given** a stopped app, **When** anything tries to deploy it (a deploy, the webhook, a
   promotion into it, a redeploy of a version, editing it), **Then** it is refused with "the app is
   stopped: start it first", and nothing is recorded.
3. **Given** a stopped app, **When** the owner starts it, **Then** what was running when it stopped
   runs again (same images, same settings), without resolving tags again.
4. **Given** a stopped app with a backup policy, **When** its scheduled backup comes, **Then** no
   backup runs and nothing is reported as failed; the backups resume after Start.
5. **Given** a deploy or a volume backup in progress, **When** the owner stops, starts or restarts
   the app, **Then** it is refused with "busy: wait for it to finish".

---

### User Story 3 - Restart an app (Priority: P2)

The owner restarts an app to pick up something outside Caravana (a rotated registry credential, a
config read at startup). Restart restarts every copy, one at a time, waiting for each to be running
(and healthy, if it has a health check) before the next, so an app with two copies never goes dark.
Restarts asked for this way never count as "keeps restarting".

**Independent Test**: restart a two-copy app while calling its domain: no failed request; each
copy's restart count went up by one and the needs-you list stays empty.

**Acceptance Scenarios**:

1. **Given** a running app, **When** the owner restarts it, **Then** its copies restart one at a time
   and the action is recorded with its outcome.
2. **Given** a copy that doesn't come back within the time allowed, **When** the restart waits for
   it, **Then** the restart stops there and its outcome says which copy didn't come back.

### Edge Cases

- An app never deployed or without a definition offers none of the actions.
- Deleting a stopped app works as for any app.
- Copying or promoting from a stopped app works (they read records); promoting into one is refused.
- Caravana restarting during a Restart leaves the copies as Nomad has them; the action's record says
  it was interrupted.

## Requirements *(mandatory)*

- **FR-001**: The owner MUST be able to run one non-interactive command, given as a program and its
  arguments, in a chosen running copy of an app, and see its stdout, stderr (each capped) and exit
  code, or that it timed out, in the answer to that request only.
- **FR-002**: Command output MUST NOT be stored, logged, put in a notice, a live update, a record,
  a URL or a cache. Only the time, copy, service, the command's first word, the exit code (or
  "timed out"/"failed to start") and the trigger are recorded.
- **FR-003**: The owner MUST be able to stop an app (every copy stops; nothing else is lost) and to
  start a stopped app again with exactly what was running.
- **FR-004**: While an app is stopped, every path that deploys it MUST be refused with one clear
  error; its scheduled backups MUST be skipped, not failed.
- **FR-005**: The owner MUST be able to restart an app, copy by copy, without taking a multi-copy app
  offline; requested restarts never count as a crash loop.
- **FR-006**: Stop, Start and Restart MUST be refused while a deploy or a volume backup of the app is
  running.
- **FR-007**: Every action (stop, start, restart, run) MUST be recorded with when, how it was
  started and its outcome, and shown on the app's page; and every action MUST be available from the
  console and the API alike.

## Success Criteria *(mandatory)*

- **SC-001**: Resetting Umbral's operator credential takes one form in the console and no SSH.
- **SC-002**: A secret printed by a command appears nowhere after its answer (checked with a
  sentinel across pages, API answers, records, logs and the live stream).
- **SC-003**: A two-copy app restarted from the console never goes dark: at least one copy serves
  throughout, and at most 1 call in 10 fails (one reaching a copy in the moment it restarts;
  lab 2026-10-18: 2 of 103). A hitless restart by replacement is in the backlog.
- **SC-004**: Stop then Start runs the same job definition and image digests as before the stop.

## Assumptions

- One installation, one admin: anyone signed in or holding an API token may run commands, as they
  can already deploy anything. Per-person permissions are Phase 4.
- The interactive terminal was built next, as 020.6 (v0.9.0); this is its no-JavaScript first step.
- Stop and Start are per app (all its services), like the handoff's app actions.

## Out of Scope

- An interactive shell or terminal; stdin to the command; a TTY.
- Running a command in a fresh copy (that is 022 Schedules' Run now).
- Stopping or restarting a single service of a multi-service app.
