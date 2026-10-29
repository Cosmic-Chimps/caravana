# Feature Specification: Keep and edit the Compose file

**Feature Branch**: `015-compose-file-edit`
**Created**: 2026-10-14
**Status**: Implemented, v0.3.0
**Input**: "Keep and edit the Compose file (feature 015, from the maintainer's daily use: Uptime
Kuma deployed from a Compose file can't be edited as a file, only field by field or by pasting
the whole file again). Caravana keeps the Compose file and its overlay (domains, health, secret
names, backup; never secret values) with a Compose-made app and with every deployment made from
one. Configure for such an app shows the file in an editor, prefilled, with the overlay fields
beside it, the existing preview, and Deploy (conditional on the app's revision, as 014's forms
are); the per-field edit form becomes read-only for these apps so the file is the one thing
edited. Redeploying a past version brings its file back as the stored one. Secret set/remove on
Configure, webhook redeploys and plain redeploys keep the stored file. Apps made from Compose
before this feature have no stored file: Configure asks for it to be pasted once, after which it
is kept. The API returns the stored file and overlay (parity with the console). A deploy of a full
definition through the API (deployApp with a body) on an app with a stored file drops the file and
says so in the deployment's warnings. Out of scope: volume backups and the volumes panel (feature
016), a code editor with syntax highlighting (plain textarea, no Node build), storing files of
image-made apps."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Edit the Compose file an app was made from (Priority: P1)

The owner made Uptime Kuma from a Compose file. Weeks later they want a newer image tag and one
more environment variable. They open Configure and find the file they deployed, as they wrote it
(comments included), with the domains, health check, secret names and backup beside it. They
change two lines, preview, and deploy. Next time, the file they open is the one they just
deployed.

**Why this priority**: it is the gap the maintainer hit on the first real app: today the file is
thrown away, so the only ways to change a Compose app are a field form that doesn't match the file
or pasting the whole file again from somewhere else.

**Independent Test**: create an app from a Compose file with a comment in it. Open Configure: the
file is there, comment and all. Change the image tag, preview, deploy. The deployment is healthy,
the running copy has the new image, and Configure now shows the edited file.

**Acceptance Scenarios**:

1. **Given** an app created from a Compose file (console or API), **When** the owner opens
   Configure, **Then** the file is shown exactly as last deployed, and the overlay fields hold the
   app's domains, health checks, secret names (values empty) and backup settings.
2. **Given** the owner edits the file, **When** they preview, **Then** they see the same preview as
   on New app (services, warnings and refusals naming the Compose path, services added and
   removed), and nothing is saved.
3. **Given** a previewed edit without refusals, **When** the owner deploys, **Then** one deployment
   starts with the edited file and overlay, and after it the stored file is the edited one.
4. **Given** the app changed since the owner opened Configure (a deploy from the API or another
   tab), **When** they deploy, **Then** it is refused as stale, nothing is deployed, and their edit
   is still in the editor.
5. **Given** an app with a stored file, **When** the owner looks at the per-field definition on
   Configure, **Then** it is read-only, with a line saying the app is edited through its Compose
   file.
6. **Given** a secret left empty in the overlay, **When** the owner deploys, **Then** the secret
   keeps its current value.

---

### User Story 2 - Apps made from Compose before this feature (Priority: P2)

Uptime Kuma was created before Caravana kept files, so there is nothing to show. Configure says
the file wasn't kept and offers an empty editor with the overlay prefilled from the app. The owner
pastes the file once and deploys; from then on it is kept like any other.

**Why this priority**: every Compose app on the maintainer's host today is in this state; without
this path they would never get the P1 experience.

**Independent Test**: take an app made from Compose with no stored file. Configure says so and
offers the editor with domains, health and secret names prefilled. Paste the file, deploy. Reload
Configure: the file is shown.

**Acceptance Scenarios**:

1. **Given** a Compose-made app with no stored file, **When** the owner opens Configure, **Then**
   the page says "This app's Compose file wasn't kept. Paste it once to edit it here from now on."
   with the editor empty and the overlay prefilled from the app.
2. **Given** the owner pastes and deploys, **When** they open Configure again, **Then** the file is
   shown and the per-field form is read-only.
3. **Given** a Compose-made app with no stored file, **When** the owner doesn't paste anything,
   **Then** the per-field form stays editable, as in feature 014.

---

### User Story 3 - The file follows the app's history (Priority: P2)

The owner deploys a broken edit of the file and rolls back to the version that worked. The
stored file goes back to the one that version was deployed with, so the next edit starts from
what is running, not from the broken file. Each history entry made from a file lets the owner
download that file.

**Why this priority**: rollback (feature 011) restores the app; if it left the broken file in the
editor, the next edit would redeploy the broken change.

**Independent Test**: deploy file A, then file B. Redeploy A's version. Configure shows file A. The
history entries of A and B each offer their own file for download.

**Acceptance Scenarios**:

1. **Given** two deployments made from files A and B, **When** the owner redeploys A's version,
   **Then** the stored file becomes A, with A's overlay.
2. **Given** a deployment made from a file, **When** the owner downloads it from the history,
   **Then** they get that file, byte for byte as deployed.
3. **Given** a deployment not made from a file (an image app, or a Compose app before this
   feature), **When** the owner looks at its history entry, **Then** no file download is offered.

---

