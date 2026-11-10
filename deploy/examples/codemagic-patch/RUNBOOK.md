# Runbook: Codemagic Patch on Caravana

[Codemagic Patch](https://patch.codemagic.io/docs/) sends over-the-air updates (the JavaScript bundle
and assets) to React Native and Capacitor apps. You run its server; a CLI or CI publishes a release to
it; the app's SDK checks for updates and downloads them.

On Caravana it is one app with two services, `server` (the API, the release worker and the dashboard,
port 3000) and `postgres` (on a volume, backed up by Caravana). Upstream's Caddy and Silo aren't used:
Traefik serves HTTPS, and updates are downloaded from Cloudflare R2.

| Address | What answers |
| --- | --- |
| `https://patch.example.com` | the server, through Caravana (API, dashboard, `cmpatch`) |
| `https://patch-files.example.com` | the public R2 bucket: what the apps download |

Both addresses are written into the app's store builds, so choose them once.

## What you need first

- A Caravana host with a backup destination set (Settings › Backup storage), and an API token
  (Settings › API tokens) on your Mac as `CARAVANA_URL` and `CARAVANA_TOKEN`.
- The domain's DNS at Cloudflare (for the R2 custom domain).
- A GitHub account in the organization that will own the image.

The licence: the server is under the source-available Codemagic Server License and free below
1,000,000 monthly active users; each version becomes Apache 2.0 after two years. Running it for your
own apps is fine; offering it to others as a hosted service isn't. Keep the built image private.

## 1. Build the server image

Upstream publishes no image; its installer builds `server/Dockerfile` on the host. Caravana deploys
images, so CI builds it.

1. Create a private repository, for example `Cosmic-Chimps/codemagic-patch-image`.
2. Copy `build-image.yml` from this folder to `.github/workflows/build-image.yml` there and push.
3. Find the host's architecture: `ssh <host> uname -m` (`x86_64` is `linux/amd64`, `aarch64` is
   `linux/arm64`).
4. **Actions › build-image › Run workflow**, with the version (`0.9.0`, the newest
   `codemagic-patch-server-v*` tag at
   [releases](https://github.com/codemagic-ci-cd/codemagic-patch/releases)) and the platform.
   It takes a few minutes and pushes `ghcr.io/cosmic-chimps/codemagic-patch-server:0.9.0`.
5. The package is private. Leave it so.

## 2. Let Caravana pull it

1. On GitHub, **Settings › Developer settings › Personal access tokens › Tokens (classic) ›
   Generate new token**, scope **`read:packages` only**, no expiry or a long one. A read-only token
   matters: Caravana gives the image's credentials to the app's own copy (see Settings ›
   Registries in the console).
2. In the console, **Settings › Registries › Add**: host `ghcr.io`, your GitHub user name, the token.
   **Test** says it's accepted.

## 3. Two R2 buckets

In the Cloudflare dashboard, **R2 Object Storage**:

1. **Create bucket** `patch-public`. Under its **Settings**:
   - **Custom Domains › Connect Domain**: `patch-files.example.com`. Cloudflare adds the DNS record
     and the certificate itself.
   - **Public Development URL (r2.dev)**: leave it **disabled**.
2. **Create bucket** `patch-internal`. No custom domain, no r2.dev: it must never be readable without
   a key (the server keeps its own `_internal/` files there).
3. **Manage API tokens › Create API token**: permissions **Object Read & Write**, **only the two
   buckets**, no expiry. Note the **Access Key ID** and the **Secret Access Key** (shown once), and the
   account ID from the S3 endpoint `https://<ACCOUNT_ID>.r2.cloudflarestorage.com`.

Manifests are sent with "revalidate always", so Cloudflare's cache in front of `patch-files` never
serves a stale one. Nothing else to set.

## 4. DNS for the server

In Cloudflare **DNS › Records**, add **A** `patch` → the host's IP, **Proxy status: DNS only** (grey
cloud), as for the console's own name. Check with `dig +short patch.example.com`.

## 5. The GitHub OAuth app (dashboard sign-in)

On GitHub, **Settings › Developer settings › OAuth Apps › New OAuth App** (the organization's settings
work too):

- Homepage URL: `https://patch.example.com`
- Authorization callback URL: `https://patch.example.com/auth/callback`

Note the **Client ID** and generate a **Client secret**.

## 6. Fill in overlay.json

```bash
cd deploy/examples/codemagic-patch
cp overlay.example.json overlay.json     # gitignored: it holds secret values
openssl rand -hex 32                     # once for WORKER_SHARED_SECRET, once for OAUTH_CLI_AUTH_SECRET
openssl rand -hex 24                     # the database password
```

- `vars`: the version you built, both domains, the bucket names, the R2 account ID, the OAuth client
  ID, and `ADMIN_EMAILS` (comma-separated; the first sign-in with one of them becomes the owner).
- `services.server.domains[0].host` is the same as `vars.PATCH_DOMAIN`.
- `services.server.secrets`: the database password goes into `DATABASE_URL` **and** into
  `services.postgres.secrets`' `POSTGRES_PASSWORD`, the same value in both.

Secrets travel only in the overlay. Caravana keeps them in Nomad Variables, and no page, answer or
log shows them.

## 7. Deploy

```bash
export CARAVANA_URL=https://caravana.example.com CARAVANA_TOKEN=cvat_...
./deploy.sh preview      # the app it becomes; expect a warning on depends_on
./deploy.sh deploy
```

The first start runs the database migrations. If the server starts before Postgres answers, it exits
and Nomad starts it again; the deploy turns healthy once `/health/ready` answers.

Check:

```bash
curl -s https://patch.example.com/health/ready
```

Then in the console: the app is healthy, **Domains** shows `patch.example.com` live, and **Backups**
lists `postgres` (use **Back up now** once).

## 8. First sign-in and keys

1. Open `https://patch.example.com`, sign in with GitHub using an address in `ADMIN_EMAILS`.
2. Create the app and its deployments (for example `staging` and `production`) in the dashboard, and
   note each deployment key.
3. On your Mac: `npm install -g @codemagic/patch-cli`, then
   `cmpatch config set server-url https://patch.example.com` and `cmpatch login`.
4. For CI: `cmpatch token create` gives a `cm_pat_…` token; store it as the CI secret
   `CODEMAGIC_PATCH_TOKEN`.

## 9. The app

In the app's native configuration (React Native bare: `Info.plist` and `strings.xml`):

| Key | Value |
| --- | --- |
| `CodemagicPatchApiUrl` | `https://patch.example.com` |
| `CodemagicPatchDownloadBaseUrl` | `https://patch-files.example.com` (exactly `PUBLIC_BASE_URL`) |
| `CodemagicPatchDeploymentKey` | the deployment's key, per build flavor |

Changing any of these needs a new store build. Then publish with `cmpatch release-react` from CI.

## Upgrades

Run **build-image** with the new version, change `vars.PATCH_VERSION`, and `./deploy.sh deploy`.
Migrations run on start; the previous version stays on the deployment history for a rollback, and
Caravana's Postgres backup is the way back if a migration can't be undone.
