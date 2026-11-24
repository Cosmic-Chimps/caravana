### Added
- **Change the console's address from the console.** Operator › Console address checks a new domain (its name, its DNS
  and, once Traefik has the route, a certificate for it) before anything moves, and a Switch then moves Caravana to it
  within about a minute: no SSH session, no rerun of the installer. The console never gets root: the server agent that
  runs beside Caravana rewrites the public URL and the installer's record of the domain after checking both as text,
  restarts Caravana and puts the old files back if it doesn't answer. If a Cloudflare zone is connected and has no
  record for the domain, **Create the record in the zone and check** makes it.
- **The old address keeps working for a while.** For 0, 7, 30 or 90 days (30 by default) the old domain stays routed and
  keeps its certificate: its pages redirect to the same path on the new address, and its `/v1/…` API and
  `/.well-known/…` discovery documents are still served, so CI tokens, Git webhooks and joined servers keep working
  until you update them. The page lists what holds the old address: passkeys (they belong to a domain; password
  sign-in stays on at the new one), webhook, deploy and API tokens, the workload identity issuer, and joined servers,
  which follow by themselves. See `docs/console-address.md`.
- API 0.57.0: `getConsoleAddress`, `startConsoleAddressChange`, `getConsoleAddressChange`, `cancelConsoleAddressChange`
  and `switchConsoleAddress`, with the codes `domain_not_here`, `console_address_change_open`,
  `console_address_unsupported`, `console_address_change_not_found`, `console_address_not_cancellable`,
  `console_address_not_ready` and `console_address_check_failed`. Deploy tokens are refused.

### Upgrade
- Rerun the installer once on an existing host before the first change: Traefik's console route now reads its hosts
  from a Nomad Variable, so Traefik restarts once (a few seconds without HTTP). Until then the page asks for the rerun.
  The agent that applies the switch is the one servers already use, so nothing else is installed.
