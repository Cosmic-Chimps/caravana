-- Feature 023: domains. What Caravana's own DNS and certificate checks found
-- for each host (a cache the page and the needs-you list read), the shared
-- passwords of Team-only domains (hashes only), and the one certificate setting.

CREATE TABLE domain_checks (
  host           TEXT PRIMARY KEY,
  app_id         TEXT NOT NULL,
  service        TEXT NOT NULL,
  state          TEXT NOT NULL CHECK (state IN
    ('dns_elsewhere', 'cert_expiring', 'cert_pending', 'dns_none', 'self_signed',
     'redirect', 'live', 'unknown', 'not_served')),
  dns_status     TEXT NOT NULL CHECK (dns_status IN ('ok', 'elsewhere', 'none', 'unknown')),
  dns_seen       TEXT NOT NULL DEFAULT '[]',        -- JSON array of the addresses the resolvers answered
  dns_expected   TEXT NOT NULL DEFAULT '[]',        -- JSON array of this host's addresses at check time
  cert_status    TEXT NOT NULL CHECK (cert_status IN
    ('issued', 'pending', 'self', 'expiring', 'error', 'none')),
  cert_issuer    TEXT NOT NULL DEFAULT '',
  cert_not_after TEXT,                              -- RFC 3339, UTC; NULL when there is none
  checked_at     TEXT NOT NULL
) STRICT;
CREATE INDEX domain_checks_by_app ON domain_checks (app_id);

-- One shared password per Team-only domain. The password itself is never
-- stored: this is its bcrypt hash.
CREATE TABLE team_passwords (
  host       TEXT PRIMARY KEY,
  app_id     TEXT NOT NULL,
  hash       TEXT NOT NULL,
  created_at TEXT NOT NULL                          -- the last rotation
) STRICT;
CREATE INDEX team_passwords_by_app ON team_passwords (app_id);

CREATE TABLE certificate_settings (
  id               INTEGER PRIMARY KEY CHECK (id = 1),
  warn_before_days INTEGER NOT NULL DEFAULT 14 CHECK (warn_before_days IN (7, 14, 30)),
  updated_at       TEXT NOT NULL
) STRICT;
INSERT INTO certificate_settings (id, warn_before_days, updated_at)
VALUES (1, 14, strftime('%Y-%m-%dT%H:%M:%f000000Z', 'now'));
