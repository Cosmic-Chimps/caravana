# Runbook: Caravana on Hetzner, with Hollo, and the Phase 2 gate

From nothing to a public Caravana host with your Hollo on it, about an hour. It also proves the
Phase 2 gate: a multi-service Compose app with its database on a volume, and a backup that
restores.

Replace `example.com` with your domain everywhere below.

| Name | What |
| --- | --- |
| `caravana.example.com` | Caravana's console, API and webhooks |
| `social.example.com` | Hollo |
| `whoami.example.com` | the test app `host.sh prove` deploys; delete it afterwards |

## 0. On your Mac

- This repository on `main`, Go 1.27+, `python3`, `ssh`, `curl`, `openssl`.
- An SSH key (`~/.ssh/id_ed25519.pub`). Hetzner puts it on the server.

## 1. The server (Hetzner Cloud console)

1. **Add Server**:
   - Location: any, e.g. Falkenstein.
   - Image: **Ubuntu 24.04**.
   - Type: **CAX11** (Arm64, 2 vCPU, 4 GB, about €4/month). Caravana runs on arm64 and amd64, and so does Hollo.
   - Networking: public IPv4 on.
   - SSH key: yours.
   - Name: `caravana`.
2. Note its IPv4 address. Below it is `203.0.113.10`.
3. If you add a Hetzner **Firewall**, allow inbound TCP 22, 80 and 443 only. Caravana's
   installer also sets up `ufw` on the server with the same rule.
4. Check you can log in: `ssh root@203.0.113.10`, then `exit`.

## 2. DNS (at your domain's registrar or DNS host)

Three **A** records to the server's IPv4, or one wildcard `*.example.com`:

```
caravana.example.com  A  203.0.113.10
social.example.com    A  203.0.113.10
whoami.example.com    A  203.0.113.10
```

Wait until each one answers with the IP: `dig +short caravana.example.com`. The installer
refuses to start until the console's name does, because Let's Encrypt can't issue its
certificate before that.

## 3. Backup storage (Hetzner console › Object Storage)

1. Create a bucket in the server's location, e.g. `caravana-backups`.
2. Create S3 credentials and note the access key and the secret key. The secret is shown once.
3. Note the endpoint and region: `https://fsn1.your-objectstorage.com` and `fsn1` for
   Falkenstein (`nbg1`, `hel1` elsewhere).

You enter these in step 7, after the gate run.

## 4. Install Caravana (on your Mac)

```bash
cp deploy/vm/host.env.example deploy/vm/host.env
```

Fill in `deploy/vm/host.env`:

```bash
CARAVANA_HOST=root@203.0.113.10
CARAVANA_DOMAIN_CONSOLE=caravana.example.com
CARAVANA_DOMAIN_APP=whoami.example.com
CARAVANA_ADMIN_EMAIL=you@example.com
CARAVANA_ACME_EMAIL=you@example.com
CARAVANA_ACME_STAGING=0
```

Then:

```bash
deploy/vm/host.sh up
```

It builds a release on your Mac, copies it to the server, and runs the installer there. That
takes about 5 minutes. It asks for the admin's password twice, at least 12 characters. It ends
with:

```
Caravana 0.0.0-dev.… is installed.
  Console:  https://caravana.example.com/
```

Open `https://caravana.example.com/login` and sign in.

If it stops early, it says why: a domain that doesn't resolve yet, a port in use, and so on.
Fix that and run `host.sh up` again. Rerunning is always safe.

## 5. Prove the gate (on your Mac, about 15 minutes)

```bash
deploy/vm/host.sh prove
```

It deploys whoami at `whoami.example.com`, breaks it on purpose, rolls it back, and checks HTTPS,
the redirects, the closed ports, secrets, a Compose app, backups, rollback and export. It ends
with `GATE PASSED`. If it doesn't, keep the output: the failing step is printed with its reason.

Do this **before** step 7. Its backup step points the backup destination at its own test
bucket.

## 6. An API token (console)

Go to **Settings › API tokens**, create one named `laptop`, and copy it. It is shown once. Then:

```bash
export CARAVANA_URL=https://caravana.example.com
export CARAVANA_TOKEN=cvat_…
curl -s -H "Authorization: Bearer $CARAVANA_TOKEN" $CARAVANA_URL/v1/apps   # should list whoami
```

