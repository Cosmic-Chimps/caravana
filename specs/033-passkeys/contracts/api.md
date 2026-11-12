# API contract: passkeys (API 0.36.0)

All five take a full API token or a signed-in session (the document's default security); a deploy token is 403
`token_scope` by the default deny. None of them starts a ceremony.

## Schemas

```yaml
Passkey:
  required: [id, name, domain, createdAt, lastUsedAt, transports, backedUp]
  properties:
    id:         {type: string}                     # ours, not the credential ID
    name:       {type: string, maxLength: 64}
    domain:     {type: string}                     # the RP ID it was made at
    createdAt:  {type: string, format: date-time}
    lastUsedAt: {type: [string, "null"], format: date-time}
    transports: {type: array, items: {type: string}}
    backedUp:   {type: boolean}                    # a synced passkey (backup state)
SignIn:
  required: [passwordSignIn, passkeys]
  properties:
    passwordSignIn: {type: boolean}
    passkeys:       {type: integer}
```

## Operations

| Operation | Route | Body | Answers |
| --- | --- | --- | --- |
| `listPasskeys` | `GET /v1/account/passkeys` | | 200 `{passkeys: [Passkey]}` |
| `updatePasskey` | `PATCH /v1/account/passkeys/{passkeyId}` | `{name}` | 200 `Passkey`; 404 `passkey_not_found`; 422 `invalid_field` `name` (1–64 characters, no control characters) |
| `deletePasskey` | `DELETE /v1/account/passkeys/{passkeyId}` | | 204; 404 `passkey_not_found`; 409 `last_passkey` (password sign-in is off and it is the last passkey of its domain) |
| `getSignIn` | `GET /v1/account/sign-in` | | 200 `SignIn` |
| `putSignIn` | `PUT /v1/account/sign-in` | `{passwordSignIn}` | 200 `SignIn`; 409 `no_passkey` (turning it off with no passkey for the console's domain: `CARAVANA_PUBLIC_URL`'s host, or any passkey when it isn't set) |

Messages: `last_passkey` "this is the last passkey for <domain> and password sign-in is off: turn password sign-in on
first, or add another passkey"; `no_passkey` "add a passkey for <domain> before turning password sign-in off".
