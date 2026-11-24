# API 0.59.0: people and roles

Every protected route is checked against its access rule (plan D4) after the credentials and, for a deploy token, its
scope. Refusal: 403

```json
{"error": {"code": "not_allowed", "message": "your role in shop › production is view; this needs deploy", "field": ""}}
```

`field` is set when a body deploy changes more than images (`services[0].env`). A route no rule lists is refused for
everyone with the same code ("this isn't open to your role").

## Schemas

- `Role`: `view` | `deploy` | `maintain`. `EnvironmentRole`: `none` | `view` | `deploy` | `maintain`.
- `ProjectRole`: `{project, role: Role, endsAt?: date-time, environments?: {<environment>: EnvironmentRole}}`.
- `Person`: `{id, email, orgRole: owner|admin|member, state: invited|active, endsAt?, invitedBy?, createdAt,
  inviteExpiresAt?, projects: [ProjectRole]}`.
- `Me`: `Person` without `inviteExpiresAt`, plus `token?: {name, scope?: TokenScope}` when the call came with a token.
- `PersonInput`: `{email (create only), orgRole, endsAt?, projects?: [ProjectRole]}`.
- `Invited`: `{person: Person, inviteToken: "cvin_…", inviteUrl}` — the token is in this answer only.
- `actor?: string` (an email) on `Deployment`, `AppAction`, `Backup` and `Restore`.

## Operations

| Operation | Method and path | Need | Answers |
| --- | --- | --- | --- |
| `getMe` | `GET /v1/me` | signed in | 200 `Me` |
| `listPeople` | `GET /v1/people` | admin | 200 `{people: [Person]}` |
| `invitePerson` | `POST /v1/people` | admin (owner for owner/admin) | 201 `Invited`, `Cache-Control: no-store`; 400 field; 404 `project_not_found`/`environment_not_found`; 409 `email_taken` |
| `getPerson` | `GET /v1/people/{personId}` | admin | 200 `Person`; 404 `person_not_found` |
| `updatePerson` | `PUT /v1/people/{personId}` | admin (owner for owner/admin) | 200 `Person`; 409 `last_owner` |
| `removePerson` | `DELETE /v1/people/{personId}` | admin (owner for owner/admin) | 204; 409 `last_owner` |
| `reinvitePerson` | `POST /v1/people/{personId}/invite` | admin (owner for owner/admin) | 200 `Invited`; 409 `not_invited` |
| `acceptInvite` | `POST /v1/invites/accept` `{token, password}` | public | 200 `{email}`; 400 field `password`; 404 `invite_invalid`; 429 as sign-in |

Changed: `listTokens`, `createToken`, `revokeToken` act on the caller's own tokens; `createToken` with a scope needs deploy
on some environment of the project (403 `not_allowed`). `listPasskeys`, `updatePasskey`, `deletePasskey`, `getSignIn`,
`putSignIn`, `changePassword` act on the caller. Lists that span projects are filtered (plan D6). Deploy tokens are denied
the new routes by the default deny.
