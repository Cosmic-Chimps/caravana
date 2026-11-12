# Implementation Plan: Sign in with a passkey

**Branch**: `alta/ready-tabor-hzqw` | **Date**: 2026-11-12 | **Spec**: [spec.md](./spec.md)

## Summary

WebAuthn passkeys for the one admin, with `github.com/go-webauthn/webauthn` v0.18.2 doing the parsing and
verification. `service.Auth` gains the ceremonies (begin and finish for sign-in, confirm and add), the passkey list and
the password-sign-in setting; the store gains a `passkeys` table and two columns. The console gains the passkey
button on `/login`, Settings › Sign-in and one script of ours; the API gains five management operations; the host CLI
gains `admin passkeys` and a reset that turns password sign-in back on. A software authenticator in Go
(`internal/webauthntest`) drives the unit tests and the lab step `console-passkeys`.

## Technical Context

Go 1.27, `net/http`, `html/template` + htmx, SQLite (`modernc.org/sqlite`). New module: `github.com/go-webauthn/webauthn`
v0.18.2 (brings `fxamacker/cbor/v2`, `go-webauthn/x`, `golang-jwt/jwt/v5`, `google/go-tpm`, `google/uuid`,
`tinylib/msgp`, `go-viper/mapstructure/v2`; the metadata service is not configured, so nothing is fetched).
No new runtime dependency, no Nomad change, no policy change.

## Constitution Check

- **I. Small by Default**: one new Go module, justified in the commit message (WebAuthn's CBOR, COSE keys, attestation
  and assertion checks are not to be hand-rolled). No new runtime dependency. ✔
- **II, III, IV, VI**: untouched (no app model, deploy, Nomad or translator change). ✔
- **V. API-First**: the passkey list, rename, remove and the password-sign-in setting are API operations
  (`api/openapi.yaml` 0.36.0, same commit). The ceremonies are console-only: see Complexity Tracking. ✔ with entry
