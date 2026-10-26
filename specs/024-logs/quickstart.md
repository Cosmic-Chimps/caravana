# Quickstart: Logs (024)

How to see the feature work end to end. Details are in [plan.md](./plan.md),
[data-model.md](./data-model.md), [contracts/api.md](./contracts/api.md) and the page contract
[contracts/logs-screen.md](./contracts/logs-screen.md).

## Prerequisites

- The lab up (`deploy/dev/lab.sh up`), Caravana running (`go run ./cmd/caravana`), an admin and a
  full token (`admin init`, `admin create-token`), signed in at `http://127.0.0.1:7070/`.
- Two apps in one place whose services print the sample lines: a busybox app running
  `sh -c 'while true; do echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) ERROR boom"; echo "[warn] careful"
  >&2; echo "{\"level\":\"error\",\"msg\":\"json boom\",\"time\":\"…\"}"; printf "\033[31mred\033[0m\n";
  echo same; echo same; echo same; echo "no time here"; sleep 5; done'` and one with `traefik/whoami`
  (Go `log` lines on stderr, untimed level).

## 1. The page

- `/logs`: both apps' services in the rail with copy counts and hues; the stream newest first with
  times in UTC, levels (the `ERROR` line red-tinted, `[warn]` warn, the JSON line error with "Raw"
  in its open row, the red line shown without its escape, `same` folded "×3", "no time here" with
  a leading `~`), the histogram with the error share, the footer's retention sentence.
- The range bar (15 min default, 1 h, 24 h, 7 d); the level chips with counts; the search
  (`boom` → the two error lines, "N lines"); the rail's "Only" a service, then a copy.

## 2. Around, older, gone

- Open an error row → "Lines around this" → the around view: 50 before and after, every level, that
  copy only, the line highlighted, the notice sentence, "Back to the stream".
- "Show older" at the bottom reads further back; "Older lines were dropped." when the read reached
  Nomad's oldest file.
- Deploy the busybox app with a new argument: the previous copy appears in the rail as "(gone)"
  with its last lines still in the stream while Nomad keeps them.

## 3. Deploys and Live

- While `/logs` is open with Live on, deploy an app: a "started rolling out" row and a marker, then
  "is live"; new lines arrive at the top briefly tinted; Paused shows "N new lines since".
- Without JavaScript (curl the page): the snapshot sentence; every filter is a link or a form.

## 4. Download and the API

- Download with a service filter: the file named `caravana-logs-<project>-<environment>-<from>-<to>.txt`,
  `# newest last` first, oldest first, tab-separated, the folded rows written out, `# deploy …` rows.
- `GET /v1/logs?project=…&environment=…&q=boom` with the token: the same lines newest first with
  `level`, `timeSource` and `id`; `GET /v1/logs/around?copy=…&at=…`; a deploy token on either → 403
  `token_scope`.
- `GET /v1/apps/<id>/logs`: oldest first as before, each line with `level`, `id`, `timeSource`.

## 5. The app's tab and the sidebar

- `/apps/<id>/logs` is the same page filtered to the app with "All logs"; the sidebar lists Logs
  after Domains.

## 6. The lab steps

- The spike first (research R8.1–R8.8): a test job with `LogConfig {MaxFiles 3, MaxFileSizeMB 1}`
  written past rotation; `logs?origin=end` across files; `readat` past a file's size and on a purged
  file; file names after a restart; a dead copy's lifetime; the scoped token on `ls`/`readat`; a
  `TZ=Europe/Madrid` container; escape bytes end to end; the read cost. Recorded in `research.md`
  as "lab, observed" before the service is written.
- `TestConsoleLab` step `console-logs` (`cmd/caravana/console_logs_test.go`): the two apps above
  through the forms: the stream, levels and times per sample line, folding, the search, the chips'
  counts, only-service and a copy, around with the highlight, Raw, a deploy during the step as a
  row and marker, Show older, Download's first lines and order, the app's tab with "All logs", the
  sidebar item, a gone copy, and no escape byte in any page or file; Nomad unreachable through the
  service's fake in a unit test (the lab's Nomad isn't stopped).
- The gate step `logs` (`cmd/caravana/gate_logs_test.go`): `listLogs`, `getLogsAround` and
  `downloadLogs` with the gate's token against the same app, every level and time as expected, the
  JSON `raw`, the fold, a deploy token's 403 on each, a wrong `at` → 422, a purged file → 404
  `log_rotated` (after the spike's job), and a search of every answer for `\x1b`, `\x07` and NUL.

## Checks before calling it done

`go build ./...`, `go vet ./...`, `go tool staticcheck ./...`, `gofmt -l .` empty, the two
cross-compiles, `go mod tidy -diff`, `tools/ui-css.sh check`, `tools/ui-vendor.sh check`,
shellcheck, `go test -race ./...`, `TestConsoleLab` (every step) and the gate step `logs` in the
lab; a sentinel a copy prints (`s3cret-sentinel-024…`) found only in the Logs page, the download
and the API's answers for that copy, never in the store, the server log, a notice or an event.
