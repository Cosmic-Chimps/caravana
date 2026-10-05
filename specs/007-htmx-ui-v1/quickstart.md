# Quickstart: HTMX UI v1

How to check that feature 007 works, from the automated checks to a phone-width look at every
page. Routes: [contracts/api.md](contracts/api.md) and [contracts/pages.md](contracts/pages.md).

## 1. Automated checks (no lab, no network)

```bash
go test -race ./...            # store, deploy (fake Nomad), service, api, ui, parity test
go vet ./... && go tool staticcheck ./... && gofmt -l .
tools/ui-css.sh check          # app.css matches its sources and templates
tools/ui-vendor.sh check       # htmx, its SSE extension: committed files match their pinned checksums
shellcheck deploy/dev/*.sh deploy/vm/*.sh tools/*.sh   # via Docker on the Mac
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
```

Expected: everything passes. `go test ./internal/ui -run TestNoSampleData -v` renders every page
against an empty and a populated store and finds no text that isn't from the store or the fake
Nomad (SC-007). `go test ./internal/api -run TestConsoleParity -v` lists each console action with
its `/v1` operation (SC-006).

## 2. The console against the lab

```bash
deploy/dev/lab.sh up
eval "$(deploy/dev/lab.sh env)"
go run ./cmd/caravana &
go run ./cmd/caravana admin init --email you@example.com     # once per data directory
open http://127.0.0.1:7070/                                 # sign in → /apps
```

1. **Empty**: `/apps` says "No apps yet." with "New app". The live indicator says "Live".
2. **New app**: name `whoami`, image `traefik/whoami:latest`, port `80`, domain
   `whoami.localhost`, health path `/`. "Create and deploy" lands on Deployments; the entry
   goes pending → running (with progress) → healthy without a reload, within seconds.
   `https://whoami.localhost:8443` answers.
3. **Field errors**: New app with name `whoami` again → "An app called `whoami` already exists."
   on Name. Name `Bad_Name` and image `nope/nope:0` → errors on both fields, values kept.
4. **Live from another client**: keep `/apps` open in one tab and run
   `curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:7070/v1/apps/whoami/deployments`.
   Within 5 s the row shows rolling out, the headline counts it, and activity lists it "through
   the API".
5. **Changed image**: Deployments › change the image to `traefik/whoami:v1.10` › Deploy. The
   new entry shows the new digest; Configure shows the new image and nothing else changed.
6. **A failed deploy**: through the API, deploy whoami with health path `/nope`. The entry ends
   `reverted` with "v3 failed its health checks. Nomad rolled back to v2, which is serving." and
   the raw error under it; Running shows the failed copies of v3 while Nomad keeps them.
7. **Running**: two copies (`replicas: 2` through the API). From `deploy/dev/lab.sh shell`, stop
   one allocation; Running shows it stop and its replacement start, live.
8. **Logs**: `curl -sk https://whoami.localhost:8443/` a few times; the Logs tab shows the
   request lines arriving. Pause → "Paused. 3 new lines." → Resume shows them. Choose one copy
   and `stdout` → only its lines. Wrap toggles.
9. **Webhook**: Configure › Create token shows the URL once; calling it redeploys; reload and
   it's gone; Revoke (confirm) → the call gets 401.
10. **Session**: in a second browser profile, sign in and leave the Logs tab open; in the first
    profile, change the password in Settings (that signs out other browsers) → the open tab's
    indicator says "Signed out. Sign in again." within 30 s and the log stream stops.
11. **Nomad down**: `limactl stop caravana-lab`. `/apps` still lists apps with status
    "unknown" and the sentence about Nomad; Running and Logs say Nomad can't be reached. Start
    the lab again; the pages recover without a reload.
12. **Server restart**: restart `go run ./cmd/caravana` while `/apps` is open; the indicator
    shows "Reconnecting…", then "Live", and the page is current, with no duplicated rows.
13. **Delete**: Configure › Delete app… The button stays disabled until `whoami` is typed;
    deleting lands on `/apps` with "`whoami` was deleted."; the Nomad job is gone
    (`sudo lab-nomad job status whoami` in the lab shell: not found); `GET /v1/apps` doesn't
    list it; New app with `whoami` works again.
14. **Settings**: `/account` lands on `/settings`; create and revoke an API token there.

## 3. Without JavaScript, at phone width, with the keyboard

- Turn JavaScript off: every page renders its current data; New app, Deploy, webhook, delete
  (the server checks the typed name) and Settings work as plain form posts; Logs shows the
  server-rendered tail and says following needs JavaScript.
- At 390 px wide (browser dev tools): the sidebar is a "Menu" disclosure; app rows stack; no
  page scrolls sideways; touch targets are at least 44 px.
- Keyboard only: every link, tab, button, disclosure and field is reachable with Tab, has a
  visible focus ring and works with Enter/Space.
- With "reduce motion" on: nothing animates.
- Open five console tabs over plain HTTP: they all stay live. A sixth may wait for a free
  connection (HTTP/1.1 allows 6 per host; research R12). Over HTTPS on the VM there's no limit
  in practice.

## 4. Lab end-to-end test and the VM gate

```bash
CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 25m
deploy/vm/host.sh up        # applies the policy with read-logs
deploy/vm/host.sh prove     # GATE PASSED, now including the console-reads step
```

`TestLab` adds: sign in through `/login`, create whoami through the New app form, receive a
`deployments` notice on `/live/apps/whoami` within 5 s, see the deploy turn healthy, read a log
line through `GET /v1/apps/whoami/logs`, deploy a changed image, delete the app and see Nomad's
job gone.
