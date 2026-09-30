# ACL policy for Traefik's workload identity: list and read service registrations, nothing else.
namespace "default" {
  capabilities = ["read-job"]
}
