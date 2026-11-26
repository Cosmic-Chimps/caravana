# Screenshots of the documentation

The pictures in `docs/images/` (`<name>-light.png` and `<name>-dark.png`, one pair per screen) are made by
a script, never by hand, from a throwaway Caravana console filled with made-up data:

```bash
site/scripts/screenshots/run.sh
```

Re-run it after a change to the console and commit the pictures that changed.

## What it needs

- Go (the version in `go.mod`) and Node 20 or newer with npm.
- Network access the first time: `npm ci` fetches Playwright and sharp, and
  `npx playwright install chromium` downloads Chromium. Nothing is fetched on later runs.
- No Docker, no Nomad, no real installation, and no account: the demo console runs in the test
  process and talks only to stand-ins on loopback (see "What the demo is").

It takes about five minutes: the Go build, the demo's seed, about two minutes for the demo's sampler
to take its second reading (so Monitoring has request numbers up to the present), then the captures.

## What it does

1. Builds the console's test binary (`go test -c ./internal/ui`), so a UI change is always in the pictures.
2. Starts the demo, `TestDocsDemo` in `internal/ui/docsdemo_test.go`, on `127.0.0.1:7099`
   (`DEMO_PORT` changes it), and waits for its line `docs demo ready at http://…`.
3. `capture.mjs` signs in through the real form as `ana@example.com` and takes every screen of its
   `SCREENS` table in the light theme and then the dark one, into `out/raw/` (git-ignored).
4. `optimise.mjs` makes 256-colour PNGs in `out/final/` and fails over 250 KB for one picture or 16 MB
   for the set.
5. The pictures are copied to `docs/images/`.
6. The demo is stopped through a stop file and its temporary directory removed, pass or fail.

The command exits non-zero if any screen failed (a selector moved, a page answered an error or
sent the browser to the sign-in page), if a page shows a token (`cvat_…`, `cvwh_…`, `cvin_…`,
`cvjn_…`), the text "Reconnecting" or a name that is not made up, or if a picture is over its budget.

`ONLY=app-running,logs run.sh` takes only those screens, leaves them in `out/final/` and does not
copy them. `KEEP=1` leaves the demo running at the end, to look at it in a browser (sign in as
`ana@example.com`, password `demo-password-for-screenshots`: it protects nothing). `FULL=1` takes whole
pages instead of the frames, to choose a frame.

## How a screen is taken

- The theme is the console's own choice, the cookie `caravana_theme` (feature 028), set to `light` and
  then `dark`: the same data, drawn by the server in each theme. Nothing in a page is changed.
- The pictures are 1280×800 at device scale factor 2. A screen is either the whole viewport, or a
  clip: the union of the boxes of some selectors plus 16 px of the page around them.
- The console's live streams are left to connect, and a picture is taken when the live indicator says
  "live", so "Reconnecting…" is never in one.
- `localtime.js`, the optional script that adds "· 16:15 local" after each time, is not loaded, and the
  browser's zone is UTC: the pictures show UTC only, as the console does without the script.

## Adding a screen

1. Find its route in `internal/ui/access.go`, and give the demo the data it needs in
   `internal/ui/docsdemo_seed_test.go` or `docsdemo_data_test.go` (through the service, the store or
   the fake Nomad, like the other tests of `internal/ui`; never a change to production code).
2. Add an entry to `SCREENS` in `capture.mjs`: `path`, a `ready` selector, a `clip` (or none for the
   viewport) and, if it needs clicks, `steps`.
3. Run `ONLY=<name> run.sh`, look at `out/final/<name>-light.png` and `-dark.png`, and refer to them
   from a page of the guide as `../images/<name>-light.png` inside a `<picture>`.

## What the demo is

The made-up installation the guide is written against: project `shop` (display name "Shop") with the
environments `staging` and `production` and a preview `pr-31`; the apps `web` (two copies, `shop.example.com`),
`api` (`api.shop.example.com`, two secrets, two schedules) and `db` (Postgres on a volume, backed up daily)
in production and `web` in staging, each with a few deployments (the console, a deploy token `shop-ci`
from CI, one reverted); a second person `ben@example.com`; a registry `ghcr.io/acme`; a backup
destination; a notification channel; API tokens; three servers; a week of monitoring history; one
needs-you item (the failed run of `nightly-report`). Every name is made up and ends in `example.com`;
logs and requests are generated, with no secret in them.

It is the console of `internal/ui` over a real service and SQLite store in a temporary directory,
with Nomad, the registry, the release check, the template catalog and Traefik's metrics replaced by
stand-ins on loopback. Nothing in the demo reaches the network. The deploy history is stored directly,
not made by real deploys, which is why the copies' output, CPU and memory are the fake Nomad's.

## Why a few screens may change between runs

- Times are relative to the run: "3 hours ago", the clock of the last log line, the 7-day band of
  Monitoring. The numbers (requests, memory) are generated from fixed seeds, so they repeat, but
  the sampler adds one reading of the current minute.
- Copy and request IDs are made from fixed seeds, so they repeat.
- The terminal prints what the fake shell prints (`echo` works, `stty size` and `exit`); nothing else.

## Files

| File | What it is |
| --- | --- |
| `run.sh` | The whole pipeline, with the reasons for running the browser on the host. |
| `capture.mjs` | The `SCREENS` table and the walk. |
| `optimise.mjs` | 256-colour PNGs and the size budget. |
| `package.json` | Playwright and sharp, pinned. |
| `../../../internal/ui/docsdemo_test.go` | `TestDocsDemo`: builds, seeds and serves the demo. |
| `../../../internal/ui/docsdemo_seed_test.go`, `docsdemo_data_test.go` | The made-up data. |
