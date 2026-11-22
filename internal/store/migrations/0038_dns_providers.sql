-- Feature 048: DNS providers. A zone Caravana may make records in, and the
-- records it made.
--
-- Only what isn't secret is here: the zone's API token lives in the Nomad
-- Variable caravana/dns/<zone, "." written "~">, as the registries' and the
-- secret providers' credentials do.
CREATE TABLE dns_zones (
  zone         TEXT PRIMARY KEY,             -- a lowercase domain name
  provider     TEXT NOT NULL CHECK (provider IN ('cloudflare')),
  proxied      INTEGER NOT NULL DEFAULT 0,   -- the proxy setting of the records Caravana makes
  zone_id      TEXT NOT NULL DEFAULT '',     -- the provider's ID, learned by a test or the first record
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  tested_at    TEXT,                         -- NULL until tested
  test_ok      INTEGER,                      -- 1 listed, 0 refused or unreachable
  test_message TEXT NOT NULL DEFAULT '',
  test_count   INTEGER NOT NULL DEFAULT 0    -- records the test listed
) STRICT;

-- A record Caravana made. Only these are ever changed or removed. Deleting an
-- app does NOT delete its rows: the record still exists at the provider, and
-- the service removes it first (and again, from the pass, when it couldn't).
-- A host may have several records of one type (one per address of this
-- installation), so the row is keyed by the provider's record ID.
CREATE TABLE dns_records (
  record_id  TEXT PRIMARY KEY,               -- the provider's
  host       TEXT NOT NULL,
  type       TEXT NOT NULL CHECK (type IN ('A', 'AAAA')),
  zone       TEXT NOT NULL,
  app_id     TEXT NOT NULL,
  content    TEXT NOT NULL,
  proxied    INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
) STRICT;

CREATE INDEX dns_records_host ON dns_records (host);
CREATE INDEX dns_records_zone ON dns_records (zone);
CREATE INDEX dns_records_app ON dns_records (app_id);
