# Feature 050: Build from a Git repository on the host

CodeAlta task `c9fd` ("21 · Build from a Git repository on the host", raised by the maintainer on 2026-11-12).
Branch: the worktree branch `alta/solid-zagros-wbqx`. Plan: `plan.md`; lab facts: `research.md`; API: `contracts/api.md`.

## Why

Today an image must already exist: CI builds it and caravana-action deploys it (020). Someone without a CI
pipeline, or with a repository that only has a Dockerfile, can't deploy from source. `docs/plan.md` has "Build on a
build server" in Phase 3 and the handoff draws it (`NewAppScreen`'s Git repository source, `DeployScreen`'s Build
stage and log). This feature builds the image from a Git repository, as a Nomad batch job, and then deploys it like
any image: pinned by digest, health checked, reverted on failure.

## Words

- **Built service**: a service with a `build` source. Its `image` is the repository builds push to (no tag).
- **Build**: one run of the build job for one deployment: clone the branch, build the Dockerfile, push the image.
- **Git access**: how the build reads the repository: public, a deploy key Caravana makes, or a token the owner
  gives. The private key and the token live only in a Nomad Variable.

## User stories

### US1 (P1): deploy a repository that has a Dockerfile

The owner gives a repository URL, a branch, an optional folder and Dockerfile, and the image repository to push to.
A deploy builds the newest commit of the branch on the host, pushes the image, and deploys it by digest. The
deployment's history entry shows the Build stage, the commit, the branch and the author, and its build log.

- A deploy of a built service answers at once (202, status `pending`, the build `queued`). The build runs, then the
  deploy goes on as usual. The origin of the deployment is the commit, branch, author and repository (020's origin).
- The build log is kept with the deployment (its last 64 KiB) and can be read while the build runs.
- Rollback to a past version and promotion deploy that version's digest again: no build.

### US2 (P1): a push builds and deploys the new commit

The app's webhook (004) is the repository's push webhook. A push to the app's branch builds and deploys the pushed
commit. A push to another branch, a branch deletion or a ping is answered 200 and builds nothing; a push of the commit
already live builds nothing. GitHub, GitLab, Gitea and Forgejo push bodies are understood; any other body (or none)
builds the branch's newest commit.

### US3 (P1): a failed build fails the deployment and leaves the running version alone

A build that fails (clone, Dockerfile, push, timeout) fails its deployment with the build's last lines in the
deployment's error and a notification like any failed deploy. Nothing is registered with Nomad, so the running version
keeps running.

### US4 (P1): a private repository with a deploy key

The owner asks Caravana for a deploy key for the service. Caravana makes an ed25519 key pair, keeps the private key in
a Nomad Variable and shows the public key (and its fingerprint) to add to the repository's deploy keys. The build
clones over SSH with it, checking the host's key: github.com and gitlab.com are known, any other host needs its key
in the build source. The private key appears in no page, answer, log, job, export or SQLite row. A token for HTTPS
(GitHub, GitLab, Gitea) works the same way and is never shown again.

### US5 (P2): builds have limits and stay off the ingress

Each build has a memory and CPU reservation, a timeout and a build cache per app (on a host volume, cleared when it
grows past its size); a newer deploy of the app cancels its build in progress; at most N builds run at once in the
installation (1 by default) and the others wait queued. When another server exists, builds never run on the server
that holds the proxy (031). The owner changes the limits in Settings › Builds.

### US6 (P2): the console

New app has a "Build from Git" tab; Configure shows the build source and the Git access (the public key, New key,
the token); each history entry of a built deploy shows the Build stage and links to its build log page.

## Refusals (each names its field)

- `services[i].build.repository`: not `https://`, `ssh://` or `user@host:path` (plain `http://` only for a loopback
  host); credentials in the URL; a query or fragment.
- `services[i].build.branch`, `.path`, `.dockerfile`: not a plain relative name (no `..`, no leading `/`).
- `services[i].build.access`: `deploy-key` with an HTTPS URL, `token` with an SSH URL; no stored key or token at deploy
  time (409 `git_access_missing`).
- `services[i].build.hostKey`: missing for an SSH host Caravana doesn't know (422 `git_host_key_unknown`), or not a
  known_hosts key.
- `services[i].image`: a built service's image with a tag or digest; no registry entry (017/032) for it, unless the
  host is loopback (422 `build_registry_missing`).
- `services[i].build`: a second built service in one app.
- `services[i].volumes[j].name`: an app volume named `caravana-…` (the prefix is Caravana's).

## Out of scope

Compose `build:` (still refused), more than one built service per app, polling a repository, build arguments and
build secrets, multi-architecture images, a registry run by Caravana (builds push to a registry entry from 017), Git
providers as installation-wide accounts, Settings › Git providers, buildpacks (no Dockerfile), a designer pass on
the new pages.
