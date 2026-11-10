### Added
- **Add a server.** Operator › Servers lists every server with its CPU, memory and disk, and **Add
  server** makes a command to run on a new Ubuntu 24.04 machine. Its token works once and for one hour.
  The page shows the checks as they pass: connected, Docker running with its free disk, reaches the other
  servers on the private network, ready to take work. The new server installs Docker, Nomad and WireGuard
  and runs no Caravana service and no Traefik: it takes copies, and public traffic still enters through
  the first server.
- **A private network between servers.** Servers form a WireGuard mesh (`caravana-mesh`, UDP 51820).
  Nomad's own traffic, the apps' ports and the containers' addresses travel on it, so Traefik reaches a
  copy on any server and the services of an app reach each other by name across servers.
- **Nomad's traffic between servers is authenticated and encrypted**, from the first install: TLS on its
  RPC with certificates Caravana issues for each server's own key, and an encrypted gossip. A small root
  agent on every server (`caravana-node`, on a 30-second timer) keeps the server's peers, role and
  certificate in line with the console, and renews the certificate 30 days before it ends without
  restarting Nomad. The console still can't run anything as root: the agent applies values it checks and
  takes no command or path from the console.
- **Three servers survive the loss of one.** With three servers ready, the two oldest joined ones run
  Nomad's scheduler too, so it has three voters. A service with more than one copy has its copies on
  different servers. When a server is lost its copies are replaced on the others, the Servers page shows
  it as down, and it is on the needs-you list and sent to every notification channel. Losing the first
  server keeps the scheduler and the other servers' copies running, but the console and public traffic are
  down until it is back: both live there.
- **Remove a server** that is down, or cancel a join nobody ran, from its page. A server that is running
  is taken out with `install.sh --leave` on it first.
- Each server has its own page, numbers and disk warning; the Running tab names the server of each copy,
  Volumes say which server they are on, and Monitoring's host section has a server select.
- API 0.34.0: `listServers`, `getServer`, `addServer`, `getServerJoin`, `removeServer`, the agent's calls
  (`joinServer`, `syncNode`, `renewNodeCertificate`, with credentials of their own), `getHost?server=`,
  `server` on volumes and on the disk item, and the needs-you kinds `server_down` and
  `server_certificate_expiring`.

### Fixed
- A restart of Nomad (an installer run that changes Nomad) could replace running copies: systemd gave a
  container's network interface a new address just after it was made, and Nomad, checking each copy
  after its restart, no longer knew it. The installer now tells systemd to leave those addresses alone.
  Copies started before that can still be replaced once, by the next Nomad restart.

### Changed
- All volumes of one service are created on the same server. Before, with more than one server, Nomad
  could have placed them on different ones and the service could never start.
- `caravana <a word it doesn't know>` prints the usage and exits 2. It used to start the server.

### Upgrade
- Rerun the installer on your server. It sets up the private network's interface and Nomad's
  certificates, and Nomad restarts twice, once only (the second restart moves its raft address to the
  private network). Apps keep running, but Nomad may replace a copy whose network it can't verify after
  the restart: an app with one copy may not answer for a few seconds. Until then, Add server says to run
  the installer first.
- To add servers, open UDP 51820 between them at your provider's firewall. The ranges `10.97.0.0/16`,
  `10.98.0.0/24` and `10.99.0.0/24` must be free on every server.
