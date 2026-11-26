# The HTTP API

Everything the console does is also in the HTTP API, and the console is one of its clients. Scripts, CI
jobs, the [GitHub Action](../deploy/ci.md), the .NET Aspire publisher and [coding agents](../../mcp.md)
use it too. This page is how to use it: authentication, errors, conditional deploys and following a
deployment. The complete description of every operation, request and answer is the OpenAPI file,
[api/openapi.yaml](../../../api/openapi.yaml).

The API's version is still 0.x. Clients must accept error codes they do not know.

## Base URL

The API is on the console's own address, under `/v1`:

```text
https://caravana.example.com/v1
```

On the host, it also answers on `http://127.0.0.1:7070/v1`. Request bodies are JSON, sent with
`Content-Type: application/json` (parameters such as `charset=UTF-8` are fine). Unknown fields in a body
are refused, not ignored. The API never answers with a redirect. Durations, such as a health check's
`interval`, are strings like `"10s"`.

Only three operations need no API token: `GET /v1/health`, the webhook `POST /v1/webhooks/{id}`
(which has its own token) and accepting an invitation. Two more groups are outside the token rules: the
calls a server's own agent makes (`/v1/join` and `/v1/node/…`) carry their own credentials, a join token
or a node token, and the two discovery documents under `/.well-known` (the cluster's OpenID
configuration and its signing keys) are public.

```bash
curl -s https://caravana.example.com/v1/health
```

```json
{"status":"ok","version":"0.x.y"}
```

## API tokens

Send a token in the `Authorization` header:

```bash
export CARAVANA_URL=https://caravana.example.com
export CARAVANA_TOKEN=cvat_…
curl -s -H "Authorization: Bearer $CARAVANA_TOKEN" "$CARAVANA_URL/v1/apps"
```

A token belongs to a person and acts with that person's rights, never more. A token is shown once, when
it is made, and Caravana keeps only a hash of it. Make one:

- in the console, in **Settings › API tokens**, with **New token**: a name, and optionally an expiry date;
- on the host, with `sudo caravana admin create-token --name ci` ([The caravana command](cli.md));
- with the API itself, `POST /v1/tokens`:

```bash
curl -s -X POST -H "Authorization: Bearer $CARAVANA_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"ci-github"}' "$CARAVANA_URL/v1/tokens"
```

The answer includes the token. Revoke one with `DELETE /v1/tokens/{tokenId}`, or **Revoke** in
**Settings › API tokens**.

A request without valid credentials gets `401` with code `unauthorized`, the same for every reason:
missing, malformed, wrong, expired or revoked. Removing a person ends their tokens at once.

A browser signed in to the console can use its session cookie instead of a token. Then every request
that changes something must come from the console's own pages, or it gets `403` `cross_origin`. Tokens
never get that error.

## Deploy tokens

A deploy token is an API token limited to one app, for CI. Make it in **Settings › API tokens** by
filling **Only for one app** (a project and an app name; the app does not have to exist yet), on the
app's **Configure › Integrations** page with **Create a deploy token for this app**, or by sending a `scope` to
`POST /v1/tokens`:

```bash
curl -s -X POST -H "Authorization: Bearer $CARAVANA_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"web-ci","scope":{"project":"shop","app":"web"}}' "$CARAVANA_URL/v1/tokens"
```

That token can deploy the app named `web` of project `shop`, in any environment of that project. It can
read the app, its Compose file and its deployments, create the app, deploy it from a body or a Compose
file, preview and make a promotion, and refresh its secrets. It can also create and delete the
preview environments (`pr-<n>`) of its project, and delete its own app inside one. Everything else, and everything about another app, is refused before any work with `403`
`token_scope`, and the message says what the token can do. [Deploy from CI](../deploy/ci.md) shows it in a
workflow, and [Previews](../../previews.md) covers the preview environments.

## Errors

Every error is JSON with the same shape and the matching HTTP status, including `404` for an unknown
path and `405` for a wrong method:

```json
{"error": {"code": "invalid_field", "message": "must be at least 10 MB, got 5", "field": "services[0].resources.memoryMB"}}
```

`code` is stable and meant for programs. `message` is for people and is not meant to be parsed.
`field` names the one request field at fault, as a path such as `services[0].domains[1].port`, and is
absent when no single field is.

| Status | Codes you will see most | Meaning |
| --- | --- | --- |
| 400 | `bad_request`, `invalid_field`, `unknown_field` | The request is invalid. Nothing was saved. Deploy bodies get 400 for a bad field. |
| 401 | `unauthorized` | No valid token or session. |
| 403 | `cross_origin`, `not_allowed`, `token_scope` | A cookie request from another site; a role that does not allow this (the message names the role and the one needed); a deploy token used outside its app. |
| 404 | `not_found`, `project_not_found`, `environment_not_found` | No such thing, or one you may not see. |
| 409 | `app_exists`, `job_conflict`, `app_stopped`, `app_busy`, `domain_taken` | The current state does not allow it. |
| 412 | `app_changed` | `If-Match` was sent and the app changed since you read it. |
| 413, 415 | `body_too_large`, `unsupported_media_type` | The body is too big, or not JSON. |
| 422 | `image_not_found`, `registry_auth_failed`, `invalid_field` | The deploy could not start: the image does not exist or the registry refused the credentials. Settings and query parameters, and the `Caravana-Origin-*` headers, get `invalid_field` with 422 instead of 400. |
| 502 | `registry_unavailable`, `nomad_unavailable`, `nomad_rejected` | A registry or Nomad could not be used. |

Most codes are listed in the description of the `Error` schema in the OpenAPI file; each operation's
description names the ones it adds.

