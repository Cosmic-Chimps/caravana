# Data Model: Minimal Auth

Reasons are in [research.md](research.md); Go signatures in
[contracts/go-packages.md](contracts/go-packages.md).

## Setting

| Env var | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `CARAVANA_TRUSTED_PROXIES` | `-trusted-proxies` | empty | Comma-separated IPs or CIDRs whose `X-Forwarded-For` and `X-Forwarded-Proto` caravana believes (R9). An entry that isn't an IP or CIDR fails startup, naming the setting. |

The `admin` CLI reads `CARAVANA_DATA_DIR` / `-data-dir` like the server.

## Migration `0003_auth.sql`

```sql
-- The one admin. id is always 1, so a second row can't exist (FR-001).
CREATE TABLE admin (
  id                  INTEGER PRIMARY KEY CHECK (id = 1),
  email               TEXT NOT NULL,   -- trimmed, lower case
  password_hash       TEXT NOT NULL,   -- pbkdf2-sha256$<iterations>$<salt>$<key> (R2)
  created_at          TEXT NOT NULL,   -- RFC 3339, UTC
  password_changed_at TEXT NOT NULL
) STRICT;

-- Signed-in browsers. Only the SHA-256 of the cookie value is stored (R6).
CREATE TABLE sessions (
  id_hash      TEXT PRIMARY KEY,       -- hex SHA-256 of the cookie value
  created_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,          -- written at most once a minute
  expires_at   TEXT NOT NULL           -- created_at + 7 days; idle limit is last_seen_at + 12 h
) STRICT;

-- API tokens. Only the SHA-256 of the token is stored (R10).
CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,       -- 26 lowercase base32 characters, not secret
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,   -- hex SHA-256 of the token
  created_at   TEXT NOT NULL,
  last_used_at TEXT,                   -- NULL until first used
  expires_at   TEXT,                   -- NULL: never
  revoked_at   TEXT                    -- NULL: not revoked
) STRICT;
CREATE INDEX api_tokens_name ON api_tokens (name);

-- Failed sign-ins per source address (IPv4 address or IPv6 /64), R8.
CREATE TABLE signin_limits (
  address      TEXT PRIMARY KEY,
  failures     INTEGER NOT NULL,
  window_start TEXT NOT NULL,
  locked_until TEXT                    -- NULL: not locked
) STRICT;
```

The webhook token table (feature 004) is untouched; webhook and API tokens never share a table
or a lookup (FR-025).

## Entities

### Admin

| Field | Rule |
| --- | --- |
| email | Required. Trimmed and lower-cased before it's stored and compared. Must contain one `@` with text on both sides; caravana never sends mail to it. |
| password | At least 12 characters (FR-004), no other rules. Never stored; only `password_hash`. |
| password_hash | `pbkdf2-sha256$600000$<salt>$<key>`; verification uses the iteration count stored in it. |

Lifecycle: none → **exists** (`admin init`). Password changes by `reset-password` (CLI, all
sessions end) or by the account page / `PUT /v1/account/password` (current password needed, other
sessions end). Nothing deletes the admin.

### Session

Valid while `now < expires_at` **and** `now < last_seen_at + 12h`. Ends on sign-out (row
deleted), password change (every other row deleted), reset (all rows deleted) or expiry (row
deleted at the next sign-in). The cookie value exists only in the browser.

### API token

| Field | Rule |
| --- | --- |
| name | 1–64 characters `[A-Za-z0-9._-]`, starting with a letter or digit. Unique among active tokens, else 409 `token_name_taken`, `field: name`. |
| expires_at | Optional; must be in the future when created, else 400 `invalid_field`, `field: expiresAt`. |

State, derived on read:

```text
revoked_at set                  → revoked
else expires_at set and passed  → expired
else                            → active
```

Only `active` tokens authenticate. Revoking an already revoked token changes nothing (204).
Revoked and expired tokens stay listed.

### Sign-in limit (per address)

```text
no row ──fail──▶ failures=1, window_start=now
row, now − window_start ≥ 15 min ──fail──▶ failures=1, window_start=now, locked_until=NULL
row, inside window ──fail──▶ failures+1; at 5: locked_until = now + 15 min
row, now < locked_until ──any try──▶ refused, password not checked
any row ──success──▶ row deleted
rows with window_start older than 1 day ──any write──▶ pruned
```

Tries left = `5 − failures` (shown after each failure). When locked, the page shows the minutes
until `locked_until`.

## Authentication of a request

```text
API route marked public (GET /v1/health, POST /v1/webhooks/{id}) → no check here
Authorization header present
  └─ "Bearer cvat_…" and an active token with that hash → OK (as the admin; last_used_at ≤ 1/min)
  └─ anything else → 401 unauthorized   (a valid session cookie doesn't rescue it)
no Authorization header, valid session cookie
  └─ safe method → OK
  └─ non-safe method, browser says cross-site → 403 cross_origin
  └─ non-safe method otherwise → OK
nothing valid (including "no admin yet") → 401 unauthorized, WWW-Authenticate: Bearer
```

Pages (`/account`, `/logout`): a valid session or a `303` to `/login?next=…`; non-safe methods
also pass the cross-site check (403 page). `/login` itself is public, and its `POST` also passes
the cross-site check.

## API schemas and errors

Full definitions in [contracts/openapi.yaml](contracts/openapi.yaml):

- `CreateTokenRequest {name, expiresAt?}` → 201 `CreatedToken` (`TokenInfo` + `token`, with a
  `Location: /v1/tokens/{tokenId}`).
- `GET /v1/tokens` → `TokenList {tokens: [TokenInfo]}`, newest first. `TokenInfo` =
  `{id, name, createdAt, lastUsedAt?, expiresAt?, revokedAt?, state}`.
- `DELETE /v1/tokens/{tokenId}` → 204; unknown ID → 404.
- `PUT /v1/account/password {currentPassword, newPassword}` → 204; a wrong current password →
  400 `invalid_field` (`currentPassword`); a short new one → 400 `invalid_field`
  (`newPassword`).

New error codes: `cross_origin` (403) and `token_name_taken` (409). `unauthorized` (401, from
feature 004) now also covers API routes.
