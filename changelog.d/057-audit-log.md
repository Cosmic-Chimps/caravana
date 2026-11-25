### Added
- **An audit log: who changed what.** Every change leaves one entry with when, who (a person, a token, a webhook, Caravana
  itself, or the host's command line), the door it came through (console, API, MCP, webhook), what was done, to what, and
  the fields that changed, such as a domain removed and another added. A secret, a token or a password is never in an
  entry: it shows as `set`, `changed` or `removed`. Failed sign-ins, deploys, rollbacks, promotions, deletions, settings,
  tokens, people, servers and more are all there. Owners and admins read it in **Settings › Audit log** (filters, 100 a
  page, **Older**, changes under each entry) and over the API (`listAuditEvents`), and export a range as CSV. Entries are
  kept 90 days by default; an owner sets 30 to 3650 days. No route edits or deletes an entry. API 0.62.0 (`listAuditEvents`,
  `exportAuditEvents`, `getAuditSettings`, `putAuditSettings`). See `docs/audit-log.md`.

### Upgrade
- Migration 0045 adds the log's two tables when Caravana starts. There is nothing to rerun and nothing to configure.
  Changes made before the upgrade aren't in the log.
