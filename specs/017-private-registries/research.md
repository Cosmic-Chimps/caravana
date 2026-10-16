# Research: Private registries

Lab: Nomad 2.0.7, Docker in the Lima VM, a `registry:2` job with htpasswd auth on `127.0.0.1:5000`
(2026-10-16).

## Lab findings

| # | Experiment | Result |
| --- | --- | --- |
| P1 | Docker task `auth { username = "${CARAVANA_REGISTRY_USERNAME}", password = "${CARAVANA_REGISTRY_PASSWORD}", server_address = "127.0.0.1:5000" }`, the two variables from a template of a Nomad Variable into `secrets/registry.env` with `env = true` | The private image (pinned by digest, not cached) **was pulled and ran**. Nomad interpolates template env into the task config before the pull. |
| P2 | The registered job (`nomad job inspect -json`) | Holds the `${...}` placeholders only: **no secret** (searched for it). |
| P3 | The container's environment | **Contains** `CARAVANA_REGISTRY_USERNAME` and `CARAVANA_REGISTRY_PASSWORD`: `env = true` exports to the task, and the task's env is the container's. |
| P4 | A prestart "puller" task with the same image | Not usable: it must run the app's own image to completion, which an image without a shell can't do safely. Dropped. |
| P5 | A client-level Docker auth file (`plugin "docker" { config { auth { config = ... } } }`) | Not tried, because it can't work here. Caravana runs on the Mac in the lab and as a non-root, sandboxed service on a host, and in Phase 3 on another node than the copies, so it can't own a file the Nomad client reads. |
| P6 | Digest lookup from the Mac, `HEAD /v2/priv/busybox/manifests/v1` with Basic auth and the manifest `Accept` list | 200 with `Docker-Content-Digest`. Lima forwards the VM's `127.0.0.1:5000` to the Mac. Without credentials: 401 `WWW-Authenticate: Basic realm="lab"`. |
| P7 | `nomad var put` with every item empty | Refused: "variable missing required Items object". |

## R1. How the node pulls with credentials

**Decision**: when a service's image comes from a registry Caravana has credentials for, its task
gets:
- a Docker `auth` block that interpolates `${CARAVANA_REGISTRY_USERNAME}` and
  `${CARAVANA_REGISTRY_PASSWORD}`, plus the registry's `server_address`;
