# Tasks: App templates

**Input**: [plan.md](./plan.md), [app-templates.spec.md](./app-templates.spec.md), [research.md](./research.md),
[contracts/](./contracts/)

Tests are required (constitution VIII): each layer's tests are in the task that changes it.

## Part A — server side (one implementer)

- [x] **T001** Package `internal/templates`: types, `ParseCatalog` with every rule of the format contract
  (errors name template and field), placeholders, plain vs secret-valued names, logo checks, `Newer`;
  table tests including each refusal · internal/templates/catalog.go, catalog_test.go
- [x] **T002** `CheckInputs`, `Generate` (rejection sampling, test with a fixed reader and a bias check on a
  small alphabet), `Render` (text for Compose, JSON leaves for the overlay), `RenderNotes`, `DomainInputs`,
  `ExampleInputs`; tests for each type, the Compose-safe set and an injection attempt
  (`a\nservices:`, `x # y`, `${HOME}`, `{{other}}`) · internal/templates/render.go, render_test.go
- [x] **T003** `ReadSource`, `Sums`, `Check` (compose.Translate with any warning an error, nomadjob.Check with
  default settings, registry.Parse, nomadjob.Translate), `Embedded` · internal/templates/source.go, check.go,
  embed.go, tests
- [x] **T004** The six templates of research R9 in the local catalog repository's source layout, written from
  each upstream's docs (verify health paths and env names against upstream; Hollo's README/env page,
  Uptime Kuma's wiki, Umbral's own repo at ~/Documents/code4fun/Umbral); bundle them into
  `internal/templates/catalog/` with `caravana template bundle`; a test runs `Check` on every embedded
  template and verifies `SHA256SUMS` · /Users/jjchiw/Documents/cosmic-chimps/a__cc/caravana-templates/,
  internal/templates/catalog/
- [x] **T005** `cmd/caravana template check <dir>` / `bundle <dir> <outdir>` with tests on a temp source dir ·
  cmd/caravana/template.go, template_test.go
- [x] **T006** Migration 0028 and the store methods of the plan, tests (old app rows read with no template) ·
  internal/store/migrations/0028_templates.sql, internal/store/templates.go, apps.go
- [x] **T007** Service: catalog in use (stored newer valid bundle, else embedded), `ListTemplates(q, tag)`,
  `Template(name)`, `PreviewTemplate`, `InstallTemplate(ctx, name, id, inputs, trigger)` (R4, R5),
  the daily check and `CheckTemplates`/`SetTemplateChecking` (R6, reusing the release check's `get`,
  timeouts and too-soon/disabled errors), `SetTemplatesURL`; tests with an httptest server (good, bad sum,
  unknown format, older version, 304-like unchanged sum, off = no request) and a **sentinel test**: a
  template with generated secrets fed by a reader yielding `s3cret-sentinel-035…`, installed through the
  fake deployer; the sentinel is in the secret store and in no store row, kept file, deployment record,
  log line or the SQLite file · internal/service/templates.go, templates_test.go, service.go (Start, setter)
- [x] **T008** Config `CARAVANA_TEMPLATES_URL` / `-templates-url` wired in main · internal/config, cmd/caravana/main.go
- [x] **T009** API routes of contracts/api.md, error mapping, `AppRecord.template`/`AppSummary.template`,
  openapi 0.36.0 (the route-sync test passes), scope walk table (all denied), sentinel test on install and
  preview answers · internal/api/templates.go, router, api/openapi.yaml, tests
- [x] **T010** MCP `list_templates`, `get_template` + contract test · internal/mcpserver

## Part B — console, lab, catalog repository (one implementer, after A is reviewed)

- [ ] **T011** New app's fourth tab, the grid, search and tags, the catalog footer with Check now and the
  setting; the template page with inputs, Preview and Create; the logo route; the created notice; the
  Configure fact row; `tools/ui-css.sh build`; UI tests (no JS needed, secret inputs never echoed, sentinel
  absent from every page) · internal/ui/…
- [ ] **T012** `TestConsoleLab` step `console-templates`: through the forms, Uptime Kuma with only a domain,
  healthy, volume backup policy on, `template` on the record, cleanup · cmd/caravana/console_templates_test.go
- [ ] **T013** Gate step `templates`: Hollo through the API with a domain, both services healthy, Back up now
  of postgres succeeds (reuse the gate's backup destination set-up), cleanup ·
  cmd/caravana/gate_templates_test.go
- [ ] **T014** Catalog repository: README, LICENSE, `catalog.json`, CI and release workflows,
  `lab/install-all.sh` (shellcheck clean); `git init` and one commit, **no remote, no push** ·
  ../caravana-templates
- [ ] **T015** `changelog.d/035-app-templates.md`, CLAUDE.md paragraph, `docs/plan.md` line, the full check
  list of CLAUDE.md (build, vet, staticcheck, test -race, gofmt, cross-compile, ui-css check, ui-vendor
  check, shellcheck, release.sh check)
