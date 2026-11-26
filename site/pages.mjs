// The pages of the site: one Markdown source file is one page.
//
// Each entry is [slug, source] or [slug, source, { title }]. The slug is the page's address under `base`
// (`index` is the home page) and the source is a path from the repository root.
// A source carries no frontmatter and starts with one `# Title`, which is the
// page's title (the one entry that gives a `title` replaces it: CHANGELOG.md's
// heading says "Changelog"). scripts/collect.mjs reads this table, fails when a source is
// missing (unless it runs with --allow-missing, for local drafts) and when a
// page here is not in sidebar.mjs, or the other way round.
//
// Files that are not listed here are not pages: a link to one goes to GitHub.
export const pages = [
  // Start
  ['index', 'docs/guide/index.md'],
  ['install', 'docs/guide/install.md'],
  ['first-app', 'docs/guide/first-app.md'],
  ['console', 'docs/guide/console.md'],

  // Deploy
  ['deploy/image', 'docs/guide/deploy/image.md'],
  ['deploy/compose', 'docs/guide/deploy/compose.md'],
  ['deploy/templates', 'docs/guide/deploy/templates.md'],
  ['deploy/git', 'docs/building-from-git.md'],
  ['deploy/ci', 'docs/guide/deploy/ci.md'],
  ['deploy/aspire', 'docs/guide/deploy/aspire.md'],
  ['deploy/registries', 'docs/guide/deploy/registries.md'],
  ['deploy/previews', 'docs/previews.md'],
  ['deploy/rollback', 'docs/guide/deploy/rollback.md'],

  // Configure an app
  ['configure/variables', 'docs/guide/configure/variables.md'],
  ['configure/secret-providers', 'docs/secret-providers.md'],
  ['configure/domains', 'docs/guide/configure/domains.md'],
  ['configure/dns-providers', 'docs/dns-providers.md'],
  ['configure/certificates', 'docs/certificates.md'],
  ['configure/volumes', 'docs/guide/configure/volumes.md'],
  ['configure/resources', 'docs/guide/configure/resources.md'],
  ['configure/autoscaling', 'docs/autoscaling.md'],
  ['configure/published-ports', 'docs/published-ports.md'],
  ['configure/opensearch-apps', 'docs/opensearch-apps.md'],
  ['configure/workload-identity', 'docs/workload-identity.md'],

  // Day to day
  ['operate/projects', 'docs/guide/operate/projects.md'],
  ['operate/logs', 'docs/guide/operate/logs.md'],
  ['operate/requests', 'docs/requests.md'],
  ['operate/commands', 'docs/guide/operate/commands.md'],
  ['operate/schedules', 'docs/guide/operate/schedules.md'],
  ['operate/backups', 'docs/guide/operate/backups.md'],
  ['operate/database-backups', 'docs/database-backups.md'],
  ['operate/backup-destinations', 'docs/backup-destinations.md'],
  ['operate/monitoring', 'docs/guide/operate/monitoring.md'],
  ['operate/notifications', 'docs/guide/operate/notifications.md'],

  // Run the host
  ['host/upgrade', 'docs/guide/host/upgrade.md'],
  ['host/servers', 'docs/guide/host/servers.md'],
  ['host/placement', 'docs/placement-and-drain.md'],
  ['host/network-blips', 'docs/network-blips.md'],
  ['host/io', 'docs/host-io.md'],
  ['host/reclaim-space', 'docs/reclaim-space.md'],
  ['host/image-scanning', 'docs/image-scanning.md'],
  ['host/console-address', 'docs/console-address.md'],
  ['host/people', 'docs/people.md'],
  ['host/sign-in', 'docs/guide/host/sign-in.md'],
  ['host/audit-log', 'docs/audit-log.md'],
  ['host/troubleshooting', 'docs/guide/host/troubleshooting.md'],

  // Reference
  ['reference/settings', 'docs/guide/reference/settings.md'],
  ['reference/cli', 'docs/guide/reference/cli.md'],
  ['reference/api', 'docs/guide/reference/api.md'],
  ['reference/mcp', 'docs/mcp.md'],
  ['release-notes', 'CHANGELOG.md', { title: 'Release notes' }],
];
