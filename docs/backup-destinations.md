# Backup destinations and copies

Backups go to an S3-compatible bucket. An installation may keep **several** of them, each with a name, and a service
may have its backups **copied** to others, so that losing one bucket (or one provider) doesn't lose the backups.
This works for every engine: `postgres`, `mysql`, `mongodb`, `opensearch`, `volume` and `sqlite`.

## Destinations

A destination has a name (1 to 32 lowercase letters, digits and hyphens, starting and ending with a letter or digit),
an endpoint, a region, a bucket, an optional prefix, an access key ID and a secret access key. The secret key lives
only in a Nomad Variable (`caravana/backup-destinations/<name>`). No page, answer, log line, error or job ever has it,
and the key field on every form is empty.

Manage them in **Settings › Backup storage**: the list shows each destination's name, a **default** mark, its endpoint
and bucket, the last test with its time, and which services and projects use it. Each row has Edit (leave the key
empty to keep it), Test, Make default and Remove, and the page has an Add destination form. The same is
`GET /v1/backups/destinations`, `PUT` and `DELETE /v1/backups/destinations/{name}` and `POST …/{name}/test`.

- The **first** destination saved is the default. There is always exactly one default while any destination exists.
  The default can't be removed: make another the default first.
- A destination something uses can't be removed. The refusal names the users (up to five, then "and N more"): the
  services whose backups go there or are copied there, and the projects whose default it is.
- Names can't change. Backups already made keep the name of the destination they went to, so a record of a backup in
  a removed destination says so.
- **Test** runs a small one-off job on the servers (`_backup-check-<id>`): it lists the destination's folder, writes a
  small object and deletes it. The outcome ("listed, wrote and removed a test object", or "could not write: …") is
  kept with the destination and shown on its row. The message is rclone's line with the key blanked out.
- Changing a destination's settings points every backup job that uses it at the new values from its **next run**.
  Nothing is deployed.

## Where a service's backups go

A service's destination is, in order:

1. the policy's own `destination`,
2. else its **project's** default (project settings › **Backups go to**, or `PATCH /v1/projects/{project}` with
   `backupDestination`; an empty value goes back to the installation's),
3. else the installation's default.

```yaml
backup:
  engine: postgres
  destination: client-a       # optional
  copyTo: [offsite]           # up to 2 other destinations, distinct, never the destination itself
  copyKeep: 14                # optional, 1 to 100; default: the policy's keep
```

On the **Backups** tab each service has a **Where backups go…** form (the destination, "Also copy to", "Keep in each
copy") that saves as a deploy of the app on the revision the page was drawn from. The policy line under the service
says it: "to main, copied to offsite (keeps 14)". The Compose form of New app has the same three inputs in its Backup section (the form of an app that exists holds
only the file and keeps these as they are), and a
Compose overlay takes `destination`, `copyTo` and `copyKeep` under `backup`.

An unknown destination name, a copy to the destination the backups already go to, a duplicate, more than two copies,
or `copyKeep` without `copyTo` is refused at deploy, naming the field (`services[0].backup.copyTo[0]`).

## Copies

When a backup succeeds, Caravana queues one **copy** per name in `copyTo`. A copy is a Nomad batch job beside the app,
`<app>_<service>_copy-<id>`, that copies the object from the backup's destination to the other with rclone (a file, or
a folder for the `volume` engine), checks the copy isn't empty and applies the retention of the target:

- each destination keeps its own newest `keep` backups of the service (`copyKeep` for a copy), and never deletes a
  **pinned** backup;
- at most two copies run at once; a copy waits in the queue across a restart of Caravana;
- a copy that fails is recorded with its message, and the needs-you list says "The copy of web's backup to offsite
  failed" (`backup_copy_failed`, a warning, also a notification with the `destination`). A later copy that succeeds
  resolves it. A failed backup's item and notification name the destination too ("to main");
- a copy of a backup that retention already deleted from its first destination is failed ("the backup was deleted from
  main by retention before it was copied").

Each backup row says where it is: "in main · copied to offsite", "copying to offsite…", "copy to offsite failed: …",
or, once retention deleted it from its own destination, "deleted from main, kept in offsite". A backup is listed and
restorable while **any** place holds it.

## Restore from a place

A restore reads the backup from one of its places:

- Without a choice, Caravana tries the backup's own destination first, then each copy in `copyTo` order. With one
  place it simply reads it. With several it asks a check job (`_backup-check-<id>`) which place still holds the
  object, and a place that says it's gone is marked so (the record says "deleted from main"). If none has it the
  restore is refused (409 `backup_missing`: "the backup isn't in main or offsite any more"); if the check couldn't
  reach a place the answer says so (502).
- The restore confirmation has **Restore from** (Automatic: the first place that has it, then each place that holds the
  backup), and the API takes `{"from": "offsite"}` as the body of `POST /v1/apps/{id}/backups/{backupId}/restore`.
  A place the backup isn't in is refused on `from`. The restore record says which destination it read from.
- A scheduled restore (`restore: {schedule, from}`) uses Automatic.

## An existing host after the upgrade

Nothing to do. The destination you had becomes `main`, the default, with its key where it was
(`caravana/backup-destination`); every backup and restore already recorded says `main`. No policy changed, so no job
changed, and no deploy is needed for backups to keep working. `GET` and `PUT /v1/backups/destination` still read and
write the default. Add a second destination, then give a service `copyTo`, and the first copy is made after its next
backup.

## What it doesn't do

Other kinds of destination (SFTP, local disk), encryption by Caravana, moving existing backups to another destination,
renaming a destination, more than two copies, copies made by the provider (bucket replication), and per-copy pinning.
