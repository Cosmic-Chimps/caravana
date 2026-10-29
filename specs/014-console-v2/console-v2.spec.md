# Feature Specification: Console v2

**Feature Branch**: `014-console-v2`
**Created**: 2026-10-12
**Status**: Implemented, v0.2.0
**Input**: "Console v2 (docs/plan.md item 012.5): bring the web console up to what the design
handoff shows for a single-node install, so the maintainer (now a daily user) never needs curl or
deploy.sh for routine changes. Four parts, all through internal/service so the API keeps parity:
(1) edit the definition on the Configure tab (image, env, domains, copies, health check, CPU and
memory per service), save and deploy in one action; (2) paste a Compose file on New app, with the
overlay filled in the console and a preview, and redeploy an existing compose app from a pasted
file; (3) CPU and memory per copy on the Running tab, live, against reservation and ceiling;
(4) a 'needs you' list on the Apps home: failed deploys, unhealthy or restarting copies, failed
backups, each with the fix one click away. Out of scope: registry credentials, canary-first, git
or binary sources, projects/environments, size presets, multi-node, metrics history beyond what
Nomad keeps live."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Change an app from its Configure tab (Priority: P1)

The owner wants a new image tag, one more environment variable, a second domain, two copies
instead of one, or more memory for a service. Today the Configure tab only shows the definition;
the change means writing JSON and calling the API with curl. With this feature the owner opens
Configure, changes the fields of the service, and presses one button that saves the definition
and deploys it. The page then shows the deployment rolling out, as a deploy from the Deployments
tab does.

**Why this priority**: it is the change the owner makes most often, and the roughest part of daily
use today.

**Independent Test**: on an app made from an image, change the image tag, add an env var and set
memory to 512 MB from Configure. A new deployment starts, becomes healthy, and the stored
definition and the running job carry all three changes; nothing else in the definition changed.

**Acceptance Scenarios**:

1. **Given** a running app, **When** the owner changes a service's image, env, domains, container
   port, copies, health check, CPU or memory on Configure and saves, **Then** one deployment
   starts with the edited definition and the owner lands where its progress shows.
2. **Given** a service with env vars `A` and `B`, **When** the owner removes `B` and saves, **Then**
   the deployed service has only `A` (the edit replaces the whole list).
3. **Given** a service with secrets, **When** the owner saves an edit that doesn't touch them,
   **Then** every secret keeps its current value.
4. **Given** a service with volumes or a backup, **When** the owner saves an edit, **Then** the
   volumes and the backup stay as they were (the form doesn't offer them, and doesn't drop them).
