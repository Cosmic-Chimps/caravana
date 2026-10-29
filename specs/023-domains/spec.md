# Feature Specification: Domains

**Feature Branch**: `023-domains` (stacked on `022-schedules`)

**Created**: 2026-10-24

**Status**: Implemented, v0.13.0

**Input**:
- **The maintainer, 2026-10-23:** "after you finish continue with other page": 023 follows 022
  with no pause. On 2026-10-24 they decided the two open questions: a domain with options gets
  its own router (constitution VI clarified in 1.0.1), and Team only's password hash lives in a
  Nomad Variable rendered by the proxy's file provider, never in an app's job.
- **docs/plan.md, row 023 (Phase 2.6, the handoff's `DomainsScreen`, `AddDomainPanel` and the
  issuer part of `CertsScreen`):** one page for every address: status (live, getting
  certificate, DNS points elsewhere, certificate expiring), the app and port it goes to, the
  certificate's renewal date, per-domain options as switches, Add a domain with the record to
  create and checks, and the issuer settings.
- **The designer's contract:** `contracts/domains-screen.md` (amended 2026-10-24 for the
  research, `research.md`).

## How this feature is built

As for 022: the designer's contract, then the model and the checks in the service layer and the
API, then the pages on real data, the designer's review, the critique pass, rulings and the
last look.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Every address, with its state (Priority: P1)

The owner opens Domains and sees every address their apps answer to, each with a state they
can act on: live, getting its certificate, DNS points elsewhere (with the address seen and the
one expected), no DNS record yet, certificate expiring and not renewed, served with the proxy's
own certificate, a redirect, the app stopped or never deployed, or couldn't check. Each row says
the app (and service) the address goes to, its port and environment, and when the certificate
renews or expires. Opening a row explains the state in plain words and what to do. The same
rows appear on an app's own Domains page, filtered to that app.

**Why this priority**: an address nobody can reach is the first thing an owner needs to see,
and today the console shows domains as plain text with no state.

**Independent Test**: in the lab, a busybox app with a `.localhost` domain shows the proxy's own
certificate state; a host that resolves elsewhere shows "DNS points elsewhere" with the address
seen; a host with no record shows "no DNS record"; a stopped app's domain shows "app stopped";
the app's Domains page shows the same rows.

**Acceptance Scenarios**:

1. **Given** apps with domains, **When** the owner opens Domains, **Then** every domain of
   every app is listed once, problems first, with its state, where it goes, and its
   certificate's renewal or expiry.
2. **Given** a domain whose DNS points at another address, **Then** its row reads "DNS points
   elsewhere", a banner above the table names the host and the address seen, and the open row
   shows the address seen, the address expected and when it was last checked.
3. **Given** a domain served with a public certificate, **Then** the row says it is live, and
   its certificate's issuer, renewal date and expiry.
4. **Given** a domain whose certificate expires within the warn window and hasn't been
   renewed, **Then** the row and a banner say so, with "Check again".
5. **Given** a domain of an app that is stopped or never deployed, **Then** its row says so and
   links to the app, and no check is counted as a problem.
6. **Given** the checks can't run (the resolver or the proxy didn't answer), **Then** the row
   reads "couldn't check", never a wrong state, and the page draws without waiting.
7. **Given** the list is open, **Then** its states refresh about every minute without a reload
   where live updates work, and a reload shows states no older than a minute.

---

### User Story 2 - Per-domain options (Priority: P1)

The owner opens a domain's row and sets its options with switches: Team only (a shared
password; the console shows the generated password once and can issue a new one), a redirect
between the apex and its `www.` form, and a path prefix. Force HTTPS is shown on and locked:
it is on for every domain. Saving deploys the app with the options, like any definition change,
also for an app deployed from a Compose file. The same options are in the API.

**Why this priority**: the switches are the handoff's reason for the page; today an owner has
no way to protect a staging domain or redirect `www.`.

