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
- [ ] T008 [US1] Checks (CI list), `TestConsoleLab` (fix selectors that moved), a local run for the
  designer's live review, then an `impeccable` critique pass and the fixes it brings.

## Page 2 (US2), Page 3 (US3), Page 4 (US4)

Tasks are written when each page's contract is approved.
