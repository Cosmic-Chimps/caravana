# Quickstart: Minimal Auth

How to check that feature 006 works, from a clean data directory to a revoked token. Routes and
bodies: [contracts/openapi.yaml](contracts/openapi.yaml); pages:
[contracts/pages.md](contracts/pages.md); CLI: [contracts/cli.md](contracts/cli.md).

## 1. Automated checks (no lab, no network)

```bash
go test -race ./...            # store, service.Auth, httpx, api (401/403 per route), ui, cli
go vet ./... && go tool staticcheck ./... && gofmt -l .
tools/ui-css.sh check          # the committed app.css matches its sources
shellcheck deploy/dev/*.sh deploy/vm/*.sh tools/*.sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...
```

Expected: everything passes; `tools/ui-css.sh check` prints that `app.css` is up to date.

## 2. A fresh install refuses the API

```bash
rm -rf /tmp/cv006 && go run ./cmd/caravana -data-dir /tmp/cv006 &
curl -s localhost:7070/v1/health                    # {"status":"ok"}
curl -si localhost:7070/v1/apps/whoami | head -1    # HTTP/1.1 401 Unauthorized
curl -s localhost:7070/v1/apps/whoami               # {"error":{"code":"unauthorized",...}}
```

The server log has a warning naming `caravana admin init`. Opening
<http://localhost:7070/login> shows "This caravana has no admin yet" with the command, and no
form.

## 3. Create the admin while the server runs

```bash
go run ./cmd/caravana admin init --email you@example.com -data-dir /tmp/cv006
go run ./cmd/caravana admin init --email other@example.com -data-dir /tmp/cv006; echo $?   # 3
go run ./cmd/caravana admin status -data-dir /tmp/cv006                                      # admin: you@example.com
```

Reload `/login`: the form is there now, without restarting the server.

## 4. Sign in on the web

1. Sign in at <http://localhost:7070/login>. You land on `/account`: "Signed in as
   you@example.com."
2. Sign out, then sign in with a wrong password: "That email and password don't match. 4 tries
   left before a 15-minute wait." After the fifth failure: "Too many failed sign-ins from this
   address. Try again in 15 minutes." The right password is refused too until then.
3. Open <http://localhost:7070/account> in a private window: you're sent to
   `/login?next=/account`, and back to `/account` after signing in.
4. Check the page against DESIGN.md at desktop and phone widths, tab through it with the
   keyboard, and load it with JavaScript off. The browser's network panel shows no request to
   another host.

To reset the limit while testing, restart nothing: wait 15 minutes, or delete the data
directory and start over.

## 5. API tokens

```bash
TOKEN="$(go run ./cmd/caravana admin create-token --name local -data-dir /tmp/cv006)"
curl -s -H "Authorization: Bearer $TOKEN" localhost:7070/v1/tokens        # lists "local", state active
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"ci-github"}' localhost:7070/v1/tokens                          # 201, token shown once
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"ci-github"}' localhost:7070/v1/tokens                          # 409 token_name_taken
go run ./cmd/caravana admin revoke-token --name local -data-dir /tmp/cv006
curl -si -H "Authorization: Bearer $TOKEN" localhost:7070/v1/tokens | head -1  # 401
```

On `/account`, the revoked token shows as revoked ■; creating one there shows it once.

## 6. Cross-site requests are refused

With a signed-in browser, a form on another origin that posts to
`http://localhost:7070/account/tokens` gets the 403 page and creates nothing. The same request
with a Bearer token from `curl` works (no browser headers, no cookie).

## 7. No secrets in the log

Search the server's output for the password you typed, `$TOKEN`, and the session cookie value
from the browser's dev tools: none of them appear. The log has `signin`, `signin_failed`,
`signin_limited`, `token_created` and `token_revoked` events with names and addresses only.

## 8. Lab and VM gate

```bash
deploy/dev/lab.sh up && eval "$(deploy/dev/lab.sh env)"
CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 20m     # creates its own admin and token

deploy/vm/host.sh up
deploy/vm/host.sh admin init --email you@example.com                  # once per host
deploy/vm/host.sh prove                                               # ends GATE PASSED; its token is revoked after
```

Expected: both pass, starting with the new `api-needs-token` step.
