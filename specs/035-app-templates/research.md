# Research: App templates

Decisions taken without asking (the maintainer's rule: a reasoned default, recommended first). Each says
what was chosen, why, and what was rejected.

## R1 · A template is data: substitution, not a template language

**Decision**: placeholders `{{name}}` (letters, digits, `_`; optional spaces inside the braces), replaced
once, left to right, with no logic, escaping or functions. A placeholder names a declared input or
generated secret; an unknown one makes the template invalid.

**Why**: the task says "nothing executable". `text/template` has conditionals, loops and functions; a
catalog fetched from the network should not carry a program. Plain substitution is easy to audit and to
explain to a template author.

**Rejected**: Go `text/template` (logic), Compose's own `${VAR}` (Caravana's compose loader deliberately
gives variables only the file's defaults, and the overlay is JSON, not Compose).

## R2 · Where each value may go: the Compose file is never a carrier of secrets

**Decision**: two kinds of names.
- *Plain*: `domain`, `email` and `text` inputs. Allowed in the Compose file and in any overlay string.
- *Secret-valued*: `secret` inputs and generated secrets. Allowed **only** inside an overlay
  `secrets[].value`. Catalog validation refuses a template that uses one anywhere else.

The Compose file is substituted as text; the overlay is parsed JSON whose string leaves are substituted
(so no value can change the overlay's structure). Plain values are checked before substitution against
their type and then against the **Compose-safe set** `^[A-Za-z0-9._@+:/=-]+$` (1–253 bytes): no space,
quote, `#`, `$`, comma, brackets, braces or newline, so a value can't open a YAML structure, a comment, an
interpolation or another placeholder. The rendered file is then loaded by `compose.Translate` like any file.

**Why**: the kept Compose file, the deployment record and the preview are shown and stored; only the
overlay's secret values go to the secret store and are stripped everywhere else
(`Request.WithoutSecretValues`). Keeping secrets out of the file keeps 015's "no secret in a kept file"
property without new code.

## R3 · Generated secrets

**Decision**: `{"name", "length" 16–128, "alphabet": "hex" | "alnum" | "base64url"}`, made with
`crypto/rand` (rejection sampling, no modulo bias) at install time only. Preview does not generate (it
renders the secret values as empty: the preview's app holds secret names only anyway). The values exist in
the install call's memory and in the request sent to `DeployComposeIfResult`; nothing else.
`templates.Generate` takes an `io.Reader` so a test can feed a sentinel.

## R4 · Install is a Compose deploy of a new app

**Decision**: `Service.InstallTemplate` renders, then calls `DeployComposeIfResult(ctx, id, req,
Condition{IfNew: true}, trigger)`, then records `(template_name, template_version)` on the app row with a
store call of its own. If the deploy call returns an error nothing is recorded (the app may not exist). A
crash between the two loses only the provenance.

**Why**: no new deploy path (constitution II and V: every input normalises into `app.App`, through the same
service). The kept file is the rendered one, so editing, rollback, promotion and export just work.

**Rejected**: storing the template reference in the app document (it would move every revision, and
nothing reads it to deploy); a deploy record field (a template is per app, not per deployment).

## R5 · Errors land at the input the owner typed

**Decision**: input errors are `*app.FieldError` with field `inputs.<name>` (422 `invalid_field`). A
refusal whose field is an overlay domain host that was exactly `{{name}}` in the template
(`services.<svc>.domains[<i>].host`, and the `DomainTakenError` of feature 023) is renamed to
`inputs.<name>`. Any other refusal is a template defect and keeps its path in the file; the console shows
it at the top with the template's name ("The template … can't be installed: …").

## R6 · Catalog distribution: embedded, then fetched like the release check

**Decision**:
- Bundle: one `catalog.json` (`format` 1, `version` `YYYY.MM.DD[.N]`, the templates with their Compose text
  and logo inline) plus `SHA256SUMS` (`<hex>  catalog.json`). Built by `caravana template bundle`.
- Embedded: `internal/templates/catalog/catalog.json` + `SHA256SUMS`, `//go:embed`; a test checks the sum,
  parses it and runs the check on every template.
- Fetched: from `https://github.com/Cosmic-Chimps/caravana-templates/releases/latest/download/` (setting
  `CARAVANA_TEMPLATES_URL` / `-templates-url`, for tests and dev only; https, or http for loopback). Once a day
  with jitter (first check a minute after start), and on Check now (not within 10 minutes of the last).
  Get `SHA256SUMS` (≤ 4 KiB); if the `catalog.json` line equals the stored sum, done; else get
  `catalog.json` (≤ 4 MiB), check its sum, parse and validate it, and store it only if its version is
  newer than the embedded one's. Same `User-Agent` as the release check, no query string, no cookie, no
  other header: nothing about the server leaves it.
- In use: the stored catalog when it is valid, of a known format and newer than the embedded one; else the
  embedded one. Failures are stored as fixed words (never the server's text).
- Off: `template_catalog.enabled = 0` makes no request, ever, and the embedded catalog (or the stored one
  still newer) stays offered. On by default, like the release check.

**Why**: the release check is the pattern the maintainer already accepted (one request a day, no
telemetry, off switch). A checksum file from the same origin doesn't prove authorship, but it catches a
truncated or mixed download, and a template can do nothing a hand-written Compose deploy can't: it still
goes through every validation, and the owner sees the images before Create. Signing is a later step if the
catalog ever accepts outside contributions without review.

**Rejected**: fetching each template file separately (many requests, partial states); git (a runtime
dependency); a registry of third-party catalogs (later, with signatures).

## R7 · Broken and too-new templates

**Decision**: a template may say `"broken": "reason"` (set in the catalog when its lab job fails) and
`"requires": "0.x.y"` (the oldest Caravana that can install it). Hidden from the grid; listed by the API
with `available: false` and the reason; install answers 409 `template_unavailable`. A dev build (no
release version) counts as new enough.

## R8 · Logos

**Decision**: optional SVG, ≤ 32 KiB, refused at catalog read if it contains `<script`, `javascript:`,
an `on…=` attribute, `<foreignObject`, `<iframe`, `<image`, `<use` with an external `href`, `@import` or
`url(http`. Served only from `GET /templates/{name}/logo.svg` (console) and `/v1/templates/{name}/logo`
(API) with `Content-Type: image/svg+xml`, `X-Content-Type-Options: nosniff`,
`Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; sandbox`, and drawn with
`<img>` (an SVG in `<img>` runs no script). No logo: a monogram tile in the template's hue (the console's
stable hash over six, as services have).

## R9 · The first templates

| Template | Services | Inputs | Generated | Backup | Lab job |
| --- | --- | --- | --- | --- | --- |
| `uptime-kuma` | uptime-kuma (`louislam/uptime-kuma:2.5.11`, volume `/app/data`) | domain | — | volume, daily | yes |
| `hollo` | hollo (`ghcr.io/fedify-dev/hollo:0.9.25`, media volume), postgres (`postgres:17`, volume) | domain | `db_password` (alnum 32), `secret_key` (hex 64) | postgres, daily, keep 14 | yes |
| `postgres` | postgres (`postgres:17`, volume), adminer (`adminer:5`, Team only domain) | domain (Adminer's) | `db_password` | postgres, daily | yes |
| `adminer` | adminer, Team only domain | domain | — | — | yes |
| `umbral` | umbral (`ghcr.io/cosmic-chimps/umbral:0.10.0`, volume `/data`, health `/healthz`) | domain | — | volume, daily | yes |
| `codemagic-patch` | server (owner's built image), postgres | image, domain, files domain, buckets, R2 account, OAuth client ID, admin email; secrets: OAuth client secret, S3 keys | `db_password`, `worker_shared_secret`, `oauth_cli_auth_secret` | postgres | no: needs a private image (notes say how to build it) |

- **Hollo and the admin email**: Hollo has no setting for an owner's email (its env reference lists none;
  the first visit to `/setup` creates the account). The template asks for the domain only and its notes say
  to open `https://<domain>/setup` first. Asking for an email it can't use would be a field Caravana drops,
  which constitution IV forbids. SC-002 is changed accordingly.
- **Postgres on its own** can't be reached by another app (services find each other by name only inside
  one app), so the `postgres` template ships Adminer beside it behind Team only; the notes say to add your
  own services to its Compose file (Configure › Compose) to use the database. `adminer` alone is for
  databases reachable by address.
- **Continuwuity** waits for tasks 07 (encoded `/` through Traefik) and 09 (a fixed UDP/TCP port), both still
  pending: not in the first catalog.
- No `depends_on` anywhere (it would be a warning and fail the check); a service that starts before its
  database exits and Nomad restarts it, as the Hollo runbook already says.

## R10 · The catalog repository

**Decision**: `Cosmic-Chimps/caravana-templates`, prepared as a local repository beside this one
(`../caravana-templates` of the main checkout) and **not pushed**: creating the GitHub repository and the
first release is the maintainer's go-ahead. Layout: `catalog.json` (format and version), `templates/<name>/
{template.json, compose.yaml, logo.svg?}`, `README.md` (the format, how to add one, the gate), `LICENSE`
(AGPL-3.0, as Caravana, until the maintainer says otherwise), `.github/workflows/ci.yml`
(`go run github.com/cosmic-chimps/caravana/cmd/caravana@<pinned ref> template check .`),
`.github/workflows/release.yml` (bundle on a tag, upload `catalog.json` and `SHA256SUMS`), and
`lab/install-all.sh` (installs each template with `lab` not `false` into a Caravana through the API with its
example inputs, waits for healthy, deletes it).
