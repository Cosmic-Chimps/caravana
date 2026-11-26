# Secrets from a secret provider (Bella Baxter)

An app's secret can come one of three ways:

| | Who fetches the value | Where it lives while the app runs | Rotation |
|---|---|---|---|
| A typed secret | you type it in Caravana | a Nomad Variable only that service reads | deploy again |
| **A provider reference** | Caravana, at each deploy | copied into the same Nomad Variable | Refresh secrets |
| The app fetches it (`bella run` in the container) | the app, at each start | the app's process only | none needed |

This page is about the second way. Bella Baxter is the first provider.

## Connect Bella Baxter

1. In Bella's console, open the environment the apps use (for example `shop` › `production`) and make an
   **API key for Caravana alone**. Bella makes a key pair with it, records the public half and shows the
   private key once: copy both the API key (`bax-…`) and the private key.
2. In Caravana, open **Settings › Secret providers** and add one:
   - **Name**: how apps refer to it, a lowercase name such as `bella-prod`.
   - **Bella URL**, **project** and **environment**: where the secrets are.
   - **API key** and **private key**: the two from step 1. Caravana keeps them in the Nomad Variable
     `caravana/secret-providers/<name>`, never in its database, a page or a log.
   - **Allowed in** (optional): the Caravana projects (`shop`) or project environments
     (`shop/production`) that may use it. Empty means every project.
3. Press **Test**. It reads the environment once and says how many secrets Caravana can read, or why it
   can't (the key refused, no private key registered for this key, not this key's environment, Bella
   unreachable, an answer that couldn't be decrypted).

> [!IMPORTANT]
> **Allowed in** is a security boundary. Everyone who can configure apps in an allowed place (maintain, see
> [people and roles](people.md)) can point an app there at any path the provider's key can read, and anyone who
> may deploy an app can run an image that reads what the app has. In a preview (`pr-<n>`), a person below
> maintain, or a deploy token, may only use the references the base app already has.

> [!WARNING]
> Bella encrypts every answer to the private key (zero-knowledge encryption), and Caravana decrypts it to
> copy the values into Nomad Variables. So Caravana holds the private key: give it a key made for
> Caravana alone, in the one environment its apps use, and revoke that key in Bella if the host is
> compromised.

## Use a secret from Bella

In the app's definition a secret is either a value or a reference:

```json
{"name": "DATABASE_URL", "from": {"provider": "bella-prod", "path": "DATABASE_URL"}}
```

`path` is the secret's name in the provider's Bella environment. The same `from` works in a Compose
overlay's `secrets`. In the console, the Variables page's Add form has **From a provider**: pick the
provider and type the path. The page shows "from `bella-prod` · `DATABASE_URL`", never the value.

At each deploy Caravana reads every reference, one call per provider, then writes the values into the
service's Nomad Variable before Nomad gets the new version, exactly as for a typed value. If it can't:

| What happened | Answer | The deploy |
|---|---|---|
| The provider doesn't exist, or isn't allowed for this project/environment | 400 `invalid_field` on `services[i].secrets[j].from.provider` | refused before anything is saved |
| Bella has no secret with that path | 422 `secret_not_found` on `…from.path` | fails, nothing replaced |
| Bella refuses the keys | 422 `secret_provider_refused` on `…from` | fails, nothing replaced |
| Bella is down or slow | 502 `secret_provider_unavailable` on `…from` | fails, nothing replaced |

Running copies are never touched by a failed read: they keep the values they started with, and a copy
that restarts while Bella is down starts with the values already in Nomad. Only deploys and refreshes
talk to Bella.

## Templates: a value built from text and references

A reference replaces a whole value. Often the value you need is a *line of text with one secret in it*: a
connection string with a database password, a URL with a token. A secret can then be a **template**:

```json
{"name": "ConnectionStrings__postgres",
 "template": "Host=postgres;Username=banana;Password=${pw}",
 "refs": {"pw": {"provider": "bella-prod", "path": "DB_PASSWORD"}}}
```

At each deploy Caravana reads the references, puts each value where its `${name}` is and writes the result
into the service's Nomad Variable like any other secret. The same `template` and `refs` work in a Compose
overlay's `secrets` (the Aspire publisher sends them), and in the API's `deployApp` body.

The rules:

- `${name}` inserts the value of `refs.name`. `$$` is a literal `$` (write `$$5` for "$5"). Any other
  `$` (a lone one, `${` with no `}`, a name that isn't letters, digits and `_` starting with a letter or
  `_`) is refused with the byte where it is.
- A value from the provider is inserted exactly as it is. A `$` or `${x}` inside it is never expanded
  again, so a password such as `pa$${word}` arrives as typed.
- Every `${name}` needs an entry in `refs`, and every entry must be used. The template is 1 to 4096
  bytes of UTF-8 text without a NUL byte, with at most 16 references. A secret has a `value`, a `from`
  or a `template`, only one.
- Each reference is checked like a `from`, and its failures name the reference:

| What happened | Answer |
|---|---|
| The template doesn't parse, or uses a name `refs` lacks | 400 `invalid_field` on `services[i].secrets[j].template` |
| A reference is never used | 400 `invalid_field` on `…refs.<name>` |
| The provider doesn't exist or isn't allowed here | 400 `invalid_field` on `…refs.<name>.provider` |
| The provider has no secret with that path | 422 `secret_not_found` on `…refs.<name>.path` |
| The provider refuses, or is down | 422 `secret_provider_refused` or 502 `secret_provider_unavailable` on `…refs.<name>` |

The template and its references are not secret: Caravana stores and shows them as written ("template with 2
references: `bella-prod/DB_USER`, `bella-prod/DB_PASSWORD`"). The built value is never stored, shown or
logged. The console lists a template secret read-only on the Variables page and keeps
it through a save and through a deploy from the Compose form; it has no editor for templates yet, so set one through the API or the Compose overlay.

**Refresh secrets** renders every template again and rolls the app out when a built value changed, and a
provider with a template that uses it can't be removed, as for a plain reference.

## Rotate a value

Change the value in Bella, then press **Refresh secrets** on the app's Variables page, or:

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" https://caravana.example.com/v1/apps/<app>/secrets/refresh
```

Caravana reads every reference of the running version again. When a value changed, it rolls the running
version out again with the same images and the new values (a normal health-checked deploy, shown as
"Refreshed secrets" in the history); when nothing changed it answers `{"changed": false}` and deploys
nothing. The app's deploy token may call it, so CI can refresh after rotating.

Refresh rolls out the version that is running: if a later change of the app's definition never got
healthy, the running definition becomes the app's again, as a rollback does.

## Rollback reads today's values

"Redeploy this version" deploys an old version's definition and images, but its references are read
**now**: it gets today's values from Bella, not the ones it ran with. Typed secrets keep their current
values, as before.

## Removing a provider

A provider some app still references can't be removed: the refusal names the apps. Remove or change
their references first. Changing **Allowed in** so that an app may no longer use the provider doesn't
touch the running app; its next deploy is refused naming the field.

## Debug logging

Bella's Go SDK logs requests to stderr when the environment variable `BELLA_DEBUG=1` is set in
Caravana's environment. It masks the API key's headers and never logs a successful answer (which holds
values), but it does log the body of a failed one. Leave it unset on a host.
