# Contract: Console

Plain HTML forms and links; nothing needs JavaScript. Copy below is exact.

## Backups tab

Shown on every app, as today. An app with no volume and no backed-up service keeps today's empty
state; the Volumes section appears only when the app has a volume.

### Volumes (new, first on the tab)

A table with one row per volume:

| Column | Shows |
| --- | --- |
| Volume | name, mono |
| Mounted at | `service:/path` per mount, "(read-only)" when so; "not mounted" when no service mounts it |
| On the host | the host path, muted mono |
| Size | "312 MB", with "measured 2 h ago" muted; "not measured yet" |
| Backup | "every day at 03:00 UTC, last 2 h ago" with the status mark; "not backed up" muted |

Under it, when a volume isn't backed up: "Set a backup on the service in Configure to keep a copy
off this server." with a link to Configure.

### Backups (existing, per backed-up service)

- A `volume` service's heading line: "Volume backup, every day at 03:00 UTC, keeping 7. The service
  stops while a backup runs (last time 4 s)."
- Each row gains "stopped 4 s" for volume runs, and its size is the sum of its archives.
- "Back up now" for a volume service has the confirmation: "The service stops for a few seconds
  while the backup runs." with "Back up now" inside, as a `<details>`.
- "Restore" confirmation for a volume backup: "The service stops, its volumes are replaced by this
  backup's files, and it starts again. Files added since the backup are deleted. Type the service's
  name to confirm."

### Refusals

- No running copy: "The service has no running copy to back up. Start it with a deploy first."
- A run already going: as feature 010 ("A backup or restore of this service is running.").

## Configure and the Compose overlay

- The backup engine select gains "Volume (stops the service while it runs)" beside "PostgreSQL".
- A `volume` engine on a service without volumes shows the field error "This service has no
  volume to back up."
