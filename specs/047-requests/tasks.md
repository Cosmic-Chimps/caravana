# Tasks 047: Requests

Plan: `plan.md` (D1–D13). Each phase is one commit; the lab steps run before the task is reported done.

## Phase A: the log, the parser, the read, the API

- [x] A1 Traefik jobs: `deploy/dev/traefik.nomad.hcl` (done while the plan was written: the access log
      arguments, `--log.filepath=/dev/stderr`, the `logs` block) and `deploy/install/traefik.nomad.hcl` the
      same, with its head comment rewritten (D1, D2, D12); `install.sh`'s `run_traefik` says Traefik is
      restarted once for its access log when the running job lacks `accesslog`.
- [x] A2 `internal/accesslog`: `Request`, `Parse`, `Class`, `Owner`/`Owners`/`OwnersOf` (D3, D4), table
      tests from the lab's lines (plan "Lab, observed"), the no-network-imports test.
- [x] A3 `internal/deploy`: `ProxyCopy(ctx) (Copy, error)` (the running allocation of job `traefik`,
      `ErrNoProxy`), `ProxyAccessLog(ctx) (bool, error)` (D8), with `deploytest` support (the fake's
      `SetTraefikJob` and a running allocation of it, its stdout files through `AddLogFile`/`AppendLogLine`).
- [x] A4 `internal/service/requests.go`: `RequestQuery` with `Validate` (D7, D11), `ReadRequests` (D5),
      `RequestRead`, `Histogram`; `requestslive.go`: `WatchRequests` and `ChangeRequests` (D6). Tests against
      the fake file system: the range, every filter, the owner resolution (a service of another place, a
      deleted app's, `caravana@file`), sorting by `StartUTC` across completion order, coverage and
      `dropped`, `accessLog` false, Traefik missing, and the sentinel search over rows, the log and errors.
- [x] A5 `internal/api/requests.go`: `GET /v1/requests` `listRequests` (`no-store`), the query parsing, the
      errors (422 `invalid_field`, 404 as the logs', 503 `proxy_unavailable`); `api/openapi.yaml` 0.52.0
      (`listRequests`, `RequestRead`, `RequestEntry`, `RequestHistogram`, the code); the scope walk test
      (deploy tokens denied by the default deny: nothing to allow) and the parity test entries; the API
      sentinel test.
- [x] A6 `go build ./...`, `go vet ./...`, `go tool staticcheck ./...`, `gofmt -l .`, `go test ./...`,
      `shellcheck` on the changed scripts, `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`.

## Phase B: the console, Monitoring's links, the docs

- [x] B1 `internal/ui/requests.go` and `templates/requests.html`: `/requests`, `/parts/requests`,
      `/apps/{id}/requests` (the tab after Logs in `builtTabs`), the sidebar item, the live routes
      (`watch=requests`), the states (no Traefik, no access log yet, nothing in range, read cut short),
      "read from <clock>" and "the oldest kept" as Logs says them (D10). `tools/ui-css.sh build`.
- [x] B2 Monitoring: the Errors chart's caption link, the plot click in `static/js/monitoring.js` (and the
      hidden table rows' links), the apps table's Requests link (D10).
- [x] B3 Tests in `ui`: the page's shape, each filter's hidden fields and hrefs, the app tab, the states,
      the sentinel search over every page and the log. `api/parity_test.go` entries for the new routes.
- [x] B4 `docs/requests.md` (what is recorded, what isn't, behind a proxy, how far back, the API), the row in
      `docs/plan.md`'s "What other PaaS tools have" table (13, done in 047), `CLAUDE.md`'s "Where we are"
      paragraph, `changelog.d/047-requests.md` (Added, and Upgrade: rerun the installer, Traefik restarts
      once).
- [x] B5 The checks of A6 plus `tools/ui-css.sh check` and `tools/ui-vendor.sh check`.

## Phase C: the lab

- [ ] C1 `TestConsoleLab` step `console-requests` (`cmd/caravana/console_requests_test.go`, listed after
      `console-logs`): a busybox httpd app with a CGI script that answers 500 on `/cgi-bin/boom`; requests
      through Traefik with `?token=s3cret-sentinel-047`, a POST, the 500; the app's Requests tab shows them
      within 10 s with the path alone; the 5xx chip and a path prefix filter; "read from"; the sentinel in no
      page, answer or log line; the workspace page and the Monitoring link.
- [ ] C2 The gate step `requests` (`gate_requests_test.go`, after `logs`): the same through the API with the
      gate's token, the refusals naming their fields, a deploy token's 403, and the sentinel search over
      every answer, the log and the data directory.
- [ ] C3 `deploy/dev/install-test.sh`: the `accesslog` argument in the running Traefik job after a rerun and
      one request on `GET /v1/requests` (D12).
- [ ] C4 Run the two lab steps (`-run 'TestConsoleLab/^console-(sign-in|requests)$'` and
      `-run 'TestLab/^(api-needs-token|requests)$'`) and record the outcome here and in `CLAUDE.md`.
