# Runbook: Caravana on netcup, with Cloudflare, Hollo, and the Phase 2 gate

From nothing to a public Caravana host with Hollo on it, in about an hour plus the wait for DNS.
It also proves the Phase 2 gate: a multi-service Compose app with its database on a volume, and
a backup that restores.

The providers are one example: netcup, Cloudflare and R2. Others work the same way. What matters
is Ubuntu 24.04, ports 22, 80 and 443, DNS records pointing at the server, and an S3-compatible
bucket. Replace `example.com` with your domain throughout.

| | |
| --- | --- |
| Server | netcup VPS Lite 3 (8 vCore x86, 16 GB), Ubuntu 24.04 |
| Domain | `example.com`, registered anywhere (Porkbun here), DNS at Cloudflare |
| Backups | Cloudflare R2 |
| `caravana.example.com` | Caravana: console, API, webhooks |
| `social.example.com` | Hollo; your handle becomes `@you@social.example.com` |

Below, `SERVER_IP` is the server's IPv4 address.

## 0. What you need on your Mac

- `ssh` with a key. If `~/.ssh/id_ed25519.pub` doesn't exist, make one with
  `ssh-keygen -t ed25519`.
- `curl`, `openssl`, `python3`, `dig`, all preinstalled on macOS.
- This repository, for Hollo's files in `deploy/examples/hollo/`.

## 1. Publish Caravana (once)

The install command downloads Caravana from the repository's latest GitHub release, so there
has to be one:

1. Push `main`.
2. On GitHub, open `Cosmic-Chimps/caravana`, then **Settings › General › Danger Zone**, choose
   **Change visibility**, and make it **Public**.
3. Tag a version and push the tag:

   ```bash
   git tag v0.1.0 && git push origin v0.1.0
   ```

4. **Actions** shows the `release` workflow running: tests, then the build, about 5 minutes.
   When it's green, **Releases** has `v0.1.0` with four files: `install.sh`, two
   `caravana_0.1.0_linux_*.tar.gz` and `SHA256SUMS`.

Check that the command's address works:

```bash
curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/latest/download/install.sh | head -3
```

## 2. Move the domain's DNS to Cloudflare (it can take hours, so start early)

1. At **cloudflare.com**, sign up, then choose **Add a domain**. Enter `example.com`, pick the
   **Free** plan, and continue.
2. Cloudflare imports the records it finds. **Delete them all**: they're Porkbun's parking
   records. You add the right ones in step 5.
3. Cloudflare shows **two nameservers**, like `xxx.ns.cloudflare.com`. Keep that page open.
4. At **porkbun.com**, go to Account › Domain Management, then to `example.com`:
   - If **DNSSEC** is on, turn it off first. Otherwise the switch breaks the domain.
   - Under **Authoritative Nameservers**, choose Edit, replace Porkbun's with Cloudflare's two,
     and save.
5. Back in Cloudflare, choose **Check nameservers**. The domain turns **Active** within minutes,
   rarely a few hours, and Cloudflare emails you. Carry on with steps 3 and 4 meanwhile.

## 3. The server (netcup)

1. At **netcup.com**, order a **VPS Lite 3**. Lite 2 also works; it upgrades to Lite 3 in
   place later.
   - Network: **IPv4 + IPv6**, not IPv6-only. The installer and Let's Encrypt use IPv4.
   - You can't pick the location (Nuremberg, Vienna or Amsterdam). Any of them is fine.
   - A new netcup account may be asked to verify its identity, which can take up to a day.
2. netcup emails the login for the **Server Control Panel (SCP)** at `servercontrolpanel.de`,
   and the server's **IPv4 address**.
3. Install Ubuntu 24.04. The server may come with another OS. In the SCP, open your server,
   then **Media › Images**:
   - Choose **Ubuntu 24.04**, the minimal or server image. Nothing with a panel or Docker
     preinstalled: the installer refuses a server that already runs Docker.
   - Paste your **SSH public key** (`cat ~/.ssh/id_ed25519.pub`) if the dialog has a field for
     it. Note the root password it shows or emails.
   - Use the whole disk, the default. It takes a few minutes.
4. If you couldn't add the key, add it now. It asks for the root password once:

   ```bash
   ssh-copy-id root@SERVER_IP
   ```

5. Check that `ssh root@SERVER_IP` gets you in without a password. Then `exit`.
6. Optional, once key login works: on the server, set `PasswordAuthentication no` in
   `/etc/ssh/sshd_config.d/50-cloud-init.conf` (or `/etc/ssh/sshd_config`), then run
   `systemctl restart ssh`.

