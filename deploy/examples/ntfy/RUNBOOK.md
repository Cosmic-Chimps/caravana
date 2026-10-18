# Runbook: ntfy on Caravana, closed to strangers

ntfy sends push notifications to your phone from anything that can make an HTTP request,
Caravana's own notifications included. Out of the box, an ntfy server lets anyone who can reach
it publish to and read any topic. This example closes it:

- Nobody reads or publishes without an account (`NTFY_AUTH_DEFAULT_ACCESS: deny-all`).
- Two users: `admin`, for you and your phone, which reads and writes every topic, and
  `caravana`, which may only publish to `caravana-alerts`, with an access token.
- The users, the access rule and the token are Caravana secrets. There is no config file to
  mount and no command to run inside the container: ntfy reads all of it from `NTFY_*`
  variables and creates the users at start.
- Its data (users, message cache, attachments) is on a volume, backed up every day.

It takes about 15 minutes on a Caravana host that already runs. Replace `example.com` with your
domain throughout.

| | |
| --- | --- |
| `caravana.example.com` | Caravana: console, API, webhooks |
| `ntfy.example.com` | ntfy: the web app, the API, and the address your phone subscribes to |

## 0. What you need

- A Caravana host. `deploy/examples/hollo/RUNBOOK.md`, steps 1 to 8, sets one up from nothing.
- An API token on your Mac, as in that runbook's step 7:
  `export CARAVANA_URL=https://caravana.example.com CARAVANA_TOKEN=cvat_…`
