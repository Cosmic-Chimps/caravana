# Implementation Plan: An MCP server for Caravana

**Branch**: `026.5-mcp-server` | **Date**: 2026-10-20 | **Spec**: [mcp.spec.md](./mcp.spec.md)
| **Contract**: [contracts/tools.md](./contracts/tools.md)

## Summary

`caravana mcp` is a Model Context Protocol server over stdio, run where the agent is. It turns
each tool call into one call of Caravana's public API with the owner's token (a full token, or a
deploy token for one app) and returns the API's answer. Read tools by default, action tools with
`--allow-actions`. No API change (openapi stays 0.21.0), nothing new on the host; releases gain
macOS binaries.

## Research (2026-10-20)

- **R1 Where it runs: a stdio subcommand, not an `/mcp` endpoint.** Agents (Claude Code,
  CodeAlta, Codex) start local MCP servers as a command (`claude mcp add caravana -- caravana
  mcp`). A stdio server runs on the owner's machine with the owner's token, adds nothing to the
  host's attack surface and goes through the API like any client (constitution V). An `/mcp`
  endpoint on the API would serve remote agents (claude.ai connectors) but needs MCP's
  authorization flow (OAuth) and a second listener's worth of review: out of scope, noted in the
  spec.
- **R2 The Go MCP module: `github.com/modelcontextprotocol/go-sdk` v1.8.0** (new direct
  dependency, to justify in the commit). It is the official SDK, maintained by the MCP project
  with Google, stable since v1.0, Go 1.25+. It handles the protocol's version negotiation, the
  JSON-RPC framing, input schemas from Go structs (`github.com/google/jsonschema-go`), tool
  annotations, progress notifications, and has in-memory and IO transports for tests. Its other
  requirements (golang-jwt, x/oauth2, segmentio/encoding, uritemplate) serve its HTTP and auth
  features; only the packages we import are linked. Rejected: `mark3labs/mcp-go` (popular, but
  community-maintained and pre-1.0 in its API promises); writing the protocol ourselves (about
  400 lines of JSON-RPC, but the protocol has had three revisions in 18 months and clients check
  versions and capabilities; getting it subtly wrong breaks agents silently).
- **R3 Binaries for the agent's machine.** Releases ship Linux tarballs for the host only. The
  binary cross-compiles for darwin with `CGO_ENABLED=0` (checked: `GOOS=darwin GOARCH=arm64 go
  build ./cmd/caravana`). `tools/release.sh build` adds `caravana_<v>_darwin_arm64.tar.gz` and
  `caravana_<v>_darwin_amd64.tar.gz` (the binary only, no host files) to the release and to
  `SHA256SUMS`. Linux users use the existing tarballs' binary. No signing: a binary fetched with
  curl gets no quarantine flag; the docs say how to clear it for a browser download.
- **R4 What a deploy token reaches.** `internal/api/scope.go` allows a deploy token: getApp,
  getCompose, listDeployments, getDeployment, deployApp, deployCompose, previewPromotion,
  promoteApp, createApp and the two place routes, for its one app. So with a deploy token the
  agent can read the app, its deployments and Compose file, deploy and promote it; copies, logs,
  backups, the needs-you list and listing apps are 403 `token_scope`. That is the API's decision
  (default deny), passed through unchanged; widening it is not part of this feature.
- **R5 Answers are the API's JSON, unmodified.** Each tool returns the body as text content. No
  output schema per tool: the API's schemas are in `api/openapi.yaml` and change with it; a copy
  in the MCP server would drift. Clients pass text content to the model; JSON reads fine.
- **R6 Secrets.** The API never returns a secret value, a token hash or a webhook token after
  creation; the tools that could carry secrets (Run a command, terminal, token creation, the
  settings with secrets, whole-definition deploys) are not offered. The token: read from env or a
  file (never a flag), checked for its alphabet, sent only in `Authorization`, redirects refused,
  plain HTTP refused except loopback, never logged (a sentinel test reads stderr and every answer).
- **R7 Prompt injection.** Logs, app names and deploy messages are data an app (or whoever pushes
  to it) controls, and they reach the model. The server can't sanitize meaning; it labels
  `get_logs` as the app's own output in its description, keeps the default read-only, and marks
  action tools so clients ask before running them.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small | PASS with a justified module (R2). One binary still; `caravana mcp` is a subcommand. Nothing runs on the host. |
