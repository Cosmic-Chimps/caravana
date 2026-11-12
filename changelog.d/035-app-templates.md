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
- API 0.36.0: `listTemplates`, `getTemplate`, `getTemplateLogo`, `previewTemplate`, `installTemplate`
  (`withoutBackups` in the body), `checkTemplates` and `putTemplateSettings`; `AppRecord.template` and
  `AppSummary.template`; codes `template_not_found` and `template_unavailable`. Deploy tokens can't use
  any of them. `caravana mcp` gains the read tools `list_templates` and `get_template` (there is no
  install tool: it would carry secret answers through an agent).
- `CARAVANA_TEMPLATES_URL` (`-templates-url`) points the catalog check somewhere else; it is for tests and
  the lab.

### Fixed
- The API answered `"keep": 0` for a backup that left `keep` out, and `"port": 0` for a Compose overlay that
  left its health port out, which broke the contract's minimums for clients that validate answers. A zero
  is left out now.

### Upgrade
- Nothing to do. Migration 0028 adds the template columns and the catalog's one row; apps made before it
  have no template.
