# Tasks 053: People and roles

## Phase A: store, Auth per person, principal (existing behaviour unchanged with one owner)

- [x] T001 Migration `0043_people.sql` (D1) and `internal/store/people.go`; sessions, tokens and passkeys with
      `person_id`; the admin-singular methods made per person; `actor` columns. Tests: a store migrated from 0042 with an
      admin has one owner with the same email, hash, sign-in setting and handle, and every session, token and passkey is
      the owner's; with no admin, no person; `DeletePerson` removes sessions, tokens, passkeys and roles; the role tables'
      checks and cascades (deleting an environment drops its overrides).
- [x] T002 `Auth` per person (D2): sign-in by email among active people, per-person password sign-in and passkeys,
      `CheckSession`/`CheckAPITokenNamed` returning the `Principal`, the end date, tokens of their own; the CLI
      (`admin init|status|reset-password|create-token|passkeys` with `--email`). Every existing auth, passkey, token and
      CLI test passes, adapted to a person.
- [x] T003 `internal/service/access.go` pure part (D3): `Level`, `Principal.LevelAt`, `ProjectLevel`, `Admin`, `Owner`,
      `WithPrincipal`/`PrincipalFrom`; table tests (overrides, previews, ended roles, admin and owner).

## Phase B: service

- [x] T004 `Need`, `Target`, `Service.Allow`, `NotAllowedError` (D4); tests.
- [x] T005 People (D8): invite, reinvite, accept (with the sign-in limit), update, remove, list, `Me`; tests for every
      refusal and the last owner.
- [x] T006 Filtering (D6) in every listed method; tests that a member sees exactly their places, and admins and no
      principal see everything.
- [x] T007 Images-only deploys and the state-dependent checks (D5, D7); `app.ImagesOnlyChange` table tests.
- [x] T008 Actor (D9) on deployments, actions, manual backups and restores.

## Phase C: API

- [x] T009 `accessRules` for every route, the order in `authenticate`, 403 `not_allowed`; `TestRolesWalkEveryRoute`
      (every route × owner, admin, member with deploy on staging and view on production, a deploy token of that member,
      an ended guest) against a written-out expectation, as `TestScopedTokenWalksEveryRoute` does.
- [x] T010 People routes, `getMe`, `acceptInvite`, `actor`, own tokens and passkeys; `api/openapi.yaml` 0.59.0; the
      contract and parity tests; MCP `get_me`.

## Phase D: console

- [x] T011 The guard and `internal/ui/access.go` for every route; `TestConsoleRolesWalkEveryRoute`; refusal page and
      fragment; live notices filtered.
- [x] T012 Settings › People (list, invite, person page, remove), `/invite`; the sidebar and Settings nav by role; the
      main actions hidden by role; who-words with the actor; `tools/ui-css.sh build`.

## Phase E: lab and docs

- [ ] T013 Gate step `people-roles` (API and MCP) and console step `console-people`; run in the lab.
- [ ] T014 `docs/people.md`, `changelog.d/053-people-and-roles.md`, CLAUDE.md entry, `docs/plan.md` (Phase 4's
      `MembersScreen` row).
