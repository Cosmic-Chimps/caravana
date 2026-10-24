# Contract: 023 Domains, the `DomainsScreen`, `AddDomainPanel` and the issuer settings

Written by the caravana_designer session on 2026-10-23, before the spec; amended 2026-10-24 for
the research (Traefik 3.7 in the lab) and the maintainer's two decisions (a router per domain with
options; Team only's hash in a Nomad Variable rendered by Traefik's file provider). The values come from
the handoff's `DomainsScreen.jsx` (`DOMAINS`, `DomainStatus`, `AddDomainPanel`, the row and its
`detail`), `CertsScreen.jsx` (the issuer panel and the renewal problem banner) and
`theme/caravana.css`, rendered from the prototype (`design/console/_shots/p2-domains.png`,
`p6-certs.png`). Where this list and the handoff disagree, this list wins. The 021 and 022
contracts still apply (shell, fields, selects, icons, page head, time rule: UTC first, the
viewer's local time beside it; the console never says "ship").

## What 023 adds (the calls the backend follows)

- **A domain is still a service's domain** in the app definition (`Service.Domains`), deployed
  with the app; nothing moves out of the definition. 023 adds **per-domain options** to
  `app.Domain` (`wwwRedirect`, `teamOnly`, `path`), so changing an option is a deploy of the
  app, as every other definition change is. **A domain with any option gets its own router**,
  `<app>-<service>-<hash8(host)>` (the maintainer's decision: a middleware attaches to a
  router, not a host); domains without options stay on the service's router `<app>-<service>`.
  Constitution VI's router-name rule is clarified to say so. The `Translate` golden files grow;
  the HCL export writes the extra routers and middlewares. An app that keeps a Compose file is
  treated like any app: an option change rewrites the overlay, as secret edits do.
- **Force HTTPS is not an option:** the installer redirects the whole HTTP entrypoint, so it is
  on for every domain. The detail draws it as a locked-on switch with the note "On for every
  domain."
- **Port is per service**, not per domain: one Traefik service per registration. The port a
  service's domains go to is chosen once, in the app's Configure (the Deploy panel's "Container
  port" is it; a service with several ports gets a "Domains go to" select there). Nothing
  port-shaped on the Domains page except the "Goes to" cell.
- **Status per domain**, derived on every read and cached for 60 s (`service.DomainStatus`):
  - **DNS**: Caravana resolves the host (A and AAAA, system resolver, 2 s timeout) and compares
    with the host's public addresses (`CARAVANA_PUBLIC_URL`'s host resolved, else the
    addresses Traefik's entrypoint listens on as the installer wrote them): `ok`, `elsewhere`
    (with the addresses seen), `none` (no record), `unknown` (lookup failed).
  - **Certificate**: a TLS handshake from Caravana to Traefik's own edge (loopback or the
    entrypoint address) with the domain as server name, reading the leaf: `issued` (a
    certificate for the host from a public issuer, with `notAfter` and the issuer's name),
    `pending` (Traefik answered its default certificate and a resolver is configured: ACME
    hasn't finished), `self` (Traefik's own certificate and no resolver: the lab, or
    `acme=none`), `expiring` (issued, within the warn window), `error` (the handshake failed).
    `acme.json` is not read (root-only, every private key) and Traefik's API has no dates.
  - **Served**: the same handshake proves Traefik serves the host; there is no probe "from
    outside" (the host can't be its own outside visitor). A trusted certificate is the proof the
    page shows.
  - These give the row's **state**, the first that applies: `dns_elsewhere` (■ failed, "DNS
    points elsewhere"), `cert_expiring` (◆ warning, "certificate expiring": issued, within the
    warn window and not renewed, which is how a failed renewal shows, since ACME's errors
    aren't readable), `cert_pending` (◐ rolling, "getting certificate"), `dns_none` (◆ warning,
    "no DNS record"), `self_signed` (○ idle, "Traefik's own certificate", the lab and
    `acme=none`), `redirect` (○ idle, "redirects to www.…", for the apex of a `wwwRedirect`
    pair), `live` (● healthy, "live · HTTPS"), and `unknown` (○ idle, "couldn't check") when
    the checks themselves failed. An app that is stopped or never deployed gives `not_served`
    (○ idle, "app stopped" / "not deployed").
- **Needs-you items**: `domain_dns_elsewhere` and `domain_cert_expiring` (one per domain, on
  the state), and so notifications. `cert_expiring` fires `warnBeforeDays` before `notAfter`
  (the setting below; default 14); since ACME renews 30 days before, an expiring certificate
  means renewal has been failing.
- **Issuer settings** (`CertsScreen`'s "How certificates are issued", without issuer fallback,
  wildcard DNS or cluster certificates, which are Phase 3): **Contact email** is Traefik's
  static configuration, set by the installer and handed to Caravana (`CARAVANA_ACME_EMAIL`, or
  however the installer passes it): read-only in the console, with "Set when caravana was
  installed; run the installer again to change it." **Warn when** (7, 14 or 30 days before
  expiry; default 14) is the one editable setting. They live in Settings as **Certificates**
  under Connections (`/settings/certificates`), after Backup storage, so the Settings nav rule
  from page 3 applies: ◆ when any certificate is expiring, else the count of issued
  certificates.
