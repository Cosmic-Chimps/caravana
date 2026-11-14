# Implementation Plan: Workload identities for apps

**Spec**: [spec.md](./spec.md) · **Tasks**: [tasks.md](./tasks.md)

## Summary

Two pieces. **Identities** (Part A): a service field, translated into the task's Nomad `identity` blocks, through
every front door (API, Compose overlay, console read-only, Aspire). **The cluster's issuer** (Part B): the
console's public URL as Nomad's `oidc_issuer` on every Nomad server, delivered by the node sync and applied one
server at a time, and the two discovery documents served by Caravana on the console domain. No migration (the
field lives in the app document; `omitempty` keeps every revision), no new module, no Nomad policy change
(the discovery endpoints need no token). API **0.41.0** (0.38.0 is left for 034, 0.40.0 is 037's).

## Constitution Check

- **I Small**: no module, no service; the discovery is two routes. ✓
- **II One model**: identities are a field of `app.Service`, so image, Compose and Aspire normalize into it;
  `Translate` stays pure and golden-tested. ✓
- **III Safe deploys**: an identity is part of the job version; nothing about rollouts changes. ✓
- **IV Fail loudly**: every bad identity is a field error naming its path; `env` isn't silently dropped because
  it isn't accepted (an unknown field is refused as today). ✓
- **V API-first**: the field and its schema change in the same commit; the console only reads it. ✓
- **VI Nomad-native**: Nomad's own `identity` block; the export writes it, so an exported job keeps it. The issuer
  is Nomad's own setting. The discovery routes are Caravana's own routes on its console domain, like the console
  itself (no Traefik change). ✓
- **VII Secure**: the token is a credential while valid: Caravana never reads it (the Nomad API can't read
  `secrets/` anyway), and a test proves it reaches no answer, page, record or log. The issuer and the JWKS are
  public by design. ✓
- **VIII Plain Go**: table tests, goldens, lab steps. ✓

No Complexity Tracking entry.

## Decisions

**D1 Field shape.** `Identity{Name string; Audience []string; TTLSeconds int}` with JSON `name`, `audience`,
`ttlSeconds,omitzero`. `TTLOrDefault()` gives 3600. `file = true` always (the file is the one source Nomad
refreshes on renewal, Bella 081 R1); `env` is not offered (spec edge case). Names follow FR-002: the name ends up
in `nomad_<name>.jwt` and `NOMAD_TOKEN_<name>`, so letters, digits and `_` only; `default` is Nomad's own, and
`vault_`/`consul_` are Nomad's integrations' prefixes.

**D2 Translate.** `task()` appends `&api.WorkloadIdentity{Name, Audience, TTL: time.Duration(ttl)*time.Second,
File: true}` per identity, in order, after the existing task fields. `ScheduleRunJob` gets the same through one
helper `workloadIdentities(svc)`. A new golden `identity` (one service, two identities, one with a TTL) in JSON
and HCL. The HCL writer writes
```hcl
      identity {
        name = "bella"
        aud  = ["bella-spiffe/env-1"]
        ttl  = "1h0m0s"
        file = true
      }
```
(the TTL as Go's duration string, which Nomad parses). The reflection test covers the field.

**D3 Compose.** `compose.Overlay.Identities []app.Identity` (`identities`), copied onto the service in
`service.go` with secrets and backup. Errors from `app.Validate` are renamed to the overlay path as for backup.
The console's Compose form doesn't edit identities; it must carry the kept overlay's identities through a deploy
from the form (as `keepDomains` does for domains), so a deploy from the form never drops them.

**D4 Promotion and copy.** `promoted()` keeps the target service's `Identities` (as env, secrets, domains);
a source-only service is added whole. `CopyApp` copies them.

**D5 Console.** Configure's service panel: a read-only "Identities" block per service with identities: name, the
audience values in mono, the TTL in words ("1 hour"), "read from `/secrets/nomad_<name>.jwt`", and "for Bella
Baxter" when any audience starts with `bella-spiffe`. A line under it: "Set through the API, the Compose overlay or
Aspire." `merge.go`/`applyEdit` keeps `Identities`. A kept-file app shows the same.

**D6 API.** `Identity` schema (no `env`, `additionalProperties: false` as other schemas do), `Service.identities`
(maxItems 4), `info.version` 0.41.0. The parity test keeps the schema in step with `app.Identity`.

**D7 The issuer's source.** `Service.ClusterIssuer()` is the public URL when it parses as `https://host[:port]`
with no path, query or user; else "" (no issuer).

**D8 Delivery.** `NodeAnswer.Issuer` (`issuer`, omitempty) and `nodeagent.Answer.Issuer`. `nodeagent.Report` gains
`issuer` (what the agent last rendered). The agent's `Validate` accepts "" or an https URL with a host name
(`is_hostname` rules), an optional port and nothing after it; anything else refuses the whole answer as today.
`State.Issuer` stores it; `nomadData.Issuer` renders `oidc_issuer = "<url>"` inside `server { }` only. An old
agent ignores the new JSON member (the answer is decoded without `DisallowUnknownFields`; keep it so). An answer
**without** an issuer keeps the one the server has (review): a console older than the issuer, after a rolled-back
update, sends none, and dropping it would restart every Nomad server at once. The issuer and the console share one
rule, `mesh.CleanIssuer`, so the console never sends what the agent refuses. `init-first --issuer` renders it on a
fresh first server from the start (the installer passes `https://<domain>`), so a new host needs no extra restart.

**D9 One at a time.** A pure function decides what issuer each answer carries:
`issuerFor(name, role, reported, current, inFlight map[string]time.Time, now) (send string, start bool)`.
- A client: `current` (it only stores it).
- A Nomad server that already reports `current`: `current` (and it leaves `inFlight`).
- A Nomad server in `inFlight`: `current`.
- A Nomad server while another server is in `inFlight` since under 2 minutes: its own `reported` value, so its
  agent renders no change.
- Otherwise: `current`, and it enters `inFlight` now.
`inFlight` lives in memory in `Service` (no migration); a Caravana restart forgets it, which at worst lets one more
server take the issuer at once. An old agent reports no issuer and ignores the answer's: harmless.
On the agent: if the rendered issuer changed and the role is server, `systemctl restart nomad` (research R1 may
show a SIGHUP reload is enough: then `reload`). A client renders and restarts nothing (a servers-only setting).

**D10 Discovery routes.** `internal/api` (it is the public API's listener; no auth, outside `/v1`):
`GET /.well-known/openid-configuration`, `GET /.well-known/jwks.json`. Handler: `ClusterIssuer()==""` → 404
`issuer_not_set`; else `deploy.NomadDiscovery(ctx, path)` reads Nomad's same path through the api client's raw
query (5 s timeout, 64 KiB cap, JSON only), answered with `Content-Type: application/json` and
`Cache-Control: public, max-age=60`; any failure → 502 `nomad_unavailable` (no body text from Nomad). Not on the
webhook-only listener. Listed in openapi (tag `discovery`, `security: []`) so the parity test passes.

**D11 Installer and lab.** The installer writes nothing new (the issuer comes from the sync). `install-test.sh`
checks: `curl -sk https://<domain>/.well-known/openid-configuration` has `issuer` = `https://<domain>`, the jwks
has a key, Nomad's `nomad.hcl` has the `oidc_issuer` line, and an app with an identity has a token whose `iss` is
the URL (read with `docker exec`, decoded, never printed). The dev lab stays without an issuer (Bella asked us not
to change it; its `CARAVANA_PUBLIC_URL` isn't https anyway).

## Research to do in the lab (record in research.md)

- **R1** Does `systemctl reload nomad` (SIGHUP) apply a new `oidc_issuer`, or does it need a restart?
- **R2** After the issuer is set on a running server, do running copies keep their default identity working
  (a secrets app's template still renders after a redeploy and a restart of its copy)? Do named identities get
  the new `iss` at their next renewal?
- **R3** The token's place in a docker task: `/secrets/nomad_<name>.jwt`, `NOMAD_SECRETS_DIR=/secrets`; and that
  no `NOMAD_TOKEN_<name>` exists without `env = true`.
- **R4** The claims of a named identity on Nomad 2.0.7 without an issuer (lab): `aud` string, `exp − iat` = TTL,
  `nomad_job_id` = app ID, `nomad_task` = service, `sub` shape.

## Tests

- `app`: validation table (each rule, each path). `nomadjob`: golden `identity`, schedule run with identities,
  HCL reflection. `compose`: overlay identities and error paths. `service`: promotion keeps target identities;
  `ClusterIssuer`; the one-at-a-time rule (table over server reports). `nodeagent`: validate issuer (good, http,
  path, query, user, IP, bad host), render goldens `server-issuer.golden.hcl` and that a client never renders it,
  restart on change only for a server. `api`: discovery routes (no issuer 404, Nomad fake 200 passthrough, Nomad
  error 502, not on webhook listener), schema parity, scope walk (deploy tokens: these routes need no token at
  all, so list them with health). `ui`: Configure shows identities and keeps them on save; Compose form keeps them.
- Lab gate step `workload-identity` (`gate_identity_test.go`): deploy busybox with identity `bella`
  (audience `bella-spiffe/lab038`, TTL 120 s), Run a command `cat /secrets/nomad_bella.jwt` to get the token in the
  test process, verify its signature with Nomad's JWKS (read through the lab's Nomad address), the claims of SC-001,
  wait past half the TTL and see a renewed token (different `iat`), then search every API answer of the app
  (app, deployments, export, copies, actions, logs) and the request log for the token: absent. `exportMatchesLive`
  for the identity app. Then delete.
