# Reclaim space: unused images and orphan volumes

A server's disk fills with things no app uses any more. Caravana now shows what could go, on each server's
page under **Operator › Servers › (server) › Space**, removes the unused images by itself every day, and
deletes a volume nobody declares any more only after you type its name.

## What the agent reports

Every server Caravana manages runs a small agent (`caravana node sync`, every 30 seconds, as root). Besides what
it already reported, it now sends:

- **Images**: every image Docker holds, with its digests and tags, its size, and whether a container (running or
  stopped) uses it. At most 1000 images are sent, the largest first.
- **Docker's leftover volumes**: Docker's own anonymous volumes that no container uses (how many, how big). Nomad
  removes a container without removing its volumes, so every container made from an image with a `VOLUME` line
  leaves one behind.
- **The build cache** (its size).
- **The size of each Nomad host volume directory**, measured at most every 15 minutes (so a volume deleted a minute
  ago may show its old size until the next measurement).

`caravana node space`, run as root on a server, prints the same report as JSON.

## What a clean-up removes, and what it keeps

A clean-up removes:

- every image **no container uses** and that is not on the keep list below. An image Docker knows by two digests
  is kept when either is on the list; one with no digest (built or loaded by hand) is removed when no container
  uses it;
- the build cache;
- Docker's leftover volumes, **only** when you tick "Also remove Docker's leftover volumes" on a Clean up now.
  The daily clean-up never removes them.

The **keep list** is the digests of:

- each app's newest **3 healthy deployments** (a setting, 1 to 10), so *Redeploy this version* and a rollback start
  without pulling the image again;
- every deployment that is pending or running, whatever its app's history;
- every stored app's current definition, so a **stopped** app starts again without a pull;
- Caravana's own helper images (the names, volume, SQLite and backup images).

A clean-up never touches an image a container uses, a volume an app declares, a volume a copy mounts, Traefik,
Nomad's data, or anything on the host outside Docker's images and volumes and Nomad's host volume directories.
Caravana never sends the agent a path, a command or an image to remove: only an ID and the digests to **keep**.

One small window exists and isn't guarded: an image that Nomad pulled a moment ago for a container it hasn't
created yet, and that isn't on the keep list, could be removed in that instant. Nomad pulls it again when it needs
it.

## The settings

**Settings** on the Space section (and `GET`/`PUT /v1/cleanup/settings`):

| Setting | Default | |
| --- | --- | --- |
| Keep the last N deployments | 3 | 1 to 10 |
| Clean up every day | on | one clean-up per managed server at **04:00 UTC** (the hour isn't a setting) |

The daily run asks every ready managed server that has reported its images; a server with a clean-up already open
is skipped, and a Caravana that was down at 04:00 runs it when it is back the same day, never twice.

## Clean up now

On the server's page, **Clean up now…** shows what will be removed and asks for confirmation (API:
`POST /v1/servers/{name}/cleanups`, answered 202). The agent picks it up at its next sync (within 30 seconds),
runs it (10 minutes at most) and reports the outcome in the sync after that. The record goes through `requested`,
`running` and `done` (or `failed`, `lost`), with how many images were removed, how many bytes were freed and the
first error if any.

- A second clean-up while one is open on that server is refused (`cleanup_running`).
- A clean-up whose agent never picks it up is `failed` after 10 minutes ("is `caravana-node.timer` running?");
  one that started and was never reported is `lost` after 30 minutes, and a new one can be asked for.
- Each clean-up has one entry in the activity feed ("Cleaned up server one: removed 7 images, freed 1.2 GB").

## Orphan volumes

An **orphan volume** is a Nomad host volume that no stored app declares: it was dropped from a definition, renamed
in a Compose file, or left by a delete that failed. Its data stays on the server (the deploy that dropped it
warns, naming the server). The Space section lists each one with its name, the app it came from ("its app was
deleted" when that app is gone), its size when measured ("not measured yet" until the agent has), when its app was
last deployed with it, and whether a copy still mounts it.

**Delete…** opens a page that asks you to type the volume's name exactly. Only then is the volume and its data
deleted, through Nomad (API: `POST /v1/volumes/{name}/deletions` with `{"confirm": "<name>"}`). It is refused when:

- the typed name differs (400 on `confirm`);
- a stored app still declares the volume (409 `volume_not_orphan`): delete it with its app, or drop it from the
  definition first;
- a running copy mounts it (409 `volume_in_use`), for example after a deploy that dropped it was reverted: the
  live version still has it, and the page offers no delete;
- the volume no longer exists (404 `volume_not_found`).

Each deletion is recorded (the volume, its app, the server, who asked, the outcome) and listed in the feed.

### Back up first

When the backup destination is set, the delete page offers **Back up first** (`backUpFirst: true`). Caravana runs a
one-off Nomad batch job on the server that holds the volume, `<app>_<volume>_archive`, which mounts the volume
read-only and writes one `.tar.gz` to the destination under the app's folder:
`<bucket>/<prefix>/<app>/volumes/<time>/<volume>.tar.gz` (`…/volumes/<time>/…` when the volume's name doesn't read
as an app's). The answer is 202 with a record that is `archiving`, then `deleting`, then `done` with the object's
name; the page and `GET /v1/volumes/{name}/deletions/{id}` follow it.

- The volume is **deleted only after the archive is there**. If the archive fails (the destination refuses, the job
  can't start, the server isn't placing work) the volume is kept and the record is `failed` with the reason.
- The destination's keys are only in the job's own Nomad Variable; the job and its variable are removed when the
  archive ends, whatever happened. If Caravana is killed mid-archive, the next start settles the record as failed
  and removes a leftover job and variable.
- Without a destination the box isn't offered and the API refuses it (409 `no_backup_destination`).

## Servers without an agent

The Nomad node of a development lab, or any node Caravana doesn't manage, has no agent. Its page still lists the
orphan volumes Nomad can read (without sizes), but images, Docker's volumes and the build cache read "—" with
"This server has no agent reporting its images: run the installer on it.", and **Clean up now** is not offered
(API: 409 `no_agent`).

A server whose agent has not reported for 10 minutes is treated the same way for a clean-up. **Rerun the
installer on each server** after upgrading, so its agent reports its images (it does so from its next sync; no
other change is needed).

## The disk item

When 025's disk-nearly-full item is on the needs-you list, its link now goes to the Space section of the server it
names.

## API

`getServerSpace`, `cleanServer`, `listServerCleanups`, `getServerCleanup`, `deleteOrphanVolume`,
`getVolumeDeletion`, `getCleanupSettings`, `putCleanupSettings` (API 0.50.0). Deploy tokens are refused. The MCP
server has one read tool, `get_server_space`. Nothing in MCP deletes or cleans.
