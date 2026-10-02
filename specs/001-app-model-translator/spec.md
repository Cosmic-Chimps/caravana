# Feature Specification: App Model and Nomad Job Translator

**Feature Branch**: `001-app-model-translator`

**Created**: 2026-10-02

**Status**: Draft

**Input**: User description: "Feature 001: App model and Nomad job translator. Turn an app
definition into a Nomad job as pure code with no I/O (no Nomad, no registry, no database), so
the deploy flow (003) and webhook (004) can call it. Replaces the proof-of-concept in
`scratchpad/`, which is deleted when this feature is done. Phase 1 scope: one prebuilt image per
service, single node. Acceptance: the translator reproduces the lab's hand-written
`deploy/dev/whoami.json` field for field, and that output deploys healthy in the lab."

## Context

This is the first product feature and the core of every future deploy. Its consumers are:

- **The deploy flow (feature 003)**: hands in an app, a deployment carrying the resolved image
  digest for each service, and the environment's settings; registers whatever job comes back.
- **App owners, indirectly**: whatever they declare must become a correct, safe job, or the
  deploy fails with an error that names the exact field to fix.
- **The maintainer**: this is their first Go feature, so the result must be plain, well tested,
  and its Go idioms explained.

In this spec, "job", "task group", "service registration", "router" and "certificate resolver"
are Nomad and Traefik terms. The constitution makes both part of the product, so these words
describe what the feature produces, not how it's built.

## Clarifications

### Session 2026-10-02

- Q: Replicas set to 0: default to 1, or reject? → A: An omitted replica count defaults to 1;
  an explicit 0 is rejected, naming the field.
- Q: Ports without domains: register them as internal services, or reject until Phase 2? → A:
  Register one internal service `<app>-<service>` on the health check's port, with no Traefik
  tags and a health check required. Other ports are only mapped.
- Q: Should translator warnings exist yet? → A: The result always carries a warnings list; in
  Phase 1 it is always empty, and tests assert that.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A routed web app becomes a job that runs (Priority: P1)

The deploy flow hands in a single-service web app (an image, one port, one domain, a health
check, a replica count) plus a deployment with the resolved digest. It gets back a job that
runs as-is: health-checked rolling updates with automatic revert, the image pinned by digest,
bridge networking with a dynamic host port mapped to the container port, a service registration
Traefik can route over HTTPS, and a shutdown delay so Traefik drops the route before the
container stops.

**Why this priority**: Nothing else in Phase 1 works without this. It's the MVP. The lab's
`whoami` job is the known-good target, so success can be checked exactly.

**Independent Test**: Build the app and deployment equivalent to `deploy/dev/whoami.json`
(deployment ID `lab-manual`, 2 replicas, host `whoami.localhost`, Traefik's default
certificate), translate it, and compare with the reference file field for field. Then submit
the output to the lab and call `https://whoami.localhost:8443`.

**Acceptance Scenarios**:

1. **Given** the whoami-equivalent app, deployment and lab settings, **When** it is translated,
   **Then** the output matches `deploy/dev/whoami.json` field for field, with no warnings.
2. **Given** that output, **When** it is submitted to the lab, **Then** the deployment becomes
   healthy and `https://whoami.localhost:8443` answers.
3. **Given** the same app with a new deployment ID, **When** it is translated, **Then** the only
   difference in the output is the deployment ID in the job metadata, so Nomad records a new job
   version.
4. **Given** the same app, deployment and settings, **When** it is translated any number of
   times, **Then** the output is identical every time.

---

### User Story 2 - Bad input fails loudly and names the field (Priority: P2)

An app owner declares something Caravana can't honor safely: a floating tag, a domain pointing
at a port that doesn't exist, two routers' worth of domains on one service, a web service
without a health check. Translation fails with an error that names the field, so the owner
knows what to fix. Nothing is silently dropped or guessed.

**Why this priority**: The constitution's "fail loudly" and "safe deploys" rules hold only if
the translator enforces them. Without this, a deploy can "succeed" while serving the wrong
thing.

**Independent Test**: A table of invalid inputs, one per rule in FR-020 to FR-033. Each case
asserts that translation fails, that no job is returned, and which field the error names.

**Acceptance Scenarios**:

1. **Given** a deployment whose image for a service is `traefik/whoami:latest` (a tag, not a
   digest), **When** it is translated, **Then** it fails naming that service's digest in the
   deployment.
2. **Given** a service with domains on two different ports, **When** it is translated,
   **Then** it fails naming the second domain's port.
3. **Given** a service with a port but no health check, **When** it is translated, **Then** it
   fails naming that service's health check.
4. **Given** two services that both declare `app.example.com`, **When** it is translated,
   **Then** it fails naming the second domain's host.

---

### User Story 3 - The other Phase 1 shapes translate correctly (Priority: P3)

Beyond the lab's single web service, the translator handles the other shapes Phase 1 needs:
several domains on one service, a background worker with no ports, production HTTPS through a
Let's Encrypt certificate resolver, and services with and without their own CPU and memory.

**Why this priority**: Real apps need these, but each one builds on the P1 path.

**Independent Test**: One golden JSON file per shape, compared byte for byte with the
translator's output.

**Acceptance Scenarios**:

1. **Given** a service with domains `a.example.com` and `b.example.com` on the same port,
   **When** it is translated, **Then** one router matches both hosts, in the order declared.
2. **Given** a worker service with no ports and no domains, **When** it is translated,
   **Then** its task group has no service registration and no health check.
3. **Given** settings with certificate resolver `letsencrypt`, **When** a routed app is
   translated, **Then** the router uses that resolver; with an empty resolver it uses Traefik's
   default certificate, as in the lab.
4. **Given** a service without its own CPU and memory, **When** it is translated, **Then** it
   gets the environment's defaults; a service that sets them keeps its own values.
5. **Given** a service with a port and a health check but no domains, **When** it is
   translated, **Then** it gets an internal service registration with its health check and no
   Traefik tags.

---

### Edge Cases

- **Replicas omitted vs set to 0**: an omitted replica count means 1. An explicit 0 is rejected,
  naming `services[i].replicas`, so a typo never quietly becomes one replica, and "0 means
  stopped" stays free for a later feature. The model must therefore tell "not set" apart from 0.
- **Ports without domains** (e.g. an internal API that only other services call): the service
  gets one internal service registration on its health check's port, with no Traefik tags.
  Traefik only routes services that opt in, so it stays private. Its health check still gates
  the rolling update and auto-revert. Any other ports are mapped but not registered.
- **Supported but changed input**: the constitution requires warnings for input whose meaning
  changes. Phase 1 input has none: every field is honored exactly or rejected. So the result
  carries a warnings list that is always empty for now; the compose translator (009) is the
  first feature to fill it.
- **Domain on a service with several ports**: allowed, as long as every domain of that service
  uses the same port. The router and service registration use that port.
