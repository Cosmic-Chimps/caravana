# Research: Bring your own certificates

What the lab showed on 2026-11-12 ("lab, observed": a second Traefik v3.7.13 on other ports, a Pebble ACME
server with EAB required, a whoami job, all in the lab VM), and what Traefik's source says (v3.6 branch).

## R1 · Traefik's static configuration is one source: a file

Traefik reads its static configuration from a file, or flags, or environment variables, never a mix
(`paerser`'s loaders stop at the first that finds something). EAB can't be an always-present flag: in
`register`, `if p.EAB != nil` registers with external account binding, so `--…eab.kid=` with an empty value
would break plain Let's Encrypt. **Decision**: the whole static configuration becomes a file rendered by a
Nomad template into `secrets/traefik.yml` (`--configFile`), with the `eab` and `caCertificates` blocks only
when set. The HMAC key is never on a command line or in the job. *Rejected*: a shell wrapper building the
flags (the key would be in the process list), env vars (ignored once a flag is given).
Lab, observed: the Nomad provider needs no token setting; it defaults to `NOMAD_TOKEN`, which the task's
workload identity sets.

## R2 · The console applies an issuer change by writing one variable

A template with `change_mode = "restart"` over the variable `caravana/acme` restarts Traefik's task in place
when the variable changes (lab, observed: restart signaled, back in under 2 s, same allocation). So the
console needs no root, no installer run and no updater request. *Rejected*: re-registering Traefik's job
from Caravana (the installer owns its shape; an upgrade would undo it), the updater of 030 (a root path for
something a variable does).

## R3 · A job-scoped policy, applied before the job

Traefik's task reads `caravana/acme` and `caravana/certificates/*` with its workload identity, so the
job-scoped policy `traefik-read` gains `variables` rules. Lab, observed: without the rule the task doesn't
die, it waits (`Missing: nomad.var.get(…)`), also when the template guards with `nomadVarExists`; about a
minute after the policy is applied it starts. So the installer applies the policy before it runs the job
(it already does), and a missing variable (the normal case) renders the defaults.

## R4 · Traefik already skips ACME for a host a provided certificate covers

`getUncheckedDomains` removes the names the TLS store already has. Lab, observed: with a wildcard in the
file provider, a new router that names the resolver logs "No ACME certificate generation required" and the
wildcard is served; when the variable is purged, the same router gets an ACME certificate within 20 s, with
no restart. **Decision**: the router keeps `tls.certresolver` (the translator is untouched). Dropping the
tag, as the task first said, would need a redeploy of every covered app on upload and again on removal.

## R5 · A certificate Traefik already has wins over an uploaded wildcard

`GetBestCertificate` sorts the store's keys (the certificate's names, sorted and joined) in reverse and
takes the first that matches: `a.example.test` sorts after `*.example.test`, so an ACME certificate for the
exact name beats an uploaded wildcard, and ACME keeps renewing it (`renewCertificates` walks every stored
certificate, whatever the routers ask). Lab, observed: after the upload, `a.example.test` kept its Pebble
certificate while `x.example.test` got the wildcard. Two certificates with the same names collide and one
is skipped. **Decisions**: (1) an upload with exactly the names of an earlier upload replaces it;
(2) Caravana reports covered hosts that still present another certificate (`keptEarlier`), from the real
handshake; (3) the installer, which already stops Traefik to reset the store on a change of `--acme`, drops
from every store file the ACME certificates whose every name is covered by an uploaded one. Caravana can't
do (3) itself: the store is a host volume its policy denies, on purpose. A console action for it is a
follow-up task.

## R6 · Certificates as content, in `secrets/`

The file provider's `certFile`/`keyFile` take PEM content. One template lists the variables under
`caravana/certificates` (`nomadVarList`, `nomadVar`) and writes `secrets/dynamic/certificates.yml` with
`toJSON` strings (YAML reads JSON strings), `change_mode = "noop"`; Traefik's watch picks it up in about
10 s. Lab, observed: Nomad's fs API refuses `secrets/` ("Reading secret file prohibited"); a file with no
certificate must be `{}`. So the provider's directory moves from `local/dynamic` to `secrets/dynamic`,
with 023's `team.yml` and the console route. `secrets/` is a 1 MiB tmpfs: 30 certificates at most.

## R7 · Renewal

Traefik renews at start and then daily every certificate that ends within 30 days. Lab, observed: with
Pebble's profile at 20 days, restarting the allocation renewed all three certificates (new serials). The lab
step uses that: a 20-day profile, an allocation restart, a changed serial.

## R8 · One store file per issuer

A change of `storage` starts a new account (lab, observed: the new file, a new registration with EAB, every
router's certificate issued again). The file name is `acme-<12 hex of SHA-256(server + "\n" + key ID)>.json`;
the installer's issuer keeps `acme.json`. Going back finds the old file and its certificates.

## R9 · Which words name an issuer

The handshake's leaf says who issued it (`issuerName`: the organization, else the common name), as 023 shows
it. An uploaded certificate is recognised by the SHA-256 of its leaf, never by names. The presets:
Google Trust Services `https://dv.acme-v02.api.pki.goog/directory`, ZeroSSL
`https://acme.zerossl.com/v2/DV90`, DigiCert `https://acme.digicert.com/v2/acme/directory/`; all three need
EAB.

## R10 · One needs-you item per uploaded certificate

The task said 023's `domain_cert_expiring`. A wildcard on twenty hosts would open twenty items, and one used
only by the console's domain none. **Decision**: kind `certificate_expiring`, key
`certificate_expiring|||<id>`, derived from SQLite on every read; a domain presenting an uploaded
certificate raises no `domain_cert_expiring` (its row still says "certificate expiring").
