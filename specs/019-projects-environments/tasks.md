# Tasks: Projects and Environments (019)

**Input**: plan.md, research.md (R1–R8), data-model.md, contracts/api.md, contracts/console.md,
quickstart.md. Tests are required (constitution VIII).

## Phase 1: Foundation (blocks everything)

- [X] T001 [P] `internal/app/place.go` + `place_test.go`: `Place`, `DefaultProject`,
  `DefaultEnvironment`, `PlaceOf`, `Place.ID`, `ValidName` (data-model.md). Table tests: round
  trip, bare names, refusals (`a--b`, four parts, `a---b--c`, empty part, uppercase), field
  errors on `id`/`name`.
- [X] T002 `internal/store/migrations/0011_projects.sql` and `internal/store/projects.go` +
  tests: tables, Default rows, apps columns, `ErrNotEmpty` (with app IDs), project and
  environment CRUD, `AppRecord.Place`, place filled on insert from `PlaceOf` (grandfathered
  fallback), never changed on update, `ListAppsIn`, `DeploymentRecord.SourceApp`. Upgrade test:
  a store at 0010 with two apps (webhook token, backup row, deployment) migrates with IDs,
  revisions, tokens and history unchanged and both in default/production.

## Phase 2: US1 + US2 — projects, environments, nothing moves (P1)

- [X] T003 [US1] `internal/service/projects.go` + tests: `Projects`, `Project`, `CreateProject`,
  `SetProjectDisplayName`, `DeleteProject`, `CreateEnvironment`, `DeleteEnvironment` (409
  `protected` for default/production), `checkPlace` on every create path (`CreateApp`,
  `saveAndDeploy` for an unknown ID, Compose) with `ErrProjectNotFound`/`ErrEnvironmentNotFound`;
  existing apps skip it. `AppSummary`/`AppRecord` place exposed. Test: deploy to
  `shop--staging--web` before `staging` exists → environment not found, nothing saved.
- [X] T004 [US1] `internal/api/projects.go` + tests, `api/openapi.yaml` 0.16.0: `listProjects`,
  `createProject`, `getProject`, `updateProject`, `deleteProject`, `createEnvironment`,
  `deleteEnvironment`, `listEnvironmentApps`, `createEnvironmentApp`, `getEnvironmentApp`;
  `project`/`environment`/`appName` on `AppRecord`, `AppSummary`, `listApps` filters; error codes
  (contracts/api.md). The openapi/route sync test passes.

## Phase 3: US3 + US4 — promote and copy (P1/P2)

- [X] T005 [US3] `internal/service/promote.go` + tests: `PreviewPromotion`, `Promote(ctx, id,
  to, cond, trigger)`, `CopyApp`; refusals per research R4/R5 and contracts/api.md;
  digests pinned from the source deployment (a moved tag is not followed: registrytest returns a
  new digest for the tag, the target still gets the old one); env/secrets/domains/replicas/
  resources/backup kept; source-only service added without domains; missing secret on it refused
  by the existing check; dropped Compose file warning; `promotedFrom` on the record.
- [X] T006 [US3] `internal/api/promote.go` + tests: `previewPromotion`, `promoteApp` (If-Match),
  `copyApp` (`missingSecrets`); `Deployment.promotedFrom`.
- [X] T007 [US5] FR-011: `AttentionItem`, `notify.Event` (+ `PromotedFrom`), activity rows and
  `listRecentDeployments` carry the place; webhook message schema in openapi; notify goldens
  updated; deploy events of a promotion say "promoted from <env>" in their text.

## Phase 4: Console (US1, US3, US4)

- [X] T008 `internal/ui`: grouped home (and `/parts/apps`), "New project", `/projects/new`,
  `/projects/{project}` (display name, environments, add, delete, delete project), place select
  on both New app tabs, breadcrumb and "Environments" line on app pages, Copy (POST) and Promote
  (GET preview, POST confirm with revision, 412 page), history entry "Promoted from …".
  Handler tests per contracts/console.md; routes listed in `routeTable` and in the parity test;
  `tools/ui-css.sh build`.

## Phase 5: Lab, docs, checks

- [X] T009 `cmd/caravana/gate_test.go`: step `projects-promote` (quickstart.md 1).
  `cmd/caravana/console_lab_test.go`: step `console-projects` (quickstart.md 2).
- [X] T010 Docs: CHANGELOG "Unreleased" (Added, Upgrade: nothing to do, nothing moves),
  CLAUDE.md 019 paragraph and phase list, `docs/plan.md` 019 row Done with the namespace finding.
- [ ] T011 Full check (CLAUDE.md "Commands"), including `go test -race ./...`.
- [ ] T012 Full `TestLab` alone in the lab.

## Dependencies

T001 → T002 → T003 → T004; T003 → T005 → T006; T005 → T007; T004 + T006 + T007 → T008 →
T009 → T010 → T011 → T012.

## Implementation strategy

Four agents in sequence, each reviewed before the next: (1) T001–T004, (2) T005–T007, (3) T008,
(4) T009–T010. The main session runs T011–T012.
