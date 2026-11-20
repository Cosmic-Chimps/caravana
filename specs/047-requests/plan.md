# Plan 047: Requests

Spec: `spec.md`. Task `5k6z`. Branch: the worktree branch `alta/hopeful-glen-8f72`.

## Constitution check

- I Small by default: no new module, no new runtime dependency. The log is Traefik's own, kept by Nomad's
  log rotation; Caravana stores nothing.
- II One model: nothing in the app model changes; `nomadjob` is untouched (Traefik's job is the installer's
  and the lab's hand-written job, not a translated one).
- III Safe deploys: unchanged.
- IV Fail loudly: every refusal names its field (D11); a host whose Traefik doesn't write the log yet is told
  on the page and in the answer (D8).
- V API-first: `api/openapi.yaml` 0.52.0 in the same change (`listRequests`); the console calls the same
  service read. `api/parity_test.go` maps the console's routes to it.
- VI Nomad-native: the log is read through the official client's fs API (`read-fs`, in the policy since
  016), from Traefik's allocation; nothing else of Traefik's is touched.
- VII Secure: the query string and every header are dropped by Traefik before the line exists (D2); a
  request line is never logged, stored, put in an error, a notice or a live event (D9); every answer is
  `no-store`.
- VIII Plain, tested Go: a pure parser with table tests, the service read tested against `deploytest`'s
  fake file system, sentinel tests in `service`, `api` and `ui`.

## Decisions

- **D1 Where the log is.** Traefik's access log goes to its stdout (`--accesslog=true` with no `filepath`;
  Traefik's `accesslog.Handler` opens `os.Stdout` when the path is empty, v3.7.13 source). Nomad keeps a
  task's stdout in `alloc/logs/traefik.stdout.<n>`, rotated by size and purged beyond `max_files`, which is
  exactly what 024's reader walks. The job's `logs { max_files = 10, max_file_size = 20 }` keeps 200 MB:
  about 500,000 requests at ~400 bytes a line. Traefik's own log moves to stderr
  (`--log.filepath=/dev/stderr`; lab, observed: the stderr file holds the "Starting provider" lines and
  the stdout file holds access lines only), so the stdout stream is the access log alone; the parser still
  skips any line that isn't one (D3).
- **D2 What Traefik writes.** `--accesslog.format=json`, `--accesslog.fields.defaultmode=drop` and `keep`
  for exactly `StartUTC`, `RouterName`, `ServiceName`, `RequestMethod`, `RequestHost`, `RequestPath`,
  `DownstreamStatus`, `Duration`, `DownstreamContentSize`, `ClientHost`, `TLSVersion`, `RetryAttempts`,
  `entryPointName`; `--accesslog.fields.headers.defaultmode=drop` (no header, so no cookie, authorization,
  referer or user agent) and `--accesslog.fields.queryparameters.defaultmode=drop` (`RequestPath` is the
  path alone: Traefik v3.7's logger copies the URL with `RawQuery` only when `KeepQueryParameters()`; lab,
  observed: `?token=s3cret-sentinel-047&x=1` gave `"RequestPath":"/"`). `ClientHost` is the TCP peer, or
  `X-Forwarded-For` when a trusted proxy sent it (Traefik strips the header from untrusted peers), so
  behind Cloudflare it is Cloudflare's address: said in the docs, not solved here. logrus adds `time`
  (seconds), `level` and `msg` (empty) to every line; `RouterName` and `ServiceName` are absent on a request
  no router took (lab, observed: an unknown host's 404).
- **D3 The parser is pure.** `internal/accesslog`: `Parse(line) (Request, bool)` reads one JSON line that has
  `StartUTC` and `DownstreamStatus` (anything else is not a request and is skipped: Traefik's own lines if
  they ever land there, a cut line, junk); `Request{Time, Router, Service, Method, Host, Path, Status,
  Duration, Bytes, Client, TLS, Retries, EntryPoint}`, the `@nomad` provider suffix removed from the router
  and service names. `Class(status)` is `2xx`…`5xx`, and `other` for anything outside 100–599 (a 0 for a
  request the client dropped). A path longer than 2 KiB is cut (what the page shows is text). A test
  forbids network and Nomad imports.
- **D4 Whose request it is.** Traefik's `ServiceName` is `<app ID>-<service>` (the router may be
  `<app>-<service>-<hash8>` for a domain with options, 023), and app IDs and service names both hold `-`,
  so the name is resolved against the stored apps: `accesslog.Owners`, built once per read from
  `store.ListApps`, maps `id + "-" + service` to the app's place, ID and service (`OwnersOf(apps)`,
  pure). A service name no stored app owns (`caravana@file`, a deleted app) belongs to no app and is left
  out. The place filter, the app and service filters are applied on the owner.
