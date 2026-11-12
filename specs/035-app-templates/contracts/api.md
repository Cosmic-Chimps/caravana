# Contract: API (0.36.0)

All routes need an API token or a signed-in session; deploy tokens get 403 `token_scope` by the scope
middleware's default deny (add the routes to the scope walk test's table as denied). Every answer that
comes from an install or a preview is `Cache-Control: no-store`.

## `GET /v1/templates` · `listTemplates`

```json
{
  "catalog": {
    "version": "2026.11.12", "source": "embedded" | "fetched",
    "embeddedVersion": "2026.11.12",
    "checking": true, "checkedAt": "…Z" | null, "checkedOkAt": "…Z" | null, "fetchedAt": "…Z" | null,
    "error": "" 
  },
  "templates": [
    {"name": "uptime-kuma", "title": "Uptime Kuma", "version": "2.5.11-1", "description": "…",
     "tags": ["monitoring", "status page"], "upstream": "https://…", "hasLogo": true,
     "available": true, "unavailable": ""}
  ]
}
```

`unavailable` is the reason when `available` is false: the catalog's `broken` text, or
"needs caravana 0.21.0 or newer". Query `q` (case-insensitive substring of title, description, tags) and
`tag` (exact) filter; both optional.

## `GET /v1/templates/{name}` · `getTemplate`

The manifest as in the catalog (inputs without any value but `default` and `example`; generated secrets as
`{name, length, alphabet}`; `services` overlay with its placeholders; `notes` unrendered), plus `compose`
(the text with placeholders), `available`, `unavailable`. 404 `template_not_found`.

## `GET /v1/templates/{name}/logo` · `getTemplateLogo`

`image/svg+xml` with the headers of research R8, 404 `template_not_found` when it has none.

## `POST /v1/templates/{name}/preview` · `previewTemplate`

Body `{"id": "<app id>", "inputs": {"domain": "…"}}`. Answer 200 as `previewCompose`: `{app, warnings}`
(secret names only), plus `"compose"`: the rendered file. Generates nothing. Errors as install.

## `POST /v1/templates/{name}/install` · `installTemplate`

Body `{"id": "<app id>", "inputs": {…}}` (`id` as `deployApp`'s path: bare name in Default › production,
else `<project>--<environment>--<name>`; the place must exist, as for any first deploy). Optional
`Caravana-Origin-*` headers as `deployCompose`.

- 202 with the same body, `Location` and headers as `deployCompose` (deployment ID, version when known,
  warnings, `teamPasswords` for Team only domains: shown once, as there).
- 422 `invalid_field` with `field` `inputs.<name>` for an input; `id` for a bad ID; a path in the file
  for a template defect (R5).
- 404 `template_not_found`, `project_not_found`, `environment_not_found`.
- 409 `app_exists` (the ID is taken), `template_unavailable` (broken or needs a newer Caravana),
  `domain_taken` (field `inputs.<name>` when the host came from that input).

## `POST /v1/templates/check` · `checkTemplates`

202 when a check started; 409 `check_disabled` when checking is off; 429 `check_too_soon` (with
`Retry-After`) within 10 minutes of the last one — the same codes the release check's `checkUpdates` uses
(reuse them if they exist).

## `PUT /v1/templates/settings` · `putTemplateSettings`

Body `{"checking": true|false}` → 200 with the `catalog` object above.

## Changes to existing schemas

- `AppRecord.template`: `{"name": "hollo", "version": "0.9.25-1"}`, absent when the app wasn't installed
  from a template. `AppSummary.template` the same.
- New codes: `template_not_found`, `template_unavailable`.

## MCP (`caravana mcp`)

Read tools `list_templates` (optional `q`, `tag`) and `get_template` (`name`): the API's answers as they come.
No install tool (it would carry secret inputs through an agent's transcript).
