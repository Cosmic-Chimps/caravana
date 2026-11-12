# Feature Specification: Sign in with a passkey

**Feature Branch**: `alta/ready-tabor-hzqw` (worktree; no feature branch of its own)
**Created**: 2026-11-12
**Status**: Implemented
**Input**: CodeAlta task `2026-11-11-sign-in-with-a-passkey-instead-of-a-code-based-second-factor-4p36`. The console's
only sign-in is an email and a password (006). A password can be phished, reused or guessed, and this console can
deploy anything, read logs and open a terminal into any app. Decided with the maintainer on 2026-11-11: passkeys
instead of a code-based second factor (TOTP), which the plan had as "2-step code in 2.6" for the login screen.

A passkey is phishing-resistant (it works only on the console's own domain), easier than a password (fingerprint,
Face ID or a device PIN), and already two factors (the device plus its unlock).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Add a passkey and sign in with it (Priority: P1) 🎯 MVP

The owner opens Settings › Sign-in, confirms it's them (their password, or a passkey they already have), names a
new passkey ("MacBook") and creates it with Touch ID. They sign out, and on `/login` they press "Sign in with a
passkey", touch the sensor, and are in, without typing their email.

**Why this priority**: it is the feature; everything else manages it.

**Independent Test**: with a virtual authenticator, add a passkey through Settings, sign out, sign in with it, and see
the Apps page; the passkey's row now says when it was last used.

**Acceptance Scenarios**:

1. **Given** a signed-in owner who confirmed it's them in the last 10 minutes (signing in counts), **When** they add
   a passkey named "MacBook", **Then** Settings › Sign-in lists it with its name, the domain it belongs to, when it
   was added and "never used".
2. **Given** a session older than 10 minutes, **When** the owner opens Settings › Sign-in, **Then** adding is
   offered only after "Confirm it's you" with the password or a passkey; a wrong password counts against the
   address's sign-in limit.
3. **Given** a passkey for this domain, **When** the owner signs in with it on `/login`, **Then** a session starts as
   with a password, and the passkey's sign count and last use are recorded.