### User Story 4 - Everything else keeps the file, and the API sees it (Priority: P3)

The owner sets a secret from Configure's Secrets section, a webhook redeploys the app, or someone
presses Deploy on the Deployments tab: the stored file stays. An API client can read the stored
file and overlay, and the files of past deployments. A client that deploys a full definition
instead (the API's `deployApp` with a body) replaces what the file described; the file is dropped
and the deployment's warnings say so.

**Why this priority**: it keeps the file trustworthy; the main paths (P1–P3) already work without
it, but a file that silently drifted from the app would be worse than none.

**Independent Test**: on an app with a stored file, set a secret, trigger the webhook and redeploy;
the file is unchanged after each. Then deploy a definition through `deployApp`: the deployment has
the warning, and Configure shows the per-field form editable and the "not kept" note.

**Acceptance Scenarios**:

1. **Given** an app with a stored file, **When** a secret is set or removed from Configure,
   **Then** the stored file is unchanged and the overlay's secret names follow the change.
2. **Given** an app with a stored file, **When** it is redeployed (webhook, Deploy button, API
   without a body), **Then** the stored file is unchanged.
3. **Given** an API client, **When** it reads an app made from a file, **Then** it gets the stored
   file and overlay; **When** it reads a deployment made from a file, **Then** it can get that
   deployment's file.
4. **Given** an app with a stored file, **When** a client deploys a full definition, **Then** the
   deploy proceeds, the stored file is dropped, and the deployment's warnings include "This app's
   Compose file was dropped: it was deployed from a definition, not a file."

---

### Edge Cases

- A file edit that changes only comments or formatting deploys like any other (a new deployment
  record, no change to what runs), and the stored file keeps the new text.
- A file that refuses on deploy (after a preview that passed, because it changed in between) is
  refused as on New app; the editor keeps what was typed.
- Secret values typed in the overlay are never stored with the file, shown back, or offered in a
  download.
- The file can hold plain environment values; they are stored and shown like the field form shows
  env values since feature 014. The editor's hint says to put passwords and keys in secrets.
- A stored file that today's rules refuse (the rules got stricter in a later version) is still
  shown; the preview says what is refused.
- Deleting an app deletes its stored files with it.
- Export (feature 011) is unchanged: it exports the Nomad job, not the Compose file.
- A file is at most 1 MiB, as the API already accepts.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Every deploy made from a Compose file (console or API) MUST keep the file, exactly
  as sent, and its overlay (domains, health checks, secret names, backup), with the app and with
  the deployment.
- **FR-002**: Secret values MUST NOT be kept with the file or overlay, shown back, or included in
  any download or API answer.
- **FR-003**: Configure for an app with a kept file MUST show the file in an editor and the overlay
  fields prefilled, offer the preview, and deploy on the condition that the app hasn't changed since
  the page was drawn.
- **FR-004**: For an app with a kept file, the per-field edit form MUST be read-only and say the app
  is edited through its file.
- **FR-005**: For a Compose-made app without a kept file, Configure MUST say so, offer an empty
  editor with the overlay prefilled from the app, and keep the per-field form editable until a file
  is kept.
- **FR-006**: Redeploying a past deployment made from a file MUST make that file and overlay the
  kept ones; redeploying one not made from a file MUST drop the kept file, with the FR-009 warning.
- **FR-007**: Setting or removing a secret, webhook redeploys and redeploys without a new
  definition MUST keep the kept file unchanged; secret changes update the overlay's secret names.
- **FR-008**: The API MUST return an app's kept file and overlay, and each deployment's file and
  overlay, so everything the console shows can be read with an API token.
- **FR-009**: A deploy of a full definition (not a file) on an app with a kept file MUST drop the
  kept file and add a warning to the deployment saying so.
- **FR-010**: Each history entry of a deployment made from a file MUST offer that file for
  download; others MUST NOT.
- **FR-011**: Deleting an app MUST delete its kept files.
- **FR-012**: The editor is a plain text area that works without JavaScript; every form posts as
  plain HTML.

### Key Entities

- **Kept file**: the Compose file text, as sent, plus its overlay without secret values; belongs to
  one app (the current one) and to each deployment made from it (the one it deployed).
- **App**: gains "has a kept file" and the kept file itself; its source stays "compose".
- **Deployment**: gains the file and overlay it deployed, when made from a file.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: The owner changes an image tag in a Compose app's file and deploys it from the console
  in under one minute, without pasting the file from anywhere.
- **SC-002**: After a rollback, the file in the editor is the rolled-back version's file in 100 % of
  cases.
- **SC-003**: No secret value appears in any page, download or API answer that shows a kept file,
  checked on every path that shows one.
- **SC-004**: A kept file is never silently out of date: every way of changing the app either keeps
  the file in step or drops it with a warning.
- **SC-005**: Uptime Kuma on the maintainer's host gets its file kept by pasting it once, and is
  then edited through it.

## Assumptions

- Only Compose-made apps keep a file; image-made apps are unchanged.
- The overlay is kept in the same shape the API's Compose request takes, so a client can send a
  kept file and overlay back as they are.
- Comments and formatting in the file are kept as typed; Caravana never rewrites the file.
- The editor is a plain text area with a monospace font; no syntax highlighting (no Node build step).
- Volume backups and a volumes panel are feature 016, not this one.
- Files are kept in Caravana's own store, like the app definition; nothing new runs on the host.
