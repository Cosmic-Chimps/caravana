# Workload identities: apps that prove who they are

A service can ask Nomad for a **workload identity**: a short-lived token, signed by your cluster, that says which
app, service and copy it is. The app shows it to another system (Bella Baxter, for one) instead of a stored
password, and that system checks the signature against your cluster's published keys. Nothing is stored in
Caravana, and the token renews itself.

## Ask for an identity

Add `identities` to a service, through the API, the Compose overlay or Aspire's publisher:

```json
{
  "name": "api",
  "image": "ghcr.io/acme/api:1.4.0",
  "identities": [
    { "name": "bella", "audience": ["bella-spiffe/<environment-id>"], "ttlSeconds": 3600 }
  ]
}
```

- `name`: lowercase letters, digits and `_`, starting with a letter. `default` and names starting with `vault_` or
  `consul_` are Nomad's own. At most 4 identities per service.
- `audience`: who the token is for (1 to 4 values). The receiver refuses a token meant for someone else.
- `ttlSeconds`: how long one token lives, 60 to 86400 (24 hours); 3600 when left out.

Each copy of the service then finds its token in **`/secrets/nomad_<name>.jwt`** (`$NOMAD_SECRETS_DIR` is
`/secrets`). Nomad writes a new one at about half the TTL, without restarting anything, so read the file each time
you need the token instead of keeping it. There is no environment variable: Nomad would never update it.

A scheduled run of the service gets the same identities. A promotion keeps the identities of the environment it
promotes into, because an audience belongs to one environment.

The console shows a service's identities under **Configure**, read-only. Caravana never reads the token: it isn't
in any page, answer, log or record.

## Your cluster's issuer

A receiver checks a token in two steps: the token names its **issuer**, and the issuer publishes its keys at
`<issuer>/.well-known/openid-configuration` (which points at `<issuer>/.well-known/jwks.json`).

On a host with a console domain, the issuer is the console's address, `https://<console domain>`, and Caravana
answers both documents there with Nomad's own. Every Nomad server of the cluster is set to it, so one issuer and one
set of keys cover all your servers.

```bash
curl -s https://caravana.example.com/.well-known/openid-configuration
```

> [!WARNING]
> Changing the console's domain changes the issuer. Tokens issued before carry the old one until they renew, and
> every receiver that trusts your cluster must be told the new address.

A host without an `https` console address (a development setup) has no issuer: tokens are still issued, without
`iss`, and the two documents answer 404.

## Bella Baxter

Bella Baxter's Nomad attestor turns this identity into Bella access, so `bella` in the container fetches the app's
secrets with no Bella secret stored in Caravana.

1. In Bella, open the environment's SPIFFE settings, set the **Nomad issuer** to your console's address and use the
   **recommended audience** it offers (`bella-spiffe/<environment-id>`), or with the CLI:

   ```bash
   bella spiffe set-mode --strict --nomad-oidc https://caravana.example.com --nomad-audience-recommended
   ```

2. Give the service the identity `bella` with that audience (above).
3. Register the workload. The job is the app's ID (`<project>--<environment>--<name>`, or the bare name in
   Default › production) and the task is the service's name:

   ```bash
   bella spiffe add --name api \
     --node nomad:cluster=https://caravana.example.com \
     --selector nomad:namespace=default --selector nomad:job=<app id> --selector nomad:task=api
   ```

4. Run Bella's agent in the container, which finds `/secrets/nomad_bella.jwt` by itself:

   ```bash
   bella spiffe agent --environment-id <environment-id> --name api --node-type nomad
   ```

Bella's agent also takes the environment's bootstrap token (`BELLA_BOOTSTRAP_TOKEN`); give it as a Caravana secret.
In Strict mode it is useless without the identity. Bella's own guide covers the rest: *Workload identity on Nomad*.
A scheduled run's job is `<app id>_run-<id>`, so a workload registered with `nomad:job=<app id>` doesn't match it.
