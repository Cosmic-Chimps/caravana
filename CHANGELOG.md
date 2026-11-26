# Changelog

What changed in each release, for people who run Caravana. Newest first. A release's notes on
GitHub are its section here: `tools/release.sh notes <version>` prints it.

Notes are not written here. Each change adds one file in `changelog.d/` (see its README), and the
release gathers those files into the new version's section below and deletes them, so this file holds
released sections only. `tools/release.sh unreleased` shows what the next release will say.

Each section has the changes that matter to a user, in this order and only as needed: Added,
Changed, Fixed, Security, then **Upgrade** when an upgrade needs more than rerunning the installer.

## 0.23.1 - 2026-11-26

### Fixed
- **Settings › People listed everyone twice.** Each person now has one row.

## 0.23.0 - 2026-11-25

### Added
- **Another ACME issuer.** Settings › Certificates can now get every domain's certificate from an ACME issuer other
  than Let's Encrypt: Google Trust Services, ZeroSSL, DigiCert (presets fill their address) or any ACME server, with
  the External Account Binding key ID and HMAC key those issuers give you. Saving restarts Traefik once; renewal stays
  automatic. The HMAC key is kept in Nomad's variables and never shown again. Going back to Let's Encrypt finds its
  certificates where they were.
- **Upload a certificate.** A certificate you bought, or your company's, with its intermediates and its private key: a
  single name, several, or a wildcard. Caravana checks that the key matches, the chain is complete, the certificate
  isn't expired and which names it covers, then Traefik serves it for those names at once, with no restart, and asks the
  ACME issuer for none of them. Nothing renews an uploaded certificate: its expiry shows on Domains and in Settings, and
  a needs-you item and a notification come before it ends. Uploading one with the same names replaces it; removing it
  hands its names back to the ACME issuer. The console's own domain can use one too. See `docs/certificates.md`.
- **Domains say who issued each certificate**: Let's Encrypt, the other issuer, or "uploaded: <subject>".
- API 0.63.0: `putCertificateIssuer`, `deleteCertificateIssuer`, `listUploadedCertificates`, `uploadCertificate`,
  `getUploadedCertificate`, `deleteUploadedCertificate`, the issuer on `getCertificateSettings`, the uploaded certificate
  on a domain's certificate, the needs-you kind `certificate_expiring`, and the codes `issuer_unavailable`,
  `certificates_unavailable`, `too_many_certificates` and `certificate_not_found`. Admins only; deploy tokens are refused.
- **A Space section on each server's page.** Operator › Servers › (server) shows what could go: images no container
  uses (count and size), Docker's own leftover volumes, the build cache and the volumes no app declares any more.
  Each server's agent now reports its images and the size of its Nomad volume directories with every sync; a
  server without an agent says so and lists only the volumes Nomad can read. The disk-nearly-full item on the
  needs-you list links to it. API 0.50.0 (`getServerSpace`, `cleanServer` and six more operations), and one MCP
  read tool, `get_server_space`. See `docs/reclaim-space.md`.
- **Unused images are removed every day, and on Clean up now.** At 04:00 UTC each managed server removes the images
  no container uses, keeping the digests of each app's last 3 healthy deployments (a setting, 1 to 10) so Redeploy
  this version and a rollback start without a pull, every deployment under way, every stored app's definition (a
  stopped app starts without a pull) and Caravana's helper images. The daily run can be switched off in the same
  place; Clean up now can also remove Docker's leftover volumes, which the daily run never does. Each clean-up is
  recorded with what it removed and freed, and listed in the activity feed. Nomad forgets the images it pulled when
  it restarts, so without this old app images stayed for good.
- **Delete a volume nobody declares any more.** A volume dropped from a definition or renamed in a Compose file keeps
  its data on the server; the Space section lists it with its app, size and when it was last deployed with, and
  Delete… asks for its name typed exactly. When the backup destination is set, Back up first archives it there
  (`<app>/volumes/<time>/<volume>.tar.gz`) and deletes it only after the archive is made. A volume a stored app
  declares or a copy still mounts is refused, and every deletion is in the activity feed. The deploy warning for a
  dropped volume now names the server that holds its data.
- **MySQL, MariaDB and MongoDB are backed up without stopping them.** `backup: {engine: mysql}` (for the official
  `mysql` and `mariadb` images, or one built on them) and `backup: {engine: mongodb}` (the official `mongo` image)
  work like PostgreSQL's: each scheduled run and each Back up now dumps the live database and uploads it to the
  backup destination (`<time>.sql.gz`, `<time>.archive.gz`), and a restore loads a dump back into the running
  service. Records, pins, retention, restore schedules, `schedule: never`, the needs-you items, notifications and
  the built-in schedules list work as for PostgreSQL. The login is the one the image reads to start
  (`MARIADB_ROOT_PASSWORD` and friends, `MONGO_INITDB_ROOT_USERNAME` and `MONGO_INITDB_ROOT_PASSWORD`) and reaches the
  backup job only through its own Nomad Variable, never in its arguments; a password given as plain environment is a
  deploy warning, as for PostgreSQL. In the console the Backups tab offers "Back up daily" on these images and the
  Compose form has the two engines. API 0.51.0 (`mysql` and `mongodb` engines). See `docs/database-backups.md`.
- **Requests: see each HTTP request Traefik served, per app.** The new Requests page (sidebar, and a tab on every
  app after Logs) lists the requests of one project › environment newest first: when, the service, the method,
  the host and path, the status, how long it took, the bytes and the client's address. Filter by status class
  (2xx to 5xx), method, path prefix, minimum duration and client; a histogram of the range is stacked by status;
  a row opens the Logs around its time. Monitoring's Errors chart links to the 5xx of its range (a click on the
  plot opens the minute you point at) and its apps table has a Requests link per service. Only the path is
  recorded: Traefik drops the query string and every header before the line exists, and Caravana stores
  nothing. The page says how far back it read; after a Traefik restart the earlier allocations are read too while Nomad keeps them. `GET /v1/requests` (API 0.53.0). See `docs/requests.md`.
- **Caravana makes the DNS record for a domain you add, at Cloudflare.** Connect a zone in Settings › DNS providers
  with a Cloudflare API token (Zone › DNS › Edit for that zone) and, at the deploy that adds a domain under it,
  Caravana creates an A record (and an AAAA record when the installation has an IPv6 address) pointing at this
  installation, with the comment `caravana:<app>`; it removes the records when the domain goes or the app is deleted.
  It never changes a record you made: a host that already has a record pointing elsewhere refuses the deploy, naming
  the domain. A zone can proxy its records through Cloudflare (the orange cloud), and changing that brings the records
  Caravana made to the new setting. Test lists the zone's records, Remove forgets the zone and leaves the records at
  Cloudflare. The Domains page says which domains' records Caravana made, and its Add panel names the zones Caravana
  handles itself. The installation's public address comes from `CARAVANA_PUBLIC_URL`. API 0.54.0 (`listDNSZones`,
  `putDNSZone`, `deleteDNSZone`, `testDNSZone`, `listDNSRecords`; codes `dns_record_exists`, `dns_address_unknown`,
  `dns_provider_refused`, `dns_provider_unavailable`, `dns_zone_not_found`). See `docs/dns-providers.md`.
- **A preview for every pull request.** An environment named `pr-<number>` is a preview: caravana-action's new
  `preview: true` opens it with a deploy token, copies the app of `staging` into it with the pull request's image and
  its own address, and removes it when the pull request closes. A deploy token may now create preview environments
  of its own project and delete its own app and the empty environment there, and nothing else.
