# Tasks: Keep and edit the Compose file

**Input**: [plan.md](./plan.md), [compose-file-edit.spec.md](./compose-file-edit.spec.md),
[research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/),
[quickstart.md](./quickstart.md)

**Scale note**: 20 tasks over about 25 files. The stories are independent in what they do, but
they share five files: `internal/service/deploy.go`, `api/openapi.yaml`,
`internal/api/parity_test.go`, the console's route table (`internal/ui/ui.go`) and the generated
`internal/ui/static/app.css`. Run the story phases one after another, in the order below. Inside a
phase, tasks marked [P] touch different files and can run side by side. The two risky tasks are:
- T003: every existing call site must keep working with a `nil` file.
- T017: no secret value may reach any page, download or API answer.

**Tests are required** (constitution VIII). Each task writes its own tests next to the code
(table-driven, `testing` only) and leaves these passing: `go build ./...`, `go vet ./...`,
`gofmt -l .` and `go test ./<its packages>`. A task that changes templates or
`internal/ui/style/` runs `tools/ui-css.sh build` and commits the regenerated `app.css`. A task
that adds or changes an API route changes `api/openapi.yaml` in the same commit (the contract
test checks it).

## Phase 1: Setup

No new module and no new tool. The migration is in Phase 2.

## Phase 2: Foundational (blocks every story)

Files: `internal/compose/compose.go`, `internal/compose/compose_test.go`,
`internal/store/migrations/0007_compose_file.sql`, `internal/store/apps.go`,
`internal/store/deployments.go`, `internal/store/*_test.go`, and the store call sites in
`internal/service`, `internal/api`, `internal/ui` and `cmd/caravana` (tests included)

- [X] T001 [P] `Request.WithoutSecretValues` (data-model "Kept file", research R11): in internal/compose/compose.go, beside `Request`, add `func (r Request) WithoutSecretValues() Request`. It returns a copy with a new `Services` map and new `Secrets` slices in which every `app.Secret.Value` is nil and names are kept; the caller's request is never changed. A nil or empty `Services` stays as it is. Tests in internal/compose/compose_test.go: values gone, names and order kept, other overlay fields equal, the original request unchanged (mutate the copy and check the original), empty and nil maps.
- [X] T002 [P] Migration (data-model "Migration"): add internal/store/migrations/0007_compose_file.sql with the two `ALTER TABLE … ADD COLUMN compose TEXT;` statements and the comments in data-model.md. Check that the store's migration runner picks it up the way it picks up 0006_rollback.sql (embedded, by file name), and extend the store's migration test if there is one listing the files.

**⟶ Wait for T001 and T002, then:**

- [X] T003 Store records and write calls (research R1–R3, data-model "Store records"), in internal/store/apps.go and internal/store/deployments.go:
  - Add `Compose *compose.Request` to `store.AppRecord` and `store.DeploymentRecord`.
  - Change the signatures to `SaveApp(ctx, a, file *compose.Request)`, `SaveAppIf(ctx, a, file, revision)`, `InsertApp(ctx, a, file)` and `CreateDeployment(ctx, appID, d, definition, file, source)`.
  - Each write stores `json.Marshal(file.WithoutSecretValues())` in the `compose` column, or NULL when `file` is nil.
  - `revisionOf(document, compose string, kept bool)` hashes the document, then, only when there is a file, a zero byte and the compose JSON. An app without a file must keep exactly today's revision; add a test that pins a known revision value computed before the change.
  - `SaveApp`'s `WHERE apps.document != excluded.document` becomes "document or compose differs" (mind NULLs: use `IS NOT`). Make the same change to `SaveAppIf`'s `UPDATE … WHERE`.
  - Add the column to `appColumns` and `deploymentColumns` and to `scanApp` and `scanDeployment`. All reads return `Compose`.

  Then update every caller in non-test code to pass `nil`. The service paths get real files in T004, so the service must stay behavior-identical in this task. Update every test call site the same way: `grep -rn -e 'SaveApp(' -e 'SaveAppIf(' -e 'InsertApp(' -e 'CreateDeployment(' --include='*.go' .`, about 90 sites in internal/store, internal/service, internal/api, internal/ui and cmd/caravana.

  New store tests:
  - the file round-trips byte for byte, including comments and a trailing newline;
  - secret values are never stored (read the raw column with SQL and look for a sentinel value);
  - a comment-only file change moves the revision and `UpdatedAt`;
  - saving the same app and file again changes nothing;
  - `SaveAppIf` refuses a stale revision when only the file changed;
  - `DeleteApp` leaves no row holding the file.

  Files: internal/store/apps.go, internal/store/deployments.go, internal/store/apps_test.go, internal/store/deployments_test.go, plus the mechanical call-site edits.

