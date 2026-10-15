# Quickstart: validate volume backups

Shapes and copy are in [contracts/api.md](./contracts/api.md) and
[contracts/console.md](./contracts/console.md).

## Prerequisites

```bash
deploy/dev/lab.sh up
eval "$(deploy/dev/lab.sh env)"
go run ./cmd/caravana
TOKEN="$(go run ./cmd/caravana admin create-token --name qs)"
```

A backup destination: in the lab, the gate's `rclone serve s3` bucket job (see
`cmd/caravana/gate_test.go`, step `backup-restore`), or any S3-compatible bucket. Set it in
Settings › Backup destination.

## 1. The volumes panel (US3)

1. New app › Compose file, name `kuma`:

   ```yaml
   services:
     uptime-kuma:
       image: louislam/uptime-kuma:2
       expose: ["3001"]
       volumes: ["uptime-kuma-db-data:/app/data"]
   volumes:
     uptime-kuma-db-data: {}
   ```

   Domain `kuma.localhost`, health `/`. Deploy and wait for healthy.
2. Backups tab. Expected: a Volumes row `uptime-kuma-db-data`, mounted at
   `uptime-kuma:/app/data`, a host path, a size measured just now, and "not backed up".
3. `curl -s -H "Authorization: Bearer $TOKEN" localhost:7070/v1/apps/kuma/volumes | jq .` shows
   the same.

## 2. Back up (US1)

1. Configure: set the service's backup to "Volume", keep 3, and deploy. Expected: the Backups tab
   says the service stops while a backup runs.
2. In Uptime Kuma (`https://kuma.localhost:8443`), finish setup and add a monitor "A".
3. Backups › Back up now › confirm. Expected:
   - within a minute, a succeeded backup with its size and "stopped N s" (under 30 s);
   - the app is healthy again, monitor A is there;
   - `nomad job history kuma` has no new version;
   - the bucket has `<prefix>/kuma/uptime-kuma/<ts>/uptime-kuma-db-data.tar.gz`.
4. Back up three more times. Expected: three listed, the oldest gone from the bucket.

## 3. Restore (US2)

1. Delete monitor A, add monitor B.
2. Restore the first remaining backup, typing `uptime-kuma`. Expected: the restore succeeds, with a
   stop duration; the app comes back healthy with A and without B.
3. Restore a backup whose archive you deleted from the bucket. Expected: the restore fails with the
   reason, the app is healthy with its data unchanged.

## 4. Edges

- Deploy (Configure › Deploy) while a backup runs. Expected: the deploy starts after the backup
  ends.
- Back up now twice quickly. Expected: the second is refused, "A backup or restore of this
  service is running."
- Set `engine: volume` on a service without volumes. Expected: 422 naming
  `services[0].backup.engine`.
- Three backups in an hour. Expected: the app is not on the needs-you list.

## 5. Automated

```bash
go test ./internal/app ./internal/nomadjob ./internal/deploy ./internal/store ./internal/service ./internal/api ./internal/ui
CARAVANA_LAB=1 go test ./cmd/caravana -run TestConsoleLab -v
CARAVANA_LAB=1 go test ./cmd/caravana -run TestLab -v -timeout 25m   # gate step volume-backup-restore
```

On the maintainer's host after the release: rerun the installer (new policy capabilities), deploy
Uptime Kuma once (it gains the `caravana-volumes` task), set its backup to Volume, back up now,
and restore once (SC-001).
