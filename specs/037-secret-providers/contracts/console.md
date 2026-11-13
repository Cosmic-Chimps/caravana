# Console contract: secret providers (037)

Follows `DESIGN.md` and the existing Settings and Variables pages; forms work without JavaScript; no value or
credential is ever drawn, echoed back into a form, put in a redirect or logged.

## Settings › Secret providers

`/settings/secret-providers`, in the Settings nav's group with Registries and Backup storage (icon `key-round`
or the closest one already vendored). One row per provider:

- `bella-prod` (mono) · "Bella Baxter · `shop`/`production`" · the URL's host · "All projects" or the allowed list
  (mono) · "Used by 2 apps" · the last test ("12 secrets readable · Nov 13", or the reason, in the failed tone).
- Row actions: Edit (opens the form filled with the non-secret settings), Test (`POST
  /settings/secret-providers/test`, hidden `name`), Remove (`POST /settings/secret-providers/remove`; refused with
  the apps named when in use).

Add / edit form (`POST /settings/secret-providers`): Name (read-only when editing), Bella URL (placeholder
`https://api.bella-baxter.io`), Bella project, Bella environment, API key (password input; when editing, hint
"Leave empty to keep the one stored"), Private key (textarea; same hint), "Allowed in" (text, comma separated
`project` or `project/environment`, hint "Empty: every project"). Field errors under each field. The notice above
the form:

> Caravana reads these values at each deploy and copies them into Nomad Variables, so it holds this API key and,
> with zero-knowledge encryption, its private key. Make a key for Caravana alone, in the Bella environment the apps
> use.

Empty state: "No secret providers. Connect Bella Baxter to use its secrets in apps without typing them here."

## Variables & secrets page

- A secret row with a reference shows, where a typed secret shows its hidden marker:
  "from `bella-prod` · `DATABASE_URL`" (both mono). Remove works the same.
- The Add form's "Secret" choice gains a second way: radio `source=value` ("A value") / `source=provider`
  ("From a provider"), with a Provider select (only providers allowed for this app's place) and a Path input. With
  no allowed provider, the radio is absent and a line links to Settings › Secret providers. Field errors land under
  the Path or Provider input.
- Aside "Where secrets come from" (the design's panel): one row "Caravana · Built in · stored in Nomad Variables ·
  N values", then one row per provider the app references or may use: its status mark from the last test, name,
  "Bella Baxter · project/environment", "N references". Below: "Connect a source" (link to Settings › Secret
  providers) and the line "To use a value from a source, pick it when you add a secret. Caravana shows the path,
  never the value."
- When the app has a reference: **Refresh secrets** (`POST /apps/{id}/secrets/refresh`, revision not needed) under
  the panel, with the hint "Reads every reference again and rolls the app out if a value changed." Answers: changed
  → 303 to the Deployments tab; unchanged → the page with the notice "Secrets unchanged: nothing to roll out.";
  a refusal → the page with the reason and its status.

## Deployments tab

- A refresh's history entry reads "Refreshed secrets" as its kind words, with "(vN's version)" like a redeploy.
- "Redeploy this version" of a version whose definition has a reference gets the line "Reads secrets from
  providers again now, not as they were."

## Compose form

A secret in the kept overlay with `from` is listed read-only ("from `bella-prod` · `DATABASE_URL`") and kept
through the draft and the deploy; references are added on the Variables page, which writes them into the kept
overlay for a kept-file app.
