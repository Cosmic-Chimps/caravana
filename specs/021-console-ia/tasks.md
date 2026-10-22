# Tasks: Console IA (021)

## Page 1: the shell, Projects and the app header (US1)

- [X] T001 [US1] service + API: `AppStatus.Services` (name, wanted, healthy) in `statusOf`;
  `AppStatus.services` in `internal/api/apps.go` and `api/openapi.yaml` (0.22.0); tests.
- [X] T002 [US1] view.go: the copies sentence (five shapes of the contract) as a pure helper used by
  `summaryOf`; the warning pill for a short service; table tests.
- [X] T003 [US1] Icons: an `icon` template function with the Lucide paths needed (house, folder,
  settings, log-out, plus, rotate-ccw, rocket); LICENSES.md entry.
- [X] T004 [US1] The shell per contracts/page1-shell.md: sidebar (brand block, Workspace group,
  Settings row, account row with POST sign-out), top bar (crumbs, live, New app), active-item rule,
  narrow `<details>` with the same blocks; remove the per-page `action` slot.
- [X] T005 [US1] `/projects`: the projects panel; route, handler, template; tests.
- [X] T006 [US1] The app header per the contract: back link, title row (pill, domains, Restart /
  Stop… / Deploy link, or Start), lede, Environments line kept, tabs with the status mark; remove
  `cv-where` and `cv-actions`; handler tests.
- [X] T007 [US1] Styles in `internal/ui/style/`, `tools/ui-css.sh build`; DESIGN.md's Shell,
  Navigation and icons sentences updated; CHANGELOG Unreleased.
- [X] T008 [US1] Checks (CI list), `TestConsoleLab` (fix selectors that moved), a local run for the
  designer's live review, then an `impeccable` critique pass and the fixes it brings.

## Page 2: the short Configure, Variables, Domains and Backups (US2)

Contract: `contracts/page2-app-pages.md` (the designer, 2026-10-22). Constitution V: every form
here goes through `internal/service` (`DeployIf` with the page's revision), and each change the
pages make (an env variable, a secret, a domain, a backup policy) is also possible through the
API's `deployApp`, so no API change is needed. "Back up daily" is a console shortcut for a
definition edit the API already takes.

- [ ] T009 [US2] Cross-cutting: fields to 32px (`cv-field`, sign-in keeps 42px), the select, the
  new icons (lock, external-link, file-text, history, play, chevron-right, chevron-down).
- [ ] T010 [US2] Configure: the two-column grid per service (Copies & size, Shipping with the
  health check row and the revert sentence, "Also set"), the Domains and Secrets panels with
  their links, the registry prerequisite, the console's own panels after the grid; Environment
  and the secret forms move out; kept-file, undefined and stopped rules unchanged.
- [ ] T011 [US2] Variables page `GET /apps/{id}/variables`: head, table, footnote, "Add or change"
  (variable or secret, Set and deploy), the .env editor per service; kept-file read-only with
  secrets still editable; undefined and stopped rules.
- [ ] T012 [US2] Domains page `GET /apps/{id}/domains`: head and count, table with open link and
  Remove, "Add a domain" with the DNS record box (A, apex or first label, the public IP resolved
  from CARAVANA_PUBLIC_URL with a 2 s timeout and a process cache); add and remove deploy.
- [ ] T013 [US2] Backups tab in the handoff's shape: head, no-destination banner, gap rows with
  "Back up daily" (`POST /apps/{id}/backups/policy`), the `<details>` rows with the 30-day strip,
  runs, Restore… and Back up now, Restores after.
- [ ] T014 [US2] DESIGN.md, CHANGELOG, tests (handler tests per page and per rule; no secret value
  on any page, a sentinel test), lab test selectors.
- [ ] T015 [US2] Checks, TestConsoleLab, the review instance seeded for the designer, the
  designer's review, the impeccable critique agent (no detector), fixes.

## Page 3 (US3), Page 4 (US4)

Tasks are written when each page's contract is approved.

Carried to page 4, the deployment stages (the designer, 2026-10-21): after Nomad reverts, the live
job version (`v2`) is a copy of an older one (`v0`). Say what the owner deployed, from the stored
definition match: "`v2` (`v0` again) keeps running" in the lede, and "rolled back to `v2` (`v0`
again)" in the history, changed in both places together.
