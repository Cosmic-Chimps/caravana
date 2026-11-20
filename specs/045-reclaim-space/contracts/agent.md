# Contract: the agent's part (extends 031's `contracts/node.md`)

The agent (`caravana node sync`, root, every 30 s) reports what Docker and Nomad's volume directory hold and runs
the clean-up the console hands it. Everything the console sends is checked before use, as in 031 §5; the console
never sends a command, a path or an image to remove, only an ID and the digests to **keep**.

## 1. The report (`POST /v1/node/sync`, body `Report`) gains `space`

```json
"space": {
  "images": [
    {"id": "sha256:7f1b…", "digests": ["sha256:c471…"], "tags": ["traefik/whoami:v1.10.1"], "bytes": 18000000, "inUse": true}
  ],
  "imagesTruncated": false,
  "dockerVolumes": {"count": 154, "bytes": 91050000},
  "buildCacheBytes": 0,
  "volumes": [{"id": "e971d06a-1f46-fc04-583b-25618814daeb", "bytes": 2300000000}],
  "volumesMeasuredAt": "2026-11-20T03:35:20Z",
  "lastClean": {"id": "…32 hex…", "startedAt": "…", "finishedAt": "…", "imagesRemoved": 7, "bytesFreed": 1200000000, "volumesRemoved": 0, "error": ""}
}
```

- `images`: every image Docker lists (research R4), at most 1000 (`imagesTruncated: true` beyond; the ones kept
  are the largest). `digests` are the `sha256:…` parts of `RepoDigests`; `tags` are `RepoTags` without
  `<none>:<none>`; `bytes` is `Size`; `inUse` says a container, running or stopped, uses the image.
- `dockerVolumes`: Docker's local volumes no container uses (`TotalCount − Active` of `docker system df`'s
  "Local Volumes" line; `bytes` its `Reclaimable`).
- `buildCacheBytes`: the `Reclaimable` of the "Build Cache" line.
- `volumes`: the children of `/opt/nomad/data/host_volumes` (research R3), each by its directory name (a Nomad
  volume ID) with `du -sk` in bytes. Measured at most every 15 minutes (and right after a clean); the sizes and
  the time live in `state.json` (`volumeSizes`, `volumesMeasuredAt`) between syncs, so every sync carries them.
- `lastClean`: the outcome of the last clean-up this agent ran, from `state.json`; absent before the first.
- The whole block is absent when `docker` doesn't answer. A console that predates the field ignores it.

The console checks the block (`validateReport`): at most 1000 images, each `id` and tag at most 300 characters of
printable text, at most 16 digests of `^sha256:[0-9a-f]{64}$` and 16 tags per image, every count and byte value 0
to 2^50, at most 4096 volumes with IDs of `^[A-Za-z0-9-]{1,64}$`, the times RFC 3339 or empty, `lastClean.id` 32
hex or empty, `error` at most 500 characters. A bad block is a field error of the sync (400), like any other.

## 2. The answer gains `clean`

```json
"clean": {"id": "a3f9…32 hex…", "keep": ["sha256:c471…", "sha256:2d2b…"], "dockerVolumes": false}
```

Present only while the console has a clean-up `requested` or `running` for this server that its report hasn't
settled. Validation (§5 of 031, same refusal rule: one failure refuses the whole answer): `id` is 32 lowercase hex
characters; `keep` has at most 4096 entries, each `^sha256:[0-9a-f]{64}$`; `dockerVolumes` a bool. Absent
`clean` means nothing to do; an older console sends none.

## 3. What `sync` applies: step 4, the clean-up (after 031's three steps)

When `clean` is present and its `id` differs from `state.lastClean.id`:
1. Write `state.lastClean = {id, startedAt: now}` first: a crash mid-run must never repeat a clean-up. A
   `lastClean` with a `startedAt` and no `finishedAt` is what the console reads as "interrupted on the server".
2. `docker system df --format '{{json .}}'` (sizes before).
3. The inventory (§1). Every image with `inUse: false` whose digests are all outside `keep` is **removable**
   (`removable(images, keep)`, pure). Remove each: `docker image rm <ref>…` with all its tags and digests, or
   `docker image rm <id>` when it has neither. A refusal (Docker's conflict, a race) counts as not removed and
   its message is kept as the run's first error.
4. `docker builder prune -af`.
5. When `dockerVolumes`: `docker volume prune -f` (anonymous volumes no container uses; Docker's own rule).
6. `docker system df` again: `bytesFreed` = the sum over the types of (Size before − Size after), never below 0;
   `volumesRemoved` = Local Volumes `TotalCount` before − after.
7. Write `state.lastClean` whole (`finishedAt`, counts, `error` = the first failure's text, cleaned, at most
   200 characters) and measure the volume directories again. Print one line: `Cleaned up: removed N images,
   freed X MB` or `Clean-up failed: <error>`.

The run has a 10-minute budget (a context). An agent whose `lastClean` has a `startedAt` and no `finishedAt`
reports it so; the console settles that run as `failed` "interrupted on the server".

## 4. `caravana node space`

Prints the `space` block as indented JSON, for a person on the host. Reads only.

## 5. What a compromised console can and can't do (adds to 031 §9)

It can make the agent remove any image no container uses (the apps' running images are safe) and Docker's
anonymous volumes no container uses; Nomad pulls an image again when it needs it. It can't name a path, a
container, a Nomad host volume or a command: the agent takes none of those from the answer.
