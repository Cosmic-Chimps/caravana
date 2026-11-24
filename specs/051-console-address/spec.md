# Feature Specification: Change the console's address from the console

**Feature Branch**: `alta/loyal-wombat-v69b` (worktree; CodeAlta task `xbf5`)

**Created**: 2026-11-23

**Status**: Implemented, unreleased

**Input**: CodeAlta task `xbf5` ("11 · Change the console's domain from the console", raised by the maintainer on
2026-11-23 while setting up a GCP VM for Codemagic Patch: "there should be a way to change the domain in the UI").

## Why

The console's domain is chosen once, by the installer's `--domain` (default `<public-ip>.sslip.io`). Changing it means
an SSH session and a rerun of the installer, because the domain lives where only root writes:

- Caravana's env file `/etc/caravana/caravana.env` (`CARAVANA_PUBLIC_URL=https://<domain>`), read by systemd;
- the installer's own record `/var/lib/caravana-host/install.env` (`CARAVANA_DOMAIN`), which every later run reuses;
- Traefik's static route to the console, rendered from the Traefik job's `console_domain` variable;
- and, through the public URL, the cluster's workload identity issuer (038) and the passkeys' relying party (033).

The common case is moving from the `sslip.io` default to a real domain once DNS is set up.

## User Scenarios & Testing

### User Story 1 — Check a new address before anything changes (Priority: P1)

On Operator › Console address the owner sees the current address and what depends on it, types a new domain and
asks Caravana to check it. Each check is shown with its result: the name is a host name; it resolves to this
server's public address; Traefik routes it to Caravana; a certificate is issued for it.

**Acceptance scenarios**
1. Given a domain that isn't a host name (`a b.test`, an IP address, the current domain), when it is checked, then it
   is refused naming `domain` and nothing is recorded or changed.
2. Given a domain that doesn't resolve to this server (no record, or another address), when it is checked, then it is
   refused naming `domain` with what DNS said, and nothing is recorded or changed (no route, no file).
3. Given a domain with no record under a zone connected to a DNS provider (048), when it is checked, then the refusal
   says Caravana can create the record, and checking again with "create the record" creates an A (and AAAA) record
   pointing at this server and goes on.
4. Given a domain that resolves here, when it is checked, then Caravana adds it to Traefik's console route beside the
   current one and waits (up to 5 minutes) until Traefik routes it to Caravana and presents a valid certificate for it;
   the change is then `ready`. With no certificate after 5 minutes it is `check_failed` and the route is taken back.
5. Given an installation whose Traefik serves its own certificate (`--acme none`), the certificate check passes with
   "Traefik's own certificate (this installation doesn't use Let's Encrypt)".

### User Story 2 — Switch, without root in the console (Priority: P1)

When the checks pass the owner presses Switch. The console never gets root: the server agent (031), which runs as
root beside Caravana, receives the request in its next sync answer (an ID and the domain only), checks both as text,
rewrites the public URL in Caravana's env file and the domain in the installer's record, restarts Caravana and reports
`applied`, `failed` or `refused` with a one-line reason, which the page shows. Caravana comes back under the new
address; the issuer follows (038).

**Acceptance scenarios**
1. Given a `ready` change, when the owner switches, then within about a minute Caravana answers at the new domain with
   `PublicURL` = `https://<new>`, the env file and `install.env` hold the new domain, the change is `applied`, and every
   app's domain still answers (Traefik isn't restarted).
2. Given a crafted answer or request (a domain with a newline, a path, a shell word, an IP, a wrong ID), the agent
   changes nothing and reports `refused`.
3. Given Caravana doesn't answer its health check after the restart, the agent puts both files back, restarts it again
   and reports `failed`.
4. Given no outcome five minutes after the switch, the change is `lost` and the page says to look at the agent's log.
5. Given a later rerun of the installer (an update), the console keeps the new domain.

### User Story 3 — The old address keeps working for a while (Priority: P1)

After the switch the old domain stays in Traefik's route for the redirect window (a choice made with the change,
default 30 days, 0 to 90) and keeps its certificate. Pages there redirect (308) to the same path on the new address;
the API, webhooks, the node agents' calls and the issuer's discovery documents (`/v1/…`, `/.well-known/…`) are served
as before, because CI's tokens and GitHub's webhooks don't follow a redirect. The page lists what still uses the old
address. When the window ends Caravana takes the old domain out of the route.

**Acceptance scenarios**
1. Given an applied change with 30 days, when `https://<old>/apps` is requested, then it answers 308 to
   `https://<new>/apps`; `https://<old>/v1/health` answers 200 from Caravana.
2. Given the window ended, the old domain is no longer in Traefik's route.
3. The page lists: passkeys registered on the old domain, deploy and API tokens and webhooks (their senders still hold
   the old URL), the workload identity issuer (anything that trusts it, such as Bella's Nomad attestor, needs the new
   one), and what follows by itself (notification links, joined servers).

### User Story 4 — Nobody is locked out (Priority: P1)

Passkeys belong to a domain (033). Before the switch the page says passkeys on the old domain won't work on the new
one and that password sign-in stays on at the new domain until a passkey exists there. The session cookie is per host,
so the owner signs in again at the new address.

**Acceptance scenarios**
1. Given password sign-in is off and passkeys exist only for the old domain, when the owner signs in with the password
   at the new domain, then it works.

### User Story 5 — Joined servers follow (Priority: P2)

Joined servers' agents call the console at the URL they joined with. Every sync answer carries the console's public
URL; an agent whose URL differs (and isn't loopback) switches to it once `/v1/health` answers there.

### Edge cases

- One change at a time: a second check while one is open is 409 `console_address_change_open`.
- A change can be cancelled until it is switched; its pending domain leaves the route.
- An installation whose Traefik job predates this feature (no route from the variable) is refused with "run the
  installer again"; one without a managed agent beside Caravana (the lab, a host from before 031) is refused at the
  switch with `no_agent`.
- `sslip.io` names are host names like any other; the page shows this server's own `<ip>.sslip.io` as a hint.
- A domain an app uses is refused (`domain_taken`).

## Requirements

- **FR-001** `GET /v1/console-address`: the current domain, public URL, issuer, the old domains still redirecting with
  their end, the open or last change, the dependents and whether the installation can change (agent, Traefik route).
- **FR-002** `POST /v1/console-address/changes {domain, redirectDays?, createDNSRecord?}`: name and DNS checked before
  anything is recorded (422 naming `domain`); then 202 with the change, `checking`.
- **FR-003** `GET /v1/console-address/changes/{id}`, `DELETE …/{id}` (cancel), `POST …/{id}/switch` (202, `switching`).
- **FR-004** The agent applies only an ID of 32 hex digits and a host name; it writes only the two known lines of the
  two known files and restarts only `caravana`. Never a path, command or file name from the console reaches root.
- **FR-005** Deploy tokens are denied every new route by the default deny.
- **FR-006** No Traefik restart for a check, a switch or the end of a window.

## Success Criteria

- On `install-test.sh`'s VM: from `caravana.test` to `console2.caravana.test` through the API; the checks pass; the
  console answers on the new domain; the old one redirects pages and serves `/v1/`; the env file, `install.env`,
  `PublicURL` and the issuer are the new one; whoami still answers; a domain that resolves nowhere is refused before
  anything changes; a rerun of the installer keeps the new domain.
- Unit tests: the agent's validation refuses crafted requests; password sign-in at the new domain with passkeys only on
  the old one.
- A valid Let's Encrypt certificate on a real domain is the maintainer's check on a real host (the VM uses `--acme none`).
