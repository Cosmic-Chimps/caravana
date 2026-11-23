# Plan 050: Build from a Git repository on the host

Spec: `spec.md`. Task `c9fd`. Branch: the worktree branch `alta/solid-zagros-wbqx` (from `main` at 6d60142, where
everything up to 049 is merged). Numbers: API **0.56.0**, migration **0040**. Lab facts: `research.md`. API: `contracts/api.md`.

## Constitution check

- I Small by default: no new Go module (`golang.org/x/crypto/ssh` is in `golang.org/x/crypto`, already direct since
  023) and no host dependency beyond Docker and Nomad. The builder (`moby/buildkit` rootless) and the Git image
  (`alpine/git`) are pinned by digest and run as Nomad tasks, like 044's scanner.
- II One model: the Git source is `app.Service.Build`, part of the user's model; nothing after validation reads
  `Source`. The build job `<app>_build-<id>` and the cache volume `<app>_caravana-build` are new helpers, named here
  and in constitution 1.2.2 (PATCH, its own commit). `Translate` is unchanged for every app; `BuildJob` is a new pure
  function with goldens.
- III Safe deploys: the built image is deployed by the digest the push reported; no floating tag. A failed build
  never reaches `Register`. Rollback and promotion use recorded digests.
- IV Fail loudly: every refusal names its field (spec "Refusals").
- V API-first: every operation in `api/openapi.yaml` 0.56.0 in the same change; the console calls the same service
  methods. A deploy of a built service answers 202 with the deployment's ID and `Location` (V allows it).
- VI Nomad-native: builds are Nomad batch jobs through the official client. The build job's network is bridge, and
  host only when the repository's or the registry's host is loopback (the lab; 044 did the same for scans).
- VII Secure: the deploy key's private half, a token and the registry's push credentials live only in Nomad
  Variables (the app's `caravana/builds/<app>/<service>` and the job's own `nomad/jobs/<job>`), never in argv, the
  job's env, a page, an answer, a log line or SQLite. The build log is masked of the token's value. Builds run
  rootless; the docker driver's `allow_privileged` stays false.
- VIII Plain, tested Go: table tests for validation, the URL rule and the push-body rule; goldens for the job;
  script tests under a real `sh` with fakes; a lab gate step.

## Decisions

- **D1 The model.** `app.Service.Build *app.Build` (`build`, omitempty, so no revision moved):
  `Repository string` (`https://host/path[.git]`, `ssh://user@host[:port]/path`, `user@host:path`; `http://` only for
  a loopback host; at most 512 bytes; printable; no `user:pass@` in an `http(s)` URL, no query or fragment),
  `Branch` (default `main`; `^[A-Za-z0-9._/-]{1,255}$`, not starting with `-` or `/`, no `..`, no `//`, not ending in `/`
  or `.lock`), `Path` (default `.`; a relative folder, segments of `^[A-Za-z0-9._-]+$`, no `..`), `Dockerfile` (default
  `Dockerfile`, relative to `Path`, same rule), `Access` (`""`/`public`, `deploy-key`, `token`), `HostKey` (a known_hosts
  key `<type> <base64>`, optional; required for an SSH host other than github.com and gitlab.com). `app.Build.Defaults()`
  fills the defaults; `app.App.BuiltService() (int, bool)`. A built service's `Image` is the repository to push to and
  must have no tag or digest. One built service per app. Validation lives in `internal/app/build.go`
  (`validateBuild`), called from `Validate` after the service's image rule; the image rule takes the build into
  account. App volume names starting with `caravana-` are refused (the cache volume's name is Caravana's).
  Compose's `build:` stays refused; the overlay has no `build`. A promotion takes the source's `build` (`build` in
  `Changed`); Copy to keeps it. The HCL export is unchanged (the job has the digest).
- **D2 Where builds push.** To the service's `image` repository, as `<image>:git-<commit12>`, with the registry entry
  that covers it (017's host entries and 032's path entries, `registry.MatchEntry`); its credentials must be allowed
  to push. No entry and a non-loopback host is 422 `build_registry_missing` on `services[i].image`, checked in
  `saveAndDeploy` and `Redeploy` before anything is saved or recorded (`checkBuild`). A loopback registry without an
  entry is pushed to anonymously over plain HTTP (`registry.insecure=true`), like 017's pulls.