- **Limit, expiry and a password for previews.** Each project keeps at most 5 open previews by default (0 to 20) and
  closes one 7 days after its newest deploy (1 to 30), so a missed close still cleans up. An optional project
  password makes every preview address Team only. All three are on the new Previews page (sidebar, "setup per
  project"), whose first tab lists every open preview with its address, memory, state and a Close… button; the
  project's settings page shows each preview's expiry too. The API has `getPreviews`, `putPreviewSettings` and
  `closePreview` (0.55.0). See `docs/previews.md`, including the one wildcard DNS record to set by hand.
- **Deploy a repository that has a Dockerfile, with no CI.** New app has a **Build from Git** tab: give a repository, a
  branch, an optional folder and Dockerfile, and the image to push to. A deploy clones the branch, builds the Dockerfile
  with rootless BuildKit as a Nomad batch job, pushes the image with the credentials of the registry entry that covers
  it (Settings › Registries) and deploys the pushed digest like any image: health checked, reverted on failure.
  Rollback and promotion deploy a past digest and never build. Through the API a service has `build` and its `image` is
  the repository builds push to. See `docs/building-from-git.md`.
- **Private repositories.** A deploy key that Caravana makes (its public half and fingerprint are shown, the private
  half stays in a Nomad Variable and in no page, answer or log) or a token you give. Configure has a Build panel for
  the source and the Git access, and Next steps on a new app's page walk through the key or token, the push webhook and
  the first build. The keys of github.com and gitlab.com are built in; any other SSH host gives its key.
- **A push builds and deploys.** The app's webhook is the repository's push webhook: GitHub, GitLab (also its
  `X-Gitlab-Token`), Gitea and Forgejo bodies are understood. A push to another branch, a branch deletion, a ping and a
  push of the commit already live build nothing.
- **Builds on the Deployments tab.** A deployment that built shows a Build stage (queued, building with the time it has
  taken, built in 41 s, failed), the commit, branch and subject, and a **Build log** page that follows a build that is
  going. A failed build fails its deployment with the last log lines, sends the usual `deploy.failed` notification and
  leaves the running version alone. A newer deploy cancels the build in progress.
- **Settings › Builds**: memory, CPU, timeout, builds at once and the size of each app's build cache. API 0.56.0:
  `getDeploymentBuild`, `getGitAccess`, `createDeployKey`, `putGitToken`, `deleteGitAccess`, `getBuildSettings`,
  `putBuildSettings`, `Service.build`, `Deployment.build`, `WebhookResult.skipped`.
- **A secret can be built from text and references.** Instead of one reference replacing a whole value, a secret may be
  a `template` with `refs`: `Host=postgres;Username=banana;Password=${pw}` with `pw` read from your secret provider
  (Bella Baxter). A deploy reads the references, puts each value where its `${name}` is (`$$` is a literal `$`) and
  keeps the result like any other secret, so a connection string can use a password that never leaves the provider.
  Refresh secrets builds the values again and rolls the app out when one changed. A template that doesn't parse, or a
  reference that is missing or unused, is refused naming `services[i].secrets[j].template` or `…refs.<name>`. The console
  lists a template read-only ("template with 2 references: …") and keeps it through a save; set one through the API or
  the Compose overlay (the Aspire publisher sends them). API 0.59.0. See `docs/secret-providers.md`.
- **People and roles.** An installation can have several people, each with their own sign-in and API tokens. An
  owner or an admin invites someone from **Settings › People** (an email, a role, an optional end date and, for a
  member, a role per project with an override per environment) and sends them the link Caravana shows once; they
  choose a password and are in. A member sees only the places where they have a role: a **viewer** reads, a **deployer**
  deploys new images, redeploys, restarts and backs up now, a **maintainer** changes how an app is set up. A guest has
  an end date, after which they can't sign in. The installation's pages (Operator view, servers, registries, backup
  storage, notifications, people and the rest) are for owners and admins. See `docs/people.md`.
- **API tokens act as their person.** `GET /v1/me` says who a credential is, the people have their own routes
  (`listPeople`, `invitePerson`, `getPerson`, `updatePerson`, `removePerson`, `reinvitePerson`, and the public
  `acceptInvite`), and a refusal is `403 not_allowed` with the field that needs more. A deploy-level person may
  deploy images only: any other change is refused, naming the field, which is what the CI action and MCP's
  `deploy_image` send. MCP has a read tool, `get_me`.
- **Who did it.** Deployments, actions, backups started by hand and restores say who made them: "by you",
  "by ana@example.com", or "through the API by ci (ana@example.com)". The API records carry `actor`.
- **Network and disk throughput per server.** Each server's page (Operator › Servers) has a **Network and disk**
  section: bytes per second in and out of its public network interface, bytes read and written on the disk that
  holds Nomad's data and Docker, and how busy that disk is. **Monitoring › This host** draws two small charts of
  the same over the page's range. The server's agent reads the kernel's counters and Caravana works out the rates,
  so nothing new is installed or logged in to. A server without an agent, or with an older one, shows a dash and
  says why. `GET /v1/host` has the same numbers under `io`. See `docs/host-io.md`.
- **Drop the earlier certificates, from the console.** After you upload a certificate, a domain that already had a
  Let's Encrypt certificate keeps presenting it, and the uploaded certificate's row says "Still on their earlier
  certificate". Settings › Certificates now has a **Drop the earlier certificates** button for it: the agent on the
  first server removes from Traefik's store the ACME certificates whose every name an upload covers, Traefik restarts
  once (a few seconds without HTTPS), and the page says what was dropped. Only names are exchanged, never a
  certificate or a key. The API is `dropEarlierCertificates` (`POST /v1/certificates/drops`), with
  `listCertificateDrops` and `getCertificateDrop`.
- **An audit log: who changed what.** Every change leaves one entry with when, who (a person, a token, a webhook, Caravana
  itself, or the host's command line), the door it came through (console, API, MCP, webhook), what was done, to what, and
  the fields that changed, such as a domain removed and another added. A secret, a token or a password is never in an
  entry: it shows as `set`, `changed` or `removed`. Failed sign-ins, deploys, rollbacks, promotions, deletions, settings,
  tokens, people, servers and more are all there. Owners and admins read it in **Settings › Audit log** (filters, 100 a
  page, **Older**, changes under each entry) and over the API (`listAuditEvents`), and export a range as CSV. Entries are
  kept 90 days by default; an owner sets 30 to 3650 days. No route edits or deletes an entry. API 0.62.0 (`listAuditEvents`,
  `exportAuditEvents`, `getAuditSettings`, `putAuditSettings`). See `docs/audit-log.md`.
- **Templates can ask a choice.** An app template may now have a *choice* between a few answers (shown as radios, or a
  select for more than four), inputs that are optional, up to 20 inputs (it was 12), and inputs, Compose lines and
  deploy settings that only one answer needs. The page of a template groups those under "Only for GitHub" and the like,
  hides the groups another choice doesn't ask for (with script; without it every input is shown and only the asked ones
  are read), and its Preview lists the secrets and the Compose file the answers make. What is typed for an input a choice
  doesn't ask for, a password included, is ignored and kept nowhere.
- **Codemagic Patch is one template again:** pick GitHub, Bitbucket Cloud or GitLab at install (in the template catalog).
- `caravana template check` renders every combination of a template's choices, with its optional inputs empty and filled.
- API 0.65.0: the template's inputs have `required`, `choices` and `when`, the type `choice`; its services' list items may
  have `when`.
- **Autoscaling.** A service may carry `scale: {min, max, target, cooldownSeconds}` (API, Compose overlay or
  Configure's Scaling panel), with one target: `cpu` (percent of its reservation), `requestsPerCopy` or `p95`
  (milliseconds; both need a domain). After each minute's sample Caravana adds one copy when the value stayed over
  the target, removes one when the load would stay well under it on one copy fewer, and waits the cooldown between
  steps, never past `min` or `max`. Each step is an app action `scale` with its counts and reason, shown in Recent
  actions, the Activity feed and Monitoring. A deploy keeps the count the service has now; `replicas` becomes the
  starting count. A service at its maximum and still over its target is on the needs-you list (`scale_at_max`).
  A service with a volume or a published port can't scale. See `docs/autoscaling.md`. API 0.47.0.
- **Several backup destinations, and a second copy of every backup.** Settings › Backup storage keeps any number of
  named S3-compatible destinations: add, edit (leave the key empty to keep it), **Test** (lists, writes and removes a
  small object from the servers), **Make default** and Remove (refused for the default and for one a service or
  project uses, naming who). A service's backups go to its policy's `destination`, else its project's default
  (project settings › Backups go to), else the installation's default, and may be copied to up to two others with
  `copyTo` and `copyKeep`, for every engine. The Backups tab has a **Where backups go…** form and says on each backup
  where it is ("in main · copied to offsite", "copying to offsite…", "deleted from main, kept in offsite"). A failed
  copy is its own needs-you item and notification. See `docs/backup-destinations.md`.
- **Restore from a place.** A restore reads the backup from its own destination or from any copy that still has it;
  **Restore from** chooses (Automatic tries them in order), so a bucket you lost doesn't lose the backup. API 0.58.0:
  `listBackupDestinations`, `putBackupDestination`, `deleteBackupDestination`, `testBackupDestination`, the policy's
  `destination`, `copyTo` and `copyKeep`, `Backup.destination` and `copies`, `Restore.destination`, the restore's
  `{from}` body and `Project.backupDestination`.
- **Change the console's address from the console.** Operator › Console address checks a new domain (its name, its DNS
  and, once Traefik has the route, a certificate for it) before anything moves, and a Switch then moves Caravana to it
  within about a minute: no SSH session, no rerun of the installer. The console never gets root: the server agent that
  runs beside Caravana rewrites the public URL and the installer's record of the domain after checking both as text,
  restarts Caravana and puts the old files back if it doesn't answer. If a Cloudflare zone is connected and has no
  record for the domain, **Create the record in the zone and check** makes it.
- **The old address keeps working for a while.** For 0, 7, 30 or 90 days (30 by default) the old domain stays routed and
  keeps its certificate: its pages redirect to the same path on the new address, and its `/v1/…` API and
  `/.well-known/…` discovery documents are still served, so CI tokens, Git webhooks and joined servers keep working
  until you update them. The page lists what holds the old address: passkeys (they belong to a domain; password
  sign-in stays on at the new one), webhook, deploy and API tokens, the workload identity issuer, and joined servers,
  which follow by themselves. See `docs/console-address.md`.
- API 0.57.0: `getConsoleAddress`, `startConsoleAddressChange`, `getConsoleAddressChange`, `cancelConsoleAddressChange`
  and `switchConsoleAddress`, with the codes `domain_not_here`, `console_address_change_open`,
  `console_address_unsupported`, `console_address_change_not_found`, `console_address_not_cancellable`,
  `console_address_not_ready` and `console_address_check_failed`. Deploy tokens are refused.
- **Email and Telegram notification channels.** Settings › Notifications can now send what needs you, and how deploys
  end, by email (through any SMTP server, with STARTTLS, TLS or, for a mail server on the same host, no encryption) and to
  a Telegram chat (through a bot you make with BotFather). They have the same events, Send a test, Pause, Edit and
  Remove as the other channels, and the same retries (after 30 seconds, then 2 minutes). The SMTP password and the bot
  token are write-only: they live only in the cluster's secret store and are never shown or logged. The API takes
  them as `email` and `password`, and `telegram` and `token`, on the notification channel calls (API 0.52.0).
- **Image scanning, an add-on.** Operator › Security installs the image scanner (Grype, pinned by digest; about
  3.5 GB of disk on one server and about 204 MB downloaded a day, nothing until you install it). Installed, it
  scans each image a healthy deploy brings and, every day, every image a running app uses, from a kept SBOM so the
  rescan pulls nothing. Findings show on the app's Deployments tab (Security panel, counts on each history entry)
  and on Operator › Security, with advisory links, fixed versions, EPSS and "known exploited". A package with a
  fixable critical (or high, a setting) is a needs-you item `image_vulnerable` and a notification. An optional gate
  refuses to deploy an image with a fixable critical (422 `image_vulnerable`). Nothing about your servers or apps
  is sent anywhere; a mirror of the database can be set. API 0.49.0 (`/v1/addons`, `/v1/scans`,
  `/v1/apps/{id}/scan`), MCP tool `get_app_scan`. See `docs/image-scanning.md`.
- **Copies survive a short network cut.** When a server stops answering, Nomad now keeps its copies running for
  five minutes (`disconnect.lostAfterSeconds`, 60 to 86400, settable per service through the API and the Compose
  overlay) instead of replacing them after about 45 seconds. A service without a volume or a published port gets
  replacements meanwhile and keeps its original copies when the server returns; a service with a volume or a
  published port waits for its server. A cut longer than the limit behaves as before. A service with a domain
  also gets a Traefik health check on its `health.path`, so the proxy stops sending to the unreachable copy
  within one interval. A service with a domain and only a TCP health check, or an HTTP check on another port,
  keeps today's behaviour, and setting `disconnect` on it is refused. See `docs/network-blips.md`. API 0.48.0.
- **A cut-off server reads "disconnected", not "down".** While Nomad keeps the copies of a server it stopped hearing,
  the console and the API (`state: "disconnected"`, `disconnectedSince`, `keptCopies`; a copy's `status:
  "disconnected"`) say so, the server takes no new work, and the needs-you list stays quiet. If the copies are lost
  after `lostAfterSeconds`, the server is down and the `server_down` item opens as before.
- **Server labels and placement rules.** Give a server labels on its page or through the API (`ssd=true`, `region=eu`),
  and say on which servers a service runs: `placement.require` (labels a server must have), `placement.prefer`
  (labels the scheduler favours, with a weight) and `placement.avoid` (server names it never runs on), through the API,
  the Compose overlay or Configure. The rules become the job's constraints and affinities (the export writes them),
  and a rule no server satisfies is refused before anything is registered, naming the field (`409 placement_impossible`).
  A service with rules whose copies can't be placed is on the needs-you list. See `docs/placement-and-drain.md`.
- **Move work off a server.** On a server's page (or `POST /v1/servers/{name}/drain`), the server stops taking new
  work and its copies move to other servers one at a time, each waiting for its replacement to pass its health checks.
  Services with a volume or a published port, Traefik and batch runs stay and are listed with their reason. The page
  follows the move; **Accept work again** (`DELETE …/drain`) lets the server take work, and cancels a move in progress.
  A move that passes its deadline with copies left is on the needs-you list. API 0.46.0.

### Changed
- **A PostgreSQL backup lists only its own objects when it keeps the newest ones.** The upload step now looks at
  files ending in the engine's extension (`.dump`) and is told it, which is what lets the three engines share one
  script. An app gets it at its next deploy.
- **The Logs page accepts a window.** `/logs?from=…&to=…` (RFC 3339) reads exactly that window, paused, which
  is where a request's row leads. The range bar returns to the live ranges.
- **The prefix `pr-` is kept for previews.** An environment name that starts with `pr-` must be `pr-<number>`.
- The console's live updates, lists and pages follow the person's role: a member's Apps, Projects, Logs, Requests,
  Monitoring and Schedules show their places only, and the buttons they can't use aren't drawn.
- **The installer drops covered certificates with Caravana's own agent.** Rerunning the installer still removes the
  ACME certificates an upload covers and says which, with the same rule as the button, and no longer needs
  `python3` for it.
- **One way to write a date in the console.** A time more than a week away (a schedule's next run, a
  certificate's expiry, when a preview closes, when a server started) now reads "Nov 29 18:11 UTC", with
  the year only when it isn't this year, like every other date on the console; it was "2026-11-29".
  The local time added beside it uses the same form.
- **The installer refuses a container.** Run inside a Docker or LXC container, or on a node of a
  container platform (Virtuozzo, Jelastic), the installer now stops before changing anything and says
  so: Caravana runs Docker and Nomad and makes network interfaces, a firewall and a private network,
  which a container doesn't allow. Install it on a VM or a physical server.
- **The console and Caravana's watchers follow Nomad's event stream.** One subscription tells Caravana
  when a copy, a rollout, a job or a server changes, so pages refresh, rollouts end and schedule runs and
  backups are recorded within moments instead of on the next poll. Every watcher still reads Nomad on a
  slow timer as a safety net, so nothing stops working if the stream is down: Caravana logs that it lost
  the stream once, reconnects by itself and looks at everything again when it is back. The
  all-allocations query that ran while a page was open is gone.

### Fixed
- **A server with cloud-init boots cleanly again.** Caravana's private network unit no longer forms an
  ordering loop with cloud-init at boot, which made systemd drop a job to break it (on some boots ssh,
  leaving the server unreachable) and left the system degraded. Rerun the installer to get the new unit;
  it takes effect at the next boot.
- **No more 502s while a new copy starts outside a deploy.** Nomad registers a copy before it listens, and a copy
  placed by an autoscale step, Move work off or a reschedule isn't hidden the way a deploy hides its new copies,
  so the proxy sent it a few requests too early. Every router now retries a connection that failed on the next
  copy. Only a request that never reached the copy is retried, so a POST is never sent twice.
- A data directory whose path holds `#`, `?`, `%` or a space no longer makes the database open at a shorter path
  (SQLite read the rest as a fragment or a query). The path is escaped when the store opens.
- A terminal opened through the API whose first line is typed at once (a script) now runs it at the
  size asked for with `cols` and `rows`, not at 80x24: Caravana passes nothing typed on to Nomad
  before the starting size.

### Security
- In a preview (`pr-<n>`), a person with only the deploy role, or a deploy token, may use only the secret references
  (a `from` or a template's refs: provider and path) that the app of the same name already has in the project's other
  environments. Any other reference is `403 not_allowed`, naming it; maintain may add one. `docs/people.md` now also
  says plainly what roles don't protect: deploy rights on an app are in effect access to its secrets, view includes
  logs, and a secret provider's allowed places are a security boundary.

### Upgrade
- Rerun the installer once on an existing host: Traefik's static configuration is now a file its job renders, so
  Traefik restarts once (a few seconds without HTTP). Until then Settings › Certificates asks for the rerun. A later run
  also drops the certificates Let's Encrypt issued for names an uploaded certificate covers, since Traefik would keep
  serving those.
- Rerun the installer on every server so its agent reports its images (it does so from its next sync). Until then
  the server's Space section says it has no agent reporting its images.
- Rerun the installer on the first server: Traefik's job now writes its access log, so Traefik restarts once
  (a short gap on ports 80 and 443). Until then the Requests page says "Traefik isn't writing its access log
  yet" and the API answers `accessLog: false`.
- Nothing to rerun: the new columns are added when Caravana starts, and the Nomad policy is unchanged. Previews need caravana-action with preview mode, and one wildcard DNS
  record for the preview addresses.
- Nothing to do on a host beyond rerunning the installer as usual: builds use the jobs, variables (`nomad/jobs/*`,
  `caravana/*`), host volumes and file reads Caravana's Nomad policy already allows. The first build pulls the Git and
  BuildKit images (about 100 MB), and an image to push to needs a registry entry whose credentials may push.
- The one admin becomes the **owner** (same email, password, passkeys, sessions and tokens). Rerunning the
  installer is all: nothing else to rerun on the host. Records made before have no `actor`.
- Rerun the installer on a server to get the numbers for it: an older agent reports no I/O, and the page says so.
- The button needs the first server's agent to be up to date: rerun the installer on that server once. Until its
  agent reports that it holds Traefik's certificate store, the button answers that the installer must be run.
- Migration 0045 adds the log's two tables when Caravana starts. There is nothing to rerun and nothing to configure.
  Changes made before the upgrade aren't in the log.
- Older Caravana versions keep reading the catalog's `catalog.json`, which stays in the format they know, so their
  template list keeps updating; templates that need the new format are offered to this version and newer ones, and a
  template made for a newer version than yours is listed as unavailable instead of stopping the catalog.
- The destination you have becomes `main`, the default, with its key where it was: nothing to do, and every backup
  and restore recorded so far says `main`.
- Rerun the installer once on an existing host before the first change: Traefik's console route now reads its hosts
  from a Nomad Variable, so Traefik restarts once (a few seconds without HTTP). Until then the page asks for the rerun.
  The agent that applies the switch is the one servers already use, so nothing else is installed.
- Apps get the disconnect policy and the Traefik health check at their next deploy; running apps don't change
  until then.
- Nothing to do: the token's existing `read-job` and `node` rules cover the stream's topics.
- Rerun the installer on the first server: Caravana's Nomad policy gains `node:write` (node labels, eligibility).
  Until you do, saving a label or moving work off says Nomad refused it.
- Each app picks the retry up at its next deploy; no installer rerun is needed.

## 0.22.0 - 2026-11-17

### Added
- **Published ports.** A service can publish a port: a fixed TCP or UDP port on your server's public address, for apps
  that aren't HTTP (a TURN server, LiveKit, a game server, MQTT). Set `published` and `protocol` (`tcp`, `udp` or
  `tcp+udp`) on a port through the API, or publish a port of a Compose file with the overlay's `publish` list (the
  file's own `published` numbers still only warn). The port is open while the copy runs and closed when it stops or the
  app is deleted; no firewall rule is written. Such a service runs one copy, and its deploys stop the old copy before
  starting the new one. Reserved ports (22, 80, 443, Nomad's, Caravana's and the mesh's), a port another app publishes
  (`409 port_taken`) and a health check or domain on a UDP-only port are refused naming the field. A promotion keeps
  the target's numbers and Copy to leaves them out. Configure shows them read-only. API 0.45.0. See
  `docs/published-ports.md`.
- **A schedule that runs in the running copy.** A schedule can now say where its command runs:
  `target: copy` runs it with Nomad's exec in the service's oldest healthy copy (or in every healthy
  copy, one after the other, with `allCopies: true`) instead of in a fresh one. It works on a service
  with volumes too, which a fresh copy still can't (two writers on one volume would be unsafe): a
  cleanup command, a cache purge, a nightly reset of a demo. The limit is 1, 2 or 5 minutes (1 by
  default), it has the copy's own memory and always skips a slot while its last run goes. Like Run a
  command, it keeps **no output**: a run records its status, exit code, why it failed and the copies
  it ran in, and each copy gets a `run` entry in the app's Recent actions "on a schedule". A failed
  run is a needs-you item and a notification, as before. While the app is stopped a slot is recorded
  as skipped ("the app is stopped"), and a slot with no healthy copy waits for one. In the console,
  New schedule has "Run in: A new copy / The running copy" and "In every copy", and a run's page says
  where it ran and that its output isn't kept.
- API 0.43.0: `target` and `allCopies` on `NewSchedule` and `Schedule`, `target` and `copies` on
  `ScheduleRun`, `schedule` as an app action's trigger, 404 `output_not_kept` for a run in the running
  copy, and 409 `no_running_copy` from `runSchedule`.
- **DNS servers, search domains and options per service.** A service can now set the resolvers its
  copies use: up to 3 nameserver IP addresses, up to 6 search domains and up to 8 resolver options
  such as `ndots:2`. Nomad writes them to the container's `resolv.conf`, and a schedule's fresh
  copies get the same ones. Set them in the API (`dns` on a service) or in a Compose file with `dns`,
  `dns_search` and `dns_opt`. Configure shows them read-only, and a promotion takes the source's. API 0.44.0.

### Changed
- A Compose file's `ports` and `expose` now accept UDP (`"3478:3478/udp"`, `3478/udp`); a UDP port was refused before.
  The warning for a file's `published` port now says how to publish it.
- **The sidebar shows Caravana's logo** where the server's initial was, beside the server's name and
  Caravana's version. It follows the light and dark themes like the rest of the console.

### Upgrade
- Rerun the installer on the first server so its Nomad declares the host network that carries published ports (Nomad
  restarts once). Until you do, a deploy that publishes a port is refused with `409 no_public_network`.

## 0.21.0 - 2026-11-15

### Added
- **App templates.** New app has a fourth tab, **From a template**: a grid of known apps you can search
  and filter by tag. Pick one, type its domain (and whatever else it asks for), and Caravana renders its
  Compose file, makes the passwords it needs, and deploys it like any other app. The first six are Uptime
  Kuma, Hollo (with its Postgres), PostgreSQL with Adminer, Adminer, Umbral and Codemagic Patch. The
  passwords Caravana makes go to the app's secret store and are never shown, stored or logged. A
  **Preview** button shows the app the answers would make, and nothing is created.
- **Install without backups, when you choose it.** A template with a backup is refused until a backup
  destination is set, as any deploy is. On a new installation the page says so, links to Settings ›
  Backup storage and offers "Install without backups for now"; the deployment then warns once per service
  that its backup is off and how to turn it on.
- **The catalog lives in its own repository** (`Cosmic-Chimps/caravana-templates`) and each Caravana
  release embeds a copy. Once a day Caravana asks the repository's latest release for a newer catalog
  (`SHA256SUMS` first, `catalog.json` only if its sum changed, both checked, every template run through
  Caravana's own checks). The request carries Caravana's User-Agent and nothing about your server. Turn it
  off under the grid ("Stop checking for new templates") and Caravana makes no request about templates at
  all.
- **Apps remember their template.** An app installed from one names it (Configure shows "Template ·
  Hollo 0.9.25-1") and its Deployments tab shows the template's notes after the install. It is a normal app
  afterwards: edit its Compose file, promote it, export it, delete it.
- `caravana template check <dir>` and `caravana template bundle [--version <v>] <dir> <outdir>` are the
  catalog repository's gate and release step.
- API 0.37.0: `listTemplates`, `getTemplate`, `getTemplateLogo`, `previewTemplate`, `installTemplate`
  (`withoutBackups` in the body), `checkTemplates` and `putTemplateSettings`; `AppRecord.template` and
  `AppSummary.template`; codes `template_not_found` and `template_unavailable`. Deploy tokens can't use
  any of them. `caravana mcp` gains the read tools `list_templates` and `get_template` (there is no
  install tool: it would carry secret answers through an agent).
- `CARAVANA_TEMPLATES_URL` (`-templates-url`) points the catalog check somewhere else; it is for tests and
  the lab.
- **A backup that only runs when you ask.** A service's backup policy can now say `schedule: never`
  (in the API, the Compose overlay and the Compose form), for every engine: Caravana takes no backup by
  itself, and Back up now, pinning a backup, the backups you take by hand (kept as many as `keep`) and a
  scheduled restore of a pinned backup all work as before. Nothing on the needs-you list or in the
  notifications complains that no scheduled backup ran. The console says "No automatic backups (Back up
  now only)" on the Backups tab, Configure and Schedules, and the Backups tab's "Back up" forms have a
  Schedule choice. An empty schedule still means every day at 03:00 UTC, and `never` is refused as a
  restore schedule.
- API 0.42.0: `BackupPolicy.schedule` accepts `never`.
- **Caravana installs on Ubuntu 26.04 LTS, Debian 12 and Debian 13**, besides Ubuntu 24.04 LTS, on amd64 and
  arm64: the same installer and the same packages, for the first server and for one that joins. Any other
  system is refused before anything changes, naming the four supported ones. Each of them is tested by
  `deploy/dev/install-test.sh` (`IMAGE=debian-12`); `deploy/dev/install-test-all.sh` runs all four.
- **Caravana has a logo.** The console now has a tab icon, so caravana's tabs are easy to find among
  many, and the mark (four shapes on one path) shows above the wordmark on the sign-in page and message
  pages and in the narrow top bar. It also takes the colors of the light or dark theme.
- **Sign in with a passkey.** Settings › Sign-in lists the passkeys you have, adds one (after you
  confirm it's you with your password or a passkey you already have, or by having just signed in),
  renames and removes them. On the sign-in page, "Sign in with a passkey" asks for your fingerprint,
  face or device PIN and signs you in without typing an email. A passkey belongs to the domain it was
  made at, so it works only there: it is offered at the console's https domain and at `localhost`,
  never at an IP address. Caravana keeps only the passkey's public key and a counter.
- **Passkeys only.** Once a passkey exists for the console's domain, you can turn password sign-in
  off. The password form then refuses without checking the password, and the last passkey of a domain
  can't be removed while it is off. If the console's domain changes, the password works again at the
  new domain until you add a passkey there, so a move never locks you out.
- **On the host.** `caravana admin passkeys list` and `caravana admin passkeys remove --id <id>`.
  `caravana admin reset-password` now also turns password sign-in back on (passkeys are kept), and
  `caravana admin status` says how you sign in.
- Failed passkey sign-ins count against the same per-address limit as wrong passwords (5 in 15
  minutes).
- API 0.36.0: `listPasskeys`, `updatePasskey`, `deletePasskey`, `getSignIn` and `putSignIn` under
  `/v1/account/`. Adding a passkey and signing in with one are console routes only: a passkey is
  bound to the console's own origin and to a person's device unlock, which an API client has neither.
- **Registry credentials per path.** An entry in Settings › Registries may now name a host and a path,
  like `ghcr.io/cosmic-chimps` and `ghcr.io/aerosens`, so two accounts that no single login can read
  both work on one registry. An image uses the most specific entry whose path covers it by whole
  segments (`ghcr.io/acme` covers `acme/shop`, never `acme-old/shop`). An entry with no path stays the
  fallback for every other image on that host, and an image no entry covers is pulled without
  credentials. Nothing is chosen per app: the image's own name says which entry applies, so Compose,
  CI, MCP and the Aspire publisher need no change.
- **Saving or removing an entry rewrites only the copies that use it.** Adding a prefix moves the
  apps under it off the host's entry, and removing one moves them back (or, with no host entry, leaves
  empty credentials, as before).
- **Test checks the login only.** A registry doesn't say which images a login can read, so for an
  entry with a path an accepted test says that the first deploy of an image under it shows that.
- API 0.35.0: `putRegistry`, `deleteRegistry` and `testRegistry` take an optional `path` query next to
  the host in the URL, and `Registry` has `path`.
- **Pin a backup.** A backup can be pinned under a label such as `golden`, from "Pin…" on its row in
  the Backups tab or with `PUT /v1/apps/{id}/backups/{backupId}/pin`. Retention never deletes a
  pinned backup, neither its record nor its file in the destination, and only the unpinned ones count
  against how many to keep ("7 backups, plus 1 pinned"). A label is on one backup of a service at a
  time: pinning it again moves it. This holds for PostgreSQL, volume and SQLite backups.
- **Restore a pinned backup on a schedule.** A service's backup policy may have
  `restore: {schedule: "0 4 * * *", from: golden}`, or "Restore on a schedule…" on the Backups tab, or
  the two fields of the Compose form. At each time (cron, UTC) Caravana restores the backup pinned
  under that label, as the Restore button does, with no token: the clock is inside Caravana. It is
  meant for a demo that starts the day from the same state. A time that comes while a backup or
  restore is running is skipped, and one missed while Caravana was down runs once if it is less than
  an hour late. A label with no backup pinned, or a restore that fails, is a failed restore on the
  needs-you list and in the notifications. Restores now say who started them: you, or the schedule.
- **A PostgreSQL restore is one transaction**, so a restore that fails partway leaves the database as
  it was.
- API 0.39.0: `pinBackup` and `unpinBackup`, `pin` and `pinnedAt` on a backup, `trigger`, `pin` and the
  status `skipped` on a restore, and `restore` in a backup policy.
- **Secrets from Bella Baxter.** A secret can name where its value lives instead of holding it:
  `{"name": "DATABASE_URL", "from": {"provider": "bella-prod", "path": "DATABASE_URL"}}`, or "From a
  provider" on the Variables page. At each deploy Caravana reads the value from Bella and puts it in the
  service's Nomad Variable, as it does with a typed value, so the app never talks to Bella and a restart
  never waits on it. The console shows where a value comes from, never the value.
- **Settings › Secret providers.** Connect a Bella Baxter environment with its URL, project, environment,
  an API key made for Caravana and the private key Bella showed with it, limit it to some projects or
  environments, and Test it ("12 secrets readable"). The keys stay in Nomad Variables.
- **Refresh secrets.** After a value changes in Bella, "Refresh secrets" on the Variables page (or
  `POST /v1/apps/{id}/secrets/refresh`) reads every reference again and rolls the running version out
  with the new values, same images; when nothing changed it says so and deploys nothing. An app's deploy
  token may call it.
- A deploy whose reference can't be read fails and names the secret and the provider: not in Bella (422
  `secret_not_found`), Bella refusing the keys (422 `secret_provider_refused`) or Bella down (502
  `secret_provider_unavailable`). Running copies keep the values they have.
- API 0.40.0: `from` on a secret, the provider operations (`listSecretProviders`, `putSecretProvider`,
  `deleteSecretProvider`, `testSecretProvider`), `refreshSecrets` and `secretsRefreshed` on a deployment.
- **Workload identities.** A service may ask for up to 4 Nomad workload identities,
  `identities: [{name: bella, audience: ["bella-spiffe/<environment-id>"], ttlSeconds: 3600}]`, through the
  API, the Compose overlay or Aspire's publisher. Each copy finds a token the cluster signed in
  `/secrets/nomad_<name>.jwt`, which Nomad renews at about half its lifetime; the app shows it to another
  system (Bella Baxter's Nomad attestor, for one) instead of a stored password. Caravana never reads the
  token. Configure shows a service's identities, read-only. A promotion keeps the identities of the
  environment it promotes into. See `docs/workload-identity.md`.
- **The cluster's issuer.** On a host with an `https` console address, every Nomad server signs identities
  with that address as their issuer, and Caravana answers `/.well-known/openid-configuration` and
  `/.well-known/jwks.json` there with Nomad's own documents, so a receiver can check a token. One issuer
  covers every server of the cluster.
- API 0.41.0: `identities` on a service and in the Compose overlay (schema `Identity`), and the two
  discovery documents.

### Changed
- **Updates on a Debian server**: the Operator › Updates page counts the waiting updates with
  `apt list --upgradable` (read inside Caravana's sandbox, never applied) on a host that has no
  `updates-available` file, and names the server's system in its words ("Debian updates"). Package names on
  demand still come from Ubuntu's `pro`, so a Debian server says it can't list them.
- In the console, a registry row's Test and Remove post the entry in a form field
  (`/settings/registries/test` and `/settings/registries/remove`) instead of putting the host in the
  address; "used by" counts the apps each entry covers.
- Redeploying an old version reads its provider references again now: it gets today's values from
  Bella, not the ones it had.
- **A backed-up Postgres service warns about a plain password.** When a service with a `postgres`
  backup gets `POSTGRES_PASSWORD` from its environment instead of a secret, the deploy still works
  and now warns on that field: a plain value shows in the app's definition, its Nomad job and
  exports. Make it a secret to keep it out of all three.

### Fixed
- The API answered `"keep": 0` for a backup that left `keep` out, and `"port": 0` for a Compose overlay that
  left its health port out, which broke the contract's minimums for clients that validate answers. A zero
  is left out now.
- **A down server's times on Operator › Overview** no longer add a date to a time from today. The
  page compared the UTC time with the date in the server's local time zone, so in the hour after
  local midnight east of UTC it said "Nov 12, 23:16 UTC" for a time from the same UTC day.
- A SQLite backup or restore is no longer recorded as failed ("the size of the copy is unreadable") when
  Nomad reports a step's end before its output arrives. Each step also writes its result to a file in the
  copy, and Caravana reads that file when the output is missing.

### Upgrade
- Nothing to do. Migration 0028 adds the template columns and the catalog's one row; apps made before it
  have no template.
- Nothing to do: a new migration (0026) adds the passkeys table and two columns. Sessions that were
  open before the upgrade have not confirmed anything yet, so adding a passkey from one asks you to
  confirm it's you first.
- Nothing to do: every existing entry is a host-only entry, with the same stored credentials and the
  same Nomad Variable. A new migration (0025) adds the path column.
- Apps with a PostgreSQL, volume or SQLite backup get the retention that spares pinned backups at
  their next deploy. Redeploy them before pinning anything, or an old backup may still be deleted by
  the previous retention. Nothing to rerun on the host.
- After the update, each Nomad server restarts once, one at a time, to take the issuer; running apps keep
  running. Nothing to rerun on the host.
- Changing the console's domain later changes the issuer: tell every system that trusts your cluster.

## 0.20.0 - 2026-11-11

### Added
- **Add a server.** Operator › Servers lists every server with its CPU, memory and disk, and **Add
  server** makes a command to run on a new Ubuntu 24.04 machine. Its token works once and for one hour.
  The page shows the checks as they pass: connected, Docker running with its free disk, reaches the other
  servers on the private network, ready to take work. The new server installs Docker, Nomad and WireGuard
  and runs no Caravana service and no Traefik: it takes copies, and public traffic still enters through
  the first server.
- **A private network between servers.** Servers form a WireGuard mesh (`caravana-mesh`, UDP 51820).
  Nomad's own traffic, the apps' ports and the containers' addresses travel on it, so Traefik reaches a
  copy on any server and the services of an app reach each other by name across servers.
- **Nomad's traffic between servers is authenticated and encrypted**, from the first install: TLS on its
  RPC with certificates Caravana issues for each server's own key, and an encrypted gossip. A small root
  agent on every server (`caravana-node`, on a 30-second timer) keeps the server's peers, role and
  certificate in line with the console, and renews the certificate 30 days before it ends without
  restarting Nomad. The console still can't run anything as root: the agent applies values it checks and
  takes no command or path from the console.
- **Three servers survive the loss of one.** With three servers ready, the two oldest joined ones run
  Nomad's scheduler too, so it has three voters. A service with more than one copy has its copies on
  different servers. When a server is lost its copies are replaced on the others, the Servers page shows
  it as down, and it is on the needs-you list and sent to every notification channel. Losing the first
  server keeps the scheduler and the other servers' copies running, but the console and public traffic are
  down until it is back: both live there.
- **Remove a server** that is down, or cancel a join nobody ran, from its page. A server that is running
  is taken out with `install.sh --leave` on it first.
- Each server has its own page, numbers and disk warning; the Running tab names the server of each copy,
  Volumes say which server they are on, and Monitoring's host section has a server select.
- API 0.34.0: `listServers`, `getServer`, `addServer`, `getServerJoin`, `removeServer`, the agent's calls
  (`joinServer`, `syncNode`, `renewNodeCertificate`, with credentials of their own), `getHost?server=`,
  `server` on volumes and on the disk item, and the needs-you kinds `server_down` and
  `server_certificate_expiring`.

### Changed
- All volumes of one service are created on the same server. Before, with more than one server, Nomad
  could have placed them on different ones and the service could never start.
- `caravana <a word it doesn't know>` prints the usage and exits 2. It used to start the server.

### Fixed
- A restart of Nomad (an installer run that changes Nomad) could replace running copies: systemd gave a
  container's network interface a new address just after it was made, and Nomad, checking each copy
  after its restart, no longer knew it. The installer now tells systemd to leave those addresses alone.
  Copies started before that can still be replaced once, by the next Nomad restart.

### Upgrade
- Rerun the installer on your server. It sets up the private network's interface and Nomad's
  certificates, and Nomad restarts twice, once only (the second restart moves its raft address to the
  private network). Apps keep running, but Nomad may replace a copy whose network it can't verify after
  the restart: an app with one copy may not answer for a few seconds. Until then, Add server says to run
  the installer first.
- To add servers, open UDP 51820 between them at your provider's firewall. The ranges `10.97.0.0/16`,
  `10.98.0.0/24` and `10.99.0.0/24` must be free on every server.

## 0.19.0 - 2026-11-07

### Added
- **SQLite backups that don't stop the app.** A service that keeps its data in one SQLite file on a
  volume can be backed up with the `sqlite` engine: `backup: {engine: sqlite, file: /data/app.db}` in
  its definition or Compose overlay, **SQLite file** in the Compose form, or **A SQLite file instead** on
  the Backups tab. Each backup is a consistent copy made with `VACUUM INTO` while the service keeps
  running, checked with `PRAGMA quick_check`, gzipped and uploaded as `<time>.sqlite.gz` to the backup
  destination, with the same schedule, retention, Back up now, needs-you items and notifications as the
  other engines. A restore downloads and checks the backup while the service runs, then stops it only for
  the few seconds it takes to put the file in place, keeping the file's owner and mode. In WAL mode the
  copy never blocks the app's writes; with a rollback journal they wait while it reads. A service with a
  sqlite backup runs two small idle helper tasks beside it (`caravana-sqlite` and `caravana-upload`,
  48 MB reserved together). API 0.32.0 (`BackupPolicy.file`, the `sqlite` engine).
- **Update Caravana from the console.** When a newer release is out, Operator › Updates has **Update to
  X…**: a confirmation page says what restarts (the console for about a minute, and Nomad or Traefik when
  the release changes their versions; apps keep running), and the update starts within a minute. The page
  follows it while the console restarts and says how it went: updated, rolled back with the reason, or
  failed. A rolled-back or failed update is on the needs-you list and sent to every notification channel.
  The console still can't run anything as root: it only writes a request holding a version, and a root
  timer from the installer (`caravana-update.timer`) checks the version, downloads that release's
  installer and binary, verifies them against the release's checksums and runs the installer, which puts
  the old version and its data back if the new one fails its health check. **Automatic caravana updates**
  (on Updates, off by default) install patch releases (0.18.x) at a set UTC hour, and never retry a version
  that failed. API 0.33.0 (`requestUpdate`, `listUpdateRuns`, the updater's state and the automatic
  setting on `getUpdates` and `putUpdateSettings`, the needs-you kind `caravana_update_failed`).

### Upgrade
- Every app with a volume gets an updated `caravana-volumes` task on its next deploy;
  an app needs one deploy with this version before its first sqlite restore (setting the policy is one).
  `CARAVANA_SQLITE_IMAGE` (`-sqlite-image`) replaces the pinned `alpine/sqlite` image if you mirror
  images.
- To update from the console, rerun the installer of this release once on the server: it adds the updater
  (`/usr/local/libexec/caravana-update`, `caravana-update.service` and `.timer`, and
  `/var/lib/caravana-update`). Until then the Updates page says to.

## 0.18.0 - 2026-11-05

### Fixed
- **Deploying an app's Compose file from the console keeps its domains' options.** Saving the file on
  Configure (or Deploy from Compose of an app that exists) dropped each domain's path, www redirect and
  Team only, since the form's Domains box held host names only. The form of an existing app has no
  Domains box now: a deploy from it keeps every domain as it is, and domains are added and changed on the
  app's Domains page, which also works for an app that keeps its Compose file. Configure lists each
  domain once, in its Domains panel, with Add domain. A file that removes a service names the domains
  that stop answering. New app › Compose file still takes its domains in the form.

## 0.17.0 - 2026-11-04

### Added
- **A light theme.** The console follows the browser's choice: dark, as before, unless the browser
  prefers light. **Settings › This browser › Appearance** forces one (Match the system, Light, Dark),
  kept in a cookie of that browser only, so it holds on the sign-in page and after sign-out. It is the
  same look with the same rules: status is the only colour, every status keeps its shape, depth is
  distance from the panel face. The terminal page's terminal stays dark in both. Two things read a shade
  darker in both themes on the way: a field's placeholder and the Logs level chip that is off are Label Text
  now, and the terminal's box has no black band at its edges.
- **A backup run says who started it.** A run started with Back up now reads "by you" on the Backups tab,
  and `owner` as its `trigger` in the API (`Backup.trigger`, and the runs of a built-in schedule); a run its
  schedule started reads `timer`. Runs from before this release read `timer`. API 0.31.0.

### Security
- **An ntfy topic URL is a secret now**: it lives in the channel's Nomad Variable like a Slack or Discord URL,
  SQLite and the API keep only its host (`ntfy.sh/••••`), and a channel from before moves there on its first
  listing or send. API 0.30.0.

### Changed
- **The project page leads with the apps**: one tab per environment, the environment's apps as a table
  (status, copies, address, version and when; problems first), and a rail with how the environment was last
  deployed, what needs you there, the apps that can be promoted and its newest deploys. The display name,
  environments and Delete project moved to **Project settings** (`/projects/<project>/settings`).
- **The sidebar's order**: Home, Projects, Logs, Monitoring and Schedules under Workspace, Domains under a new
  Configure group.

### Fixed
- **A domain that points here and at another address is "DNS points elsewhere" again, even with a valid
  certificate.** An old A record left beside the new one, or an AAAA record to another server, sends some
  visitors to that host, and a proxy never answers with the origin's own address. The row, its banner and
  the needs-you item name the record to remove. A domain whose addresses are all a proxy's (or none this
  host's) stays live by its certificate.

## 0.16.0 - 2026-11-01

### Added
- **The Operator view**: a Developer/Operator toggle under the brand in the sidebar, and three
  pages about the host Caravana runs on. **Overview** (`/ops`) has the server panel (size, OS,
  kernel, CPU, memory and disk meters, every running copy), every app across projects and
  environments, the needs-you list and a feed of what happened lately in Caravana's own words.
  The **server page** (`/ops/servers/<name>`) has its state, numbers, the copies running on it,
  its facts and recent events, including boots and Caravana upgrades. **Updates** (`/ops/updates`)
  shows a newer Caravana (with its release notes and the command to run), the OS updates and the
  pending reboot read from files on the host, how to apply each by hand, and past updates. It
  reads and never applies: nothing here installs, restarts or reboots.
- **API 0.29.0**: `getHost` gains a `node` block, `listActivity`, `getUpdates`,
  `putUpdateSettings` and `checkUpdates`, the needs-you kinds `os_reboot_required` and
  `os_security_updates`, and the `caravana.release` notification (one message per new release,
  to every channel that isn't paused).
- **A stop timeout per service** (API 0.29.0): `stopTimeout` (1 to 30 seconds), how long Nomad
  waits after the stop signal before it kills the container, on a deploy, a restart, a stop and a
  volume backup.
  Unset is 30 for a service with a volume and Nomad's 5 otherwise. Compose's `stop_grace_period`
  sets it, a promotion takes the source's, and Configure shows it as "Stop timeout".

### Changed
- **A service with a volume now gets 30 seconds to stop** (before: Nomad's 5). A volume backup
  stops the service, and an in-process database such as Uptime Kuma 2's embedded MariaDB that is
  killed mid-shutdown leaves a crash-consistent archive and runs crash recovery at every start.
  The cost: a service that ignores the stop signal (a `sh -c` wrapper that doesn't pass it on)
  now takes up to 30 s to stop on each deploy and backup. Set `stopTimeout` lower for such a
  service.
- **Compose's `stop_grace_period` is honoured**, rounded up to whole seconds, instead of ignored
  with a warning. More than 30 s is refused, naming the field: Nomad clients cap a task's kill
  timeout at 30 s (`max_kill_timeout`).
- A copy is "starting" until Nomad decides its health, in a rollout and outside one, instead of
  showing as healthy for a moment.
- The needs-you wording has one source, so the home, the Operator pages, the feed and the
  notifications say the same words, with names, versions and kernels in mono.
- vCPU values have two decimals, and whole totals are whole.
- Dates read "Oct 22". The local time leaves out its date when it is the UTC date.
- The sidebar's nav marks wear their hue.

### Upgrade
- Redeploy each app that has a volume once, so its job gets the stop timeout (the running job
  keeps Nomad's 5 s until then). For Uptime Kuma 2 (embedded MariaDB) that is enough; or set
  `stop_grace_period: 30s` in its Compose file. Then check that the next start's log has no
  "crash recovery".
- For the Operator view, nothing to rerun: no new Nomad capability and no installer change. The
  store gains four small tables when the new version starts.
- The release check is on by default and can be turned off on Updates. It fetches only GitHub's
  latest release and its files (at most once a day, plus Check now) and sends nothing about the
  server. Ubuntu's `pro` command runs only when you ask for package names.

## 0.15.0 - 2026-10-30

### Added
- **API 0.28.0**: `Deployment.registeredAt` (the time Nomad accepted the job, `null` until it did),
  the time the console splits a deploy's stages on. `AppStatus.state` now says what each of its
  six states means.

### Changed
- **Configure of an app deployed from a Compose file** says why it looks different: Caravana keeps
  the file, so the app is edited through it, while an app deployed from an image is edited field by
  field. When the last deploy came from CI, it adds that a deploy from CI can replace changes made
  here, so lasting changes belong in the source.
- **Secrets in the Compose form** (New app, Deploy from Compose, Configure) are one row each: the
  name at the left, its input at the right, so a long name such as
  `Frunchy__Auth__Providers__Umbral__ClientSecret` no longer runs into the next one. "Leave a value
  empty to keep it." is said once. A new secret's name and value sit side by side.
- **MCP**: `deploy_image`, `promote_app` and `redeploy_version` are marked destructive, so agent
  clients ask before running them. `docs/mcp.md` now says what bounds an agent: the server offers
  no action tools without `--allow-actions`, but the token is the real limit, since an agent with
  a shell can read the token file. Use a deploy token scoped to one app. It also warns that deploy
  names and messages and env values are text someone else may control.
- An upgrade that rolls back restores Caravana's binary and data only. Nomad's policies, Caravana's
  settings and its systemd unit stay at the new version, which the older version tolerates
  (policies only ever add capabilities).

### Fixed
- **Log lines written before a copy's last restart keep their own times.** The Logs page took a
  copy's latest start as the earliest a line of its first log file could have been written, but a
  restarted task keeps appending to the same file, so every line from before the restart was
  refused and given an estimated time near the restart: an app restarted nightly by its volume
  backup showed days-old failures as today's. The lower bound is now when Nomad made the copy,
  which a restart doesn't move. The API's `Copy.startedAt` is documented as it always was: the
  tasks' latest start.
- **The installer validates and quotes its options.** `--email` and `--acme-email` must be plain
  addresses and `--domain` a host name, or it refuses, naming the option, before changing anything.
  The values it writes into `install.env` and `caravana.env` are quoted, and the installer reads
  `install.env` as text instead of sourcing it.
- An exported jobspec says in its header that it is rendered with this Caravana's current settings
  (entrypoint, certificate resolver, default memory and CPU, helper images), so an old
  deployment's export can differ from the job that ran if those settings changed since.
- Two console reads that failed without a word (the Deploying now card's redeploy source, and the
  history behind "(v0 again)") now log a warning. The "form too large" hint says to shorten the
  longest field.

## 0.14.0 - 2026-10-27

### Fixed
- **Domains behind Cloudflare's proxy** (or another proxy or CDN) with a valid certificate are
  live, not "DNS points elsewhere". Their DNS answers the proxy's addresses, not the host's, but a
  trusted certificate from Traefik proves the issuer reached this host, so the certificate now
  decides: live, or "certificate expiring". The DNS line says "· Cloudflare's proxy" (or "· a proxy
  or CDN in front of this host") as information, and the Add panel shows a Cloudflare address as a
  warning that explains the origin, not as a failure. The needs-you items and notifications for
  such domains clear by themselves at the next check (every 15 minutes at most), with one
  "resolved" each. A domain with no valid certificate yet still reads "DNS points elsewhere".

## 0.13.0 - 2026-10-26

### Added
- **Schedules**: a command on a timer in a fresh copy of an app. A schedule names an app and one
  of its services, a command (no shell implied: use `sh -c '…'`), when it runs (words such as
  "every day at 03:00" or "weekdays at 09:00", or a cron line), a stop-after limit (5 min to
  2 h), the memory of the run, and whether a run is skipped while the last one is still going.
  Each run starts a copy from the service's newest healthy deployment (the same image, variables
  and secret values), receives no traffic, and ends with the command; it is recorded with its
  exit code and the last 64 KiB of each output stream, with the service's secret values masked.
  Run now, Stop this run, Pause and Resume, Edit and Delete. A failed run (an exit code, the
  limit, couldn't start) is on the needs-you list and goes to the notification channels as a
  problem; three skipped runs in a row are a warning. Each service's backup appears in the same
  list as a built-in schedule. A service that mounts a volume can't have a schedule (its volume
  allows one copy). The Schedules page is in the sidebar; an app's Configure page lists its
  schedules.
- API 0.24.0: `listSchedules`, `createSchedule`, `getSchedule`, `updateSchedule`,
  `deleteSchedule`, `runSchedule`, `pauseSchedule`, `resumeSchedule`, `listScheduleRuns`,
  `getScheduleRun`, `stopScheduleRun` and `getScheduleRunOutput` (`text/plain`, never cached);
  `AttentionItem.kind` gains `schedule_failed` and `schedule_skipping` with `schedule` and
  `scheduleId`; the notification event gains `schedule` and `exitCode`, and documents `origin`,
  which it has sent since 0.8.1. Deploy tokens can't use any of it.
- Every clock time the console shows (a schedule's next run, a run's start, a backup's time, a
  deployment's Live time) reads in UTC with the viewer's local time beside it ("14:15 UTC ·
  16:15 local"), added in the browser; without JavaScript the UTC time stands. Schedules and
  backups are written in UTC; there is no timezone setting.

- **Domains**: every address your apps answer to, with its state, on **Domains** in the sidebar
  and on each app's Domains page. A row says where the address goes (the app, its service and
  port, the environment) and one of: live, getting its certificate, DNS points elsewhere (with the
  address seen and the one expected), no DNS record yet, certificate expiring and not renewed,
  served with Traefik's own certificate, a redirect, not served (the app is stopped or has no
  healthy deployment) or couldn't check. The state comes from Caravana's own checks: the name
  asked of the system's resolver and of two public ones, and a TLS handshake with Traefik for that
  name, so it reads the certificate Traefik really serves. Caravana checks every address every 15
  minutes, and every minute while a Domains page is open; **Check now** asks again at once.
  Opening a row says what the state means and what to do.
- A domain's options are switches in its row: **Team only** (HTTP basic auth, user `team`; Caravana
  makes the password and shows it once, **New password** replaces it with no deploy), **Redirect
  www** (the other of `www.host` and `host` sends visitors to this one, keeping the path and
  query) and a **path** (`/api`: only that prefix goes to the app). **Force HTTPS** is on for every
  domain and says so. Each option you change is a deploy of the app, like any change to its
  definition; an app deployed from a Compose file keeps its file, and the page edits the overlay's
  domains. Team only's password hash is kept in SQLite and handed to Traefik through a Nomad
  Variable; no job, export, record or log holds the password or its hash.
- **Add a domain** shows the DNS record to create (an A record to this host) and
  **Check now** before you save: whether the name already points here, and what certificate it will
  get. An address another app already uses is refused (409 `domain_taken`). Configure's Deploy
  panel has **Domains go to** when a service has several ports.
- Two needs-you items and notifications: **DNS points elsewhere** (a domain whose address is now
  somewhere else, with the address seen) and **Certificate expiring** (not renewed inside the warn
  window), each opened and resolved like any problem and pushed to every notification channel.
- **Settings › Certificates**: every certificate Traefik holds for your addresses (issuer, when it
  renews, when it ends), who issues them (Let's Encrypt production or staging, or none) and the
  contact address, read-only: they are the installer's settings. **Warn when** (7, 14 or 30 days
  before the end, 14 by default) is the one setting you can change. The sidebar's Settings entry
  shows a mark when a certificate is expiring.
- API 0.25.0: `listDomains`, `getDomain`, `checkDomain`, `rotateTeamPassword`,
  `getCertificateSettings`, `putCertificateSettings` and `listCertificates`; `path`, `wwwRedirect`
  and `teamOnly` on `App.services[].domains[]`; `Deployment.teamPasswords` (the password, once,
  `no-store`); `AttentionItem.kind` gains `domain_dns_elsewhere` and `domain_cert_expiring`;
  the notification message gains `domain`, `addressSeen` and `expiresAt`. A deploy token is still
  refused on every domain route and may turn Team only on for its own app.

- **Logs**: the output of every copy of every service of one project › environment on one page,
  **Logs** in the sidebar, newest first. Caravana reads it live from Nomad on every view and
  stores no line (what an app prints may hold anything, so none of it is kept, logged or sent in
  a live notice). Each line's **time** and **level** are read from the line: a JSON line's fields,
  the usual leading timestamps, a time inside the Common Log Format bracket, and the first words
  for the level (`ERROR`, `[warn]`, `E1025` and the like); an indented continuation line (a stack
  trace) takes the level above it; a line that matches nothing is info, **on stderr too** (Go's
  `log` writes there with no level). A line with no time of its own shows `~` and the time of the
  line before it, and a file whose times disagree with its last write (a container in another
  time zone) is marked the same way, never placed hours off. Control sequences (colors, links,
  progress `\r`, NUL) are removed, not rendered, and repeats of the same line are folded into one
  row with a count (an nginx health check every 10 s is one row).
- On the page: a rail of services with the number of copies and a hue each (a copy that has
  stopped but whose output Nomad still holds reads "(gone)"); a **histogram** of what was read
  with the error share of each bar and a marker for each deploy, where the part before what was
  read is hatched and never drawn as zero; **deploys as rows** in the stream ("started rolling
  out", "is live", "failed its health checks · rolled back", with who did it); the search
  (a word or a status code, case-insensitive, over the text and the app and service names); the
  ranges (15 minutes, 1 hour, 24 hours, 7 days) and the level chips with counts; **Only this
  service**; **Lines around this** (the same copy, both streams, every level, the line
  highlighted; it follows the line itself, so two lines in the same millisecond never get mixed
  up, and it says so when Nomad has already rotated that file away); **Raw** for a JSON line;
  **Show older**; and "Older lines were dropped." when what was read begins at the oldest line
  Nomad keeps. **Live** follows new lines while the page is open and **Paused** counts them
  instead. **Download as a text file** writes the filter, oldest first, one line per row with the
  time in UTC, the service, the copy, the stream and the level.
- A page reads at most 1 MiB per stream (16 MiB for the page) and for at most 5 seconds, two
  pages at a time, so a busy app or a starved node answers with what was read and says so ("read
  from 14:02 UTC", "not all read in time") instead of hanging. Search and the histogram see what
  was read.
- API 0.26.0: `listLogs`, `getLogsAround` and `downloadLogs` (`text/plain`), all `no-store`, and
  all refused to a deploy token. `getLogs` (the per-app read) gains `id`, `level` and
  `timeSource` on each line; `listLogs`' description writes out the level and escape rules so a
  client can match the page.

- **Monitoring**: how your apps and the host are doing, on **Monitoring** in the sidebar. Four charts for a project › environment (6 hours by default; 1 hour, 24 hours
  and 7 days too): **requests**, **errors** (the share that were 5xx), the **slowest 5 %** of
  requests (the time that 19 in 20 beat) and **memory**, each drawn against the same 10 minutes of
  the last 7 days (a shaded band from the lowest to the highest, a line for the usual), with a
  marker for each deploy. Above them, the services that are **unusual** come first, each with what
  it did and since when (its value, "usually 210 ms"), the deploy
  nearby ("It started 2 minutes after web v24 rolled out") and, for memory and restarts, the last exit;
  below, a table of every service sorted unusual first (or by traffic, or by memory) with its
  requests, errors, slowest 5 %, memory against its ceiling, CPU sparkline and restarts. A service
  with no domain shows "—" for the request numbers, not zero. Caravana reads Traefik's numbers and
  Nomad's every minute and keeps minute rows for 48 hours and 10-minute rows for 8 days (a few MB
  in its SQLite file); nothing is kept per request, and websocket traffic isn't in the numbers.
- **The host's capacity**, at the foot of Monitoring: memory and CPU **used** and **reserved** (what
  Nomad has promised to the apps, counted by Nomad's own rule for startup tasks), the two disks
  worth watching (the one holding Nomad's data and the one holding Docker's images, one row when
  it is the same disk), a 7-day memory chart, and **"room for about 4 more apps like whoami"**
  (the app in the middle of your apps by memory, and whether memory or CPU runs out first). The
  Apps home has a **Platform** panel with the same numbers and a link.
- **Disk nearly full** on the needs-you list and in notifications: a watched disk at 85 % opens an
  item (a warning; failed from 95 %) that says it the same way everywhere, in the console, the API
  and every message: "The disk at / is 87% full: 1.1 GB left." It resolves once the disk is below
  83 %, so a disk at 84 % doesn't flap, and the disk's meter keeps showing the item's state until
  then. A disk that rises from warning to failed sends one more message (`problem.worsened`: "The
  disk at / is now 96% full: 410 MB left."); a fall sends nothing. Slack, Discord, ntfy and webhook
  channels get it like any problem.
- Monitoring reads as the rest of the console does. A row's mark is the app's own status first (■
  not running or deploy failed, ○ stopped, ◐ rolling out; ◆ only means "unusual against its week",
  and only for a service that serves), and a service that serves nothing gets no "less traffic than
  usual" issue (its cause is on the needs-you list). A chart's head is the last 10 minutes at every
  range, said so, and each chart has a scale on its top line (Errors never zooms in below 5 %) and
  its band's edges drawn. The host's meters read "1.7G used · 1.3G reserved of 1.9G" with the
  reserved hatching above the fill; CPU is in vCPU everywhere; the room sentence says when memory in
  use is already at 85 % or more. The minute's refresh swaps the issues, each chart and each row in
  place, so the sort bar, keyboard focus and a chart you are pointing at stay where they are.
- API 0.27.0: `getMetrics` (`GET /v1/metrics`), `getAppMetrics` (`GET /v1/apps/{id}/metrics`) and
  `getHost` (`GET /v1/host`), all read from the stored samples (a gap is `null`, and `sources` says
  what couldn't be read); `AttentionItem.kind` gains `disk_nearly_full` with `mount`, `holds`,
  `usedPercent`, `freeBytes` and `level`, and the notification message carries the same five
  properties. A chart's `now` is the last 10 minutes at every range (the window the unusual rules
  read), not the range's newest point. Deploy tokens can't use any of it.

### Changed
- Dim text raised to fg-3 for contrast across the console: counts, times, the copy id on Logs and other
  small labels that were drawn in the dimmest grey now read a shade brighter.
- The Backups tab's run and next-backup times are drawn as clock times (UTC, local beside them).
- An app's Domains page grew a status and a certificate column (the same rows as the Domains
  page, filtered to the app), and its domains are edited there with the options above.
- An app's **Logs** tab is now the same page filtered to the app, with "All logs" to go to the
  whole environment; its Copy and Stream selects are gone (the rail and the filters replace
  them).
- `getLogs`: `text` no longer carries escape sequences, and the tail lines now have a `time`
  (read from the line, or estimated) where they had none. A client that read the colors out of
  `text` loses them.
- The Running tab's **memory** (and `listCopies`' `usage`) leaves the page cache out: it is what the
  app holds (Nomad's `Usage` minus `Cache`), the number Monitoring uses, so it drops for apps that
  read files, such as a database or a static site. A copy whose usage Nomad didn't measure (not
  running, or collection off) shows "—" instead of 0 MB.

**Upgrade**: rerun the installer. Traefik is restarted once (a few seconds without HTTP on 80 and
443): it now reads its configuration from a directory so that Team only can hand it password
hashes, and the installer says so when it does it. The installer also writes three lines into
Caravana's settings: `CARAVANA_ACME` and `CARAVANA_ACME_EMAIL` (what Settings › Certificates
shows) and `CARAVANA_EDGE_ADDRESS=127.0.0.1:443` (where Caravana connects to read each domain's
certificate). The scoped Nomad policy needs no new capability (Traefik reads its own job's
variable, and Caravana already writes `nomad/jobs/*`); a schedule run's job is one more batch job
while it runs and is purged after. Until the installer is rerun, a domain with Team only is
refused by Traefik (its router is disabled and answers 404).

**Logs, upgrade**: nothing to rerun. The scoped Nomad policy needs no new capability (the page
uses `read-fs`, which it has had since 0.4.0's volume backups), and nothing is migrated or
stored. The page shows only what Nomad keeps: by default 10 files of 10 MiB per stream for each
copy, so how far back it reaches depends on how much the app prints, and a copy is readable
until Nomad collects it. A host whose Caravana token predates `read-fs` must rerun the
installer, or the app's Logs tab says Nomad refused the read.

**Monitoring, upgrade**: rerun the installer. Traefik is restarted once (a few seconds without HTTP
on 80 and 443) to turn on its request numbers, and the installer says so when it does it. Caravana's scoped Nomad policy gains `node { policy = "read" }`
(for the host's memory, CPU and disks; no drain, no write), and `caravana.env` gains
`CARAVANA_DOCKER_ROOT` (read from `docker info`). Until it is rerun, Monitoring says what is
missing and to run the installer again (Traefik isn't serving its request numbers, or the token
can't read the host). History builds from the upgrade: the first day has no
band and no "usually" (the page says it is still being learned), a band appears with the first
earlier day and is complete after 7.

## 0.12.0 - 2026-10-22

### Added
- API 0.23.0: `Deployment.tokenName` is the name of the API token that made a deploy (`deployApp`,
  `deployCompose`, `promoteApp`, `redeployDeployment`), as it was then. It is absent for a deploy from
  the console, by the webhook, and for deployments from before. Deployments also keep when Nomad
  accepted their job, which the console reads.
- The Apps home shows **Deploying now**: a card for each app with a rollout in progress, with what it
  carries (who pushed it, a promotion, a redeploy), its copies as chips (each with the version it runs,
  the new version's first; starting and stopping ones marked) and, under them, how many new copies are
  healthy. It updates live and is gone when the rollout ends. While an app rolls out its Version cell
  reads `v5 → v6`.
- A filter on the Apps home, once there are more than 5 apps: by name, project, environment, image or
  address, by state and by kind. "Needs you" is the apps with an item in the needs-you list. An app that
  isn't a plain image app has its kind (compose, database, volume) after its name.
- The Activity rail, a deployment's entry and the app's summary say who deployed in the same words:
  "pushed to main by ana · 4e0a77a", "promoted from staging", "through the API by ci-github", "by the
  webhook", "from the console" ("Last deployed 26 minutes ago, promoted from staging."). An entry's run
  link is on the same line.
- Deployments in the history have a stages strip: Queued, Roll out and Live, with the wait, the
  rollout's progress ("1 of 2 new copies healthy") or duration, and the time it went live. The panel is
  drawn for a rollout in progress, the newest deployment and any that failed or was reverted; older
  ones get the same stages as one line. A reverted deployment's raw line from Nomad is prefixed "Nomad
  said:". After a rollback the app and its entry say what the running version repeats: "v3 (v1 again)
  keeps running."
- API 0.22.0: `AppStatus.services` lists, for each service of an app, its wanted and healthy
  copies (`name`, `wantedCopies`, `healthyCopies`), in the app's service order. It is absent for
  an app with no services.
- A Projects page (`/projects`) lists every project with its environments and app count, each row
  a link to the project. "New project" is on it.
- "Back up daily" on the Backups tab: a service with a database image (PostgreSQL, with a port) or a
  volume and no backup gets a row offering it. One click sets the backup (every day at 03:00 UTC,
  the last 7) and deploys; an app that keeps its Compose file keeps it, with the backup in its
  overlay.
- Variables & secrets (`/apps/{id}/variables`) and Domains (`/apps/{id}/domains`), two pages of
  Configure. Variables lists every secret (a lock and ten dots, never a value) and variable of the
  app, sets or replaces one (a secret with "Keep as a secret"), edits a service's variables as a
  `.env` text, and removes a secret. Domains lists the app's domains with a link to open each,
  adds one (with the DNS record to create) and removes one. Each change is a deploy, refused with
  the page again if the app changed since the page was drawn.

### Changed
- The console has one select, 32px tall with the field's 13px mono (it was 38px and 14px); the filter's
  and the Logs toolbar's buttons match it.
- Settings is one page per entry under a grouped nav: Account (`/settings`), Registries
  (`/settings/registries`), Backup storage (`/settings/backup-storage`), Notifications
  (`/settings/notifications`) and API tokens (`/settings/tokens`). The nav shows each entry's count, or a
  mark when something is breaking (a refused registry, a failed backup or a missing destination while a
  service has a backup policy, a failed delivery, a token unused for 90 days); the rows mark more (a paused
  channel, a full token without an end date). Registries say how many apps use each and "changed · not tested
  since" after a save; Notifications and API tokens lead with what needs a look, and an ntfy topic is shown
  by its host only, like a Slack URL. A failed backup says which service of which app, and why.
  `/settings/account` and `/account` redirect to `/settings`; every form posts where it did, and
  answers with the entry's own page. Links to `/settings#registries` and the like now land on Account:
  use the new addresses.
- The console's sidebar and top bar follow the design. The sidebar links Home, Projects and
  Settings (the pages that exist), with the host's initial, the host and the version at the top
  and the account row at the foot, where Sign out is now an icon button. The top bar's
  breadcrumbs read `project / environment` and "New app" is on every page.
- An app's header has its actions in the title row: Restart, Stop… and Deploy (a link to the
  deploy form), or Start when the app is stopped. A "← Home" link sits above the name, and the
  Running tab has a status mark when the app isn't healthy.
- An app with several services names each one and its copies, such as "2 services running v2:
  hollo and postgres, 1 copy each", instead of "2 copies running". A service short of its own
  copies now makes the app degraded even when another has a surplus.
- An app whose newest deployment failed or was reverted shows a "deploy failed" pill even while an
  older version still runs, and the Apps list sorts it first. Its summary and the needs-you list
  say the same sentence, "v3 failed its health checks. v2 keeps running." (or "No copies are
  running."). "0 of 1 copies healthy" now says so when copies exist and none is healthy.
- Configure is a short form: per service, Copies & size (copies, CPU, memory, ceiling) and Deploy
  (image, port and the health check), a read-only "Also set" for what the form leaves alone, then
  Domains and Secrets panels that link to their pages. The environment, the domains and the secrets
  left the form, and saving it keeps them as they are. The registry notice shows when an image
  can't be pulled for want of credentials.
- On the Variables page "Keep as a secret" is ticked by default.
- The Backups tab is redrawn: a row for each backed-up service that opens to its last 30 days (one
  bar per day, each a link to its run), its last ten runs with Restore…, "Restore from the last
  backup" and "Back up now", and the volume's host path and size; the restores follow. The
  console's fields are 32px tall (the sign-in's stays 42px) and a select is 38px.
- The app header offers only what can work: Restart and Stop… need a live version, and "Promote
  to <environment>" needs a healthy newest deployment with its definition recorded; the line is
  labeled "Other environments". On narrow screens the back link also names the project and
  environment.

## 0.11.0 - 2026-10-21

### Fixed
- An app can no longer take the console's own domain. Giving an app the domain in
  `CARAVANA_PUBLIC_URL` made Traefik hold two routes for it, and the console answered 502 while
  that app rolled out. Every way of saving or deploying an app (the API, the console's forms,
  Compose, Promote) now refuses it, naming the domain, and so does a redeploy or webhook of an
  app that already has it. The console's own route in Traefik also now has top priority, so no
  app's route can win over it.
  - **Upgrade:** rerun the installer on an existing host to put the route change in place. If an
    app uses the console's domain today, give it another one first (Configure).

## 0.10.0 - 2026-10-21

### Added
- Coding agents can read and act on an installation: `caravana mcp` is an MCP server that an
  agent such as Claude Code starts on your machine. Give it the console's address and an API
  token (`CARAVANA_URL`, and `CARAVANA_TOKEN` or `CARAVANA_TOKEN_FILE`). The agent can then answer
  "is umbral healthy?" or "why did the last deploy fail?" itself.
  - It reads projects, apps, deployments, copies, logs, volumes, backups, recent actions and the
    needs-you list, and it can wait for a deployment to end.
  - With `--allow-actions` it can also redeploy, deploy a new image to a service, roll back,
    promote, restart, stop, start and back up now.
  - A deploy token keeps it to one app. It never offers deleting, restoring, Run a command, the
    terminal, or any setting that holds a secret, and it never logs the token or an answer.
  - Setup: [docs/mcp.md](docs/mcp.md).
- Releases include the `caravana` binary for macOS (`darwin_arm64` and `darwin_amd64`), for
  running `caravana mcp` where the agent is.

## 0.9.1 - 2026-10-19

### Fixed
- The terminal opens on a host behind HTTPS. It said "Couldn't connect" every time: Traefik
  marks a websocket request as `wss`, not `https`, so Caravana read the request as plain HTTP,
  looked for the wrong session cookie and answered 401. Caravana now takes `wss` from a trusted
  proxy as HTTPS. When the session is the reason a terminal can't connect, the page now says so.

## 0.9.0 - 2026-10-19

### Added
- A service may set its `entrypoint`: the program and its first arguments, replacing the
  image's own, with `command` and `args` after it. A Compose file's `entrypoint` (list or
  string form) is now honored instead of refused. Configure shows it beside the command and
  keeps it when you save, a promotion takes the source's, and the exported jobspec writes it.
  Nothing to do when upgrading beyond rerunning the installer.
- API 0.21.0: `Service.entrypoint` (an array of strings, at most 64 items of at most 4096
  bytes, none empty), and `entrypoint` among the parts a promotion preview can list as changed.
- A terminal in a running app: the Running tab has a Terminal link for each copy, and it opens
  an interactive shell in that copy, drawn in the page (it needs JavaScript; Run a command stays
  for the cases that don't). Pick the shell: Automatic starts bash when the image has it and sh
  otherwise, or choose bash or sh. Colors, the arrow keys, history, resizing the window and
  programs like `top` or an editor work. Nothing typed or printed is kept: not in the logs, not
  in the app's history, not in a notification. What is recorded is that a terminal was opened,
  its shell, how long it lasted and how it ended. A terminal closes after 15 minutes without
  input, or after 60 minutes, and at most 5 are open at once. Only a signed-in console or a
  full API token can open one; a deploy token can't, and neither can another website (the
  request must come from the console's own address). It replaces SSH for looking around inside
  an app. Nothing to do when upgrading beyond rerunning the installer: Nomad's `alloc-exec`
  came with 0.8.0.
- API 0.20.0: `openTerminal` (`GET /v1/apps/{id}/copies/{copyId}/terminal`, a websocket; its
  messages are described in the operation). A new code, `too_many_terminals` (429).

### Changed
- A Compose file as Aspire generates it is accepted: `networks.*.driver: bridge` and
  `volumes.*.driver: local` (the defaults, which Aspire writes out) no longer fail. Another
  driver is still refused, and a refused network driver now says it is about networks, not
  volumes. Networks are warned about only when a file declares more than one ("networks don't
  separate services here", once, on `networks`), since that is when someone meant to keep services
  apart; a single network, like the one Aspire always writes, joins every service already and
  gets no warning. It used to be one warning per service plus one for the file.

## 0.8.1 - 2026-10-19

### Added
- Deploy from CI. Your GitHub workflow builds the image and a new step,
  `Cosmic-Chimps/caravana-action@v1`, deploys it: it waits until the app is healthy, prints the
  deploy's warnings, fails the workflow with the reason when the deploy fails or is reverted, and
  can promote to another environment afterwards. The action is its own repository; Caravana
  never builds an image.
- Deploy tokens: a token that can deploy only one app (its name, in every environment of one
  project). If it leaks it can deploy and promote that app, read its deployments and create it,
  and nothing else: not other apps, not Settings, not tokens. Make one in Settings › API tokens
  ("Only for one app") or on an app's Configure tab ("Create a deploy token for this app"). Like
  every token it is shown once.
- Where a deploy came from: a deploy started by the action records the commit, the branch or tag,
  who pushed and the workflow run. The history says "from CI" with a link to the run, and Slack,
  Discord, ntfy and webhook notifications of that deploy carry it too.
- New app › "From a GitHub repository": create the app without deploying it, get a deploy token
  for it, and copy the workflow and the repository secret to add. Your next push deploys it.
- API 0.19.0: `createToken` takes a `scope` (`{project, app}`) and `listTokens` shows it; a deploy
  token gets 403 `token_scope` for anything outside its app. `deployApp`, `deployCompose`,
  `promoteApp` and the webhook accept optional `Caravana-Origin-Commit`, `-Ref`, `-Actor`, `-Run`
  and `-Repository` headers (422 naming one that breaks its rule), and `Deployment` gains
  `origin`.

### Fixed
- Volume backups and restores failed with "the copy didn't pick up the request" once Nomad had
  been restarted on the host (as rerunning the installer does) after the app's copy started:
  Nomad then stops handing new requests to the copy's backup task until the copy restarts. A
  backup or restore now restarts the copy anyway when the request isn't picked up, which hands
  it over, so it works again without restarting the app by hand.
- A "Back up now" or a restore from the console no longer fails with an error when you leave the
  page while it starts; it goes on to its end.

## 0.8.0 - 2026-10-19

### Added
- The console's sidebar shows which Caravana it is, under the host name: `v0.7.0` for a release,
  `dev` for a build of your own.
- Run a command in a running app: the Running tab has a "Run a command" form for each copy. Type
  a program and its arguments (quotes work like in a shell; for pipes or variables run
  `sh -c '...'`) and the page answers with what it printed, what it wrote to stderr and its exit
  code. A command that runs longer than a minute is stopped and the answer says so; output past
  64 KB is cut and says so. The output is shown once, on that answer, and is kept nowhere: not in
  the logs, not in the app's history, not in a notification, not when you reload. What is
  recorded is that a command ran, its first word (not its arguments) and its exit code. This
  replaces SSH and Nomad's CLI for things like resetting an app's admin credential.
- Stop, Start and Restart for an app, in the app's header. Stop asks first and says what it does:
  every copy stops and the app stops answering, while its definition, secrets, volumes, webhook
  token and history stay. A stopped app shows as stopped (a ring mark) on the Apps page, the
  Apps page doesn't count it as trouble, and anything that would deploy it (the deploy form,
  Configure, the webhook, a promotion into it, a redeploy of a version) is refused with "The app
  is stopped: start it first." Its scheduled backups are skipped, not failed. Start runs exactly
  what ran before, with no new image lookup. Restart restarts the copies one at a time, waiting
  for each to be running (and healthy) before the next, so an app with two copies keeps
  answering (a request that reaches a copy in the second it restarts can still fail); restarts
  you ask for never count as "keeps restarting". Stop, Start and Restart are
  refused while a deploy or a volume backup of the app is running.
- A list of recent actions on the Running tab: restarts, stops, starts and commands, with how
  each ended and whether it came from the console or the API.
- API 0.18.0: `runCommand` (`POST /v1/apps/{id}/copies/{copyId}/run`, answered with
  `Cache-Control: no-store`), `stopApp`, `startApp`, `restartApp` (202, it goes on in the
  background) and `listAppActions`. `AppStatus.state` gains `stopped`, `AppRecord` and
  `AppSummary` gain `stoppedAt`. New codes `app_stopped`, `app_not_stopped`, `app_busy` and
  `copy_not_running` (all 409).

### Fixed
- "Keeps restarting" no longer stays on the Apps page after the app has settled. A copy's
  restarts add up for as long as it runs, so three restarts in the morning kept the warning up
  all day. Now a copy counts only while it has restarted within the last hour, as failed copies
  already did, and the warning clears by itself an hour after the last restart (a notification
  channel then gets "resolved").

### Upgrade
- Rerun the installer: Caravana's Nomad token gains `alloc-exec`, the permission "Run a command"
  needs. Until you do, the form says Nomad refused the command and to run the installer again;
  everything else works as before. Stop, Start and Restart need no new permission.

## 0.7.0 - 2026-10-18

### Added
- Create an app without deploying it: New app has a second button, "Create without deploying".
  Give it a name and the image your CI will push (it doesn't have to exist yet) and nothing is
  deployed, nothing fails and nothing reaches your notifications. The app's page says it is
  waiting for its first deploy and shows how to start it: Deploy now, a webhook token (the first
  call of the webhook deploys the app as it is stored), or the API call. So CI can be set up
  before its first build.
- A name is enough: create an app with only its name and let your automation fill in the rest.
  The app is listed as "not defined yet", can have a webhook token, and its page offers Define it
  in Configure (an image, a port, a domain and a health check, saved and deployed in one step),
  Deploy from Compose, and the API call with a body. Until it has a definition, Deploy now and
  the webhook answer that there is nothing to deploy and record nothing, because the webhook
  never chooses an image. Secret values can't be given when creating without deploying, only
  with a deploy; copying such an app to another environment copies its name.
- The Apps page counts apps that were never deployed as "waiting for a first deploy", not as
  something that needs you.
- API 0.17.0: `createApp` and `createEnvironmentApp` take `?deploy=false`: 201 with the app (its
  `revision` as `ETag`, its URL as `Location`) and no deployment, and the body may hold only an
  `id` (or `appName`) and a `name`. `AppRecord` and `AppSummary` gain `defined`. New code
  `app_not_defined` (409) on `deployApp` with an empty body, `redeployDeployment` and the
  webhook call.

## 0.6.0 - 2026-10-18

### Added
- Notifications: Caravana now tells you when something needs you and when a deploy ends, even
  when nobody has the console open. Settings › Notifications adds channels of four kinds: ntfy
  (a topic, with an optional access token), Slack and Discord (an incoming webhook URL), and a
  plain webhook that gets signed JSON. Each channel picks what it hears: problems, failed
  deploys, successful deploys. "Send a test" checks a channel, Pause and Resume stop and restart
  it, and each channel lists its last 20 deliveries.
- What is sent: a message when a problem on the Apps page's "needs you" list opens (failed or
  reverted deploys, out-of-memory kills, crash loops, unhealthy copies, failed backups and
  restores, no backup destination) and another when it has been gone for 5 minutes. A problem
  that flaps sends one "opened", not many. More than 10 new problems at once arrive as one
  summary. A deploy that ends sends its outcome, the version, how it was started (console,
  API or webhook) and the reason when it failed.
- Messages are queued, so a receiver that is down or slow never holds up deploys: a failed
  delivery is tried again after 30 seconds and after 2 minutes, then marked failed with its
  reason. Messages queued before a restart are still sent after it.
- Webhooks are signed: each message carries `Caravana-Signature: t=<unix seconds>,v1=<hex>`
  (HMAC-SHA256 of `<t>.<body>` with the channel's signing secret), plus `Caravana-Event` and
  `Caravana-Delivery`. The secret is shown once when the channel is added or the secret is
  replaced (the old one stops working at once). Receivers should refuse a `t` older than 5
  minutes. The message's fields are in `api/openapi.yaml`.
- Messages link to the console page about the problem or deploy when `CARAVANA_PUBLIC_URL` is
  set (the installer sets it from the console domain).
- Secrets of a channel (a Slack or Discord URL, an ntfy token, a webhook's signing secret) are
  write-only: kept in Nomad Variables, never shown again, never in a log, an error or a page.
- Settings: `CARAVANA_PUBLIC_URL` (`-public-url`) is the console's address for links;
  `CARAVANA_NOTIFY_RESOLVE_AFTER` (`-notify-resolve-after`, default 5m, at least 10s) is how
  long a problem must be gone before "resolved" is sent.
- API 0.15.0: `GET` and `POST /v1/notifications/channels`, `PATCH` and `DELETE`
  `/v1/notifications/channels/{id}`, `POST .../{id}/test`, `GET .../{id}/deliveries` and
  `POST .../{id}/secret`; a 409 `not_a_webhook`.
- Projects and environments: apps now live in a project and an environment, such as a project
  `shop` with `staging` and `production`, each with its own copy of every app: its own env,
  secrets, domains, memory, copies and backups. New project (and Add environment on the
  project's page) are in the console, and New app has a "Project and environment" choice. The
  Apps page groups apps by project and environment once you add the first one; until then it
  looks as it did. An app's page shows where it is, and lists the project's other environments.
- Copy to environment: "Copy to production" makes the same app in another environment without
  deploying it. The copy has the app's definition but no domains, and none of its secret values
  (they were never stored): its Configure page lists the secrets that need a value before the
  first deploy. Webhook tokens, backups and volume contents are not copied.
- Promote: "Promote to production" shows what would change (image and digest per service, the
  parts of the service's shape that are replaced, services added) and deploys the staging
  version to production by digest, so production runs exactly the image staging ran. The target
  keeps its own env, secrets, domains, memory, copies and backups. It refuses, with the reason,
  when staging's newest deployment isn't healthy, when its image is gone from the registry, or
  when production has a service staging doesn't (promoting would delete it). The history says
  "Promoted from staging" and links to the deployment it came from, and notifications name the
  project, the environment and the promotion.
- The app ID of an app outside Default › production is `<project>--<environment>--<name>`, for
  example `shop--staging--web`; names are lowercase labels and never contain `--`. The ID is
  still the Nomad job, router, secrets path and volume prefix, so a client can compute it and
  deploy with the same `deployApp` as before. The whole ID plus a service name must fit in 63
  characters (the console's New app also keeps names to 31).
- API 0.16.0: `listProjects`, `createProject`, `getProject`, `updateProject`, `deleteProject`,
  `createEnvironment`, `deleteEnvironment`, `listEnvironmentApps`, `createEnvironmentApp`,
  `getEnvironmentApp`, `previewPromotion`, `promoteApp` and `copyApp`. Apps, summaries, needs-you
  items, deployments and webhook messages gain `project`, `environment` and `appName`;
  `listApps` takes `?project=` and `?environment=`; a promotion's deployment has `promotedFrom`.
  New codes: `project_not_found`, `environment_not_found`, `project_exists`, `environment_exists`,
  `not_empty`, `protected`, `app_not_in_environment` and `promotion_removes_service`.

### Security
- An app created with `POST /v1/apps` and secret values in its body kept those values in
  Caravana's SQLite database, beside the names. Secret values now live only in Nomad Variables,
  whichever way an app is created, and the new version removes any such value from the database
  when it starts. Apps made in the console, by a deploy or from Compose were not affected. If you
  created apps with secrets through `POST /v1/apps`, consider changing those secrets, since older
  copies of the database (backups of `/var/lib/caravana`) may still hold them.

### Upgrade
- Rerun the installer so `CARAVANA_PUBLIC_URL` is set to your console domain; without it,
  messages still go out, just without links. The store gains its notification tables when the
  new version starts, and no new Nomad capability is needed.
- Nothing moves on upgrade: every app you have is in the project Default, environment
  `production`, with the ID it has now, so no job, secret, volume, backup, address or webhook
  URL changes and nothing is redeployed. The store gains its project tables when the new version
  starts. Default and its production environment can't be deleted (your existing apps live
  there); Default can be renamed for display. All environments share Nomad's `default`
  namespace: a namespace per environment would need Traefik restarted each time one is added,
  and gives nothing until there are teams.

## 0.5.0 - 2026-10-16

### Added
- Private registries: Settings › Registries keeps a user name and a password or token per
  registry (GitHub's `ghcr.io`, GitLab, Quay, Docker Hub, or your own). Deploys, redeploys,
  webhooks and rollbacks of images from that registry then work. "Test" checks the credentials
  against the registry and shows when they last worked. The password is write-only: it is never
  shown again, never in a log, a page, an export or a deployment record.
- Images from any registry host now deploy, including hosts with a port (`registry.example.com:5000`)
  and your own self-hosted ones. A host on `localhost` or another loopback address is reached over
  plain HTTP; every other host must serve its registry over HTTPS.
- Changing or removing a registry's credentials updates the apps that use them. Nothing restarts;
  the new credentials are used the next time Nomad starts a copy of the app (a deploy, or a copy
  that restarts).
- API 0.14.0: `GET /v1/registries`, `PUT` and `DELETE /v1/registries/{host}`,
  `POST /v1/registries/{host}/test`; a 422 `registry_auth_failed` when a registry turns the stored
  credentials down.

### Changed
- When an image is missing or private, the message now says "add credentials for <host> in
  Settings › Registries", instead of saying credentials aren't supported.
- A deploy that fails because the registry refused the stored credentials says so, with one action
  that opens Settings › Registries.

### Security
- To let Nomad pull a private image, the credentials reach that app's own container as the
  environment variables `CARAVANA_REGISTRY_USERNAME` and `CARAVANA_REGISTRY_PASSWORD`. Only
  apps whose image comes from a registry you added get them, and the job Nomad stores holds
  placeholders, not the password. Use a token that can only read images (for GitHub, a
  personal access token with `read:packages`), never your account password.

### Upgrade
- Nothing to do beyond rerunning the installer: the store gains its registries table when the new
  version starts, and no new Nomad capability is needed.

## 0.4.0 - 2026-10-16

### Added
- Volume backups: a service's backup can now use the engine `volume`, for apps that keep their
  data in files (SQLite, an embedded MariaDB, uploads) rather than in Postgres. On its schedule,
  or with "Back up now", the service stops, its volumes are archived to your backup destination,
  and it starts again. The stop is usually a few seconds, and the Backups tab shows how long the
  last one took.
- Restore a volume backup from the Backups tab. The service stops, its volumes get back exactly
  the backup's files (files added since are deleted), and it starts again. If anything fails
  along the way, the volumes are left as they were.
- The Backups tab opens with a Volumes section: each volume's mount, where it lives on the host,
  its size, and its last backup or "not backed up".
- API 0.13.0: `GET /v1/apps/{id}/volumes`; backups and restores say their `engine` and
  `stoppedSeconds`; a 409 `no_running_copy` when there is no running copy to back up or restore.

### Changed
- Every service with a volume gains a small task, `caravana-volumes`, that runs before the service
  starts (it measures the volumes, and does backups and restores). It reserves 32 MB while the
  service runs.
- Restarts Caravana asks for no longer count towards "keeps restarting" on the needs-you list.

### Upgrade
- Rerun the installer: Caravana's Nomad policy gains `alloc-lifecycle` and `read-fs`.
- Then deploy each app that has a volume once, so it gains the `caravana-volumes` task. Until
  then its volumes show "not measured yet" and can't be backed up with the `volume` engine.

## 0.3.0 - 2026-10-15

### Added
- An app deployed from a Compose file keeps the file, exactly as you wrote it (comments
  included), with the app and with every deployment. Secret values are never kept.
- Configure edits that file: change it and its domains, health checks, secrets and backup, then
  deploy (Preview is there for a look first). The definition below it is read-only, so the file
  is the one thing you edit.
- Apps made from Compose before this release ask for their file once on Configure. Paste it and
  deploy; from then on it is kept.
- Redeploying a past version brings back the file it was deployed from, and each history entry
  made from a file offers "Download Compose file".
- API 0.12.0: `GET /v1/apps/{id}/compose` (with the app's revision as `ETag`, so a client can
  edit and send it back with `If-Match`), `GET /v1/apps/{id}/deployments/{deploymentId}/compose`,
  and `composeKept` on apps and deployments.

### Changed
- Setting or removing a secret, the webhook and redeploys keep the file. Deploying a plain
  definition instead (`POST /v1/apps/{id}/deployments` with a body) drops it, and the deployment
  says so in its warnings.
- An app's top bar no longer has a Deploy button. Each tab's Deploy is its own form's button,
  so pressing it deploys what that page shows.

### Upgrade
- Rerun the installer. The store gains a column for the kept file (migration 0007) when the new
  version starts; nothing else to do.

## 0.2.0 - 2026-10-14

### Added
- Configure is an edit form: image, environment, port, domains, copies, health check, CPU and
  memory per service. Saving deploys, and is refused when the app changed since you opened the
  form.
- New app takes a pasted Compose file, with a preview before anything is created; Configure can
  deploy an existing app from a Compose file.
- The Running tab shows CPU and memory per copy, read from Nomad while the tab is open.
- The Apps home opens with a "needs you" list: failed or reverted deploys, out-of-memory kills,
  crash loops, unhealthy copies, failed backups and restores, and no backup destination.
- API 0.11.0: `getApp` sends the revision as `ETag`; deploys take `If-Match` (412
  `app_changed`) and Compose creates take `If-None-Match: *`; `listCopies?usage=true`;
  `GET /v1/attention`.

### Upgrade
- Rerun the installer: it turns on Nomad's allocation metrics, without which the Running tab
  has no usage to show.

## 0.1.1 - 2026-10-12

### Added
- Memory per service: a reservation (`memoryMB`, default 256) and a ceiling it may burst to
  (`memoryMaxMB`, default 1024 or the reservation if larger). From Compose, `mem_reservation`
  is the reservation and `mem_limit` the ceiling. API 0.10.0.

### Upgrade
- Rerun the installer: it turns on Nomad's memory oversubscription. Without it Nomad ignores the
  ceiling, and the deployment's warnings say so.

## 0.1.0 - 2026-10-11

The first release: a single-node PaaS on Nomad, installed with one command.

### Added
- One-command install on a fresh Ubuntu 24.04 VM: Docker, Nomad with ACLs, Traefik with Let's
  Encrypt, and Caravana under systemd. Rerunning the installer upgrades (and rolls back if the
  new version fails), `--uninstall` leaves your apps running.
- Deploy an image or a Compose file. Tags are pinned to digests, every deploy is health-checked
  and blue/green, and a failed one reverts on its own.
- Redeploy by webhook, with a per-app token.
- Secrets per service, write-only, kept in Nomad Variables.
- PostgreSQL backups on a schedule to S3-compatible storage, with restore.
- Redeploy any past version, and export any app as a plain Nomad jobspec.
- The web console (apps, deployments with live progress, configure, logs, backups) and the HTTP
  API (`api/openapi.yaml`), behind one admin login and API tokens.
