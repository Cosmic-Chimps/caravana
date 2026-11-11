# Contract: registries API (0.35.0)

Every operation keeps its route. The optional query parameter `path` names an entry under the host. Without it, the call means the host-only entry, as before.

| Operation | Route | Change |
|---|---|---|
| `listRegistries` | `GET /v1/registries` | each item has `path` when it has one; ordered by host, then path |
| `putRegistry` | `PUT /v1/registries/{host}?path=` | saves the entry `host/path` |
| `deleteRegistry` | `DELETE /v1/registries/{host}?path=` | removes that entry only |
| `testRegistry` | `POST /v1/registries/{host}/test?path=` | tests that entry's login |

`Registry` schema: `host`, `path` (omitted when empty), `username`, `createdAt`, `updatedAt`, `lastTest`. Still no password property.

A path that isn't an image path: 422 `invalid_field`, field `path`. An unknown entry on delete or test: 404.

Example:

```
PUT /v1/registries/ghcr.io?path=cosmic-chimps
{"username": "jjchiw", "password": "<token>"}

200 {"host": "ghcr.io", "path": "cosmic-chimps", "username": "jjchiw", ...}
```

Deploy tokens stay denied by the default deny.
