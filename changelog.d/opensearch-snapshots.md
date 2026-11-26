### Added
- **Back up an OpenSearch or Elasticsearch cluster without stopping it.** The new backup engine `opensearch`
  (`backup: {engine: opensearch, repository: /usr/share/opensearch/snapshots}`) has the cluster take a snapshot of itself
  through its own API while it runs, then archives it to the backup destination. It keeps the newest `keep`, pins,
  copies to a second destination, restore schedules and records like every other engine. A restore closes and replaces
  the snapshot's indices while the cluster runs (queries on them fail meanwhile, though the service never stops), makes
  again an index deleted since, and leaves indices made since. A backup holds the indices whose name doesn't
  start with a dot (not the cluster's settings, templates, security configuration or Dashboards' objects). The cluster
  needs the folder in its `path.repo` setting, which a deploy warns about when it is missing, and the login is found
  in `OPENSEARCH_INITIAL_ADMIN_PASSWORD`, `INDEXER_PASSWORD` or `ELASTIC_PASSWORD`. The service needs a port (the first one is the
  cluster's HTTP API) and `sh`, `curl`, `grep`, `sed` and `mktemp` in its image. The Compose form of New app offers
  the engine, and so does **Change the policy…** on the Backups tab for a service that already has a backup policy; a
  service with none is offered only **Back up daily** with the volume engine, so set the `opensearch` policy through
  the API or a Compose request. See "OpenSearch and Elasticsearch snapshots" in Database backups.

### Upgrade
- An app that already runs gets what the engine needs (a sidecar that mounts the snapshot folder) at its next deploy,
  not before; a new policy is a deploy anyway.
