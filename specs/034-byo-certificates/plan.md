# Implementation Plan: Bring your own certificates

**Spec**: [spec.md](./spec.md) · **Branch**: `alta/quick-baker-rvch` · **Date**: 2026-11-12

> **Scale note**: about 45 files: a new package (`internal/certs`), store, deploy, service, api, ui, both
> Traefik jobs, the installer and the lab. The risky piece is Traefik's job: its static configuration moves
> from flags to a rendered file, so a mistake there takes HTTP down on a host. It is proven twice: by hand in
> the lab, and by `install-test.sh` on a throwaway VM.

## Summary

Two ways to bring a certificate. (1) The ACME issuer becomes a setting: the console writes the Nomad Variable
`caravana/acme`, Traefik's job renders its static configuration from it and restarts its own task. (2) A
certificate and key are uploaded, checked by `internal/certs`, stored in `caravana/certificates/<id>`, and
rendered into Traefik's file provider with no restart. The translator is untouched: Traefik itself skips ACME
for a host a provided certificate covers. The Domains checks recognise an uploaded certificate by the
fingerprint of the leaf the handshake shows.

## Project Structure

```
internal/certs/parse.go, cover.go, directory.go (+ tests)   new: check an upload, name coverage, read an ACME directory
internal/edge/cert.go                                       Certificate.Fingerprint
internal/store/migrations/0027_byo_certificates.sql         new
internal/store/domains.go, certificates.go                  issuer columns, fingerprint, uploaded_certificates
internal/deploy/acme.go, uploadedcerts.go, ingress.go       the two variables, the job's meta; deploytest fakes
internal/service/certificates.go, issuer.go, uploadedcerts.go, attention…, sentenceparts.go
internal/notify                                             event fields certificate, names
internal/api/domains.go, certificates.go, api/openapi.yaml  0.37.0
internal/ui/certificates.go, certissuer.go, certupload.go, domainspage.go, domainstates.go, templates
deploy/install/traefik.nomad.hcl, traefik-policy.hcl, install.sh, README.md
deploy/dev/traefik.nomad.hcl, traefik-policy.hcl, install-test.sh
cmd/caravana/gate_certificates_test.go, console_certificates_test.go, lab_test.go
docs/certificates.md, changelog.d/byo-certificates.md, CLAUDE.md
```

**Structure decision**: one new package, `internal/certs`, because checking a PEM chain and a key is pure
x509 work that the service, its tests and the lab share; it imports nothing of Nomad, the store or the
service. No new Go module: `crypto/x509`, `encoding/pem` and `net/http` do it all.

## Constitution Check

| Principle | Assessment |
|---|---|
| I. Small by default | PASS: no new module, no new service; Traefik's memory unchanged. |
| II. One model | PASS: `app.App` and `Translate` untouched; goldens unchanged. |
| III. Safe deploys | PASS: nothing deploys; no job of an app changes. |
| IV. Fail loudly | PASS: an upload or issuer Caravana can't use is refused naming the field; a host that keeps its earlier certificate is named, never reported as done. |
| V. API-first | PASS: the console and the API call the same service methods; openapi in the same commit; additive. |
| VI. Nomad-native | PASS: certificates reach Traefik through its file provider, as 023's Team-only passwords do; app routing is still only service tags. The resolver keeps its name. |
| VII. Secure by default | PASS: the HMAC key and private keys live only in Nomad Variables under `caravana/`, are rendered only into the task's `secrets/` (unreadable through the fs API), and appear in no job, flag, record or log; SQLite holds public facts. The directory check never registers an account. |
| VIII. Plain, tested Go | PASS: table tests per layer, sentinel tests, lab steps. |

Re-checked after design: no change. No Complexity Tracking entry.

## Decisions carried (see research.md)

- The console applies an issuer change itself (R2); the installer keeps `--acme` and gains only the store
  clean-up (R5).
- The router keeps `tls.certresolver`; the task's "drops the ACME resolver" is Traefik's own rule (R4).
- One needs-you item per uploaded certificate (R10).
- The provider directory moves to `secrets/dynamic` (R6).

## Phases

- Phase 0: [research.md](./research.md) (R1–R10, lab 2026-11-12).
- Phase 1: [data-model.md](./data-model.md), [contracts/traefik.md](./contracts/traefik.md),
  [contracts/api.md](./contracts/api.md), [contracts/page-certificates.md](./contracts/page-certificates.md).
- Phase 2: [tasks.md](./tasks.md).
