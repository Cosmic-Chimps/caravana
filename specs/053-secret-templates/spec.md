# Feature Specification: Secret templates

**Feature branch**: `053-secret-templates` · **Created**: 2026-11-24 · **Source**: the secrets talk (2026-11-11) and the
Aspire publisher: Aspire builds values from parameters, for example
`ConnectionStrings__postgres = Host=postgres;Username=banana;Password=<db password>`. **Plan**: [plan.md](./plan.md)

## Why

Feature 037's `from: {provider, path}` replaces a *whole* value. A password kept in Bella Baxter can't be part of a
connection string, so an Aspire app has to keep the whole string in Bella, or type it in Caravana. A secret that is
text with references inside it (a **template**) closes the gap: the owner (or the publisher) writes the text, the
password stays in the provider, and Caravana builds the value at each deploy.

## User stories

### US1 · Build a value from text and references (P1)

A secret in an app's definition (API, Compose overlay) is `{name, template, refs}`: `template` is text with
`${name}` places, `refs` maps each name to a provider reference `{provider, path}`. A deploy reads every
reference, renders the text and writes the result into the service's Nomad Variable like any other secret.

**Acceptance**
1. `"Host=postgres;Username=banana;Password=${pw}"` with `pw` = `bella-prod/DB_PASSWORD` deploys, and the service's
   environment holds `Host=postgres;Username=banana;Password=<the password>`.
2. `$$` is a literal `$`; the value read is inserted as it is (a `$` or `${x}` inside it is not expanded).
3. A secret has a value, a `from` or a template, only one. `refs` is required with a template and refused without one.
4. Each provider is read once per deploy, whatever number of references use it.

### US2 · Refusals name the field (P1)

**Acceptance**
1. A template that doesn't parse (a lone `$`, `${` without `}`, an invalid or empty name) is 400 `invalid_field` on
   `services[i].secrets[j].template` with the problem and the byte offset.
2. A `${name}` with no entry in `refs` is an error on `…template`; an entry no `${name}` uses is an error on
   `…refs.<name>`.
3. An unknown provider, or one not allowed for the app's place, is 400 on `…refs.<name>.provider`, before anything is
   saved. A path the provider doesn't have is 422 `secret_not_found` on `…refs.<name>.path`; a provider that refuses
   or is down is 422 `secret_provider_refused` or 502 `secret_provider_unavailable` on `…refs.<name>`.
4. A template is 1 to 4096 bytes of UTF-8 text without a NUL byte, with at most 16 references; the rendered value is
   text too.

### US3 · Refresh re-renders (P1)

**Acceptance**
1. After a value changes in the provider, Refresh secrets renders every template again and rolls the live version out
   when a built value changed; it deploys nothing when nothing did.
2. A provider that only a template uses can't be removed (the refusal names the app).

### US4 · Never a value (P1)

**Acceptance**
1. The template and its references are stored, recorded and shown as written. No rendered value or value of a
   reference is in the stored app, an answer, the kept Compose file, a deployment record, a log line, an error, a
   notice or the Nomad job (sentinel tests).
2. The console lists a template secret read-only ("template with N references: provider/path, …") on the Variables
   page and the Compose form, and keeps it through a Configure save and a Compose deploy. It has no editor.
3. A copy to another environment keeps the template and doesn't count it among the missing secrets; a promotion keeps
   the target's secrets, as before.

## Out of scope

A template editor in the console, other functions than `${name}` (default values, encodings), templates for plain
environment variables, references across services, Bella's change webhook, an MCP tool.
