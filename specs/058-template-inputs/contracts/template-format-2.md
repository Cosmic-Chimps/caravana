# Contract: the template format (format 2, current)

This is the **current** format. `specs/035-app-templates/contracts/template-format.md` is format 1: it stays
the contract of `catalog.json`, which every Caravana up to 0.22 reads. Everything not named here is as in
format 1 (field rules, values, placeholders, the bundle's layout, `Check`).

Feature 060 adds to it, in format 2 only: the manifest's `certificates` and the placeholder filter
`{{name|bcrypt}}`. See `specs/060-opensearch-apps/contracts/template-format-060.md`.

## The two bundles

A release of the catalog repository publishes:

| File | Format | Holds | Read by |
| --- | --- | --- | --- |
| `catalog.json` | 1, for good | templates valid under format 1's rules, unchanged; a frozen variant where one has it | Caravana 0.22 and older |
| `catalog.v2.json` | 2 | every template, as written | Caravana from the release of 058 |
| `SHA256SUMS` | | one line for each: `<64 hex>  catalog.json`, `<64 hex>  catalog.v2.json` | both |

Why format 1 never changes: Caravana up to 0.22 reads `catalog.json` with unknown fields refused and format 1's
limits, and refuses the **whole** catalog when one template breaks a rule; its template list would stop
updating. Its `SumFromSums` skips lines for other names, so the extra `SHA256SUMS` line is harmless (checked
against v0.22.0's own parser, plan D2).

Source layout (the catalog repository), as in format 1, plus an optional frozen variant:

```
catalog.json                       {"format": 2, "version": "2026.11.25"}   (1 is accepted too)
templates/<name>/template.json     the manifest, format 2
templates/<name>/compose.yaml      the Compose file, format 2
templates/<name>/template.v1.json  optional: the frozen format-1 manifest
templates/<name>/compose.v1.yaml   its Compose file (required with template.v1.json)
templates/<name>/logo.svg          optional, shared
```

`caravana template bundle` makes `catalog.v2.json` from every `template.json`/`compose.yaml`; and `catalog.json`
from, per folder: the frozen variant if there is one (it must pass format 1), else the format-2 files when they
pass format 1's rules (no `required`, `choices`, `when`, marker, more than 12 inputs, or a type but the four),
else nothing. A folder with only the frozen files is in `catalog.json` only. The command says which templates
are in one bundle only. `template check` checks both bundles' templates.

## Fetch (Caravana)

`SHA256SUMS` first. Its line for `catalog.v2.json` names the bundle to fetch; with none (a release made before
format 2) the line for `catalog.json` does. The bundle is fetched only when its sum differs from the stored and
the embedded one, verified against that line, parsed, checked, and kept when its version is newer. A stored
format-1 bundle keeps being read. The embedded catalog is `catalog/catalog.v2.json` (format 2).

## Inputs

| Field | Format 2 |
| --- | --- |
| `inputs[]` | 0–**20** (format 1: 12) |
| `type` | `domain`, `email`, `text`, `secret` or **`choice`** |
| `required` | optional bool, default true. `false`: an asked input may be left empty and renders as an empty string (a `default` still fills it). Not for format 1. |
| `choices[]` | for `choice` only: 2–8 of `{"value": "^[a-z][a-z0-9_-]{0,31}$", "label": 1–40 printable}`, values unique |
| `default` | on a `choice`, one of the values. A choice with no default and `required` is required. |
| `pattern`, `example` | not on a `choice` (every choice is rendered by `Check`). A choice is never secret. |
| `when` | optional condition (below) on **earlier choice inputs** by value (a string or a list): the input is asked only when it holds, and is then required unless `required: false`. An input that isn't asked is the empty answer, and **anything sent for it is ignored**: never checked, rendered, stored or drawn back. |

A choice's value renders like a text input's (it is Compose-safe by its pattern). Notes may use a choice's
placeholder. `{{name}}` of an optional input left empty is `""`. A choice used only in conditions counts as used.
The combinations of choice values must be at most 64 (the product of the numbers of choices).

## Conditions

A condition maps input names to what each must be: `"signin": "github"`, `"signin": ["bitbucket", "gitlab"]` or
`"region": true` ("not empty"; not on an input's own `when`). Every clause must hold. Names are inputs (not
generated secrets); a value clause names a `choice` input and one of its values.

1. **Compose lines.** A single-line mapping entry (`key: value`) or list item (`- value`) may end with
   `# when signin=github`, `# when signin=bitbucket,gitlab` or `# when s3_region`; several clauses separated by
   spaces must all hold. The line is kept without its marker when the condition holds, else dropped whole.
   Format errors, naming template and line: a marker on a line of its own, after a key that has a block under it,
   a block scalar or anchor, the first key of a list item that is a mapping, a flow value that goes on over several
   lines, or inside a quoted string; a bad clause; a name that isn't an input; a value that isn't a choice. An
   ordinary trailing comment that doesn't read `# when …` stays a comment.
2. **Overlay items.** An item of a service's `domains`, `secrets`, `identities` or `publish` may carry
   `"when": {...}`. Render drops the items whose condition doesn't hold and removes the key. The kept Compose file
   and the stored app never hold a marker or a `when`.
3. Format 1 refuses all of it.

## Check

`Check` renders **every** combination of the choice inputs (and so also those of a conditional choice), each once
with every optional input empty and once with each filled with its example, through the Compose translator (any
warning is an error), `nomadjob.Check` and `Translate`. A failure names the combination: `(with signin=gitlab,
optional inputs empty)`.

## Tolerance

`ParseCatalogFor(data, needsNewer)`: in a **format-2** catalog, a template this Caravana can't read keeps the
rest of the catalog and is listed with `Unsupported` (so `available: false`, `unavailable` says why):

- a field it doesn't know (`made for a newer caravana: …`), or
- a rule it fails while its `requires` is newer than the running release (`needs caravana X or newer`).

An unknown field at the catalog's top level, a template that isn't JSON of the right shape, a failure with no such
excuse, and a duplicate name still fail the catalog. Format 1 tolerates nothing. `Check` and the catalog check skip
a listed-only template.

## API (0.65.0)

`TemplateInput` gains `required`, `choices`, `when` and the type `choice`; the template's `services` are a
`TemplateOverlay` whose list items may have `when`; `TemplateInstallRequest.inputs` takes a choice's value. A
refused answer is `invalid_field` on `inputs.<name>` as before. No new route or code.

## Functions (package `internal/templates`), what is new

```go
const FormatV1, Format = 1, 2
func ParseCatalogFor(data []byte, needsNewer func(requires string) bool) (Catalog, error)
func ReadSource(fsys fs.FS, version string) (Bundles, error)   // V1, V2, OnlyV1, OnlyV2
func Sums(b Bundles) []byte
func SumFromSums(sums []byte, name string) (string, error)
func VerifySums(bundle, sums []byte, name string) error
func (t Template) Asked(in map[string]string) map[string]bool
func (t Template) DomainInputsFor(values map[string]string) map[string]string
```
