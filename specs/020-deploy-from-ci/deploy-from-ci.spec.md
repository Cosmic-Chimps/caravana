# Feature Specification: Deploy from CI (caravana-action)

**Feature Branch**: `020-deploy-from-ci` (stacked on `020.5-app-actions`); the action lives in its
own repository, `Cosmic-Chimps/caravana-action`

**Created**: 2026-10-18

**Status**: Draft

**Input**: the maintainer, 2026-10-18: build from source is the heaviest thing for Caravana; let CI
build the image, and instead of every repository carrying a deploy script (Umbral's
`caravana-deploy.sh`), publish a GitHub Action, `caravana-action`. Caravana gets what the action
needs: a token that can deploy only one app, and a way to start from a GitHub repository.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deploy from a workflow in a few lines (Priority: P1)

The owner has a repository with a Dockerfile. Their workflow already builds and pushes the image.
They add one step, `uses: Cosmic-Chimps/caravana-action@v1`, with Caravana's address, a token
from the repository's secrets, the app and the image the build just pushed. On every push the step
deploys exactly that image, waits until the app is healthy, prints the deploy's warnings and
fails the workflow if the deploy fails or is reverted, with the reason.

**Why this priority**: it is the Phase 2.5 gate's path (a git-built app, deployed) without
Caravana ever building.

**Independent Test**: a workflow (or the action's script run locally with the same inputs) deploys
a new image to an existing app: the step succeeds when the app is healthy and fails with the
reason when the image's health check never passes.

**Acceptance Scenarios**:

1. **Given** a defined app, **When** the step runs with an image, **Then** the app is deployed with
   that image (its other settings unchanged), and the step ends when the deploy is healthy.
2. **Given** a defined app, **When** the step runs without an image, **Then** the app is redeployed
   as stored (its tags looked up again).
3. **Given** an app that doesn't exist yet or has no definition, **When** the step runs with a
   definition file, **Then** the app is created or defined from it and deployed.
4. **Given** a deploy that fails or is reverted, **When** the step waits for it, **Then** the step
   fails with the deploy's reason; the job summary shows the deployment, its version and status.
5. **Given** any run, **When** the step talks to Caravana, **Then** the token never appears in the
   log, a process list or the summary.

---

### User Story 2 - A token that can only deploy one app (Priority: P1)

The owner creates, for one app, a deploy token to paste into the repository's secrets. If it
leaks, it can deploy and promote that app and read its deployments, nothing else: not other apps,
not secrets of other apps, not settings, not tokens.

**Why this priority**: today the only token for CI is an API token that can do everything.

**Independent Test**: a deploy token for `shop/web` deploys `shop--staging--web` and promotes it to
production; it gets 403 for another app, for Settings' endpoints and for creating tokens.

**Acceptance Scenarios**:

1. **Given** an app, **When** the owner creates a deploy token for it (console or API), **Then**
   the token is shown once and covers that app's name in every environment of its project.
2. **Given** a deploy token, **When** it is used for anything outside its app (another app, the
   apps list, settings, tokens, run a command, stop or delete), **Then** the answer is 403 naming
   the token's scope, and nothing changes.
3. **Given** a deploy token, **When** it deploys its app, promotes it between environments, or
   creates it without deploying in an existing environment of the project, **Then** it works.
4. **Given** deploy tokens, **When** the owner lists tokens, **Then** each shows its app, last use
   and expiry, and can be revoked.

---

### User Story 3 - Where a deploy came from (Priority: P2)

A deployment started by the action shows "from CI: commit `abc1234` on `main`, by ana, run #212",
linking to the workflow run, in the app's history and in its notification.

**Acceptance Scenarios**:

1. **Given** a deploy started by the action, **When** the owner looks at the history, **Then** it
   shows the commit, the branch or tag, who pushed it, and a link to the run.
2. **Given** a deploy without that information (console, webhook, plain API), **When** shown,
   **Then** it reads as today.

---

### User Story 4 - Start from a GitHub repository (Priority: P2)

On New app the owner picks "From a GitHub repository", names the app (and its project and
environment), and gets: the app created without deploying (019.5), a deploy token for it, and the
workflow to paste, with the app, the address and the secret name filled in, plus the definition
file to commit, built from what they typed (port, domain, health path, image name).

**Acceptance Scenarios**:

1. **Given** the form filled in, **When** submitted, **Then** the app exists without a deployment,
   the token is shown once, and the page shows the workflow and the definition file to copy.
2. **Given** that workflow committed with the token as a secret, **When** CI runs, **Then** the app
   is defined and deployed by the action's first run.

---

### User Story 5 - Promote from the workflow (Priority: P3)

After deploying to staging, a later job (perhaps behind a GitHub environment approval) runs the
action with `promote: production`; production runs staging's healthy deployment by digest.

### Edge Cases

- The action can't reach Caravana, or Caravana answers 401/403: the step fails at once, saying
  which input to check.
- The app is stopped (020.5): the step fails with "the app is stopped: start it first".
- An `image` for an app with more than one service needs `service:` to say which; otherwise the
  step fails naming the input.
- A deploy still running after the timeout: the step fails saying so; Caravana goes on.
- Two pushes close together: Caravana's newest deploy wins (003); the older step ends with its
  deployment `superseded` and fails or passes as the owner chooses (`fail-on-superseded`, default
  false).

## Requirements *(mandatory)*

- **FR-001**: A public GitHub Action MUST deploy a Caravana app from a workflow with inputs for the
  address, the token, the app (ID, or project, environment and name), an optional image (and
  service), an optional definition file, whether to wait and for how long, and an optional
  environment to promote to.
- **FR-002**: The action MUST wait for the deployment's final status, show its warnings, fail the
  step on failed or reverted, and expose the deployment ID, job version and status as outputs.
- **FR-003**: The action MUST never print or pass its token where it can be seen (log, process
  list, summary, outputs).
- **FR-004**: Caravana MUST offer deploy tokens scoped to one app name in one project (every
  environment), allowed only to read that app and its deployments, deploy it, create it without
  deploying in an existing environment, preview and run promotions between its environments; any
  other request with it is 403.
- **FR-005**: A deploy MAY carry where it came from (commit, ref, actor, run URL); Caravana MUST
  record it on the deployment, show it in the history and notifications, and never require it.
- **FR-006**: New app MUST offer "From a GitHub repository": create the app without deploying, make
  a deploy token, and show the workflow and the definition file to commit.
- **FR-007**: The console and the API MUST offer the same: deploy tokens are created, listed and
  revoked through both.

## Success Criteria *(mandatory)*

- **SC-001**: A repository with a Dockerfile goes from nothing to deployed on Caravana by adding
  one workflow step and one secret.
- **SC-002**: A leaked deploy token can't change anything outside its one app.
- **SC-003**: Umbral's release workflow replaces its deploy script with the action and keeps
  working.

## Assumptions

- GitHub Actions first; the action's script is plain shell, so GitLab CI or others can call it
  later.
- The action is versioned on its own (`v1`, moving major tag) and talks to API 0.18 and later;
  it says which Caravana it needs.
- Building on the host is Phase 3 ("Build on a build server").

## Out of Scope

- Building images (CI does it); a registry on the host.
- GitHub App installation, OAuth with GitHub, reading repositories from Caravana.
- Preview environments per pull request (Phase 4).
