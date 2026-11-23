# API contract 052 (openapi 0.58.0)

Every route needs a full API token or a signed-in session; deploy tokens are refused (`403 token_scope`) by the
default deny. Errors are `{"error": {"code", "message", "field"}}`.

## Destinations

`GET /v1/backups/destinations` → 200 `BackupDestination[]`, ordered by name.

```json
{
  "name": "offsite", "default": false,
  "endpoint": "https://s3.eu-central-1.example.com", "region": "eu-central-1",
  "bucket": "acme-backups", "prefix": "caravana", "accessKeyId": "AKIA…",
  "updatedAt": "2026-11-23T10:00:00Z",
  "lastTest": {"ok": true, "message": "listed, wrote and removed a test object", "testedAt": "…"},
  "usedBy": [{"app": "shop--production--db", "service": "db", "role": "copy"}, {"project": "acme"}]
}
```

`lastTest` is absent until a test ran. `usedBy` entries are either `{app, service, role}` (`role` is `destination` or
`copy`) or `{project}`.

`PUT /v1/backups/destinations/{name}` body `BackupDestinationRequest` plus `default` (boolean, optional): creates or
updates. 200 with the destination (201 when created, with `Location`). `secretAccessKey` is required on create and
kept when left out. The first destination is the default whatever `default` says. `default: true` makes it the
default; `default: false` on the default is 400 on `default`. 400 on the field for every invalid input (`name` for a
bad name). 502 when Nomad can't store the key; "saved, but …" 502 when the repoint failed.

`DELETE /v1/backups/destinations/{name}` → 204. 404 `destination_not_found`, 409 `destination_is_default`, 409
`destination_in_use` (the message names who uses it).

`POST /v1/backups/destinations/{name}/test` → 200 `{ok, message, testedAt}` whatever the bucket said (a failed test is
`ok: false`). 404 `destination_not_found`; 502 when Nomad couldn't run the check.

`GET /v1/backups/destination` and `PUT /v1/backups/destination` stay: they read and write the default (a PUT with no
destination yet creates `main`). The answer gains `name` and `default`.

## Projects

`Project.backupDestination` (string, absent for the installation's default). `ProjectUpdate.backupDestination`:
a name, or `""` for the installation's default; absent leaves it. 400 on `backupDestination` for an unknown name, or
when a policy of the project copies to that destination without naming its own.

## Backup policies

`BackupPolicy` (and the Compose overlay's `backup`) gains:

- `destination` (string, optional): a destination's name; absent means the project's default, else the
  installation's.
- `copyTo` (array of at most 2 names, optional): where each backup is copied after it succeeds.
- `copyKeep` (integer 1–100, optional, only with `copyTo`): how many backups each copy destination keeps; absent
  means `keep`.

## Backups and restores

`Backup` gains `destination` (string: where the backup went) and `copies`:

```json
"copies": [{"destination": "offsite", "status": "succeeded", "object": "acme-backups/caravana/shop/db/1763890000.dump",
            "bytes": 52311, "expired": false, "finishedAt": "…"},
           {"destination": "cold", "status": "failed", "message": "copy: …", "expired": false}]
```

`status` is `pending`, `running`, `succeeded` or `failed`. A backup is listed while its own destination or any copy
still holds it (`expired` false there).

`POST /v1/apps/{id}/backups/{backupId}/restore` takes an optional JSON body `{"from": "offsite"}`. Without `from`,
Caravana restores from the backup's own destination, or from the first copy that still holds it when it is gone there
(a check job looks, when the backup has more than one place). 400 on `from` when the backup has no live copy there;
409 `backup_missing` when no place holds the object any more. `Restore` gains `destination` (where it was read from).

## Needs you and notifications

Attention kind `backup_copy_failed` (warning), with `destination`. `backup_failed` items gain `destination`. The
webhook notification's problem object gains `destination` for both.
