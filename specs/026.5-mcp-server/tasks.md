# Tasks: An MCP server for Caravana (026.5)

- [ ] T001 `internal/mcpserver`: config from env (contract "Starting it"), the API client (Bearer,
  no redirects, 30 s, errors without the token), API error mapping, the server with the read tools
  of contracts/tools.md (including `wait_for_deployment` with progress), annotations and
  descriptions; tests against an `httptest` fake with in-memory MCP transports (plan.md "Tests":
  each read tool, errors, config, sentinel, imports, contract against `api/openapi.yaml`). Add
  `github.com/modelcontextprotocol/go-sdk` v1.8.0.
- [ ] T002 [US2] Action tools behind `--allow-actions` (`deploy_image` with `If-Match` and the
  412 retry, `promote_app` via the preview's revision, the rest one call each); tests (absent
  without the flag, by list and by name; each call's request; `deploy_image` edge cases).
- [ ] T003 `cmd/caravana`: `caravana mcp` (`mcp.go`, dispatch, usage); test for the usage error.
- [ ] T004 Release and docs: darwin tarballs in `tools/release.sh build` and `SHA256SUMS`;
  `docs/mcp.md` (setup for Claude Code and others, tokens, `--allow-actions`, never offered);
  link it from `README.md`.
- [ ] T005 Lab: `TestConsoleLab` step `console-mcp` (plan.md "Lab").
- [ ] T006 Docs: CHANGELOG Unreleased (Added), CLAUDE.md 026.5 paragraph, docs/plan.md 026.5
  status.
- [ ] T007 Full check (CI's list), `TestConsoleLab`, and a real agent: `claude mcp add` against the
  lab Caravana, ask for an app's status (main session).