- **D3 Git access.** Per built service, in the Nomad Variable `caravana/builds/<app>/<service>` (`deploy.GitAccessPath`):
  items `kind` (`deploy-key` or `token`), `private_key` (OpenSSH PEM) and `public_key` (authorized_keys line, with the
  comment `caravana <app>/<service>`) for a key; `username` and `token` for a token. `Service.GitAccess(app, svc)`
  answers the kind, the public key, its SHA256 fingerprint, the username and when it was made, never the private key
  or the token. `NewDeployKey` makes an ed25519 key (`crypto/ed25519` + `ssh.MarshalPrivateKey`) and replaces any
  access; `SetGitToken(app, svc, username, token)` (username default `x-access-token`; token 1–4096 printable bytes);
  `RemoveGitAccess`. Deleting the app deletes the variable. A deploy whose `access` is `deploy-key` or `token` without
  the matching stored kind is 409 `git_access_missing` on `services[i].build.access`. Known hosts: github.com and
  gitlab.com are built in (`research.md` R2, fingerprints checked against the providers' published ones on
  2026-11-23); any other SSH host needs `hostKey` (422 `git_host_key_unknown` on `services[i].build.hostKey`).
- **D4 The build job** (`nomadjob/build.go`, pure, goldens `build-*`): `BuildJob(BuildSpec) *api.Job`, ID
  `<app>_build-<buildID>` (buildID 12 hex), type batch, no restart and no reschedule, one group `build`:
  - network `bridge`, or `host` (and `network_mode = "host"` in both tasks' docker config) when `HostNetwork`;
  - a constraint `${meta.caravana_ingress} != "true"` when `AvoidIngress`;
  - the host volume `<app>_caravana-build` (read-write) mounted at `/cache` in both tasks when `CacheMB > 0`;
  - task `source` (prestart, not sidecar, `GitImage`, entrypoint `sh -c`, the script `BuildSourceScript`): empties
    `/cache` when `du -sm` says it is over `CacheMB`, sets up access (`GIT_SSH_COMMAND` with `-i secrets/deploy_key
    -o IdentitiesOnly=yes -o UserKnownHostsFile=local/known_hosts -o StrictHostKeyChecking=yes`, or a credential store
    file `secrets/git-credentials` with `credential.helper`), `GIT_TERMINAL_PROMPT=0`, `git clone --depth 1 --branch
    <branch> --single-branch -- <repo> /alloc/data/src`, writes `/alloc/data/caravana/source.env` (`COMMIT`, `AUTHOR`,
    `SUBJECT`, each base64 so no value can break the file), deletes `.git`, checks the Dockerfile exists (a sentence
    naming it if not); 128 MB;
  - task `build` (main, `BuilderImage`, entrypoint `sh -c`, the script `BuildScript`): reads `COMMIT` from
    source.env, runs `buildctl-daemonless.sh build --progress=plain --frontend dockerfile.v0 --local context=<dir>
    --local dockerfile=<dir of the Dockerfile> --opt filename=<its base> --output
    type=image,name=<image>:git-<commit12>,push=true[,registry.insecure=true] --metadata-file
    /alloc/data/caravana/build.json [--import-cache type=local,src=/cache --export-cache type=local,dest=/cache,mode=max]`;
    `security_opt = ["seccomp=unconfined", "apparmor=unconfined"]`, env `BUILDKITD_FLAGS=--oci-worker-no-process-sandbox`
    and `DOCKER_CONFIG=/secrets/docker` (only with push credentials); memory and CPU from the settings (D8);
  - templates read only the job's own variable `nomad/jobs/<jobID>` (`change_mode = "noop"`): `secrets/deploy_key`
    (perms 0400) or `secrets/git-credentials` (0400) in `source`, `local/known_hosts` in `source`,
    `secrets/docker/config.json` (0444, the rootless user is uid 1000) in `build`. No secret is in the job.
  The scripts are tested under a real `sh` with fake `git`, `du` and `buildctl-daemonless.sh` (as 036 did for the
  backup scripts). `deploy/build.go`: `StartBuild` (write the job's variable: `deploy_key` or `git_credentials`,
  `known_hosts`, `docker_config`; ensure the cache volume with `uid=1000,gid=1000` and the job's constraint; register),
  `BuildState` (022's `ScheduleRunState`), `BuildResult` (read `source.env` and `build.json` with the fs API),
  `BuildLog` (the last 64 KiB of `source`'s and `build`'s stdout and stderr, in that order, marked), `StopBuild`,
  `PurgeBuild` (job and its variable), `DeleteBuildCache`, `BuildJobs(appID)` (for recovery). `deploytest` gains a
  fake that a test can make succeed with a digest and commit, or fail with a log.
- **D5 The flow** (`service/build.go`). `deployWarned` is split: `newDeployment` (the record, `markLatest`) and
  `deployRecorded(ctx, rec, a, m, pin, built)` (from the lock to the watcher, unchanged). In `saveAndDeploy` and
  `Redeploy`, with `pin == nil` and a built service, the record is made with its build (status `pending`, build
  `queued`) and the call answers at once; a goroutine on the process context (`s.processCtx`, as Restart's) runs the
  build and then `deployRecorded` with `built{service, digest}`: `Resolve` skips the built service and takes its digest.
  - Newest wins: a new deployment of the app cancels the build in progress (its job stopped, its deployment
    `superseded`, its build `cancelled`). `appState.buildCancel`. Stop app and Delete app cancel it too.
  - The build doesn't hold the app's deploy lock (Stop, Restart, backups go on while it builds).
  - A global queue: at most `concurrency` builds at once (a channel semaphore); a queued build whose deployment is no
    longer the app's latest gives up as `cancelled`.
  - Timeout: the build is stopped after `timeoutMinutes` and fails "the build took longer than 30 minutes".
  - Failure: the deployment fails with `*BuildError` whose text is "build failed: <reason>" and its last 20 log lines
    (masked), and `notifyDeploy` sends `deploy.failed`. Reasons in Caravana's words: "the repository could not be read"
    (clone failed), "no Dockerfile at <path>", "the image could not be pushed", "the build failed" (exit ≠ 0), "the
    build job was lost", "Caravana restarted during the build".
  - Success: the origin is the context's (CI headers) when set, else `{Commit, Ref: branch, Actor: author,
    Repository: host/path without .git}`; the build row gets the digest; `deployRecorded` goes on.
  - Restart: at `Start`, builds `queued` or `running` fail ("Caravana restarted during the build"), their deployments
    fail, and every `<app>_build-*` job and its variable are purged.
  - `AvoidIngress` is true when the cluster has a ready, eligible node without `caravana_ingress`. `HostNetwork` is
    true when the repository's or the image's host is loopback.
  - The cache volume is deleted with the app, and when the app has no built service any more (a warning is enough
    if that fails).
- **D6 Push webhooks.** `POST /v1/webhooks/{id}` (and the token may also come in `X-Gitlab-Token`) reads at most 1 MiB of
  the body for an app with a built service. `service.PushEvent` (pure, `internal/service/pushevent.go`) reads GitHub
  and Gitea/Forgejo (`X-GitHub-Event`/`X-Gitea-Event`/`X-Forgejo-Event`: `ping` skips; `push` reads `ref`, `after`,
  `deleted`) and GitLab (`X-Gitlab-Event: Push Hook`: `ref`, `checkout_sha`, `after`). A push to another ref, a deleted
  branch or a ping answers 200 with `changed: false` and `skipped` (a sentence); a push whose commit is the live
  deployment's origin commit answers unchanged; anything else builds the branch's newest commit. An app without a
  built service ignores the body, as today. `WebhookResult.Skipped`.
- **D7 Store** (migration `0040_builds.sql`): `builds` (`id` PK, `deployment_id` UNIQUE references deployments ON DELETE
  CASCADE, `app_id`, `service`, `status` CHECK `queued|running|succeeded|failed|cancelled`, `repository`, `branch`,
  `commit_sha`, `author`, `subject`, `image`, `digest`, `error`, `log` TEXT, `log_cut` INT, `node`, `queued_at`,
  `started_at`, `ended_at`); `build_settings` (one row: `memory_mb` 2048 (512–16384), `cpu_mhz` 2000 (500–32000),
  `timeout_minutes` 30 (5–180), `concurrency` 1 (1–4), `cache_mb` 5120 (0–102400, 0 = no cache)). The log is kept for
  the newest 50 builds of an app (older rows keep their fields with `log` NULL). The deployment record carries the
  build summary (`store.DeploymentRecord.Build *BuildRecord` read by a join).
- **D8 Settings.** `Service.BuildSettings`/`SetBuildSettings` (each value checked, errors naming its field).
  `CARAVANA_BUILDER_IMAGE` and `CARAVANA_GIT_IMAGE` (`-builder-image`, `-git-image`) pin the images, defaulting to
  `nomadjob.DefaultBuilderImage` and `DefaultGitImage` (research R1's digests).
- **D9 API 0.56.0** (`contracts/api.md`): `Service.build` (`ServiceBuild`), `Deployment.build` (`BuildSummary`),
  `getDeploymentBuild`, `getGitAccess`, `createDeployKey`, `putGitToken`, `deleteGitAccess`, `getBuildSettings`,
  `putBuildSettings`, `WebhookResult.skipped`, codes `git_access_missing`, `git_host_key_unknown`,
  `build_registry_missing`, `build_not_found`, `git_access_not_found`. Deploy tokens: `deployApp` keeps working for a
  built app (it builds); every new route is denied by the default deny. MCP: `get_deployment` shows `build` as the
  API answers it; no new tool.
- **D10 Console** (no designer pass; plain pages in the console's existing parts, `DESIGN.md` rules):
  - New app › fourth source tab "Build from Git" (`/apps/new?from=git`, `newapp.html`): repository, branch, folder,
    Dockerfile, access (Public, Deploy key, Token), image to push to (a hint names the registry entries), port,
    domain, health path, place. Buttons `deploy` ("Build and deploy", public only) and `create` ("Create, then add
    access"). After create: the app page with a "Next steps" panel (the public key to add, or the token form; the
    webhook URL with Create webhook token; Build and deploy).
  - Configure: a Build panel for the built service (the source, editable as a deploy on the page's revision;
    Git access with the public key and fingerprint, New key…, Token set/replace, Remove access; the push webhook line).
  - Deployments: a built entry shows the Build stage (queued, building with elapsed time, failed, built in 41 s) and
    "Build log"; `/apps/{id}/deployments/{deploymentId}/build` is the log page (commit, branch, author, subject,
    image digest, the log in `<pre>`), its log region refreshed on the `deployments` notice while the build runs
    (`/parts/apps/{id}/deployments/{deploymentId}/build`). Deploy now on a built app says "Build and deploy the newest
    commit of <branch>".
  - Settings › Builds (`/settings/builds`, in the Connections group): the five limits.
- **D11 Docs**: `docs/building-from-git.md` (operator doc), `docs/plan.md` (Phase 3 "Build on a build server" row
  marked built as 050, what's left), `changelog.d/050-build-from-git.md`, a CLAUDE.md paragraph.
- **D12 Lab**: the gate step `build-from-git` (`cmd/caravana/gate_build_test.go`), in the lab only (it skips on a real
  host): a registry job with htpasswd on `127.0.0.1:5001` and its registry entry; a Git server job (sshd on
  `127.0.0.1:2222` and a dumb-HTTP server on `127.0.0.1:8098` over one bare repository, `alpine` pinned by digest);
  the test pushes commits over SSH with its own key. Then: deploy a public `http://127.0.0.1:8098/app.git` app (a
  busybox httpd answering its commit), healthy, answered through Traefik, the build row and the origin; push v2 and call
  the webhook with a GitHub-shaped body: v2 live; a push to another branch skipped; the same commit unchanged; a
  broken Dockerfile fails with its last lines and v2 still answers; switch to `deploy-key` over SSH with the server's
  host key: builds; the private key (read from the Nomad Variable with the lab's management token) is in no answer,
  page, log line, job, export or the SQLite file; delete the app: the variable and the cache volume are gone.
  `TestConsoleLab` gets a step `console-build` (New app's form with a public repository, the history's Build stage and
  the log page).
