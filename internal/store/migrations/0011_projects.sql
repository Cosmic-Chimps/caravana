-- Feature 019: projects and environments.

-- A project holds environments; an app sits in one environment. Names are
-- lowercase labels without "--" (app.ValidName), so the three parts can be
-- read back from an app ID.
CREATE TABLE projects (
  id           TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  created_at   TEXT NOT NULL,   -- RFC 3339, UTC, nine fraction digits
  updated_at   TEXT NOT NULL
) STRICT;

CREATE TABLE environments (
  project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  position   INTEGER NOT NULL,  -- the order shown; a new project's production is 0
  created_at TEXT NOT NULL,
  PRIMARY KEY (project_id, name)
) STRICT;

-- Every app that exists today lives here. The times are in the stored format
-- (storedTime in apps.go): UTC with all nine fraction digits, so they sort as text.
INSERT INTO projects (id, display_name, created_at, updated_at) VALUES
  ('default', 'Default',
   strftime('%Y-%m-%dT%H:%M:%S', 'now') || '.000000000Z',
   strftime('%Y-%m-%dT%H:%M:%S', 'now') || '.000000000Z');
INSERT INTO environments (project_id, name, position, created_at) VALUES
  ('default', 'production', 0, strftime('%Y-%m-%dT%H:%M:%S', 'now') || '.000000000Z');

-- Where an app is. They are read from the app ID and kept beside the document,
-- not in it, so no stored revision changes. No REFERENCES: SQLite refuses to
-- add a referencing column with a non-NULL default while foreign keys are on,
-- and the service checks the environment exists before it inserts an app.
ALTER TABLE apps ADD COLUMN project     TEXT NOT NULL DEFAULT 'default';
ALTER TABLE apps ADD COLUMN environment TEXT NOT NULL DEFAULT 'production';
ALTER TABLE apps ADD COLUMN app_name    TEXT NOT NULL DEFAULT '';
UPDATE apps SET app_name = id;
CREATE UNIQUE INDEX apps_by_place ON apps (project, environment, app_name);
