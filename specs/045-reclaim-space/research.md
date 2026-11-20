# Research: Reclaim space

**R1 — Nomad never garbage-collects an image across a restart (source, verified 2026-11-20).**
`drivers/docker/driver.go`: `StartTask` pulls through the coordinator and calls
`d.coordinator.IncrementImageReference(imageID, image, taskID)`. `RecoverTask` (after a client restart) inspects
the container, rebuilds the handle and starts `h.run()`, and never calls `IncrementImageReference`. `DestroyTask`
calls `d.coordinator.RemoveImage(handle.containerImage, taskID)`, and `dockerCoordinator.RemoveImage`
(`coordinator.go`) does `references, ok := d.imageRefCount[imageID]; if !ok { Warn("RemoveImage on
non-referenced counted image id"); return }`. So every image whose task was running at a restart is never removed
when that task ends. The pending deletes (`deleteFuture`, `time.After(removeDelay)`) are in memory and die with
the process. Nomad's own GC removes with `ImageRemove{Force: true}`.

**R2 — Nomad leaves Docker's anonymous volumes behind (source and lab).** Every `ContainerRemove` in the driver
passes `ContainerRemoveOptions{Force: true}` and no `RemoveVolumes`, so a container made from an image with a
`VOLUME` line leaves its anonymous volume. Lab, observed 2026-11-20 (`docker system df` in `caravana-lab`): Local
Volumes 154 total, 0 active, 91.05 MB, 100 % reclaimable; images 7 total, 3 active, 326.5 MB reclaimable; build
cache 0. The anonymous volumes come from the gate's `registry:2` (`VOLUME /var/lib/registry`), the Postgres apps
and the like. Nothing ever reattaches one: a new container gets a new empty volume.

**R3 — Where a dynamic host volume lives (lab, observed 2026-11-20).** `nomad volume create` with the `mkdir`
plugin in the lab made `/opt/nomad/data/host_volumes/<volume ID>` (`drwxrwxrwx`, ID
`e971d06a-1f46-fc04-583b-25618814daeb`), and `volume status -json` answered `HostPath` equal to it. The folder
`host_volumes` is `drwx------ root`. Deleting the volume removed the directory. So the agent can `du` the
children of the fixed path `<data_dir>/host_volumes` (both the lab's and the agent's `nomad.hcl` set
`data_dir = "/opt/nomad/data"`) and report each by its directory name, which is the volume ID: no path from the
console. A volume `Get` also answers `Allocations`, the non-terminal copies that claim it (`api.HostVolume`).

**R4 — How the agent sees Docker.** It already runs `docker info --format {{.ServerVersion}}` as root. The
inventory uses four reads: `docker image ls -q --no-trunc` (IDs) and `docker image inspect --format '{{json .}}'
<ids…>` (`Id`, `RepoDigests`, `RepoTags`, `Size` in bytes, `Created`); `docker container ls -a -q` and `docker
container inspect --format '{{.Image}}' <ids…>` (the image ID each container, running or stopped, uses); and
`docker system df --format '{{json .}}'`, one JSON line per type (`Type` "Images", "Containers", "Local
Volumes", "Build Cache"; `TotalCount`, `Active`, `Size`, `Reclaimable` as Docker's human text, e.g.
`"326.5MB (22%)"`): the dangling volumes are `TotalCount − Active` of "Local Volumes" with `Reclaimable` as
their bytes, and the build cache is `Reclaimable` of "Build Cache". Human sizes are parsed with Docker's own
decimal units (B, kB, MB, GB, TB), to the precision Docker prints. A removal is `docker image rm <ref>…` with
every `RepoTags` and `RepoDigests` entry of the image (an image known by two names is untagged twice and the
last untag deletes it, where `docker image rm <id>` would refuse "referenced in multiple repositories"), `docker
image rm <id>` for an image with no name, `docker builder prune -af` for the cache, and `docker volume prune -f`
(anonymous volumes no container uses; Docker's own rule) when asked. No `--force` on an image: Docker refuses
an image a container uses, which is the second guard after the agent's own `container ls -a` check. Every
command's exact output is checked on the lab VM (`limactl shell caravana-lab sudo docker …`) by the agent task.

**R5 — The keep rule.** Caravana's digests are `repo@sha256:<index or manifest digest>` as pulled
(`internal/registry` pins a tag to the digest the registry answers for the tag, and Nomad pulls that string).
Docker records the pulled reference in `RepoDigests` as `repo@sha256:…`, so the digest part matches what
`deployments.definition` holds. An image pulled by tag *and* by digest has both references; it is kept when any
digest of its `RepoDigests` is in the keep list. Helper images come from `nomadjob.Settings` (names,
caravana-volumes/upload = `BackupImage`, sqlite) and are pinned by digest too, so they are in the list by digest.
Traefik's and the pause image are in use by containers.

**R6 — Why not a Nomad job or the Docker socket.** A job that prunes needs the socket mounted, which is root on the
host for whatever runs in it; the docker plugin has `allow_privileged = false` and no `volumes.enabled`, and
loosening either for one helper gives every job the same door. Caravana itself runs as a service user under
`ProtectSystem=strict`; a Docker group membership is root-equivalent. The agent is root already, is the one
thing on the host that applies what the console says, and applies it only after validation (031 §9): it fits.
The cost: the lab (no agent) can't run the images flow, so `install-test.sh` checks it on a real host.

**R7 — The lab's one node and the console's name for it.** `ListServers` lists a Nomad node without a row as
`managed: false` under its Nomad node name; `/v1/servers/{name}` finds it. The space read for it answers the
orphan volumes (from Nomad, by `NodeID`) and `images: null` with `reason: "no agent"`.

**R8 — Lab, observed in Phase E (2026-11-20).** On a real Nomad 2.0.7 a one-off batch job mounting a dynamic host volume with
`single-node-single-writer`/`file-system` is placed on the volume's node, reads it through a read-only mount, and once the job is
purged its claim is gone at once: the volume deletes with no "in use". A deployment whose copy never passes its check is
rescheduled until the 10 minute progress deadline before Nomad reverts it. On Docker 29, `docker image rm` with an image's tag and
its digest reference removes the image at the first name and fails on the second with "No such image", exit 1 (so the agent asks
Docker whether the image is still there before calling that a failure).
