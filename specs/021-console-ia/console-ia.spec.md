# Feature Specification: Console IA, the design handoff's shape

**Feature Branch**: `021-console-ia` (from `main` at 0.11.0)

**Created**: 2026-10-21

**Status**: Implemented, v0.12.0 (pages 1-4)

**Input**:
- **The maintainer, 2026-10-21:** "now let's do feature 021 ... we should follow 'almost' pixel by
  pixel the ui, we should go page by page", with the caravana_designer session supervising the
  design.
- **docs/plan.md, row 021:** put the console on the design handoff's information architecture
  (`design/console/design/ui_kits/console/`):
  - Configure becomes the short form, and Variables and secrets, Domains and Backups become their
    own app pages;
  - Settings gets a grouped nav;
  - the Apps home gets "Deploying now";
  - the deployment entry gets its stages.
- **Found while running real apps:**
  - the project pages can't be reached from the console's navigation;
  - an app with several services reads "2 copies running", which sounds like two replicas.

## How this feature is built

One page at a time, each its own release.
1. The designer lists what must match the handoff (sizes, spacing, colors, states) and what may
   differ because the console is server-rendered HTMX without JavaScript. That list is the page's
   contract in `contracts/`.
2. The page is built.
3. The designer reviews it live, state by state, before the release.
4. The `impeccable` skill is used for a critique and polish pass after each page is built, not
   for transcribing the numbers.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Page 1: the shell, Projects and the app header (Priority: P1)

**What the owner sees:**
- A fixed sidebar as the handoff draws it:
  - the brand block (the host's initial, the host, `caravana <version>`), linking to Home;
  - a Workspace group with Home and Projects;
  - Settings;
  - the account row, with sign-out.
- **No links to pages that don't exist yet:** Deploys, Previews, Logs, Monitoring and Schedules
  arrive with their own features.
- **A top bar:** crumbs (project / environment / page), the live mark, and "New app" on every
  page.
- **Projects** is a page: one row per project with its environments and app count, each row
  linking to the project.
- **An app's header** reads as the handoff's AppScreen: a back link to its environment, the name,
  the status pill, its domains and the actions (Restart, Stop…, Deploy, or Start when stopped).
  Under it, one line says what runs. For an app with several services, that line names each
  service and its copies.

**Why this priority**: the shell is on every page, and every later page builds on it.

**Independent Test**: sign in. The sidebar shows Home, Projects and Settings. Projects lists Default
with production and its app count. Hollo's header reads "2 services running v2: hollo and
postgres, 1 copy each. Last deployed … through the API." A stopped app shows Start and no Deploy.

**Acceptance Scenarios**:

1. **Given** the console, **When** any page is open, **Then** the sidebar and top bar match
   `contracts/page1-shell.md`, and the active sidebar item is right:
   - Home on `/apps`;
   - Projects on `/projects*` and every `/apps/{id}*`;
   - Settings on `/settings*`.
2. **Given** projects with environments, **When** the owner opens Projects, **Then** each project
   is a row (name, description, environment pills, app count) linking to `/projects/{project}`.
3. **Given** an app with one service of 2 copies, **Then** its header says "2 copies running v7."
   **Given** hollo 2 and postgres 1, **Then** "2 services running v2: hollo 2 copies, postgres 1."
   **Given** both at 1, **Then** "… hollo and postgres, 1 copy each." **Given** hollo 1 of 2
   healthy, **Then** "hollo 1 of 2 copies healthy" stands in for hollo's part, and the pill is
   warning.
4. **Given** a stopped app, **Then** the header has Start and no Deploy. **Given** a running app,
   **Then** it has Restart, Stop… and Deploy, where Deploy is a link to the deploy form.
5. **Given** no JavaScript, **Then** every link and action still works: sign-out and the app
   actions are forms, and the narrow layout uses `<details>`.

---

### User Story 2 - Page 2: the app's short Configure and its own pages (Priority: P2)

Configure becomes the handoff's short form: copies and size, deploy (image, port, health check), the list of domains, and
the secret names. Variables and secrets, Domains and Backups become pages of the app. Open
question for its contract: seven tabs, or Configure linking to the three pages.

### User Story 3 - Page 3: Settings with its grouped nav (Priority: P3)

Settings gets the handoff's left nav, grouped as Connections, Security and People, with counts and
warning marks per entry.

### User Story 4 - Page 4: the Apps home and the deployment stages (Priority: P3)

The Apps home gets "Deploying now" for a rollout in progress, a kind word per row and an app filter.
A deployment entry shows its stages (Queued, Roll out, Live) and who deployed it.

### Edge Cases

- **Long names:** a long project, environment or app name in a crumb or the header is ellipsized,
  never wrapped across the bar.
- **No email:** an account with no email has the initials "?" and still shows Sign out.
- **Nomad unreachable:** the copies line keeps today's sentence for it; the per-service parts need
  Nomad.
- **Three or more services at the same count:** "a, b and c, 1 copy each".

## Requirements *(mandatory)*

- **FR-001**: Each page MUST match its contract in `contracts/`, which the designer approves before
  release.
- **FR-002**: The sidebar MUST link only to pages that exist.
- **FR-003**: Every page MUST keep working without JavaScript. The terminal page stays the one
  exception (020.6).
- **FR-004**: `GET /projects` MUST list every project, with its environments and app counts, from
  `internal/service`.
- **FR-005**: An app's status MUST carry, per service, its wanted and healthy copies. Because
  constitution V says no feature exists only in the UI, the API's `AppStatus` gains this as an
  additive field.
- **FR-006**: The copies sentence MUST follow the five shapes of User Story 1, scenario 3.
- **FR-007**: DESIGN.md MUST be updated in the same release as each page, so it describes what
  ships.
- **FR-008**: Icons MUST be inline SVG (Lucide, ISC, listed in the licenses), and no icon font or
  run-time fetch.

## Success Criteria *(mandatory)*

- **SC-001**: The designer approves each page against the handoff, state by state.
- **SC-002**: The owner reaches any project, app or setting from the sidebar in at most two clicks.
- **SC-003**: No multi-service app's header can be read as replicas.

## Assumptions

- One owner, one node: no organization switcher, no Developer/Operator toggle and no node names in
  the header (Phase 3/4).
- The handoff is React. The console reproduces its look in server-rendered HTML, CSS-only hover and
  focus states, and forms.

## Out of Scope

- Deploys, Previews, Logs, Monitoring and Schedules pages (022, 024, 025 and later).
- The ProjectScreen's environment switcher and its "Deploys in staging" rail (page 4 or later).
