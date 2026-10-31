# API contract: 027 Operator view (openapi 0.28.0 → 0.29.0)

Every operation needs a full API token or a signed-in session; a deploy token is refused with 403
`token_scope` by the default deny (`TestScopedTokenWalksEveryRoute` lists the new routes). Times are RFC
3339 UTC. No answer holds a secret value or command output.

## `getHost` (`GET /v1/host`, existing) gains `node`

```yaml
node:                       # null before the first reading
  name: v2202610435163537080
  state: ready              # ready | draining | ineligible | down | initializing
  pool: default
  os: { name: ubuntu, version: "24.04" }
  kernel: 6.8.0-147-generic
  arch: x86_64
  versions: { nomad: 2.0.7, docker: 27.5.1, traefik: 3.7.13, caravana: 0.15.0 }
  cpu: { cores: 4, totalMHz: 9780, model: "AMD EPYC-Rome Processor", coreMHz: 2445 }   # model, coreMHz optional
  memoryMB: 7953
  address: { public: caravana.lacalabaza.xyz, private: 10.99.0.1 }
  bootTime: 2026-10-21T03:12:00Z
  lastHeartbeat: 2026-10-30T14:02:11Z
  rebootRequired: true      # from Updates' state, for page 2's pill
```

## `listActivity` (`GET /v1/activity`)

Query: `limit` (1-50, default 8), `before` (RFC 3339, for paging), `server` (`true`: only entries about
the server and its copies). Answer `200`:

```yaml
entries:
  - id: dep_8f2k1z.ended     # stable per entry
    time: 2026-10-30T13:58:04Z
    kind: deploy             # deploy | restart | stop | start | command | backup | restore | schedule | needs you | server
    status: healthy          # healthy | warning | failed | idle
    message: "lacalabaza v35 is live, promoted from staging."
    app: lacalabaza          # optional
    place: { project: default, environment: production }   # optional
```

## `getUpdates` (`GET /v1/updates`)

```yaml
caravana:
  current: 0.15.0
  latest: 0.16.0              # absent when not newer or never checked
  releasedAt: 2026-11-02T10:00:00Z
  url: https://github.com/Cosmic-Chimps/caravana/releases/tag/v0.16.0
  notes: ["API 0.28.0", "..."]  # up to 6, one per bullet: its bold lead, else its first sentence (backticks kept)
  pins: { nomad: { from: 2.0.7, to: 2.1.0 }, traefik: { from: 3.7.13, to: 3.8.0 } }   # optional
  install: "curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/download/v0.16.0/install.sh | sudo bash"
  checkEnabled: true
  checkedAt: 2026-10-30T03:10:00Z     # the last attempt
  checkedOkAt: 2026-10-30T03:10:00Z   # the last attempt that worked (null when none has)
  checkError: ""              # e.g. "GitHub didn't answer"
os:
  readable: true
  distro: { id: ubuntu, version: "24.04" }
  updates: { total: 14, security: 9, checkedAt: 2026-10-30T06:05:00Z, stale: false,
             packages: [libc6, linux-image-6.8.0-149-generic], packagesCheckedAt: ..., packagesError: "" }
  reboot: { required: true, since: 2026-10-28T06:12:00Z, packages: [linux-image-6.8.0-149-generic],
            runningKernel: 6.8.0-147-generic, installedKernel: 6.8.0-149-generic,
            installedAt: 2026-10-28T06:12:00Z, installedBy: unattended }   # who ran the apt run that installed what the marker names
  unattended: { enabled: true, autoReboot: false, autoRebootTime: "" }   # null fields when unreadable
history:
  - { at: 2026-10-28T06:12:00Z, kind: os_packages, text: "Ubuntu updates · 14 packages", detail: "by Ubuntu's automatic updates · needed a reboot" }
  - { at: 2026-10-22T10:00:00Z, kind: version, text: "caravana 0.12.0 → 0.13.0", detail: "" }
checking: { release: false, packages: true }     # a check in progress
```

## `putUpdateSettings` (`PUT /v1/updates/settings`)

Body `{ "checkEnabled": false }` → `200` with `getUpdates`' `caravana` block. `422 invalid_field` naming
`checkEnabled` when it isn't a boolean.

## `checkUpdates` (`POST /v1/updates/check`)

Body `{ "what": "release" | "packages" }` → `202` with `{ "checking": { ... } }`; the result arrives on
`getUpdates` and the `updates` live notice.

| Code | Status | When |
|---|---|---|
| `check_too_soon` | 429 | the same check ran less than 10 minutes ago; the message says when it may run again |
| `check_disabled` | 409 | `what: release` while the owner turned the release check off |
| `check_unavailable` | 409 | `what: packages` on a host without `pro` or not Ubuntu |
| `invalid_field` | 422 | `what` is neither |

## Attention kinds (in `listAttention` and the webhook's `problem.*` events)

- `os_reboot_required` (failed), key `os_reboot_required|||`: "This server needs a reboot: kernel
  6.8.0-149 is installed, but 6.8.0-147 is still running." or "…: `libc6` was updated and needs a reboot."
- `os_security_updates` (warning), key `os_security_updates|||`: "14 updates are waiting, 9 of them
  security updates."

## Notification event `caravana.release`

Webhook body: `{ "type": "caravana.release", "release": { "current": "0.15.0", "latest": "0.16.0",
"url": "..." }, ... }` (the existing envelope). Slack, Discord and ntfy: "caravana 0.16.0 is out. You run
0.15.0." with the release link. Once per release.
