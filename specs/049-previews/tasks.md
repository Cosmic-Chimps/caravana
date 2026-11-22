# Tasks 049: Previews

## Phase A: model, store, service, API

- [x] T001 `app.PreviewNumber(name) (int, bool)` and the reserved `pr-` prefix rule (D1), table tests.
- [x] T002 Migration `0039_previews.sql` (D9); store: preview settings on `ProjectRecord`
      (`PreviewLimit`, `PreviewExpireDays`, `PreviewPasswordHash`), `SetPreviewSettings`,
      `InsertPreviewEnvironment` with the limit in one transaction and `*PreviewLimitError` (D4),
      `PreviewActivity(project)` (D2), the Team-only row rewrite for a project's preview hosts (D6); tests.
- [x] T003 Service `previews.go`: `CreateEnvironment` uses the name rule and the limit; `Previews` (D8);
      `SetPreviewSettings` (validation, password hash, row rewrite and sync with rollback, `previews_open`, the
      first-time warning); `ClosePreview` and `ErrNotAPreview` (D7); the reaper with `SetPreviewReapGap` and
      `SetPreviewExpiryForTests` (D3); tests with the fake clock and fakes.
- [x] T004 Service deploy path: `previewTeamOnly` and its warning, `ensureTeamPasswords` using the project hash,
      `RotateTeamPassword`'s `preview_password` refusal (D6); tests (no password in any answer or row but the hash).
- [x] T005 API: `scopeRules` as rules with the three new entries (D5), `allowedForDeployTokens`,
      `TestScopedTokenWalksEveryRoute` for the new shapes; `getPreviews`, `putPreviewSettings`, `closePreview`;
      `Environment.preview`; the four codes; error mapping; `api/openapi.yaml` 0.55.0 (contract test, parity test).
- [x] T006 MCP: nothing new (deploy-only tools); check the contract test still passes.

## Phase B: console and docs

- [x] T007 Project page Previews section and preview rows with expiry and Close preview (D11), no-JS forms,
      `tools/ui-css.sh build` if classes changed, UI tests (no password on any page).
- [x] T008 `docs/previews.md`, `docs/plan.md`, `changelog.d/049-previews.md`, CLAUDE.md paragraph (D12).

## Phase C: lab

- [x] T009 Gate step `previews` (plan "Lab"), run alone:
      `CARAVANA_LAB=1 go test ./cmd/caravana -run 'TestLab/^(api-needs-token|previews)$' -v -timeout 20m`.

## Phase D: caravana-action (`../caravana-action`)

- [ ] T010 Preview mode in `caravana-deploy.sh` and `action.yml` per `contracts/action.md`.
- [ ] T011 `test/run.sh` and `test/fake_caravana.py` cases; shellcheck.
- [ ] T012 README: preview mode, the workflow example (`pull_request` types, `permissions: pull-requests: write`,
      skipping the build on `closed`), DNS and password notes, the Caravana version it needs (0.55.0's release).
