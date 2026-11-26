### Added
- **Back up an OpenSearch or Elasticsearch cluster without stopping it.** The new backup engine `opensearch`
  (`backup: {engine: opensearch, repository: /usr/share/opensearch/snapshots}`) has the cluster take a snapshot of itself
  through its own API while it runs, then archives it to the backup destination. It keeps the newest `keep`, pins,
  copies to a second destination, restore schedules and records like every other engine. A restore closes and replaces
  the snapshot's indices while the cluster runs; indices made since stay. A backup holds the indices whose name doesn't
  start with a dot (not the cluster's settings, templates, security configuration or Dashboards' objects). The cluster
  needs the folder in its `path.repo` setting, which a deploy warns about when it is missing, and the login is found
  in `OPENSEARCH_INITIAL_ADMIN_PASSWORD`, `INDEXER_PASSWORD` or `ELASTIC_PASSWORD`. The Compose form of New app and the
  Backups tab's **Change the policy…** offer it. See "OpenSearch and
  Elasticsearch snapshots" in Database backups.

### Upgrade
- An app that already runs gets what the engine needs (a sidecar that mounts the snapshot folder) at its next deploy,
  not before; a new policy is a deploy anyway.
