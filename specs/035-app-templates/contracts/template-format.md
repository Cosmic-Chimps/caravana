# Contract: the template format (format 1)

Read by `internal/templates` (pure: no network, no disk but an `fs.FS` given to it, no clock, no
randomness but an `io.Reader` given to it).

## Source layout (the catalog repository)

```
catalog.json                    {"format": 1, "version": "2026.11.12"}
templates/<name>/template.json  the manifest
templates/<name>/compose.yaml   the Compose file with {{placeholders}}
templates/<name>/logo.svg       optional
```

`<name>` is the directory's name and must equal the manifest's `name`.

## Bundle (what Caravana embeds and fetches)

`catalog.json`:

```json
{
  "format": 1,
  "version": "2026.11.12",
  "templates": [ { ...manifest..., "compose": "<compose.yaml text>", "logo": "<svg text or absent>" } ]
}
```

Templates sorted by name. `SHA256SUMS` beside it: one line `<64 hex>  catalog.json\n`.
`caravana template bundle <dir> <outdir>` writes both from the source layout, after checking every
template (the bundle of an invalid catalog is never written).

## Manifest (`template.json`)

```json
{
  "name": "hollo",
  "title": "Hollo",
  "version": "0.9.25-1",
  "description": "A single-user fediverse server: your own account on Mastodon's network.",
  "tags": ["fediverse", "social"],
  "upstream": "https://github.com/fedify-dev/hollo",
  "appName": "hollo",
  "notes": "After the first deploy, open https://{{domain}}/setup to create your account.",
  "requires": "0.20.0",
  "broken": "",
  "lab": true,
  "inputs": [
    {"name": "domain", "label": "Domain", "type": "domain", "help": "Its DNS must point at this server.",
     "example": "social.example.com"}
  ],
  "generated": [
    {"name": "db_password", "length": 32, "alphabet": "alnum"},
    {"name": "secret_key", "length": 64, "alphabet": "hex"}
  ],
  "services": {
    "hollo": {
      "domains": [{"host": "{{domain}}", "port": 3000}],
      "health": {"path": "/nodeinfo/2.1"},
      "secrets": [
        {"name": "SECRET_KEY", "value": "{{secret_key}}"},
        {"name": "DATABASE_URL", "value": "postgres://hollo:{{db_password}}@postgres:5432/hollo"}
      ]
    },
    "postgres": {
      "secrets": [{"name": "POSTGRES_PASSWORD", "value": "{{db_password}}"}],
      "backup": {"engine": "postgres", "keep": 14}
    }
  }
}
```

Field rules (each violation is an error naming the template and the field, e.g.
`hollo: inputs[0].type: must be one of domain, email, text, secret`):

| Field | Rule |
| --- | --- |
| `name` | `app.ValidName`, ≤ 31 bytes, unique in the catalog |
| `title` | 1–60 printable characters |
| `version` | 1–40 bytes of `[A-Za-z0-9._+-]` |
| `description` | 1–200 printable characters, one line |
| `tags` | 0–6, each `^[a-z0-9][a-z0-9 -]{0,23}$`, no duplicates |
| `upstream` | an `https://` URL |
| `appName` | `app.ValidName`; the console's default app name |
| `notes` | optional, ≤ 2000 bytes, plain text; may hold **plain** placeholders, rendered for display after install |
| `requires` | optional release version (`0.20.0`) |
| `broken` | optional; non-empty means not offered, the text is the reason |
| `lab` | optional bool, default true; `false` skips the catalog's lab job (the notes say why) |
| `inputs[]` | 0–12; `name` `^[a-z][a-z0-9_]{0,31}$` unique across inputs and generated; `label` 1–40; `type` one of `domain`, `email`, `text`, `secret`; `help` ≤ 200; `default` optional (not for `secret`), must itself be valid; `pattern` required for `text` (Go regexp, anchored by Caravana with `^(?:…)$`, ≤ 200 bytes); `example` required unless `default` is set, valid for the type (`secret` examples are used only by `check`) |
| `generated[]` | 0–12; `name` as inputs; `length` 16–128; `alphabet` `hex`, `alnum`, `base64url` |
| `services` | map of service name (must be a service of the Compose file) → overlay with the shape of `compose.Overlay` (`domains`, `health`, `secrets`, `backup`) |
| `compose` | ≤ `compose.MaxComposeBytes`; placeholders only plain names |
| `logo` | see research R8 |

Placeholders: `\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`. Every placeholder must name a declared input or generated
secret; secret-valued names only inside `services.*.secrets[*].value`; every declared input and generated
secret must be used at least once (an unused input is a field the owner fills for nothing).

## Values

| Type | Accepts | Then |
| --- | --- | --- |
| `domain` | a host name: lowercase after trimming, labels `[a-z0-9-]` not starting or ending with `-`, at least one dot or `.localhost`, ≤ 253 | Compose-safe |
| `email` | `local@domain`, local `[A-Za-z0-9._%+-]{1,64}`, domain as above | Compose-safe |
| `text` | matches the input's pattern | Compose-safe |
| `secret` | 1–4096 bytes, no NUL, no newline | overlay secret values only |

Compose-safe: `^[A-Za-z0-9._@+:/=-]{1,253}$`. A value is trimmed of surrounding spaces before checks. An
input with a `default` that the owner leaves empty takes the default; without one it is required.

## Functions (package `internal/templates`)

```go
func ParseCatalog(data []byte) (Catalog, error)            // bundle → catalog, every template validated
func ReadSource(fsys fs.FS, version string) ([]byte, error) // source layout → bundle bytes (deterministic)
func Sums(bundle []byte) []byte                             // the SHA256SUMS text
func (t Template) CheckInputs(in map[string]string) (map[string]string, error) // trimmed+defaults, or *app.FieldError inputs.<name>
func (t Template) Generate(r io.Reader) (map[string]string, error)
func (t Template) Render(values map[string]string) (compose.Request, error)     // values = inputs ∪ generated
func (t Template) RenderNotes(inputs map[string]string) string
func (t Template) ExampleInputs() map[string]string
func (t Template) DomainInputs() map[string]string // overlay path "services.<svc>.domains[<i>].host" → input name, for R5
func Check(t Template, settings nomadjob.Settings) error // example inputs + fixed fake generated values → Render → compose.Translate (any warning is an error) → nomadjob.Check → registry.Parse per image → nomadjob.Translate
func Embedded() (Catalog, []byte)                          // the embedded bundle, parsed once
func Newer(a, b string) bool                               // catalog versions, numeric parts
```

`Render` returns the request with `Name` empty (the display name is the app ID, as for any Compose deploy).