**Independent Test**: turn Team only on for a domain: the password is shown once; a request
without credentials gets 401 and one with `team` and the password gets 200; New password makes
the old one stop; a `www.` redirect answers a permanent redirect with the right Location; a
path prefix routes only requests under it; Remove takes the domain out with a deploy.

**Acceptance Scenarios**:

1. **Given** a domain, **When** Team only is turned on and saved, **Then** the app is deployed,
   the password is shown once with the user name `team`, visitors without it get 401, and the
   password appears in no record, log, export, job or answer after that.
2. **Given** a Team-only domain, **When** the owner asks for a new password, **Then** the new
   one is shown once and the old one stops working at once.
3. **Given** `example.com` and `www.example.com`, **When** the redirect is set, **Then** the one
   redirects to the other permanently, and the redirecting domain's row reads "redirects to …".
4. **Given** a path `/api` on a domain, **Then** only requests under `/api` reach the app, with
   the path kept.
5. **Given** an option change on an app that keeps a Compose file, **Then** the change is kept
   in the app's file as domains are, and a later deploy from the file keeps it.
6. **Given** an option change on a stopped app, **Then** it is refused with the stopped sentence.
7. **Given** a stale form (the app changed meanwhile), **Then** the save is refused and the
   form redrawn, as elsewhere.

---

### User Story 3 - Add a domain with its checks (Priority: P2)

From Domains or an app's Domains page, the owner adds a domain: they type the host, pick the
app (and service), see the DNS record to create for this host (A, and AAAA when the host has a
public IPv6; `@` for an apex), press "Check now" to see whether DNS already points here and
whether a certificate is served, then add it, which deploys the app. A host already used by
another app is refused by name.

**Why this priority**: adding a domain is the moment DNS is wrong most often; the check turns a
silent failure into a sentence.

**Independent Test**: Check now with a host that points here, one that points elsewhere and
one with no record shows each result with its reason; adding a domain deploys and the row
appears; a duplicate host is refused naming the app that has it.

**Acceptance Scenarios**:

1. **Given** the Add form, **Then** it shows the record to create with this host's public
   address, or says the address isn't known.
2. **Given** a typed host, **When** Check now is pressed, **Then** the DNS line says it points
   here, elsewhere (with the address seen) or has no record, and the certificate line says
   "issued after you add it" for a new host, or its real state for a host already served.
3. **Given** a host used by another app, **When** the owner adds it, **Then** it is refused
   naming that app.
4. **Given** the console's own host, **Then** it is refused.
5. **Given** Add, **Then** the app is deployed with the new domain and the list shows its state
   as the checks find it.

---

### User Story 4 - Problems reach the owner (Priority: P2)

A domain whose DNS points elsewhere, and a certificate expiring without renewal, appear on the
Apps home's needs-you list naming the domain, and go out through the notification channels as
problems; they resolve when the state clears. The owner sets how many days before expiry the
warning fires.

**Why this priority**: a certificate that quietly expires is the failure an owner can't see
coming.

**Independent Test**: a DNS-elsewhere domain puts an item on the needs-you list and sends one
"opened" notification; fixing DNS (or removing the domain) resolves it after the usual delay.

**Acceptance Scenarios**:

1. **Given** a domain whose DNS points elsewhere, **Then** the needs-you list has one item for
   it, naming the host and the address seen, with "Open domain", and channels that receive
   problems get one "opened" notification.
2. **Given** a certificate expiring within the warn window and not renewed, **Then** the same,
   with the days left.
3. **Given** the state clears, **Then** the item leaves and the "resolved" notification goes
   out.
4. **Given** the warn window set to 7, 14 or 30 days in Settings › Certificates, **Then** the
   expiring state and its item use it.

---

### User Story 5 - Certificates in Settings (Priority: P3)

