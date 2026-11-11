-- Registry credentials per path: an entry is a host, or a host with a path
-- prefix ("ghcr.io", "ghcr.io/acme"). The key becomes (host, path); every row
-- that exists is a host-only entry, so its path is ''.
--
-- SQLite can't change a primary key, so the table is made again, the rows are
-- copied and the new table takes the old one's name. The secrets aren't here:
-- they live in the Nomad Variable caravana/registries/<key>, and the key of a
-- host-only entry is the one it had.
CREATE TABLE registries_new (
  host              TEXT NOT NULL,      -- lowercased; "docker.io" for Docker Hub; may hold ":port"
  path              TEXT NOT NULL DEFAULT '',  -- '' for the whole host, else a path prefix like "acme"
  username          TEXT NOT NULL,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  last_test_at      TEXT,               -- NULL until tested
  last_test_ok      INTEGER,            -- 1 ok, 0 refused or unreachable
  last_test_message TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (host, path)
) STRICT;

INSERT INTO registries_new (host, path, username, created_at, updated_at, last_test_at, last_test_ok, last_test_message)
  SELECT host, '', username, created_at, updated_at, last_test_at, last_test_ok, last_test_message
  FROM registries;

DROP TABLE registries;
ALTER TABLE registries_new RENAME TO registries;