netcup puts no firewall in front of the server. The installer sets up `ufw`, which lets only
22, 80 and 443 in. If you ever lock yourself out, the SCP has a **console** that works
without SSH.

## 4. Backup storage (Cloudflare R2)

1. In the Cloudflare dashboard, open **R2 Object Storage**. The first time, it asks for a payment
   method. The free tier, 10 GB, covers this, so you won't be charged.
2. **Create bucket**: name it `caravana-backups`, with the location on Automatic and the default
   storage class.
3. **Manage API tokens › Create API token**, then:
   - Permissions: **Object Read & Write**.
   - Specify bucket: **only `caravana-backups`**.
   - TTL: forever.
4. Note the three values it shows once:
   - **Access Key ID**
   - **Secret Access Key**
   - **S3 endpoint**: `https://<ACCOUNT_ID>.r2.cloudflarestorage.com`

## 5. DNS records (Cloudflare, once the domain is Active)

In **DNS › Records**, add two **A** records, both with **Proxy status: DNS only** (grey cloud,
not orange):

| Type | Name | IPv4 | Proxy |
| --- | --- | --- | --- |
| A | `caravana` | `SERVER_IP` | DNS only |
| A | `social` | `SERVER_IP` | DNS only |

"DNS only" matters. With Cloudflare's proxy on (orange cloud), the names resolve to Cloudflare,
not to the server, and three things break:

- The installer refuses to run: it checks that the console's name points at the server.
- Let's Encrypt can't validate the server.
- Caravana sees Cloudflare's addresses instead of the real visitors', so sign-in limits would
  lock everyone out together.

Add no AAAA (IPv6) records for now.

Check from your Mac until both print `SERVER_IP`:

```bash
dig +short caravana.example.com social.example.com
```

## 6. Install Caravana (on the server, one command)

```bash
ssh root@SERVER_IP
```

Then, on the server:

```bash
curl -fsSL https://github.com/Cosmic-Chimps/caravana/releases/latest/download/install.sh |
  sudo bash -s -- --email <your email> --domain caravana.example.com
```

It checks the machine first and changes nothing if a check fails. Then it installs Docker, Nomad,
Traefik and Caravana, about 5 minutes. It asks for the admin's password twice (at least 12
characters) and ends with:

```
Caravana 0.1.0 is installed.
  Console:  https://caravana.example.com/
```

Open `https://caravana.example.com/login` and sign in with your email and that password.

If it stops early, it says why. The usual reason is a name that doesn't point at the server yet:
check step 5. Running the command again is always safe.

## 7. An API token (console)

Go to **Settings › API tokens**, create one named `laptop`, and copy it. It's shown once. On your
Mac:

```bash
export CARAVANA_URL=https://caravana.example.com
export CARAVANA_TOKEN=cvat_…
curl -s -H "Authorization: Bearer $CARAVANA_TOKEN" $CARAVANA_URL/v1/apps   # {"apps":[]}
```

## 8. The backup destination (console)

Go to **Settings › Backup destination** and enter step 4's values:

- Endpoint: `https://<ACCOUNT_ID>.r2.cloudflarestorage.com`
- Region: `auto`
- Bucket: `caravana-backups`
- Prefix: `caravana/`
- Access key ID and secret access key: from the R2 token

Save.

## 9. Hollo's settings (on your Mac)

```bash
cd deploy/examples/hollo
cp overlay.example.json overlay.json      # gitignored: it holds secrets
openssl rand -hex 32                      # SECRET_KEY
openssl rand -hex 24                      # the database password
```

In `overlay.json`:

- `social.example.com` becomes `social.example.com`. `deploy.sh` also uses it for Hollo's
  asset URLs, so you don't edit `compose.yaml`.
- `REPLACE: openssl rand -hex 32` becomes the first value.
- `REPLACE-DB-PASSWORD` becomes the second value, in **both** places. It's the same password.

Hollo's identity and the fediverse's follows are tied to `social.example.com`, so changing it
later breaks them.

## 10. Deploy Hollo

```bash
./deploy.sh preview      # the app it becomes; changes nothing
./deploy.sh deploy
```

`preview` should show two services, `hollo` and `postgres`, each with a volume. The one warning,
about `depends_on`, is expected: Nomad restarts Hollo until Postgres answers.

In the console, open the **hollo** app and its **Deployments** tab. The first deployment pulls
both images, which takes a minute or two, and ends **healthy**.

Each service has a volume, so it runs as one copy, and a later deploy stops it before starting
the new one. That means a few seconds offline per deploy.

## 11. Set up Hollo (browser)

Hollo is headless: it has a few pages of its own for setup and accounts, and you read and
write through a Mastodon client. The web client Hollo recommends is **Phanpy**, which you
use at `phanpy.social`, so there's nothing more to deploy.

