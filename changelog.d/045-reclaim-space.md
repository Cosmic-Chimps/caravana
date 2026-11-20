### Added
- **A Space section on each server's page.** Operator › Servers › (server) shows what could go: images no container
  uses (count and size), Docker's own leftover volumes, the build cache and the volumes no app declares any more.
  Each server's agent now reports its images and the size of its Nomad volume directories with every sync; a
  server without an agent says so and lists only the volumes Nomad can read. The disk-nearly-full item on the
  needs-you list links to it. API 0.50.0 (`getServerSpace`, `cleanServer` and six more operations), and one MCP
  read tool, `get_server_space`. See `docs/reclaim-space.md`.
- **Unused images are removed every day, and on Clean up now.** At 04:00 UTC each managed server removes the images
  no container uses, keeping the digests of each app's last 3 healthy deployments (a setting, 1 to 10) so Redeploy
  this version and a rollback start without a pull, every deployment under way, every stored app's definition (a
  stopped app starts without a pull) and Caravana's helper images. The daily run can be switched off in the same
  place; Clean up now can also remove Docker's leftover volumes, which the daily run never does. Each clean-up is
  recorded with what it removed and freed, and listed in the activity feed. Nomad forgets the images it pulled when
  it restarts, so without this old app images stayed for good.
- **Delete a volume nobody declares any more.** A volume dropped from a definition or renamed in a Compose file keeps
  its data on the server; the Space section lists it with its app, size and when it was last deployed with, and
  Delete… asks for its name typed exactly. When the backup destination is set, Back up first archives it there
  (`<app>/volumes/<time>/<volume>.tar.gz`) and deletes it only after the archive is made. A volume a stored app
  declares or a copy still mounts is refused, and every deletion is in the activity feed. The deploy warning for a
  dropped volume now names the server that holds its data.

### Upgrade
- Rerun the installer on every server so its agent reports its images (it does so from its next sync). Until then
  the server's Space section says it has no agent reporting its images.
