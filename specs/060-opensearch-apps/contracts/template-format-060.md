# Contract: template format 2, 060 additions

Format 2 (`specs/058-template-inputs/contracts/template-format-2.md`) stays the contract of the manifest and
the bundles; this file adds to it, field by field. Everything not named here is as there. It is linked from
that contract. Plan decisions: `specs/060-opensearch-apps/plan.md` D9, D10.

There is no new catalog format and no new bundle: both additions are fields and syntax **format 2 only**, so
a template that uses them is in `catalog.v2.json` and never in `catalog.json` (see "Old hosts").

## `certificates`

```json
"certificates": {
  "ca":     {"subject": [{"type": "CN", "value": "Wazuh root CA"}, {"type": "OU", "value": "Wazuh"}, {"type": "O", "value": "Wazuh"}, {"type": "L", "value": "California"}, {"type": "C", "value": "US"}]},
  "leaves": [
    {"name": "indexer", "subject": [{"type": "CN", "value": "indexer"}, ...], "dnsNames": ["indexer"]},
    {"name": "admin",   "subject": [{"type": "CN", "value": "admin"}, ...]}
  ]
}
```

At install Caravana makes one certificate authority and, for each leaf, a certificate it signs and the leaf's
key (`Template.Generate`, which the install calls with the time of the install; `Check` and a preview make
none). Only the install path pays for RSA keys.

| Field | Rule |
| --- | --- |
| `ca.subject`, `leaves[].subject` | a list of `{type, value}`, **in the order the name is written** (the common name first). `type` is `CN`, `OU`, `O`, `L`, `ST` or `C`, each at most once; a `CN` is required. `value` is 1–64 printable characters; a `C` is two capital letters. |
| `leaves` | 0–8 |
| `leaves[].name` | `^[a-z][a-z0-9_]{0,23}$`, unique |
| `leaves[].dnsNames` | 0–8 lowercase DNS names (a single label such as `indexer` is allowed); the certificate's subject alternative names |

**Names made.** `ca_cert`, and for each leaf `<name>_cert` and `<name>_key`. They are *secret-valued*, like a
generated secret: they may stand only in an overlay secret's `value` (a secret file reads them from there),
never in the Compose text, a domain, a file's content or path, or the notes, and the format-error names the
place (`uses the secret {{node_key}}: …`). A made name may not equal an input's, a generated secret's or another
made name (`certificates.leaves[1].name: makes "node_cert", which an input, a generated secret or another
certificate has already`).

**Declared but never used.** Every made name must be used in some overlay secret value, as for a generated
secret: `certificates.ca: makes ca_cert, which is never used in the services`, or
`certificates.leaves[2].name: makes spare_cert, …`. (Per name, not per leaf: a certificate whose key is
never used is a mistake worth a message; a leaf's `_cert` alone can't be left out by accident.)

**What is made.**

- RSA 2048; keys are **PKCS#8** PEM (`-----BEGIN PRIVATE KEY-----`), certificates PEM (`CERTIFICATE`); a
  value is one PEM block ending in a newline. A leaf's certificate is the leaf alone, the authority's is
  `ca_cert`.
- SHA-256 signatures; valid from one hour before the install to ten years after it; random 127-bit serials.
- The authority: `IsCA`, key usage cert sign and CRL sign, self-signed. Its **private key is thrown away**
  once the leaves are signed: nothing can be issued under it later, and nothing keeps it.
- A leaf: key usage digital signature and key encipherment; extended key usage **server and client
  authentication**, so one certificate serves both ends of a connection; its `dnsNames` as SANs; the
  authority's key identifier.
- **The subject is exact.** It is written as the sequence in the reverse of the order given, so RFC 2253 (what a
  Java service such as OpenSearch compares `admin_dn` and `nodes_dn` with) reads it as written:
  `CN=admin,OU=Wazuh,O=Wazuh,L=California,C=US`. A test checks `cert.Subject.String()` and the RFC 2253 text of
  `RawSubject` equal that, and an order Go's own `pkix.Name` can't write (`C=ES,CN=x`) comes out as given.

