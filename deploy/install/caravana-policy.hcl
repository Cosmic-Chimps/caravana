# ACL policy for Caravana's own Nomad token: register jobs (submit-job also covers purging one,
# for Delete app), follow their deployments and read
# their copies and output (read-logs, for the console's Logs tab and GET /v1/apps/{id}/logs), in
# the default namespace. Not the management token: Caravana never needs to manage ACLs, nodes or
# other namespaces, so a leaked caravana.env can't do more than deploy and read what it deployed.
# The variables rule is the apps' secrets (feature 008): one variable per service under
# nomad/jobs/<app>/, which only that service's tasks can read.
# The host volume capabilities are the apps' volumes (feature 009): Caravana creates a dynamic host
# volume "<app>_<volume>" before the first deploy that names it and deletes it with the app
# (host-volume-create also allows reading them). The host_volume rules let Caravana's jobs mount
# those volumes and not the host's own traefik-acme (Traefik's certificates and keys). Nomad
# accepts only letters, digits, "-" and "*" in these names, so a rule can't match "_"; an exact
# name wins over a glob, so the deny holds.
# alloc-lifecycle and read-fs are volume backups (feature 016): Caravana restarts a copy's tasks
# (alloc-lifecycle) so its caravana-volumes task backs up or restores the volumes while the service
# is stopped, then reads the result file that task wrote (read-fs). Not alloc-exec: Caravana never
# opens a shell in an app's container.
# install.sh applies it on every run, so running the installer again brings a host up to date.
namespace "default" {
  capabilities = ["submit-job", "read-job", "list-jobs", "read-logs", "host-volume-create", "host-volume-delete", "alloc-lifecycle", "read-fs"]

  variables {
    path "nomad/jobs/*" {
      capabilities = ["read", "write", "destroy", "list"]
    }
    # The backup destination's secret key (feature 010). Backup jobs read their own copy under
    # nomad/jobs/<app>_<service>_backup; submit-job also covers forcing a backup run.
    path "caravana/*" {
      capabilities = ["read", "write", "destroy", "list"]
    }
  }
}

host_volume "*" {
  policy = "write"
}

host_volume "traefik-acme" {
  policy = "deny"
}
