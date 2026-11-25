# Feature 058: template inputs (choices, optional and conditional inputs, 20 inputs, catalog format 2)

Requested by the maintainer, 2026-11-25. Feature 035's templates allow 12 inputs, require every input without a
default and can't choose between alternatives. Codemagic Patch signs in with GitHub, Bitbucket Cloud or GitLab and
stores files in R2 or Google Cloud Storage, so the catalog had to ship `codemagic-patch` and
`codemagic-patch-bitbucket`, near-identical.

## User stories

1. As a template author I can ask a **choice** between a few alternatives, ask more questions (up to 20), and mark
   an input optional, so one template covers what two did.
2. As an author I can ask an input, write a Compose line and add an overlay item **only for one answer**, so nothing
   empty reaches the app.
3. As an owner installing a template I see only the questions my choices ask (with script), or all of them under
   a heading that says which choice each is for (without), and a Preview that shows what my choices make.
4. As an owner of a Caravana that is not updated, my template list **keeps updating**: the catalog's old file stays
   readable by it.
5. As the catalog's CI, `template check` renders every combination so a defect that only one alternative has is
   found before a release.

## Requirements

- FR-1 Inputs: at most 20; `required: false`; type `choice` (2–8 choices, default one of them); `when` on an input.
- FR-2 Conditions on Compose lines (marker comment) and on overlay items (`when`); the kept file and the stored app
  never hold either.
- FR-3 An input that isn't asked is empty and what was sent for it is ignored, stored nowhere, drawn nowhere.
- FR-4 `catalog.json` stays format 1 with only what format 1 can say; `catalog.v2.json` is format 2; one
  `SHA256SUMS` has a line for each. Caravana fetches v2 and falls back to `catalog.json`.
- FR-5 A format-2 template this Caravana can't read is listed as unavailable; the rest of the catalog is kept.
- FR-6 `Check` renders every combination of choices (at most 64) with optional inputs empty and filled; any warning
  is an error.
- FR-7 API 0.65.0, MCP `get_template`, console radios/select, "Only for X" groups, script hiding groups.
- FR-8 The catalog repository's `codemagic-patch` is one template with a `signin` choice.

## Out of scope

Choices that depend on a text input, conditions with `or` between inputs, a `multi` choice, templates that compute
values, catalog format 3, a console editor of templates.
