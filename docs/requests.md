# Requests: each HTTP request Traefik served

Monitoring counts requests, errors and the slowest 5 % per minute. **Requests** shows the requests themselves:
when, which service, the method, the host and path, the status, how long it took, the bytes answered and the
client's address. Open it from the sidebar (one project › environment), from an app's Requests tab (after Logs),
from Monitoring (the Errors chart and the Requests link in each row of its apps table) or from the API.

It is a read of Traefik's own access log. Caravana stores nothing: no table, no copy, no file of its own.

## What is recorded

Traefik writes one JSON line per request with **only** these fields: when it started, the router and service
that took it, the method, the host, the **path**, the status, how long it took, the bytes of the answer, the
client address, the TLS version, how many times Traefik retried it on another copy and the entry point.

What is **never** recorded, because Traefik drops it before the line exists (`deploy/install/traefik.nomad.hcl`
sets it, and the page says it):

- the **query string** (`/login?token=abc` is `/login`),
- every **header**: cookies, `Authorization`, the referer, the user agent,
- request and response bodies.

A path may still hold what a person typed (`/users/ana@example.com`), so Caravana never writes one to its own
log, an error, a notice or the database. Only the page and the API's answer show it, and neither is cached.

## Which requests

The requests an **app's router** served: a request to an unknown host (Traefik's 404), the console's own
requests, the entry points' redirect to HTTPS and the webhook-only listener's requests are left out, since no app
owns them. A request is the app's when its Traefik service is one of the app's services; a deleted app's old
requests are left out too.

Traefik runs on the first server only, so one allocation holds every app's requests, whatever server the copies
are on. Traefik names the service, not the copy: a request is not attributed to a copy.

## Behind a proxy

The client address is the TCP peer, or the `X-Forwarded-For` address when the request came from a proxy Traefik
trusts (Traefik drops the header from any other peer). Caravana does not configure Traefik's trust list, so
behind Cloudflare or another proxy the client is **the proxy's address**. Filtering by client still works, on
that address.

## How far back it reaches

The log is Traefik's standard output, which Nomad keeps in files and rotates: 10 files of 20 MB, about 200 MB,
which is roughly 500,000 requests at 400 bytes a line. Nothing older exists.

A page reads Traefik's newest files backwards, until it has the range, or has read 8 MiB (about 20,000
requests), or 5 seconds have passed. The page says how far it got:

- **"read from 14:02 UTC"** under the histogram: the read stopped there (the budget, or the time) and the
  bars before it are hatched, not zero. **"not all read in time"** says it was the clock. **Show older** reads
  the requests before the oldest one shown.
- **"This is the oldest Nomad keeps"**: the read reached the oldest file Nomad has, and older ones were purged.
- A page shows the newest 200 requests and says how many the read found.

After Traefik restarts (the installer's rerun restarts it once), the earlier requests are in the previous
allocation's files, which a read does not open: the page reads from the new allocation's start and says so.

A request is listed at the time it **started**. The file is written when a request **ends**, so a slow request
sits after faster ones that started later; the page sorts them by start. A request that began just before the
range and ended inside it may be missed.

## The page

`/requests` has the Logs page's shape: a range (15 min, 1 h, 24 h, 7 d) and Live, the place bars, the services
rail with each service's count, a histogram of the range **stacked by status class** (2xx, 3xx, 4xx, 5xx, and
the rare 1xx and "other" for a client that went away) and the table. Everything is a link or one form, so it
works without JavaScript; Live needs it, and refetches the histogram and the table when one of the place's apps
was asked something.

Filters, all in the address: the status chips (2xx, 3xx, 4xx, 5xx), a method, a path prefix, a minimum duration
(100 ms, 500 ms, 1 s, 5 s) and a client address. A row opens **Logs around its time** for its service, a minute
either side. Monitoring's Errors chart links to the 5xx of its range ("See the requests that failed"), a click
on its plot opens the requests of the minute you point at, and each of its hidden table's rows is the same link.

## A host that doesn't write the log yet

Traefik's job gains the access log with this feature. Until you **rerun the installer** (which restarts Traefik
once, a short gap on ports 80 and 443) the page says "Traefik isn't writing its access log yet: rerun the
installer, which restarts Traefik once." and the API answers `accessLog: false`. If Traefik is not running at
all the page says so, and the part answers 503.

## API

`GET /v1/requests` (`listRequests`, API 0.52.0): the filters above as `status` (a comma list), `method`, `path`,
`minDuration` (milliseconds), `client`, with the logs' `project`, `environment`, `app`, `service`
(`<app>/<service>`), `range` or `from` and `to`, `before` and `lines` (1 to 1000, 200 by default). The answer has
`accessLog`, `requests` (newest first), `total`, a `histogram` of 30 buckets by class, `coverage`, `dropped` and
the `services` of the rail. It is `Cache-Control: no-store`. A bad value is 422 `invalid_field` naming it, an
unknown place or app 404, Traefik not running 503 `proxy_unavailable`. Deploy tokens are refused.