## 7. The backup destination (console)

Go to **Settings › Backup destination** and enter what step 3 gave you:

- Endpoint: `https://fsn1.your-objectstorage.com`
- Region: `fsn1`
- Bucket: `caravana-backups`
- Prefix: `caravana/`
- The access key and the secret key

Save. This replaces the gate's test bucket.

## 8. Hollo's settings (on your Mac)

```bash
cd deploy/examples/hollo
cp overlay.example.json overlay.json      # gitignored: it holds secrets
openssl rand -hex 32                      # SECRET_KEY
openssl rand -hex 24                      # the database password
```

In `overlay.json`:

- `social.example.com` becomes your Hollo domain.
- `REPLACE: openssl rand -hex 32` becomes the first value.
- `REPLACE-DB-PASSWORD` becomes the second value, in **both** places: it is the same password.

In `compose.yaml`, set `STORAGE_URL_BASE` to `https://<your Hollo domain>/assets`.

Pick the domain now and keep it. Hollo's identity and the fediverse's follows are tied to it, so
changing it later breaks them.

## 9. Deploy Hollo

```bash
./deploy.sh preview      # the app it becomes; changes nothing
./deploy.sh deploy
```

`preview` should show two services, `hollo` and `postgres`, each with a volume. The one warning,
about `depends_on`, is expected: Nomad restarts Hollo until Postgres answers.

In the console, open the **hollo** app and its **Deployments** tab. The first deployment pulls
both images and starts them, which takes a minute or two. It ends **healthy**.

Each service has a volume, so it runs one copy, and a later deploy stops it before starting the
new one: a few seconds offline per deploy.

## 10. Set up Hollo (browser)

1. Open `https://social.example.com/`. Hollo walks you through creating its login and then your
   account (your handle, e.g. `@you@social.example.com`).
2. Write a first post.
3. From any Mastodon account, search for `@you@social.example.com` and follow it. That shows
   federation works.

## 11. Back up and restore: the Phase 2 gate (console, hollo app, Backups tab)

1. Write a post in Hollo: **"before the backup"**.
2. Backups tab › **Back up now** on `postgres`. Wait until the backup is **succeeded** with a
   size.
3. Write another post: **"after the backup"**.
4. On that backup, choose **Restore**, type `postgres` to confirm, and wait for **succeeded**.
5. Deployments tab › **Deploy**, keeping the same image, so Hollo reconnects to the restored
   database.
6. Reload Hollo. "before the backup" is there, and "after the backup" is gone.

That is the gate. Tell me, and I record it as passed.

## 12. Afterwards

- Delete **whoami**: console › whoami › Configure › Delete app. It was only the gate's test app.
- Backups run every day at 03:00 UTC (the default schedule) and keep the last 14
  (`"keep": 14` in `overlay.json`).
- Only the database is backed up, not Hollo's media volume (uploaded images). If you need those,
  that's a feature to ask for.
- To upgrade Hollo, change its `image:` tag in `compose.yaml` and run `./deploy.sh deploy`. To go
  back, use Deployments › **Redeploy this version** on the previous deployment.
- To upgrade Caravana, run `deploy/vm/host.sh up` again. It keeps the data and the apps.
- Export Hollo as a plain Nomad job any time: Configure › Export, or
  `GET /v1/apps/hollo/jobspec`.

## If something goes wrong

| What | Where to look |
| --- | --- |
| The installer refuses | It names the check. `host.sh up` again after fixing it. |
| No certificate yet | The first one takes up to a minute. `deploy/vm/host.sh ssh sudo caravana-nomad alloc logs -job traefik` |
| A deployment fails | Its entry in the Deployments tab has the reason, and the Logs tab has the app's output |
| Hollo can't reach Postgres | `DATABASE_URL`'s password must equal `POSTGRES_PASSWORD`. Fix `overlay.json` and `./deploy.sh deploy` again |
| Caravana itself | `deploy/vm/host.sh ssh journalctl -u caravana -n 50` |
| Nomad | `deploy/vm/host.sh tunnel`, then http://127.0.0.1:14646/ui with the token from `deploy/vm/host.sh env` |
