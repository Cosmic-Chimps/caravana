# Tasks 050: Build from a Git repository on the host

## Phase A: model and job (pure)

- [x] T001 `internal/app/build.go`: `Build`, `Defaults()`, `BuiltService()`, `validateBuild` and the URL rule (D1),
      the image rule for a built service, one built service per app, the `caravana-` volume prefix; promotion's
      `Changed` takes `build`; table tests.
- [x] T002 `internal/nomadjob/build.go`: `BuildSpec`, `BuildJob`, `BuildSourceScript`, `BuildScript`,
      `DefaultBuilderImage`, `DefaultGitImage`, the built-in known hosts (research R2) and `KnownHosts(host, hostKey)`
      (D3, D4); goldens `build-public`, `build-deploy-key`, `build-token`, `build-host-network`, `build-no-cache`;
      script tests under `sh` with fakes.

## Phase B: store, deploy, service

- [x] T003 Migration `0040_builds.sql`; store `builds` (insert, update, get by deployment, prune logs, open builds for
      recovery), `build_settings`, the `Build` summary on `DeploymentRecord` (D7); tests.
- [x] T004 `internal/deploy/build.go` and the `deploytest` fake (D4): variable, cache volume, register, state,
      result, log, stop, purge, list; `GitAccess` variable helpers (D3); tests with the fakes.
- [x] T005 `internal/service/build.go` and `gitaccess.go`: the split of `deployWarned`, the build flow (D5), newest
      wins, the queue, the timeout, failure words, origin, recovery at `Start`, cancel on stop and delete, the cache
      volume's life, `checkBuild` (D2, D3), settings (D8), `cmd/caravana` flags; tests including a sentinel token and
      key in no record, log line or error.
- [x] T006 `PushEvent` and the webhook (D6); tests with GitHub, Gitea and GitLab bodies.

## Phase C: API

- [x] T007 `internal/api`: the routes and codes of `contracts/api.md`, error mapping, `Deployment.build`,
      `WebhookResult.skipped`, `X-Gitlab-Token`; `api/openapi.yaml` 0.56.0 (contract and parity tests); the scope walk;
      MCP contract test still passes.

## Phase D: console and docs

- [x] T008 Console (D10): New app › Build from Git, Next steps, Configure's Build panel and Git access, the Build
      stage and the log page, Settings › Builds; `tools/ui-css.sh build`; UI tests (no key or token on any page).
- [x] T009 Docs (D11): `docs/building-from-git.md`, `docs/plan.md`, `changelog.d/050-build-from-git.md`, CLAUDE.md.

## Phase E: lab

- [x] T010 Gate step `build-from-git` and console step `console-build` (D12), each run alone in the lab.