**Checkpoint**: `go build ./... && go test ./...` pass. Nothing visible has changed yet: every app has no file.

## Phase 3: User Story 1 - Edit the Compose file an app was made from (P1) 🎯 MVP

**Goal**: Configure for a Compose app shows the kept file and overlay, previews and deploys an edit (conditional on the revision), and shows the definition read-only.

**Independent test**: quickstart §1. Create `kuma` from a file with comments, open Configure (file and comments shown), change the tag, preview, deploy: the deployment is healthy, the new image runs, Configure shows the edited file, and a second tab's deploy is refused as stale.

Files: `internal/service/deploy.go`, `internal/service/compose.go`, `internal/service/deploy_test.go`, `internal/service/compose_test.go`, `internal/ui/composeform.go`, `internal/ui/composeform_test.go`, `internal/ui/configure.go`, `internal/ui/configure_test.go`, `internal/ui/configure_save_test.go`, `internal/ui/composedeploy.go`, `internal/ui/composepage_test.go`, `internal/ui/app.go`, `internal/ui/templates/app.html`, `internal/ui/templates/compose.html`, `internal/ui/style/`, `internal/ui/static/app.css`, `cmd/caravana/console_lab_test.go`

- [X] T004 [US1] Service keeps the file (research R4, R5; data-model "Service"), in internal/service/deploy.go and internal/service/compose.go:
  - Add `type made struct { file *compose.Request; warnings []app.Warning }`.
  - `saveAndDeploy(ctx, a, cond, trigger, m made, pin)` replaces its `warnings` argument with `made`. `saveApp` passes `m.file` to the store. `deployWarned(ctx, a, trigger, m made, pin)` passes `m.file` to `CreateDeployment`.
  - In `saveAndDeploy`, when the stored app has `Compose != nil` and `m.file == nil`, append the warning `This app's Compose file was dropped: it was deployed from a definition, not a file.` (exact text, no `Field`), next to `unmountedVolumes` and `RemovedSecrets`.
  - `DeployComposeIf` passes `made{file: &req, warnings: warnings}`. The store strips secret values, but strip them here too: `req.WithoutSecretValues()` is the kept copy, while the unstripped `req` still feeds `compose.Translate` so values reach the secret store.
  - `Deploy`, `DeployIf` and `CreateApp` pass `made{}`, so a definition deploy on a kept-file app drops the file with the warning.
  - `Redeploy` and `RedeployVersion` stay as they are in this task (US3 and US4 change them), but compile with `made{}`.

  Tests in internal/service/compose_test.go and deploy_test.go:
  - a Compose deploy keeps the file on the app and on the deployment, byte for byte;
  - no secret value is kept (sentinel);
  - a second Compose deploy replaces the file;
  - `DeployIf` with a stale revision after a comment-only Compose edit gives `ErrAppChanged`;
  - `Deploy` of a definition on a kept-file app drops the file and records the warning once;
  - `Deploy` on an app without a file adds no warning.
- [X] T005 [P] [US1] `draftOf` (research R7), in internal/ui/composeform.go: add the pure `draftOf(req compose.Request) composeDraft`, the inverse of `composeDraft.request()`. It sets the file text and name and, per overlay service (sorted, as `sortServices`), sets domains (with `:port` only when the request names a port), health path, port, interval and timeout (`secondsText`), backup engine, schedule and keep, and secrets as `draftSecret{Exists: true}` with empty values. Then it calls `build()` on each service.

  Round-trip test in internal/ui/composeform_test.go: `parse(post(draftOf(r))).request()` equals `r.WithoutSecretValues()` for each pair in internal/compose/testdata (`*.compose.yaml` + `*.overlay.json`), for deploy/examples/hollo (`compose.yaml` + `overlay.example.json`), and for a request with no overlay at all. Here `post` turns the draft into the form values the template would post; reuse the helper composeform_test.go already has for 014's tests if there is one.
