# Research: Workload identities for apps

Lab, observed, unless a section says otherwise. Nomad v2.0.7.

## R3 · Where the token is in a docker task (lab, 2026-11-14)

A probe job (`busybox`, docker driver) with `identity { name = "bella", aud = ["bella-spiffe/lab038"],
ttl = "2m", file = true }` showed:
- The token is `/secrets/nomad_bella.jwt` inside the container, mode 0644, owner root. `NOMAD_SECRETS_DIR` is
  `/secrets`. The directory also holds Nomad's `api.sock` (the task API, there for every task).
- No `NOMAD_TOKEN_bella` variable exists without `env = true` (none of `NOMAD_TOKEN*` is set).
- Nomad rewrote the file 61 s after the token's `iat`, at about half the TTL, with a new `jti`, `iat` and `exp`;
  the task wasn't restarted.

## R4 · The claims without an issuer (lab, 2026-11-14)

The lab's Nomad has no `oidc_issuer`, and `/.well-known/openid-configuration` answers 404 there, while
`/.well-known/jwks.json` answers the keys (three RS256 keys, the token's `kid` among them).
- Header: `{"alg":"RS256","kid":"…","typ":"JWT"}`.
- Payload: `aud` (a string, not a list, for one audience), `exp`, `iat`, `nbf`, `jti`, `nomad_allocation_id`,
  `nomad_job_id` (the job ID: the app ID), `nomad_namespace` (`default`), `nomad_task` (the task: the service),
  `sub` = `global:default:<job>:<group>:<task>:<name>`. `exp − iat` = the TTL.
- **No `iss` claim.** Bella's attestor refuses a token without the issuer it expects, which is why Part B sets
  `oidc_issuer`.

## R2 · Taking the issuer on a running host (install-test, 2026-11-14)

`deploy/dev/install-test.sh` passed with `check_issuer_host` on a fresh Ubuntu 24.04 VM (Nomad 2.0.7):
- A fresh first server renders `oidc_issuer = "https://caravana.test"` from `init-first --issuer`; the console domain
  serves Nomad's discovery document (its `issuer` is that address, its `jwks_uri` on the console domain) and keys,
  through Traefik and Caravana.
- A service's token, read inside its container, carries `iss` = the console's address, its audience and a 600 s life.
- With the issuer taken out of `state.json` and `nomad.hcl` (a host from before the feature) and Nomad restarted,
  the agent's next sync put it back with one Nomad restart, and whoami answered every call meanwhile (0 failures).
- A copy started before the change, restarted after it, still rendered its secret: the default identity used for
  Nomad Variables keeps working when the issuer changes, and the named identity's new token has the new `iss`.

## R1 · Reload or restart

Not measured: the agent restarts Nomad on a Nomad server whose issuer changes (one at a time), which R2 shows is
harmless to running copies. Whether a SIGHUP reload would be enough is left open; a restart is what ships.
