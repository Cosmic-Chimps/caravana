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

- [X] T009 [US2] Cross-cutting: fields to 32px (`cv-field`, sign-in keeps 42px), the select, the
  new icons (lock, external-link, file-text, history, play, chevron-right, chevron-down).
- [X] T010 [US2] Configure: the two-column grid per service (Copies & size, Deploy with the
  health check row and the revert sentence, "Also set"), the Domains and Secrets panels with
  their links, the registry prerequisite, the console's own panels after the grid; Environment
  and the secret forms move out; kept-file, undefined and stopped rules unchanged.
- [X] T011 [US2] Variables page `GET /apps/{id}/variables`: head, table, footnote, "Add or change"
  (variable or secret, Set and deploy), the .env editor per service; kept-file read-only with
  secrets still editable; undefined and stopped rules.
- [X] T012 [US2] Domains page `GET /apps/{id}/domains`: head and count, table with open link and
  Remove, "Add a domain" with the DNS record box (A, apex or first label, the public IP resolved
  from CARAVANA_PUBLIC_URL with a 2 s timeout and a process cache); add and remove deploy.
- [X] T013 [US2] Backups tab in the handoff's shape: head, no-destination banner, gap rows with
  "Back up daily" (`POST /apps/{id}/backups/policy`), the `<details>` rows with the 30-day strip,
  runs, Restore… and Back up now, Restores after.
- [X] T014 [US2] DESIGN.md, CHANGELOG, tests (handler tests per page and per rule; no secret value
  on any page, a sentinel test), lab test selectors.
- [ ] T015 [US2] Checks, TestConsoleLab, the review instance seeded for the designer, the
  designer's review, the impeccable critique agent (no detector), fixes.

## Page 3: Settings with its grouped nav (US3)

Contract: `contracts/page3-settings.md` (the designer, 2026-10-22). Every page and form goes
through `internal/service` as today; no API change.

- [X] T016 [US3] Routes and layout: `/settings` (Account), `/settings/registries`,
  `/settings/backup-storage`, `/settings/notifications`, `/settings/tokens`; `/settings/account` 301;
  existing POST routes answer 303 to their entry; the sticky Settings nav with groups, icons
  (user, package, hard-drive, bell, key, hash, message-square, webhook) and the count-or-mark rule
  (one function per entry, shared by the nav and the rows); crumbs `Settings / <entry>`; the content
  column and page head; under 900px the nav as a scrolling row.
- [X] T017 [US3] Account and Registries pages (setting rows; connection rows with marks, state,
  used by, actions, the refused note; Add).
- [X] T018 [US3] Backup storage (the one connection row or the problem panel, the form; no Remove)
  and Notifications (channel rows with kind icons, marks, Send a test, Pause/Resume, Edit, New
  signing secret, Remove, Recent deliveries; the sentence instead of the rules grid; Add).
- [X] T019 [US3] API tokens (the token table with marks and notes, sorted; New with "Only for one
  app"; the shown-once panel).
- [X] T020 [US3] DESIGN.md, CHANGELOG, tests (each page, each mark rule, the redirects, no secret or
  token value on any page), lab-test selectors and form posts.
- [ ] T021 [US3] Checks, TestConsoleLab, the review instance staged, the designer's review, the
  critique agent, fixes.

Already decided:
- **No Remove on Backup storage.** The API has no delete of the backup destination
  (`/v1/backups/destination` offers GET and PUT only, API 0.22.0). Adding one is a separate
  change (API and service, then the button); it is not part of page 3.

## Page 4 (US4)

Tasks are written when its contract is approved.

Carried to page 4, the deployment stages (the designer, 2026-10-21): after Nomad reverts, the live
job version (`v2`) is a copy of an older one (`v0`). Say what the owner deployed, from the stored
definition match: "`v2` (`v0` again) keeps running" in the lede, and "rolled back to `v2` (`v0`
again)" in the history, changed in both places together.
