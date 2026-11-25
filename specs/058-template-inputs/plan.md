# Plan: template inputs (feature 058)

Spec: `spec.md`. Contract: `contracts/template-format-2.md` (current; 035's is format 1). API 0.65.0 (0.64.0 was 056's),
no migration, no new module, no policy change.

## Decisions

- **D1. Two bundles, one release.** `catalog.json` is format 1 forever, `catalog.v2.json` format 2, one `SHA256SUMS`
  with two lines. Old Caravanas refuse a whole catalog for one unknown field, type or a 13th input, so nothing of
  format 2 may reach `catalog.json`.
- **D2. Checked against v0.22.0.** v0.22.0's `SumFromSums` ignores lines for other names, so the second line is
  harmless (no separate `SHA256SUMS.v2`). Its `ParseCatalog` parsed the bundle this feature's `bundle` made for the
  catalog repository (10 templates) with no error (a throwaway `git worktree` of v0.22.0 and a 30-line program);
  `legacyParse` in `internal/templates/bundles_test.go` keeps the same proof in CI (strict decode into format-1's
  own types plus its limits), and fails on `catalog.v2.json`.
- **D3. How the bundle knows what old hosts can read.** Per template folder: the format-1 file is the frozen
  `template.v1.json` + `compose.v1.yaml` when the folder has it, else the format-2 files when they pass format-1
  validation (`validate(FormatV1)` refuses `required`, `choices`, `when`, markers, > 12 inputs, type `choice`),
  else the template is left out of `catalog.json`. A folder with only the frozen files is in `catalog.json` only
  (that is how `codemagic-patch-bitbucket` stays offered to old hosts while v2 has one Codemagic template).
- **D4. Fetch order.** `SHA256SUMS`; the line for `catalog.v2.json` names the bundle, else `catalog.json`
  (a release made before 058). The stored row keeps its bundle and sum as before; a stored format-1 bundle parses.
- **D5. The embedded catalog is format 2** (`catalog/catalog.v2.json`, `SHA256SUMS` with that line). Its templates
  are the ones it had; it is rebuilt from the catalog repository at release time, not here.
- **D6. Tolerant parser.** `ParseCatalogFor(data, needsNewer)`: format 2 only; unknown template field, or a rule
  failure with a newer `requires`, makes a stub (`Template.Unsupported`) kept in the list as unavailable; everything
  else still fails the catalog. The service passes the running release; `unavailable()` reads `Unsupported` first.
- **D7. Conditions.** One `Condition` type (`name → true | value | [values]`) for input `when`, overlay items and
  Compose markers. A marker is `# when clauses` at the end of a single-line mapping entry or list item, checked by a
  small line scanner (no YAML parser in this package), strict: a bad marker is a format error naming the line.
  Overlay items keep their `when` in `Template.ItemWhens` (parallel to the lists), so `compose.Overlay` and the app
  types stay clean; `Template` has its own `UnmarshalJSON` (strict, pulls `when` out of items) and `MarshalJSON`.
- **D8. Not asked means empty and ignored.** `checkInputs` walks the inputs in order; an input whose `when` doesn't
  hold for the answers so far is `""` whatever was sent. So a secret sent for the other provider is never checked,
  rendered, kept or logged. A name the template doesn't have is still refused.
- **D9. Preview.** `PreviewValues` gives a typed secret a stand-in (`preview`) instead of `""`, so a condition on "is
  it given?" reads as at install; the preview shows secret names only and the Compose file (no secret in it).
- **D10. Check enumerates.** `checkCases`: product of the choices × (optional empty, optional filled). Failures name
  `(with signin=gitlab, optional inputs empty)`.
- **D11. Console.** A choice is radios (up to four answers, with a default or required) or a select. Inputs asked by
  the same choice are one `<fieldset data-when>` with the heading "Only for GitHub"; `static/js/template-form.js`
  (ours, no inline script) hides and disables the group when its condition doesn't hold and sets `required` only
  while shown. Without script everything is drawn, none of the conditional inputs is `required`, and the server reads
  only the asked ones (what was typed for the others isn't drawn back). The notes stay above the form.
- **D12. Audit.** `InstallTemplate` still notes one `installed` entry; it also lists `choices.<name>` for each asked
  choice (never secret).
- **D13. Domain input paths.** A refusal at `services.web.domains[1].host` is shown at the input the owner typed; with
  conditional domains the index is after dropping, so `DomainInputsFor(values)`.

## Constitution check

I Small by default: no module, no runtime dependency; one 1.5 KB script of ours. II One model: a template still
normalizes to one `compose.Request` and the app; conditions are resolved in `Render`, before translation, and
`Translate` is untouched and pure. IV Fail loudly: every illegal marker, condition or choice is a format error naming
template and field; nothing is silently dropped (a dropped line or item is the condition the author wrote). V API-first:
`api/openapi.yaml` 0.65.0 in the same commit; the console calls the service. VII Secure: secrets of a choice that wasn't
made are never read; sentinel tests in `service`, `api` and `ui`. VIII Plain tested Go: tables for the format, markers,
inputs, combinations, bundles, fetch, tolerance, API and UI.

## Deviations

- The format-2 parser's marker check is a line scanner rather than a YAML parse; the combination check
  (`Check`) renders and translates every case, which catches what the scanner can't.