Settings › Certificates shows how certificates are issued (the issuer and the contact email,
set when caravana was installed and changed by running the installer again), the warn window
(editable), and a table of every domain's certificate: issuer, renewal, expiry, problems first.
The Settings nav marks the entry when a certificate is expiring, else shows the count.

**Why this priority**: it is the handoff's screen, and the only place the installation's
issuer is visible; nothing here is urgent day to day.

**Independent Test**: the page lists the lab's domains with "Traefik's own" as issuer, the
email and issuer read-only with the installer sentence, and Warn when saved from 14 to 7.

**Acceptance Scenarios**:

1. **Given** the page, **Then** the issuer and contact email are shown read-only with "Set when
   caravana was installed; run the installer again to change it."
2. **Given** Edit, **Then** only the warn window can change, and saving it is immediate.
3. **Given** an expiring certificate, **Then** the Settings nav marks Certificates and the table
   lists it first.
4. **Given** an installation without an issuer, **Then** the page says the proxy serves its own
   certificate and the table's issuer column says "Traefik's own".

---

### Edge Cases

- A domain listed on two apps can't happen (refused at add); a domain on two services of one
  app is refused as today.
- The checks never block a page: each has a short timeout and the page shows "couldn't check"
  for what didn't answer; a check that keeps failing is tried again on the next cycle.
- A domain with options keeps its own router; removing every option moves it back to the
  service's router on the next deploy, with no gap for other domains of the service.
- The redirect pair: the redirecting host must be served too (it needs a certificate); both
  hosts are listed, the redirecting one with the "redirects to" state.
- Team only and the ACME challenge: certificates are obtained and renewed before any password
  applies (the proxy answers the challenge first); if the lab shows otherwise, the challenge
  path is excluded.
- A Team-only domain's password is shown once in the console and once in the API answer of the
  deploy that turned it on, never again; the hash is never in a job, export, dashboard, record
  or log.
- The installation's public address isn't known (no public URL, no entrypoint address): the
  record box says "this host's public IP address" and DNS comparison reports "couldn't check"
  rather than "elsewhere".
- An IPv6-only or dual-stack host: both record types are shown; a stale AAAA record counts as
  pointing elsewhere.
- A wildcard or a name with a scheme or a path typed as the host is refused, naming the field.
- The proxy serves its own certificate for every host it doesn't know: a host that isn't routed
  at all reads "not deployed"/"app stopped", not "live".
- Many domains (50+): the checks run in parallel with a cap, and the list draws from the cache
  while they run.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Caravana MUST list every domain of every app on one page, and the same rows on
  each app's Domains page filtered to that app, each with its state, the app (and service),
  port and environment it goes to, and its certificate's issuer and renewal or expiry.
