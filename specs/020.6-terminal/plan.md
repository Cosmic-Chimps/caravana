# Implementation Plan: Terminal into a copy

**Branch**: `020.6-terminal` | **Date**: 2026-10-19 | **Spec**: [terminal.spec.md](./terminal.spec.md)
| **Contract**: [contracts/terminal.md](./contracts/terminal.md)

## Summary

A websocket endpoint bridges the browser and Nomad's exec (TTY) for one copy; a console page shows
it with xterm.js, vendored like htmx. Sessions are limited, recorded without content, and refused
cross-site. API 0.20.0.

## Technical Context and findings (2026-10-19)

- **Nomad**: `api.Allocations().Exec(ctx, alloc, task, tty=true, command, stdin io.Reader,
  stdout, stderr io.Writer, sizeCh <-chan api.TerminalSize, q)` (the call Run a command uses with
  tty=false). `alloc-exec` is already in the scoped policy (020.5). With a TTY, stderr is merged
  into stdout by the runtime.
- **Websocket**: `github.com/gorilla/websocket` v1.5.3, already a direct dependency since 020.5
  (deploytest); now used in production code. No new module.
- **Hijacking**: gorilla's Upgrader asserts `w.(http.Hijacker)`; `httpx`'s request-log
  `statusRecorder` doesn't implement it, so every upgrade would 500. Add `Hijack()` to
  `statusRecorder` (through `http.NewResponseController(r.ResponseWriter).Hijack()`, recording
  101), with a test. The webhook-only listener never serves this route.
- **Timeouts**: `http.Server` sets read/write deadlines (30 s) on the connection before the
  handler; after the upgrade, clear them (`NetConn().SetDeadline(time.Time{})`) and rely on the
  session's own idle, limit and ping timers.
- **Browser widget**: `@xterm/xterm` 6.0.0 (`lib/xterm.js`, `css/xterm.css`) and
  `@xterm/addon-fit` 0.11.0 (`lib/addon-fit.js`), MIT, vendored with `tools/ui-vendor.sh` (lines
  in `tools/ui-vendor.versions` with their SHA-256; `xterm.css` goes to `static/css/` — extend the
  script for a css destination or vendor it under `static/js/` and link it from there) and listed in
  `static/js/LICENSES.md`. The page's own script `static/js/terminal.js` (no framework).
- **CSP**: the console's is `default-src 'self'; …`. xterm.js creates `<style>` elements and sets
  `style` attributes, which `style-src 'self'` blocks. The terminal page alone is served with
  `style-src 'self' 'unsafe-inline'` added (styles only; scripts stay `'self'`); `connect-src`
  falls back to `default-src 'self'`, which allows a same-origin `ws:`/`wss:` in current browsers —
  set `connect-src 'self'` explicitly anyway. Check in the lab browser that the page shows no CSP
  violation in the console.
- **Origin check**: with a session cookie, the `Origin` header must equal the console's origin
  (`httpx` already knows the scheme behind trusted proxies, `s.proxies.HTTPS(r)`, and the host);
  otherwise 403 `cross_origin`. `http.CrossOriginProtection` doesn't cover GET, so this is
  explicit. Deploy tokens: the scope middleware's default deny already answers 403.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small | PASS. No new module or service; ~600 KB of vendored JS/CSS embedded. |
| V. API-First | PASS. `openTerminal` in openapi 0.20.0 (a GET with 101 and the protocol described); the console page is a client of it. |
| VII. Secure | PASS with care: root-equivalent access, so session or full token only, explicit Origin check, deploy tokens refused, limits, nothing stored (sentinel tests). |
| Technology (UI: html/template + HTMX, no SPA, no build step) | Deviation, see Complexity Tracking: one page uses a vendored widget and its own small script. |

## Design

- `internal/deploy/terminal.go`: `OpenTerminal(ctx, copyID, task string, command []string,
  stdin io.Reader, out io.Writer, sizes <-chan TerminalSize) (exitCode int, err error)` over
  `Allocations().Exec` with tty=true; `ErrCopyNotRunning` as for Exec. deploytest's fake exec gains
  TTY and resize support (it serves the exec websocket already).
- `internal/service/terminal.go`: `OpenTerminal(ctx, appID, copyID, shell string, trigger)
  (*TerminalSession, error)` checks app/copy/stopped/Stopping (as RunCommand), the 5-session
  limit, records the action `running`; the session runs the exec with a pipe for stdin, a writer for
  output, the size channel, idle (15 min) and limit (60 min) timers, and on end finishes the record.
  `FailRunningActions` at start already settles interrupted ones.
- `internal/api/terminal.go`: auth (cookie+Origin or Bearer), scope (default deny), query
  validation, upgrade, clear deadlines, pump frames per the contract, pings.
- `internal/store`: migration `0014_terminal.sql` (recreate `app_actions` with `terminal` in the
  CHECK; copy rows; keep the index).
- Console: Running tab, per running copy, a "Terminal" link to `GET /apps/{id}/copies/{copyId}/terminal`
  (a full page: header with the app and copy, shell picker, the terminal, a status line, Close;
  `<noscript>` text pointing to Run a command); Recent actions show terminal sessions.
- Lab: gate step `terminal` (Go websocket client with the API token: busybox copy, `echo
  $((6*7))` → 42, resize then `stty size` → the new size, `exit 3` → exit code 3; a cookie
  request with a foreign Origin → 403; a deploy token → 403; the action recorded without the
  typed sentinel). Console lab step: the page loads its scripts (status 200 for each asset) and
  has the CSP with the terminal's additions.

## Complexity Tracking

| Deviation | Why | Simpler option rejected because |
| --- | --- | --- |
| A JavaScript widget (xterm.js) and `style-src 'unsafe-inline'` on one page | A terminal needs to render a screen and read keystrokes; no HTML form can. | Run a command (no JS) exists and stays; it can't do interactive programs, which is what was asked for. |
