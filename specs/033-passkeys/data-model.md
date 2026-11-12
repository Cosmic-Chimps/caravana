# Data model: passkeys

## Migration `0026_passkeys.sql`

```sql
-- Passkeys (feature 033). Only public material: a credential's public key and counter are verifiers,
-- like the password hash. user_handle is the WebAuthn user.id of the owner (the admin's handle today);
-- users and roles (Phase 4) adds a user_id column and nothing else.
CREATE TABLE passkeys (
  id                 TEXT PRIMARY KEY,         -- 26 lowercase base32 characters (rand.Text), not secret
  user_handle        BLOB NOT NULL,
  credential_id      BLOB NOT NULL UNIQUE,
  rp_id              TEXT NOT NULL,            -- the domain it was made at
  public_key         BLOB NOT NULL,            -- COSE_Key as the authenticator gave it
  attestation_type   TEXT NOT NULL,
  attestation_format TEXT NOT NULL,
  aaguid             BLOB NOT NULL,
  sign_count         INTEGER NOT NULL,
  transports         TEXT NOT NULL,            -- comma-separated, "" for none
  user_present       INTEGER NOT NULL,
  user_verified      INTEGER NOT NULL,
  backup_eligible    INTEGER NOT NULL,
  backup_state       INTEGER NOT NULL,
  name               TEXT NOT NULL,
  created_at         TEXT NOT NULL,
  last_used_at       TEXT                      -- NULL: never used
) STRICT;
CREATE INDEX passkeys_rp ON passkeys (rp_id);

ALTER TABLE admin ADD COLUMN password_signin INTEGER NOT NULL DEFAULT 1;  -- 0: passkeys only
ALTER TABLE admin ADD COLUMN webauthn_handle BLOB;                         -- 32 random bytes, made at the first registration
ALTER TABLE sessions ADD COLUMN verified_at TEXT;                          -- last confirmation; NULL for sessions from before
```

Times are written with the store's `formatTime` (RFC 3339, nine fraction digits).

## Store

- `PasskeyRecord` (every column), `InsertPasskey`, `ListPasskeys` (by `created_at`), `GetPasskey(id)`,
  `GetPasskeyByCredential(credentialID)`, `RenamePasskey(id, name)`, `DeletePasskey(id)`,
  `UpdatePasskeyUse(id, signCount, flags, at)`, `CountPasskeys(rpID)` ("" = all).
- `AdminRecord` gains `PasswordSignIn bool` and `WebAuthnHandle []byte`; `SetPasswordSignIn(on)`,
  `SetWebAuthnHandle(handle)` (only when NULL, in one statement, so two first registrations agree; it returns the
  stored one). `SetPassword` (reset) keeps its behaviour; the service also sets password sign-in on.
- `SessionRecord.VerifiedAt *time.Time`; `CreateSession` writes it; `SetSessionVerified(idHash, at)`.

## Service types

```go
type RelyingParty struct{ ID, Origin string }      // "console.example.com", "https://console.example.com"
type Passkey struct {
    ID, Name, Domain string
    CreatedAt        time.Time
    LastUsedAt       *time.Time
    Transports       []string
    BackedUp         bool                          // backup_state: a synced passkey
}
type PasskeyOptions struct{ Ceremony string; Options json.RawMessage } // the publicKey object as JSON
type SignInSettings struct{ PasswordSignIn bool; Passkeys int }
```
