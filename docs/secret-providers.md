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
