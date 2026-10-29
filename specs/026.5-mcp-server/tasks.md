# Tasks: An MCP server for Caravana (026.5)

- [X] T001 `internal/mcpserver`: config from env (contract "Starting it"), the API client (Bearer,
  no redirects, 30 s, errors without the token), API error mapping, the server with the read tools
  of contracts/tools.md (including `wait_for_deployment` with progress), annotations and
  descriptions; tests against an `httptest` fake with in-memory MCP transports (plan.md "Tests":
  each read tool, errors, config, sentinel, imports, contract against `api/openapi.yaml`). Add
  `github.com/modelcontextprotocol/go-sdk` v1.8.0.
- [X] T002 [US2] Action tools behind `--allow-actions` (`deploy_image` with `If-Match` and the
  412 retry, `promote_app` via the preview's revision, the rest one call each); tests (absent
  without the flag, by list and by name; each call's request; `deploy_image` edge cases).
- [X] T003 `cmd/caravana`: `caravana mcp` (`mcp.go`, dispatch, usage); test for the usage error.
- [X] T004 Release and docs: darwin tarballs in `tools/release.sh build` and `SHA256SUMS`;
  `docs/mcp.md` (setup for Claude Code and others, tokens, `--allow-actions`, never offered);
  link it from `README.md`.
- [X] T005 Lab: `TestConsoleLab` step `console-mcp` (plan.md "Lab").
- [X] T006 Docs: CHANGELOG Unreleased (Added), CLAUDE.md 026.5 paragraph, docs/plan.md 026.5
  status.
- [X] T007 Full check (CI's list) and `TestConsoleLab`.
  - 2026-10-20: all of CI's checks pass, including the whole suite under `-race`, shellcheck, the
    UI checks and the darwin cross-compile. `TestConsoleLab` passed (509 s, 21 steps;
    `console-mcp` 27.5 s).
  - The built binary was driven over real stdio with raw JSON-RPC against a throwaway Caravana on
    the lab:
    - handshake: server `caravana`, protocol 2025-06-18; 18 read tools listed;
    - reads answer the API's JSON, and a missing app is Caravana's 404 refusal;
    - `../etc` is rejected by the schema, and `deploy_image` is unknown without the flag;
    - stderr has one line per call, and the token appears in no output;
    - a remote `http://` address and both token settings exit 2 with the usage.
- [ ] T007a A real agent: `claude mcp add` against the lab Caravana, ask for an app's status. Left
  for the maintainer: it changes the maintainer's agent configuration (docs/mcp.md has the command).
  (Split from T007 on 2026-10-29. The Frunchy gate on 2026-10-21 read its app through `caravana mcp`,
  see `docs/plan.md`, but that is not this walk.)
