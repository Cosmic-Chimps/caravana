# Implementation Plan: PublishToCaravana

**Branch**: `026-publish-to-caravana` + repo `Cosmic-Chimps/aspire-hosting-caravana` | **Date**:
2026-10-19 | **Spec**: [publish-to-caravana.spec.md](./publish-to-caravana.spec.md)

## Findings (2026-10-19)

Real Aspire 13.6.1 output (the Dokploy example's `aspire-output/docker-compose.yaml`: dashboard,
Redis with a password, Seq with a data volume, two projects with references) run through
Caravana's own `POST /v1/apps/{id}/compose/preview` with its `${...}` replaced:

1. `networks.aspire.driver: "bridge"` and `volumes.<name>.driver: "local"` are refused (and the
   message for the network one talks about volumes).
2. `services.cache.entrypoint` is refused ("isn't supported yet").
3. The dashboard (`ports: ["18888"]` + `expose: [18889, 18890]`, no domain) needs `health.port`.
4. Volume names like `cosmicchimps.aspire.apphost-a6dba33c27-seq-data` have dots, which Caravana's
   volume names refuse.
5. With those out of the way it translates to 4 services, with warnings: one per service about
   networks (5 of them) and one about `depends_on` conditions.
6. Secrets sit inside other values: `ConnectionStrings__cache: "cache:6379,password=${CACHE_PASSWORD}"`,
   `CACHE_URI: "redis://:${CACHE_PASSWORD}@cache:6379"`. Images are `${APISERVICE_IMAGE}`; values
   live in `.env.Production` (or `.env`) next to the file.
7. References are already `http://<service>:<port>` (`services__apiservice__http__0`), which resolve
   inside one Caravana app by service name: no service-discovery rendering needed.

## Caravana side (this repo)

- `internal/compose`: accept `networks.*.driver` and `volumes.*.driver` when they are the defaults
  (`bridge`, `local`): ignored silently for volumes, folded into the one network warning. Other
  drivers stay refused, with a message that names networks for networks.
- One networks warning per file at most (not one per service): "networks don't separate services
  here: every service of the app reaches the others by name".
- `entrypoint`: `app.Service.Entrypoint []string` (`json:"entrypoint,omitempty"`); Compose's
  `entrypoint` maps to it (`command` stays Command+Args); `nomadjob.Translate` sets the docker
  driver's `entrypoint`; the HCL export writes it (the reflection test enforces it); validation (no
  empty strings); goldens updated; openapi `Service.entrypoint` (API 0.21.0). Console: shown
  read-only in Configure like command (kept by the merge).
- Fixtures: `internal/compose/testdata/aspire/` with the sample's Compose file in two forms (as
  generated, with `${...}`, which must fail naming the first variable; and resolved as the
  publisher sends it), golden app JSON, and a test that the resolved one yields 4–5 services with
  the expected warnings.

## The publisher (`../aspire-hosting-caravana`)

Layout like the Dokploy repo: `CosmicChimps.Aspire.Hosting.Caravana.slnx`, `src/…Caravana/`,
`tests/…Caravana.Tests/` (xUnit v3), `example/` AppHost (Redis + a project + a container with a
volume), `nuget.config` (nuget.org only), `.github/workflows/ci.yml` (restore, build, test, pack
check), MIT `LICENSE`, `README.md`. net10.0, Aspire.Hosting(.Docker) 13.6.1, YamlDotNet; no Flurl
(HttpClient is enough).

- **API**: `PublishToCaravana(this IDistributedApplicationBuilder, string name, Action<CaravanaSettings>?)`
  → `IResourceBuilder<CaravanaResource>`; it adds `AddDockerComposeEnvironment($"{name}-compose")`.
  `CaravanaSettings`: `Url`, `Token` (`CaravanaValue`: literal or ParameterResource, like
  `DokployValue`), `Project` (default "default"), `Environment` ("production"), `AppName` (default
  `name`), `DisplayName`, `DeployDashboard` (false), `WaitForDeployment` (true),
  `DeploymentTimeout` (10 min).
  Per resource: `WithCaravanaDomain(caravana, host | parameter, port?)`,
  `WithCaravanaHealthCheck(caravana, path?, port?)`, `WithCaravanaBackup(caravana, engine,
  schedule?, keep?)`, `WithCaravanaMemory(caravana, reservedMB, maxMB?)`, `WithCaravanaExclude(caravana)`.
- **Pipeline**: a `PipelineStepAnnotation` step `caravana-deploy-{name}` with the four
  dependencies the Dokploy one learned (`publish`, `build`, `WellKnownPipelineSteps.Push`,
  `prepare-{name}-compose`), `RequiredBy("deploy")`; and the `PipelineConfigurationAnnotation`
  that turns `docker-compose-up-{name}-compose` into a no-op. Resolve all values first, then
  validate (the Dokploy lesson).
- **Transform** (pure, unit-tested on the fixtures):
  1. read `aspire-output/{name}-compose/docker-compose.yaml` (then the two fallbacks) and the
     first of `.env.Production`, `.env.production`, `.env`;
  2. the secret keys: every `ParameterResource` with `Secret = true`, as its env name (upper case,
     `-` → `_`), plus their values;
  3. substitute every `${VAR}` (not `$$`) with its value; a variable without one is an error naming
     it;
  4. drop the dashboard service (and its references in other services' `OTEL_*`?) unless
     `DeployDashboard`; drop excluded resources; drop `depends_on` conditions? (no: keep, Caravana
     warns);
  5. rename volumes: the part after the AppHost prefix (`…-<hash>-seq-data` → `seq-data`), lower
     case, `[^a-z0-9_-]` → `-`, trimmed to 63, unique; same name every run;
  6. move every environment entry whose resolved value contains a secret's value into the
     overlay's `secrets` for that service (name, value), removing it from the file;
  7. memory → `mem_reservation`/`mem_limit` on the service in the file; domains, health, backup →
     the overlay.
- **Deploy**: `GET /v1/projects/{project}`; on 404 create it (`POST /v1/projects`) and on 404 of
  the environment create it, unless the token is scoped (403 → "create project X and environment
  Y in Caravana first"). App ID = the place ID rule (bare name in default/production, else
  `p--e--n`). `GET /v1/apps/{id}`: 404 → `POST /v1/apps/{id}/compose` with `If-None-Match: *`;
  else with `If-Match: "<revision>"` (one retry on 412). `Caravana-Origin-*` headers from
  `GITHUB_*` when present. Then poll the deployment (`Location`) every 5 s until
  healthy/failed/reverted/superseded or the timeout; print warnings; fail with `code: message`.
- **Secrets hygiene**: the token only in the `Authorization` header; values never logged; a
  `Redact` for error bodies as in the Dokploy package; `ToString()` of values prints
  `<parameter:name>`.
- **Tests**: transform on the fixture (secrets moved, volumes renamed, dashboard dropped, no `${`
  left, no secret value in the file), the HTTP flow with a fake handler (create vs update, 412
  retry, project creation, scoped-token message, wait outcomes), pipeline step dependencies.

## Constitution Check (Caravana side)

| Principle | Assessment |
| --- | --- |
| II. One Model | `Service.Entrypoint` is a new App field, translated purely and exported; goldens. |
| IV. Fail Loudly | Non-default drivers still refused, naming the path; fewer, truer warnings. |
| V. API-First | openapi 0.21.0 `Service.entrypoint`; the publisher uses only the public API. |
| VI. No Lock-In | The export writes `entrypoint`. |

## Complexity Tracking

None.
