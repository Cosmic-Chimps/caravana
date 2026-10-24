# Research: Domains (023)

Read-only research on the code at `022-schedules`, on the installer and the lab, on the vendored
Nomad (`hashicorp/nomad@v1.11.0-rc.1.0.20260917172403-9dcbdc5e64ec` in the module cache, cited as
`nomad:` paths) and on the lab's live Traefik v3.7.13 (queried read-only on 2026-10-24 at
`localhost:8088`, the lab's forward of Traefik's API port; cited as "lab, observed"). No Traefik
source is vendored in this repo or the module cache, so every statement about how Traefik treats a
tag, a middleware or ACME that was not observed in the lab is marked **lab check**. Nothing was
changed in the lab.

Handoff files read: `design/console/design/ui_kits/console/DomainsScreen.jsx` (`DomainStatus`,
`AddDomainPanel`, `DomainsScreen`) and `CertsScreen.jsx`.

## Findings that change the plan's wording

`docs/plan.md` row 023 says "Every option is a Traefik middleware on the existing router, so
constitution VI's router name is untouched". Five facts make that harder than it reads. The spec
must settle each; the R-items below carry the detail.

1. **One router serves all of a service's domains** (`routerTags`, `internal/nomadjob/translate.go:380-396`),
   and Traefik attaches a middleware to a router, not to one host of its rule. A per-domain switch
   ("Team only" on `admin.x` but not on `www.x`, same service) needs a router per domain that has
   options. That is a second router name beside `<app>-<service>`, which constitution VI spells out.
   R4 recommends a PATCH clarification and keeps every domain without options exactly where it is.
2. **HTTP to HTTPS is already global on a real host**: the installer's Traefik redirects the whole
   `web` entrypoint (`deploy/install/traefik.nomad.hcl:91-92`). The lab's Traefik has no redirect
   (`deploy/dev/traefik.nomad.hcl:27-37`). "Force HTTPS" therefore cannot be a per-domain switch
   that turns off; R4 says what it can be.
3. **Traefik's API doesn't expose certificate expiry** (lab, observed: `/api/tls/certificates` is
   404). The expiry has to come from the TLS handshake (R2).
4. **A domain can't have its own port** with the model's single registration per service:
   `reg.PortLabel = svc.Domains[0].Port` (`translate.go:341`) and `validateDomains` makes every
   domain of a service use the same port (`internal/app/validate.go:351-354`).
5. **The handoff's story "Team only blocks the certificate renewal" is not how Traefik's HTTP-01
   works**: the challenge is answered by Traefik itself on `web` before any router or middleware
   (R4, **lab check**). The designer's detail panel for `bi.[your-domain]` ("Let the check through
   and renew") describes a failure Caravana can't have; the designer should rule on what that row
   says instead.

---

## R1. What Caravana knows about a domain today

**Decision**: build 023 on what exists: a domain is `{Host, Port}` stored in the app, one router per
service in the job's tags, the console's Domains tab already adds and removes through `DeployIf`.
Everything else on the page (status, expiry, DNS) is a read made on demand, not stored in the app.

**Facts**

- The model: `app.Domain{Host, Port}` (`internal/app/app.go:208-214`): `Host` is a lowercase
  hostname unique across the app, `Port` the name of a port of the same service. `Service.Domains`
  is at `app.go:83-85`. Validation (`validateDomains`, `validate.go:337-358`): `validHost` (labels
  of `labelRE`, `validate.go:361-371`, so no `*` and no backtick or `||` in the rule), host unique
  *within the app* (the `hosts` map, not across apps), the port exists, and all of a service's
  domains use `Domains[0].Port`. Across apps nothing stops two apps from claiming one host:
  Traefik then picks by rule length and a tie is arbitrary. Only the console's own host is refused
  (`checkConsoleDomain`, `internal/service/consoledomain.go:23-40`; the installer gives that route
  priority 10000, `traefik.nomad.hcl:74`).
- The translation (`internal/nomadjob/translate.go`):
  - `registration` (`:324-346`): a service with ports gets one Nomad service named `<app>-<service>`
    (`:335`, provider `nomad`); with domains its `PortLabel` is `Domains[0].Port` and its `Tags`
    are `routerTags(name, svc.Domains, s)`; `CanaryTags` is the single tag `caravana.canary=true`
    (`:362-375`), so a canary has no `traefik.enable` and Traefik doesn't route it.
  - `routerTags` (`:380-396`) writes exactly: `traefik.enable=true`,
    ``traefik.http.routers.<router>.rule=Host(`a`) || Host(`b`)``,
    `.entrypoints=<Settings.Entrypoint>`, `.tls=true` and, when `Settings.CertResolver != ""`,
    `.tls.certresolver=<resolver>`. `<router>` is `<app>-<service>`. The Traefik service is the
    Nomad registration itself, which Traefik's Nomad provider names the same, so there is no
    `service` tag.
  - `Settings{Entrypoint, CertResolver}` (`:23-30`) come from the entrypoint flag and
    `CARAVANA_CERT_RESOLVER` (`internal/config/config.go:91,153`). The installer writes
    `CARAVANA_CERT_RESOLVER=letsencrypt`, or empty for `--acme none`
    (`deploy/install/install.sh:395-410`); the lab runs with it empty (Traefik's default cert,
    `deploy/dev/traefik.nomad.hcl:6`).
  - The golden `internal/nomadjob/testdata/letsencrypt.golden.json` and the lab's
    `deploy/dev/whoami.json:39-41` show the tags in a job; the lab console step reads them back
    (`cmd/caravana/console_page2_test.go:71`).