- **VII. Secure by Default**: no secret is stored: a passkey's public key and counter are verifiers, like the
  password hash (constitution VII's last sentence). No credential material is logged. Failed passkey sign-ins share
  the per-address limit. ✔
- **VIII. Plain, Tested Go**: table tests with a software authenticator; the CI checks. ✔

## Complexity Tracking

| Deviation | Why | Simpler alternative rejected because |
| --- | --- | --- |
| Adding a passkey and signing in with one are console routes with no API operation (V: "no feature exists only in the UI") | A WebAuthn ceremony is between a browser, the user's authenticator and the console's own origin: the credential is bound to that domain and to a person's device unlock. An API client authenticates with a token and has neither. `POST /login` is already console-only for the same reason. | An API ceremony (`/v1/account/passkeys/options` + create) would let a token holder mint a browser credential, and no API client can use one. |

## Decisions (research)

- **R1 Library**: go-webauthn v0.18.2. `BeginDiscoverableLogin` / `ValidatePasskeyLogin` for sign-in, `BeginLogin` /
  `ValidateLogin` with allowed credentials for "Confirm it's you", `BeginRegistration` / `CreateCredential` for adding.
  Responses are parsed with `protocol.ParseCredentialRequestResponseBytes` and
  `protocol.ParseCredentialCreationResponseBytes` from the posted form field. A `webauthn.WebAuthn` is built per
  request with `RPID`, `RPDisplayName: "caravana"` and `RPOrigins: [origin]`.
- **R2 Relying party**: the RP ID is the hostname the browser addressed (`r.Host` without the port, lowercased), the
  origin `scheme://r.Host` with the scheme from `proxies.HTTPS(r)` (the terminal's rule, 020.6). Passkeys are
  available only when (a) the hostname is a domain, not an IP address (browsers refuse an IP as an RP ID), (b) the
  request is HTTPS or the hostname is `localhost` / ends in `.localhost` (a secure context), and (c) when
  `CARAVANA_PUBLIC_URL` is set, the hostname is its host or a localhost name. Otherwise the pages say where passkeys
  work. Trusting `Host` is safe here: an assertion carries the SHA-256 of the RP ID and the origin it was made at, so
  a forged Host only matches a passkey made at that domain.
- **R3 Console domain** (for the password fallback and for turning password sign-in off): `CARAVANA_PUBLIC_URL`'s host
  when set, else the RP ID of the request. Through the API with no public URL, "a passkey exists" means any passkey.
- **R4 Password sign-in off**: `admin.password_signin = 0`. `SignIn` refuses (`ErrPasswordSignInOff`, no password
  check, not counted) when it is off **and** a passkey exists for the console domain; otherwise the password works,
  which keeps a way in after a domain change (the task's rule). `ResetPassword` sets it back to 1.
- **R5 Last passkey**: while password sign-in is off, removing the last passkey of its own domain is refused
  (`ErrLastPasskey`). The CLI's `admin passkeys remove` skips the rule (root is the owner) and prints a reminder.
- **R6 Confirm window ("sudo")**: `sessions.verified_at`. Creating a session sets it; `VerifyPassword` and a passkey
  confirm set it; adding a passkey needs it within 10 minutes (`confirmWindow`). Old sessions have NULL: not confirmed.
  A wrong password there counts against the per-address limit. The password confirms whenever it may sign in (R4's
  rule): while password sign-in is off and a passkey exists for the console domain, only a passkey confirms, but
  after a domain change the password confirms at the new domain so a passkey can be added there.
- **R7 Ceremonies** live in memory in `Auth` (a mutex and a map): a random 128-bit ID (base64url), the kind
  (`signin`, `confirm`, `register`), the `webauthn.SessionData`, the RP ID and origin, the address that asked, the
  session hash for `confirm` and `register`, and an expiry of 5 minutes. A finish takes the ceremony out of the map
  first (single use), then checks kind, session, RP and expiry. At most 8 pending per address and 512 in all; the
  oldest goes. The CLI never needs them, and a restart only drops ceremonies under way.
- **R8 Options**: user verification `required` everywhere; resident key `required`; attestation `none`; registration
  excludes the passkeys already stored for that RP ID; timeouts 2 minutes. The user is the admin: name and display name
  are the email, the ID is the admin's WebAuthn handle (32 random bytes, made at the first registration and stored on
  the admin row).
- **R9 Sign-in lookup**: the user handle in the assertion must equal the admin's handle, and the credential must be
  stored with `rp_id` = this RP ID; anything else is a failure (counted).
- **R10 Sign count**: after `ValidatePasskeyLogin`, a `CloneWarning` (count not above the stored one, unless both are
  0) is a failure; on success the count, flags and `last_used_at` are saved.
- **R11 Limit**: `FinishPasskeySignIn` calls `beginAttempt(address)` first (a locked address is refused before any
  parsing), then `recordFailure` on every failure, `clearFailures` on success, as `SignIn` does. Errors:
  `*SigninLimitError`, `*PasskeyFailure{TriesLeft}` (wraps `ErrPasskeyRefused`). Asking for options is not counted.
  The confirm ceremony's failures are counted the same way.
- **R12 Script**: `static/js/passkeys.js` (ours): feature detection (`window.PublicKeyCredential`, a secure context),
  base64url conversions (no reliance on `parseRequestOptionsFromJSON`/`toJSON`, for older Safari), `fetch` of the
  options with `POST` and `Accept: application/json`, then the browser call, then a hidden form's fields filled and
  `form.submit()` so the server answers with its usual 303 or page. Cancelling (`NotAllowedError`) shows a sentence
  and posts nothing. Elements are hidden until the script reveals them (`hidden` attribute), so a browser without
  JavaScript sees only the password path.
- **R13 Logs**: events `passkey_signin`, `passkey_signin_failed` (address and a reason word: `ceremony`, `credential`,
  `domain`, `verification`, `counter`), `passkey_confirmed`, `passkey_added`, `passkey_renamed`, `passkey_removed`,
  `password_signin_off`, `password_signin_on`, with our passkey ID only; never a credential ID, a key, a challenge,
  a client data blob or the library's error text.

## Project Structure

```
internal/store/migrations/0026_passkeys.sql   passkeys table; admin.password_signin, admin.webauthn_handle; sessions.verified_at
internal/store/passkeys.go (+_test)           CRUD; admin sign-in columns; session verified_at
internal/service/passkeys.go (+_test)         RelyingParty, ceremonies, Passkey, the Auth methods
internal/service/ceremonies.go (+_test)       the in-memory ceremony table
internal/service/auth.go, sessions.go         SignIn domain rule, ResetPassword, verified_at on create
internal/webauthntest/authenticator.go (+_test)  a software authenticator (ES256, packed "none" attestation)
internal/api/passkeys.go (+_test), errors     the five operations; api/openapi.yaml 0.36.0
internal/api/parity_test.go                   the console-only routes with reasons
cmd/caravana/admin.go (+_test)                passkeys list|remove, reset-password, status
internal/ui/passkeys.go (+_test)              /login/passkey*, /settings/sign-in*, /settings/passkeys*
internal/ui/login.go, templates/login.html, templates/settings.html, settingsnav.go, icons.go
internal/ui/static/js/passkeys.js, tools/ui-vendor.versions, internal/ui/static/app.css (tools/ui-css.sh build)
cmd/caravana/console_passkeys_test.go         lab step console-passkeys
changelog.d/passkeys.md, CLAUDE.md, docs (install's --domain note)
```
