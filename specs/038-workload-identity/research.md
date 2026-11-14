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
