# Implementation Plan: App templates

**Spec**: [app-templates.spec.md](./app-templates.spec.md) · **Branch**: `alta/fresh-kestrel-ng5c` · **Date**: 2026-11-12

> **Scale note**: one new pure package (`internal/templates`), one migration, about 25 files across
> templates, store, service, api, ui, mcpserver and cmd/caravana, six templates, and a second repository
> prepared locally. The one subtle piece is keeping generated secrets in exactly one place: the request
> handed to `DeployComposeIfResult`.

## Summary

A template is a manifest plus a Compose file with `{{placeholders}}`. `internal/templates` parses and
validates a catalog bundle, checks inputs, generates secrets from a given reader and renders a
`compose.Request`. `Service.InstallTemplate` renders and calls the existing
`DeployComposeIfResult` for a new app, then records `(template_name, template_version)` on the app row.
The catalog is embedded (`//go:embed`) and replaced by a newer verified bundle fetched once a day from the
catalog repository's latest release, the way the release check works. The API, the console's fourth New
app tab and two MCP read tools sit on top. The catalog repository holds the sources, its CI runs
`caravana template check`, its release runs `caravana template bundle`.

## Data model

Migration `0028_templates.sql` (0026 and 0027 are taken by the unmerged 033 passkeys and 034 certificates
branches; the runner applies any missing version, so a gap is fine):

```sql
ALTER TABLE apps ADD COLUMN template_name TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN template_version TEXT NOT NULL DEFAULT '';

CREATE TABLE template_catalog (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  checking      INTEGER NOT NULL DEFAULT 1,
  bundle        BLOB,                       -- the fetched catalog.json, verified; NULL until one is kept
  bundle_sum    TEXT NOT NULL DEFAULT '',   -- its SHA-256 (hex)
  version       TEXT NOT NULL DEFAULT '',
  fetched_at    TEXT,
  checked_at    TEXT,
  checked_ok_at TEXT,
  error         TEXT NOT NULL DEFAULT ''
) STRICT;
INSERT INTO template_catalog (id) VALUES (1);
```

(Follow the store's own conventions for time columns and STRICT if they differ.) The app's `revision` is
untouched (the columns are beside the document, as `project`/`environment` are). `store.AppRecord` gains
`Template store.AppTemplate{Name, Version}`; `store.SetAppTemplate(ctx, id, name, version)`;
`store.TemplateCatalog(ctx)`, `SaveTemplateCheck(ctx, row)`, `SetTemplateChecking(ctx, bool)`.

## Project structure

```
internal/templates/            new, pure: catalog.go (types, ParseCatalog, validation), render.go
                               (CheckInputs, Generate, Render, RenderNotes, DomainInputs), source.go
                               (ReadSource, Sums), check.go (Check), embed.go (Embedded),
                               catalog/catalog.json + SHA256SUMS (embedded), *_test.go
internal/store/migrations/0028_templates.sql, internal/store/templates.go, apps.go (columns)
internal/service/templates.go  catalog in use, ListTemplates, Template, PreviewTemplate, InstallTemplate,
                               the daily check (runTemplateCheck, CheckTemplates, SetTemplateChecking),
                               SetTemplatesURL; started in Service.Start beside the release check
internal/api/templates.go, router, scope test table, api/openapi.yaml 0.36.0
internal/ui/templates.go, templates/newapp*.html (fourth tab), templatepage.html, Configure fact row,
                               app.css rebuilt (tools/ui-css.sh build)
internal/mcpserver: list_templates, get_template (+ contract test against openapi)
internal/config: CARAVANA_TEMPLATES_URL / -templates-url (https, or http for loopback only; empty = default)
cmd/caravana/template.go       `caravana template check <dir>` and `bundle <dir> <outdir>`
cmd/caravana/console_templates_test.go   TestConsoleLab step console-templates
cmd/caravana/gate_templates_test.go      TestLab gate step templates
changelog.d/035-app-templates.md, CLAUDE.md "Where we are" paragraph, docs/plan.md line
../caravana-templates/         the catalog repository (local, not pushed)
```

**Structure decision**: `internal/templates` imports `compose`, `nomadjob`, `registry` and `app`, all pure;
`service` imports `templates`. Nothing in `templates` talks to the network; the fetch lives in the service
next to the release check and reuses its `get` helper and HTTP client.

## Constitution check

- **I Small by default**: no new runtime dependency and no new Go module (stdlib `crypto/rand`, `regexp`,
  `encoding/json`, `embed`). The embedded catalog is tens of KiB.
- **II One model**: every template becomes a `compose.Request`, so an `app.App` through `compose.Translate`;
  nothing after normalisation reads the template. `nomadjob.Translate` untouched.
- **III Safe deploys**: the install is a normal deploy: digest pinning, health checks, auto-revert. Each
  template pins its image versions; Caravana pins the digest.
- **IV Fail loudly**: inputs refused with `inputs.<name>`; template defects refused with their path; a
  template that would warn is refused by the catalog's gate, and at install the warnings are shown as for
  any Compose deploy. An input a template can't use is refused at catalog read (every input must be used).
- **V API-first**: the console calls only `internal/service`; `api/openapi.yaml` 0.36.0 changes in the same
  commit as the routes; JSON errors.
- **VI Nomad-native**: untouched; templates know nothing of Nomad.
- **VII Secure by default**: generated and typed secrets go only into the overlay's secret values, which go
  to Nomad Variables; never stored, logged or shown (sentinel tests). The catalog check sends nothing about
  the server and can be turned off. Logos never run script.
- **VIII Plain, tested Go**: table tests for parsing, inputs, rendering, generation; golden-free; the
  embedded catalog checked by a test.
- **Originality**: every template is written here from the upstream project's own documentation; nothing
  from other PaaS catalogs.

No violations; no Complexity Tracking entries.