| II. One model | Not touched (no input path; the API's model is used as it is). |
| III. Safe deploys | PASS. Deploys go through the API's normal path (digests, health checks, auto-revert); `deploy_image` sends `If-Match`. |
| IV. Fail loudly | PASS. Every API refusal reaches the agent with its code, message and field; unreachable and refused tokens are said plainly. |
| V. API-First | PASS. The MCP server is an API client only: no `internal/service`, `internal/store` or Nomad import (a test checks the package's imports). No endpoint added, openapi unchanged; a test checks every tool's operation exists in `api/openapi.yaml`. |
| VI. Nomad-native | Not touched. |
| VII. Secure | PASS. Read-only default, opt-in actions, no secret-bearing tools, token never in args, logs or answers (sentinel test), loopback-only plain HTTP, no redirects. |
| VIII. Plain Go | PASS. Table-driven tests with in-memory MCP transports against an `httptest` API fake. |

## Design

- `internal/mcpserver` (new): `Config{URL, Token, AllowActions, Version}`, `ConfigFromEnv(getenv,
  readFile, args) (Config, error)` (the usage errors of the contract), `New(cfg, *http.Client,
  *slog.Logger) *mcp.Server` registering the tools, `Run(ctx, cfg, in io.Reader, out io.Writer,
  log *slog.Logger) error`. Inside: a small `apiClient` (`do(ctx, method, path, query, body,
  headers) (status int, body []byte, header http.Header, err error)`; error text never holds the
  token), `apiError` parsing, one function per tool, the tool table (name, description,
  annotations, input struct, action or read). Imports only the SDK, `net/http`, `encoding/json`,
  `log/slog` and `internal/app` for `ValidName`-style checks (or its own pattern; no service or
  store import).
- `cmd/caravana/main.go`: `mcp` dispatch beside `admin` and `version` (`runMCP(ctx, args, getenv,
  stdin, stdout, stderr)` in `cmd/caravana/mcp.go`), usage text in the top-level usage.
- `tools/release.sh`: darwin tarballs (R3); `deploy/install/README.md` or a new `docs/mcp.md`:
  setup for Claude Code (`claude mcp add caravana -e CARAVANA_URL=… -e CARAVANA_TOKEN_FILE=… --
  caravana mcp`), CodeAlta and others, the token choice (deploy token for one app, full token for
  the installation), `--allow-actions`, what is never offered.
- Tests:
  - `internal/mcpserver`: each tool against an `httptest` fake asserting method, path, query,
    headers (Bearer, no origin headers, `If-Match` quoted), body; error mapping (API JSON error,
    401, 403 `token_scope`, 404 without JSON, 3xx, unreachable); `deploy_image` (one service
    replaced, the rest byte-equal after a JSON round trip, multi-service needs `service`, 412
    retry once); `wait_for_deployment` (final, timeout, progress); action tools absent without
    `--allow-actions` (list and call by name); annotations; config errors (both token settings,
    bad alphabet, http to a remote host, http loopback ok); a sentinel token found in no answer,
    error or log line; imports test (no `internal/service`, `internal/store`, `internal/deploy`,
    `nomad`); contract test (every tool's method and path is in `api/openapi.yaml`, with
    kin-openapi as `internal/api/contract_test.go` loads it).
  - `cmd/caravana`: `run(["mcp"])` with a missing URL exits 2 with the message.
  - Lab (`TestConsoleLab` step `console-mcp`, `cmd/caravana/console_mcp_test.go`): `runMCP`
    in-process over pipes against the lab's Caravana with a full token: tools/list (read only),
    `get_version`, `list_apps`, `get_app`, `list_copies`, `get_logs`, `get_needs_you`; with
    `--allow-actions` on a lab busybox app: `deploy_image` (another tag of the same image) then
    `wait_for_deployment` healthy, `restart_app`; with a deploy token for that app: `get_app`
    works, `list_apps` and `get_logs` give `token_scope`; the token in no stderr line.

## Complexity Tracking

| Deviation | Why | Simpler option rejected because |
| --- | --- | --- |
| A new direct module (`modelcontextprotocol/go-sdk`) | The MCP protocol, its versions and schemas (R2) | Writing it ourselves risks silent incompatibility with agents; the SDK is the protocol owners' own. |