1. Open `https://social.example.com/setup` and create Hollo's login: an email and a password.
   This is how you sign in to Hollo itself.
2. You land on **Accounts**. Choose **Create a new account** and fill it in. Only the
   **username** is permanent. It makes your handle, e.g. `@you@social.example.com`. The
   rest can change later.
3. Open **https://phanpy.social**, choose **Log in with Mastodon**, type `social.example.com`
   as the instance, sign in with step 1's login, and choose **Allow**.
4. Write a first post in Phanpy.
5. From any Mastodon account, search for `@you@social.example.com` and follow it. That shows
   federation works.

Phones work too: Hollo recommends Moshidon on Android and Nightfox DAWN on iOS, with the same
instance name.

## 12. Back up and restore: the Phase 2 gate (console, hollo app, Backups tab)

1. In Phanpy, post **"before the backup"**.
2. On the Backups tab, choose **Back up now** on `postgres`. Wait for **succeeded** with a size.
   In Cloudflare, the dump appears in `caravana-backups` under `caravana/hollo/postgres/`.
3. In Phanpy, post **"after the backup"**.
4. On that backup, choose **Restore**, type `postgres` to confirm, and wait for **succeeded**.
5. On the Deployments tab, choose **Deploy** without changing the image, so Hollo reconnects to
   the restored database.
6. Reload your profile in Phanpy. "before the backup" is there, and "after the backup" is
   gone.

That's the gate. Tell me, and I'll record it as passed.

## 13. Afterwards

- Backups run every day at 03:00 UTC and keep the last 14 (`"keep": 14` in `overlay.json`).
  Only the database is backed up, not Hollo's uploaded images.
- **Upgrade Hollo**: change its `image:` tag in `compose.yaml`, then run `./deploy.sh deploy`.
  To go back, use Deployments › **Redeploy this version** on the previous deployment.
- **Upgrade Caravana**: when there's a new release, run step 6's command again on the server,
  without options; it reuses the ones you gave the first time. It keeps the data and the apps.
  If the new version doesn't come up, it puts the old one back.
- **On the server**:
  - `sudo caravana version`
  - `sudo caravana admin reset-password`
  - `sudo caravana-nomad status`
  - `journalctl -u caravana`
- **Export Hollo** as a plain Nomad job: Configure › Export.
- **More apps** (Frunchy, …): add an A record per domain (DNS only) and deploy each as its own
  app. Every Compose service needs a `mem_limit`: without one, it gets 64 MB.

## Optional: the automated gate (`host.sh prove`)

Caravana's own end-to-end checks against this server: about 15 minutes, it needs Go 1.27+ on your
Mac, and it adds a test app. Run it **before step 8**, because it points the backup destination
at its own test bucket. Otherwise set the destination again afterwards.

1. Add a third DNS record, `whoami`, DNS only, to `SERVER_IP`.
2. Copy the example settings: `cp deploy/vm/host.env.example deploy/vm/host.env`. Set:
   - `CARAVANA_HOST=root@SERVER_IP`
   - `CARAVANA_DOMAIN_CONSOLE=caravana.example.com`
   - `CARAVANA_DOMAIN_APP=whoami.example.com`
   - your email in the two email settings
   - `CARAVANA_ACME_STAGING=0`
3. Run `deploy/vm/host.sh prove`. It ends with `GATE PASSED`.
4. Afterwards, delete the **whoami** app in the console.

Don't run `host.sh up` against this server: it would replace the release with a build from your
Mac.

## If something goes wrong

| What | Where to look |
| --- | --- |
| The installer refuses | It names the check. Fix it and run the command again. |
| A name doesn't resolve | Cloudflare › DNS: the record exists, is **DNS only**, and the domain is Active |
| No certificate yet | The first one takes up to a minute. On the server: `sudo caravana-nomad alloc logs -job traefik` |
| A deployment fails | Its entry in the Deployments tab has the reason, and the Logs tab has the app's output |
| A backup fails | Its row on the Backups tab has the message. Check the R2 token is for this bucket and the endpoint has no bucket name in it |
| Hollo can't reach Postgres | `DATABASE_URL`'s password must equal `POSTGRES_PASSWORD`. Fix `overlay.json`, then `./deploy.sh deploy` |
| Caravana itself | On the server: `journalctl -u caravana -n 50` |
| Nomad's web UI | On your Mac: `ssh -L 14646:127.0.0.1:4646 root@SERVER_IP`, open http://127.0.0.1:14646/ui with the token from `sudo cat /var/lib/caravana-host/bootstrap.token` |
| No SSH at all | netcup's SCP › your server › Console, logged in as root |
