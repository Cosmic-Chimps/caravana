# Building from a Git repository

A repository with a Dockerfile deploys without CI. Caravana clones a branch, builds the Dockerfile on one of your
servers, pushes the image to a registry you have given it credentials for, and deploys that image by its digest like
any other: health checked, reverted when it fails. Rollback and promotion deploy a past version's digest again and
never build.

If your CI already builds the image, keep it: caravana-action deploys that image through the API. Building on the
server is for the repository that has only a Dockerfile.

## Make an app that builds

In the console: **New app › Build from Git**. Give the repository, the branch, an optional folder and Dockerfile, how
the repository is read, and **the image to push to**. Through the API, a service has a `build` source and its `image`
is the repository builds push to:

```json
{
  "id": "shop",
  "source": {"kind": "image"},
  "services": [{
    "name": "web",
    "image": "ghcr.io/acme/shop",
    "ports": [{"name": "http", "containerPort": 8080}],
    "domains": [{"host": "shop.example.com", "port": "http"}],
    "health": {"path": "/healthz"},
    "build": {
      "repository": "https://github.com/acme/shop.git",
      "branch": "main",
      "path": ".",
      "dockerfile": "Dockerfile",
      "access": "public"
    }
  }]
}
```

- `repository`: `https://host/path`, `ssh://git@host/path` or `git@host:path`. It holds no credentials, query or
  fragment; a plain `http://` address is accepted only for a host on the server itself.
- `branch` (default `main`), `path` (the build context, default the root) and `dockerfile` (relative to `path`,
  default `Dockerfile`) are plain relative names.
- `access`: `public` (default), `deploy-key` or `token`, below.
- The `image` has **no tag or digest**. Each build pushes `<image>:git-<first 12 of the commit>` and deploys the digest
  the push reported.
- One service of an app may be built. Compose's `build:` is still refused, naming the field.

A deploy of a built app answers at once (202, status `pending`) and the build runs in the background; deploy the app
again, or call its webhook, and it builds the branch's newest commit. **Create, then add access** in New app saves the
app without deploying it, for a repository that needs a key or a token first.

## Where the image goes

Builds push with the credentials of the registry entry that covers the image (Settings › Registries; an entry may be a
host or `host/path`, and a path entry wins over the host). The credentials must be allowed to push, not only to pull.

Without an entry for it, a deploy is refused naming `services[i].image` (`build_registry_missing`), before anything is
saved. A registry on the server itself (a loopback address) needs no entry: builds push to it over plain HTTP.

The image Caravana deploys is `<image>@sha256:…`, so the registry has to keep it: deploys, rollbacks and the
copies' restarts pull it from there.

## How the repository is read

| `access` | Address | What Caravana stores |
| --- | --- | --- |
| `public` | `https://` | nothing |
| `deploy-key` | `ssh://` or `git@host:path` | a private key it made, in a Nomad Variable |
| `token` | `https://` | a token you gave, in a Nomad Variable |

**Deploy key.** Under Configure › Source › Git access (or Next steps after New app), **Make a deploy key**. Caravana
makes an ed25519 key pair, keeps the private half and shows the public key and its fingerprint: add the public key to
the repository's deploy keys, with read-only access. **New key…** replaces it; the old key stops working at once.

**Token.** **Add a token…** takes a token the repository accepts over HTTPS (a GitHub fine-grained token with read access
to the repository's contents, a GitLab project access token with `read_repository`, a Gitea token) and, optionally, a
user name (`x-access-token`, which GitHub, GitLab and Gitea accept, when left empty). It is never shown again.

**Remove access** deletes the stored key or token. The private key and the token are in no page, answer, log line, job,
export or database row. The build log is masked of the token's value.

A deploy whose `access` names a key or token that isn't stored is refused (`409 git_access_missing` at
`services[i].build.access`).

### Host keys

Builds check the SSH host's key (`StrictHostKeyChecking=yes`). The keys of `github.com` and `gitlab.com` are built in. Any
other SSH host needs its key in `build.hostKey`, as `"<type> <base64>"` without the host's name:

```sh
ssh-keyscan -t ed25519 git.example.com | cut -d' ' -f2-
```

Without it a deploy is refused (`422 git_host_key_unknown` at `services[i].build.hostKey`). Configure › Source's Build panel has
the field. A host on a port other than 22 always needs its key.