- The console (`internal/ui/domains.go`, 021 page 2): `domainsTab` (`:25-39`) is one app's page,
  `CanEdit` is `Defined && !Kept && !Stopped` (`:47`), so **an app that keeps a Compose file (every
  Aspire and Compose app) can't add or remove a domain here**: the page answers with
  `fileEditedSentence` (`addDomain` `:234-317`, `removeDomain` `:319-372`). Add reads the stored
  app, appends `app.Domain{Host, Port}` to the service and calls
  `s.svc.DeployIf(ctx, edited, service.Condition{IfRevision: revision}, app.TriggerConsole)`
  (`:289-291`; Remove the same, `:353`). `domainPort(svc)` picks the port; a service with several
  ports can't take a second port for a domain.
- The DNS record box: `recordFor` (`:152-158`) gives type `A`, name from `recordName`
  (`:90-100`: `@` for a host with one dot, the first label otherwise; no public-suffix list, so
  `example.co.uk` reads as a subdomain) and the value from `hostIP.find` (`:103-150`): the IPv4 the
  **console's own host name** (`CARAVANA_PUBLIC_URL`) resolves to through
  `net.DefaultResolver.LookupIP`, 2 s timeout, loopback and unspecified ignored, a success kept for
  the process life, a failure kept a minute. Without `CARAVANA_PUBLIC_URL` (the lab) the box says
  "this host's public IP address" (`hostIPWords`, `:83`). The installer already refuses to install
  unless the console domain resolves to the machine's public IP (`install.sh:156-168`), which is
  why this is a sound source on an installed host. R3 needs the same lookup, so it becomes a
  shared helper rather than a UI detail.
- Compose and overlay: `compose.Domain` (`internal/compose/compose.go:81`) and
  `internal/compose/service.go:162-171` turn the overlay's domains into `app.Domain{Host, Port}`;
  any new per-domain field must be added to the overlay too, or a Compose app can never use it.
- Promotion keeps the target's own domains (`internal/service/promote.go:214-217`); copy removes
  them (`promote.go:325,370,376`). Options live with the domain, so they follow the same rules.
- The gate checks a domain by the TLS server-name trick: `hostStatus`
  (`cmd/caravana/gate_projects_test.go:44-66`) dials `127.0.0.1:8443` (lab) or `<public IP>:443`
  (host) and sets `tls.Config.ServerName = host` with `InsecureSkipVerify`, so Traefik routes by
  name where DNS has no record; `labClient` (`cmd/caravana/lab_test.go:244-253`) does the same for
  `whoami.localhost`. The lab maps guest 443 to the Mac's `8443`, 80 to `8080` and Traefik's API
  `8081` to `8088` (`deploy/dev/lab.yaml:45-52`).

**Rationale**: the page needs one thing the model lacks (options per domain) and four facts it
derives (DNS, certificate, reachability, router accepted). Keeping the facts out of the stored app
keeps `revision` and every golden stable.

**Alternatives**: storing certificate facts on the app (rejected: changes `revision` on every probe
and goes stale at once).

---

## R2. Certificate state

