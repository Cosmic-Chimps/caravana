### Added
- **Apps that need limits, kernel settings and files, such as OpenSearch and Wazuh.** A service may set `ulimits`
  (`memlock` and `nofile`), network `sysctls` (`net.…` only) and read-only `files`: a config file whose content is in
  the app, or a file that holds one of the service's secrets (a key). A secret a file holds is written to the file and
  never to the environment. Compose's `ulimits`, `sysctls` and inline `configs` map to them, and the overlay takes
  `files`. Anything else is refused naming the field. Configure shows them read-only, a promotion takes them, and the
  export writes them. See "Limits, sysctls and files" in the guide.
- **Every server has `vm.max_map_count` of at least 262144**, which OpenSearch and Elasticsearch need. The installer sets
  it (a larger value already there is kept), and the server's page shows the value, in the warning colour when it is
  lower. A Compose file's own `vm.max_map_count` is answered by the server: 262144 or less is dropped with a warning,
  more is refused.
- **Templates can make certificates and password hashes at install** (catalog format 2): `certificates` asks for a
  certificate authority and the certificates it signs, and `{{password|bcrypt}}` writes the bcrypt hash of a secret.
  The Wazuh template, in the catalog repository, uses both.

### Upgrade
- **Rerun the installer on each server** to set `vm.max_map_count`. It needs no reboot and no restart of your apps.
