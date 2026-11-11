# Implementation Plan: Registry credentials per path

**Spec**: [registry-paths.spec.md](./registry-paths.spec.md) · **Branch**: `alta/arctic-palm-fhmv` · **Date**: 2026-11-11

> **Scale note**: about 30 files across seven packages (registry, nomadjob, deploy, store, service, api, ui) plus the gate. The one subtle piece is `SyncRegistryCopies`: it now works out each copy's entry from its job's image, so watch that a save leaves every other app's variable untouched.

## Summary

A registry entry becomes `host` or `host/path`. An image uses the stored entry whose path is the longest whole-part prefix of its own, else the host-only entry, else no login. The translator, the digest lookup and the per-task pull logins all go through one matching function in `internal/registry`. A path entry's secret is a deeper Nomad Variable under `caravana/registries/`. The API keeps its routes with an optional `?path=`, and migration 0025 makes `(host, path)` the key. Existing entries are host-only entries, so nothing moves on upgrade and the translator's goldens don't change.

## Project Structure

```
internal/registry/entry.go, entry_test.go        new: EntryCandidates, MatchEntry, SplitEntry, EntryHost
internal/registry/client.go                       Credentials(ctx, host, repository); Test(ctx, host, repository)
internal/nomadjob/translate.go                    PrivateRegistry returns the entry; auth server_address stays the host
internal/deploy/registries.go, deploy.go, export.go
                                                  variable path per entry, candidate lookup, sync by entry
internal/store/migrations/0025_registry_paths.sql new
internal/store/registries.go                      Registry.Path, Key(), (host, path) keys
internal/service/registries.go, export.go         entries in, field errors on host or path, Test note
internal/api/registries.go, api/openapi.yaml      ?path=, Registry.path, 0.35.0
internal/ui/registries.go, settingsnav.go, templates/settings.html
                                                  host/path field, test/remove routes, count per entry
cmd/caravana/gate_test.go                         private-registry step with two paths
```

**Structure Decision**: no new package. The matching rule lives in `internal/registry`, beside `Parse`, because every layer already imports it and it is pure.

## Constitution Check

| Principle | Assessment |
|---|---|
| I. Small by default | PASS: no new module, no new service. |
| II. One model | PASS: `app.App` unchanged; `Translate` stays pure and its goldens unchanged. |
| III. Safe deploys | PASS: digests still pinned; nothing deploys on a save. |
| IV. Fail loudly | PASS: a bad path is refused naming `path`. |
| V. API-first | PASS: the console and the API call the same service methods; openapi changes in the same commit; additive, old calls keep their meaning. |
| VI. Nomad-native | PASS: the job's auth block is unchanged; Nomad code stays in `deploy`/`nomadjob`. |
| VII. Secure by default | PASS: secrets stay in Nomad Variables; per-task copies as 017; the gate's sentinel search covers the new passwords. |
| VIII. Plain, tested Go | PASS: table-driven tests per layer. |

Re-checked after design: no change.

## Phases

- Phase 0: [research.md](./research.md) (R1–R8).
- Phase 1: [data-model.md](./data-model.md), [contracts/api.md](./contracts/api.md), [contracts/console.md](./contracts/console.md).
