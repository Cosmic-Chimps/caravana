### Added
- **DNS servers, search domains and options per service.** A service can now set the resolvers its
  copies use: up to 3 nameserver IP addresses, up to 6 search domains and up to 8 resolver options
  such as `ndots:2`. Nomad writes them to the container's `resolv.conf`, and a schedule's fresh
  copies get the same ones. Set them in the API (`dns` on a service) or in a Compose file with `dns`,
  `dns_search` and `dns_opt`. Configure shows them read-only, and a promotion takes the source's. API 0.44.0.
