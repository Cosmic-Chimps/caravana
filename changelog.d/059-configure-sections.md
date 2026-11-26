### Added
- **A health check form for apps edited through a Compose file.** Configure › Services now shows the health
  check of each service as a form, "Save and deploy <service>", even when the app keeps its Compose file. The
  check is written into the file's settings, so the file you typed stays as it is.
- **Change the policy… on the Backups tab.** A backed-up service can change its engine, its schedule (daily,
  or no automatic backups) and how many backups it keeps without going through the Compose form. Where backups
  go, copies and the restore schedule keep their own forms.

### Changed
- **Configure is now six sections, each with its own page.** Overview (a read-only summary, with Delete app
  last), Source (the images, the Compose file or the build), Services (copies, size, health check, placement and
  scaling), Variables & secrets, Domains and Integrations (webhook, deploy token, export), with a menu beside
  them. Each section has its own form and its own "Save and deploy", so one change is one deploy. Nothing was
  removed: every form is on the page that holds what it changes.
- **The Compose form of an app that already exists holds only the file.** Deploy from Compose and Configure ›
  Source of an app that keeps its file show the file, Preview and Deploy, and say that secrets, domains, health
  checks and backups stay as they are. Set secrets on Variables & secrets, health checks on Services and backups
  on the Backups tab. New app › Compose keeps its form.