- [X] T006 [US1] Configure with a kept file (research R6, contracts/console.md "App with a kept file"):
  - In internal/ui/app.go `configurePage`: when `sum.Compose != nil`, build a `composeView` (as `newComposeView` does) whose draft is `draftOf(*sum.Compose)` with `keepStored(sum.App)` applied and `Revision = sum.Revision`, posting to `/apps/{id}/compose/preview` and `/apps/{id}/compose`. Put it on `configureTab` as `ComposeForm *composeView`, and set `Edit.ReadOnly = true`.
  - In internal/ui/composedeploy.go `showComposeDeploy`: prefill the same way when the app has a kept file.
  - In internal/ui/templates/app.html: the "configure" template draws `{{template "compose-form" .ComposeForm}}` first when set. With `ReadOnly`, the per-service definition is drawn as read-only `defPanel`s headed "This app is edited through its Compose file." (no inputs, no Save). Replace 014's `Compose` note with this when a file is kept.
  - In internal/ui/templates/compose.html: give the file textarea `class` font-mono (already, check), `spellcheck="false"`, `wrap="off"`, `autocapitalize="off"`, rows from the line count bounded to 12–40 (a view field, not template maths), and the hint "Put passwords and keys in secrets below, not in the file."
  - Run `tools/ui-css.sh build` if a new class is used.

  Tests in internal/ui/configure_test.go: the page shows the kept file text (with a `#` comment) HTML-escaped, the overlay values, secret names with empty inputs, the hidden revision, the read-only definition and its line; image apps and Compose apps without a file render as in 014. Test in internal/ui/composepage_test.go: `GET /apps/{id}/compose` is prefilled the same way.
- [X] T007 [US1] Per-field save refused on a kept-file app (contracts/console.md "Per-field save"), in internal/ui/configure.go `saveConfigure`: when the stored app has a kept file, answer 409 with the Configure page and the notice "This app is edited through its Compose file. Change the file above and deploy it." Nothing is deployed. Test in internal/ui/configure_save_test.go: 409, the notice, no new deployment, the file still kept.

**⟶ Wait for T004–T007, then:**

- [X] T008 [US1] Lab step, first half (research R12), in cmd/caravana/console_lab_test.go: add the step `console-compose-file-edit` after `console-compose`. It does the following:
  - creates a Compose app `cfe` from a one-service file with a `#` comment (whoami `v1.10`, port 80, domain `cfe.localhost`, health `/`) through the console form;
  - waits for healthy, then GETs Configure and checks the comment is in the page;
  - posts the Compose form with the tag changed to `v1.11` (preview, then deploy, with the revision from the page);
  - waits for healthy, then checks that Configure shows `v1.11` and that `GET /v1/apps/cfe` has the new image.

  Leave room for T012 to add the rollback half. Delete the app at the end of the step (or in `console-delete` if that step is the shared cleanup).

**Checkpoint**: US1 works end to end on its own, and is the MVP.

## Phase 4: User Story 2 - Apps made from Compose before this feature (P2)

**Goal**: a Compose app with no kept file gets a "paste it once" note, an empty editor and the overlay prefilled; after one deploy the file is kept.

**Independent test**: quickstart §5. On a Compose app with no file, Configure shows the note, an empty editor, the overlay from the app and an editable per-field form. Paste and deploy, and Configure then shows the file and the read-only definition.

Files: `internal/ui/app.go`, `internal/ui/configure.go`, `internal/ui/configure_test.go`, `internal/ui/templates/app.html`, `internal/ui/templates/compose.html`, `internal/ui/static/app.css`

- [X] T009 [US2] Not-kept Configure (research R6, contracts/console.md "App made from Compose, no kept file"):
  - In internal/ui/app.go `configurePage`: when `sum.Compose == nil` and `sum.App.Source.Kind == app.SourceCompose`, set `ComposeForm` to a view whose draft is `draftFrom(sum.App)` (014) with an empty file, `NotKept: true` and the revision. Leave `Edit.ReadOnly` false.
  - In the template: above the form, the notice "This app's Compose file wasn't kept. Paste it once to edit it here from now on." Both the Compose form and the editable per-field form are on the page.

  Tests in internal/ui/configure_test.go: the note, an empty textarea, overlay domains, health and secret names from the app, the editable form (inputs present), and image apps unchanged. A service test (internal/service/compose_test.go) checks that a Compose deploy on an app saved with a nil file, then read, has the file.