5. **Given** an invalid value (a reservation above the ceiling, a domain used by another app, an
   image reference that can't be parsed, a health path without a leading `/`), **When** the owner
   saves, **Then** nothing is deployed, the form comes back with every typed value kept, and the
   message sits at the field it is about.
6. **Given** the definition changed after the owner opened the form (a webhook, an API call or
   another tab deployed), **When** the owner saves, **Then** the save is refused with a message
   saying the app changed since the form was opened, and the form shows the current definition
   next to what was typed. Nothing is deployed.
7. **Given** an image whose tag doesn't exist in its registry, **When** the owner saves, **Then**
   the deploy fails as an API deploy would today, and the message names the image field.

---

### User Story 2 - Create an app from a pasted Compose file (Priority: P2)

The owner has a `compose.yaml` that runs with `docker compose up`. On New app they choose
"Compose file", give the app a name and paste the file. They first see a preview: the services it
becomes, with image, ports, volumes, memory and copies, and every warning and refusal naming the
Compose path it is about (`services.web.build`). For each service with ports they set its domains
and health check; for each secret the file asks for they type the value. Then one button creates
the app and deploys it.

**Why this priority**: Compose is the main input of Phase 2 and the console can't take it today;
the Hollo deploy needed curl and a script.

**Independent Test**: paste the Hollo example's Compose file with its domains, health check and
secrets set in the console. The app created is the same app the API makes from the same file and
overlay, and it deploys healthy.

**Acceptance Scenarios**:

1. **Given** a Compose file Caravana can honor, **When** the owner previews it, **Then** they see
   each service it becomes and every warning, and nothing is saved or deployed yet.
2. **Given** a Compose file with something Caravana refuses, **When** the owner previews it,
   **Then** the preview lists each refusal naming its Compose path, and creating is not offered
   until the file changes.
3. **Given** a previewed file with domains, health checks and secret values filled in, **When**
   the owner creates the app, **Then** the app is saved and its first deployment starts, and the
   owner lands on its deployment progress.
4. **Given** a name already taken by another app, **When** the owner previews or creates, **Then**
   the name field says so and nothing is saved.
5. **Given** the single-image form, **When** the owner uses New app as today, **Then** it works as
   before; Compose is a second choice on the same page.

---

### User Story 3 - Redeploy an existing Compose app from a pasted file (Priority: P3)

The owner changed their Compose file (a new image version, one more service, a removed env var).
On the app's Configure tab they choose "Deploy from Compose", paste the new file and see the same
preview as on New app, plus which services would be added or removed. The overlay starts from
the app's current domains, health checks and secret names, so they only type what is new. One
button deploys it.

**Why this priority**: it completes the Compose round trip without curl, but owners can already
edit fields with Story 1.

**Independent Test**: paste the Hollo file with a newer image version for one service. The preview
shows one changed image and no added or removed service; after deploy, the running job has the
new image and the other service is unchanged.

**Acceptance Scenarios**:

1. **Given** an existing app, **When** the owner pastes a file and previews it, **Then** the overlay
   is prefilled from the app's current domains, health checks and secret names, and the preview
   names each service that would be added or removed.
2. **Given** a pasted file without a service the app has today, **When** the owner deploys it,
   **Then** that service is removed, as a Compose deploy through the API does; the preview said so
   before.
3. **Given** a secret name already set, **When** the owner leaves its value empty, **Then** the
   secret keeps its current value.

---

### User Story 4 - See each copy's CPU and memory (Priority: P3)

The owner opens an app's Running tab and sees, for each running copy, how much CPU and memory it
uses now, next to what the service reserves and, for memory, the ceiling it may not pass. A copy
close to its memory ceiling is marked as a warning. The numbers refresh on their own while the
page is open.

**Why this priority**: after 013 a service may burst past its reservation, and the owner can't see
how close a copy is to being killed without opening Nomad.

**Independent Test**: run a service with a 256 MB reservation and a 300 MB ceiling and make it use
280 MB. The Running tab shows about 280 MB "of 300 MB ceiling" with a warning mark, and the
number changes within the refresh interval when usage changes.

**Acceptance Scenarios**:

1. **Given** a running copy, **When** the owner opens the Running tab, **Then** the copy shows its
   current CPU use against the service's CPU reservation, and its current memory use against
   the reservation and the ceiling.
2. **Given** a copy using 90 % or more of its memory ceiling, **When** it is shown, **Then** it
   carries a warning mark and the words say it is close to its ceiling.
3. **Given** the Running tab is open with live updates, **When** usage changes, **Then** the
   numbers update within 10 seconds without reloading the page.
4. **Given** a copy that isn't running (starting, stopped, failed) or whose usage Nomad can't give
   right now, **When** it is shown, **Then** its usage says so in words instead of a number, and
   the rest of the page still shows.
5. **Given** an API client, **When** it lists an app's copies, **Then** it can get the same usage
   numbers.

---

### User Story 5 - A "needs you" list on the Apps home (Priority: P2)

The owner opens the console and sees first what only they can decide: an app whose last deploy
failed or was reverted, a copy that is unhealthy, restarting over and over or killed for running
out of memory, a backup that failed. Each item is one plain sentence naming the app and the
service, says when it happened, and has one action that takes the owner to the fix. When the
problem is gone, the item is gone; when nothing needs them, the home says so.

**Why this priority**: the owner runs real apps now; a failure must reach them without opening
every app.

**Independent Test**: deploy an image whose health check fails, so the deploy is reverted. Within
30 seconds the home shows one item for it with an action to its deployment. Deploy a working
image; the item disappears.

**Acceptance Scenarios**:

1. **Given** an app whose latest deployment failed or was reverted, **When** the owner opens the
   home, **Then** an item says which app, which version and what happened, with an action to that
   deployment, from where the owner can redeploy the last healthy version.
2. **Given** a copy that is unhealthy, or restarted 3 or more times in its current version,
   **When** the owner opens the home, **Then** an item names the app and service, with an action
   to that service's logs.
3. **Given** a copy whose last restart was because it ran out of memory, **When** shown, **Then**
   the item says so and its action leads to the service's memory on Configure.
4. **Given** a service whose latest backup or restore failed, **When** shown, **Then** an item says
   so with an action to the app's Backups tab, where "Back up now" is.
5. **Given** several items, **When** shown, **Then** they are newest first, and each problem
   appears once (a failed deploy and its unhealthy copies are one item).
6. **Given** a problem that is resolved (a later deploy is healthy, the copy recovered, a later
   backup succeeded), **When** the home refreshes, **Then** its item is gone, with no action from
   the owner.
7. **Given** the home is open with live updates, **When** an item appears or goes away, **Then**
   the list updates within 30 seconds without reloading.
8. **Given** an API client, **When** it asks for what needs attention, **Then** it gets the same
   items the home shows.

---

### Edge Cases

- An app made from Compose is edited on Configure: allowed; Configure says the app came from a
  Compose file and that deploying a file again replaces these edits.
- An app with several services: Configure shows one editable section per service; adding or
  removing a service is done with a Compose file or the API, not the form.
- A Compose service with a volume runs one copy; the copies field for it can't go above 1, and
  saying more is refused naming the field.
- The owner edits while a deploy is rolling out: saving is allowed, and the newest deploy wins, as
  for the API today.
- The Compose file is larger than the API accepts, or isn't YAML: the preview says so at the
  file field.
- Secret values typed on a Compose form are never written back into the page; when a create fails
  after preview, the form asks for the secret values again and says why.
- Nomad is unreachable: Configure still shows the stored definition and refuses to save with the
  existing "Nomad unavailable" message; the Running tab and the needs-you list say they can't
  read live state now instead of showing stale numbers as current.
- A "needs you" item for an app that is deleted disappears with the app.
- The backup destination was never configured but a service asks for backups: shown once as an
  item with an action to Settings › Backup destination.
- Without JavaScript every form works as plain HTML posts and the pages show a snapshot; only the
  automatic refresh needs JavaScript.

## Requirements *(mandatory)*

### Functional Requirements

**Edit the definition**

- **FR-001**: The Configure tab MUST let the owner edit, per service: image, environment variables
  (add, change, remove), container port, domains (add, remove), copies, health check (path or
  TCP, interval, timeout), CPU reservation, memory reservation and memory ceiling.
- **FR-002**: Saving MUST store the edited definition and start one deployment with it, as the
  API's deploy does; the owner MUST land where that deployment's progress shows.
- **FR-003**: Fields the form doesn't offer (command, args, volumes, backup, secrets, extra ports)
  MUST be kept unchanged by a save. Secrets keep their values and stay managed by the existing
  Secrets section.
- **FR-004**: Every field the form offers MUST be replaced as a whole by what the form sends: an
  env var or domain removed in the form is removed from the app.
- **FR-005**: Invalid input MUST be refused before anything is stored or deployed, with the message
  at the field it names; every typed value MUST be kept in the form that comes back.
- **FR-006**: A save MUST be refused when the stored definition changed after the form was opened,
  and the owner MUST see that it changed. The API MUST offer the same protection to its clients as
  an option.

**Compose**

- **FR-007**: New app MUST offer "Compose file" next to the single-image form: an app name, the
  pasted file, and a preview step before anything is saved.
- **FR-008**: The preview MUST show each service the file becomes and every warning and refusal,
  each naming its Compose path, using the same rules as the API's Compose preview.
- **FR-009**: After a preview without refusals, the owner MUST be able to set, per service with
  ports, its domains and health check, and for each secret a value, then create and deploy in
  one action.
- **FR-010**: Configure MUST offer "Deploy from Compose" for an existing app, with the overlay
  prefilled from the app's current domains, health checks and secret names, and a preview that
  also names the services that would be added or removed.
- **FR-011**: A Compose secret left empty MUST keep its current value when the secret already
  exists, and MUST be refused naming the secret when it doesn't.
- **FR-012**: Secret values typed in the console MUST NOT be written back into any page, log or
  stored definition.

**Usage per copy**

- **FR-013**: The Running tab MUST show for each running copy its current CPU use against the
  service's CPU reservation, and its current memory use against its reservation and ceiling.
- **FR-014**: A copy at 90 % or more of its memory ceiling MUST carry a warning mark and words.
- **FR-015**: With live updates on, usage MUST refresh at least every 10 seconds while the tab is
  open, and MUST NOT be read while no one is looking.
- **FR-016**: When usage can't be read for a copy, the copy MUST say so in words; the page MUST NOT
  fail.
- **FR-017**: The API MUST offer the same usage for an app's copies.

**Needs you**

- **FR-018**: The Apps home MUST open with a "needs you" list built from: an app's latest
  deployment failed or reverted; a copy unhealthy, or restarted 3 or more times in its current
  version, or last restarted for running out of memory; a service's latest backup or restore
  failed; backups asked for but no destination configured.
- **FR-019**: Each item MUST be one sentence naming the app (and service where it applies), say
  when it happened, and have one action leading to the fix: the deployment, the service's logs,
  the service's memory on Configure, the Backups tab, or Settings › Backup destination.
- **FR-020**: Items MUST be ordered newest first, one item per problem, and MUST disappear by
  themselves once the condition is no longer true. They are derived from current state, not
  dismissed by hand.
- **FR-021**: When nothing needs the owner, the home MUST say so in one line.
- **FR-022**: With live updates on, the list MUST update within 30 seconds of a change.
- **FR-023**: The API MUST offer the same list.

**All parts**

- **FR-024**: Every change the console makes MUST go through the same service layer as the API;
  every new read or change MUST have its API twin in the contract in the same change.
- **FR-025**: Every page and form MUST work without JavaScript; live refresh is the only thing that
  needs it.
- **FR-026**: Styles MUST follow `DESIGN.md` (status as shape and color, one sentence per problem).

### Key Entities

- **Definition edit**: the stored app with the form's fields replaced per service, and a marker of
  which stored definition it was based on, so a stale save can be refused.
- **Compose draft**: an app name, the pasted file and the overlay (per service domains and health,
  secret values); never stored before create, secret values never echoed.
- **Copy usage**: for one copy at one moment: CPU used, memory used, and the service's CPU
  reservation, memory reservation and ceiling; or the reason it can't be read.
- **Attention item**: kind (deploy failed, copy unhealthy, restarting, out of memory, backup failed,
  restore failed, no backup destination), app, service, when, one sentence, and the place of the
  fix. Derived from current state; nothing new is stored for it.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: The owner changes an image tag, adds an env var, adds a domain or raises memory from
  the console in under one minute, with no curl or script, for each of these four changes.
- **SC-002**: The Hollo example can be created, and later updated to a new image version, entirely
  from the console, and the result is the same app the API makes from the same file and overlay.
- **SC-003**: A deploy that fails or is reverted shows on the home within 30 seconds, and its item
  is gone within 30 seconds of a healthy deploy.
- **SC-004**: Usage per copy on the Running tab is never more than 10 seconds old while live
  updates are on.
- **SC-005**: No save from the form loses a field it doesn't show (volumes, backup, secrets,
  command, args): checked on every example app the project ships.
- **SC-006**: Every console action of this feature can also be done through the documented API.

## Assumptions

- Single node, as in all of Phase 2; usage and attention come from one Nomad cluster.
- Usage is what Nomad reports for a running copy now; no history is kept (no sparklines over
  time, no "peak over 7 days").
- "Restarting over and over" means 3 or more restarts of a copy in its current job version; the
  number can be tuned in the plan.
- The 90 % warning applies to the memory ceiling, which is the limit that kills a copy; CPU over
  its reservation is normal and gets no warning.
- Attention items aren't stored, acknowledged or notified by email; that belongs with
  notifications in a later phase.
- Editing on Configure covers existing services only; adding or removing services is done with
  Compose or the API.
- Compose files are not stored; "Deploy from Compose" needs the file pasted again.
- The restart button, canary-first and registry credentials from the design handoff are not part
  of this feature.