## Build on every push

The app's webhook is the repository's push webhook. Create the app's webhook token (Next steps or Configure › Integrations › Webhook),
which shows the webhook address once: `https://console.example.com/v1/webhooks/shop?token=cvwh_…`.

| Provider | Where | Settings |
| --- | --- | --- |
| GitHub | Repository › Settings › Webhooks › Add webhook | Payload URL: the address. Content type: `application/json`. Events: **just the push event**. |
| GitLab | Project › Settings › Webhooks | URL: the address without `?token=…`. Secret token: the token (`cvwh_…`). Trigger: **Push events**, on the branch. |
| Gitea, Forgejo | Repository › Settings › Webhooks › Add webhook › Gitea (or Forgejo) | Target URL: the address. Content type: `application/json`. Trigger on: **Push events**. |

What a call does for an app that builds:

- a push to the app's branch builds and deploys the branch's newest commit (the build clones the branch when it
  starts, so a push that arrives while another builds is built by the next one);
- a push to another branch, a branch deletion and a ping answer `200` with `changed: false` and `skipped` saying why,
  and build nothing;
- a push of the commit that is already live builds nothing;
- any other body, or none, builds the branch's newest commit.

A newer deploy of the app **cancels the build in progress**: its deployment is `superseded`.

## What a build is

One Nomad batch job `<app>_build-<id>` with two tasks: **source** (`alpine/git`) clones the branch and checks the
Dockerfile is there, and **build** (rootless BuildKit) builds it and pushes. Both images are pinned by digest
(`CARAVANA_GIT_IMAGE` and `CARAVANA_BUILDER_IMAGE` change them) and are pulled by the first build: about 100 MB.

- Builds run on the bridge network, and never on the server that holds the proxy once there is another server.
- A build that is queued waits its turn: **Settings › Builds** sets how many run at once in the installation.
- Each app has a **build cache** on a host volume, `<app>_caravana-build`, emptied at the start of a build when it has
  grown past its size. It is deleted with the app.
- The log is the last 64 KiB of the two tasks' output, masked, kept for the app's newest 50 builds. The Deployments tab
  shows a Build stage per deployment and links to **Build log**; the page follows a build that is going.

Settings › Builds (and `GET`/`PUT /v1/builds/settings`):

| Limit | Default | Range |
| --- | --- | --- |
| Memory per build | 2048 MB | 512 – 16384 |
| CPU per build | 2000 MHz | 500 – 32000 |
| Timeout | 30 minutes | 5 – 180 |
| Builds at once | 1 | 1 – 4 |
| Build cache | 5120 MB | 0 – 102400 (0: no cache) |

A change reaches the next build; one going keeps the limits it started with. A build uses memory that the apps on a
small server may need: leave it room, or keep the default of one build at a time.

## When a build fails

A failed build fails its deployment: nothing is registered with Nomad, so the running version keeps running, and a
`deploy.failed` notification goes out like any failed deploy. The deployment's error is `build failed: <reason>`, then
the last 20 lines of the log; the Build log page has all of it. The reasons:

| Reason | What it means |
| --- | --- |
| the repository could not be read | the clone failed: the address, the branch, the key or token, or the host's key |
| no Dockerfile at `<path>` / no folder in the repository at `<path>` | `build.dockerfile` or `build.path` isn't in the branch |
| the build failed | BuildKit failed: read the log |
| the image could not be pushed | the registry refused: the entry's credentials, or the image's path |
| the build ran out of memory | raise the memory in Settings › Builds |
| the build took longer than 30 minutes | the timeout; raise it, or add a cache |
| the build job could not start / was lost | Nomad couldn't run it, or lost it |
| Caravana restarted during the build | a build doesn't outlive the process that ran it; deploy again |

## What it doesn't do

- Compose's `build:` and more than one built service in an app.
- Polling a repository: a push webhook, or a deploy, starts a build.
- Build arguments and build secrets, multi-architecture images, buildpacks (no Dockerfile).
- Running a registry for the images: builds push to one you already use.
- Git providers as installation-wide accounts (Settings › Git providers): access is per service.
- Builds on a separate build pool of servers: until then they run on any server but the proxy's, when there is one.
