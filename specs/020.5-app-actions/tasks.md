# Tasks: App actions and Run a command (020.5)

- [X] T001 Migration `0012_app_actions.sql` and `internal/store/actions.go` + tests (research R6;
  `stopped_at`, actions CRUD, deleted with the app, `FailRunningActions`).
- [X] T002 `internal/deploy/actions.go` + tests: `StopApp`, `StartApp`, `Exec` with the capped writer
  and `ExecResult`, `ErrCopyNotRunning`; deploytest fakes for each. `deploy/vm/caravana-policy.hcl`
  gains `alloc-exec`.
- [X] T003 [US1][US2][US3] `internal/service/actions.go` + tests: `RunCommand`, `StopApp`,
  `StartApp`, `RestartApp` (background, lock held, per-copy wait), `ErrAppStopped` / `ErrAppBusy` /
  `ErrAppNotStopped`, refusals in every deploy path and backup-now/restore, scheduler skip,
  `statusOf` stopped, Attention ignores stopped apps, startup `FailRunningActions`. A sentinel test:
  after `RunCommand` printing it, the sentinel is in no store row and no log line (capture the
  slog handler).
- [X] T004 API 0.18.0: the five operations, `stopped` state, `stoppedAt`, codes; `no-store` on
  runCommand; tests incl. the sentinel; openapi sync.
- [X] T005 Console: header actions, Stop confirm page, Running tab run form + result panel + recent
  actions, stopped states across pages, `splitCommand` + tests, handler tests incl. the sentinel
  absent after reload, parity, `tools/ui-css.sh build`.
- [ ] T006 Lab: gate step `app-actions`, console step `console-app-actions` (plan.md "Lab").
- [ ] T007 Docs: CHANGELOG Unreleased (Added; Upgrade: rerun the installer for `alloc-exec`),
  CLAUDE.md 020.5 paragraph, docs/plan.md 020.5 row Done, ranked table row 2/8.
- [ ] T008 Full check and full `TestLab` (main session).
