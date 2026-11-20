# Feature Specification: Reclaim space (unused images and orphan volumes)

**Feature Branch**: `alta/sturdy-erebus-k2fw` (worktree; CodeAlta task `p535`)

**Created**: 2026-11-20

**Status**: Implemented, unreleased

**Input**: CodeAlta task `p535` ("12 · Clean up unused images and orphan volumes", raised by the maintainer on
2026-11-19): show what could go on each server, remove unused images automatically while keeping each app's last
deployments, list orphan volumes and delete one only after its name is typed.

## Why

A host fills its disk with things no app uses any more, and the console can neither show nor remove them. 025's
`disk_nearly_full` says the disk is filling; nothing says what could go.

- **Images.** Nomad's docker driver removes an image it pulled once no task uses it (`gc { image = true }`, the
  default our `nomad.hcl` leaves in place). Its count of which image is in use lives in memory only: `RecoverTask`
  (Nomad's source, `drivers/docker/driver.go`) adopts a running container after a restart **without** counting its
  image, and `RemoveImage` logs "non-referenced counted image" and does nothing when the task later ends. The
  pending delete timers die with the process too. Caravana restarts Nomad on purpose (a host is converted, a server
  is promoted, the issuer changes, the installer runs again), so old app images stay for good. Lab, observed
  (2026-11-20): the lab VM has 7 images of which 3 are in use, 326 MB reclaimable by Docker's own count.
- **Docker's own volumes.** Nomad removes a container with `Force: true` and never `RemoveVolumes`, so every
  container made from an image with a `VOLUME` line leaves an anonymous Docker volume behind, which nothing ever
  uses again. Lab, observed: 154 such volumes, 0 in use, 91 MB.
- **Caravana's volumes.** An app's volumes are deleted only with the app (`deploy.DeleteVolumes`). A volume
  dropped from a definition, renamed in a Compose file, or left by a delete that failed stays as `<app>_<volume>`
  with its data. 016 warns at the deploy that drops it and the app's Volumes panel lists it as "not mounted", but a
  volume of a deleted app is listed nowhere, nothing says how big any of them is, and nothing deletes one.

## User Scenarios & Testing

### User Story 1 — See what could go, per server (Priority: P1)

On a server's page in the Operator view the owner sees a Space section: how much is reclaimable on that server,
made of unused images (count and size), Docker's leftover volumes (count and size), the build cache (size) and
Caravana's orphan volumes (each with its name, the app it came from, its size and when it was last deployed with).
The `disk_nearly_full` item links to that section.

**Why this priority**: knowing is what the task was raised for; everything else acts on this list.

**Independent test**: a server whose agent reports its images, Docker volumes and host volume sizes shows the
numbers; the lab's unmanaged node (no agent) shows the orphan volumes it can read from Nomad and says images can't
be counted without the agent.

**Acceptance scenarios**
1. Given a managed server whose agent has reported, when its page is read, then the Space section shows the
   reclaimable total, unused images (count, size), Docker's leftover volumes (count, size), the build cache and the
   orphan volumes, and `GET /v1/servers/{name}/space` answers the same numbers.
2. Given an app whose definition dropped a volume, when the server that holds the volume is read, then the volume
   is listed as orphan with the app's name, its size (when the agent measured it) and the date of the last
   deployment that mounted it.
3. Given a volume of an app that was deleted (its delete failed after the job was purged), when the server is
   read, then the volume is listed with "its app was deleted".
4. Given a server without an agent (the lab's node, or a Nomad node Caravana doesn't manage), when its page is
   read, then the orphan volumes are still listed (no size) and the images line says the agent is needed.
5. Given a `disk_nearly_full` item, when it is drawn on the home or the Operator pages, then it links to the Space
   section of the server it names.

### User Story 2 — Unused images go away, the last deployments stay (Priority: P1)

Every day, and when the owner chooses "Clean up now", each managed server removes the images no container uses
and that Caravana doesn't want kept. Caravana keeps the digests of each app's last 3 healthy deployments (a
setting, 1 to 10), so "Redeploy this version" starts without a pull, and its own helper images. The server page
shows when the last clean-up ran and what it freed, and the activity feed records each one.

**Why this priority**: the disk is what fills; images are the bulk and the safe part to remove.

**Independent test**: on a host from `install.sh` (`deploy/dev/install-test.sh`), deploy four versions of an
app, restart Nomad, request a clean-up: the first version's image is gone, the last three and the running images
stay, and redeploying the newest of the kept versions doesn't pull.

**Acceptance scenarios**
1. Given an app with four healthy deployments of four images and Nomad restarted since, when a clean-up runs
   on its server, then the first image is removed, the three newest stay, every image a container uses stays, and
   Caravana's helper images (names, volumes, sqlite, upload) stay.
2. Given a clean-up that ran, when the server page and `GET /v1/servers/{name}/cleanups/{id}` are read, then
   they say when it ran, how many images it removed and how many bytes it freed, and the activity feed has one
   entry for it.
3. Given automatic clean-up on (the default), when 04:00 UTC passes, then every managed server that reports
   images gets one clean-up, recorded like one started by the owner (trigger `schedule`).
4. Given a deployment pending or running, when a clean-up runs, then that deployment's images are kept whatever
   its app's history.
5. Given a stopped app, when a clean-up runs, then the images of its last healthy deployment are kept, so Start
   doesn't pull.
6. Given "Clean up now" on a server with no agent, then the answer is 409 `no_agent` and the page says so.
7. Given a clean-up requested while one is going on that server, then the second is refused 409 `cleanup_running`.
8. Given the setting changed to keep 5 deployments, then the next clean-up keeps the images of the last 5.

### User Story 3 — Delete an orphan volume by typing its name (Priority: P2)

From the Space section the owner deletes an orphan volume: a page asks for the volume's name typed, offers "Back up
first" when the backup destination is set, and deletes the volume (data included) only when the typed name matches.
Nothing else on the host is touched, and the activity feed records the deletion.

**Why this priority**: data is deleted here; the name typed and the record are what make it safe enough.

**Independent test**: in the lab, deploy an app with two volumes, redeploy with one, delete the dropped volume
through the page and the API: the typed name is checked, the volume is gone from Nomad, the other volume's data is
intact (the app still answers with its file), the feed has the entry.

**Acceptance scenarios**
1. Given an orphan volume, when its name is typed as it is and confirmed, then Nomad's volume and its directory
   are gone, the server's reclaimable space no longer counts it, and the feed says "Deleted the volume X of app Y
   (N MB)".
2. Given a typed name that differs (a letter, a space), then nothing is deleted and the page says the name
   doesn't match (API: 400 `invalid_field` on `confirm`).
3. Given a volume that a stored app still declares (mounted or not, the app stopped or not), when its deletion is
   asked for through the API, then it is refused 409 `volume_not_orphan`: it is deleted with its app, or by
   dropping it from the definition first.
4. Given an orphan volume that a running copy still mounts (the deploy that dropped it was reverted, so the live
   version still has it), then the page says so, offers no delete, and the API refuses 409 `volume_in_use`.
5. Given a volume Nomad no longer has, then the API answers 404 `volume_not_found`.
6. Given a delete that Nomad refuses for another reason, then the error is reported in Caravana's words and the
   volume stays listed.

### User Story 4 — Back up first (Priority: P3)

When the backup destination is set, the delete page offers "Back up first": the volume is archived to the
destination (one `.tar.gz` under the app's folder, like a volume backup) and deleted only once the archive is
there; the feed names the object.

**Why this priority**: the owner can archive by other means; this makes the safe path the easy one.

**Independent test**: in the lab, with the gate's `rclone serve s3` bucket as the destination, delete an orphan
volume with "Back up first": the object appears in the bucket, then the volume is gone.

**Acceptance scenarios**
1. Given "Back up first" ticked, when confirmed, then the answer is 202 with a deletion record `archiving`, the
   archive object appears at the destination, and then the volume is deleted and the record is `done` with the
   object's name.
2. Given the archive fails (the destination refuses, the job can't start), then the volume is **not** deleted,
   the record is `failed` with the reason, and the page says so.
3. Given no backup destination, then the page doesn't offer the box, and the API refuses `backUpFirst: true` with
   409 `no_backup_destination`.

### User Story 5 — Docker's leftover volumes (Priority: P3)

Docker's anonymous volumes that no container uses are counted and sized in the Space section. "Clean up now"
has a box "Also remove Docker's leftover volumes"; the daily clean-up never removes them.

**Acceptance scenarios**
1. Given leftover Docker volumes, when a clean-up is requested with the box ticked, then they are removed and the
   record says how many and how much.
2. Given the daily clean-up, then Docker's volumes are left alone.

### Edge Cases

- A clean-up handed to an agent that never reports back (the server went down): the record is `lost` after 30
  minutes, and a new clean-up may be requested.
- A clean-up requested while the server's agent doesn't sync (10 minutes without a sync): `failed`, "the server's
  agent didn't pick it up".
- The agent removes an image Nomad is about to use for a container it is creating (the image was pulled, the
  container not yet created): the window is milliseconds and only touches images outside the keep list; Nomad
  pulls again on the restart. Documented, not guarded.
- An image known by two digests (pulled by tag and by digest): it is kept when any of its digests is in the keep
  list.
- Images with no digest (built or loaded by hand): removed when no container uses them, like any other.
- The keep list is capped at 4096 digests; a cluster with more refuses the clean-up with a plain error (it would
  need thousands of apps).
- An orphan volume listed while its app is being deleted (between the store delete and `DeleteVolumes`): a
  manual delete racing with it tolerates Nomad's 404.
- A volume whose name doesn't parse as `<app>_<volume>` (made by hand with the mkdir plugin): listed with no app,
  deletable like any other.
- The agent's measurement of the volume directories runs every 15 minutes, not every sync: a volume deleted a
  minute ago may still show its old size until the next measurement.

## Requirements

### Functional Requirements

- **FR-001**: Every managed server's agent reports, with each sync, the Docker images on the server (ID, digests,
  tags, size, whether a container uses it), Docker's volumes no container uses (count, bytes), the build cache
  (bytes) and the size of each host volume directory (Nomad volume ID, bytes), the sizes measured at most every
  15 minutes.
- **FR-002**: Caravana derives the keep list: the digests of each app's newest N healthy deployments (N the
  setting, default 3, 1–10), of every deployment pending or running, of every stored app's definition (a stopped
  app restarts from it), and its own helper images; a promotion's target deployment is one of the target app's
  own, so no extra rule.
