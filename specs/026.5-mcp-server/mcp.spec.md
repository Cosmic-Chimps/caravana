# Feature Specification: An MCP server for Caravana

**Feature Branch**: `026.5-mcp-server` (from `main` after 0.9.1 and the Phase 2.5 gate record)

**Created**: 2026-10-20

**Status**: Draft

**Input**: the maintainer, 2026-10-19: "caravana needs an mcp so agents like you can connect to
caravana and get the info needed". docs/plan.md, Phase 2.75: "026.5: an MCP server for Caravana,
so agents (Claude Code and others) can read and act on an installation through the API: list apps,
deployments and copies, read logs, the needs-you list, backups; deploy, promote, restart.
Read-only tools first; a deploy token limits it to one app. It goes through the API only
(constitution V), as `caravana mcp` over stdio (the binary run where the agent is, with an API
URL and token), so nothing new runs on the host." Why it matters now: in the last days, agents
working on Frunchy, the blog and Umbral had to ask the maintainer (or this repo's session) for an
installation's state: an app's status, the host's architecture, why a deploy failed. With this,
they look it up themselves.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - An agent reads an installation (Priority: P1)

The owner adds Caravana to their coding agent once (`caravana mcp`, the console's address and a
token). Then, in any session, the agent can answer "is umbral healthy?", "why did the last deploy
of lacalabaza fail?", "what needs me?", "when did hollo last back up?" by itself: it lists
projects and apps, reads an app (its definition, status and revision), its deployments (with
warnings, origin and outcome), its copies (with CPU and memory), its recent output, its backups,
volumes and recent actions, the needs-you list and the installation's version.

**Why this priority**: reading is what agents ask for most, and it can't break anything.

**Independent Test**: start `caravana mcp` against an installation with a full API token, list
the tools (only read tools), call the app list, an app, its deployments, its copies and its logs,
and get the same facts the console shows.

**Acceptance Scenarios**:

1. **Given** a token and the console's address, **When** an agent starts `caravana mcp`, **Then**
   it is offered read tools only, each described well enough to pick without reading docs.
2. **Given** an app, **When** the agent reads its deployments, **Then** it sees each one's
   status, warnings, origin ("from CI: commit on main") and the reason it failed.
3. **Given** an app whose output holds a line, **When** the agent reads the logs of one copy,
   **Then** it gets that copy's recent lines (up to the number it asked for, at most 500).
4. **Given** the address or token is wrong, or Caravana is down, **When** the agent calls a tool,
   **Then** it gets a clear error saying which (unreachable, refused token, not found), never a
   hang, and never the token.

---

### User Story 2 - An agent acts, when the owner allows it (Priority: P2)

Started with `--allow-actions`, the server also offers tools that change things: redeploy an app,
deploy a new image to a service, roll back to a past version, promote to another environment,
restart, stop and start an app, and back up a service now (waiting for a deployment to end is a
read tool, offered always). An agent
can then ship a fix and watch it reach healthy, or promote staging to production when asked.

**Why this priority**: useful, but every action changes a running installation, so it is opt-in.

**Independent Test**: with `--allow-actions`, deploy a new image to an app, wait for the
deployment, see it healthy; promote it to another environment; restart it.

**Acceptance Scenarios**:

1. **Given** the server started without `--allow-actions`, **Then** no tool that changes anything
   is offered, and calling one by name fails.
2. **Given** `--allow-actions`, **When** the agent deploys a new image to a service, **Then** only
   that image changes (the rest of the stored app is kept), and a concurrent edit is not
   overwritten (the deploy carries the revision it read).
3. **Given** a deployment in progress, **When** the agent waits for it, **Then** it returns when the
   deployment is healthy, failed, reverted or superseded, or when the wait it asked for ends, with
   the deployment as it then is.
4. **Given** each action tool, **Then** the agent's client can tell it changes things (and which
   ones stop service or replace data) before calling it.

---

### User Story 3 - The token decides what an agent can reach (Priority: P1)

