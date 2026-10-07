# ACL policy for Caravana's own Nomad token: register jobs (submit-job also covers purging one,
# for Delete app), follow their deployments and read
# their copies and output (read-logs, for the console's Logs tab and GET /v1/apps/{id}/logs), in
# the default namespace. Not the management token: Caravana never needs to manage ACLs, nodes or
# other namespaces, so a leaked caravana.env can't do more than deploy and read what it deployed.
# The variables rule is the apps' secrets (feature 008): one variable per service under
# nomad/jobs/<app>/, which only that service's tasks can read.
# The host volume capabilities are the apps' volumes (feature 009): Caravana creates a dynamic host
# volume "<app>_<volume>" before the first deploy that names it and deletes it with the app
# (host-volume-create also allows reading them). The host_volume rule lets Caravana's jobs mount
# those volumes, and only those: their names always contain "_", which the host's own volumes
# (traefik-acme) don't.
# setup.sh applies it on every run, so `host.sh up` brings an existing host up to date.
namespace "default" {
  capabilities = ["submit-job", "read-job", "list-jobs", "read-logs", "host-volume-create", "host-volume-delete"]

  variables {
    path "nomad/jobs/*" {
      capabilities = ["read", "write", "destroy", "list"]
    }
  }
}

host_volume "*_*" {
  policy = "write"
}
