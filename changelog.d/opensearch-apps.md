### Added
- **Apps that need limits, kernel settings and files, such as OpenSearch and Wazuh.** A service may set `ulimits`
  (`memlock` and `nofile`), network `sysctls` (`net.…` only) and read-only `files`: a config file whose content is in
  the app, or a file that holds one of the service's secrets (a key). A secret a file holds is written to the file and
  never to the environment. Compose's `ulimits`, `sysctls` and inline `configs` map to them, and the overlay takes
  `files`. Anything else is refused naming the field. Configure shows them read-only, a promotion takes them, and the
  export writes them. See "Limits, sysctls and files" in the guide.
- **Every server has `vm.max_map_count` of at least 262144**, which OpenSearch and Elasticsearch need. The installer sets
  it (a larger value already there is kept), and the server's page shows the value once its agent reports it, in the
  warning colour when it is lower. A Compose file's own `vm.max_map_count` is answered by the server: 262144 or less is dropped with a warning,
  more is refused.
- **Templates can make certificates and password hashes at install** (catalog format 2): `certificates` asks for a
  certificate authority and the certificates it signs, and `{{password|bcrypt}}` writes the bcrypt hash of a secret.
  The Wazuh template, which will be in the catalog with this release (it needs 0.24.0), uses both.

### Upgrade
- **Run the installer again on each server** to set `vm.max_map_count`: on the first server, **Update to X…** on
  Operator › Updates (it runs the installer) or the installer command; on a joined server, its join command again.
  Setting it needs no reboot and restarts no app.
