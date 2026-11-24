# ACL policy for Traefik's workload identity: list and read service registrations, and read the
# variables Caravana writes for it (feature 034): the custom ACME issuer and the uploaded
# certificates. Nothing else under caravana/.
namespace "default" {
  capabilities = ["read-job"]

  variables {
    path "caravana/acme" {
      capabilities = ["read"]
    }
    path "caravana/certificates/*" {
      capabilities = ["read", "list"]
    }
  }
}