- **FR-003**: A clean-up is a record (server, trigger owner or schedule, requested, started, ended, status,
  images removed, bytes freed, Docker volumes removed, error) handed to the server's agent in its sync answer with
  the keep list; the agent removes every image no container uses whose digests are all outside the list, and the
  build cache, then reports the outcome in its next sync; Caravana records it and the feed lists it.
- **FR-004**: The agent validates the request (an ID of 32 hex characters, at most 4096 digests each
  `sha256:` and 64 hex characters) and runs a request once; it never takes a command, a path or an image name to
  remove from the console.
- **FR-005**: Automatic clean-up (on by default) requests one clean-up per managed server reporting images, once a
  day at 04:00 UTC; "Clean up now" requests one for one server; a second clean-up on a server with one open is
  refused.
- **FR-006**: Docker's leftover volumes are removed only when the clean-up was requested with `dockerVolumes`
  true; the daily clean-up never sets it.
- **FR-007**: An orphan volume is a Nomad dynamic host volume that no stored app declares (the app stopped or not);
  it is listed per server with its name, the app it came from (deleted or not), its size when measured, when its
  app was last deployed with it, and whether a copy still mounts it.
- **FR-008**: Deleting an orphan volume needs its name typed exactly; the volume and its data are deleted through
  Nomad; a volume a stored app declares, or one a copy mounts, is refused; the deletion is recorded (volume, app,
  server, trigger, outcome, bytes, archive object) and listed in the feed.
