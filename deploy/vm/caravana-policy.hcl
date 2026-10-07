# ACL policy for Caravana's own Nomad token: register jobs (submit-job also covers purging one,
# for Delete app), follow their deployments and read
# their copies and output (read-logs, for the console's Logs tab and GET /v1/apps/{id}/logs), in
# the default namespace. Not the management token: Caravana never needs to manage ACLs, nodes or
# other namespaces, so a leaked caravana.env can't do more than deploy and read what it deployed.
# The variables rule is the apps' secrets (feature 008): one variable per service under
# nomad/jobs/<app>/, which only that service's tasks can read.
# setup.sh applies it on every run, so `host.sh up` brings an existing host up to date.
namespace "default" {
  capabilities = ["submit-job", "read-job", "list-jobs", "read-logs"]

  variables {
    path "nomad/jobs/*" {
      capabilities = ["read", "write", "destroy", "list"]
    }
  }
}
