### Changed
- **The installer refuses a container.** Run inside a Docker or LXC container, or on a node of a
  container platform (Virtuozzo, Jelastic), the installer now stops before changing anything and says
  so: Caravana runs Docker and Nomad and makes network interfaces, a firewall and a private network,
  which a container doesn't allow. Install it on a VM or a physical server.
