
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

