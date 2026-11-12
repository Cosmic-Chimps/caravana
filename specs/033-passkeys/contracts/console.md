# Console contract: passkeys

Every POST is wrapped in `s.protected` (cross-origin protection). JSON answers are `Cache-Control: no-store`, with
errors as `{"error": {"code", "message"}}` and the right 4xx. Pages follow DESIGN.md and use the existing
`cv-setrow`, `cv-notice`, `cv-btn`, `cv-field` pieces; no new design language.

## Sign-in page `/login`

- After the password form (or in its place when password sign-in is off for this domain): a
  `<div data-passkey-signin hidden>` with a secondary button "Sign in with a passkey" (primary when the password form is
  hidden), a status line (`role="status"`), and a hidden `<form method="post" action="/login/passkey">` with `next`,
  `ceremony` and `credential`. The script reveals it only when the browser supports passkeys and the server rendered
  `data-available="true"` (R2).
- Password sign-in off: the form is not drawn; the page says "Password sign-in is off on this caravana. Lost your
  passkeys? On the host, run:" with `caravana admin reset-password`, and in `<noscript>` "Passkeys need JavaScript."
- `POST /login/passkey/options` (no session): 200 `{ceremony, publicKey}`; 409 `passkeys_unavailable` (R2, with the
  sentence saying where they work); no admin: 409 `no_admin`.
- `POST /login/passkey` (form `ceremony`, `credential`, `next`): success sets the session cookie and answers 303 to
  `next` or `/apps`; a failure draws `/login` (200) with one notice: "That passkey didn't work here." plus the tries
  left (the password sentences' shape), or the limit's sentence.
- `POST /login` while password sign-in is off for this domain: 200 with the notice "Password sign-in is off. Sign in
  with a passkey." and no password check.

## Settings › Sign-in `/settings/sign-in`

Nav: the Security group becomes Sign-in (`/settings/sign-in`, icon `fingerprint`), then API tokens. Lede: "How you sign
in to this caravana: passkeys and your password."

1. **Passkeys** rows: name; domain in mono; "Added Oct 22" and "Last used 3 hours ago" / "Never used"; "Synced" when
   backed up. A row for another domain is marked warning: "Made for `old.example.com`: it doesn't work at this
   address." Each row has Rename (a `<details>` with the name field, posting `/settings/passkeys/{id}`) and Remove (a
   `<details>` confirm, posting `/settings/passkeys/{id}/remove`). The last passkey of this domain while password sign-in
   is off has no Remove; it says "Your only passkey for `<domain>`, and password sign-in is off: turn it on first, or
   add another passkey." No passkeys: "No passkeys yet."
2. **Add a passkey**: when passkeys aren't available here (R2), one sentence saying where they work. When the session
   isn't confirmed in the last 10 minutes: "Confirm it's you first." with the password form (`POST
   /settings/sign-in/verify`, field `password`; not drawn while password sign-in is off) and a "Use a passkey" button
   (script, only with a passkey for this domain). When confirmed: a Name field (required, 1–64, placeholder "MacBook")
   and an "Add passkey" button (script), plus a hidden form posting `/settings/passkeys` with `name`, `ceremony`,
   `credential`. Without JavaScript: "Adding a passkey needs JavaScript."
3. **Password sign-in**: "On" or "Off (passkeys only)" with one button: "Turn password sign-in off" (disabled, with the
   reason, when no passkey exists for the console's domain) or "Turn password sign-in back on". Under it: "Lost every
   passkey? On the host, `sudo caravana admin reset-password` turns password sign-in back on."
4. A note: "A passkey belongs to the domain it was made at, `<domain>`. If the console's domain changes, passkeys made
   here stop working there; password sign-in keeps working at the new domain until you add a passkey there."

Routes:

| Route | What | Answer |
| --- | --- | --- |
| `GET /settings/sign-in` | the page | `?added=1`, `?renamed=1`, `?removed=1`, `?saved=1`, `?verified=1` notices |
| `POST /settings/sign-in/verify/options` | confirm ceremony (JSON) | 200 `{ceremony, publicKey}`; 409 `passkeys_unavailable` / `no_passkey` |
| `POST /settings/sign-in/verify` | `password`, or `ceremony` + `credential` | 303 `/settings/sign-in?verified=1#add`; failure: the page (200) with the notice |
| `POST /settings/passkeys/options` | registration ceremony (JSON) | 200 `{ceremony, publicKey}`; 403 `not_confirmed`; 409 `passkeys_unavailable` |
| `POST /settings/passkeys` | `name`, `ceremony`, `credential` | 303 `?added=1`; 422 page (name or credential refused); `not_confirmed` page |
| `POST /settings/passkeys/{id}` | `name` | 303 `?renamed=1`; 422 page |
| `POST /settings/passkeys/{id}/remove` | | 303 `?removed=1`; 409 page with the last-passkey sentence |
| `POST /settings/sign-in` | `password=on|off` | 303 `?saved=1`; 409 page with the no-passkey sentence |

Parity (`internal/api/parity_test.go`): the GET reads `settingsReads` + `listPasskeys` + `getSignIn`; rename is
`updatePasskey`, remove `deletePasskey`, the setting `putSignIn`; the two options routes, the two finishes
(`/login/passkey`, `/settings/passkeys`) and `/settings/sign-in/verify` are console-only with reasons (plan,
Complexity Tracking), listed in one set next to `POST /login`, `/logout` and `/settings/appearance`.

## Script `static/js/passkeys.js`

Loaded with `defer` from `login.html` and the Sign-in page only. Reads `data-` attributes, never inline script. One
function per button: fetch options (POST, `Accept: application/json`, same-origin credentials), convert base64url
fields (`challenge`, `user.id`, `excludeCredentials[].id`, `allowCredentials[].id`) to `ArrayBuffer`, call
`navigator.credentials.create|get`, encode the answer (`id`, `rawId`, `type`, `authenticatorAttachment`,
`response.clientDataJSON`, `attestationObject` + `getTransports()` or `authenticatorData` + `signature` + `userHandle`,
`clientExtensionResults`) as base64url JSON into the hidden form's `credential`, and submit. Errors go to the status
line in words ("Cancelled.", "This browser couldn't use a passkey here.", the server's message).
