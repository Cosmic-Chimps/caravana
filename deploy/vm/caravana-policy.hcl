# ACL policy for Caravana's own Nomad token: register jobs and follow their deployments, in the
# default namespace. Not the management token: Caravana never needs to manage ACLs, nodes or
# other namespaces, so a leaked caravana.env can't do more than deploy.
namespace "default" {
  capabilities = ["submit-job", "read-job", "list-jobs"]
}
