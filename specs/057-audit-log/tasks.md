# Tasks: Audit log

**Spec**: [spec.md](./spec.md) · **Plan**: [plan.md](./plan.md)

## Phase A — the log itself (US1 core, US3 API, US4)

- [x] T001 `internal/audit`: Entry, Actor, Resource, Change, Record, Related, the Action and Kind constants
  (D7, D8), `Diff` with keyed lists and redaction (D5, D6), the CSV writer with formula-cell escaping; table
  tests (domain host change shows removed old / added new; a secret value shows `changed`; a whole new secret
  shows only its key; every denylisted key with a sentinel; cuts at 200 bytes); a test that the package imports
  no `net/http`, `store`, `service`.
- [x] T002 Migration `0044_audit.sql` (`audit_events` without foreign keys, indexes on `at`, actor, resource,
  place; `audit_settings` with `retention_days` 90, CHECK 30–3650) and `internal/store/audit.go` (D10), tests.
- [x] T003 `internal/service/audit.go`: `AuditScope`, `WithAuditScope`, `WithAuditDoor`, `(*AuditScope).SetActor`,
  `Finish(status, fallbackResource)`, `s.audit` / `a.audit`, actor resolution (D4), immediate write outside a
  scope, the prune pass (D11), `AuditEvents`, `ExportAuditEvents`, `AuditSettings`, `SetAuditSettings` (owner,
  noted). A failed write of an entry is logged, never returned. Tests.
- [x] T004 API: the scope for every non-GET route in `handler()` (door `api`, or `mcp` by User-Agent, or
  `webhook` for the webhook route), `authenticate` sets the actor on the scope, the fallback from the access
  rule's target (D3); routes `listAuditEvents`, `exportAuditEvents`, `getAuditSettings`, `putAuditSettings`,
  access rules, `api/openapi.yaml` 0.61.0. `TestEveryWriteRouteLeavesOneEntry`: walks the route table with
  stubbed handlers (as `TestRolesWalkEveryRoute`) and checks one entry with actor and resource per non-GET
  route but the named exceptions (D12), and none for a refused request.

## Phase B — notes: apps and what hangs off them (US1, US2, US5)

- [x] T005 Notes in the app paths: create (with and without deploy), deploy/define/compose (diff of the app
  view, link to the deployment), redeploy, rollback, promote, copy, delete, stop, start, restart, run a command
  (program only), open a terminal, refresh secrets, secret and domain edits through Configure, placement,
  scaling, build settings of a service, webhook tokens, deploy tokens; the webhook as actor.
- [x] T006 Notes for backups and restores (back up now, restore, pin, unpin, backup places, restore schedule),
  schedules (create, update, delete, run now, stop a run), projects and environments (create, update, delete),
  previews (settings, close, the reaper as Caravana), domains (Team only password rotation, certificate settings),
  Git access.
- [x] T007 Service tests: one entry per method above with action, kind, ID, place and record; domain change
  diff; secret `changed`/`set`; sentinel over every entry and the SQLite file.

## Phase C — notes: the installation and accounts (US1, US2)

- [x] T008 Registries, backup destinations, notification channels (incl. rotate, test), DNS zones, secret
  providers, servers (add, remove, labels, move off, accept work, clean up), orphan volume deletions, cleanup
  settings, updates (settings, check, request update, automatic update as Caravana), templates (settings,
  check, install), add-ons and scans (install, remove, settings, request), build settings, console address
  (start, cancel, switch).
- [x] T009 Accounts: people (invite, reinvite, update, remove, accept invite), API tokens (create, revoke), passkeys
  (add, rename, remove), sign-in settings, password change, sign in / sign out / sign-in failed (console and
  passkey), the CLI (`caravana admin …`) as `host`.
- [x] T010 Tests as T007 for these, with sentinels in every secret field.

## Phase D — console, docs (US3, US4)

- [x] T011 Console scope for every POST (`routes()`), Settings › Audit log (`/settings/audit`: filters as one GET
  form, entries newest first with changes in `<details>`, record links, "Older" link by cursor, the export
  link, the retention form for owners), `/settings/audit/export`, `POST /settings/audit`, console access rules,
  the Security nav entry after API tokens, parity test twins. Forms work without JavaScript. `tools/ui-css.sh build`.
- [x] T012 UI tests: the page for owner, admin, member (403); filters; a sentinel never drawn.
- [x] T013 `docs/audit-log.md`, `changelog.d/057-audit-log.md`, CLAUDE.md's paragraph, the plan's Deviations.
- [x] T014 Full verification: `go build`, `go vet`, staticcheck, `go test -race ./...`, gofmt, the cross-compile,
  `tools/ui-css.sh check`, `tools/release.sh check`.
