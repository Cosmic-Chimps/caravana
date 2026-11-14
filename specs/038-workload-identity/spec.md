# Feature Specification: Workload identities for apps

**Feature Branch**: `alta/hushed-apricot-zvbx` (worktree) · **Spec**: 038 · **Created**: 2026-11-14
**Input**: CodeAlta task `j37f` ("06 · Nomad workload identity for apps (bella run without a stored token)"),
from the secrets talk of 2026-11-11 (way C: the app fetches its own secrets at start). The other side is Bella
Baxter's spec 081 (Nomad node attestor, merged into Bella's `development`).

## Why

An app that fetches its secrets itself (`bella spiffe agent`, `bella run`) needs to prove who it is. Today the
only way on Caravana is to store a long-lived Bella credential as an ordinary Caravana secret, so it sits in the
app's environment. Nomad can give each task a signed, short-lived **workload identity**: a JWT that names the
job, group, task and namespace, signed by the cluster's keys and renewed by Nomad. Bella's attestor (081) checks
such a token against the cluster's published keys and admits the workload, so no Bella access credential has to
be stored for the app.

Bella's attestor needs two things Caravana doesn't do yet:
1. a **named** identity on the task, with Bella's audience and a TTL of 24 hours or less (the default identity
   has no expiry and Nomad's own audience, so Bella refuses it);
