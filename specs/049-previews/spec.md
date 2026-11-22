# Feature 049: Previews from CI-built images, one environment per pull request

Task `rg2u`. The handoff's Previews (`PreviewsScreen.jsx`): every pull request gets its own copy of the apps it
changes, at its own address, removed when the pull request closes.

## Why

CI already builds every image (the blog, Umbral, Frunchy), so it can build a pull request's image the same way, and
Caravana only runs it. No build on the host is needed. What was missing (2026-10-28): a deploy token can't open or
close an environment, nor delete its app, and caravana-action has no preview mode. A full API token in CI is what
deploy tokens exist to avoid.

## Words

- A **preview environment** is an environment named `pr-<n>`, where `<n>` is a pull request number (1 to 999999999,
  no leading zero). The prefix `pr-` is reserved for them: an environment name that starts with `pr-` and isn't
  `pr-<n>` is refused.
- A **preview** is a preview environment with the apps in it.

## User stories

### US1 (P1): CI opens a preview with a deploy token

The deploy token of `blog`/`web` creates `pr-12` in project `blog`, deploys `web` there with the pull request's
image, waits until it is healthy, and the pull request gets one comment with the address. A later push deploys the
new image into the same preview and edits the comment.

- A deploy token may create an environment of its own project only when the name is `pr-<n>`. Any other name, or
  another project, is 403 `token_scope`.
- In `pr-<n>` it may create, read and deploy its own app (it already could in any environment of its project).

### US2 (P1): CI closes the preview

When the pull request closes, the deploy token deletes its app in `pr-12`, then the environment. If another app of
the project is still in `pr-12` (a second repository's preview), the environment stays (409 `not_empty`), and that
app's own close deletes it.

- A deploy token may delete its own app only when the app is in a preview environment. Its app in `staging` or
  `production` is 403 `token_scope`.
- A deploy token may delete a preview environment of its own project (empty, as for any token).

### US3 (P1): a missed close still cleans up

A preview expires a number of days (`expireAfterDays`, default 7, 1 to 30, per project) after the later of its
creation and the newest deployment of any app in it. Caravana checks every 5 minutes and closes expired previews:
every app in it is deleted as `DeleteApp` does (job, helper jobs, volumes, secrets), then the environment. A failure
is logged and tried again at the next check. Every push extends the preview, so an active pull request never
expires; an abandoned one goes after a week.

### US4 (P1): a busy repository can't fill the host

Each project has a limit of open previews (`limit`, default 5, 0 to 20; 0 means no previews). Creating one more
preview environment, with any token, is 409 `preview_limit` on `name`, naming the open ones: "project blog has 5
open previews (pr-3, pr-4, pr-7, pr-9, pr-12), its limit; close one or raise the limit". The count and the insert
are one transaction.

### US5 (P1): previews are private

A preview is unreleased code on a public address. The owner sets one preview password per project (12 to 128
printable characters). While it is set, every domain of every app deployed into a preview environment of the
project is Team only (user `team`) with that password: the deploy turns `teamOnly` on for each domain that didn't
have it, with one warning per app naming the first such field ("domains of preview environments are Team only
with project blog's preview password"). No password is made or shown for these domains (no `teamPasswords` in the
answer).

- Changing the password takes effect for every open preview at once, with no deploy.
- Setting it the first time protects each open preview at its next deploy (the answer says which are open).
- Removing it is refused (409 `previews_open`, field `removePassword`) while the project has an open preview:
  their routers name the password's middleware.
- Without a password, previews are public; the settings answer says so (`passwordSet: false`).

### US6 (P2): the owner sees and manages previews

`GET /v1/projects/{project}/previews` lists the settings and the open previews: environment, pull request number,
created, expires, and per app its status, addresses, reserved memory and its newest deployment (status, when, who,
branch and commit from its origin). A full token or the console may close a preview whole (every app, then the
environment). The project page shows the settings form and marks preview environments with their expiry; the
handoff's Previews page is a later designer-cycle feature.

### US7 (P1): caravana-action has a preview mode

`preview: true` on a `pull_request` event (types `opened`, `synchronize`, `reopened`, `closed`). See
`contracts/action.md`.

## Refusals

| Request | Answer |
| --- | --- |
| an environment name `pr-…` that isn't `pr-<n>` | 422 `invalid_field` on `name` |
| a preview beyond the limit | 409 `preview_limit` on `name` |
| a deploy token: another project, a non-preview name, deleting its app outside a preview, deleting a non-preview environment | 403 `token_scope` |
| `limit` outside 0–20, `expireAfterDays` outside 1–30, a password outside 12–128 printable characters | 422 `invalid_field` naming it |
| `removePassword` while previews are open | 409 `previews_open` on `removePassword` |
| closing an environment that isn't a preview | 409 `not_a_preview` on `environment` |

## Out of scope

Sleeping previews (a proxy that holds a request while the app starts), copying staging's database into a preview
(a preview with Postgres starts empty), sign-in through OpenID Connect, DNS records per preview (task "Let Caravana
create and remove DNS records through a provider key", feature 048), notifications and needs-you items about
previews beyond what any app gets, the handoff's Previews page.