- **FR-002**: A domain's state MUST be derived from checks Caravana runs itself: the host's
  DNS records against this host's public addresses, and a TLS handshake to the proxy with the
  host as server name (certificate issued, pending, the proxy's own, expiring, error). The
  states, in order of precedence: DNS elsewhere, certificate expiring, certificate pending, no
  DNS record, the proxy's own certificate, redirect, live, couldn't check; an app stopped or
  never deployed is "not served".
- **FR-003**: Checks MUST have short timeouts (seconds), run in parallel with a cap, be cached
  for about a minute and refreshed while the page is open, and never delay a page; a check
  that fails gives "couldn't check", never a wrong state.
- **FR-004**: A domain MAY carry options in the app's definition: Team only, a redirect between
  the apex and its `www.` form, and a path prefix. Force HTTPS is on for every domain and is
  shown locked on. Changing an option is a deploy of the app, through the console and the API,
  for apps deployed from a definition and from a Compose file alike.
- **FR-005**: A domain with any option MUST be routed by its own router on the same service,
  so its options apply to it alone; domains without options keep the service's router
  (constitution VI, 1.0.1).
- **FR-006**: Team only MUST be HTTP basic authentication with the user name `team` and a
  password Caravana generates, shown once when it is turned on and once on "New password";
  the password's hash MUST live only in a Nomad Variable the proxy renders, never in an app's
  job, export, the proxy's dashboard, a record or a log; the password itself is never stored.
- **FR-007**: Adding a domain MUST show the DNS record to create (A, and AAAA when the host has
  a public IPv6, `@` for an apex) with this host's public address, offer a "Check now" that
  runs the DNS and certificate checks fresh and shows each result with its reason, refuse a
  host another app has (naming it), the console's own host, and a malformed host, and deploy
  the app with the domain.
- **FR-008**: A domain whose DNS points elsewhere, and a certificate expiring within the warn
  window without renewal, MUST be needs-you items naming the domain and the cause, and
  notifications through channels that receive problems; both resolve when the state clears.
- **FR-009**: Settings › Certificates MUST show the issuer and the contact email read-only as
  the installer set them, the warn window (7, 14 or 30 days, default 14) editable, and the
  certificates table (domain, used by, issuer, expiry, problems first); the Settings nav MUST
  mark the entry when a certificate is expiring, else show the count of issued certificates.
- **FR-010**: The API MUST offer the list with states, one domain, an on-demand check, the
  password rotation, and the certificate settings and table; the domain options travel on the
  app's definition in the existing deploy operation; deploy tokens MUST NOT use any of it.
- **FR-011**: The row's and the banner's sentences MUST be in Caravana's words from a fixed
  table per state, never the resolver's or the proxy's raw text.
- **FR-012**: Nothing of this feature MUST change the routing of domains without options; an
  app deployed before it deploys the same job.

### Key Entities

- **Domain** (in the app definition): host, the service it belongs to, and its options
  (teamOnly, wwwRedirect, path).
- **Domain state** (derived, cached): DNS (status, addresses seen, addresses expected),
  certificate (status, issuer, notAfter, renewsAt), the state word, checked time.
- **Team password**: generated per Team-only domain; its hash in a proxy-rendered variable.
- **Certificate settings**: the issuer and contact email (read-only, from the installation),
  the warn window.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every domain of the installation is on one page with a state, within one minute
  of a change to DNS or a certificate; the page itself draws in under a second regardless of
  how many hosts are being checked.
- **SC-002**: An owner turns Team only on for a domain in under a minute from the Domains
  page, including copying the password; the next request without it is refused.
- **SC-003**: A DNS mistake on a new domain is explained on the Add form before the domain is
  added, with the address seen, in the time of one Check now.
- **SC-004**: A domain pointing elsewhere reaches the needs-you list within one minute and
  every subscribed channel within two, once; it resolves when fixed.
- **SC-005**: Apps without domain options deploy the same job as before this feature (golden
  files unchanged); a domain's routing never breaks for the other domains of its service when
  one gains or loses options.
- **SC-006**: The lab test covers the states the lab can produce (the proxy's own certificate,
  DNS elsewhere through an injected resolver, no record, not served, redirect), Team only's 401
  and 200, New password, a path, the needs-you item and notification, Settings' warn window,
  and no password or hash anywhere but the two shown-once answers.

## Assumptions

- The installation's public addresses come from the public URL setting, else the proxy's
  entrypoint addresses as the installer wrote them; without either, DNS can't be compared.
- The proxy's edge is reached on loopback at the standard HTTPS port on an installed host and
  at the lab's port in the lab; a setting names it.
- The certificate's "renews in" is 30 days before its expiry, the issuer's renewal window.
- "Reachable from outside" can't be probed by the host itself; a trusted certificate served
  for the host is the proof the page shows.
- Issuer settings beyond the warn window (the contact email, the issuer) are the installer's;
  the installer passes them to Caravana so the console can show them.
- Out of scope: wildcard and DNS-challenge certificates, uploading a certificate, a fallback
  issuer, cluster certificates and the edge CNAME (Phase 3), routing by path between services
  (backlog), a Traffic column (025), per-domain ports (the port is per service, in Configure).
