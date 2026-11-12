# Feature Specification: Bring your own certificates

**Feature**: 034 · **Branch**: `alta/quick-baker-rvch` · **Created**: 2026-11-12 · **Status**: Draft
**Input**: CodeAlta task `k83w`, "04 · Bring your own certificates: another ACME issuer, or upload one"

## Why

Caravana's certificates come only from Let's Encrypt (`--acme production|staging|none`). A company install
often has to use its own CA: a certificate it bought, or another ACME issuer such as Google Trust Services,
ZeroSSL or DigiCert, which need External Account Binding (EAB: a key ID and an HMAC key). Certificates of a
cloud's certificate manager can't be used: their private keys can't be exported.

## User Scenarios

### User Story 1 - Another ACME issuer (P1)

The owner opens Settings › Certificates, chooses "Another ACME issuer", picks a preset (Google Trust
Services, ZeroSSL, DigiCert) or types the directory URL, gives the contact email and, when the issuer needs
it, the EAB key ID and HMAC key. On save Traefik restarts once and every domain gets its certificate from
the new issuer. Renewal stays automatic. "Let's Encrypt" brings the installer's issuer back.

**Independent test**: with a Pebble ACME server that requires EAB, a new domain's certificate is issued by
Pebble, and it is renewed.

**Acceptance scenarios**:
1. **Given** an installation on Let's Encrypt, **When** the owner saves a custom issuer with a valid
   directory URL, email and EAB pair, **Then** the settings say so, Traefik restarts once on its own, and a
   domain added afterwards presents a certificate of that issuer.
2. **Given** an issuer whose directory says an external account is required, **When** the owner saves it
   without an EAB pair, **Then** the save is refused naming `eabKeyId`, and nothing changes.
3. **Given** an address that isn't an ACME directory (or can't be reached), **When** saved, **Then** it is
   refused naming `server`, and nothing changes.
4. **Given** a custom issuer, **When** the owner edits the email and leaves the HMAC key empty, **Then**
   the stored key is kept.
5. **Given** a custom issuer, **When** the owner chooses Let's Encrypt, **Then** the installer's issuer is
   back and the certificates it had before are served again while they are valid.
6. **Given** an installation made with `--acme none`, or a Traefik job older than this feature, **Then**
   the issuer can't be changed and the page says why and what to run.

### User Story 2 - Upload a certificate (P1)

