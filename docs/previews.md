# Previews: a copy of your app for every pull request

A **preview** is an environment named `pr-<number>` with its own copy of the app, at its own address, made when a
pull request opens and removed when it closes. CI builds the image as it always does; Caravana only runs it. No build
happens on your server.

A preview is an ordinary environment of the project: its apps deploy, log, back up and monitor like any other. The
name is the only thing that makes it a preview. The prefix `pr-` is kept for them: an environment called `pr-` and
anything but a pull request number (`pr-12`, not `pr-012` or `pr-fix`) is refused.

## The workflow

Add a job to the repository's pull request workflow that builds the image and runs
[caravana-action](https://github.com/Cosmic-Chimps/caravana-action) with `preview: true`:

```yaml
on:
  pull_request:
    types: [opened, synchronize, reopened, closed]

permissions:
  contents: read
  packages: write
  pull-requests: write   # for the comment

jobs:
  preview:
    runs-on: ubuntu-latest
    steps:
      # ... on opened, synchronize and reopened: build and push the image, as for any deploy ...
      - uses: Cosmic-Chimps/caravana-action@v1
        with:
          url: https://caravana.example.com
          token: ${{ secrets.CARAVANA_TOKEN }}   # a deploy token for blog/web
          preview: true
          project: blog
          name: web
          image: ghcr.io/${{ github.repository }}@${{ steps.build.outputs.digest }}   # not needed on closed
          preview-domain: pr-{pr}-blog.example.com
          preview-secrets: |
            DB_PASSWORD=${{ secrets.PREVIEW_DB_PASSWORD }}
```

For pull request 12 the action:

- **opened, synchronize, reopened**: opens the environment `pr-12` in the project, copies the app of the source
  environment (`staging` by default, `preview-from`) into it with the pull request's image and the preview's domain,
  deploys it and waits until it is healthy. A later push deploys the new image into the same preview.
- **closed**: deletes the preview's app, then the environment. If another app of the project still has a preview in
  `pr-12` (a second repository), the environment stays and that app's own close removes it.
- keeps one comment on the pull request with the address, edited on every push.

The action's README has every input. All it needs is a **deploy token** for the app (Settings › API tokens, "Only for
one app"): Caravana lets that token open and close preview environments of its own project and delete its own app
there, and nothing else. It can't touch `staging` or `production`, or another project.

A pull request from a fork gets no preview: GitHub gives such runs no secrets, so there is no token. The action ends
with a notice and succeeds.

### Secrets

A preview copies the app's definition but never its secrets, since a preview must not read staging's. Give each
secret a value in `preview-secrets`, one `NAME=value` per line; the action stops before creating anything when one is
missing. Use values meant for previews, not production's.

### Databases

A service with a database or any volume starts **empty** in a preview. Nothing is copied from staging. A copy never
carries its backup policy or published ports either, so a preview doesn't write to your backup bucket or take a fixed
port number.

## DNS and certificates

Every preview has its own host name (`pr-12-blog.example.com`), so DNS needs to resolve all of them. Set **one
wildcard record by hand**, pointing at the server like your other records:

```
*.example.com.    A    203.0.113.10
```

- Name previews **one level under the zone**, `pr-12-blog.example.com`, not `pr-12.blog.example.com`. A wildcard
  covers one label, and Cloudflare's free certificate (for a proxied record) covers `*.example.com` only.
- The record may be proxied (Cloudflare) or not. Caravana's certificate for each host comes from Let's Encrypt either
  way.
- Let's Encrypt allows **50 new certificates a week per registered domain**. Each new preview address is a new
  certificate, so a repository with a hundred pull requests a week can run into it. Raising the project's limit of open
  previews doesn't help; reusing a host name does (the same pull request gets the same address every time).
- Caravana doesn't create or remove DNS records for previews. A provider key that does is a separate feature.

## Secrets in a preview

A preview is defined by CI's deploy token, so it is deploy-level work. Typed secret values (the action's
`preview-secrets`) work as for any app. A secret that is a reference to a secret provider (`from`, or a template's
refs) is allowed only if the app of the same name already has that same provider and path in one of the project's
other (non-preview) environments, which is what a copy of the base app gives. A deploy-level person or token that adds
another reference is refused with `403 not_allowed`, naming the reference; maintain on the project may add one. See
[people and roles](people.md#what-a-role-does-not-protect).

## The password

A preview is unreleased code on a public address. Set **one preview password per project** and every domain of every
app deployed into a preview of that project becomes Team only: the browser asks for the user `team` and this password.

Set it on the Previews page (the "setup per project" tab), or through the API:

```http
PUT /v1/projects/blog/previews/settings
{"password": "a long phrase, 12 to 72 characters"}
```

- The password is 12 to 128 printable characters and at most 72 bytes (the limit of the hash that guards the address).
- Changing it takes effect for every open preview **at once**, with no deploy.
- Setting it for the first time protects each open preview at its **next deploy**; the answer names the open ones.
- Removing it is refused while a preview is open, since the open previews' routers name it. Close them first.
- A preview's domain has no password of its own: **New password** on a preview domain is refused and points at the
  project's setting.
- Without a password the previews are public, and the settings say so. The password is stored only as a bcrypt hash,
  and no page, answer or log line holds it.

## The limit and the expiry

Two settings per project, on the same page:

- **Open previews at most** (default 5, 0 to 20; 0 means no previews). One more is refused with `preview_limit`,
  naming the open ones. CI fails with that message until one closes.
- **Close a preview after (days)** (default 7, 1 to 30), counted from the later of the preview's creation and its
  newest deploy of any app in it. Caravana checks every five minutes and closes an expired preview the way Close preview
  does. A pull request that keeps getting pushes stays open; an abandoned one goes after a week, even if CI never
  sent the close.

## Seeing and closing previews

The **Previews** page in the sidebar lists the open previews of every project, problems first: the pull request's
number, the branch when the deploy said one, who pushed it, its apps and address, the memory it reserves and when it
closes if nothing is deployed to it. Its "setup per project" tab holds the limit, the days and the password. The
project's settings page lists each preview environment with "preview · expires <time>" too. **Close…** (on either) asks
first, then deletes every app in the preview (copies, jobs, volumes, secrets) and the environment.
Through the API, `GET /v1/projects/{project}/previews` lists them with their apps, status, addresses, reserved memory
and newest deploy, and `DELETE /v1/projects/{project}/previews/{environment}` closes one (full tokens only).
