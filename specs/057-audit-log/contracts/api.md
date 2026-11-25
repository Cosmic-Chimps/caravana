# API contract: audit log (0.61.0)

All four operations are for owners and admins (`putAuditSettings`: owners); members get 403 `not_allowed`,
deploy tokens 403 `token_scope`. Every answer is `Cache-Control: no-store`.

## `GET /v1/audit` — listAuditEvents

Query (all optional): `actor` (an email or a token name, exact), `action` (one of the actions), `kind` (a
resource kind), `resource` (a resource ID, exact), `project`, `environment` (needs `project`), `from`, `to`
(RFC 3339), `before` (an entry ID: the cursor), `limit` (1–500, default 100). A bad value is 422
`invalid_field` naming the parameter.

```json
{
  "events": [
    {
      "id": 812,
      "at": "2026-11-25T10:15:02.123456789Z",
      "actor": {"kind": "token", "email": "ana@example.com", "token": "ci", "personId": "01J…"},
      "door": "api",
      "address": "203.0.113.4",
      "action": "deployed",
      "operation": "POST /v1/apps/{id}/deployments",
      "resource": {"kind": "app", "id": "shop--staging--web", "name": "web", "project": "shop", "environment": "staging"},
      "changes": [
        {"path": "services[web].image", "op": "changed", "from": "ghcr.io/acme/web:1.4", "to": "ghcr.io/acme/web:1.5"},
        {"path": "services[web].secrets[DB_PASSWORD].value", "op": "changed"}
      ],
      "record": {"kind": "deployment", "id": "01J…"},
      "related": [{"kind": "domain", "id": "new.example.com", "action": "created"}],
      "fallback": false
    }
  ],
  "next": "811"
}
```

`next` is the `before` of the next page, absent on the last. `from`/`to` are absent for a redacted change.

## `GET /v1/audit/export` — exportAuditEvents

The same filters but `before` and `limit`; `from` and `to` are required. `text/csv; charset=utf-8` with
`Content-Disposition: attachment; filename="caravana-audit-<from>-<to>.csv"`, oldest first, columns `id, at,
actor_kind, actor, token, door, address, action, operation, resource_kind, resource_id, resource_name,
project, environment, changes, record_kind, record_id`; `changes` is one cell, `path op from → to` joined by
`; `. A cell that starts with `=`, `+`, `-`, `@`, tab or CR gets a leading `'`. At most 100 000 rows; more is 422
`invalid_field` on `from` ("narrow the range").

## `GET /v1/audit/settings` — getAuditSettings / `PUT /v1/audit/settings` — putAuditSettings

```json
{"retentionDays": 90}
```

`retentionDays` 30–3650, else 422 `invalid_field` on `retentionDays`. The change is itself an entry.
