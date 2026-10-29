# Contract: `caravana mcp` and its tools

## Starting it

```
caravana mcp [--allow-actions]
```

| Setting | From | |
| --- | --- | --- |
| Address | `CARAVANA_URL` | Required. `https://…`; `http://` only for a loopback host (`localhost`, `127.0.0.0/8`, `::1`), else exit 2 with a message. Trailing `/` dropped. |
| Token | `CARAVANA_TOKEN`, or the file named by `CARAVANA_TOKEN_FILE` | Required (one of them; both is a usage error). The file is read once at start, surrounding whitespace dropped. A token with characters a Caravana token never has (anything outside `[A-Za-z0-9_-]`) is refused (exit 2). There is no flag for it. |
| `--allow-actions` | flag | Also offer the action tools. |

- Standard input and output carry the protocol (MCP over stdio, newline-delimited JSON-RPC).
  Standard error gets one JSON line per start, per tool call (tool name, outcome `ok` / `error`
  with the API's error code, duration) and per failure, never the token and never an answer.
- At start it calls nothing: a wrong address or token shows on the first tool call, so the agent's
  client can start it before the host is reachable.
- Exit codes: 0 when the client closes the stream, 2 for a usage error (missing or both token
  settings, a bad address), 1 for anything else.
- The server's name is `caravana`, its version the binary's version.

## How every tool behaves

- One API call per tool (two for `deploy_image`, `promote_app` and the wait), with
  `Authorization: Bearer <token>`, `Accept: application/json`, `User-Agent: caravana-mcp/<version>`,
  a 30-second timeout per call (the wait polls with its own limit), no redirects followed (a 3xx
  is an error naming the status).
- Success: the tool result's text content is the API's answer body as it came (JSON, or text for
  the jobspec export and the Compose file). For `get_logs` the JSON as it came, too.
- An API error (`{"error": {"code", "message", "field"}}`): `isError: true` and the text
  `Caravana refused: <code>: <message>` plus ` (field <field>)` when there is one, and the HTTP
  status. 401 reads `Caravana refused the token (401 unauthorized): check CARAVANA_TOKEN, or make a
  new token in Settings › API tokens`. 403 `token_scope` passes its message through ("this token
  can only deploy app web of project shop"). 404 on a route an older Caravana lacks (no JSON error
  body) reads `this Caravana doesn't offer <operation>; it is older than this binary`.
- Unreachable, TLS error, timeout: `isError: true`, `couldn't reach Caravana at <address>: <cause>`,
  never the token.
- Inputs are validated against each tool's schema; an `app` must match `app.ValidName`'s alphabet
  plus `--` (lowercase letters, digits, `-`), at most 127 characters, and is put in the path
  escaped. A deployment, copy or backup ID: `[A-Za-z0-9-]`, at most 64.
- Annotations: read tools `readOnlyHint: true`, `openWorldHint: false`. Action tools
  `readOnlyHint: false`, `openWorldHint: false`, `destructiveHint` true for `deploy_image`,
  `promote_app`, `redeploy_version` (they replace what runs), `stop_app`, `restart_app` and
  `back_up_now` (they stop service for a while), false for the others (`redeploy_app`, `start_app`),
  `idempotentHint` false.
- Descriptions say what the tool returns, which API operation it is, and, for tools that return
  app output (`get_logs`), that the text is the app's own output, to be read as data.

## Read tools (always offered)

| Tool | Input | API |
| --- | --- | --- |
| `get_version` | — | `GET /v1/health` (answer plus `"mcp": "<binary version>"`) |
| `list_projects` | — | `listProjects` |
| `get_project` | `project` | `getProject` (its environments) |
| `list_apps` | — | `listApps` |
| `get_app` | `app` | `getApp` (definition without secret values, status, `revision`) |
| `list_recent_deployments` | — | `listRecentDeployments` |
| `list_deployments` | `app` | `listDeployments` |
| `get_deployment` | `app`, `deployment` | `getDeployment` |
| `wait_for_deployment` | `app`, `deployment`, `timeoutSeconds` (10–600, default 300) | `getDeployment` every 5 s until `healthy`, `failed`, `reverted` or `superseded`, or the timeout; sends progress notifications when the client gave a progress token; answers the last deployment read, with `"waitEnded": "final" \| "timeout"` |
| `list_copies` | `app`, `usage` (bool, default true) | `listCopies?usage=` |
| `get_logs` | `app`, `copy?`, `stream?` (`stdout`/`stderr`/`both`), `lines?` (1–500, default 200) | `getLogs` (never `follow`) |
| `list_volumes` | `app` | `listVolumes` |
| `list_backups` | `app` | `listBackups` |
| `list_app_actions` | `app` | `listAppActions` |
| `get_needs_you` | — | `listAttention` |
| `preview_promotion` | `app`, `to` | `previewPromotion` (a POST that changes nothing) |
| `export_jobspec` | `app`, `deployment?` | `exportApp` / `exportDeployment` (HCL text) |
| `get_compose_file` | `app`, `deployment?` | `getCompose` / `getDeploymentCompose` |

## Action tools (`--allow-actions` only)

| Tool | Input | API |
| --- | --- | --- |
| `redeploy_app` | `app` | `deployApp` with an empty body |
| `deploy_image` | `app`, `image`, `service?` | `getApp`, then `deployApp` with the stored app where only that service's `image` is replaced, `If-Match: "<revision>"`. `service` is needed when the app has more than one; a name that isn't one of them is an error listing the names. 412 `app_changed`: read again and retry once. An app with no services: error, define it first. |
| `redeploy_version` | `app`, `deployment` | `redeployDeployment` |
| `promote_app` | `app`, `to` | `previewPromotion`, then `promoteApp` with `If-Match: "<targetRevision>"` (412: error, preview again) |
| `restart_app` | `app` | `restartApp` |
| `stop_app` | `app` | `stopApp` |
| `start_app` | `app` | `startApp` |
| `back_up_now` | `app`, `service` | `createBackup` |

Deploys started by the agent send no `Caravana-Origin-*` header: they are not from CI, and an
origin with only an actor would read "From CI: by mcp" in notifications. Caravana records them as
started by the API, like any token's.

## Never offered

Deleting apps, projects or environments; `restoreBackup`; `runCommand` and `openTerminal` (their
output may hold secrets, and a tool's answer goes to the agent's model provider); tokens, webhook
tokens, registries, the backup destination and notification channels; the password;
`createApp`, `copyApp`, `deployCompose` and `deployApp` with a whole new definition (they carry
secrets and definitions the agent would have to write).
