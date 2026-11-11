### Added
- **Registry credentials per path.** An entry in Settings › Registries may now name a host and a path,
  like `ghcr.io/cosmic-chimps` and `ghcr.io/aerosens`, so two accounts that no single login can read
  both work on one registry. An image uses the most specific entry whose path covers it by whole
  segments (`ghcr.io/acme` covers `acme/shop`, never `acme-old/shop`). An entry with no path stays the
  fallback for every other image on that host, and an image no entry covers is pulled without
  credentials. Nothing is chosen per app: the image's own name says which entry applies, so Compose,
  CI, MCP and the Aspire publisher need no change.
- **Saving or removing an entry rewrites only the copies that use it.** Adding a prefix moves the
  apps under it off the host's entry, and removing one moves them back (or, with no host entry, leaves
  empty credentials, as before).
- **Test checks the login only.** A registry doesn't say which images a login can read, so for an
  entry with a path an accepted test says that the first deploy of an image under it shows that.
- API 0.35.0: `putRegistry`, `deleteRegistry` and `testRegistry` take an optional `path` query next to
  the host in the URL, and `Registry` has `path`.

### Changed
- In the console, a registry row's Test and Remove post the entry in a form field
  (`/settings/registries/test` and `/settings/registries/remove`) instead of putting the host in the
  address; "used by" counts the apps each entry covers.

### Upgrade
- Nothing to do: every existing entry is a host-only entry, with the same stored credentials and the
  same Nomad Variable. A new migration (0025) adds the path column.
