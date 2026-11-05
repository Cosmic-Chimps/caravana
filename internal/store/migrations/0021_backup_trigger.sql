-- Who started a backup run (task 2026-10-23, from 022's review): timer, for a
-- run its schedule started (Nomad's periodic job or Caravana's volume clock),
-- or owner, for Back up now (the console or the API). Every row written before
-- this column reads timer: a Back up now made then wasn't recorded as one.
ALTER TABLE backups ADD COLUMN trigger TEXT NOT NULL DEFAULT 'timer'
                    CHECK (trigger IN ('timer', 'owner'));
