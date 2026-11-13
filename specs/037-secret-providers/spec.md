# Feature Specification: Secret provider references, Bella Baxter first

**Feature branch**: worktree `alta/lucid-reprise-qgp5` · **Created**: 2026-11-13 · **Source**: CodeAlta task `ptfn`
("05 · Secret provider references, Bella Baxter first"), decided with the maintainer on 2026-11-11 (the secrets talk)
and 2026-11-13 (API key, Bella's Go SDK). **Plan**: [plan.md](./plan.md)

## Why

An app's secrets come one of three ways. **A**: the owner types the value (feature 008, today). **B**: the app names
where the value lives, and Caravana fetches it at deploy and copies it into the same Nomad Variable (this feature).
**C**: the app fetches its own secrets at start (`bella run` in the container; task 06). B lets a team keep one source
of truth in a secret manager without the app knowing about it, and without a running app ever waiting on that manager.

## User stories

### US1 · Connect Bella Baxter (P1)

The owner opens Settings › Secret providers, adds "bella-prod": Bella's URL, the Bella project and environment, an
environment API key and the private key Bella showed when it made that key (zero-knowledge encryption). Optionally
they limit it to some Caravana projects or project/environments. **Test** reads the environment once and says how
many secrets Caravana can read, or why it can't.

**Acceptance**
1. Saving keeps the API key and the private key only in the Nomad Variable `caravana/secret-providers/<name>`;
   SQLite, pages, answers and logs never hold them. A save without them keeps the ones stored before.
2. A bad API key form (not `bax-…`), a private key that is not PKCS#8 P-256 (PEM or base64 DER), an `http://` URL
   that isn't loopback, or an empty project/environment is a field error naming the field.
3. Test with a working key answers "N secrets readable". Each failure says it in Caravana's words (refused key, no
   private key registered for this key, not this key's environment, Bella unreachable, answer couldn't be
   decrypted) and never quotes Bella's or the SDK's text.
4. A provider some app still references can't be removed: the refusal names the apps.

### US2 · Reference a secret (P1)

In an app's definition (API, Compose overlay, or the console's Variables page) a secret is either
`{name, value?}` as today or `{name, from: {provider, path}}`. Deploying resolves every reference, then writes the
values into the service's Nomad Variable exactly like typed values, before Nomad gets the job.

**Acceptance**
1. A service with a reference deploys with the value in its environment.
2. A secret with both `value` and `from` is a field error on `services[i].secrets[j].from`.
3. An unknown provider, or one not allowed for the app's project/environment, is a field error on
   `services[i].secrets[j].from.provider`, before anything is saved.
4. A path Bella doesn't have fails the deploy naming `services[i].secrets[j].from.path`, the provider and the path.
5. Bella down fails only that deploy (naming the field and the provider); running copies keep their values, and a
   copy restarted while Bella is down starts with the values it had.
6. The console shows the reference ("from bella-prod · DATABASE_URL"), never the value. No page, API answer, log,
   SQLite row, deployment record, export or Nomad job holds a resolved value (sentinel tests).

### US3 · Refresh secrets (P1)

After a value changes in Bella, the owner presses **Refresh secrets** (console) or calls
`POST /v1/apps/{id}/secrets/refresh`. Caravana re-reads every reference of the running version; when a value
changed it rolls the running version out again (same images, digests pinned), and when nothing changed it says so
and deploys nothing.

**Acceptance**
1. Changed value: a new deployment, recorded as a secrets refresh of the live version; copies get the new value.
2. Unchanged: answer 200 `{"changed": false}` and no deployment.
3. An app with no references is refused (409 `no_secret_references`); a stopped app (409 `app_stopped`), an app
   with no live version (409 `no_live_deployment`).
4. A deploy token of the app may refresh it (CI, and later Bella's change webhook).

### US4 · Rollback and promotion read today's values (P2)

Redeploying an old version resolves its references now, not as they were. Promotion keeps the target's secrets
(references included); a service added by a promotion brings its references, and the target's place must be allowed.
The console says so beside "Redeploy this version" when the version has references; the docs say so.

## Requirements

- **FR-001** `app.Secret.From *SecretRef{Provider, Path}`; value XOR from; stored, recorded and shown as written
  (it is not secret). `WithoutSecretValues` (app and compose) keeps `from`. Existing apps' revisions don't move.
- **FR-002** Providers: name (a lowercase label), kind `bella`, settings (URL, project, environment), allowed places
  (`project` or `project/environment`; none = all), last test. Credentials in Nomad Variables under `caravana/`.
- **FR-003** Resolution happens once per deploy, under the app's lock, just before the sets are built, one read per
  provider. Static checks (provider exists, allowed) run before the app is saved.
- **FR-004** Errors: field errors 400 `invalid_field`; a path not found 422 `secret_not_found`; the provider refusing
  Caravana 422 `secret_provider_refused`; the provider down 502 `secret_provider_unavailable`; each with the field.
- **FR-005** Refresh secrets in the service, the API and the console; deploy tokens allowed.
- **FR-006** Deleting a referenced provider: 409 `secret_provider_in_use` naming the apps.
- **FR-007** Values follow 008's rules everywhere; sentinel tests in `service`, `api`, `ui` and the lab.
- **FR-008** Bella is reached only through its Go SDK `github.com/cosmic-chimps/bella-baxter-go` pinned at
  `v0.1.1-preview.146`, always with the private key (E2EE on), with one bulk read per environment per deploy.

## Out of scope

Bella's secret-change webhook (it will call Refresh); workload-identity auth (task 06; the adapter's auth is a
pluggable piece so Bella's "Connect Bella Baxter" grant, bella-baxter#979, can be a second way); more providers
(task "Secret providers: Vault/OpenBao and more"); values built from several references; an MCP tool for refresh;
emitting references from an Aspire AppHost (the overlay already takes `from`; the publisher side is a follow-up in
`../aspire-hosting-caravana`).

## Done when

A service with a Bella reference deploys with the value in its env; changing it in Bella and pressing Refresh
secrets rolls it out; with Bella down running copies keep their value and a deploy fails naming the field and the
provider; a sentinel set in Bella appears in no page, answer, log, SQLite row or export; a project not allowed is
refused naming the field. In the lab this runs against `bellatest`, a fake Bella speaking the SDK's HMAC and E2EE
envelope; a check against a real Bella environment is left to the maintainer.
