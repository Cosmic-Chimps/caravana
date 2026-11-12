# Contract: API (034) — `api/openapi.yaml` 0.37.0

(0.36.0 is feature 033's, on another branch.) Every new operation needs a session or a full API token;
deploy tokens are refused by the default deny. No answer ever holds a private key or the EAB HMAC key.

## Changed

### `getCertificateSettings` · `GET /v1/settings/certificates`

`CertificateSettings` gains:

| Property | Type | Meaning |
|---|---|---|
| `issuer` | enum + `custom` | `custom`: the issuer set here |
| `issuerName` | string | "Let's Encrypt", "Let's Encrypt (staging)", a preset's name ("Google Trust Services", "ZeroSSL", "DigiCert") when `server` is its address, else the server's host; "" for `none`/`unknown` |
| `acme` | `ACMEIssuer` or null | the custom issuer; null with the installer's |
| `issuerUnavailable` | string | "" when the issuer can be changed, else `installed_without_issuer` (`--acme none`), `installer_not_known`, `traefik_outdated`, `nomad_unreachable` |
| `uploadUnavailable` | string | "" when certificates can be uploaded, else `traefik_outdated` or `nomad_unreachable` |

`contactEmail` is the custom issuer's email when there is one.

`ACMEIssuer`: `server`, `email`, `eabKeyId` (may be ""), `eabKeySet` (bool), `privateCA` (bool: a CA
certificate is stored), `updatedAt`.

### `Domain.certificate` and `Certificate` rows (`listDomains`, `getDomain`, `checkDomain`, `listCertificates`)

gain `fingerprint` (SHA-256 of the presented leaf, hex; "" when none) and `uploaded`
(`{id, subject}` or null: the uploaded certificate the host presents).

### Attention

Kind `certificate_expiring` (`AttentionItem.certificate` = the id, `names`, `expiresAt`; level `failed` once
expired, else `warning`). The webhook message gains `certificate` and `names`.

## New

| Operation | Route | Answers |
|---|---|---|
| `putCertificateIssuer` | `PUT /v1/settings/certificates/issuer` | 200 `CertificateSettings` |
| `deleteCertificateIssuer` | `DELETE /v1/settings/certificates/issuer` | 200 `CertificateSettings` (the installer's issuer again; 200 also when none was set) |
| `listUploadedCertificates` | `GET /v1/certificates/uploaded` | 200 `{certificates: [UploadedCertificate]}` |
| `uploadCertificate` | `POST /v1/certificates/uploaded` | 201 `UploadedCertificate` with `replaced` (the id it replaced, or absent), `Location` |
| `getUploadedCertificate` | `GET /v1/certificates/uploaded/{id}` | 200, 404 `certificate_not_found` |
| `deleteUploadedCertificate` | `DELETE /v1/certificates/uploaded/{id}` | 204, 404 |

`putCertificateIssuer` body (`CertificateIssuerRequest`):

| Property | Rule |
|---|---|
| `server` | required; `https://` URL with a host, no user info, no fragment, at most 512 printable ASCII characters without spaces; Caravana reads the directory there (10 s, no redirects, at most 64 KiB) and it must have `newNonce`, `newAccount`, `newOrder` |
| `email` | required; a plain address |
| `eabKeyId` | optional; 1–256 of `A-Z a-z 0-9 . _ ~ + / = -`; "" or absent with no stored key means no EAB |
| `eabHmacKey` | base64url (padding accepted and removed, standard base64 accepted and converted); absent keeps the stored key when `eabKeyId` is the stored one; required when `eabKeyId` is new |
| `caCertificate` | optional PEM of one or more certificates, at most 32 KiB; absent keeps, "" removes |

Refusals: 422 `invalid_field` naming the field (`eabKeyId` when the directory's
`meta.externalAccountRequired` is true and there is no pair; `server` with what went wrong reading the
directory); 409 `issuer_unavailable` (the message says why and what to run); 502 as elsewhere when Nomad
can't be written.

`uploadCertificate` body (`CertificateUpload`): `certificate` (PEM: the certificate and its intermediates, any
order; may also hold the key) and `key` (PEM; may be empty when `certificate` holds it); each at most 64 KiB.
Refusals: 422 `invalid_field` on `certificate` or `key` (see `internal/certs`), 409
`too_many_certificates` (30), 409 `certificates_unavailable`.

`UploadedCertificate`: `id`, `names`, `subject`, `issuer`, `notBefore`, `notAfter`, `fingerprint`, `keyType`
("RSA 2048", "ECDSA P-256"), `trusted` (chains to a root this server trusts), `status`
(`valid`, `expiring`, `expired`, by the warning window), `uploadedAt`, `domains` (served domains whose last
check presented it), `keptEarlier` (served domains it covers whose last check presented another certificate
that isn't Traefik's own).

New error codes: `issuer_unavailable`, `certificates_unavailable`, `too_many_certificates`,
`certificate_not_found`.