The owner pastes a PEM chain and its private key. Caravana checks it (the key matches, the chain is
complete, it isn't expired, which hosts it covers) and Traefik serves it for every covered host, without a
restart and without asking the ACME issuer for those hosts. Nothing renews it: its expiry is shown, and it
becomes a needs-you item and a notification before it ends. Uploading a certificate for the same names
replaces the earlier one. Removing it falls back to the ACME issuer.

**Independent test**: a wildcard certificate for `*.byo.test` serves `a.byo.test` and `b.byo.test` with no
ACME request for them; replaced by one that expires in 5 days, the needs-you item appears; removed, both
hosts get ACME certificates.

**Acceptance scenarios**:
1. **Given** a valid chain and key, **When** uploaded, **Then** the answer lists the names, the issuer, the
   expiry and whether browsers trust it, and within a minute covered hosts present it.
2. **Given** a key that doesn't match, a chain with a missing intermediate, an expired certificate, an
   encrypted key or a certificate without DNS names, **When** uploaded, **Then** it is refused naming the
   field (`certificate` or `key`) and the reason in plain words.
3. **Given** an uploaded certificate, **When** one with exactly the same names is uploaded, **Then** it
   replaces the earlier one (the answer says so).
4. **Given** an uploaded certificate inside the warning window (or expired), **Then** the needs-you list
   has one item for it (not one per domain), and the owner's channels get `problem.opened`.
5. **Given** an uploaded certificate, **When** removed, **Then** covered hosts go back to the ACME issuer
   (or Traefik's own certificate without one).
6. **Given** a host that already has an ACME certificate, **When** a certificate covering it is uploaded,
   **Then** the page names the host as still presenting its earlier certificate and says what drops it
   (run the installer again); it is never silently "done".
7. The console's own domain is served with an uploaded certificate that covers it, like any other host.

### User Story 3 - Domains say who issued each certificate (P2)

Each domain row and each row of the certificates table says the issuer the handshake showed (Let's
Encrypt, the custom issuer's name) or "uploaded: <subject>, expires <date>" when the presented certificate
is an uploaded one.

### Edge cases

- An uploaded certificate from a private CA (root included in the upload, or self-signed): accepted, marked
  "not trusted by browsers"; domains that present it are judged by its dates.
- A custom ACME issuer with its own CA (step-ca, Pebble): the owner may give the issuer's CA certificate so
  Traefik can talk to it; domain checks trust that CA too.
- Nomad can't be reached while saving: nothing is half-saved; the answer is the usual 502.
- More than 30 uploaded certificates: refused (`too_many_certificates`).
- Switching the issuer never deletes the previous issuer's account or certificates: each issuer has its own
  store file in Traefik's volume.

## Requirements

- **FR-001** Settings › Certificates and the API read and set the ACME issuer: the installer's (Let's
  Encrypt), or a custom one (`server`, `email`, optional `eabKeyId` + `eabHmacKey`, optional
  `caCertificate`).
- **FR-002** The EAB HMAC key lives only in the Nomad Variable `caravana/acme`. SQLite holds the address,
  the email, the key ID and the issuer's CA certificate (public).
- **FR-003** Saving applies by itself: Traefik's job reads the variable and restarts its task once. No
  installer run, no root access for the console. The page says Traefik restarts.
- **FR-004** Before saving, Caravana reads the ACME directory at `server` and refuses what isn't one, and a
  missing EAB pair when the directory requires one. It never registers an account (EAB keys can be
  single-use).
- **FR-005** Each issuer has its own store file (`acme-<hash>.json`; the installer's stays `acme.json`).
- **FR-006** An uploaded certificate is checked as User Story 2 says; the normalized chain and the key go
  to the Nomad Variable `caravana/certificates/<id>`, and SQLite holds only what is public (names, subject,
  issuer, dates, fingerprint, key type, trust).
- **FR-007** Traefik reads uploaded certificates through its file provider from a file rendered into the
  task's `secrets/` folder (never readable through Nomad's fs API), with no restart.
- **FR-008** Traefik asks the ACME issuer for no host an uploaded certificate covers (research R4: Traefik's
  own rule; the router keeps naming the resolver, so removal falls back by itself).
- **FR-009** Needs-you kind `certificate_expiring`, one item per uploaded certificate inside the warning
  window or expired; a domain that presents an uploaded certificate raises no `domain_cert_expiring`.
- **FR-010** Domains and certificates say the issuer and, for an uploaded certificate, its subject and
  expiry (matched by the leaf's SHA-256 fingerprint from the real handshake).
- **FR-011** A covered host that still presents another certificate is reported (`keptEarlier` on the
  uploaded certificate); the installer's next run drops ACME certificates whose every name is covered.
- **FR-012** The private key and the EAB HMAC key appear in no page, answer, log, record, export or job
  (sentinel tests in `service`, `api` and `ui`, and in the lab).
- **FR-013** Deploy tokens can use none of the new operations (default deny).
- **FR-014** When Traefik's job predates the feature (no `caravana_certificates` meta) both parts are
  refused with what to run; with `--acme none` only the issuer part is.

## Success Criteria

- **SC-001** Lab: with Pebble (EAB required) as the issuer, a new domain presents a Pebble certificate, and
  after Traefik restarts with a short-lived certificate the serial changes (renewed).
- **SC-002** Lab: an uploaded wildcard serves two hosts and Traefik's log has no ACME order for them; its
  expiry shows on Domains; an expiring one raises `certificate_expiring`; removing it falls back to ACME.
- **SC-003** The sentinel key and HMAC appear nowhere (FR-012).
- **SC-004** `install-test.sh`: an older Traefik job is replaced with one restart, the policy lets Traefik
  read the two variable paths, an uploaded certificate is served on 443, and a rerun drops a covered ACME
  certificate from the store.

## Out of scope

- DNS-01 challenges (wildcards from ACME), several issuers at once, per-domain issuers.
- Showing Traefik's ACME errors in the console (they are in Traefik's log).
- Dropping earlier ACME certificates from the console (the installer does it; follow-up task).
- Changing `--acme none` to an issuer from the console (the installer's flag).

## Assumptions

- Traefik v3.7 (pinned): `caServer`, `eab`, `caCertificates`, and certificates given as PEM content in the
  file provider's `tls.certificates` (research R1–R6, lab 2026-11-12).
- The design contract for the page (`contracts/page-certificates.md`) is this session's; the designer's
  review, as for 030 and 031, is still to come.
