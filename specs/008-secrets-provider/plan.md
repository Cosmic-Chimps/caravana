# Implementation Plan: Secrets

**Branch**: `008-secrets` | **Date**: 2026-10-07 | **Spec**: [secrets-provider.spec.md](./secrets-provider.spec.md)

## Summary

Each service gets a list of secrets: a name, plus a value or nothing (nothing means keep the
current value). The values never reach SQLite, the Nomad job or any response. On every deploy
the service layer works out each service's full secret set and hands it to a
`secrets.Provider`. In production that is a Nomad Variables store in `internal/deploy`, which
writes one variable per service at `nomad/jobs/<app>/<service>`. The translator gives the task
an env template that reads that variable with `change_mode = "noop"`, so values reach copies
only through a rollout. Before overwriting, the store keeps the replaced sets in one more
variable, owned by the deployment. Once that deployment ends, Caravana either drops the copy
(healthy) or puts it back (failed or reverted), so the store always matches the version that
is running, also across restarts. No new module and no new runtime dependency.

## Project Structure

```text
internal/app/
├── app.go              # Service.Secrets, Secret{Name, Value *string}, App.WithoutSecretValues
└── validate.go         # secret name, value and uniqueness rules
internal/secrets/
├── secrets.go          # Provider, Sets, ErrUnavailable, Build (apply keep), size limit
├── secrets_test.go
└── secretstest/
    └── store.go        # in-memory Provider for tests, with SetUnavailable and Snapshot
internal/nomadjob/
├── translate.go        # env template per task with secrets
└── testdata/           # golden job for an app with secrets
internal/deploy/
├── secrets.go          # Variables: the Nomad Variables Provider (only Nomad code)
├── secrets_test.go
└── deploytest/nomad.go # fake /v1/var and /v1/vars endpoints
internal/service/
├── service.go          # New takes a secrets.Provider
├── deploy.go           # Build and Replace before Register; Restore on early failure
├── secrets.go          # SetSecret, RemoveSecret, settleSecrets (Commit/Restore with retry)
├── apps.go             # DeleteApp deletes secrets
└── *_test.go
internal/api/api.go     # secrets.ErrUnavailable → 502 secret_store_unavailable
internal/ui/
├── configure.go, app.go           # secrets panel data, set and remove handlers
├── ui.go                          # POST /apps/{id}/secrets, POST /apps/{id}/secrets/remove
├── templates/app.html             # the Secrets panel on Configure
└── static/app.css                 # regenerated
api/openapi.yaml        # Service.secrets, Secret, the new error code, version 0.6.0
cmd/caravana/
├── main.go                        # wires deploy.NewVariables into service.New
├── gate_test.go                   # gate step secrets-render
└── console_lab_test.go            # console step: set and remove a secret
deploy/install/caravana-policy.hcl      # variables rules for nomad/jobs/*
CLAUDE.md, docs/plan.md            # where we are
```

**Structure Decision**: the seam (interface, types, test store) lives in `internal/secrets`. The
Nomad implementation lives in `internal/deploy`, which is one of the two packages allowed to
talk to Nomad (constitution VI). It satisfies `secrets.Provider` without naming it.

## Constitution Check

| Principle | Assessment |
| --- | --- |
| I. Small by Default | PASS. Nomad Variables is part of Nomad. No new module, no new service, no Vault. |
| II. One Model, Many Inputs | PASS. Secrets are a field of `app.Service`, and `Translate` stays pure: it only emits the template that names the variable path. |
| III. Safe, Reversible Deploys | PASS. Each deploy declares the full set per service. A name with no value is an explicit keep, never kept by omission. Every change goes through a health-checked rollout, and a failed one puts the previous set back. |
| IV. Fail Loudly | PASS. Bad names, bad values, a keep with no value, a missing stored value and an oversized set all fail, naming the field. |
| V. API-First | PASS. No console-only feature: set and remove are deploys the API can send too. `api/openapi.yaml` changes in the same commit. |
| VI. Nomad-Native | PASS. Uses the official client's `Variables()`, keeps Nomad code in `deploy` and `nomadjob`, and the job stays exportable (it holds a path, not values). |
| VII. Secure by Default | PASS. Values live in Nomad Variables under `nomad/jobs/<app-id>`, never in SQLite, the job, logs or responses. A task can only read its own service's variable. The scoped token gains only the variables rules. |
| VIII. Plain, Tested Go | PASS. Table tests, a golden job, a fake Nomad over HTTP, an in-memory store for the service tests, and lab and gate steps. |

No violations, so no Complexity Tracking table.