**Checkpoint**: Uptime Kuma's path on the host works (SC-005).

## Phase 5: User Story 3 - The file follows the app's history (P2)

**Goal**: rollback makes the version's file the kept one, and each history entry made from a file offers that file for download.

**Independent test**: quickstart §3. Deploy file A, then B. Redeploy A's version, and Configure shows A. Each of A's and B's entries downloads its own file byte for byte. Entries without a file show no link.

Files: `internal/service/rollback.go`, `internal/service/deploy.go`, `internal/service/rollback_test.go`, `internal/ui/versions.go`, `internal/ui/app.go`, `internal/ui/ui.go`, `internal/ui/versions_test.go`, `internal/ui/templates/app.html`, `internal/ui/static/app.css`, `internal/api/parity_test.go`, `cmd/caravana/console_lab_test.go`

- [X] T010 [US3] Rollback restores the file (research R4, FR-006), in internal/service/rollback.go: `RedeployVersion` passes `made{file: rec.Compose}`. A version with a file makes it the kept file; one without drops the kept file, and T004's check in `saveAndDeploy` adds the warning.

  Tests in internal/service/rollback_test.go: deploy file A, then B, redeploy A, and the kept file is A with A's overlay; redeploy a version without a file over a kept-file app, and the file is dropped with the warning; secret values are still kept in the secret store (the existing redeploy secret tests stay green).
- [X] T011 [US3] History download (research R8, contracts/console.md "Deployments history"):
  - In internal/ui/app.go: add `ComposeHref string` to `historyEntry`.
  - In internal/ui/versions.go `addVersionActions`: set `ComposeHref = base + "/compose"` when `rec.Compose != nil`, independent of `rec.Definition`.
  - Add the handler `downloadCompose` for `GET /apps/{id}/deployments/{deploymentId}/compose`. It reads the deployment through `s.svc.GetDeployment(ctx, id, depID)` and answers the file text with `Content-Type: application/yaml; charset=utf-8`, `Content-Disposition: attachment; filename="<app>-<deploymentId>.compose.yaml"` and `Cache-Control: no-store`. A deployment without a file, or not of this app, gets the not-found page (404).
  - Register the route in internal/ui/ui.go `routeTable` (with `requireSession`).
  - In internal/ui/templates/app.html: add the link "Download Compose file" next to "Export jobspec".
  - In internal/api/parity_test.go: add `"GET /apps/{id}/deployments/{deploymentId}/compose": {ops: []string{"getDeploymentCompose"}}`. That operation lands in T014, so if this task runs first, add the parity entry in T014 instead and note it there.

  Tests in internal/ui/versions_test.go: link present only for entries with a file; download is byte-identical, with the headers above; 404 for no file and for another app's deployment; no secret sentinel in the body.

**⟶ Wait for T010 and T011, then:**

- [X] T012 [US3] Lab step, rollback half, in cmd/caravana/console_lab_test.go `console-compose-file-edit`: redeploy the `v1.10` deployment from the console, wait for healthy, check that Configure shows `v1.10` and the original comment, and that the history download of the first deployment equals the first file byte for byte.

**Checkpoint**: rollback leaves the right file in the editor (SC-002).

## Phase 6: User Story 4 - Everything else keeps the file, and the API sees it (P3)

**Goal**: secret edits, the webhook and plain redeploys keep the file; the API reads app and deployment files; definition deploys drop the file with the warning; no secret value anywhere.

**Independent test**: quickstart §2, §4 and §6. Set a secret, trigger the webhook and redeploy, and the file is unchanged each time with the overlay's secret names following. `getCompose` round-trips through `deployCompose` with `If-Match`. A definition deploy warns and `getCompose` answers 404 `compose_not_kept`. A sentinel secret value appears nowhere.

