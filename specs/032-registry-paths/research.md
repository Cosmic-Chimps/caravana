# Research: Registry credentials per path

## R1. What an entry is, and how an image finds one

**Decision**: an entry is the string `host` or `host/path`. For an image on `host` with repository `a/b/c`, the candidates are `host/a/b/c`, `host/a/b`, `host/a`, `host`, in that order, and the first one stored wins (`registry.EntryCandidates`, `registry.MatchEntry`).
**Rationale**: whole-part prefixes are what the task asks (`acme` never matches `acme-old/...`), and the list is short (two or three for GHCR). The same function serves the translator (which entries are stored) and the digest lookup (which variable to read), so the two can't disagree on the rule.
**Alternatives considered**: a string `HasPrefix` (matches `acme-old`); a per-app choice in Configure (the task rules it out: Compose, CI, MCP and Aspire would each need it).

## R2. Where the secret of a path entry lives

**Decision**: `caravana/registries/<host key>/<path>`, with the host key as today (`.`→`~`, `:`→`_`, IPv6 brackets dropped) and `.` written `~` in the path.
**Rationale**: Nomad Variable paths allow letters, digits, `-`, `_`, `~` and `/`. A host key never holds `/`, and an image path never holds `~`, so no new key equals an old one and no two entries share a key. Existing host-only variables keep their path, so an upgrade moves nothing. The policy's `caravana/*` already covers the deeper paths.
**Alternatives considered**: one variable per host holding every path's login (one write rewrites the other accounts' secrets, and Test/remove get harder); hashing the entry (unreadable in Nomad's UI).

## R3. How the digest lookup asks for credentials

**Decision**: the registry client's `Credentials` hook gets the repository as well as the host. The deployer's hook reads the candidates' variables in order and takes the first with a login.
**Rationale**: the hook is only called when a registry asks for a login, so a public image costs nothing, and a GHCR image costs at most one read per candidate. Reading Nomad (not SQLite) keeps the deployer free of the store, as in 017.
**Alternatives considered**: passing the stored entry set into every `Resolve` call (a signature change on every deploy path for no gain).

## R4. What `Test` can say about a path

**Decision**: `Test` still asks `/v2/` for a token with no scope, with the entry's own login. For an entry with a path, a working login is recorded with the note that the first deploy of an image under that path shows whether it reads it.
**Rationale**: `/v2/` checks the login only. A pull token "for a prefix" doesn't exist in the registry token protocol: a scope names one repository, and asking for one that doesn't exist answers differently per registry.
**Alternatives considered**: asking for `repository:<path>:pull` (GHCR and Docker Hub hand out a token for a repository that doesn't exist, so it proves nothing).

## R5. Which copies a save or removal rewrites

**Decision**: `SyncRegistryCopies(entry, entries)` looks at every stored pull login on the entry's host, works out from the job's image which entry serves it now, and rewrites it only when that changes what it holds. The `registry` item of a service's pull login now holds the entry. A pull login whose job can't be read keeps 017's rule: it changes only when it named the changed entry.
**Rationale**: adding `ghcr.io/acme` must move the `acme/` apps off `ghcr.io`, and removing it must move them back. A rule that matches the stored `registry` item alone would do neither. Comparing before writing keeps every other app's variable untouched (no new modify index), which is what "only the copies that use it" means.
**Alternatives considered**: storing the image in the variable (an extra item, and old variables don't have it, so the job read is needed anyway).

## R6. The API shape

**Decision**: keep `/v1/registries/{host}` and `/v1/registries/{host}/test`, add an optional `path` query parameter, and add `path` to the `Registry` schema. API 0.35.0.
**Rationale**: a path holds `/`, which a path segment can't carry without `%2F`, and proxies (Traefik among them) may refuse an encoded slash. A query parameter keeps every existing call's meaning (constitution V: no breaking change).
**Alternatives considered**: `{entry...}` at the end of the route (impossible for `/test`); `%2F` in the segment.

## R7. The store

**Decision**: migration `0025_registry_paths.sql` rebuilds `registries` with `path TEXT NOT NULL DEFAULT ''` and `PRIMARY KEY (host, path)`, copying the rows with an empty path.
**Rationale**: SQLite can't change a primary key in place. Keeping host and path apart keeps "every entry of a host" a plain query.
**Alternatives considered**: storing `host/path` in the `host` column (the name would lie).

## R8. Proving it in the lab

**Decision**: the gate's `private-registry` step gives its `registry:2` two htpasswd users and copies busybox to `teama/`, `teamb/` and `other/`. A wrong password on the host entry and right ones on the two paths show which entry each deploy used.
**Rationale**: htpasswd lets any valid user read every repository, so per-path separation can only show through a wrong password on the entries that must not be used.