- **FR-009**: "Back up first" archives the volume to the backup destination before deleting it and never deletes
  when the archive failed.
- **FR-010**: The deploy warning of 016 for a dropped volume names where to delete it (the server, Operator ›
  Servers).
- **FR-011**: The API has the reads and actions of all of the above; deploy tokens are denied by the default deny;
  MCP gains one read tool.
- **FR-012**: No clean-up or deletion ever touches: an image a container uses, a volume a stored app declares, a
  volume a copy mounts, Traefik, Nomad's data, or anything on the host outside Docker's images and volumes and
  Nomad's host volume directories.

### Key Entities

- **Space of a server**: reclaimable bytes; images (count, bytes, unused count, unused bytes); Docker volumes
  (count, bytes); build cache bytes; orphan volumes; measured at; the last clean-up; the reason when nothing can be
  counted ("no agent").
- **Clean-up**: ID, server, trigger, dockerVolumes, requested/started/ended, status (requested, running, done,
  failed, lost), images removed, bytes freed, Docker volumes removed, error.
- **Volume deletion**: ID, volume name, app, server, back up first, requested/ended, status (archiving, deleting,
  done, failed), archive object, bytes freed, error.
- **Clean-up settings**: automatic (bool), keep deployments (1–10).

## Success Criteria

- **SC-001**: On a host from the installer whose Nomad was restarted, a clean-up removes the image of a fourth-newest
  deployment and keeps the three newest; redeploying the newest kept version completes without a pull.
- **SC-002**: A volume dropped from an app's definition is listed as orphan with its size within 15 minutes of the
  agent's next measurement, and is deleted only after its name is typed; the app's other volume keeps its data.
- **SC-003**: The server page shows the space a clean-up freed, and the feed has one entry per clean-up and per
  deletion.
- **SC-004**: `TestConsoleLab` and the gate have a step each for the Nomad-side flows; `install-test.sh` checks the
  agent-side flow on a real host.

## Assumptions

- Nomad's `mkdir` plugin keeps every dynamic host volume at `<data_dir>/host_volumes/<volume ID>` (lab, observed
  2026-11-20): the agent measures that fixed directory's children and reports them by ID; no path comes from the
  console.
- The agent runs as root on every managed server with the `docker` CLI (it already runs `docker info`); the lab's
  one node has no agent, so the images flow is checked by `install-test.sh`.
- Docker's `image rm` without `--force` refuses an image a container (running or stopped) uses: the agent's own
  `docker ps -a` check comes first, Docker's refusal is the second guard.
- Images are identified for keeping by the digests Caravana stored (`repo@sha256:…` as pulled); Docker records the
  pulled digest in `RepoDigests`.
- The default hour 04:00 UTC follows the default backup hour (03:00) by an hour; a backup still running keeps its
  images in use.
