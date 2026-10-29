# Research: Secrets

## R1. Where the values live

**Decision**: one Nomad Variable per service with secrets, at `nomad/jobs/<app>/<service>`. Its
items are exactly that service's secrets. A service with no secrets has no variable.

**Rationale**: Nomad gives a task implicit read access to exactly four paths: `nomad/jobs`,
`nomad/jobs/<job>`, `nomad/jobs/<job>/<group>` and `nomad/jobs/<job>/<group>/<task>`
(`acl/acl.go`, exact matches, not prefixes). The group path is per service, so each service
reads its own secrets and nothing else, with no extra ACL policy. Nomad refuses a variable with
no items, which is why an empty set means no variable.

**Alternatives considered**: one variable at `nomad/jobs/<app>` with keys like `web.API_KEY`.
Every task of the app could read every service's secrets. A Caravana-only path such as
`caravana/...` would need a job-scoped ACL policy per app, and Caravana's token can't manage
ACLs (and shouldn't).

## R2. How a value reaches the container

**Decision**: the task gets one template:

```text
{{ with nomadVar "nomad/jobs/<app>/<service>" }}{{ range .Tuples }}{{ .K }}={{ printf "%s" .V | toJSON }}
{{ end }}{{ end }}
```

with `DestPath: "secrets/caravana.env"`, `Envvars: true`, `ChangeMode: "noop"`.

**Rationale**: Nomad reads env templates with `hashicorp/go-envparse`. Its double-quoted values
accept exactly JSON's escapes (`\"`, `\\`, `\/`, `\b`, `\f`, `\n`, `\r`, `\t`, `\uXXXX` with
surrogate pairs), and refuse raw control characters. A JSON-encoded string therefore round-trips
byte for byte, for any valid UTF-8 without NUL. `printf "%s"` turns the item into a plain string
before `toJSON`. `noop` means a changed variable never restarts or re-renders a running copy;
new copies of the next rollout read the new values. The `secrets/` directory isn't readable
through Nomad's alloc filesystem API.

**Alternatives considered**: `change_mode = "restart"`, which restarts copies in place with no
health gate and no revert. Values in the job's `Env`, which anyone with `read-job` sees.

**Verify in the lab**: the gate step deploys a value with a newline, quotes, `$`, `{{` and a
non-ASCII character and compares what the container prints.

## R3. Secret name and value rules

**Decision**: names match `^[A-Za-z_][A-Za-z0-9_]*$` and are unique across the service's
secrets and env. Values must be valid UTF-8 without NUL. An app's secrets (names plus values,
over all services) total at most 60 KiB.

**Rationale**: `go-envparse` keys allow `[A-Za-z_][A-Za-z0-9_./]*`; `.` and `/` aren't portable
env names, so they are left out. JSON can't carry invalid UTF-8 unchanged, and an env var can't
hold a NUL. Nomad caps a variable at 64 KiB of keys plus values. The kept copy (R4) holds all
of an app's services in one variable, so the cap applies to the app's total, with headroom for
the copy's own item.

**Alternatives considered**: the plain env rule (anything without `=` or NUL), which breaks the
env file. A per-service cap, which the kept copy could exceed.

## R4. Putting the previous secrets back

**Decision**: before writing new sets, the store saves the sets it replaces into one more
variable, `nomad/jobs/<app>/caravana/previous`. Its item `deployment` is the deployment that
owns it, and each item `<service>/<NAME>` holds the previous value. Rules:

- `Replace(app, D, sets)`: if a kept copy exists, keep its sets and make D its owner. Otherwise,
  if `sets` differ from the live ones, save the live sets owned by D. Then write `sets` live
  (put changed variables, delete those of services that no longer have secrets).
- `Commit(app, D)`: if D owns the copy, delete it. Called when D ends healthy.
- `Restore(app, D)`: if D owns the copy, write its sets live and delete it. Called when D ends
  failed or reverted, and when D fails between Replace and Register.
- A superseded deployment does nothing: the newer deploy now owns the copy.

**Rationale**: the copy always holds the sets of the last version that got healthy. A deploy
that started while an older one was still rolling out takes over the copy instead of saving
the older one's (never healthy) sets. Everything is in Nomad Variables and keyed by deployment
ID, so a restart in the middle changes nothing: `resume` settles the deployment and calls
`Restore` or `Commit` like a live run. No task can read the copy: its path would be the task
path of a group `caravana` with a task `previous`, and Caravana names each task after its
group. The values never touch SQLite.

**Alternatives considered**: no restore. Old copies keep their env because of `noop`, but a
later restart of one would render the failed deploy's values. A copy per deployment, which grows
without bound and needs cleanup. Storing old values in SQLite, which constitution VII forbids.

## R5. Keep, and where the names are stored

**Decision**: `app.Secret` is `{Name, Value *string}`. A nil `Value` means keep. Caravana saves
the definition with every `Value` cleared (`App.WithoutSecretValues`), so the stored app, `GET`
responses and the console all hold names only, and a stored app redeploys as "keep everything".
Before saving, `Deploy` checks that every keep has a stored value, so a bad keep saves nothing.
Under the app's lock, `deploy` builds the full sets again (`secrets.Build`), because values may
have changed in between.

**Rationale**: reading an app and deploying it unchanged keeps its secrets, which is what a
redeploy and the webhook need. An empty value is `"value": ""`, distinct from no value.

**Alternatives considered**: a `keep: true` flag, which is one more field that can contradict
`value`. A separate secrets endpoint, rejected in the clarify session.

## R6. Ordering and locking

**Decision**: in `deploy`, the order is: resolve images, check this deploy is still the latest,
`Build` and `Replace`, `Register`. If `Register` fails, `Restore`. `Commit` and `Restore` from a
finished rollout run in the watcher, take the app's lock per attempt, and retry with the
existing backoff until the process stops.

**Rationale**: the latest check comes first, so a superseded deploy writes nothing (FR-012).
Taking the app's lock keeps a watcher's Restore from interleaving with a newer deploy's Replace.
Taking it per attempt keeps an unreachable store from blocking deploys behind a retry loop.

**Simplified**: when the process stops while a Restore keeps failing, the bad values stay live
until the app's next deploy. That deploy takes over the kept copy, so it still ends right.

## R7. The scoped Nomad policy

**Decision**: `deploy/install/caravana-policy.hcl` gains

```hcl
variables {
  path "nomad/jobs/*" {
    capabilities = ["read", "write", "destroy", "list"]
  }
}
```

inside the `default` namespace block. A new gate step, `secrets-render`, deploys an app with a
secret through the scoped token and reads it back from the container.

**Rationale**: these are the four operations the store uses. The lab runs with a management
token, so only the gate on a real host catches a missing rule, as `console-reads` does for
`read-logs`.

## R8. Proving the value in the container

**Decision**: the gate and lab steps deploy `busybox` running
`sh -c 'mkdir -p /www && printenv CARAVANA_PROOF > /www/index.html && exec httpd -f -p 8080 -h /www'`,
with a TCP health check and a domain. The test fetches the page and compares it with the value.

**Rationale**: the image is small and anonymous on Docker Hub, and `httpd` ships in it. The
command has no `${`, so Nomad's interpolation leaves it alone. The app is deleted at the end of
the step, and its value is random per run.
