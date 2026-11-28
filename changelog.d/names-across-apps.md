### Added
- **Apps of one project and environment reach each other by name.** Every service with ports answers
  to `<app>-<service>` for the other apps of its environment, and to `<app>` when it is the app's only
  service with ports: the PostgreSQL template installed as `postgres` is reached at
  `postgres-postgres:5432`, and a lone Postgres service in an app named `postgres` at `postgres:5432`. A name two
  services claim answers to neither, and says so. Configure's Overview, the Running tab, the template's
  installed notice and `getApp`'s `addresses` say where each service answers.

### Upgrade
- Every app gets the names sidecar and its internal registration at its next deploy: redeploy both apps
  of a pair before they reach each other by name. The sidecar reserves 16 MB and 20 MHz per group, now in
  one-service apps too.
