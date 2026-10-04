# Contract: pages

Served by `internal/ui` on the main listener, never on the webhook-only one. Not part of
`api/openapi.yaml` (FR-033). Every page follows DESIGN.md and works without JavaScript
(FR-030). Every response carries the security headers from research R12.

| Route | Who | Does |
| --- | --- | --- |
| `GET /` | anyone | `303` to `/account` |
| `GET /login` | anyone | The sign-in form. Signed in already: `303` to `/account`. No admin: the host command instead of the form. |
| `POST /login` | anyone, same-origin | Sign in. Success: new session cookie, `303` to `next` (if safe, R11) or `/account`. Failure: the form again (`200`), email kept, with the sentence below. |
| `GET /account` | signed in | Change password, API tokens, sign out. |
| `POST /account/password` | signed in, same-origin | Change password; `303` to `/account` with a confirmation, or the page with the field error. |
| `POST /account/tokens` | signed in, same-origin | Create a token; `200` with the page showing the new token once (no redirect, R12). |
| `POST /account/tokens/{id}/revoke` | signed in, same-origin | Revoke; `303` to `/account`. |
| `POST /logout` | signed in, same-origin | End the session, clear the cookie, `303` to `/login`. |
| `GET /static/{file}` | anyone | Embedded CSS and fonts; long cache with `?v=<content hash>`. |

Not signed in on a signed-in route: `303` to `/login?next=<path>`. A cross-site non-safe
request: `403` page, nothing changed.

## `/login`, from the handoff's login screen

A centered 380px column on the bare ground, no shell:

1. The wordmark `caravana` (Plex Mono 600, 20px), and under it the host being signed in to, in
   Label Text mono.
2. Heading "Sign in".
3. Email and password fields (labels above in Label style), then a primary "Sign in" button.
4. Under the button, in Label Text: "Forgot your password? Reset it on the host:
   `caravana admin reset-password`."

Not on this page: SSO buttons, the "or with email" divider, the 2-step code step, the reset
flow (FR-029).

Sentences (PRODUCT.md voice: plain, calm, what happened and what to do):

| Case | Shown with | Text |
| --- | --- | --- |
| Wrong email or password, tries left | warning ◆ | "That email and password don't match. 3 tries left before a 15-minute wait." |
| Last try left | warning ◆ | "That email and password don't match. 1 try left before a 15-minute wait." |
| Locked | failed ■ | "Too many failed sign-ins from this address. Try again in 12 minutes." |
| No admin yet | idle ○ | "This caravana has no admin yet. On the host, run `caravana admin init --email you@example.com`." |
| Signed out | — | (no message) |
| Session expired | idle ○ | "Your session ended. Sign in again." |

Errors are announced (`role="alert"`), sit right above the button, and keep the email filled
in. The mark comes before the text, color and shape together (DESIGN.md, The Shape-and-Color
Rule).

## `/account`

The signed-in landing page until 007 moves it into the console shell as Settings. A centered
column (max 720px) with the wordmark at the top and "Sign out" (secondary button) at the right.
Headline sentence: "Signed in as you@example.com."

- **Password** section: current password, new password, a primary "Change password" button.
  Success: "Password changed. Other browsers were signed out."
- **API tokens** section: a panel of rows, newest first: name (mono), state pill (healthy ●
  active, idle ○ expired, failed ■ revoked), created and last used (mono, relative times with
  the exact time in `title`), expiry, and a danger "Revoke" button on active rows. Below, a form:
  name, optional expiry (date), a secondary "Create token" button.
- **New token** (only in the response that created it): a panel with the token in mono, a note
  "Copy it now. It won't be shown again.", and an example
  `curl -H "Authorization: Bearer …" https://<host>/v1/tokens`.

Revoking needs no modal in 006: it is reversible by creating a new token and can't touch
deployed apps. (007 may add a confirmation in the shell's style.)