**Decision**: option (c). Caravana reads the certificate **from the TLS handshake**: dial the local
Traefik edge (`127.0.0.1:443` on an installed host; a setting for the lab's `127.0.0.1:8443`) with
`ServerName` set to the domain, take the leaf certificate's `NotAfter`, issuer and names, and
classify. Nothing new to install, nothing readable that shouldn't be, same code in the lab and on a
host.

**What each option gives**

(a) **Read `acme.json`**. Rejected on security and on reach.
- Where: the installer declares the Nomad host volume `traefik-acme` at
  `/var/lib/caravana-host/traefik` (`deploy/install/nomad.hcl.tpl:32-36`) and Traefik writes
  `/acme/acme.json` (`traefik.nomad.hcl:107`, `install.sh:26,364`). Format (Traefik's, **lab
  check** on a host): `{"letsencrypt":{"Account":{...},"Certificates":[{"domain":{"main","sans"},
  "certificate":"<base64 PEM chain>","key":"<base64 PEM private key>"}]}}`. Parsing `NotAfter`
  would need only `certificate`.
- Not reachable by design: `$STATE` is `chmod 700` root (`install.sh:272`), Caravana runs as user
  `caravana` under `ProtectSystem=strict` with `ReadWritePaths=/var/lib/caravana` only
  (`deploy/install/caravana.service:9-10,17-20`), and its Nomad policy **denies** the volume
  (`deploy/install/caravana-policy.hcl:40-42`, `host_volume "traefik-acme" { policy = "deny" }`).
- The same file holds the **private key of every certificate and the ACME account key**. Opening it
  to the API-facing process (a group ACL, a copy) is the wrong trade for a date. Also: there is no
  `acme.json` in the lab, and changing `--acme` mode deletes it (`install.sh:363-370`).

(b) **Traefik's API**. Useful for one thing, not for expiry.
- Enabled: installer `--entrypoints.traefik.address=127.0.0.1:8081`, `--api.dashboard=true`,
  `--api.insecure=true` (`traefik.nomad.hcl:95-97`); Traefik is in host network mode, so
  `127.0.0.1:8081` is the host's loopback, where Caravana (a host process) can call it. The lab
  binds `:8081` in the VM (`deploy/dev/traefik.nomad.hcl:32-34`), which Lima forwards to the Mac's
  `8088`.
- Lab, observed: `/api/version` answers `3.7.13`; `/api/http/routers`, `/api/http/services`,
  `/api/http/middlewares`, `/api/entrypoints`, `/api/overview` and `/api/rawdata` exist;
  `/api/tls/certificates` is **404**. A router entry shows `status` ("enabled"; Traefik adds
  `error` and status `disabled` when a middleware is missing or the rule is invalid, **lab
  check**), `tls: {options: default}` (and `certResolver` when set), `using`, `provider`
  (`hollo-web@nomad`); a service entry shows `serverStatus` per backend (`UP`). **No certificate
  dates anywhere.** `/api/overview` has a `certificates` counter object (observed `total 0` in the
  lab); what it counts on a host with ACME is a lab check on the VM, and it can't carry dates.
- Where it earns a place: "the route was accepted" (`status: enabled`, no `error`) and "the
  backend is UP", which separates "Traefik refused this router's middleware" from "the app is
  down". Both are also visible through reachability (R3), so (b) is an optional follow-up, not a
  dependency. If added: a setting `CARAVANA_TRAEFIK_API` (default `http://127.0.0.1:8081`, the lab
  `http://127.0.0.1:8088`), loopback only, 2 s timeout, GET only.

(c) **TLS dial**. Chosen.
- `tls.DialWithDialer(&net.Dialer{Timeout: 3s}, "tcp", edge, &tls.Config{ServerName: host,
  InsecureSkipVerify: true})`, then `ConnectionState().PeerCertificates`. `InsecureSkipVerify` only
  lets the handshake finish; Caravana then verifies by hand (`leaf.VerifyHostname(host)`,
  `leaf.Verify` against the system roots with the other presented certificates as intermediates),
  so "trusted", "not trusted" and "wrong name" are separate facts. Nothing is sent after the
  handshake, so no data leaves.
- Lab, observed (2026-10-24, `openssl s_client -connect 127.0.0.1:8443`): `-servername
  hollo.localhost` (a real route) and `-servername nothere.example.com` (no route) both return
  `subject=CN=TRAEFIK DEFAULT CERT issuer=CN=TRAEFIK DEFAULT CERT`, valid one year. That subject is
  the signal for "Traefik has no certificate for this name": either no resolver is configured
  (lab, `--acme none`) or the resolver hasn't obtained one yet.
- Classification (host `h`, resolver known from `Settings.CertResolver`):

  | handshake | resolver set | state |
  |---|---|---|
  | leaf names include `h`, issuer not Traefik's, chain verifies | any | **valid**, with `NotAfter` |
  | leaf is Traefik's default | yes | **getting certificate** (ACME pending or failing; DNS (R3) says which is likely) |
  | leaf is Traefik's default | no | **Traefik's own certificate** (lab or `--acme none`; not an error) |
  | chain doesn't verify, issuer says "(STAGING)" | yes | **staging certificate** (host installed with `--acme staging`; `deploy/vm/host.sh:91` can do it) |
  | `NotAfter` within the warn window (R5) | yes | **expiring** (a renewal should have happened: see below) |
  | dial refused or times out | any | **edge unreachable** (a fact about Traefik, not the domain) |

- Renewal date: Traefik renews a Let's Encrypt (90-day) certificate when 30 days remain (its
  default `certificatesDuration` 2160 h, **lab check** on a host). So "renews around"
  `NotAfter - 30d` is an estimate, and "expiring" at 14 days means two weeks of failed renewals
  already: a real alarm, not noise. Draw `expires <date>` as the fact and `renews around <date>`
  as the estimate.
- Why the reason of a failed issuance isn't in the handshake: Traefik logs it
  (`Unable to obtain ACME certificate for domains "<d>": ...`, level ERROR, **lab check**) and the
  log level is INFO (`traefik.nomad.hcl:110`). Caravana's token has `read-logs` on the `default`
  namespace (`caravana-policy.hcl:2`), and Traefik is a job there, so an optional "Why?" could read
  the tail of Traefik's logs through the same call the Logs tab uses and show the last line that
  names the domain. Not required for 023's statuses; log lines hold no secrets; show them as text.
- Lab vs host: the address differs. On a host Traefik's `websecure` is `:443` on the node
  (`traefik.nomad.hcl:35`, host network), so `127.0.0.1:443`. In the lab the in-process Caravana
  is on the Mac and reaches it through Lima's forward `127.0.0.1:8443`. A setting, e.g.
  `CARAVANA_EDGE_ADDRESS` (default `127.0.0.1:443`; `TestLab` and `lab.sh` set `127.0.0.1:8443`),
  keeps it out of the code.

**Rationale**: (c) works for every domain, on every host, with no permission change, and it
measures what visitors get, which is what "live" means. Its limit, an expiry only for names
Traefik serves, is the right limit: a domain with no route has no certificate to report.

**Alternatives**: (a) and (b) above; a sidecar that writes a sanitized JSON of `acme.json`
(rejected: a new moving part for a date the handshake already gives, constitution I).

---

## R3. DNS and reachability checks

**Decision**: a **check** is three independent facts, each with its own timeout, run in parallel:
DNS (resolve and compare), certificate (R2) and route (a request through the local edge). The
status pill is derived from them by one pure function, so the order of precedence is tested.

**DNS ("points elsewhere")**

- Resolve with Go's `net.Resolver`: `LookupIPAddr` follows CNAME chains by itself, so a CNAME to a
  name that ends at this host is "points here"; `LookupCNAME` is only for showing the chain.
  Timeout 2 s per resolver (the existing `hostIP` already uses 2 s, `ui/domains.go:117-118`).