2. the cluster's **`oidc_issuer`** set, and the discovery document and keys served over **https** at that
   address (Nomad's HTTP API is on loopback on a Caravana server).

## User Scenarios & Testing

### Story 1 — A service asks for an identity (P1)

The owner deploys an app whose service has `identities: [{name: "bella", audience: ["bella-spiffe/<env-id>"]}]`
(API, Compose overlay or the Aspire publisher). Every copy of the service finds a fresh token in
`/secrets/nomad_bella.jwt`, which Nomad rewrites before it expires. The token names the app's job, the service's
group and task, and the namespace. Caravana never reads, stores, logs or shows the token.

**Acceptance**:
1. A deploy with one identity gives the task one Nomad `identity` block (`name`, `aud`, `ttl`, `file = true`); a
   service without identities gets none and its job is unchanged (every existing golden stays as it is).
2. A bad identity is refused with a field error naming it: `services[i].identities[j].name` (not a lowercase
   name, `default`, a reserved prefix, a duplicate), `.audience` (empty, too many, a bad value), `.ttlSeconds`
   (under 60 or over 86400); more than 4 identities is an error on `services[i].identities`.
3. The exported jobspec writes the identity blocks, and Nomad plans the export against the live job with no diff.
4. A Compose overlay's `services.<svc>.identities` becomes the same field, its errors named at
   `services.<svc>.identities[j].<field>`.
5. Configure shows each identity read-only: its name, audience, TTL, the file the copy reads, and "for Bella
   Baxter" when the audience is Bella's. Saving Configure's form keeps the identities.
6. A promotion keeps the target's identities (an audience belongs to an environment, like a secret); a service
   new to the target is added whole, identities included. A scheduled run of the service gets the same
   identities as the service's copies.

### Story 2 — The cluster publishes its issuer (P1)

On a host with a console domain, the cluster's issuer is the console's public URL (`https://<console domain>`).
Every Nomad server has `oidc_issuer` set to it, and Caravana answers
`https://<console domain>/.well-known/openid-configuration` and `/.well-known/jwks.json` with Nomad's own
documents, so Bella can fetch them over https without anything else exposed.

**Acceptance**:
1. A fresh install sets `oidc_issuer` on the first server; a joined server that becomes a Nomad server gets the
   same value; every Nomad server of a cluster has the same one.
2. An existing host takes the issuer after its update with no command to run: its Nomad servers restart once,
   **one at a time**, and running copies keep running (their default identities keep working).
3. `GET /.well-known/openid-configuration` on the main listener answers Nomad's document, whose `issuer` equals
   the console's public URL; `GET /.well-known/jwks.json` answers Nomad's keys. Both need no credentials, are
   not served on the webhook-only listener, and answer 404 `issuer_not_set` when there is no public https URL
   (dev, the lab).
4. A named identity minted after the change carries `iss` = the console's public URL.

### Story 3 — The owner wires Bella (P2)

The owner follows `docs/workload-identity.md`: give the service a `bella` identity with the environment's
recommended audience, set the Bella environment's Nomad issuer to the console's URL, register the workload with
`nomad:cluster=<console URL>`, `nomad:namespace=default`, `nomad:job=<app ID>`, `nomad:task=<service>`, and run
`bella spiffe agent --node-type nomad` (or `bella run`) in the container.

## Edge cases

- The console's domain changes: the issuer changes, every identity already issued stops matching Bella's
  setting until it is renewed, and the Bella environment's issuer must be changed too (the doc says so).
- A host with no `CARAVANA_PUBLIC_URL`, or an http one (dev, lab): no issuer; identities are still minted, with
  no `iss`, which Bella refuses; the discovery routes answer 404.
- Nomad unreachable from Caravana: the discovery routes answer 502 `nomad_unavailable`, never an empty document.
- A cluster of several servers: one JWKS and one issuer per cluster, so one Bella trust entry per Caravana.
- `env = true` isn't offered: Nomad doesn't refresh the variable on renewal without restarting the task at
  every renewal; an app that wants a variable reads the file in its entrypoint.

## Requirements

- **FR-001** `app.Service.Identities []Identity` (`identities`, omitempty), `Identity{Name, Audience []string,
  TTLSeconds}` (`name`, `audience`, `ttlSeconds` omitzero). Default none; no existing revision moves.
- **FR-002** Validation: name `^[a-z][a-z0-9_]{0,29}$`, not `default`, not starting `vault_` or `consul_`, unique
  in the service; audience 1–4 values, each 1–255 bytes of printable ASCII without spaces; TTL 0 (meaning 3600)
  or 60–86400; at most 4 identities per service.
- **FR-003** `nomadjob.Translate` adds one `api.WorkloadIdentity{Name, Audience, TTL, File: true}` per identity to
  the service's task, in the order given; `ScheduleRunJob`'s task gets the same. Pure, golden-tested, written by
  the HCL export.
- **FR-004** The Compose overlay takes `identities` per service; Aspire's publisher sends it.
- **FR-005** API: `Identity` schema and `Service.identities`; `api/openapi.yaml` 0.41.0.
- **FR-006** Console Configure shows identities read-only (Story 1 §5); the form's save keeps them.
- **FR-007** Promotion keeps the target's identities; copy copies them.
- **FR-008** The cluster issuer is the console's public URL when it is `https://`; the console sends it to every
  agent in the sync answer (`issuer`), the agent checks it (https, a host name, an optional port, nothing else)
  and renders `oidc_issuer` into a Nomad server's `server` block; a change restarts Nomad on that server.
- **FR-009** The console hands a new issuer to one Nomad server at a time: a server whose report doesn't show the
  issuer yet gets it only while no other Nomad server is still taking it (reported issuer differs and server not
  running). A client stores it and restarts nothing.
- **FR-010** Caravana serves `GET /.well-known/openid-configuration` and `GET /.well-known/jwks.json` on the main
  listener (Story 2 §3), by reading Nomad's own documents on loopback with a 5 s timeout and a 64 KiB cap,
  `Cache-Control: public, max-age=60`.
- **FR-011** The token never appears in a page, answer, log, record or error: Caravana never reads the task's
  secrets directory, and a test searches every answer, page and record of a deployed identity app for the token
  read from the copy.

## Answers to the task's open questions

- **`oidc_issuer`**: yes, Bella needs it (081 discovery). Caravana sets it to the console's public URL and serves
  the discovery itself (FR-008–FR-010). An existing host needs only the update; Nomad servers restart once.
- **Several servers**: one issuer and one JWKS per cluster (all Nomad servers share the keyring), so one Bella
  trust entry per Caravana installation.
- **Caravana itself for way B**: not now. Caravana runs under systemd, not as a Nomad task, so it has no workload
  identity; it keeps 037's stored API key. Revisit if Caravana ever runs as a job.

## Success criteria

- **SC-001** A deployed service with a `bella` identity has a token at `/secrets/nomad_bella.jwt` whose signature
  verifies with the cluster's JWKS, whose `aud` is the configured audience, whose `exp − iat` is the TTL and whose
  `nomad_job_id`/`nomad_task` are the app ID and service (lab gate step).
- **SC-002** The token appears in no answer, page, record or log line (same step).
- **SC-003** On a throwaway VM, the installed host serves the discovery document with `issuer` = the console URL,
  and an identity token minted there carries that `iss` (install-test).
- **SC-004** Goldens cover the identity block; the export plans against the live job with no diff.

## Out of scope

- Running Bella's attestor against the lab: Bella needs an https issuer it can reach, which a Lima VM isn't;
  the end-to-end `bella run` is the maintainer's check on a real host.
- `env = true`, `change_mode`, `extra_claims`, service identities (Consul).
- Caravana authenticating to Bella with an identity (answered above).
