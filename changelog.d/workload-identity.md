### Added
- **Workload identities.** A service may ask for up to 4 Nomad workload identities,
  `identities: [{name: bella, audience: ["bella-spiffe/<environment-id>"], ttlSeconds: 3600}]`, through the
  API, the Compose overlay or Aspire's publisher. Each copy finds a token the cluster signed in
  `/secrets/nomad_<name>.jwt`, which Nomad renews at about half its lifetime; the app shows it to another
  system (Bella Baxter's Nomad attestor, for one) instead of a stored password. Caravana never reads the
  token. Configure shows a service's identities, read-only. A promotion keeps the identities of the
  environment it promotes into. See `docs/workload-identity.md`.
- **The cluster's issuer.** On a host with an `https` console address, every Nomad server signs identities
  with that address as their issuer, and Caravana answers `/.well-known/openid-configuration` and
  `/.well-known/jwks.json` there with Nomad's own documents, so a receiver can check a token. One issuer
  covers every server of the cluster.
- API 0.41.0: `identities` on a service and in the Compose overlay (schema `Identity`), and the two
  discovery documents.

### Upgrade
- After the update, each Nomad server restarts once, one at a time, to take the issuer; running apps keep
  running. Nothing to rerun on the host.
- Changing the console's domain later changes the issuer: tell every system that trusts your cluster.