- Which resolver: the host's system resolver (Ubuntu 24.04: systemd-resolved, stub `127.0.0.53`)
  answers from a cache that can lag a change or hold a negative answer. The handoff says "Found
  the record on 3 public resolvers". Recommend asking the system resolver **and** two public ones
  directly (a `net.Resolver{PreferGo: true, Dial: ...}` to `1.1.1.1:53` and `8.8.8.8:53`; the
  firewall allows all outbound, `install.sh:337`), in parallel; an override list in a setting for
  air-gapped hosts. A result is "here" only if every resolver that answered includes the host's IP,
  "elsewhere" if any answered with addresses that don't, "not found" for NXDOMAIN on all, "can't
  tell" when none answered. Show the addresses seen ("now: 185.199.108.153") as the handoff's
  detail does.
- The host's address: reuse `hostIP` (R1). It is IPv4 only and comes from the console's own name,
  so on a machine with several public addresses it is one of them. The lab has no
  `CARAVANA_PUBLIC_URL`, so the page says "can't tell without CARAVANA_PUBLIC_URL" and shows only
  what the name resolves to; a name that resolves to loopback (`*.localhost`) is "local".
- Apex vs subdomain: the A-record advice already exists (`recordName`); the edge CNAME is Phase 3
  (plan row 023), so the box always says A. A domain that has **an AAAA record** matters: Let's
  Encrypt prefers IPv6 when present, the installer only checks IPv4 (`install.sh:163`), and
  Caravana gives the host no IPv6 identity. An AAAA answer that isn't one of the host's addresses
  is its own line ("has an AAAA record to <addr>; Let's Encrypt will try it first"), a warning, not
  "elsewhere".
- A wildcard record at the DNS provider is invisible and fine (the lookup of the concrete name
  succeeds). A wildcard **domain in the app** isn't possible (`validHost`); `*.x` previews are
  Phase 4.
- A DNS provider's proxy (Cloudflare "orange cloud") makes the name resolve to the proxy, not the
  host: "elsewhere" by this rule even though the site may work. Precedence handles it: if the
  certificate is valid and the route answers, the status is **live** and the DNS line says "points
  at <ip> (a proxy?)" as information.

**Reachability ("live")**

