# Feature Specification: Rollback and export

**Feature Branch**: `011-rollback-export`
**Created**: 2026-10-08
**Status**: Implemented, v0.1.0
**Input**: "011 Rollback + export: redeploy any past version of an app (from the API and the console's Deployments tab), and export any app as a plain Nomad jobspec (HCL generated only by this feature, per constitution VI) so uninstalling Caravana leaves the user with a runnable job."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Go back to a version that worked (Priority: P1)

An owner deployed a change that passes its health check but is wrong: a bad config value, a buggy
release under the same tag. They open the app's Deployments tab, pick an earlier deployment that was
healthy, and redeploy it. Caravana runs exactly what ran then: the same images, pinned by digest, and
the same definition (env, domains, health checks, volumes, backup policy). It is a new deployment,
recorded in the history like any other, and the app now holds that definition, so the next redeploy
starts from it. A client of the API does the same with one call.

**Why this priority**: this is the "sleep well" promise. A deploy that can't be undone in one action
is a deploy people fear.

**Independent Test**: deploy version A, then version B with a different image and env value, then
redeploy A from its history entry. The app serves A's image with A's env value, the history shows a
new deployment that names A as its source, and the stored definition is A's.

**Acceptance Scenarios**:

1. **Given** an app with a healthy deployment A and a later deployment B, **When** the owner
   redeploys A, **Then** a new deployment starts with A's images (the same digests, not the tags
   resolved again) and A's definition, and it is recorded as a redeploy of A.
2. **Given** that redeploy finished healthy, **When** the owner opens the app, **Then** its
   definition is A's, and a plain redeploy or a Configure change starts from A's definition.
3. **Given** deployment A used a secret that has since been removed, **When** the owner redeploys
   A, **Then** it is refused before anything changes, naming the missing secret, and saying to set
   it first.
4. **Given** the current definition has a secret that A doesn't use, **When** the owner asks to
   redeploy A, **Then** the confirmation and the deployment's warnings say that secret's value will
   be removed.
5. **Given** an image digest A used is no longer in its registry, **When** the owner redeploys A,
   **Then** it is refused before anything changes, naming the service and the image.
6. **Given** a deployment that failed, was reverted, or was superseded, **When** the owner looks
   at it in the history, **Then** it offers no redeploy action, and the API refuses a redeploy of
   it with a clear error.

---

### User Story 2 - Take the app with you as a Nomad jobspec (Priority: P2)

An owner wants to know they are not locked in, or wants to run the app on a Nomad cluster
without Caravana. From the app's Configure tab, or with one API call, they download the app's live
version as a plain Nomad jobspec in HCL. The file says what it needs from the cluster that
Caravana set up (Traefik for routing, the host volumes, the secrets' variable paths and names), and
holds no secret values. `nomad job run` on that file runs the same app.

**Why this priority**: "no lock-in" is a product principle, but owners use it far less often than
rollback.

**Independent Test**: deploy a multi-service app with a volume and a secret, export it, and have
Nomad parse the file. The parsed job is the job Caravana registered for the live deployment, and
the file contains no secret value.

**Acceptance Scenarios**:

1. **Given** an app with a healthy deployment, **When** the owner exports it, **Then** they get
   one HCL file for that deployment's job, named after the app, with the images pinned by digest.
2. **Given** the exported file, **When** Nomad plans it against the running cluster, **Then** it
   reports no change to what runs.
3. **Given** an app with secrets, **When** it is exported, **Then** the file names the secrets and
   their variable paths and contains none of their values.
4. **Given** an app that has never had a healthy deployment, **When** the owner asks to export it,
   **Then** it is refused with a clear error saying there is no live version yet.

---

### User Story 3 - Export an earlier version (Priority: P3)

An owner wants the jobspec of a version that ran before, to compare or to run it elsewhere. From a
history entry, or the API with a deployment ID, they export that deployment's job.

**Why this priority**: useful for audits and comparisons, and nearly free once stories 1 and 2
exist, but nobody's work is blocked without it.

**Independent Test**: export deployments A and B of the same app. Each file holds its own images
and env.

**Acceptance Scenarios**:

1. **Given** deployment A that has a recorded definition, **When** the owner exports A, **Then**
   the file holds A's images and definition, whatever the app's current state.

---

### Edge Cases

- Deployments recorded before this feature have no definition saved with them. They show in the
  history without a redeploy or export action, and the API answers a clear error naming why.
- The owner redeploys the deployment that is already live. That is allowed and is a plain new
  deployment of the same version, like a redeploy today.
- A deploy is running when the owner redeploys an older version. The newest request wins, as for
  any deploy today.
- The redeployed version's definition fails a rule that changed since (a domain now taken by
  another app, a setting Caravana no longer accepts). It is refused before anything changes, naming
  the field, as a fresh deploy would be.