A deploy token (one app of one project, feature 020) given to `caravana mcp` limits the agent to
that app, exactly as it limits CI. Caravana refuses everything else, and the agent is told why.
The server never sends the agent a secret value or a token, and it offers no tool that returns a command's output. Logs reach the model as they came, labelled as data.

**Acceptance Scenarios**:

1. **Given** a deploy token for app web of project shop, **When** the agent reads web, **Then** it
   works; **When** it lists every app or reads another one, **Then** it is told the token only
   reaches web of shop.
2. **Given** any tool, **Then** its answer holds no secret value (Caravana's API never returns
   one), no token, and nothing from Run a command or a terminal (those are not offered).
3. **Given** the server's own messages (standard error), **Then** they never hold the token or a
   tool's answer.

### Edge Cases

- The token is revoked while the agent works: the next call says the token was refused.
- A long log read: capped at 500 lines; the answer says it was cut when it was.
- A wait longer than the agent's client allows: the wait has its own limit (at most 10 minutes)
  and reports progress while it waits.
- A plain `http://` address to another machine: refused, since the token would travel in clear;
  `http://localhost` and other loopback addresses are allowed for local work.
- Caravana answers a redirect: not followed (the token must not go to another address).
- An older Caravana that lacks an endpoint: the tool says the installation doesn't offer it.

## Requirements *(mandatory)*

- **FR-001**: `caravana mcp` MUST serve the Model Context Protocol over standard input and output,
  calling only Caravana's public API (no Nomad, no database).
- **FR-002**: It MUST take the address and token from its environment, or the token from a file,
  never from a command-line argument (a process list shows those).
- **FR-003**: By default it MUST offer read tools only. Action tools MUST need `--allow-actions`.
- **FR-004**: Read tools MUST cover projects and their environments, apps (list and one), recent
  deployments, an app's deployments and one deployment, copies (with usage), logs, volumes,
  backups, recent actions, the needs-you list, the promotion preview, the exported jobspec, the
  kept Compose file, the installation's version, and waiting for a deployment to end (it only
  reads).
- **FR-005**: Action tools MUST cover redeploy, deploy an image to a service (keeping the rest of
  the app and sending the revision it read), redeploy a past version, promote (with the preview's
  revision), restart, stop, start and back up now.
- **FR-006**: Every tool MUST say whether it only reads, changes things, or stops service / replaces
  data, in the protocol's own hints.
- **FR-007**: Not offered: deleting anything, restoring a backup, Run a command, terminals, tokens,
  webhook tokens, registry, backup-destination and notification settings, passwords, and
  deploying a whole new definition or Compose file.
- **FR-008**: Caravana's refusals MUST reach the agent with their code, message and field; an
  unreachable installation or a refused token MUST be said plainly.
- **FR-009**: The token MUST NOT appear in any answer, error or log line; tool answers MUST NOT be
  logged.
- **FR-010**: Releases MUST include the binary for macOS (arm64 and amd64), so it runs where the
  agent runs.

## Success Criteria *(mandatory)*

- **SC-001**: An agent answers "is <app> healthy, and if not, why?" for any app on the
  maintainer's host with no human help.
- **SC-002**: With a deploy token, every tool outside that app's reach is refused, every time.
- **SC-003**: A sentinel token is found in no answer, error or log line of a full test session.
- **SC-004**: Setting it up in a coding agent takes one command and one token.

## Assumptions

- One owner per installation (Phase 4 adds roles); the token is the owner's choice of reach.
- Agents run on the owner's machine (or CI), not on the host.
- Logs are an app's own output and may say anything; the tools present them as data.
- Caravana's API stays the source of truth: a tool's answer is the API's answer.

## Out of Scope

- An `/mcp` endpoint on the API itself (for remote agents, with its own sign-in): later, if asked.
- MCP resources, prompts and sampling; only tools.
- Creating apps, editing definitions, secrets or Compose files through the agent.