- **Health check port left empty**: defaults to the routed port (the domains' port). With no
  domains, it defaults to the service's only port; a service with several ports and no domains
  must name the check port.
- **Health check on a service with no ports**: rejected. A check needs a port to reach.
- **Host in mixed case** (`App.Example.com`): rejected, naming the host, rather than normalized,
  so the router rule and duplicate detection see exactly what was declared.
- **Host containing characters that could alter the router rule** (a backtick, a space, `||`):
  rejected by the hostname rule, so app input can't inject Traefik rule syntax.
- **App ID plus service name too long**: the registered service name `<app>-<service>` must stay
  a valid DNS label (63 characters at most); if it doesn't, the error names the service name.
- **Digest for a service that isn't in the app**: rejected, naming that deployment entry,
  instead of being ignored.
- **Empty or duplicate environment variable names**: rejected, naming the variable.
- **Resources set below Nomad's minimums** (CPU under 1 MHz once set, memory under 10 MB):
  rejected here rather than at registration time.
- **Map ordering** (env vars, digests): never changes the output; same input, same bytes.

## Requirements *(mandatory)*

### Functional Requirements

**App model**

- **FR-001**: The app model MUST describe an **App** (stable ID, display name, source,
  services), a **Service** (name, image reference, command and args, plain env vars, ports,
  domains, replicas, resources, health check) and a **Deployment** (ID, trigger, resolved image
  digest per service).
- **FR-002**: The only supported source kind MUST be "image". Any other kind fails, naming the
  source kind. The translator MUST NOT read the source when building the job.
- **FR-003**: The model MUST NOT include fields this feature can't honor: secrets, volumes, node
  pinning, compose and git sources, GPUs, binary sources, HTTP-only domains. Each comes back
  with its own feature.

**Translation**

- **FR-004**: Translation MUST take an App, a Deployment and the environment settings, and
  return either a job plus a list of warnings, or an error. It MUST NOT perform any I/O. In this
  feature the warnings list is always empty, and every successful test case asserts that.
- **FR-005**: Translation MUST be deterministic: the same input always gives byte-identical
  output, whatever the order maps are iterated in.
- **FR-006**: Each App MUST become one Nomad service job with the app ID as job ID, the display
  name as job name, and all datacenters (`*`).
- **FR-007**: The job metadata MUST carry the app ID (`caravana.app`) and the deployment ID
  (`caravana.deployment`), so every deploy produces a new job version.
- **FR-008**: The job's update strategy MUST be health-check based, with automatic revert,
  one allocation at a time, a 10-second minimum healthy time, a 3-minute healthy deadline and a
  10-minute progress deadline (the values in `deploy/dev/whoami.json`).
- **FR-009**: Each Service MUST become one task group, named after the service, with exactly one
  Docker task of the same name, a replica count, and a 5-second shutdown delay.
- **FR-010**: The task's image MUST be the deployment's resolved digest for that service, never
  the service's own image reference.
- **FR-011**: Command and args, when set, MUST be passed to the container; when unset, the
  image's defaults apply. Plain env vars MUST be passed as task env; none set means no env.
- **FR-012**: A service's CPU and memory MUST be its own values when set, and the environment
  defaults otherwise.

**Networking and routing**

- **FR-013**: Every task group MUST use bridge networking, with one dynamic host port per
  declared port, mapped to its container port and labelled with the port name.
- **FR-014**: A service with ports MUST get exactly one service registration using Nomad's
  native provider, named `<app-id>-<service-name>`. With domains it's on the domains' port;
  without domains it's on the health check's port and carries no Traefik tags (internal only).
  Other declared ports are mapped but not registered.
- **FR-015**: For a service with domains, the routing tags MUST enable Traefik, define one router named
  `<app-id>-<service-name>` whose rule matches every declared host in declared order, bind the
  router to the configured entrypoint, and always turn TLS on.
- **FR-016**: When the environment sets a certificate resolver, the router MUST use it; when the
  resolver is empty, the router MUST use Traefik's default certificate (no resolver tag).
- **FR-017**: A service with no ports and no domains (a worker) MUST get no service registration
  and no health check.
- **FR-018**: The health check MUST be attached to the service registration, named `health`,
  HTTP when a path is given and TCP otherwise, on the check's port, with a 10-second interval and
  a 2-second timeout unless the service sets its own.

**Environment settings**

- **FR-019**: The environment settings MUST include the certificate resolver (may be empty),
  the entrypoint name, and the default CPU and memory. They are not app input. A missing
  entrypoint or a non-positive default fails, naming the setting.

**Validation** (each failure returns no job and names the field, using the path format in
Assumptions)

- **FR-020**: A service without an image reference MUST fail.
- **FR-021**: A service with no resolved digest in the deployment, or whose digest isn't of the
  form `repo@sha256:<64 lowercase hex>`, MUST fail.
- **FR-022**: A deployment digest for a service the app doesn't have MUST fail.
- **FR-023**: A deployment without an ID, or with a trigger other than `manual` or `webhook`, MUST fail.
- **FR-024**: App IDs and service names that aren't valid lowercase DNS labels MUST fail, as
  MUST duplicate service names and `<app-id>-<service-name>` combinations longer than 63
  characters.
- **FR-025**: Duplicate port names within a service, port names that aren't valid labels, and
  container ports outside 1–65535 MUST fail.
- **FR-026**: A domain whose port isn't declared on the service MUST fail.
- **FR-027**: Domains on more than one port of the same service MUST fail (one router per
  service; two ports would need two routers).
- **FR-028**: A host that isn't a valid lowercase hostname, or a host used twice anywhere in the
  app, MUST fail.
- **FR-029**: A service with ports or domains but no health check MUST fail.
- **FR-030**: A health check whose port isn't declared on the service, is ambiguous (several
  ports, no domains, no check port), or is set on a service with no ports MUST fail.
- **FR-031**: A health check path that doesn't start with `/`, or an interval or timeout that's
  negative, under 1 second once set, or a timeout not shorter than the interval, MUST fail.