- A request **through the edge, not through the Internet**: a GET to `https://<edge>/` with
  `ServerName` and `Host` set to the domain (the gate's trick, `gate_projects_test.go:50-66`),
  redirects not followed, no cookies, no credentials, 5 s total, and only the first 64 bytes read
  and dropped after classification. The target address is fixed (the edge setting), never derived
  from the domain, so a domain can't make Caravana call anything else; only a host that is in a
  stored app gets this probe (the Add form's pre-deploy check runs DNS only).
- Reading the status (lab, observed): an unknown host answers **404** with
  `content-type: text/plain; charset=utf-8`, `content-length: 19`, body `404 page not found`
  (Traefik's own), while a routed host answers with the app's status (`hollo.localhost` 200).
  Rules: Traefik's 404 signature = **no route**; 502/503/504 = **route accepted, app not
  answering**; 401 = **live, protected** (Team only); any other status, including an app's own
  404 or a 3xx = **live**. The body is never kept or shown.
- "Reachable from outside" can't be proven from the host itself (no hairpin guarantee). What proves
  it is a **trusted Let's Encrypt certificate**: HTTP-01 reached port 80 from the Internet when it
  was issued. The Add panel says so in those words instead of claiming a probe it can't make. An
  outside prober would be a runtime dependency (constitution I): no.
- Status precedence for the pill (handoff's `DomainStatus`): app stopped; then **DNS points
  elsewhere** (DNS "elsewhere" and not (certificate valid and route live)); then **certificate
  expiring**; then **getting certificate**; then **redirects** (a www redirect makes that host's
  row say "redirects to ..."); else **live**. The lab-only "Traefik's own certificate" reads "live ·
  own certificate", not a problem.

**Alternatives**: DNS-over-HTTPS (rejected: a third-party call per check); the system resolver only
(rejected: its cache hides the propagation the page exists to show).

---

## R4. Per-domain options as Traefik middlewares through tags

**Decision** (the model and the translation):

1. `app.Domain` gains optional fields, all `omitempty`, so no stored app or `revision` moves until
   one is set:

   ```go
   type Domain struct {
       Host      string    `json:"host"`
       Port      string    `json:"port"`
       Path      string    `json:"path,omitempty"`      // "/api": PathPrefix in the rule; starts with "/"
       StripPath bool      `json:"stripPath,omitempty"` // remove Path before the app sees the request
       WWW       string    `json:"www,omitempty"`       // "redirect": the other of www.<h> and <h> redirects here
       TeamOnly  *TeamOnly `json:"teamOnly,omitempty"`  // names only, like secrets
   }
   type TeamOnly struct{ Users []TeamUser } // TeamUser{Name, Password?}: Password write-only, never stored
   ```

2. **A domain with any of these options gets its own router**, named
   `<app>-<service>-<8 hex of sha256(host)>`; domains with no options stay in the base router
   `<app>-<service>` exactly as now (so every existing job, golden and the lab's
   `console_page2_test` read stay valid). The Traefik service stays the one Nomad registration; the
   extra routers bind to it by default (a router without a `service` tag binds to the registration
   it was defined in, **lab check**). The name uses a hash because router names live inside
   dot-separated tag keys, where a `.` from a host can't appear.
3. This needs a **PATCH clarification of constitution VI** ("router name `<app>-<service>`" becomes
   "the service's router is `<app>-<service>`; a domain with its own options gets
   `<app>-<service>-<hash>`"), made through `/speckit-constitution` in its own commit. The fallback
   without an amendment: options belong to the **service** (all its domains), the page draws the
   switch on each row and says "applies to all 3 domains of web". Rejected as the default because
   the handoff's page is per domain and the common case (`www` plain, apex redirecting, one
   service) breaks under it.
4. `Translate` stays pure: the tags of a router are a function of the app, the domains' options and
   `Settings`. The only impure part (Team only's hashes) is written by `deploy` before `Register`,
   like secrets (008) and registry credentials (017).

**Exact tags** (Traefik v3, Nomad provider; same keys as the Docker labels; names are scoped by
provider, so a router's `middlewares` tag names `<m>` or `<m>@nomad`). `R` is the router name; each
option adds one middleware named `R-<option>`:

- *Path* (`Path: "/api"`): the rule becomes ``Host(`d`) && PathPrefix(`/api`)`` (a longer rule, so
  a higher default priority than the plain host router, **lab check**). With `StripPath`:
  `traefik.http.middlewares.R-strip.stripprefix.prefixes=/api`, which also sends
  `X-Forwarded-Prefix: /api` (whoami echoes headers, so the lab can assert it).
- *www redirect* on host `d` (an apex-style or `www.` host only; the other host `o` is `www.`+`d`,
  or `d` without `www.`): the rule is ``Host(`d`) || Host(`o`)`` (so ACME asks for both names and
  one certificate covers both, **lab check**), and
  `traefik.http.middlewares.R-www.redirectregex.regex=^https://<o with [.] for dots>/(.*)`,
  `.replacement=https://d/$1`, `.permanent=true`. Use `[.]` for dots and `$1` rather than `${1}`:
  Nomad runs `${...}` through its interpolator on every tag (`nomad:client/taskenv/services.go:57`
  calls `ParseAndReplace`, whose pattern is `\${[a-zA-Z0-9_\-\.]+}`, `nomad:helper/args/args.go:9`;
  an unknown name is left as written, but not relying on that is safer), and `nomadjob.HCL`
  already escapes `${` for the export (`internal/nomadjob/hcl.go:281-323`). Status: Traefik v3
  answers a permanent redirect with **308** (**lab check**; the tests assert the `Location` and
  accept the code Traefik 3.7.13 really sends). The visitor needs a DNS record for `o` too, so Add
  domain asks for both and the check covers both.
- *Team only* (basic auth): `traefik.http.middlewares.R-auth.basicauth.users=<user>:<hash>`,
  `.realm=<app id>`, `.removeheader=true`. Traefik accepts MD5 (apr1), SHA1 and bcrypt hashes.
  **Where the hash lives** is the one open decision, see below.
- *Order*: `R.middlewares=R-auth,R-www,R-strip` (auth first so nothing is redirected or rewritten
  before it, the redirect before the rewrite).
- *Force HTTPS*: see below.
- *Port*: no tag. `PortLabel` is per registration and Traefik's service is per registration, so a
  domain can't go to another port without a second Nomad service. The existing validation (same
  port for all of a service's domains) stays. "Change path or port" becomes **"Change path"** plus
  a "Goes to" selector for the **service** port that applies to all of its domains; moving one
  domain to another service or port is Remove then Add (the page already does both). A second
  registration per extra port (`<app>-<service>-<port>`, its own health check) is a real design for
  a later feature; nothing in the handoff needs it except the button's label.

**Force HTTPS**: on a host the `web` entrypoint redirects everything to `websecure`
(`traefik.nomad.hcl:91-92`), and Let's Encrypt issuance works through it (the Phase 1 gate and every
installed host get real certificates under it). A per-router middleware can't turn that off (a
router bound to `websecure` never sees port-80 traffic). Options:
- *Recommended*: **draw it as a switch that is on and can't be changed** ("HTTPS only: every
  address redirects to https; set by the host's Traefik"), because that is the truth on every
  installed host and is the safe default; in the lab (no redirect) the line says so. No tag.
- A real off switch needs the global redirect removed from the installer's static config (a Traefik
  restart: a gap on 80/443 and its ACME state, as 019's research found for `namespaces=*`) and a
  second, `web`-bound router per domain with a `redirectscheme` middleware: two routers per domain
  and an installer change for a switch nobody should turn off. Deferred; if a user needs plain HTTP
  for one app (an IoT device), that is a feature of its own.

**Team only: where the hash lives**

| | A. hash in the tag | B. hash in a Nomad Variable, rendered into Traefik's file provider |
|---|---|---|
| Job, export, records | hold the hash (`basicauth.users` tag) | hold only `R-auth@file` |
| Offline guessing | anyone with `read-job`, or the dashboard (`/api/http/middlewares` lists a middleware's options, lab-observed shape) can try the hash | no copy outside the variable and Traefik's rendered file |
| Constitution VII and the 008/017 rule ("no job, export, record or log has a secret") | bends it (a hash is a verifier, not the password) | keeps it |
| Changes needed | `Translate` only | the installer's Traefik job (a second `template`, and `--providers.file.directory` in place of `.filename`), the lab's Traefik job, one Traefik restart, a variable `nomad/jobs/traefik` written by Caravana |
| Failure mode | none new | a router naming a missing middleware is disabled by Traefik (fails closed: 404), so the variable is written before `Register`, and old names are kept until the old version is gone (content-addressed names: `R-auth-<8 hex of the users string>`) |

- B's mechanics: Traefik's task already has `identity { env = true }` (`traefik.nomad.hcl:52`);
  Nomad gives a task read access to `nomad/jobs/<job>` of its own job (`nomad:acl/acl.go:698-717`,
  as 022's research R1 found), so a template can read `nomadVar "nomad/jobs/traefik"` with no
  policy change, and Caravana's token already writes `nomad/jobs/*` (`caravana-policy.hcl:7-9`).
  The template, with `change_mode = "noop"`, renders `http.middlewares.<name>.basicAuth.users`
  into a YAML file Traefik's file provider watches (the installer's `caravana.yml` template is the
  precedent, `traefik.nomad.hcl:67-85`; `nomadjob.variableTemplate` is the precedent for reading a
  variable, `internal/nomadjob/backup.go:172-181`). **Lab check**: that a changed variable
  re-renders the file and Traefik applies it without a restart, and the template syntax for
  ranging a variable's items.
- Hashing: Caravana has PBKDF2 (`internal/service/auth.go:5,53`) but Traefik needs an htpasswd
  format. bcrypt means `golang.org/x/crypto/bcrypt`, a new direct module (today `go.mod` has only
  `x/term` direct, `:16`; `x/sys` and others indirect, `:48-55`), to justify in the commit as
  constitution I demands. apr1 (MD5) is 40 lines of stdlib but weak. bcrypt, cost 10.
- **Recommendation: B**, with A as the stop-gap only if the maintainer wants 023 without an
  installer change. Rationale: the line since 008 is that values never sit in the job, and "Team
  only" is exactly a credential. The cost is paid once, by an installer rerun like those other
  features already ask for. If A is chosen, the console must say the hash is in the job and the
  export, and a test asserts only hashes, never a password, appear anywhere.
- Model and API for either: users are names in the app (`TeamOnly.Users[].Name`); the password is
  write-only on the request (`password`, like a secret's `value`), shown once if generated; no
  value keeps the current one. No page, answer, export, log or record holds a password (tests
  search for a sentinel, as 008 and 017 do).
- The ACME story: the challenge `/.well-known/acme-challenge/<token>` is answered by Traefik's
  built-in HTTP challenge handler on the `web` entrypoint ahead of routers and their middlewares,
  so basic auth on an app router can't block it (**lab check**: a Team-only domain on a host must
  renew). This contradicts the handoff's `bi.[your-domain]` panel; the designer should drop that
  failure, or keep it only for a future forward-auth sign-in (plan.md:446-466, Umbral), which is
  also a router middleware and would need this review.

**How it fits the rest**

- *HCL export*: tags are strings in `api.Service.Tags`, already written by `nomadjob.HCL`; the
  goldens gain lines. The reflection test (every field `Translate` sets is written) needs nothing
  new. The export header (`internal/deploy/export.go:82-88`) lists what the job needs from Traefik
  and gains, for B, "the middleware file for Team only" and the variable path.
- *Revision*: the options are part of the app document, so `revision` changes when one changes and
  `If-Match` works as for any edit. Password values are not in the document.
- *Validation* (`validateDomains`): `Path` starts with `/`, no backtick, space, `||` or `&&`, at
  most 200 bytes; `WWW` only for an apex-style or `www.` host (`recordName` decides) whose other
  host isn't another domain of any app; `TeamOnly.Users` names `[a-z0-9._-]`, 1 to 5 users; every
  error names its field (`services[0].domains[1].teamOnly.users[0].name`, constitution IV).
- *Compose apps* (most real ones): the overlay's domains (`compose.Domain`) get the same fields,
  and the Domains page, which today refuses a kept-file app (`ui/domains.go:47,234`), edits the
  kept request's overlay the way `editSecrets` rewrites its secret names (015), then deploys with
  `If-Match`. Without that, the page can't change a single option for an Aspire app. This is the
  largest non-obvious cost in 023 and belongs in the spec's scope.
- *Promotion and copy*: a promotion keeps the **target's** domains, so options stay the target's;
  copy removes domains (and with them any Team-only users), so no credential is copied.
- *Rollout*: two registrations with differing tags for the same router exist while an old copy
  drains; the comment at `translate.go:362-375` records that Traefik dropped a router for about a
  second in a similar overlap. A deploy that changes only options needs a **lab check** (the gate's
  `app-actions` step already calls a domain every 200 ms during a restart).

**Alternatives**: one router with `||` and all options per service (the amendment-free fallback
above); `redirectscheme` per router (cannot work on a `websecure`-only router); Traefik's
`forwardauth` for Team only (needs a service to call: Umbral's piece, plan.md:446).

---

## R5. Issuer settings

**Decision**: the issuer section of the page is **read-only in 023**: issuer mode, contact email and
the warn-before-expiry threshold. The first two are facts the installer already holds; the
threshold is Caravana's own and is the one editable value.

- Today: the contact is `--acme-email`, default `--email` (`install.sh:13,105`), passed to the
  Traefik job as `acme_email` (`:374-376`), which becomes
  `--certificatesresolvers.letsencrypt.acme.email=...` (`traefik.nomad.hcl:106`); the mode is
  `--acme production|staging|none` (`install.sh:12,129`, CA URL at `traefik.nomad.hcl:25`). Both
  are recorded in `$STATE/install.env` as `CARAVANA_ACME` and `CARAVANA_ACME_EMAIL`
  (`install.sh:502-507`), a root-only file Caravana can't read. Caravana's own env gets only
  `CARAVANA_CERT_RESOLVER` (`install.sh:405`).
- Traefik can't change it live: certificate resolver options are **static configuration**, read at
  start; the installer already treats a change of mode as "stop Traefik, delete `acme.json`, start"
  (`install.sh:360-372`) and any Traefik job change restarts it (an 80/443 gap). Changing the email
  makes Traefik register a new ACME account on next start (**lab check**; certificates are kept).
- So the console's "Let's Encrypt contact" can only be: shown, with the command that changes it
  (`sudo bash install.sh --acme-email new@example.com`, which also restarts Traefik once). To show
  it, the installer writes `CARAVANA_ACME` and `CARAVANA_ACME_EMAIL` into Caravana's env file (two
  lines, `install.sh:402-410`); a host not rerun says "set when you next run the installer" and the
  page still works. This follows 018's `CARAVANA_PUBLIC_URL`, which also needed a rerun. Editing
  from the console becomes possible when Phase 3's per-node agent exists (re-running the Traefik
  job needs more than Caravana's scoped token should do, and a restart gap deserves a confirmation
  page).
- Not carried over from `CertsScreen`: the ZeroSSL fallback, "Wildcard certificates" with a DNS
  provider token, "Upload your own", and "Cluster certificates" (plan row 023 and row 322 say the
  cluster parts wait for Phase 3; Traefik has one resolver, HTTP-01, no wildcard). The designer
  should rule that those rows are absent, not disabled.
- **Warn before expiry**: a needs-you kind `certificate_expiring` (`AttentionKind`,
  `internal/service/attention.go:16-30`; one item per host: app, service, host, `NotAfter`).
  Threshold default **14 days**, as the handoff has it; a Caravana setting (`CARAVANA_CERT_WARN_DAYS`,
  stored in SQLite if edited in the console), not a Traefik one. `Attention` is "derived on every
  read" from stored facts and Nomad (`attention.go:40-45` doc), so it can't dial every host each
  time. A small **cert watcher** (like `schedulewatch`, `internal/service/schedulewatch.go`) probes
  each stored domain every 15 minutes and writes `domain_checks(host, app_id, checked_at, not_after,
  issuer, state, dns, route)` (a migration); `Attention` reads that table, and a failed probe never
  opens or resolves an item by itself (as the notifier treats copy kinds when Nomad can't be read,
  018). The table also feeds the page's first paint ("checked 3 min ago"). The notifier's key
  `kind|app|service|deployment` (018) needs the host for this kind. With a 30-day renewal, a 14-day
  alarm fires only after renewals failed for two weeks; also open an item for **getting
  certificate for more than 1 hour** (a domain pointing here whose issuance doesn't finish), which
  is the case users hit first.
- The new kind appears in four places that enumerate kinds (the service, notify rendering, the
  API's enum in `api/openapi.yaml`, the console's sentence builder `internal/ui/attention.go`);
  the spec's tasks should list all four.

**Alternatives**: letting the console set the email through a Nomad Variable that the Traefik
template reads (rejected: the ACME email is a CLI argument, so a restart is needed anyway); giving
Caravana a root helper to rerun the installer (rejected: a new privilege for a rarely changed
value).

---

## R6. Where the page lives and the data flow

**Decision**: a workspace page **`/domains`** (a nav item beside Home, Projects and Schedules,
`internal/ui/templates/shell.html:25-27`), listing every app's domains from the stored apps plus
per-domain live facts. The per-app Domains tab (021 page 2) stays and links to it.

- Data: `Service.StoredApps` (`internal/service/settings.go:12-14`: "a plain read of SQLite", so
  the page draws when Nomad is down) gives host, app, service, port and options; app status comes
  from `ListApps`; live facts come from a new `Service.DomainFacts` that reads the 15-minute table
  (R5) for first paint and refreshes the visible hosts with the probes of R2 and R3.
- Probing: on page load draw at once from the table ("checked 3 min ago"), then refresh the hosts
  on screen in the background with a **60 s cache**, at most **8 probes in parallel**
  (`usageParallel` is the precedent, `internal/deploy/usage.go:32`), each with its own 2 to 5 s
  timeouts, and only **while the page's live stream is open** (the Running tab reads CPU and memory
  every 5 s only while `?tab=running` is open, `internal/ui/live.go:21,28-36`). The page works as
  plain HTML: the refresh is the `/parts/domains` region plus a "Check now" form button that POSTs
  and redraws.
- A duplicate host across apps is a fact the page can state ("two apps claim this address") from
  the stored apps alone; it is cheap and prevents a silent routing tie (R1).
- A stopped app's domains show "app stopped" and are not probed.
- API (constitution V; `api/openapi.yaml` changes in the same commit, next minor after the current
  version):
  - `listDomains`, `GET /v1/domains`: every domain with `appId`, `project`, `environment`,
    `service`, `host`, `port`, `path`, `stripPath`, `www`, `teamOnly` (user names only) and `facts`
    (`state`, `dns{state, seen[], expected}`, `certificate{state, issuer, notAfter,
    renewsAround}`, `route{state}`, `checkedAt`). `?live=true` refreshes instead of reading the table.
  - `checkDomain`, `POST /v1/domains/check` `{host, appId?}`: a live check. Without `appId` (the Add
    form before anything is deployed): DNS only plus the record to create; with it, all three.
    `Cache-Control: no-store`. Rate-limited per session (a check dials out): 30 per minute.
  - `getCertificateSettings`, `GET /v1/settings/certificates`: `{issuer: "letsencrypt" |
    "letsencrypt-staging" | "traefik-default" | "unknown", contact, warnDays, edgeAddress}`; a `PUT`
    for `warnDays` only.
  - Per-domain options go through `deployApp` and `deployCompose` as today (`If-Match`, the same
    `DeployIf`), no new write route. The page's switches are forms that build the edited app and
    call it, as Add and Remove do now.
  - **Deploy tokens stay denied** with no code: `scopeRules` is default-deny
    (`internal/api/scope.go:26-43`) and the three new routes aren't in it;
    `TestScopedTokenWalksEveryRoute` forces the decision in `allowedForDeployTokens`
    (`internal/api/scope_test.go:13-17`). `deployApp` stays allowed for the token's own app, so a
    deploy token could set `teamOnly` on its app. That is within its scope (it can already redeploy
    anything about that app), but a password must never be echoed.
- The run-command and terminal precedents (020.5, 020.6) say a probe's output is never stored or
  logged. Here the only stored things are state words, dates, issuer name and the addresses DNS
  returned; the route probe's 64 bytes are classified and dropped.

**Alternatives**: a table of live facts written by the page's requests (rejected: the watcher in R5
already writes it); probing from the browser (no: CORS and a different trust position).

---

## R7. Lab tests

**Decision**: assert in the lab only what the lab has (a self-signed default certificate,
`.localhost` names, one edge at `127.0.0.1:8443`); put the rest in unit tests with fakes, plus one
assertion in the public gate where a real CA exists. State which is which in each task.

- What the lab is: Traefik without ACME and without the HTTP redirect
  (`deploy/dev/traefik.nomad.hcl`: no `certificatesresolvers`, no `redirections`), version 3.7.13
  (observed); `Settings.CertResolver` empty; apps at `https://<name>.localhost:8443` with
  `ServerName` set (`lab_test.go:244-253`, `gate_projects_test.go:44-66`). The dashboard and API are
  on the Mac's `8088`: a test may read `/api/http/routers/<name>@nomad` for `status`, and
  `/api/http/middlewares` for the file-provider middleware (a **lab check**).
- By state:

  | state | lab assertion | elsewhere |
  |---|---|---|
  | live | HTTPS 200 via `127.0.0.1:8443` with the domain as SNI and Host: route live, certificate "Traefik's own" (CN `TRAEFIK DEFAULT CERT`, observed), so "live · own certificate" | gate on a host: trusted LE certificate, `NotAfter` about 90 days |
  | getting certificate | **not reachable in the lab** (needs a resolver): a unit test with a fake dialer returning the default cert and `CertResolver: "letsencrypt"` | gate on a staging host, optional |
  | DNS points elsewhere | **in-process Caravana with an injected resolver** (`hostIP.lookup` is already a seam, `ui/domains.go:111`): a fake maps `elsewhere.test` to `203.0.113.9` and the host to `203.0.113.1`; `x.localhost` gives "local", a `.invalid` name "not found" (RFC 6761 guarantees NXDOMAIN) | gate on a host: no real DNS to fake, skip |
  | expiring | unit test with a fake leaf whose `NotAfter` is 5 days out (the lab's is a year) | none |
  | no route | a name with no router: Traefik's 404 signature (observed) | all |
  | app not answering | a service whose container is down: 502 or 503 | all |

- Middlewares in the lab (a `traefik/whoami`-style app with a domain and the option; a
  `hostStatus`-style client with redirects off):
  - **Team only**: no credentials gives 401 with `WWW-Authenticate: Basic realm="<app>"`; right
    credentials 200; wrong 401; the stored app, job, export and logs hold the user name and (A) a
    bcrypt hash or (B) only `@file`, never the password sentinel.
  - **www redirect**: SNI and Host `www.<d>` gives a 308 (or 301: assert `Location: https://<d>/`),
    `<d>` gives 200. In the lab the www name has no DNS, which is fine with SNI.
  - **Path prefix**: `/api/x` reaches the app as `/x` (whoami's request line) with
    `X-Forwarded-Prefix: /api`; `/other` on that host goes to the plain router's service, or 404.
  - **Option change without outage**: change an option while a client calls the domain every
    200 ms (as the `app-actions` step does) and assert no failed request beyond the blue/green gap
    (**lab check**; R4 *Rollout*).
- Lab setup facts: `TestLab` gives Caravana a token made from `caravana-policy.hcl`, applied on
  every run, so a missing capability fails (CLAUDE.md, feature 010). Option B adds no policy line
  (R4), but the Traefik lab job changes, so `lab.sh up` re-runs it (`deploy/dev/lab.sh:44-46`).
- New steps, by the repo's convention: `console-domains-page` in `TestConsoleLab`, `domains-options`
  in the gate, and a read of the certificate settings. `TestNoShipWords` already keeps "ship" off
  every page; the page needs a test that no password sentinel is on any page, answer or log (as
  008, 017 and 018 have).

**Lab checks to run before the plan is written** (none needs more than the lab's Traefik; not run
here because this pass was read-only):
1. A router with extra tags `...middlewares`, `...redirectregex...`, `...stripprefix...` and
   `...basicauth...` registered as Nomad tags: does Traefik list them in `/api/http/middlewares` as
   `<name>@nomad`, and is the router `enabled`? (R4)
2. Two routers defined by tags on one Nomad service: do both bind to its backend without a
   `service` tag? Does a longer `Host && PathPrefix` rule win over the plain host router? (R4)
3. The permanent-redirect status code (301 or 308) of `redirectregex` in 3.7.13. (R4)
4. `acme.json` layout and the meaning of `/api/overview` `certificates` on the VM; Traefik's
   renewal window; the ACME error log line; HTTP-01 through a basic-auth router. (R2, R4)
5. A file-provider middleware from a template over `nomad/jobs/traefik`: re-render and apply
   without a restart, and the missing-middleware behavior (router disabled). (R4 option B)
6. Does Go's pure resolver here answer `x.localhost` (127.0.0.1) the way glibc does? (R3, R7)
