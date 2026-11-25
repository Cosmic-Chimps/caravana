-- Feature 057: the audit log. One row per change made in Caravana: when, who, through which door,
-- what was done, to what, and what changed.
--
-- audit_events has no foreign keys: an entry outlives the app, the person, the token or the project it
-- is about (deleting any of them keeps its entries), so the resource, the actor and the record it links
-- to are text. It holds no secret: the service notes names, never values (specs/057-audit-log/plan.md
-- D6), and `changes` is the audit package's redacted diff. No route and no store function edits or
-- deletes a single entry; the only removal is the prune of entries older than the retention.
--
-- AUTOINCREMENT, so the ID that orders the log and is the page cursor is never reused after a prune.
-- Times are RFC 3339 text in UTC with nine fraction digits, so they sort as text, like every other table.
--
-- audit_settings is the owner's one row: how many days entries are kept (90 by default, 30 to 3650).

CREATE TABLE audit_events (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  at             TEXT NOT NULL,
  actor_kind     TEXT NOT NULL,              -- person, token, deploy_token, webhook, caravana, host, anonymous
  actor_email    TEXT NOT NULL DEFAULT '',   -- the person's, or what a failed sign-in typed
  actor_token    TEXT NOT NULL DEFAULT '',   -- a token's name; a webhook's app ID
  actor_person   TEXT NOT NULL DEFAULT '',   -- the person's ID, as text: the person may be removed
  door           TEXT NOT NULL,              -- console, api, mcp, webhook, caravana, host
  address        TEXT NOT NULL DEFAULT '',   -- the client's, when there was a request
  action         TEXT NOT NULL,              -- a past-tense verb from the audit package's closed list
  operation      TEXT NOT NULL DEFAULT '',   -- METHOD /pattern
  resource_kind  TEXT NOT NULL,
  resource_id    TEXT NOT NULL DEFAULT '',
  resource_name  TEXT NOT NULL DEFAULT '',
  project        TEXT NOT NULL DEFAULT '',
  environment    TEXT NOT NULL DEFAULT '',
  changes        TEXT NOT NULL DEFAULT '[]', -- JSON: [{path, op, from?, to?}]
  record_kind    TEXT NOT NULL DEFAULT '',   -- deployment, app_action, backup, restore, ...; '' for none
  record_id      TEXT NOT NULL DEFAULT '',
  related        TEXT NOT NULL DEFAULT '[]', -- JSON: [{kind, id, action}]
  fallback       INTEGER NOT NULL DEFAULT 0  -- 1: nothing noted this API write; the route's access rule made the entry
) STRICT;

CREATE INDEX audit_events_at ON audit_events (at);
CREATE INDEX audit_events_actor ON audit_events (actor_email, id);
CREATE INDEX audit_events_resource ON audit_events (resource_kind, resource_id, id);
CREATE INDEX audit_events_place ON audit_events (project, environment, id);

CREATE TABLE audit_settings (
  id             INTEGER PRIMARY KEY CHECK (id = 1),
  retention_days INTEGER NOT NULL DEFAULT 90 CHECK (retention_days BETWEEN 30 AND 3650)
) STRICT;
INSERT INTO audit_settings (id) VALUES (1);