- **FR-032**: A replica count that is set to 0 or less MUST fail; an omitted count means 1.
  Negative CPU or memory MUST fail, as MUST CPU or memory below Nomad's minimums (1 MHz, 10 MB)
  once set.
- **FR-033**: Empty or duplicate env var names, or names containing `=`, MUST fail.
- **FR-034**: Every error MUST name the field in a consistent path format, so the API (002/003)
  can return it in the `field` of its error body.

**Replacing the proof of concept**

- **FR-035**: `scratchpad/` MUST be deleted when this feature is done. Nothing in the product
  imports it.

### Key Entities

- **App**: one project a user deploys; maps to one Nomad job. Stable ID (a DNS label, used as
  the job ID and in router names), display name, source (image only), one or more services.
- **Service**: one runnable container; maps to one task group with one task. Name, image
  reference as the user wrote it, command and args, plain env vars, ports, domains, replicas
  (optional; omitted means 1, an explicit value must be at least 1), resources, optional health
  check.
- **Port**: a named container port (name is the label, number is the container port).
- **Domain**: a public hostname routed over HTTPS to one named port of the service.
- **Health Check**: HTTP path (empty means TCP), port name, interval, timeout.
- **Resources**: CPU (MHz) and memory (MB); zero means "use the environment default".
- **Deployment**: one go-live event. ID, trigger (manual or webhook in Phase 1), resolved digest
  per service name. Status, timing and the Nomad job version belong to the deploy flow (003).
- **Environment Settings**: per-installation values that aren't app input: certificate
  resolver, entrypoint, default CPU and memory.
- **Warning**: a message about supported input whose meaning changed. Part of every result;
  always empty in Phase 1.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: The whoami-equivalent input reproduces the lab's reference job with zero
  differing fields.
- **SC-002**: That output, submitted to the lab, becomes healthy within the 3-minute healthy
  deadline and answers over HTTPS at `whoami.localhost:8443`.
- **SC-003**: Every validation rule (FR-020 to FR-033) has at least one test case, and 100% of
  those cases check which field the error names.
- **SC-004**: Golden output exists for at least six shapes (minimal web app, multiple domains,
  worker without ports, internal service with ports but no domains, Let's Encrypt resolver,
  default vs custom resources), and repeated runs
  produce byte-identical output.
- **SC-005**: Every field in the app model either changes the output or is checked by
  validation. No field is accepted and then ignored.
- **SC-006**: `scratchpad/` is gone, and every CI check (build, vet, staticcheck, race tests,
  formatting, CGO-free cross-compile for amd64 and arm64) passes.
- **SC-007**: Every Go idiom used here that differs from C# gets a one-line explanation in the
  feature's summary for the maintainer.

## Assumptions

- **Field path format**: errors name fields like `services[1].domains[0].port`,
  `deployment.digests[web]` or `settings.entrypoint`: lowercase names, list items by index, map
  entries by key. The plan may refine the exact spelling, but it must be one format everywhere.
- **First error wins**: validation checks in a fixed order and returns the first problem. This
  matches the API's single-error body (constitution V). Reporting every problem at once can come
  later.
- **Lab settings for the whoami case**: entrypoint `websecure`, empty certificate resolver,
  default CPU 100 MHz and memory 64 MB (the reference file's values); the whoami service sets no
  resources of its own.
- **Package homes**: the model lives in `internal/app` and the translator in
  `internal/nomadjob`, as the user's description and `docs/plan.md` say.
- **The whoami comparison** checks against the lab's own `deploy/dev/whoami.json`, so the
  translator and the lab can't drift apart. The plan decides how (read the file directly, or a
  test that keeps a `testdata/` copy identical).
- **Digests are trusted for format only**: this feature checks a digest's shape, not that it
  exists in a registry or matches the service's image repository. Resolution is feature 003.
- **Job name**: the job name is the app's display name; if empty, the app ID.
- **Trigger values**: Phase 1 knows `manual` and `webhook`. The translator doesn't put the
  trigger in the job (the reference job doesn't have it); it's carried for the deploy record.
- **Out of scope**: secrets, volumes, node pinning, compose and git sources, GPUs, binary
  sources, HTTP-only domains, registry digest resolution, calling Nomad, storing anything.