Files: `internal/service/deploy.go`, `internal/service/secretedit.go`, `internal/service/secretedit_test.go`, `internal/service/webhook_test.go`, `internal/service/deploy_test.go`, `internal/api/compose.go`, `internal/api/deployments.go`, `internal/api/apps.go`, `internal/api/api.go`, `internal/api/errors.go`, `internal/api/compose_test.go`, `internal/api/deployments_test.go`, `internal/api/apps_test.go`, `internal/api/parity_test.go`, `api/openapi.yaml`, `internal/ui/*_test.go`

- [X] T013 [US4] Redeploys and secret edits carry the file (research R4 "Secret edit detail", FR-007):
  - In internal/service/deploy.go: `Redeploy` passes `made{file: rec.Compose}` to `deployWarned`. The app isn't saved, but the deployment records the file.
  - In internal/service/secretedit.go `editSecrets`: when `rec.Compose != nil`, copy it with `WithoutSecretValues()`, set `file.Services[service].Secrets` to the new list's names (create the `Services` map and the service's `Overlay` entry if missing; an empty list removes the `secrets` key, leaving an overlay entry with no fields deleted), and deploy through `s.saveAndDeploy(ctx, a, Condition{}, trigger, made{file: &file}, nil)` instead of `s.Deploy`. Without a file, behavior is unchanged.

  Tests: set and remove a secret on a kept-file app, and the `compose` text is identical while the overlay's secret names follow (internal/service/secretedit_test.go). `Redeploy` and the webhook keep the file and record it on the new deployment (internal/service/deploy_test.go, internal/service/webhook_test.go). No warning is added on any of these paths.
