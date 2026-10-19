# Feature Specification: Terminal into a copy

**Feature Branch**: `020.6-terminal` (from `main` after 0.8.1)

**Created**: 2026-10-19

**Status**: Draft

**Input**: the maintainer, 2026-10-19, after using Run a command: "it doesn't give the same value
as other PaaS tools that let us run something like `docker exec -ti`. Are we not able to do that
in Nomad?" Nomad can (`nomad alloc exec -i -t`); this brings an interactive terminal into a running
copy to the console, as docs/plan.md's Phase 3 "A shell into a copy" planned, now.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Open a shell in a running copy (Priority: P1)

On an app's Running tab the owner presses "Terminal" on a copy. A terminal opens in the browser,
connected to a shell in that copy (bash when the image has it, else sh, or the one they pick). They
type `ls /data`, run the app's CLI, use the arrow keys, Ctrl-C, tab completion and full-screen
programs such as `top` or `vi`, and resize the window. Typing `exit` or closing the page ends it.

**Why this priority**: interactive work in a copy (inspecting files, running an app's own admin
CLI that asks questions) needs a terminal; Run a command answers one non-interactive command.

**Independent Test**: open a terminal into a busybox copy, type `echo $((6*7))` and Enter: `42`
comes back; resize; type `exit`: the page says the shell ended with code 0.

**Acceptance Scenarios**:

1. **Given** a running copy, **When** the owner opens its terminal, **Then** they get an
   interactive shell in that copy (bash if present, else sh), with a terminal that handles
   control keys, colors and resizing.
2. **Given** an open terminal, **When** the shell exits, **Then** the page says so with the exit
   code and offers to open a new one; **When** the page is closed, **Then** the shell ends.
3. **Given** an image with no shell (distroless), **When** the owner opens a terminal, **Then** it
   says no shell was found and suggests Run a command with a program the image has.
4. **Given** a copy that isn't running, a stopped app or a copy Nomad is stopping, **When** the
   owner opens a terminal, **Then** it is refused with the reason.

---

### User Story 2 - Nothing typed or printed is kept (Priority: P1)

What the owner types (perhaps a password) and what the shell prints never land anywhere:
not in Caravana's logs, its database, notifications or live updates. The app's Recent actions show
"Terminal, sh, 4 minutes, exit 0, from the console".

**Acceptance Scenarios**:

1. **Given** a terminal session where a secret was typed and printed, **When** it ends, **Then** no
   page, API answer, record, log line or notice holds the secret.
2. **Given** a session, **When** it ends, **Then** Recent actions show it with the shell, how long
   it lasted and how it ended (exit code, idle timeout, time limit, closed).

---

### User Story 3 - Safe by default (Priority: P1)

A terminal is as powerful as root in the container, so only the signed-in owner (or a full API
token, for a command-line client) can open one; a page on another site can't open one in their
name; a deploy token never can; and a forgotten terminal closes by itself.

**Acceptance Scenarios**:

1. **Given** the owner signed in, **When** another website tries to open a terminal with their
   cookie, **Then** it is refused.
2. **Given** a terminal left idle for 15 minutes, or open for an hour, **Then** it is closed and
   the page says why.
3. **Given** a deploy token, **When** it tries to open a terminal, **Then** it gets 403.
4. **Given** five terminals already open on the installation, **When** a sixth is asked for,
   **Then** it is refused until one closes.

### Edge Cases

- The connection drops (laptop sleeps): the shell ends within a minute; the record says so.
- Caravana restarts during a session: the session ends; its record is settled as interrupted.
- The browser has no JavaScript: the page says the terminal needs it and points to Run a command.

## Requirements *(mandatory)*

- **FR-001**: The console MUST offer an interactive terminal into any running copy of an app, with
  a choice of shell (automatic: bash, else sh; or bash; or sh).
- **FR-002**: The terminal MUST pass input, output, control keys and window size between the
  browser and the copy, and say when and how the shell ended.
- **FR-003**: Input and output MUST NOT be stored, logged or sent anywhere but between the browser
  and the copy. Each session is recorded with time, copy, shell, length, end reason and trigger.
- **FR-004**: Opening a terminal MUST need the signed-in owner (with a same-site check) or a full
  API token; deploy tokens are refused.
- **FR-005**: Sessions MUST end after 15 minutes without input or 60 minutes in all, and at most 5
  may be open at once.
- **FR-006**: The API MUST offer the same terminal to API-token clients (a websocket endpoint,
  documented in the contract), so a command-line client can be built on it.

## Success Criteria *(mandatory)*

- **SC-001**: An owner can run an app's interactive admin command in a copy without SSH.
- **SC-002**: A secret typed into or printed by a terminal is found nowhere afterwards (sentinel
  test across logs, database, pages and API answers).
- **SC-003**: A cross-site attempt to open a terminal with the owner's cookie fails, every time.

## Assumptions

- One owner per installation (Phase 4 adds roles; then terminals become a permission).
- The terminal is the console's first JavaScript widget; everything else keeps working without
  JavaScript, and Run a command stays the no-JavaScript way.
- A terminal into a server (not a copy) waits for Phase 3.

## Out of Scope

- Recording or replaying sessions; file upload or download; several panes; terminals into servers.
