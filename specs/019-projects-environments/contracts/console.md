# Contract: Console (019)

Every form works without JavaScript (POST, then 303 to a GET); htmx only refreshes regions, as
today. Styles follow `DESIGN.md`. All pages call `internal/service` only.

## Apps home (`GET /apps`)

- While the installation has exactly one project and it has one environment (every upgraded
  installation until the owner adds one), the page is as today: no headings.
- Otherwise the app list is grouped: one section per project (display name, link to its page),
  inside it one sub-heading per environment in position order, each with its apps (the existing
  rows) and an empty-state line "No apps in <environment> yet." with "New app here"
  (`/apps/new?project=<p>&environment=<e>`).
- The page header gains "New project" beside "New app". The needs-you list and activity rail
  show each app as `<project display> › <environment> › <appName>` when it isn't in
  Default › production, else its ID as today.
- `/parts/apps` renders the same grouping.

## Projects

- `GET /projects/new`: form `name` (label, help: "lowercase letters, digits and single dashes;
  part of each app's address in Caravana"), `displayName` (optional). `POST /projects` → 303 to
  `/projects/<name>` with the notice "Project <displayName> created with a production
  environment." Errors re-render the form with the field's message (422/409, no redirect).
- `GET /projects/{project}`: display name (form → `POST /projects/{project}`), environments in
  order, each with its apps (links) and a Delete button (disabled with the reason while it has
  apps; never for Default's production); "Add environment" form (`name`) →
  `POST /projects/{project}/environments`; "Delete project" (confirmation by typing its name, as
  app delete does) → `POST /projects/{project}/delete`, refused with the app list while apps
  remain, never offered for Default.

## New app (`GET /apps/new`, `POST /apps`, Compose tab)

- Both tabs gain a `place` select, value `<project>/<environment>`, labelled
  "<project display> › <environment>", defaulting to `?project=&environment=` or
  Default › production. Hidden when only Default › production exists (as today).
- The name field keeps its 31-character limit; the computed ID is what is created (research R7);
  an ID too long for a service name gets the existing field message.

## App pages (`/apps/{id}/...`)

- The header shows a breadcrumb `<project display> › <environment> › <appName>` (project linking
  to its page) for every app outside Default › production; for Default › production apps it
  shows it only when the installation has more than one environment.
- An "Environments" line in the header lists the project's other environments in order:
  - the app exists there: a link to it and "Promote to <environment>" →
    `GET /apps/{id}/promote?to=<environment>`;
  - it doesn't: "Copy to <environment>" → `POST /apps/{id}/copy` (`to`), 303 to the copy's
    Configure with the notice "Copied to <environment>. Set its domains and these secrets before
    deploying: …" (or without the secrets sentence when none).
  - Nothing is shown when the project has one environment.

## Promote (`GET /apps/{id}/promote?to=`, `POST /apps/{id}/promote`)

- The GET page shows the preview: "Promote <appName> from <from env> (deployment v<N>) to
  <to env>", a table per service (image from → to, digest short forms, changed fields), the added
  services, the warnings (a dropped Compose file), and what stays ("<to env> keeps its env,
  secrets, domains, memory, copies and backups"). A refusal (not healthy, removes a service,
  missing image, no target) is shown instead of the button, with its message.
- The form posts `to` and `revision` (the preview's `targetRevision`). A changed target answers
  the page again with "<to env> changed since this preview; review it again." (412 mapped).
- Success: 303 to the target's Deployments tab, where the new entry reads "Promoted from
  <environment> (v<N>)".

## Deployments tab

- A promotion's history entry says "Promoted from <environment> · deployment <short id>",
  linking to the source app's Deployments tab.
