-- Feature 051: changing the console's address from the console. One row per change the owner
-- asked for: the checks Caravana ran on the new domain, where the change stands, and how long
-- the old domain keeps redirecting.
--
-- Nothing secret is here: a domain, a state and Caravana's own sentences. Times are RFC 3339
-- text in UTC with nine fraction digits, so they sort as text like every other table's.
CREATE TABLE console_address_changes (
  id                 TEXT PRIMARY KEY,               -- 32 lowercase hex characters, the agent's request ID
  domain             TEXT NOT NULL,                  -- the new domain, lowercase
  old_domain         TEXT NOT NULL,                  -- the console's domain when the change was made
  redirect_days      INTEGER NOT NULL CHECK (redirect_days IN (0, 7, 30, 90)),
  status             TEXT NOT NULL CHECK (status IN
                     ('checking', 'ready', 'check_failed', 'switching', 'applied', 'failed', 'refused', 'lost', 'cancelled')),
  checks             TEXT NOT NULL DEFAULT '[]',     -- JSON: [{name, state, message}]
  reason             TEXT NOT NULL DEFAULT '',       -- one line for check_failed, failed, refused and lost
  dns_record_created INTEGER NOT NULL DEFAULT 0,     -- Caravana made the A/AAAA records of the domain
  created_at         TEXT NOT NULL,
  ready_at           TEXT,
  switch_at          TEXT,
  settled_at         TEXT,
  redirect_until     TEXT                            -- set when applied: the old domain redirects until then
) STRICT;
CREATE INDEX console_address_changes_by_time ON console_address_changes (created_at, id);

-- One change is open (checking, ready or switching) at a time.
CREATE UNIQUE INDEX console_address_changes_one_open ON console_address_changes ((1))
  WHERE status IN ('checking', 'ready', 'switching');
