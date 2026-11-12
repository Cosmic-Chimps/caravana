# Contract: what Traefik's job reads (034)

Both Traefik jobs (`deploy/install/traefik.nomad.hcl`, `deploy/dev/traefik.nomad.hcl`) follow this. Caravana
writes the variables; Traefik's job only reads them, with its workload identity and the job-scoped policy
`traefik-read`. Research R1–R8 has the lab's evidence.

## Variables

### `caravana/acme` (present only for a custom issuer)

Every item is always written (an absent item would fail the template); "" means not set.

| Item | Value |
|---|---|
| `server` | the ACME directory URL (`https://…`) |
| `email` | the contact email |
| `eab_kid` | the EAB key ID, or "" |
| `eab_hmac` | the EAB HMAC key, base64url without padding, or "" |
| `ca` | PEM of the CA that signs the ACME server's own HTTPS certificate, or "" |
| `storage` | the store's file name: `acme-<12 hex of SHA-256(server + "\n" + eab_kid)>.json` |

No variable: the installer's issuer (`--acme production|staging`), the installer's email, `acme.json`.
The lab's job has no installer values: its defaults are Let's Encrypt's staging directory, an empty email and
`acme.json`, and no router names the resolver unless Caravana runs with `CARAVANA_CERT_RESOLVER`.

### `caravana/certificates/<id>` (one per uploaded certificate, `id` = `crt_` + 12 hex)

| Item | Value |
|---|---|
| `certificate` | the chain as PEM, leaf first, then the intermediates in order, no root |
| `key` | the private key as PEM, as uploaded (PKCS#8, PKCS#1 or SEC 1), not encrypted |
| `names` | the DNS names, comma-separated, lowercase (for the installer's store clean-up) |

## Policy `traefik-read` (job-scoped, applied before the job)

```hcl
namespace "default" {
  capabilities = ["read-job"]
  variables {
    path "caravana/acme" { capabilities = ["read"] }
    path "caravana/certificates/*" { capabilities = ["read", "list"] }
  }
}
```

Without it the task waits at "Missing: nomad.var.get" (R3), so the installer and `lab.sh up` apply the policy
first, as they already do.

## The job

- `meta { caravana_certificates = "1" }` on the job: Caravana reads it (`deploy.IngressFeatures`) and refuses
  the issuer and upload operations without it (`traefik_outdated`).
- The task's arguments are only `--configFile=/secrets/traefik.yml`.
- Templates:
  1. `secrets/traefik.yml`, `change_mode = "restart"`: the whole static configuration (what the flags said
     before, unchanged in meaning), the file provider at `/secrets/dynamic` with `watch`, and the resolver
     `letsencrypt` with `email`, `caServer`, `storage: /acme/<storage>`, `httpChallenge.entryPoint: web`, and
     only when set `eab: {kid, hmacEncoded}` and `caCertificates: [/secrets/acme-ca.pem]` with
     `caSystemCertPool: true`. The resolver keeps the name `letsencrypt` whatever the issuer: every deployed
     router names it.
  2. `secrets/acme-ca.pem`, `change_mode = "restart"`: the `ca` item, or empty.
  3. `secrets/dynamic/certificates.yml`, `change_mode = "noop"`: `tls.certificates` with one entry per
     variable under `caravana/certificates` (`certFile` and `keyFile` as `toJSON` content), or `{}`.
  4. `secrets/dynamic/team.yml` (023, moved from `local/dynamic`) and, on a host,
     `secrets/dynamic/caravana.yml` (the console's route, moved).
- The lab's job keeps its ports and dashboard; the host's job keeps its volume, constraint, redirect and
  loopback dashboard.

## The installer

- A run against a Traefik job without the meta says "Traefik is restarted once for your own certificates
  (a few seconds without HTTP)".
- `--acme` keeps its three values and its reset of `acme.json` on a change.
- Before it runs Traefik's job, the installer drops from every `acme*.json` of Traefik's volume the ACME
  certificates whose every name (main and SANs) is covered by the `names` of an uploaded certificate (exact,
  or a wildcard for one label). It reads the names with its own Nomad token, needs `python3` (skips with a
  note without it), stops Traefik first when there is something to drop, and says what it dropped.
  Nothing else in the store is touched.
