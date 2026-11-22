# Contract: caravana-action's preview mode

Repository `Cosmic-Chimps/caravana-action`, cloned beside this one at `../caravana-action`. The existing modes are
unchanged; `preview` defaults to `"false"`.

## Inputs (new)

| Input | Env var | Meaning |
| --- | --- | --- |
| `preview` | `CARAVANA_PREVIEW` | `"true"` turns the mode on. |
| `preview-from` | `CARAVANA_PREVIEW_FROM` | The environment whose app is copied (default `staging`). |
| `preview-domain` | `CARAVANA_PREVIEW_DOMAIN` | The preview's host, with `{pr}` for the number: `pr-{pr}-blog.lacalabaza.xyz`. Empty: the preview has no domain. |
| `preview-secrets` | `CARAVANA_PREVIEW_SECRETS` | Lines `NAME=value`: values for the copied app's plain secrets (a preview can't read staging's). Each value is masked. |
| `github-token` | `CARAVANA_GITHUB_TOKEN` | For the pull request comment; default `${{ github.token }}`. Empty: no comment, a notice says so. |

With `preview: true` the inputs `project` and `name` are required, `app`, `environment` and `promote` are refused
(by name), `image` is required except on `closed`, `service` says which service gets the image and the domain
when the app has several (as today).

## What it reads

`GITHUB_EVENT_NAME` must be `pull_request` or `pull_request_target` (else a die naming it). From
`GITHUB_EVENT_PATH`: `.pull_request.number` (else `.number`), `.action`, `.pull_request.head.repo.fork`, and
`GITHUB_REPOSITORY` for the comment.

- An empty token on a pull request from a fork: a notice ("pull requests from forks get no preview: GitHub gives
  them no secrets") and exit 0.
- Actions other than `opened`, `synchronize`, `reopened`, `closed`: a notice and exit 0.

The preview environment is `pr-<n>`; the app ID is `<project>--pr-<n>--<name>`.

## opened, synchronize, reopened

1. `POST /v1/projects/{project}/environments` `{"name":"pr-<n>"}`: 201, or 409 `environment_exists` (fine). 409
   `preview_limit` dies with Caravana's message.
2. Read the preview app. If it exists and is defined: the existing `replace-image` flow (If-Match, one retry), with
   the domain rewritten again only if it differs. Otherwise read the source app
   `<project>--<preview-from>--<name>` (404 dies: "app web has no staging copy to make a preview from") and build
   the definition from its `.app`:
   - the image on the target service (`set_image`);
   - domains: with `preview-domain`, the target service (the one service with domains, or `service`) gets
     `[{"host": <rendered>, "port": <its first domain's port, else its first port's name>}]` and every other
     service's domains are removed; without it, every domain is removed;
   - `backup` removed from every service (a preview never backs up), `published` removed from every port, `id`
     set to the preview's ID;
   - plain secrets (no `value`, no `from`) take their value from `preview-secrets`; a name with no value there dies
     before any call, naming it and the input; a line for a name the app doesn't have is a warning;
   - then `POST /v1/apps/{id}/deployments` with the definition (the `define` path).
3. Wait as today (`wait` must be true in preview mode). Healthy: outputs and the comment. Failed: the comment
   says so, and the step fails.
4. The comment: one per app, found by the marker `<!-- caravana-preview:<app id> -->` among the pull request's
   comments (`GET /repos/{repo}/issues/{n}/comments?per_page=100`, following `Link: rel="next"`), then edited
   (`PATCH /repos/{repo}/issues/comments/{id}`) or created (`POST /repos/{repo}/issues/{n}/comments`). Its body:
   a heading line, the address as a link (`https://<host>`), the status, the commit (`GITHUB_SHA`, 12 characters)
   and, when a domain was given, "If Caravana asks for a password, sign in as `team` with your project's preview
   password." A refused comment call is a warning, not a failure. The GitHub token goes to curl on stdin, as the
   Caravana token does.

## closed

1. `DELETE /v1/apps/{id}`: 204 or 404 (fine).
2. `DELETE /v1/projects/{project}/environments/pr-<n>`: 204, 404 (fine), or 409 `not_empty` (a notice: another app
   of the project still has a preview there).
3. The comment is edited to "Preview closed and removed." (when it exists).

## Outputs (new)

`preview-environment` (`pr-<n>`), `preview-url` (`https://<host>`, empty without a domain), `mode` = `preview`
or `preview-closed`. The summary has a row for the preview's address.

## Tests

`test/run.sh` against `test/fake_caravana.py`, extended with the environment and delete routes and a fake GitHub
comments API (the script's GitHub API base is `GITHUB_API_URL`, default `https://api.github.com`, so the test
points it at the fake): opened creates the environment and deploys the rewritten definition (assert the body:
image, domain, no backup, no published, secret value from the input); synchronize replaces the image and edits the
one comment; closed deletes app and environment and edits the comment; `preview_limit` fails with the message; a
missing preview secret dies before any call; a fork without a token exits 0; no token is printed (search the
output for both tokens).
