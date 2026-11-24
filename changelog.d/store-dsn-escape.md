### Fixed
- A data directory whose path holds `#`, `?`, `%` or a space no longer makes the database open at a shorter path
  (SQLite read the rest as a fragment or a query). The path is escaped when the store opens.