- A webhook fires after a rollback. It resolves the definition's tags again, as it always does, so
  it may bring the newer image back. The redeploy confirmation says so.
- The app is deleted. Its saved definitions go with its deployment history. Exports already
  downloaded are the owner's.
- The export is asked for on the webhook-only listener. It isn't served there, like every other
  route but the webhook call.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Every deployment MUST record the full app definition it deployed, together with its
  image digests, from this feature on.
- **FR-002**: The owner MUST be able to redeploy any earlier healthy deployment that has a recorded
  definition, from the API and from that deployment's entry in the console's history.
- **FR-003**: A redeploy of a past deployment MUST deploy that deployment's definition with its
  recorded digests, without resolving tags again, as a new deployment and a new Nomad job version.
- **FR-004**: A redeploy of a past deployment MUST be recorded with the deployment it came from,
  and the history MUST show that source.
- **FR-005**: Once a redeploy of a past deployment starts, the app's stored definition MUST be that
  deployment's definition.
- **FR-006**: A redeploy of a past deployment MUST be refused, before anything changes, when a
  secret it uses has no value now, naming the secret.
- **FR-007**: A redeploy of a past deployment MUST be refused, before anything changes, when one of
  its image digests is no longer in its registry, naming the service.
- **FR-008**: A redeploy of a past deployment MUST be validated like any deploy, and refused naming
  the field when it fails today's rules.
- **FR-009**: When a redeploy of a past deployment removes secrets the current definition has, the
  console confirmation MUST list them before the owner confirms, and the deployment's warnings MUST
  name them.
- **FR-010**: Redeploy MUST be refused for a deployment that is not healthy, or that has no
  recorded definition, with an error that says which.
- **FR-011**: The owner MUST be able to export the app's live version, meaning its newest healthy
  deployment, as one Nomad jobspec in HCL, from the API and the console's Configure tab.
- **FR-012**: The owner SHOULD be able to export any past deployment that has a recorded
  definition, from the API and its history entry.
- **FR-013**: The exported jobspec MUST be the job Caravana registers for that deployment, so that
  Nomad parses it into that job and a plan against the live job shows no change to what runs.
- **FR-014**: The exported file MUST start with a comment that says what it needs from the cluster:
  the Traefik routing it relies on, each host volume, and each secret's name and variable path.
- **FR-015**: The exported file MUST NOT contain any secret value or token.
- **FR-016**: Export MUST be refused, with a clear error, for an app with no healthy deployment or a
  deployment with no recorded definition.
- **FR-017**: `api/openapi.yaml` MUST describe the redeploy and export operations and their errors
  in the same change.

### Key Entities

- **Deployment** (existing): gains the full app definition it deployed and, for a redeploy of a
  past version, the ID of the deployment it came from.
- **Jobspec export**: an HCL file of one deployment's Nomad job with a leading comment listing what
  it needs from the cluster. It is produced on request and never stored.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An owner goes back to an earlier healthy version with one action and one confirmation,
  from the app's history or with one API call.
- **SC-002**: After a redeploy of version A, the running images are A's digests and the env is A's,
  every time, whatever the tags point at now.
- **SC-003**: Nomad parses every exported file into the job Caravana registered for that deployment,
  and a plan of it against the live app reports no change to what runs.
- **SC-004**: No exported file, API answer or log line holds a secret value.
- **SC-005**: Every refused redeploy names the reason (missing secret, missing image, failed rule,
  not healthy, no recorded definition) before anything changes.

## Assumptions

- Only healthy deployments are redeploy targets. Failed, reverted and superseded ones never ran
  properly, so going back to them makes no sense.
- Secret values are not versioned. A redeploy of a past version uses the secrets' current values and
  the past version's secret names. This matches Constitution III, where secrets are declarative.
- Saved definitions are kept for as long as the app exists. They are small, and the history is
  already kept that long.
- Deployments from before this feature are not backfilled: their definitions were never saved, and
  the stored app may have changed since.
- The export holds the app's own job only. Backup jobs from feature 010 are separate Nomad jobs that
  keep running if Caravana is uninstalled, and they are not exported.
- HCL is the export format. Nomad also runs the JSON Caravana submits, but HCL is what people read
  and edit.
- The export is the job Caravana asked Nomad to run, not the job Nomad stores with its defaults
  filled in, so the file stays short and readable.
