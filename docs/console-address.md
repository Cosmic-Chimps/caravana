# Changing the console's address

The console answers on one domain, chosen when Caravana was installed (the installer's `--domain`, by default
`<public-ip>.sslip.io`). **Operator › Console address** changes it from the console, with no SSH session and no
rerun of the installer: Caravana checks the new domain first, moves nothing until you press Switch, and keeps the
old address working for a while.

The usual case is going from the `sslip.io` default to a real domain once its DNS is ready.

## What changes, and what doesn't

Changes:

- **The console's address.** Caravana's public URL (`CARAVANA_PUBLIC_URL` in `/etc/caravana/caravana.env`) and the
  installer's own record of the domain (`CARAVANA_DOMAIN` in `/var/lib/caravana-host/install.env`), so a later run
  of the installer keeps the new domain.
- **Traefik's route to the console.** The new domain is added beside the current one, and Traefik asks for its
  certificate. Traefik is not restarted.
- **The workload identity issuer** (`docs/workload-identity.md`) follows the public URL, one Nomad server at a time.
  Anything that trusts the old issuer, such as Bella Baxter's Nomad attestor, needs the new one.
- **The passkeys' domain.** A passkey belongs to the domain it was made on, so the passkeys you have do not work
  at the new address (see below).

Does not change: your apps and their domains, their certificates, secrets, volumes and backups, the cluster's
private network, and any server already joined (they follow by themselves).

## The change, step by step

1. **Point DNS at this server first.** The new domain needs an `A` record (and `AAAA` if the server has an IPv6
   address) for this server's public address. The page shows this server's `<a-b-c-d>.sslip.io` name, which is
   also the address to go back to if you ever need to. If a Cloudflare zone is connected (Settings › DNS
   providers) and covers the domain but has no record for it, the page offers **Create the record in <zone> and
   check**, and Caravana makes it there.
2. **Check.** Type the domain, choose how long to keep the old address, and press Check. Caravana refuses, before
   recording anything, a name that isn't a host name, an IP address, the current domain, a domain an app already
   uses, and a domain whose DNS doesn't point at this server (it says what it saw). Nothing has changed at this
   point.
3. **The checks run.** Caravana puts the domain in Traefik's console route beside the current one, then looks every
   10 seconds, for up to 5 minutes, for two things: that Traefik routes the domain to Caravana, and that a
   certificate was issued for it (on an installation installed with `--acme none`, Traefik's own certificate counts).
   The panel shows each check. When both pass the change is **ready**. When they don't pass in time it is
   **check failed** and the domain leaves the route again; fix the cause and check again. **Cancel** ends a change
   that has not been switched and takes the domain out of the route.
4. **Switch.** Caravana checks DNS, the route and the certificate once more, without waiting, and hands the change to
   the server agent (below). Within about a minute Caravana restarts at the new address. The panel says where, with a
   link: open it and **sign in there**, because the session cookie belongs to the old host.
5. **Done.** The change shows as **switched**. If the agent can't finish it the change is **failed** (it put the old
   address back) or **refused**, with a one-line reason; if no outcome arrives in 5 minutes it is **lost**, and the
   page points at the agent's log on the server (`journalctl -u caravana-node`).

The same steps are the API: `getConsoleAddress`, `startConsoleAddressChange`, `getConsoleAddressChange`,
`cancelConsoleAddressChange` and `switchConsoleAddress` (`api/openapi.yaml`). Only a full API token or a signed-in
session may call them; a deploy token is refused.

## How the switch happens without giving the console root

Caravana, the web console, never runs as root. The server agent (`caravana-node`, the same one that adds servers) does:
it runs as root on the first server, beside Caravana, every 30 seconds. At the next sync after the Switch, Caravana's
answer carries the request: an ID and the domain, nothing else. The agent checks both as plain text (an ID of 32 hex
digits, a host name by the installer's rule; a space, quote, slash, port or newline refuses the request), then:

1. rewrites exactly one line in `/etc/caravana/caravana.env` and one in `/var/lib/caravana-host/install.env`;
2. restarts `caravana` and waits up to 60 seconds for its health check at the loopback address;
3. reports `applied`, or, if Caravana doesn't answer, puts both files back, restarts it again and reports `failed`.

The agent never takes a path, a command or a file name from the console. A request it has already handled is not
applied twice. On a host with no agent beside Caravana (a host installed before servers could be added and not
upgraded since), the switch is refused with `no_agent`: run the installer once more on that server.

## The old address

For the **redirect window** you chose with the change (7, 30 or 90 days; 0 means none) the old domain stays in
Traefik's route and keeps its certificate:

- its **pages** answer `308` to the same path and query on the new address, so bookmarks work;
- its **API and discovery documents** (`/v1/…` and `/.well-known/…`) are still served, because CI sends a bearer
  token that a redirect to another host drops, GitHub doesn't follow a webhook redirect, and joined servers and
  Bella read the issuer's discovery documents there.

When the window ends Caravana takes the old domain out of the route (within a minute). Before then, update what
still holds the old address: CI's `CARAVANA_URL`, the webhook URLs in your Git hosts, any script that calls the API.
The page counts the webhook tokens, deploy tokens and API tokens that exist, since their senders are what holds the
old URL. With 0 days the old domain leaves the route at the switch.

## Passkeys and signing in again

A passkey belongs to a domain, so a passkey made at the old address does not work at the new one. Nobody is locked
out: password sign-in stays on at a domain that has no passkey, even when you turned it off, so you can sign in with
the password at the new address and add a passkey there (Settings › Sign-in). The page counts the passkeys that
belong to the current domain. Passkeys stay registered for the old domain, which is useful if you go back.

## Joined servers

Every joined server's agent calls the console at the address it joined with. The console tells every agent its
current public URL at each sync, and an agent whose URL differs switches to it once the new address answers its health
check. The old domain's `/v1/…` keeps answering for the window, so a server that was off during the switch catches up
within it. After the window ends, rerun that server's installer with `--url https://<new domain>`.

## Upgrading an existing host

Rerun the installer once on an existing host before the first change. The console's route in Traefik's job now reads its
hosts from a Nomad Variable that Caravana writes, which an older job doesn't, so Traefik restarts once (a few seconds
without HTTP on ports 80 and 443). Until then the page says the installation can't change its address and asks for
the rerun. A host that never changes its address behaves exactly as before.

## What it can't do

- It doesn't change the domain of an app (Domains does) or the name of a server.
- It doesn't create a DNS record outside a connected Cloudflare zone: elsewhere, make the record by hand.
- It changes the first server's console only; Caravana's data lives on that server.
- A valid Let's Encrypt certificate needs the domain to reach this server from the internet on ports 80 and 443; the
  check fails with its message until it does.
