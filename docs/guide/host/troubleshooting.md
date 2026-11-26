# Troubleshooting

Start from what you see. Each section says what to check and what fixes it. The first place to look is almost always the **needs-you list** on the Apps home: it names the app, says what is wrong in one sentence and has a button to the right page (see [Monitoring and the needs-you list](../operate/monitoring.md)).

On the host, the commands here need `sudo`.

## A deploy failed, or was reverted

A deploy is health-checked. Caravana starts the new copies beside the old ones, and the old ones keep serving until the new ones pass their checks. A copy has to stay healthy for 10 seconds. A copy that isn't healthy within 3 minutes is counted unhealthy, and a deploy in which no copy becomes healthy within 10 minutes fails and is reverted to the last good version, which was never stopped. So a failed deploy usually costs you nothing but the new version. The exception is a service with a volume or a published port: it runs one copy, which is stopped before the new one starts, so its deploys have a short gap. The needs-you item says which version keeps running.

To find out why:

1. Open the app's **Deployments** tab. The failed entry shows its stages (how far the deploy got) and an error line with the reason. Common reasons are below.
2. Open the app's **Logs**. A failed copy stays listed and readable for a while, so you can read what it printed before it died. See [Logs](../operate/logs.md).
3. Check the **health check** on **Configure › Services**. By default it is a TCP check of the port, every 10 seconds with a 2-second timeout. If the app serves HTTP and takes time to start, a path check that returns 200 only when the app is ready is better. See [Copies, memory and health checks](../configure/resources.md).

The reasons Caravana words for a deploy that never started:

| The entry says | What it means and what to do |
| --- | --- |
| the image wasn't found | See "Image not found" below. |
| the registry refused the stored credentials | See "Image not found" below. |
| the registry couldn't be reached | The registry didn't answer. Try again; check the host's outbound network and DNS. |
| Nomad couldn't be reached | The host's Nomad isn't answering. Check `sudo systemctl status nomad` and `sudo caravana-nomad status`. |
| Nomad rejected the job | Nomad refused what Caravana registered. The message names the reason; if it names a limit (a memory or health-check value), change it on **Configure › Services**. |
| a job with this name already exists in Nomad and caravana didn't create it | Another job in Nomad has this app's ID. Give your app another name, or remove the other job yourself (`sudo caravana-nomad job status <name>` shows it). |
| the Nomad job changed while deploying | Two changes raced. Deploy again. |

A deploy to a stopped app is refused with "The app is stopped: start it first."

## Image not found, or a private registry

"image not found, or private: add credentials for ghcr.io in Settings › Registries" means the registry says the image doesn't exist, which a private registry also says to someone who isn't signed in.

- Check the image's name and tag for typos.
- For a private image, add credentials in **Settings › Registries** and use **Test**. See [Private registries](../deploy/registries.md). The failed deploy offers **Check the registry's credentials**.
- "the registry refused the stored credentials" means credentials are stored for that registry and the registry turned them down: they expired, were revoked or can't read that image. Update them in Settings › Registries.
- Credentials can be limited to a path on a host (`ghcr.io/acme`); an image outside that path uses the host's entry, or none.

## A domain has no certificate or HTTPS

Open the **Domains** page. Each domain says its state, and **Check now** checks it again.