An error raised after a deployment record exists also carries a `Location` header that points at the
record.

## List apps

```bash
curl -s -H "Authorization: Bearer $CARAVANA_TOKEN" "$CARAVANA_URL/v1/apps" |
  jq '.apps[] | {id: .app.id, state: .status.state}'
```

```json
{"id": "shop--production--web", "state": "healthy"}
```

An app's ID says where it is. The bare name is an app in project `default`, environment `production`;
anywhere else it is `<project>--<environment>--<name>`, so `shop--staging--web` is `web` in environment
`staging` of project `shop`. A client computes the ID from the three names. `state` is one of
`rolling_out`, `healthy`, `degraded`, `not_running`, `unknown` and `stopped`.

## Deploy an image

`POST /v1/apps/{id}/deployments` saves an app and deploys it, or, with an empty body, deploys the stored
app again. The simplest change is a new image: read the app, replace the image, send it back. Sending
the revision you read as `If-Match` makes the deploy safe against someone else changing the app in
between.

```bash
id=shop--staging--web
auth="Authorization: Bearer $CARAVANA_TOKEN"

# 1. Read the app. Its revision is in the body and in the ETag header.
curl -s -H "$auth" "$CARAVANA_URL/v1/apps/$id" > app.json
revision=$(jq -r .revision app.json)

# 2. Put the new image in the app and deploy it, only if the app is still as read.
jq '.app | .services[0].image = "ghcr.io/acme/web:1.5"' app.json > body.json
curl -s -D headers.txt -X POST \
  -H "$auth" -H "Content-Type: application/json" \
  -H "If-Match: \"$revision\"" \
  --data @body.json "$CARAVANA_URL/v1/apps/$id/deployments"
```

The revision is a short name for the exact stored app. `getApp` sends it as the `ETag` header, in quotes,
and `If-Match` takes it back in quotes: send `"0123456789abcdef"`, not `0123456789abcdef`. If the app was
saved with a change since you read it, the answer is `412` with code `app_changed`, nothing is saved or
deployed, and the message names the current revision: read the app again and decide. A deploy without a
body ignores `If-Match`, since it changes no definition. `deployCompose` takes `If-Match` too.

## Follow a deployment

A deploy answers `202` as soon as Nomad has accepted the job. The rollout continues in the background.
The answer is the deployment, usually in status `pending` or `running`, and the `Location` header is its address:

```text
HTTP/2 202
location: /v1/apps/shop--staging--web/deployments/k3v7q2x9m4n8p6r5t1w3y7z2ab
```

Read that address until the `status` is final: `healthy`, `failed`, `reverted` (it failed and Nomad put the
previous version back) or `superseded` (a newer deploy of the same app replaced it).

```bash
location=$(awk 'tolower($1) == "location:" {print $2}' headers.txt | tr -d '\r')
while :; do
  status=$(curl -s -H "$auth" "$CARAVANA_URL$location" | jq -r .status)
  echo "$status"
  case "$status" in healthy | failed | reverted | superseded) break ;; esac
  sleep 5
done
```

The deployment also has `digests` (each service's image as deployed, pinned by digest), `warnings`,
`error` when it failed, and `progress` while it is rolling out. When two deploys of one app overlap, the
one requested last wins and the others end `superseded`.

`GET /v1/apps/{id}/deployments` lists an app's deployments, newest first. `POST
/v1/apps/{id}/deployments/{deploymentId}/redeploy` deploys a past healthy version again, with its image
digests, and `GET /v1/apps/{id}/jobspec` exports the app as a plain Nomad job. A deploy of an app with
a service built from Git builds first, and answers `202` with the build queued.

## Where a deploy came from

A deploy can say where it came from, so the history shows it. `deployApp`, `deployCompose`,
`promoteApp` and the webhook accept these optional headers:

| Header | Meaning |
| --- | --- |
| `Caravana-Origin-Commit` | The commit, 7 to 64 hexadecimal digits. |
| `Caravana-Origin-Ref` | The branch or tag, such as `refs/heads/main`. |
| `Caravana-Origin-Actor` | Who pushed it. |
| `Caravana-Origin-Run` | The CI run's `https://` address. The console links it. |
| `Caravana-Origin-Repository` | The repository as `owner/name`. |

```bash
curl -s -X POST -H "$auth" \
  -H "Caravana-Origin-Commit: abc1234def5678" \
  -H "Caravana-Origin-Ref: refs/heads/main" \
  -H "Caravana-Origin-Run: https://github.com/acme/shop/actions/runs/212" \
  "$CARAVANA_URL/v1/apps/$id/deployments"
```

A header that breaks its rule, or is sent twice, is `422` `invalid_field` with `field` naming the header,
and nothing is deployed. The deployment's `origin` has the values, and the history shows "from CI". The
GitHub Action sets these headers for you.

## Webhooks

An app can have a webhook token, made on **Configure › Integrations**. `POST /v1/webhooks/{id}` with the token in the
`token` query parameter or as `Authorization: Bearer` redeploys the stored app when an image changed; otherwise
it answers `200` with `changed: false` and deploys nothing. A webhook
token and an API token never stand in for each other. A webhook-only listener can expose just this
route: see [Settings](settings.md).

## The full description

[api/openapi.yaml](../../../api/openapi.yaml) describes every operation, with its parameters,
request and answer schemas and error codes, and is what the clients are generated from. When you make a token in
**Settings › API tokens**, the page shows a ready `curl` line for it.

## Next

- [Deploy from CI](../deploy/ci.md): a workflow that uses a deploy token
- [MCP for coding agents](../../mcp.md)
- [How a deploy works, history and rollback](../deploy/rollback.md)
