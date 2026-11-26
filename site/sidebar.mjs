// The sidebar of the site. scripts/collect.mjs imports this file and fails when
// a page of pages.mjs is not named here or when an entry names a page that
// pages.mjs does not have, so a new document cannot be published without a
// place in the navigation, and a renamed one cannot leave a dead entry behind.
// An entry with a `slug` is a page (`index` is the home page); an entry with a
// `link` is not, and is skipped by that check. The label may be shorter than the
// page's title, which is the `# ` heading of its source.
import { branch, repository } from './site.config.mjs';

export const sidebar = [
  {
    label: 'Start',
    items: [
      { label: 'Introduction', slug: 'index' },
      { label: 'Install', slug: 'install' },
      { label: 'Your first app', slug: 'first-app' },
      { label: 'A tour of the console', slug: 'console' },
    ],
  },
  {
    label: 'Deploy',
    items: [
      { label: 'From an image', slug: 'deploy/image' },
      { label: 'From a Compose file', slug: 'deploy/compose' },
      { label: 'From a template', slug: 'deploy/templates' },
      { label: 'Build from a Git repository', slug: 'deploy/git' },
      { label: 'From CI', slug: 'deploy/ci' },
      { label: 'From a .NET Aspire AppHost', slug: 'deploy/aspire' },
      { label: 'Private registries', slug: 'deploy/registries' },
      { label: 'Previews', slug: 'deploy/previews' },
      { label: 'Deploys, history and rollback', slug: 'deploy/rollback' },
    ],
  },
  {
    label: 'Configure an app',
    items: [
      { label: 'Variables and secrets', slug: 'configure/variables' },
      { label: 'Secret providers', slug: 'configure/secret-providers' },
      { label: 'Domains and HTTPS', slug: 'configure/domains' },
      { label: 'DNS providers', slug: 'configure/dns-providers' },
      { label: 'Certificates', slug: 'configure/certificates' },
      { label: 'Volumes', slug: 'configure/volumes' },
      { label: 'Copies, memory and health', slug: 'configure/resources' },
      { label: 'Autoscaling', slug: 'configure/autoscaling' },
      { label: 'Published ports', slug: 'configure/published-ports' },
      { label: 'Workload identity', slug: 'configure/workload-identity' },
    ],
  },
  {
    label: 'Day to day',
    items: [
      { label: 'Projects and environments', slug: 'operate/projects' },
      { label: 'Logs', slug: 'operate/logs' },
      { label: 'Requests', slug: 'operate/requests' },
      { label: 'Stop, start, run a command', slug: 'operate/commands' },
      { label: 'Schedules', slug: 'operate/schedules' },
      { label: 'Backups and restore', slug: 'operate/backups' },
      { label: 'Database backups', slug: 'operate/database-backups' },
      { label: 'Backup destinations', slug: 'operate/backup-destinations' },
      { label: 'Monitoring', slug: 'operate/monitoring' },
      { label: 'Notifications', slug: 'operate/notifications' },
    ],
  },
  {
    label: 'Run the host',
    items: [
      { label: 'Upgrade and uninstall', slug: 'host/upgrade' },
      { label: 'Add a server', slug: 'host/servers' },
      { label: 'Placement and moving work', slug: 'host/placement' },
      { label: 'Network blips', slug: 'host/network-blips' },
      { label: 'Network and disk', slug: 'host/io' },
      { label: 'Reclaim space', slug: 'host/reclaim-space' },
      { label: 'Image scanning', slug: 'host/image-scanning' },
      { label: 'Console address', slug: 'host/console-address' },
      { label: 'People and roles', slug: 'host/people' },
      { label: 'Sign-in and tokens', slug: 'host/sign-in' },
      { label: 'Audit log', slug: 'host/audit-log' },
      { label: 'Troubleshooting', slug: 'host/troubleshooting' },
    ],
  },
  {
    label: 'Reference',
    items: [
      { label: 'Settings', slug: 'reference/settings' },
      { label: 'The caravana command', slug: 'reference/cli' },
      { label: 'The HTTP API', slug: 'reference/api' },
      { label: 'The MCP server', slug: 'reference/mcp' },
      {
        label: 'The API description (on GitHub)',
        link: `${repository}/blob/${branch}/api/openapi.yaml`,
      },
      { label: 'Release notes', slug: 'release-notes' },
    ],
  },
];

/** The slugs of the pages the sidebar names, walking the nested groups. */
export function sidebarSlugs(items = sidebar) {
  return items.flatMap((item) => (item.items ? sidebarSlugs(item.items) : item.slug ? [item.slug] : []));
}

/**
 * The sidebar without the pages a draft build skipped (their sources do not
 * exist yet), and without a group left empty by that. The list comes from
 * `node scripts/collect.mjs --allow-missing`; a normal build skips nothing.
 */
export function visibleSidebar(skipped = []) {
  if (skipped.length === 0) return sidebar;
  const prune = (items) =>
    items
      .filter((item) => !item.slug || !skipped.includes(item.slug))
      .map((item) => (item.items ? { ...item, items: prune(item.items) } : item))
      .filter((item) => !item.items || item.items.length > 0);
  return prune(sidebar);
}