- **Add a domain** checks DNS and the certificate on demand (`checkDomain`, the checks above
  run fresh, not from the cache) and adds the domain to a service with a deploy. The record to create is an A record (and AAAA when the host has a public IPv6) to
  this host; the edge CNAME is Phase 3.
- **Team only** is HTTP basic auth on the domain's router (Traefik `basicAuth` middleware)
  with one credential per domain: the user name `team` and a password Caravana generates and
  shows once. The hash lives in a Nomad Variable that Traefik's file provider renders (the
  maintainer's decision): no hash in the app's job, its export or Traefik's dashboard; the
  password itself is never stored. The installer wires the file provider (one Traefik restart on
  existing hosts; the installer says so). Until accounts exist, that is what "sign in with
  caravana" means here; the switch's label says "Team only (a shared password)". Traefik answers
  the ACME HTTP-01 challenge before any middleware, so renewals are unaffected; the detail says
  nothing about it unless the lab proves otherwise.
- **Path**: a domain may route a path prefix (`path`, `/api`, Traefik `PathPrefix` added to its
  router's rule, the prefix kept). Routing between services by path stays in the backlog; a path
  routes to the same service.

## Where it lives

- **`/domains`**, a workspace page (the handoff's), and the sidebar's Workspace group gains
  **Domains** (icon `globe`) after Schedules. Crumb `Domains`.
- **The app's Domains page** (`/apps/{id}/domains`, 021 page 2) stays as the per-app view,
  reduced: its table gains the status pill, the certificate column and the open-row detail
  below, exactly as `/domains` draws them, filtered to the app; its head's sentence ends with a
  link "All domains" to `/domains`; its Add form is the same form with the app fixed. One
  template for both.
- **Settings › Certificates** (`/settings/certificates`) holds the issuer settings and the
  certificates table (below).

## The list (`/domains`)

- **Page head** (page 3's shape): "Domains" 30px, "Every address people use to reach your apps.
  Certificates are issued and renewed for you." 15px fg-2, **Add domain** (primary sm, plus
  icon, link to `#add`). Under it the filter row (page 4's shape): a project select (when more
  than one), an app select, Apply, Clear; then the count line 400 13px mono fg-3: "12 domains ·
  2 need you" / "· all healthy" / "· 1 getting a certificate".
- **Problem banner** (the handoff's `CertsScreen` banner, above the table, one per problem
  domain, at most 3 then "and N more" linking to the first row): `.cv-panel` with padding 16px
  18px, grid `14px minmax(0, 1fr) auto`, gap 14: a 9px mark; two lines: the sentence 15px/1.5
  fg-1 ("`bi.example.com` expires in 6 days and hasn't been renewed." / "`partners.example.com`
  points at `185.199.108.153`, which isn't this host.") and the explanation 14px/1.5 fg-2 (the
  fixed table in the detail below); the action **Check again** (secondary sm, a post that runs
  the checks fresh for that host). There is no retry into ACME: Traefik retries on its own.
- **Table** (`.cv-panel`): head 38px ink-2 `.cv-label` "Domain · Status · Goes to · Certificate
  · (open)" over columns `14px minmax(0, 1.6fr) 190px minmax(0, 1.2fr) 150px 36px` (the
  handoff's, minus Traffic: there is no request counter until 025), gap 18, padding 0 18px,
  min-height 52, 1px line-1 dividers, each row a native `<details>` whose `<summary>` is the
  row (open row ink-2 ground, no divider under it).
  - cell 1: chevron-right / chevron-down 12px fg-3;
  - **Domain:** 500 14px mono fg-1 ellipsized, then a 12px `lock` icon fg-3 with `title` "Team
    only" when set, then "`/api`" in 12px mono fg-3 when a path is set; a domain on its own
    router shows nothing extra (the router name is a Nomad detail);
  - **Status:** the quiet sm pill with the state's words (above), its mark;
  - **Goes to:** 400 13px/1.3 mono fg-2 "`booking-api` `:8080 · production`" (the app, then
    port and environment in fg-3; the service's name after the app when the app has several:
    "`hollo` › `web` :80 · production"); a redirect row reads "→ `www.example.com`";
  - **Certificate:** 400 12px/1.3 mono: "renews in 61 d" fg-3 (ACME renews 30 days before, so
    "renews in" = notAfter − 30 d), "expires in 6 d" in the warn hue when expiring, "Traefik's
    own" fg-3 for `self`, "—" for pending/unknown;
  - last cell: an `external-link` icon link (page 2's), `aria-label` "Open https://<host>".
  - Sorted: problems first (failed, warning, rolling), then live, then idle; by host within.
  - Empty: `.cv-empty` "No domains yet. Give an app a domain and HTTPS is set up for it." with
    Add domain.
- **Footer** 13px fg-3: "Checks run every minute. Certificates come from Let's Encrypt and renew
  by themselves." (no previews sentence: Phase 4).

### The row's detail

The handoff's `detail`: grid `minmax(0, 1fr) minmax(0, 1fr)`, gap 24, padding 18px 18px 20px
46px, ink-0 ground, bottom 1px line-1.

- **Left column**, by state:
  - `dns_elsewhere`: the sentence 14px/1.5 fg-1 "The domain points at `185.199.108.153`, which
    isn't this host. Until it changes, caravana can't get a certificate and visitors won't
    reach `supplier-gw`." then the facts grid `90px minmax(0, 1fr)`, gap 8px 14px, 400
    13px/1.4 mono fg-2, keys fg-3: **now** "A · `185.199.108.153`" (failed hue), **should be**
    "A · `<this host's IP>`", **last check** "40 s ago · checks every minute";
  - `cert_expiring`: "The certificate expires in 6 days and hasn't been renewed. Let's Encrypt
    renews 30 days before expiry, so renewal has been failing: check that DNS points here and
    that port 80 reaches this host." then **Check again** (secondary sm; there is no retry seam
    into ACME: Traefik retries on its own);
  - `self_signed`: "Traefik serves this domain with its own certificate: browsers will warn.
    Certificates are issued when caravana is installed with Let's Encrypt." fg-2;
  - `cert_pending`: "Waiting for Let's Encrypt. DNS points here; the certificate usually
    arrives within a minute of the first visit." fg-2;
  - `dns_none`: "No DNS record yet. Add this record where you manage DNS:" and the record box
    from Add a domain (below);
  - `live` / `redirect`: the facts grid `110px minmax(0, 1fr)`: **certificate** "Let's Encrypt
    · renews in 61 d · expires 2026-12-22" (the issuer from the leaf), **DNS** "A → `<ip>`",
    **router** "`<app>-<service>`" or "`<app>-<service>-a1b2c3d4` (its own, for its options)"
    in fg-3, **checked** "40 s ago";
  - `not_served`: "The app is stopped: start it to serve this domain." / "Nothing is deployed
    yet." with the link.
- **Right column** (`.cv-label` "Options", gap 10): the switches as a `<form method="post">`
  of checkboxes drawn as switches (the handoff's `Switch`, 30×18, radius 9, on: healthy-bg
  ground, healthy-line edge, healthy knob; off: ink-2 ground, line-2 edge, fg-3 knob; a 14px
  label after it; no script: a plain checkbox styled as the switch, `aria-checked` from the
  input): **Force HTTPS** drawn on and disabled (not a form field), with the note "On for every
  domain." in 13px fg-3; **Team only (a shared password)**; **Redirect `example.com` here** for
  a `www.` host (adds the apex as a redirect row) / **Redirect to `www.example.com`** for an
  apex whose `www.` exists; under them **Path**: a 32px mono field (`/api`, optional, hint
  "Only requests under this path reach the app; the path is kept."); then **Save and deploy**
  (primary sm; a stale form 409 as page 2); then **Remove…** (secondary sm, `<details>`,
  "Remove and deploy"). Turning Team only on answers 200 with the shown-once panel (page 3's):
  "Copy the password for `admin.example.com` now. User name `team`." and the value box; the
  detail of a Team-only domain offers **New password** (`<details>`, "The old password stops
  working at once.", then the shown-once panel). Changing options on a stopped app is refused
  with the stopped sentence.

### Add a domain (`#add`, and `/apps/{id}/domains#add`)

The handoff's drawer as a `.cv-panel` on the page (page 2's shape), padding 22, gap 22, title
"Add a domain" 500 18px:

- **Domain** (mono; a host name, lowercase, no scheme; the console's own host refused; an apex
  is "exactly one dot" as page 2 settled); **Sends traffic to** (an app select, then a service
  select when the app has several, as 022's form; the service's port is shown after it as text,
  "`:8080`, set in Configure"); **Path** (optional, mono).
- **Point it at caravana** (`.cv-label`): the record box (page 2's, `80px minmax(0, 1fr)`):
  **Type** "A" (and a second row "AAAA" when the host has a public IPv6), **Name** "@" for an
  apex else the first label, **Value** this host's public IPv4 (and IPv6), from
  `CARAVANA_PUBLIC_URL`'s host or the entrypoint addresses; "this host's public IP address"
  only when neither is known. Under it 13px fg-3: "The root of a domain needs an A record. A
  subdomain can use one too."
- **Check** (`.cv-label`): two lines 14px, each an 8px mark then words: "DNS points at
  caravana" / "HTTPS certificate"; before a check they are ○ fg-3; after **Check now**
  (secondary sm, `name="action" value="check"`, a submit that runs `checkDomain` for the typed
  host and redraws the form with the results) the DNS line is ● fg-1 or ■ with the reason
  under it ("points at `185.…`", "no record yet"), and the certificate line reads "issued
  after you add it" ○ fg-3 for a new host (or its real state for a host already served). There
  is no "reachable from outside" line: the host can't be its own outside visitor; a trusted
  certificate is the proof.
- Footer: **Add domain and deploy** (primary sm) when DNS checked ok or no check was run (the
  handoff's "Add and keep checking": we add and the list's state shows the rest), and a Cancel
  link. A domain already on another app is a field error naming it.

## Settings › Certificates (`/settings/certificates`)

- Head: "Certificates" / "HTTPS certificates for every domain come from Let's Encrypt and renew
  30 days before they expire." (with `acme=none`: "This caravana was installed without Let's
  Encrypt: Traefik serves its own certificate.") No add action.
- The problem banner(s) as on `/domains`, for `cert_expiring`.
- `SectionHead` "Domain certificates · 12 certificates" over the handoff's certificates table:
  head 34px, columns `14px minmax(0, 1.4fr) minmax(0, 1fr) 150px minmax(0, 1.1fr)`: mark 9px
  (● issued, ◐ pending, ◆ expiring, ○ self/unknown), **domain** 400 13px mono fg-1, **used by**
  the app (mono fg-2), **issuer** "Let's Encrypt" / "Traefik's own" (fg-3; the leaf's issuer),
  **expiry** "renews in 61 d" / "expires in 6 d" (warn hue) / "waiting for DNS". Sorted problems
  first. No "Upload your own" (not in the plan).
- `SectionHead` "How certificates are issued" with **Edit** (secondary sm, `<details>` opening
  the form in place) over a panel of the handoff's rows `220px 260px minmax(0, 1fr)`, gap 24,
  padding 14px 20px: **Issuer** "Let's Encrypt" / "none" with "Certificates are issued and
  renewed for you." / "Set when caravana was installed." (read-only); **Contact email** the
  address as the installer set it, "Gets expiry warnings from the issuer. Set when caravana was
  installed; run the installer again to change it." (read-only); **Warn when** "14 d before
  expiry", "Sends to the channels set up in Notifications." The Edit form holds Warn when alone
  (select 7/14/30) and **Save** (primary sm).
- No wildcard and no cluster certificates sections (Phase 3/4).

## Narrow (under 900px, checked at 390px)

- Heads stack; the filter wraps; the table rows fold to label/value pairs as the home's; the
  detail's two columns stack, switches full width; the Add panel's record box and check lines
  stack naturally.

## May differ (server-rendered, no JavaScript)

- The handoff's drawer and "…" menu become a page panel and the detail's buttons; switches are
  checkboxes styled as switches, saved by a Save button, not on toggle.
- The checks run on a Check submit (and every minute for the list); no live countdown.
- "Add and keep checking" is just Add; the list shows progress.
- Copy buttons stay hidden without script; values are selectable.

## API (names only, for the spec)

`listDomains` (every domain with its state, `GET /v1/domains`), `getDomain`, `checkDomain`
(`POST /v1/domains/check` with `host`), `getCertificateSettings` (`contactEmail` read-only,
`warnBeforeDays`, `issuer`), `putCertificateSettings` (`warnBeforeDays`), `listCertificates`,
`rotateTeamPassword` (`POST /v1/apps/{id}/domains/{host}/team-password`, the password once);
`Domain{host, app, service, port, path, wwwRedirect, teamOnly, router, state, dns{status,
seen[], expected[]}, certificate{status, issuer, notAfter, renewsAt}, checkedAt}`; the domain
options on `App.services[].domains[]` (API 0.25.0, `deployApp` carries them); attention kinds
`domain_dns_elsewhere`, `domain_cert_expiring`; the shown-once `teamPassword` on the deploy
answer when Team only was turned on. Times RFC 3339 UTC.

## Review

The designer checks at 127.0.0.1:7070 against `p2-domains.png` and `p6-certs.png`:
- `/domains` with a live domain (the lab shows `self_signed`, "Traefik's own certificate"; a
  real host shows `live`), a DNS-elsewhere one (a host pointing at another IP), a no-record
  one, a stopped app's domain, a redirect pair; the banners; the sidebar item; the count line;
  the filter by app;
- each state's detail, the locked Force HTTPS switch with its note, Team only on with the
  shown-once password and a `curl -u team:…` that passes while an unauthenticated request gets
  401, New password, a path saved and reached, the router fact on a domain with options and
  on one without, Remove;
- Add a domain: the record box with a real IP, Check now with a host that points here, one
  that points elsewhere and one with no record; a refused duplicate;
- the app's Domains page with the same rows and detail, "All domains";
- Settings › Certificates: the table with the issuer column, the read-only email with its
  installer sentence, Edit with Warn when alone, the nav mark on an expiring certificate (if
  stageable: a short-lived certificate in the lab's Traefik, else the state's table test);
- the needs-you items and a notification for a DNS-elsewhere domain;
- narrow at 390px and the pages with JavaScript off.

## Review fixes (2026-10-24)

- Sideways overflow: every fixed grid track of the table, the open row's facts and the Certificates rows is
  `minmax(0, N)`; keys, state sentences, notes and the issuer hints wrap (`overflow-wrap: anywhere`); the section
  head's children and the settings grid have `min-width: 0`. It could not be reproduced at 1440 or 1200 with the
  lab's data; a stress render (140-character unbroken values) did widen the page, through the facts' keys, and no
  longer does at 1440, 1200, 768 and 390.
- The Certificates head counts issued and expiring certificates only, as the nav does: "Domain certificates · 12
  certificates"; with none, "· 8 domains · none issued yet", or "· 8 domains · no certificates: installed without
  Let's Encrypt" when the issuer is none. The staging lede is "Certificates come from Let's Encrypt's staging
  issuer: browsers don't trust them. Run the installer with the real issuer when the domains are ready."
- Needs-you items lead with the app: "blog: `example.com` points at 104.20.23.154, which isn't this host." and
  "blog: the certificate for `example.com` expires in 6 days and hasn't been renewed."
- Edit (Warn when) is drawn in every state; a test holds it.
- A host typed with capitals is lowercased, not refused, in the Add panel and the API (`WithLowercaseHosts`).

### Critique rulings

The designer's rulings on the impeccable critique of this screen, one commit each (`fix(ui): 023 …`):

1. **Check now lands on its result.** The Add panel's Check now answers 303 to the same page with
   `?checked=<host>#add` (on `/domains` and on an app's Domains page); the GET draws the panel from the check
   the console kept for that host for 60 seconds, with what was typed. An unknown or expired host draws the
   empty panel.
2. **The Options form never sits in a swapped region.** The Domains region's own swap is `none`. A domains
   notice fetches `/parts/domains` with `known` (a key of the hosts the page was drawn with); while the hosts
   are the same the answer is out-of-band fragments by id (the summary, each row's cells, each row's left
   column), else the whole region (`HX-Reswap`). Every `<details>` has a stable id.
3. **One state table** (`domainStates`) for the list, the Add panel's check lines and Settings ›
   Certificates: each state's mark, pill words, certificate words and fixed sentence once. No DNS record is
   a warning everywhere, a pending certificate says "waiting for Let's Encrypt" everywhere, and a certificate
   that couldn't be read is idle, not failed.
4. **The elsewhere row says it once.** When a row's banner is on the page, its open detail drops the sentence
   and the second Check again and leads with the record to change it to (the no-record box), keeping now and
   last check. A DNS-elsewhere row has no external link.
5. **The smaller control gaps.** Cancel returns to the page; a pending certificate offers Open it once; a
   Team-only domain says "Visitors sign in as `team`."; the shown-once panel writes caravana in lowercase.
6. **Goes to and the Path hint.** Goes to names the project, by its display name as the rail does, when the
   app is outside Default › production ("Shop › web :80 · staging"). The Options form's Path hint ends "Saving
   deploys the app." The last check line keeps "checks every minute while this page is open" (true: 60 s only
   while a domains page's stream is open), and the footer says "Checks run every minute while this page is
   open, and every 15 minutes otherwise."
7. **The service select.** On `/domains` it is drawn only after an app is picked: the app select fetches
   `/parts/domains/add-service?app=` into the field, and Check now draws it without script; until then "The
   app's first service" is the default, in words. A service whose port is 5432, 3306, 6379, 27017, 5672 or
   11211, or that has a TCP-only health check and an image naming postgres, mysql, mariadb, redis, mongo,
   rabbitmq or memcached, is listed disabled with "not an HTTP port" (an app with no other is disabled in the
   app select, "(no HTTP port)"; the API takes any port). The filter's project select and every app select
   use the same words: "Shop / staging / web" (the project's display name, the environment, the app).
8. **Confirmations inline, the banner stacks, the locked switch, the sort.** Remove… and New password (and
   Settings › Certificates' Edit) are `<details>` whose form is a block under its button, in the flow: it
   pushes the row down, with no absolute overlay, so nothing clips. The DNS banner stacks its text, then Check
   again, below 640px. The locked Force HTTPS switch is drawn in fg-3 (knob and ground); a real switch's on hue
   stays the handoff's. A redirect row sorts directly under the host it redirects to.
9. **Settings › Certificates without Let's Encrypt.** With `acme=none`, Warn when is drawn disabled with
   "Applies once certificates are issued." (the form posts nothing; the API still accepts the setting). The
   lede is "This caravana was installed without Let's Encrypt: Traefik serves its own certificate.", which the
   page already said.
