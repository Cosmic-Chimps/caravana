# Tasks: Terminal into a copy (020.6)

- [X] T001 store: migration `0014_terminal.sql` (`terminal` action); tests incl. rows kept.
- [X] T002 httpx: `statusRecorder.Hijack` (101 recorded) + test; deploy: `OpenTerminal` over
  Exec with TTY and resizes; deploytest fake supports TTY/resize; tests.
- [ ] T003 service: `OpenTerminal` sessions (limits 5, idle 15 min, max 60 min, records, sentinel
  test that typed input and output are in no row and no log line).
- [ ] T004 API 0.20.0: `openTerminal` websocket endpoint per contracts/terminal.md (auth, Origin,
  scope, validation, upgrade, deadlines cleared, protocol, pings); tests with a Go websocket
  client against the fake (echo, resize, exit code, idle with short timers, cross-origin 403,
  deploy token 403, 429); openapi.
- [X] T005 Console: vendor xterm 6.0.0 + addon-fit 0.11.0 (ui-vendor.versions, LICENSES.md), the
  terminal page with its CSP, `static/js/terminal.js`, the Running tab link, Recent actions;
  handler tests (page, CSP header, noscript); `tools/ui-css.sh build` if styles change.
- [ ] T006 Lab: gate step `terminal`, console step `console-terminal` (plan.md "Lab").
- [ ] T007 Docs: CHANGELOG Unreleased, CLAUDE.md 020.6 paragraph, docs/plan.md row 020.6 Done and
  the Phase 3 "A shell into a copy" row reduced to servers.
- [ ] T008 Full check, full TestLab, and a look at the page in a real browser (main session).