4. **Given** `https://console.example.com` or `http://localhost:7070`, **When** the owner adds and uses a passkey,
   **Then** both work; on `http://127.0.0.1:7070` the page says passkeys need the console's https domain or
   `localhost` (browsers refuse an IP address as a passkey's domain).
5. **Given** a browser without JavaScript or without passkey support, **When** it opens `/login`, **Then** the
   password form works as before and no passkey button is shown.

---

### User Story 2 - Passkeys only (Priority: P1)

Once a passkey exists for the console's domain, the owner turns password sign-in off. The password form then
refuses, and only passkeys sign in. If every passkey is lost, `sudo caravana admin reset-password` on the host turns
password sign-in back on.

**Why this priority**: a password that still works can still be phished; this is what makes passkeys a real gain.

**Independent Test**: turn password sign-in off, see the password post refused with the form hidden, run
`caravana admin reset-password`, and sign in with the new password.

**Acceptance Scenarios**:

1. **Given** no passkey for the console's domain, **When** the owner tries to turn password sign-in off, **Then** it
   is refused saying a passkey for this domain is needed first.
2. **Given** password sign-in off, **When** anyone posts the password form, **Then** it is refused without the
   password being checked, saying password sign-in is off and how to recover on the host.
3. **Given** password sign-in off and one passkey left for the console's domain, **When** the owner removes it,
   **Then** it is refused: the page warns that it would leave no way in, and says to turn password sign-in on first
   or add another passkey.
4. **Given** password sign-in off, **When** the owner runs `caravana admin reset-password` on the host, **Then**
   password sign-in is on again, every browser is signed out, and the passkeys are kept.
5. **Given** password sign-in off, **When** the console's domain changes and no passkey exists for the new one,
   **Then** the password form works at the new domain until a passkey is added there.

---

### User Story 3 - Manage passkeys (Priority: P2)

The owner renames a passkey, removes one, and sees which are made for another domain. The same list, rename,
remove and the password-sign-in setting are in the API, and the host CLI can list and remove passkeys.

**Independent Test**: rename and remove through the console and through the API with a token; list and remove with
`caravana admin passkeys`.

**Acceptance Scenarios**:

1. **Given** two passkeys, **When** the owner renames one, **Then** the list shows the new name.
2. **Given** a passkey made at another domain, **When** the owner opens Settings › Sign-in, **Then** its row says it
   doesn't work at this address.
3. **Given** an API token, **When** a client calls `listPasskeys`, `updatePasskey`, `deletePasskey`, `getSignIn` and
   `putSignIn`, **Then** they do what the console does; a deploy token gets 403 `token_scope`.
4. **Given** the host's shell, **When** the owner runs `caravana admin passkeys list` and
   `caravana admin passkeys remove --id <id>`, **Then** the passkeys are listed and the one is removed (the last-one
   rule doesn't apply there: root on the host is the owner).

---

### User Story 4 - Refusals and the limit (Priority: P1)

A passkey made for another domain, a replayed assertion, an assertion whose sign count went backwards, and one
without user verification are refused, and each refusal counts against the address's sign-in limit like a wrong
password (5 in 15 minutes, then 15 minutes of refusals).

**Independent Test**: with a virtual authenticator, try each; after the fifth failure even a good passkey is refused
until the wait ends.

### Edge Cases

- A ceremony is good for 5 minutes and one finish; a second finish with the same ceremony is refused (replay).
- Pending ceremonies are held in memory, at most 8 per address and 512 in all (oldest dropped); a restart drops them.
- A passkey ceremony started in one session can't be finished in another; a sign-in ceremony can't be finished as a
  registration and back.
- The user cancels the browser's prompt: the page says so and posts nothing.
- No admin yet: `/login` shows the host command, as before; no ceremony is offered.
- `caravana admin reset-password` doesn't remove passkeys.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001** `/login` offers "Sign in with a passkey" (discoverable credentials, user verification required) next to
  the password form, shown only by the script when the browser supports passkeys and the address can use them.
- **FR-002** Settings › Sign-in (Security group) lists passkeys (name, domain, added, last used), renames, removes,
  adds (named, 1–64 characters), and turns password sign-in on or off.
- **FR-003** Adding a passkey needs a session confirmed in the last 10 minutes: signing in confirms it, and so does
  "Confirm it's you" with the password (while password sign-in is on) or a passkey for this domain.
- **FR-004** The relying party is the domain the browser addressed (`Host`, after trusted proxies for the scheme); the
  origin is that scheme and host. Passkeys are offered only over HTTPS or on `localhost` / `*.localhost`, never on an
  IP address, and, when `CARAVANA_PUBLIC_URL` is set, only at its host or a localhost name.
- **FR-005** Password sign-in is refused when it is turned off and a passkey exists for the console's domain
  (`CARAVANA_PUBLIC_URL`'s host when set, else the host the browser addressed). Turning it off needs such a passkey.
- **FR-006** While password sign-in is off, removing the last passkey of its domain is refused (409 `last_passkey`).
- **FR-007** `caravana admin reset-password` turns password sign-in back on and says so; `caravana admin passkeys
  list|remove --id` exist; `caravana admin status` says whether password sign-in is on and how many passkeys there are.
- **FR-008** Every failed passkey sign-in (unknown, expired or replayed ceremony, unknown credential, other domain,
  bad signature, missing user verification, sign count not increasing) counts against the per-address limit; a locked
  address is refused before any verification.
- **FR-009** Only public keys and counters are stored (`passkeys` table); nothing secret about a passkey exists on
  the server. No page, log line or answer holds anything an authenticator keeps private.
- **FR-010** API 0.36.0: `listPasskeys`, `updatePasskey`, `deletePasskey`, `getSignIn`, `putSignIn`. The ceremonies
  are console routes only (like `POST /login`). API tokens are unchanged.
- **FR-011** The script is ours, vendored (`ours` in `tools/ui-vendor.versions`), loaded only on the pages that use
  it; the CSP stays `script-src 'self'`, no inline script.
- **FR-012** Each passkey records the WebAuthn user handle of its owner, so users and roles (Phase 4) adds a user ID
  to each passkey and nothing else.

### Key Entities

- **Passkey**: our ID, owner's user handle, credential ID, domain (RP ID), public key (COSE), attestation type and
  format, AAGUID, sign count, transports, flags (user present, user verified, backup eligible, backup state), name,
  added, last used.
- **Sign-in settings**: password sign-in on or off (on the admin row); the admin's WebAuthn user handle.
- **Session**: gains `verified_at`, the last time it was confirmed.

## Success Criteria *(mandatory)*

- **SC-001** The owner adds a passkey in Settings, signs out, and signs in with it at `http://localhost:7070` and at the
  console's https domain.
- **SC-002** With password sign-in off, the password form refuses; `caravana admin reset-password` restores it.
- **SC-003** Another domain's passkey, a replayed assertion and a backwards sign count are refused and counted.
- **SC-004** A sentinel (the virtual authenticator's private key) is found in no page, log line, answer or the SQLite
  file; the lab has a `console-passkeys` step with a virtual authenticator.
