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

- [x] C1 `TestConsoleLab` step `console-requests` (`cmd/caravana/console_requests_test.go`, listed after
      `console-logs`): a busybox httpd app with a domain and two CGI scripts, `/cgi-bin/boom` (500) and
      `/cgi-bin/things` (201); requests through Traefik with `?token=<sentinel>`, a POST and the 500; the app's
      Requests tab shows them within 20 s with the path alone; the 5xx chip, a path prefix and a method
      filter; "following Traefik" and the histogram summary; the sentinel in no page, answer or log line; the
      workspace page, the row's link to Logs and the Monitoring link.
- [x] C2 The gate step `requests` (`gate_requests_test.go`, after `logs`): the same through the API with the
      gate's token, the filters, the refusals naming their fields (and the 404s), a deploy token's 403
      `token_scope`, no token's 401, `no-store`, and the sentinel search over every answer, the log and the
      data directory.
- [x] C3 `deploy/dev/install-test.sh`: `check_requests_host` (the `accesslog` arguments in the running Traefik
      job, the headers and query string dropped, and `GET /v1/requests?app=whoami` answering `"accessLog":true`
      with a request of whoami), called after "deploy whoami". **Not run** (20 minutes on a VM); shellcheck
      is clean.
- [x] C4 Both lab steps run and pass (2026-11-20, lab VM with Nomad 2.0.7):
      `TestConsoleLab/^console-(sign-in|requests)$` PASS, `console-requests` 31.9 s;
      `TestLab/^(api-needs-token|requests)$` PASS, `requests` 32.8 s.
      Deviations found by running them, none in the product:
      - busybox's httpd answers a POST to anything that isn't a CGI script with 501 (checked in the lab), so
        the steps POST to `/cgi-bin/things` (a CGI answering 201), not `/api/things`, and filter by
        `path=/cgi-bin/t`.
      - Traefik's log keeps the requests of earlier runs, and a request belongs to the app ID it names (also
        an ID made again), so each run uses an app of its own (`cl047<hex>`, `gate047<hex>`) and its three
        requests are the only ones counted; the first version of the console step failed on four boom rows
        from the three runs before it.
      - With one app listed, the services rail doesn't prefix the app name (by design, D10), so the rail check
        reads `web <n>`.