- a template that renders the task's own Nomad Variable `nomad/jobs/<app>/<service>/<service>` into
  `secrets/registry.env`, with `env = true` and `change_mode = noop` (P1). Only that task can read
  the variable (Nomad's implicit task policy). The template renders only those two keys, by name.

The job holds placeholders only (P2). A service whose registry has no credentials gets neither the
block nor the template, so its job is unchanged.

The Postgres backup and restore jobs (feature 010) run the service's own image in their `dump` and
`restore` tasks. They get the same block, reading the same two keys from the job's existing
variable `nomad/jobs/<job>`.

**Trade-off, accepted and documented** (P3): the pull credentials are in the app's own container
environment, as `CARAVANA_REGISTRY_USERNAME` and `CARAVANA_REGISTRY_PASSWORD`. The app is the
owner's own (one owner per installation until teams, Phase 4), the variable names say what they
are, and the console asks for a read-only token. Not in any page, API answer, log, job, export or
record (FR-002). A node-level credential helper would remove this, once Phase 3 puts a Caravana
agent on each node; added to the backlog.

**Alternatives considered**:
- A client-level auth file or credential helper: Caravana can't own a file on the node (P5).
- A prestart puller task (P4).
- Pulling with `docker login` on the node: same problem as P5, and it would leak into every
  container's `docker inspect`.

## R2. Where credentials are kept

**Decision**:
- **The secret**: in Caravana's own Nomad Variables, `caravana/registries/<key>` with items
  `username` and `password`. The key is the host with `.` written as `~` and `:` as `_` (`registry~example~com_5000`),
  because variable paths take neither `.` nor `:`. The policy already allows `caravana/*`.
- **Facts the console shows**, in SQLite, migration `0009_registries.sql`: host, user name, created
  and updated times, and the last test's time, outcome and message. As with the backup destination
  (feature 010), the secret is never in SQLite.
- **Per-task variables** (R1) are copies, written by Caravana and never read back from:
  - every deploy writes them before register;
  - saving or removing credentials rewrites every per-task variable naming that host. A
    variable records its registry in an item `registry`, so it is found by listing
    `nomad/jobs/*`.

**Removal** keeps each per-task variable, with `registry` set and the two credential items empty
(P7: one item must be non-empty). The template then renders empty values, the Docker driver pulls
anonymously, and a private image's pull fails with the registry's error (spec edge case). A later
deploy drops the `auth` block, because no credentials are stored.

**Rationale**: constitution VII puts the control plane's secrets in Nomad Variables, and a cold
start must not wait on anything else. Nomad Variables are also replicated to every server, which
Phase 3 needs.

## R3. Digest lookup with credentials

**Decision**: `registry.Client` gains `Credentials func(ctx, host) (username, password string, ok bool, err error)`.
`deploy.Deployer` wires it to the Nomad Variable read. On a 401:

- **`Bearer` challenge** (Docker Hub, GHCR, GitLab, Quay): the token request carries Basic auth
  when credentials exist, else none, as today.
- **`Basic` challenge** (registry:2 with htpasswd, many self-hosted ones): the manifest request is
  repeated with Basic auth. Without credentials this is `ErrNotFound`, missing or private.

New error, `ErrUnauthorized`: credentials were sent and the registry refused them (401 or 403 on
the token or manifest request). The deploy maps it to an `*ImageError` with the new kind
`ErrRegistryAuth`, which the API answers as 422 `registry_auth_failed`. The message names the
registry and says the stored credentials were refused.

`ErrNotFound` without credentials becomes: "image not found, or private: add credentials for
<host> in Settings › Registries". It is the same error as today, with the hint replacing "Caravana
doesn't support registry credentials yet".

`Exists` (rollback's digest check) and the webhook's `Resolve` go through the same code.

## R4. Any registry host

**Decision**:
- **Parsing**: `registry.Parse` accepts any host the reference grammar allows (one with `.` or
  `:`, or `localhost`). `Registry` is the host, lowercased, with `index.docker.io` mapped to
  `docker.io`. The pinned name keeps the host and port.
- **Endpoints**:
  - `docker.io` → `https://registry-1.docker.io`, as today;
  - every other host → `https://<host>`;
  - **loopback hosts only** (`localhost`, `127.0.0.0/8`, `[::1]`) → `http://<host>`. Docker's
    daemon treats those as insecure by default, so the pull matches, and the gate's registry
    uses it (P6).
- **Errors**:
  - a TLS error from a remote host: "<host> must serve the registry API over HTTPS" (spec US3-3);
  - a 401 without a challenge, or a 200 without a digest that we also can't hash: "<host> didn't
    answer as a container registry" (US3-4).
- **Nomad's check**: `nomadjob.Check` may refuse images by host (settings images, the
  `names`/backup images). It stays as is; only users' images open up.

## R5. Test

**Decision**: `Test(ctx, host)` does `GET https://<host>/v2/` (Docker Hub: `registry-1.docker.io`)
and follows the challenge with the stored credentials:
- Bearer: request a token with no scope. Docker Hub, GHCR and GitLab issue one for valid
  credentials and refuse bad ones.
- Basic: repeat the request with Basic auth.

The outcomes:
- **ok**: a 200 at the end;
- **refused**: a 401 or 403 with the registry's message;
- **unreachable**: a network or TLS error.

The time, outcome and message are stored. "Test" never runs on a schedule: an owner-triggered
check, and the check every deploy does anyway, are enough.

## R6. API and console

**Decision** (API 0.13.0 → 0.14.0), on the same session-or-token auth as every `/v1` route:

| Operation | Route |
| --- | --- |
| `listRegistries` | `GET /v1/registries` → `[]Registry{host, username, createdAt, updatedAt, lastTest?: {at, ok, message}}` |
| `putRegistry` | `PUT /v1/registries/{host}` `{username, password?}`. Creates or updates; `password` is required when creating and optional when updating (absent keeps the stored one). 200 with the `Registry`. |
| `deleteRegistry` | `DELETE /v1/registries/{host}` → 204 |
| `testRegistry` | `POST /v1/registries/{host}/test` → 200 with the `Registry`, its `lastTest` updated |

`{host}` is the registry host, port included (`registry.example.com:5000`); `docker.io` stands for
Docker Hub. New error `registry_auth_failed` (422) on deploys, `field` naming the image.

Console: Settings gains a "Registries" section. It lists the registries with their last test, and
each row has Test, Edit (user name, and a secret field where empty keeps the stored one) and
Remove (a `<details>` confirmation). An add form takes host, user name and secret. The section
carries the read-only-token note (R1).

## R7. Gate step `private-registry`

**Decision**: the gate runs its own registry on the node:
1. A `gate-registry` job, with host networking on `127.0.0.1:5000`:
   - a prestart task (`httpd:2-alpine`) writes an htpasswd line for a per-run user and password
     into `/alloc/data/htpasswd`;
   - a `registry:2` task serves with it.
2. A batch job (`quay.io/skopeo/stable`, host networking) copies `busybox:1.37` into
   `127.0.0.1:5000/gate/busybox:v1` with the credentials.
3. The step checks these, in order:
   1. **No credentials:** deploying the app `priv` (that image, a TCP health check on a busybox
      `httpd`) gives 422 `image_not_found`, whose message names the host and says to add credentials.
   2. **Wrong secret:** `putRegistry` with a wrong secret, then `testRegistry`, answers not ok.
      Deploying gives 422 `registry_auth_failed`.
   3. **Right secret:** `testRegistry` is ok, and the deploy becomes healthy.
   4. **No secret anywhere:** the job's JSON has no secret, and neither does `listRegistries`.
   5. **Removed:** `deleteRegistry` leaves the app running.
4. Cleanup removes the jobs and the variables.

It runs on the scoped token, so a missing capability fails in the lab. None is expected: variables
`nomad/jobs/*` and `caravana/*` are already allowed.

## R8. Docs

- CHANGELOG "Unreleased": Added (private registries, any registry host), Changed (the "missing or
  private" message), and Upgrade (none needed).
- CLAUDE.md "Where we are" gains a 017 paragraph.
- docs/plan.md:
  - marks the backlog item "Private registry credentials" done as 017;
  - renumbers Phase 2.5 to Aspire 018 and Bella Baxter 019;
  - adds the backlog item "Node-level registry credentials (Phase 3)" from R1's trade-off.
