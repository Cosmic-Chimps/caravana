# Data Model: Secrets

## app.Secret (new)

One secret of one service.

| Field | JSON | Rules |
| --- | --- | --- |
| `Name` | `name` | `^[A-Za-z_][A-Za-z0-9_]*$`; unique across the service's `secrets` and `env` |
| `Value` | `value`, omitted when nil | `nil` means keep the current value. Otherwise valid UTF-8, no NUL. Never stored, never returned |

Field paths in errors: `services[i].secrets[j].name`, `services[i].secrets[j].value`,
`services[i].secrets` (the size limit).

## app.Service (changed)

Gains `Secrets []Secret` (`secrets`, omitted when empty), after `Env`.

## app.App (changed)

Gains `WithoutSecretValues() App`, a copy with every `Secret.Value` set to nil. `Deploy` saves
this copy. So the stored document, `GET /v1/apps/{id}` and the console only ever hold names.

## secrets.Sets (new)

`map[string]map[string]string`: service name → secret name → value. Services without secrets
are absent. It is the only shape a `Provider` takes and returns.

`secrets.Build(a app.App, current Sets) (Sets, error)` applies the definition to the current
values: a secret with a value takes it, a keep takes the current value, and anything not named
is gone. A keep with no current value is a `*app.FieldError` on `services[i].secrets[j].value`.
A total over 60 KiB is a `*app.FieldError` on `services[i].secrets` (the first service that
crosses it).

## Nomad Variables layout (inside `deploy.Variables`)

| Path | Items | Who reads it |
| --- | --- | --- |
| `nomad/jobs/<app>/<service>` | `NAME` → value, one per secret | that service's tasks (implicit), Caravana |
| `nomad/jobs/<app>/caravana/previous` | `deployment` → owning deployment ID; `<service>/<NAME>` → previous value | Caravana only |

## Kept copy lifecycle

```text
          Replace(D) with changes        Commit(D)            (no copy)
(no copy) ───────────────────────► owned by D ──────────► 
                                   │  ▲
                Replace(D2)        │  │  Restore(D): previous sets go live
                takes it over      ▼  │
                             owned by D2 ──── Commit/Restore(D2) ──► (no copy)
```

`Commit` and `Restore` by a deployment that doesn't own the copy change nothing.

## Deployment outcome → secrets call

| Outcome | Call |
| --- | --- |
| healthy | `Commit` |
| failed, reverted | `Restore` |
| failed before the job was accepted (after `Replace`) | `Restore` |
| superseded | none |
| failed before `Replace` (bad image, bad keep) | none |