- **D5 The read reuses 024's walk.** `service/requests.go`: `ReadRequests(ctx, RequestQuery)` finds
  Traefik's running allocation (`deploy.ProxyCopy`: the running allocation of job `traefik`, newest
  first; none is `deploy.ErrNoProxy`), lists its files with `LogFiles(ctx, allocID, "traefik")`, keeps the
  stdout ones and walks them with `walkLogStream` (the stop-at-range works because `logline.TimeOf` reads
  the line's `time` field), under 024's `logSem` and deadline; the budget is 8 MiB for the one stream
  (about 20,000 requests), not the per-stream 1 MiB. Each raw line is parsed (D3), resolved (D4), kept
  when it is in the window and passes the filters, then every kept request is sorted newest first by
  `StartUTC` (the file is in completion order: a slow request lands after faster ones that started later;
  a request that ran across the range's start may be missed by the walk's stop, which is said in the
  contract). `RequestRead{Requests, Coverage (024's LogCoverage), Dropped, From, To, Total, AccessLog
  bool}` and `Histogram()`: 30 buckets with counts per class and `read` (false before the coverage).
- **D6 Live.** `WatchRequests(project, environment)`, like `WatchLogs`: while a Requests page with Live on is
  open, one follower reads Traefik's stdout from now (`Deployer.Logs` with follow, tail 0) and parses only
  the `ServiceName` of each line to find the app (D4); a `ChangeRequests` notice (`"requests"`, the app ID,
  nothing else) goes out at most every 2 seconds per place. Pages of the same place share the follower;
  Traefik's allocation is looked up again when the follow ends (a Traefik restart). The console's
  `/live?watch=requests&live=1` and `/live/apps/{id}?watch=requests&live=1` carry it, and the page's
  region refetches from `/parts/requests`.
- **D7 Filters.** `status` (comma list of classes), `method` (an HTTP method name, upper-cased), `path` (a
  prefix; starts with `/`, at most 1024 bytes), `minDuration` (milliseconds, integer, 0 = none), `client`
  (exact match of the address, at most 64 bytes), plus the logs' `project`, `environment`, `app`,
  `service` (`<app>/<service>`), `from`, `to`, `range`, `before`, `lines` (1–1000, default 200).
- **D8 A host without the log.** The read also reads Traefik's job (`Jobs().Info("traefik")`): when its task's
  arguments hold no `--accesslog=true`, `AccessLog` is false and the console says "Traefik isn't writing its
  access log yet: rerun the installer, which restarts Traefik once." instead of an empty stream; the API
  answers the same read with `accessLog: false`. No Traefik job at all (not a Caravana host) is
  `ErrNoProxy` → 503 `proxy_unavailable` with "Traefik isn't running".
- **D9 Secrets rule.** A request line is Traefik's text about an app's request and may hold a path a person
  typed. It is never written to the store, the server log, an error, a notice or a live event; the
  `requests` notice holds the app ID only; every API answer is `no-store`; the page draws the path as
  text. Sentinel tests in `service`, `api` and `ui` (and the lab steps) search every row, log line,
  answer and page for `s3cret-sentinel-047`.
- **D10 Console.** `/requests` (the workspace page: the Logs page's shape, with the range bar and Live, the
  place bars, a filter form of status chips, a method select, a path prefix input, a minimum duration
  select (any, 100 ms, 500 ms, 1 s, 5 s) and a client input, a services rail with request counts, the
  stacked histogram by class, and the table: time, service (its hue), method, path, status, duration,
  bytes, client; a row links to Logs around its time for its service,
  `/logs?service=<app>/<svc>&from=<t-1m>&to=<t+1m>`), `/parts/requests` (the histogram and the table, on
  the `requests` notice), `/apps/{id}/requests` (the tab after Logs, the same page filtered to the app,
  with "All requests"), the sidebar's Requests item after Logs. Monitoring: the Errors chart's caption
  gets "See the requests that failed" (`/requests?status=5xx` with the chart's range), a click on its
  plot opens Requests at the hovered minute (`monitoring.js` knows `t0` and `step`; the hidden table's
  rows carry the same links for a page without script), and the apps table gets a Requests link per app
  beside the Logs one. Forms and links work without JavaScript; Live needs it.
- **D11 Refusals.** Field errors as `EnvLogQuery.Validate` does; the API maps them to 422 `invalid_field`
  like `listLogs`.
- **D12 Hosts.** `deploy/install/traefik.nomad.hcl` gets the same arguments and `logs` block as the lab's
  (its head comment, which said the log stays off, now says why it is on); `install.sh`'s `run_traefik`
  says "Traefik is restarted once for its access log" when the running job lacks `accesslog`;
  `install-test.sh` checks the argument in the job and a request on `GET /v1/requests`. No policy change.
- **D13 Multi-server.** Traefik runs on the ingress server only (031), so one allocation holds every app's
  requests; the fs read reaches its node through the Nomad server as the logs' do.

## Lab, observed (2026-11-20, Traefik v3.7.13 in the lab)

- The access log goes to stdout and Traefik's own log to stderr with `--log.filepath=/dev/stderr`.
- One line: `{"ClientHost":"::1","DownstreamContentSize":398,"DownstreamStatus":200,"Duration":3552007,
  "RequestHost":"whoami.localhost","RequestMethod":"GET","RequestPath":"/","RetryAttempts":0,
  "RouterName":"whoami-web@nomad","ServiceName":"whoami-web@nomad","StartUTC":"2026-11-20T19:36:54.533596236Z",
  "TLSVersion":"1.3","entryPointName":"websecure","level":"info","msg":"","time":"2026-11-20T19:36:54Z"}`;
  `Duration` is nanoseconds.
- A request with a query string is written with its path alone.
- An unknown host's 404 has no `RouterName` and no `ServiceName`.

## Not in 047

Long-term analytics, geo lookup, storing the log, requests no router took, Traefik's `forwardedHeaders`
trust list, a download, the webhook-only listener's requests, a per-copy attribution (Traefik's line names
the service, not the copy).

## Deviations

Phase A (2026-11-20):

- **Status classes include `1xx`.** D3 listed `2xx`…`5xx` and `other` for anything outside 100–599, which leaves a
  101 (every websocket upgrade) with no class. `accesslog.Class` gives `1xx` for 100–199; the filter and the
  histogram take `1xx`, `2xx`, `3xx`, `4xx`, `5xx` and `other` (`accesslog.Classes`).
- **`OwnersOf` takes `[]accesslog.OwnerApp`**, not stored apps: the package is pure and may import no Caravana
  package (its own test says so). `service.ownersOfRecords` turns `store.AppRecord`s into them. Two services whose
  joined names are equal are owned by the app whose ID sorts first.
- **`deploy.ErrNoProxy`'s text is "no Traefik is running"** (staticcheck ST1005 refuses a capital first word); the
  API's 503 `proxy_unavailable` message is the fixed sentence "Traefik isn't running", as D8 says.
- **Coverage after a Traefik restart.** D5 reads only the running allocation, so after a restart (the installer's rerun
  makes one) the older requests are in the previous allocation's files and the walk ends at the new allocation's first
  file as if it had read the range. The read says so: when the walk ends at the oldest file and the allocation's
  `CreatedAt` is after the range's start, `Coverage` is incomplete and `From` is the allocation's start ("read from
  14:02 UTC"). Reading the older allocations too (`deploy.LogCopies([]string{"traefik"})` already lists the finished
  ones until Nomad collects them) is a follow-up, not done here.
- **`RequestRead` has `Services`** (`[]RequestService{App, Service, Count}`): the rail's counts, taken after the
  status, method, path, duration and client filters but not the app and service ones, so the rail can offer the other
  services of the place. The list and the histogram are narrowed by app and service.
- **The method filter** is any word of capital letters up to 20 (`PROPFIND` is a method Traefik logs), not a fixed
  list; the API and `ReadRequests` upper-case what they are given.
- **The API's JSON** (`RequestRead`): `requests[].durationMs` is a number in milliseconds (fractions kept),
  `statusClass` is the class, `histogram.buckets[].classes` is an object with the six classes (zeros included), and
  `services` is the rail. There is no top-level `from`/`to`: the histogram's are the range.
- **`logNotice` carries its notice kind** (`what`) and `WatchLogs`/`WatchRequests` share `watchPlace`;
  `SetLogWatchGaps` sets both watchers' gaps. `deploytest.SetTraefikJob(image, args...)` takes the job's arguments.
