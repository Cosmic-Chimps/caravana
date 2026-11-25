# Tasks: template inputs (feature 058)

- [x] T001 `internal/templates`: `Condition`, markers (`condition.go`), `Template` JSON with item `when` (`jsonv2.go`), input fields, format-aware `validate`
- [x] T002 `checkInputs`/`Asked`/`ExampleInputs`/`PreviewValues`, `Render` with markers and item conditions, `DomainInputsFor`
- [x] T003 `Check` over every combination; `CheckCatalog` skips unsupported
- [x] T004 `ParseCatalogFor` (format 1 or 2, tolerant stubs); `ReadSource` returns both bundles; `Sums`, `SumFromSums`, `VerifySums` by name
- [x] T005 `caravana template check|bundle` writes `catalog.json`, `catalog.v2.json`, `SHA256SUMS`; embedded catalog is format 2
- [x] T006 service: fetch v2 first then `catalog.json`; tolerant parse with the running release; audit lists the choices; `templateError` with values
- [x] T007 API 0.65.0 (`TemplateInput`, `TemplateChoice`, `TemplateCondition`, `TemplateOverlay`), MCP description
- [x] T008 console: choice radios/select, "Only for X" groups, `template-form.js`, Preview with secret names and the file
- [x] T009 tests: format 2 rules, markers, conditions, combinations, bundles (legacy proof), fetch fallback, tolerance, API, UI, sentinels, audit
- [x] T010 catalog repository branch `codemagic-patch-choices`: one `codemagic-patch`, frozen v1 variants, workflow and README
- [x] T011 docs: contract, changelog note, CLAUDE.md
