-- Feature 017: registries Caravana has credentials for. The secret isn't here:
-- it lives in the Nomad Variable caravana/registries/<key>.
CREATE TABLE registries (
  host              TEXT PRIMARY KEY,   -- lowercased; "docker.io" for Docker Hub; may hold ":port"
  username          TEXT NOT NULL,
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  last_test_at      TEXT,               -- NULL until tested
  last_test_ok      INTEGER,            -- 1 ok, 0 refused or unreachable
  last_test_message TEXT NOT NULL DEFAULT ''
) STRICT;
