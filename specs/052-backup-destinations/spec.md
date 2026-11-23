# Spec 052: Several backup destinations and a second copy

CodeAlta task `mv6m` ("22 · Several backup destinations and a second copy", raised by the maintainer on 2026-11-12).

## Why

Caravana has one backup destination per installation (Settings › Backup storage). A backup in one place is one
failure away from lost: the usual rule is a second copy elsewhere. Teams also want a project's backups in its own
bucket (a client's).

## User stories

1. **P1 · A second copy.** As the owner, I add a second destination (`offsite`) and tell a service's backup policy to
   copy each backup there (`copyTo: ["offsite"]`). Every backup lands in the default destination and is then copied to
   `offsite`, each place keeping its own number of backups. The backup's record says where every copy is and which
   copy failed. When the object is gone from the first place, a restore takes it from the second.
2. **P1 · Existing hosts keep working.** On an installation that had one destination, after the upgrade that
   destination is named `main` and is the default, its secret key stays where it was, and every backup runs and
   restores exactly as before, with no deploy.
3. **P2 · Named destinations.** As the owner, I keep several S3-compatible destinations, each with a name, test each
   one from Settings, mark one as the default, and remove one nothing uses.
4. **P2 · Per policy and per project.** A backup policy may name its destination (`destination: "client-a"`); a
   project may set its own default, which every policy in the project without a destination uses.
5. **P2 · Told what failed.** When a backup or a copy fails, the needs-you item and the notification name the
   destination.

## Requirements

- FR-001 Destinations are named (1–32 lowercase letters, digits and hyphens, starting and ending with a letter or
  digit). Each has an endpoint, a region, a bucket, a prefix, an access key ID and a secret access key. The secret
  key lives only in a Nomad Variable, `caravana/backup-destinations/<name>`; the migrated destination keeps its old
  variable `caravana/backup-destination`. It is never returned, logged, shown or put in a job.
- FR-002 Exactly one destination is the default while any exists. The first one saved becomes the default. The
  default can't be removed; another must be made the default first.
- FR-003 A destination used by a policy (as its destination or a copy) or by a project's default can't be removed;
  the refusal names who uses it. Records of backups kept there stay, and say the destination is gone.
- FR-004 A backup policy may set `destination` (a name) and `copyTo` (at most 2 names, distinct, not the backups'
  own destination) and `copyKeep` (1–100, only with `copyTo`; default the policy's `keep`). Unknown names are
  refused at deploy time naming the field.
- FR-005 A project may set `backupDestination`. A policy's destination is its own `destination`, else its project's
  default, else the installation's default.
- FR-006 Each backup records the destination it went to. Each copy is recorded with its destination, status
  (`pending`, `running`, `succeeded`, `failed`), object, size, message and whether retention deleted it.
- FR-007 A copy is made after the backup succeeds, by Caravana, for every engine (postgres, mysql, mongodb, volume,
  sqlite), and survives a restart of Caravana. Each destination's retention keeps its own `keep` newest backups of
  the service and never deletes a pinned one.
- FR-008 A restore may name the destination to read from (`from`). Without it, Caravana tries the backup's own
  destination first, then each copy in order, and restores from the first place that still holds the object. A
  backup is listed and restorable while any of its copies is.
- FR-009 Settings › Backup storage lists every destination with its last test, and Test checks each one from the
  servers (list, write and delete a small object).
- FR-010 A failed backup's needs-you item names its destination; a failed copy is its own item
  (`backup_copy_failed`) naming the destination, resolved by a later copy that succeeds.
- FR-011 Changing a destination's settings, the default or a project's default points every affected backup at
  the new place from its next run, without a deploy.
- FR-012 The API keeps `GET/PUT /v1/backups/destination` working: it reads and writes the default.

## Refusals

| Case | Where | Answer |
| --- | --- | --- |
| A name that isn't a label | `name`, `services[i].backup.destination`, `.copyTo[k]`, `backupDestination` | 400 `invalid_field` |
| A destination that doesn't exist | the field naming it | 400 `invalid_field` (deploy), 404 `destination_not_found` (its own routes) |
| `copyTo` names the backups' own destination | `services[i].backup.copyTo[k]` | 400 |
| More than 2 copies, a duplicate, `copyKeep` without `copyTo` | the field | 400 |
| Remove the default | — | 409 `destination_is_default` |
| Remove a destination in use | — | 409 `destination_in_use`, the message names the users |
| `default: false` on the default | `default` | 400 |
| Restore `from` where the backup has no live copy | `from` | 400 |
| The object is in none of its places | — | 409 `backup_missing` |

## Out of scope

Other kinds of destination (SFTP, local disk), encryption by Caravana, moving existing backups between destinations,
renaming a destination, a third copy, copies made by the provider (bucket replication), per-copy pinning.

## Done when

- A service backs up to the default and copies to a second destination; deleting the object in the first and
  restoring from the second works (gate step `backup-copies`).
- An existing host keeps its destination as the default and every backup as before (migration test; the existing
  backup gate steps pass unchanged).
- No key appears in any page, answer, job or log (sentinel tests in `service`, `api`, `ui` and the gate step).