**Randomness.** The keys and the certificates' random serials come from `crypto/rand`, not from the
`io.Reader` the caller gives `Generate` (since Go 1.26 `crypto/rsa` ignores it). The package's other
randomness, the generated secrets, is still the reader's. The package doc says so.

Format errors name the field: `certificates.ca.subject: needs a CN`,
`certificates.leaves[0].subject[5].type: OU is listed twice`, `certificates.leaves[1].name:`,
`certificates.leaves[0].dnsNames[0]:`, `certificates.leaves: has 9 leaves; at most 8 are allowed`.

## `{{name|bcrypt}}`

The placeholder is `{{ name }}` or `{{ name | filter }}` (spaces optional; group 2 of the expression is the
filter). The one filter is `bcrypt`: the bcrypt hash of the secret, **cost 12**, with the `$2y$` prefix
OpenSearch writes, for a file of password hashes (`internal_users.yml`).

| Rule | Error (naming template and field) |
| --- | --- |
| allowed only on a **secret input** or a **generated secret**, and only where a secret may go (an overlay secret's `value`) | `uses {{domain\|bcrypt}}, but domain is a plain input: a filter hashes a secret`; `uses the secret {{x}}: …` elsewhere |
| not on a made certificate or key | `uses {{node_cert\|bcrypt}}: a certificate's PEM can't be hashed` |
| the only filter | `uses the filter "md5" on {{admin_password}}; the only filter is bcrypt` |
| not in the notes | `notes: uses a filter on {{x}}: notes show a plain input as it was typed` |
| bcrypt reads 72 bytes: a hashed **generated** secret has `length` ≤ 72 | `generated[0].length: is 80, but {{api_password\|bcrypt}} hashes it and bcrypt reads 72 bytes at most` |
| a hashed **secret input** is at most 72 bytes | `inputs.admin_password: is 73 bytes; at most 72 are allowed, since it is stored hashed with bcrypt` (at install, when the owner types it) |

The same secret may be used plain and hashed in one template (`{{api_password}}.Wz1` and
`{{api_password|bcrypt}}`). Each use of a filter is a fresh hash (a new salt). The value hashed is the value
the secret has, so a secret input and its hash are always of one password.

`Check` renders with a **fixed stand-in** for each hash and a stand-in text for each PEM, so it stays as fast as
before however many cases it renders (the choices × optional inputs of 058). A preview
(`Template.RenderPreview`) does the same: it shows secret names, never a value, and makes no hash.

## A secret value may be a text with placeholders

An overlay secret's `value` is any text (up to what the app model allows), with its line breaks: placeholders
are replaced inside it, once, left to right, as before. That is how a whole file (`internal_users.yml`,
`wazuh.yml`) is kept as a secret and mounted by a secret file (060 phase C). Format 2 already allowed it; this
is the use the format now documents.

## Old hosts

- A template with `certificates` has a field a Caravana up to 0.23.x doesn't know: that host lists it as
  **unsupported** ("made for a newer caravana…") and keeps the rest of the catalog (058's tolerance), as does an
  overlay `files` entry. Checked against `v0.23.2`'s own `ParseCatalogFor` on a bundle holding such a template.
- A template that uses only `{{x|bcrypt}}` would be read by a host that knows neither as text it fails to
  substitute; so **a template that uses `certificates` or a filter must set `requires`** (the release of 060)
  and a host older than that never installs it: `requires: is required: the template uses certificates or
  {{name|bcrypt}}, which an older Caravana doesn't know`.
- Neither is ever in `catalog.json` (format 1): the format-1 rules refuse them by name
  (`certificates: are catalog format 2; format 1 has none`, `a filter is catalog format 2`), so `ReadSource`
  leaves the template out of it and lists it among `OnlyV2`. A test keeps it so.

## Functions

```go
func (t Template) Generate(r io.Reader, now time.Time) (map[string]string, error) // secrets from r; certificates from crypto/rand, dated from now
func (t Template) Render(values map[string]string) (compose.Request, error)       // {{x|bcrypt}} is a real hash
func (t Template) RenderPreview(values map[string]string) (compose.Request, error) // a stand-in hash
func (c Certificates) Names() []string                                           // ca_cert, <leaf>_cert, <leaf>_key
```

The API's template answers don't show `certificates` (no schema change: 0.66.0 is unchanged by this phase).
