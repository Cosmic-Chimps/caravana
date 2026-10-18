# Feature Specification: Create an app without deploying it

**Feature Branch**: `019.5-create-without-deploy` (stacked on `019-projects-environments`)

**Created**: 2026-10-18

**Status**: Draft

**Input**: the maintainer, on New app: "I should be able to create an app only by name... so the
first automation works automatically."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - The first deploy comes from CI's webhook (Priority: P1)

The owner is setting up a new app whose image CI hasn't built yet. In New app they type the
name, the image CI will push (`ghcr.io/me/shop:main`), the port and the domain, and press
"Create without deploying". Nothing is deployed and nothing fails. On the app's page they create
a webhook token and put the URL in CI. CI's first build pushes the image and calls the webhook:
Caravana deploys the app for the first time.

**Why this priority**: today "create" always deploys, so an image that doesn't exist yet gives a
failed deployment, a needs-you item and a notification on day one.

**Independent Test**: create an app with an image tag that doesn't exist yet without deploying:
no deployment, no Nomad job, no needs-you item. Create its webhook token; call the webhook once
the tag exists: one deployment, healthy.

**Acceptance Scenarios**:

1. **Given** New app with a name and an image, **When** the owner presses "Create without
   deploying", **Then** the app is saved with its definition, no deployment exists, and its page
   says it is waiting for its first deploy and how to start it (Deploy now, webhook, API).
2. **Given** such an app, **When** its webhook is called, **Then** it is deployed (trigger
   webhook) as any first deploy.

---

### User Story 2 - Only a name; the definition comes from the automation (Priority: P1)

The owner creates an app with only its name, so their automation (a script with an API token,
the .NET Aspire integration, a Compose file in CI) can fill in everything on its first deploy.
The app is listed, can get a webhook token and secrets' names later, and its page says it has no
definition yet and how to give it one: Configure, Deploy from Compose, or the API call.

**Independent Test**: create `shop` with only a name: listed, no Nomad job. Its webhook answers
"not defined yet" and deploys nothing. `POST /v1/apps/shop/deployments` with a definition deploys
it.

**Acceptance Scenarios**:

1. **Given** New app with only a name, **When** the owner creates it without deploying, **Then**
   it is listed as "Not defined yet" and nothing else happens.
2. **Given** an app without a definition, **When** something asks to redeploy it as stored (the
   webhook, Deploy now, an empty-body deploy), **Then** it is refused with a clear reason and
   nothing is recorded.
3. **Given** an app without a definition, **When** a deploy sends a definition (API body,
   Compose, Configure's form), **Then** it becomes the app's definition and deploys.
4. **Given** New app with only a name, **When** the owner presses "Create and deploy", **Then**
   the form says an image is needed to deploy now, or to create it without deploying.

### Edge Cases

- Secret values can't be given to a create without deploying (values are only ever sent with a
  deploy); names can. A value is refused, naming the field.
- Copying an app without a definition to another environment copies its name only; promoting
  into one adds every service of the source (they are all "added").
- An app without deployments never shows as failed on the needs-you list.

## Requirements *(mandatory)*

- **FR-001**: The console and the API MUST let the owner create an app without deploying it, with
  only a name or with a full definition; nothing is deployed or recorded as a deployment.
- **FR-002**: An app created with a definition and never deployed MUST deploy as stored on its
  first redeploy (webhook, Deploy now, empty-body deploy).
- **FR-003**: An app with no definition MUST refuse a redeploy as stored with its own error, and
  MUST accept any deploy that brings a definition.
- **FR-004**: The app's pages MUST say when it is waiting for its first deploy or has no
  definition, and show the ways to deploy it, including the webhook and the API call.
- **FR-005**: Everything that exists for a deployed app (webhook token, secrets' names, delete,
  copy to environment) MUST work for an app that was never deployed.

## Success Criteria *(mandatory)*

- **SC-001**: Creating an app whose image doesn't exist yet produces zero failed deployments and
  zero notifications.
- **SC-002**: A CI pipeline can be set up for a new app (create, webhook token, first build)
  without any deploy failing on the way.

## Assumptions

- The webhook keeps meaning "redeploy what is stored": it never chooses an image, so a leaked
  token can't deploy something else. An app without a definition is defined by an API token's
  deploy, Compose or the console.
- Compose's New app tab stays "create and deploy"; a name-only app is then given its file with
  Configure › Deploy from Compose.
