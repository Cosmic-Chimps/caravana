# Feature 047: Requests, each HTTP request Traefik served, per app

CodeAlta task `5k6z` (raised by the maintainer on 2026-11-12 while comparing Caravana with other PaaS consoles).

## Why

Monitoring (025) counts requests, errors and the slowest 5 % per service per minute from Traefik's Prometheus
metrics, but nobody can see a single request: which path answered 500, who is hammering an endpoint, what a
client got at 14:03. 025 left the access log out on purpose (its plan's "Not in 025").

## User stories

### US1 (P1): see each request of an app, newest first

The owner opens an app's Requests tab (beside Logs) or the workspace's Requests page (one project › environment)
and sees each request Traefik served for the place's apps, newest first: when, the service, the method, the host,
the path, the status, how long it took, the bytes answered, the client's address and the TLS version. A request
made to an app shows within seconds.

Acceptance: a request to a whoami app shows on its Requests tab within 10 seconds with its path, status and
duration.

### US2 (P1): no secret escapes

Traefik writes only the fields the page needs. The query string and every header are never recorded: tokens and
session ids travel in them.

Acceptance: a request with `?token=s3cret-sentinel-047` shows its path only, and no page, API answer, log line,
notice or file Caravana writes holds the sentinel.

### US3 (P1): filter and see how far back it reaches

The page filters by status class (2xx, 3xx, 4xx, 5xx), method, path prefix, minimum duration and client, and
bounds the time range as Logs does (15 m, 1 h, 24 h, 7 d, or from and to, and "Show older"). A histogram of
the range shows the requests per bucket by status class. The read is bounded, so the page says how far back it
reached ("read from 14:02 UTC") and when the oldest line read was the oldest Nomad keeps.

Acceptance: filtering by 5xx and by a path prefix works; the page says how far back the file reaches.

### US4 (P2): links

A 5xx on Monitoring's Errors chart opens Requests at that minute, and the Monitoring apps table has a Requests
link per app. A request row opens Logs around its time for its service.

### US5 (P2): the API

`GET /v1/requests` (`listRequests`) with the same filters, `Cache-Control: no-store`, deploy tokens denied by
the default deny.

### US6 (P1): the hosts

The installer's Traefik job and the lab's write the access log (a rerun of the installer restarts Traefik once,
and says so). A host whose Traefik doesn't write it yet is told on the page, not shown an empty list.

## Refusals (field errors, 422 on the API as the logs')

- An unknown status class, method, a path prefix that doesn't start with `/` or is longer than 1024 bytes, a
  minimum duration that isn't a number of milliseconds, a client longer than 64 bytes, a bad range or from/to
  pair, `lines` outside 1–1000: each names its field.
- An unknown place, app or service: 404 as `listLogs`.

## Out of scope

Long-term request analytics (per-day traffic stays on Monitoring), geo lookup of client addresses, storing the
access log in SQLite, requests no app's router served (an unknown host's 404, the console's own requests, the
entrypoint redirects), Traefik's `forwardedHeaders` trust list (behind a proxy such as Cloudflare the client is
the proxy's address), a download of the requests, requests of the webhook-only listener.