- [X] T014 [P] [US4] API reads (contracts/api.md, research R9):
  - In internal/api/compose.go: add `getCompose` for `GET /v1/apps/{id}/compose`. It reads `s.svc.GetApp` and answers 200 with `rec.Compose` as JSON and `ETag: "<revision>"`; with no file, 404 `compose_not_kept`, message "This app's Compose file wasn't kept."
  - Add `getDeploymentCompose` for `GET /v1/apps/{id}/deployments/{deploymentId}/compose`, using `s.svc.GetDeployment`. With no file, 404 `compose_not_kept`, message "This deployment wasn't made from a Compose file."
  - Add `codeComposeNotKept = "compose_not_kept"` to internal/api/errors.go, and register both routes in internal/api/api.go with the same auth as the other `/v1` reads.
  - Add `ComposeKept bool \`json:"composeKept"\`` to `appRecordJSON`, to `appSummaryJSON` (internal/api/apps.go) and to `deploymentJSON` (internal/api/deployments.go `toDeploymentJSON`).
  - In api/openapi.yaml, 0.11.0 → 0.12.0: the two GET operations (reusing the `ComposeRequest` schema; document "secret values are never returned"), the `composeKept` fields, the `compose_not_kept` code, and a sentence on `deployApp` (the drop warning) and on `redeployDeployment` (the version's file comes back).
  - In internal/api/parity_test.go: add `getCompose` to `GET /apps/{id}/configure` and `GET /apps/{id}/compose`. T011 already added the entry `"GET /apps/{id}/deployments/{deploymentId}/compose"` with `why: "the file download; the API twin getDeploymentCompose lands in T014"`: switch it to `ops: []string{"getDeploymentCompose"}`.

  Tests in internal/api/compose_test.go, deployments_test.go and apps_test.go: 200 with the ETag; the 404 codes; `composeKept` true and false; the round trip GET → `deployCompose` with `If-Match` gives 202 and keeps secret values (fake secret store unchanged); the contract and parity tests pass.

**⟶ Wait for T013 and T014, then:**

- [X] T015 [US4] Definition deploy through the API drops the file (FR-009), in internal/api/deployments_test.go: `deployApp` with a body on a kept-file app answers 202, the deployment's `warnings` contain the exact drop sentence, `getCompose` then answers 404 `compose_not_kept`, and `getApp.composeKept` is false. Also: `deployApp` with no body keeps the file. No production code is expected: if a test fails, fix it in internal/service/deploy.go.
- [ ] T016 [P] [US4] Console after a drop, in internal/ui/configure_test.go: after a definition deploy, Configure for that Compose app shows the "wasn't kept" note and the editable per-field form (US4 independent test), and the history entry shows the drop warning.
- [ ] T017 [US4] No secret value anywhere (SC-003, FR-002, research R11): one test per path that shows a file. Each deploys a Compose app whose overlay has secret value `s3cret-sentinel-015`, then asserts the bytes never appear in the response bodies of:
  - Configure (`GET /apps/{id}/configure`, internal/ui/configure_test.go);
  - Deploy from Compose (`GET /apps/{id}/compose`, and after a refused deploy that redraws the typed draft, internal/ui/composepage_test.go);
  - the history page and the download (internal/ui/versions_test.go);
  - `getCompose`, `getDeploymentCompose`, `getApp`, `listApps`, `listDeployments` and `getDeployment` (internal/api/compose_test.go);
  - the store's raw `apps.compose` and `deployments.compose` columns (internal/store/apps_test.go).

  Where a test already exists for a path from an earlier task, extend it rather than duplicate it.

**Checkpoint**: all four stories work in the API and the console; the file never drifts (SC-004).

## Phase 7: Polish & cross-cutting

- [ ] T018 [P] Docs: in CLAUDE.md "Where we are", add the feature 015 paragraph (kept file on app and deployment, migration 0007, revision covers it, Configure edits the file and the definition is read-only, "paste it once", rollback restores it, history download, `getCompose` / `getDeploymentCompose` / `composeKept` in API 0.12.0, definition deploys drop it with a warning, lab step `console-compose-file-edit`). In docs/plan.md, mark 015 done, keeping the numbering agreed on 2026-10-14 (015 Compose file, 016 volumes + backups, then Aspire 017, Bella Baxter 018).
- [ ] T019 Full check (CLAUDE.md "Commands"): `go mod tidy -diff`, `go build ./...`, `go vet ./...`, `go tool staticcheck ./...`, `gofmt -l .`, `go test -race ./...`, `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` and `GOARCH=amd64`, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`. Run them all and fix whatever fails.
- [ ] T020 Lab validation: `eval "$(deploy/dev/lab.sh env)"`, then `CARAVANA_LAB=1 go test ./cmd/caravana -run TestConsoleLab -v`, and then the quickstart (§1–§6) by hand in a browser with JavaScript off and on. Record the results in this file under T020, as 014's T028 did.

## Dependencies & Execution Order

- **Phase 2** blocks everything: (T001 ∥ T002) → T003.
- **US1 (Phase 3)**: (T004 ∥ T005) → (T006 ∥ T007) → T008. T005 is pure and needs nothing from T004; T006 needs both; T007 needs T004.
- **US2 (Phase 4)**: needs T006 (the Compose form on Configure). T009 only.
- **US3 (Phase 5)**: needs T004. T010 ∥ T011 → T012 (T012 also needs T008).
- **US4 (Phase 6)**: T013 needs T004; T014 needs only Phase 2 and can run in parallel with US1–US3 if someone is free, but it shares `parity_test.go` with T011. Then T015, T016 ∥, then T017 last (it reads every path).
- **Polish**: T018 any time after the stories; T019, then T020 last.

Story order: US1 → US2 → US3 → US4.

## Parallel examples

```text
Phase 2: T001 (internal/compose)        ∥ T002 (store migration)
US1:     T005 (ui/composeform.go draftOf) ∥ T004 (service), then T006 ∥ T007 after T004
US3:     T010 (service/rollback.go)     ∥ T011 (internal/ui history download)
US4:     T014 (internal/api, openapi)   ∥ T013 (internal/service)
         T016 (ui test)                 ∥ T015 (api test)
```

## Implementation strategy

- **MVP**: Phase 2 + US1 (T001–T008). The daily pain (editing Uptime Kuma as a file) is solved
  for every app deployed from Compose after the upgrade.
- **Then** US2 (existing apps, including Uptime Kuma on the host), US3 (rollback and download)
  and US4 (no drift, API parity, the secret sweep). Each phase ends at a checkpoint where
  `go test ./...` passes, and US1 and US3 also pass `TestConsoleLab`.
- **Delegation**: each phase goes to one Sonnet agent with this file, the spec, plan, research,
  data model and contracts. Opus reviews the diff, runs the tests and checks the task's
  acceptance before the next phase starts.
