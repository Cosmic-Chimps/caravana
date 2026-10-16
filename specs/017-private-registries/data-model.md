# Data model: Private registries

Decisions are in [research.md](./research.md).

## Store: migration `0009_registries.sql`

```sql
-- Feature 017: registries Caravana has credentials for. The secret isn't here:
-- it lives in the Nomad Variable caravana/registries/<key>.
CREATE TABLE registries (
  host              TEXT PRIMARY KEY,   -- lowercased; "docker.io" for Docker Hub; may hold ":port"
  username          TEXT NOT NULL,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  last_test_at      TEXT,               -- NULL until tested
  last_test_ok      INTEGER,            -- 1 ok, 0 refused or unreachable
  last_test_message TEXT NOT NULL DEFAULT ''
) STRICT;
```

`store.Registry{Host, Username, CreatedAt, UpdatedAt, LastTest *RegistryTest{At, OK, Message}}`
with these calls:
- `SaveRegistry(ctx, r)`: an upsert that keeps `created_at`;
- `ListRegistries(ctx)`: ordered by host;
- `GetRegistry(ctx, host)`;
- `DeleteRegistry(ctx, host)`: `ErrNotFound` when absent;
- `SaveRegistryTest(ctx, host, test)`.

## Nomad Variables

| Path | Items | Written by | Read by |
| --- | --- | --- | --- |
| `caravana/registries/<key>` (`<key>` = host, `.` → `~`, `:` → `_`) | `username`, `password` | Caravana on save; deleted on remove | Caravana (digest lookup, test, copying into the variables below) |
| `nomad/jobs/<app>/<service>/<service>` | `registry` (host), `CARAVANA_REGISTRY_USERNAME`, `CARAVANA_REGISTRY_PASSWORD` | Caravana before every register of a service whose registry has credentials; rewritten when the credentials change or are removed (credential items empty) | the service's main task template only |
| `nomad/jobs/<app>_<service>_backup`, `..._restore` (existing) | gain `CARAVANA_REGISTRY_USERNAME` and `CARAVANA_REGISTRY_PASSWORD` when the service's registry has credentials | `SyncBackups`, `Restore` | the `dump` and `restore` tasks' template (existing; it renders every item as env) |

## Registry package (`internal/registry`, no I/O beyond HTTP)

- `Reference.Registry`: any host. `Parse` accepts `[host[:port]/]path[:tag][@digest]`, where the
  host is recognized by `.` or `:`, or is `localhost`.
- `Endpoint(host) string`:
  - `docker.io` → `https://registry-1.docker.io`;
  - loopback hosts → `http://<host>`;
  - everything else → `https://<host>`.

  `Client.Endpoints` still overrides it in tests.
- `Client.Credentials func(ctx context.Context, host string) (user, pass string, ok bool, err error)`;
  nil means anonymous.
- Errors:
  - `ErrNotFound` (existing): its message now reads "missing or private: add credentials for
    <host> in Settings › Registries" when no credentials were sent;
  - `ErrUnauthorized` (new): credentials were sent and refused;
  - `ErrUnavailable` (existing): a TLS error says HTTPS is required, and a non-registry answer
    says so.
- `Test(ctx, host) error`: nil when the credentials work, else an error wrapping one of the three.

## Job shape (`internal/nomadjob`, pure)

`Translate` and the backup and restore jobs take, per service, whether its registry has
credentials (`Settings` is not the place: it's per deploy). They receive it through a new argument
or field on the input the deploy builds, for example `Registries map[string]bool` keyed by host.
`Translate` stays pure.

For such a service:
- the main task's `config.auth` = `{username: "${CARAVANA_REGISTRY_USERNAME}", password: "${CARAVANA_REGISTRY_PASSWORD}", server_address: <host>}`;
- one more template: the variable `nomad/jobs/<app>/<service>/<service>` → `secrets/registry.env`,
  `env = true`, `change_mode = noop`. It renders exactly the two keys, by name, and only when the
  variable exists (`{{ with nomadVar ... }}`).

`hcl.go` writes `auth` and the template, and the export header lists the variable path with
"needs CARAVANA_REGISTRY_USERNAME and CARAVANA_REGISTRY_PASSWORD". Goldens cover a private
service, a public one, and a Compose app mixing both.

## Deploy

| Function | What |
| --- | --- |
| `RegistryCredentials(ctx, host) (user, pass, ok, err)` | reads `caravana/registries/<key>`; plugged into `registry.Client.Credentials` |
| `SaveRegistrySecret(ctx, host, user, pass)` / `DeleteRegistrySecret(ctx, host)` | the variable |
| `ensureRegistryVariables(ctx, a)` | before register: the per-task variable for each service whose registry has credentials |
| `SyncRegistryCopies(ctx, host)` | after save or remove: rewrites every `nomad/jobs/*` variable whose `registry` item is host |
| `TestRegistry(ctx, host)` | via `registry.Client.Test` |
| `ErrRegistryAuth` | new `*ImageError` kind → API 422 `registry_auth_failed` |

## Service

- `Registries(ctx)`, `SaveRegistry(ctx, host, username string, password *string)`,
  `DeleteRegistry(ctx, host)` and `TestRegistry(ctx, host)` follow the shape of
  `SetBackupDestination` (feature 010).
- `SaveRegistry` validates:
  - the host is a registry host, lowercased; `docker.io` aliases are canonicalized;
  - the user name is not empty;
  - the password is required on create, and rejected when present but empty.
- After a save or remove it calls `SyncRegistryCopies`. A failure is answered as "saved, but
  running apps still have the old credentials: <err>", as feature 010 does.
- No app is deployed or restarted (FR-008).
