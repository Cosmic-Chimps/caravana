# Contract: the terminal (API 0.19.0 → 0.20.0)

## Endpoint

`GET /v1/apps/{id}/copies/{copyId}/terminal?shell=auto|bash|sh&cols=80&rows=24` with
`Upgrade: websocket`. Operation id `openTerminal`. Answers **101** and then speaks the protocol
below; before the upgrade, errors are the usual JSON errors:

| Status, code | When |
| --- | --- |
| 401 | no session cookie and no Bearer token |
| 403 `cross_origin` | a session cookie with an `Origin` header that isn't the console's own origin (scheme and host as the request arrived, after trusted proxies); a missing `Origin` with a cookie is refused too (browsers always send it on websockets) |
| 403 `token_scope` | a deploy token |
| 404 `not_found` | no such app or copy of it |
| 409 `copy_not_running`, `app_stopped` | as for runCommand |
| 422 `invalid_field` | bad `shell`, `cols` or `rows` (1–1000) |
| 429 `too_many_terminals` | 5 sessions already open |
| 400 | not a websocket upgrade |

Bearer tokens (full API tokens) need no `Origin` check (no browser sends one with a token).

## Protocol (after 101)

- **Browser → Caravana**, text frames, JSON:
  - `{"type":"input","data":"<utf-8 text>"}` — keystrokes and paste;
  - `{"type":"resize","cols":N,"rows":M}`;
  - anything else: closes with 1003.
- **Caravana → browser**:
  - **binary** frames: the shell's output bytes, as they come;
  - text frame `{"type":"exit","code":N}` (with `"message":"no shell found in this image"` when
    the shell exited 126 or 127 before anything was typed) or
    `{"type":"exit","reason":"idle|limit|error","message":"…"}` (error also for "your sign-in
    ended": the credentials are checked again every 30 s)
    as the last message, then a normal close (1000).
- Keepalive: Caravana pings every 30 s; no pong within 60 s ends the session. No frame is sent,
  since the browser is gone: the connection is lost, and the record says "connection lost". The
  exit frame never has `reason` "gone".
- Message limit: 64 KiB per frame from the browser.

## The shell

`shell=auto` runs `/bin/sh -c 'if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi'`
(the `sh -c` itself needs `/bin/sh`); `bash` runs `/bin/bash`, `sh` runs `/bin/sh`. TTY on, the
service's task (never a sidecar). `TERM=xterm-256color` is set only for `shell=auto`, exported
inside its `sh -c` (Nomad's exec can't set the environment); `bash` and `sh` take the runtime's own. When the program is missing Nomad
answers an error or exit 126/127: the exit message says "no shell found in this image".

## Record

`app_actions` gains action `terminal` (migration `0014_terminal.sql` recreates the table with the
wider CHECK and copies the rows), `program` = the shell asked for (`auto`, `bash`, `sh`),
`exit_code` when the shell exited, `error` the end reason in fixed words ("idle for 15 minutes",
"open for 60 minutes", "connection lost", "closed"). Never input or output.