| State | What to do |
| --- | --- |
| **no DNS record** | Make an A record (and AAAA, if the host has an IPv6 address) pointing at the server's public address. |
| **DNS points elsewhere** | The record leads to another host. Point it here. A domain behind Cloudflare's proxy is judged by its certificate instead, so it shows **live · HTTPS** once the certificate is issued. |
| **getting certificate** | Traefik is asking the certificate issuer (Let's Encrypt unless you changed it) for one. Wait a minute or two. If it stays, check that ports **80 and 443** reach the host from the internet, in your provider's firewall too, and that DNS is right. |
| **Traefik's own certificate** | Traefik is serving its default certificate: this installation was installed with `--acme none`, or no certificate has been issued. Browsers warn about it. |
| **certificate expiring** | A certificate is near its end and wasn't renewed. Check ports 80 and 443 and DNS. |
| **not served** | The app is stopped or has no healthy version. |
| **couldn't check** | The check couldn't be made; **Check now** again. |

See [Domains and HTTPS](../configure/domains.md) and [Certificates](../../certificates.md).

## Out of memory

The needs-you list says "1 copy of Shop › staging › web/web ran out of memory." Nomad killed a copy because it went past its memory ceiling. A service reserves 256 MB by default and may burst to the larger of its reservation and 1024 MB (the installer turns on Nomad's memory oversubscription for that).

Choose **Raise memory** on the item, or set **Memory** (the reservation) and **Ceiling** on the service's **Configure › Services** page, then deploy. If the app really needs more, check the host's room: **Monitoring › This host** says "Room for about N more apps". See [Copies, memory and health checks](../configure/resources.md).

## A copy keeps restarting

The list says "1 copy of Shop › staging › web/web keeps restarting." It appears when restarts and failed copies in the last hour reach 3. Open **View logs**: the last lines before each death are usually the cause (a bad setting, a missing variable or secret, a database that isn't reachable). If a copy restarts because of memory, you'll also see the out-of-memory item. A failing health check doesn't restart a copy by itself: during a deploy it fails the deploy, and later the list says "1 copy of Shop › staging › web/web is failing its health check." If the check is too strict for a slow app, lengthen **Every** or **Timeout** on **Configure › Services**.

## OpenSearch or Elasticsearch won't start

The copy exits and its logs say `max virtual memory areas vm.max_map_count [65530] is too low` (*as of writing, not checked live*). The server's limit is
under 262144. **Operator › Servers › the server** shows `vm.max_map_count` (in the warning colour when it is low; no row
at all on a server whose installer hasn't been rerun); rerun the installer
(it sets 262144 and needs no reboot), or run `sudo sysctl -w vm.max_map_count=262144` for now. If the logs complain about
open files or locked memory, set the service's `ulimits`. See [Limits, sysctls and files](../../opensearch-apps.md).

## "Nomad refused" and "run the installer again"

After an upgrade, a feature may need a new permission or host setting that only the installer can apply. Until then the console says so plainly and everything else keeps working. For example: "Nomad refused the command: Caravana's token can't run commands yet. Run the installer again on the host to update its policy." Rerun the installer of the version you run. See [Keeping Caravana up to date](upgrade.md) and the **Upgrade** part of each release in [Release notes](../../../CHANGELOG.md).

## The console doesn't answer

1. Is Caravana running? On the server:

   ```bash
   sudo systemctl status caravana
   sudo journalctl -u caravana -n 100 --no-pager
   curl -s http://127.0.0.1:7070/v1/health
   ```

   `/v1/health` answers without credentials, with the version, when Caravana is up. If the service isn't running, the journal says why. Caravana restarts itself on failure.
2. If health answers on `127.0.0.1:7070` but not at your domain, the problem is between: Traefik, DNS or the firewall. Traefik runs as a Nomad job: `sudo caravana-nomad status`, and Traefik's own log with `sudo caravana-nomad alloc logs -stderr -job traefik` (without `-stderr` it prints the access log, one JSON line per request). Ports 80 and 443 must be open.
3. During an update from the console, the console is down for about a minute. Reload; the Updates page follows the run.
4. If you removed Caravana with `--uninstall`, the domain answers 502 until you install it again. Your apps keep running.

Your apps don't depend on the console: Nomad runs them. A console that is down doesn't stop an app.

## Locked out

- **Wrong password, locked out for a while**: after 5 failures in 15 minutes from an address, sign-in from it is refused for 15 minutes ("Too many failed sign-ins from this address. Try again in N minutes."). Wait, or sign in from another address.
- **Forgot the password, or lost every passkey**: `sudo caravana admin reset-password` on the host. See [Sign-in, passkeys and tokens](sign-in.md).
- **No one can sign in at all**: `sudo caravana admin status` lists the people; `sudo caravana admin init --email ana@example.com` makes the first owner if none exists.

## The disk is nearly full

The needs-you list opens an item at 85 % full on the disk that holds Nomad's data or Docker's images ("The disk at /opt/nomad is 87% full: 12 GB left."), and it turns red at 95 %. Old Docker images and volumes nobody uses are the usual cause. Open the server's page (**Free up space** on the item), look at **Space**, and choose **Clean up now…**. Caravana also removes unused images every day. See [Reclaim space](../../reclaim-space.md).

## Where the logs are

- **Your apps**: **Logs** in the console. See [Logs](../operate/logs.md).
- **Caravana itself**: `sudo journalctl -u caravana`. Add `-f` to follow it.
- **The updater**: `sudo journalctl -u caravana-update`, and for the last run `sudo cat /var/lib/caravana-update/status` and `/var/lib/caravana-update/logs/<id>.log`.
- **The server agent**: `sudo journalctl -u caravana-node`.
- **Nomad**: `sudo journalctl -u nomad`.
- **The proxy**: `sudo caravana-nomad alloc logs -stderr -job traefik` for Traefik's own log; without `-stderr` it prints one JSON line per request. Requests have their own page, see [Requests](../../requests.md).

Caravana never logs secret values or tokens.

## Looking at Nomad directly

Caravana runs your apps as Nomad jobs, and you can look at them. On the host, `caravana-nomad` is Nomad's own command-line tool with the right address and token already set:

```bash
sudo caravana-nomad status                 # every job
sudo caravana-nomad job status web         # one app; the job is named like the app's ID
sudo caravana-nomad alloc status <id>      # one copy
sudo caravana-nomad alloc logs <id>
```

Looking is safe. Changes made directly to a job in Nomad aren't recorded by Caravana and the next deploy replaces them. To manage an app with plain Nomad, export its job (**Configure › Integrations › Export jobspec**) and see [How a deploy works](../deploy/rollback.md).

## Report a bug

Open an issue at [github.com/Cosmic-Chimps/caravana/issues](https://github.com/Cosmic-Chimps/caravana/issues). Say what you did, what you expected and what happened. Include the output of `sudo caravana version`, the needs-you sentence or the deployment's reason, and the relevant lines of `sudo journalctl -u caravana`. Read what you paste first: remove anything private such as tokens, passwords and secret values.

## See also

- [Monitoring and the needs-you list](../operate/monitoring.md)
- [Keeping Caravana up to date](upgrade.md)
- [Install](../install.md)