- A backup destination in **Settings › Backup destination** (that runbook's step 8). Without
  one, remove the `backup` line from `overlay.json` in step 2.
- Docker on your Mac, only to make the password hashes and the token in step 2. Nothing is sent
  anywhere: the commands run offline.
- `curl` and `python3`, preinstalled on macOS.

## 1. DNS

Add an **A** record `ntfy` pointing at the server's IPv4 address, **DNS only** (grey cloud) if
your DNS is at Cloudflare. Check from your Mac until it prints the address:

```bash
dig +short ntfy.example.com
```

## 2. ntfy's settings (on your Mac)

```bash
cd deploy/examples/ntfy
cp overlay.example.json overlay.json      # gitignored: it holds secrets
```

Then make three values with ntfy's own image:

```bash
docker run --rm -it binwiederhier/ntfy:v2.31.1 user hash    # admin's password, twice
docker run --rm -it binwiederhier/ntfy:v2.31.1 user hash    # any long random password for caravana
docker run --rm binwiederhier/ntfy:v2.31.1 token generate   # caravana's access token
```

For `caravana`'s password, `openssl rand -hex 24` is fine. Nobody types it: Caravana signs in with
the token. Each `user hash` prints a line starting with `$2a$10$`. The token starts with `tk_`.

In `overlay.json`:

- `ntfy.example.com` becomes your ntfy domain. `deploy.sh` also puts it in `NTFY_BASE_URL`, so
  you don't edit `compose.yaml`.
- `REPLACE-ADMIN-HASH` becomes the first hash, `REPLACE-CARAVANA-HASH` the second, and
  `REPLACE-CARAVANA-TOKEN` the token. Paste each whole, `$` signs included. They are secrets,
  so they reach ntfy exactly as typed.

The three secrets are lists separated by commas. Each entry is:

| Secret | Each entry |
| --- | --- |
| `NTFY_AUTH_USERS` | `name:bcrypt-hash:role`, where the role is `admin` or `user` |
| `NTFY_AUTH_ACCESS` | `name:topic:access`. The topic may end in `*`, and access is `rw`, `ro`, `wo` or `deny` |
| `NTFY_AUTH_TOKENS` | `name:tk_token:label` |

Keep your admin password in your password manager. Only its hash goes to Caravana.

## 3. Deploy ntfy

```bash
./deploy.sh preview      # the app it becomes; changes nothing
./deploy.sh deploy
```

`preview` should show one service, `ntfy`, with a volume, a domain and three secrets, and no
warnings. In the console, open the **ntfy** app and its **Deployments** tab: the first deployment
pulls the image and ends **healthy** in under a minute.

The service has a volume, so it runs as one copy, and a later deploy stops it before starting
the new one. That means a few seconds offline per deploy. Messages sent then fail, and Caravana
retries its own.

## 4. Check that it's closed

```bash
curl -s https://ntfy.example.com/v1/health                                    # {"healthy":true,...}
curl -s -o /dev/null -w '%{http_code}\n' -d hi https://ntfy.example.com/test  # 403: no account
curl -s -o /dev/null -w '%{http_code}\n' -u admin -d hi https://ntfy.example.com/test   # 200, after the password
```

And `caravana`'s token publishes to its topic but reads nothing:

```bash
TK=tk_…   # the token from step 2
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $TK" -d hi https://ntfy.example.com/caravana-alerts   # 200
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $TK" 'https://ntfy.example.com/caravana-alerts/json?poll=1'  # 403
```

## 5. Your phone

1. Install the ntfy app (Android or iOS).
2. **Settings › Manage users › Add user**: the server `https://ntfy.example.com`, the user
   `admin` and its password.
3. **Subscribe to topic** (the `+`): the topic `caravana-alerts`, with **Use another server**
   on and `https://ntfy.example.com` as the server.

The web app at `https://ntfy.example.com` works the same way: it opens on its login page
(`NTFY_REQUIRE_LOGIN`), then you subscribe. Its page loads for anyone, as any website's does; it
is the server behind it that answers 403 without an account, which is what step 4 checks.

## 6. Caravana's notifications (console)

**Settings › Notifications**, add a channel:

- Kind: **ntfy**
- Name: `phone`
- URL: `https://ntfy.example.com/caravana-alerts`
- ntfy access token: the `tk_…` token from step 2
- Send: what you want to hear about. Problems and failed deploys are on by default.

Save, then **Send a test**. It arrives on your phone, and the channel's recent deliveries show it
as sent.

## 7. Afterwards

- **Add a user or a topic**: add an entry to the secret in `overlay.json` and run
  `./deploy.sh deploy`, or edit it in the console's Configure › Secrets (each edit is a deploy).
  A new script that only publishes gets its own user, a `wo` rule on its topic and a token, just
  like `caravana`.
- **Remove a user**: take it out of `NTFY_AUTH_USERS` and deploy. ntfy deletes it from its
  database at the next start, with its tokens and access rules. That includes a user first made
  by hand and later added to the list.
- **Change admin's password**: a new `user hash`, put in place of the old hash, then deploy.
- **Backups** run every day at 03:00 UTC and keep the last 14 (`"keep": 14` in `overlay.json`).
  ntfy stops for the few seconds the archive takes. The Backups tab shows the volume and each run.
- **Upgrade ntfy**: change its `image:` tag in `compose.yaml`, then `./deploy.sh deploy`. To go
  back, use Deployments › **Redeploy this version** on the previous deployment.
- **Export ntfy** as a plain Nomad job: Configure › Export.

## If something goes wrong

| What | Where to look |
| --- | --- |
| A deployment fails | Its entry in the Deployments tab has the reason, and the Logs tab has ntfy's output |
| ntfy stops at start | The Logs tab. An entry ntfy can't read (a hash cut short, a `:` missing) is named there. Fix `overlay.json`, then `./deploy.sh deploy` |
| admin gets 401 | The password doesn't match the hash: make a new hash and deploy. Or `NTFY_AUTH_USERS` didn't reach ntfy: the Configure tab lists the service's secrets by name |
| The phone gets nothing | The app's user is for `https://ntfy.example.com`, exactly the subscribed server. On iOS, `NTFY_UPSTREAM_BASE_URL` must stay set |
| Caravana's test fails with 403 | The channel's URL must use the topic in `NTFY_AUTH_ACCESS` (`caravana-alerts`), and the token must be the one in `NTFY_AUTH_TOKENS` |
| No certificate yet | The first one takes up to a minute. On the server: `sudo caravana-nomad alloc logs -job traefik` |
