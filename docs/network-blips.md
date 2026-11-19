# Network blips: what happens to a server's copies when its link drops

Servers can sit at different providers and meet over the WireGuard mesh. A link between two of them, or
between a server and the one running Caravana, can drop for a few seconds or minutes while the server itself
runs fine. Caravana tells Nomad to ride out a short cut: the copies keep running and nothing is rebuilt.

## What a cut does

Nomad calls a server **disconnected** when it stops hearing it. That takes about 45 seconds: the heartbeat
Caravana's Nomad servers expect, plus `heartbeat_grace` of 30 seconds. When the lost server was the leader of
the Nomad servers, add up to a minute more (`failover_heartbeat_ttl`).

From then on, for `lostAfterSeconds` (300 by default), the copies on that server are kept, not stopped:

- A **stateless** service (no volume, no published port) gets **replacements** on other servers meanwhile, so it
  keeps its count. When the server comes back, the **original copies stay** and the replacements stop. Which
  copies stay is the same every time.
- A service with a **volume** or a **published port** gets no replacement: its copy can't start anywhere else
  (the data or the address is on that server), so it waits for its server. When the server comes back, the
  copy is as it was, with no restart and no outage beyond the cut itself.

If the cut lasts longer than `lostAfterSeconds`, the copies are called lost and replaced, and the old ones are
stopped when the server returns. That is what happened for every cut before this policy existed.

## Change how long copies are kept

Every service has the policy with the default. To change it, set `disconnect` on the service through the API
(`services[i].disconnect`) or the Compose overlay (`services.<name>.disconnect`):

```json
{
  "name": "web",
  "disconnect": {"lostAfterSeconds": 120}
}
```

`lostAfterSeconds` is 60 to 86400. A shorter time replaces the copies of a long cut sooner; a longer one waits
longer for a server that may come back. An empty `disconnect` is refused naming
`services[i].disconnect.lostAfterSeconds`: leave the field out for the default. A promotion takes the source's
policy, and Copy to keeps it. Nothing in the console edits it.

## The proxy needs an HTTP health path

Nomad does not remove the service registration of a copy it keeps on a disconnected server. Traefik reads those
registrations, so without more it would keep sending requests to the unreachable copy for the whole
`lostAfterSeconds`. A request over a cut link hangs until Traefik's dial timeout of 30 seconds.

So a service with a domain also gets a **Traefik health check**: Traefik calls the service's `health.path` on
every copy, at `health.interval` (10 seconds by default) with `health.timeout` (2 seconds), and stops sending to
a copy that doesn't answer. An unreachable copy leaves the load balancer within one interval. The check is on
the same path and timings as the Nomad one. When the service's copies change during the cut (the replacement
starts), Traefik rebuilds its load balancer and counts every copy as up until its next check, so a few requests
can fail once more then (in the cluster test, 2 calls in 120 seconds; before this policy a lost server cost
about 11).

That only works with an HTTP check on the domains' port. A service with a domain and a TCP health check (no
`health.path`), or an HTTP check on another port than its domains', can't be checked this way. Such a service:

- gets **no** disconnect policy, so it behaves as it did before: its copies are replaced after about 45
  seconds, and Traefik drops them with their registration;
- is refused when you set `disconnect` on it, naming `services[i].disconnect`. Give it a `health.path` on the
  domains' port first.

A service with no domain has no proxy in front of it and always gets the policy.

## What the console and API show

While Nomad keeps the copies of a cut-off server, the server is **disconnected**, not down:

- The server's page and the Servers list say "disconnected since 14:15 UTC · 16:15 local, 2 copies kept running
  there" in the warning colour, and the copies on it read `disconnected` (the Running tab, the server's own page).
  The same facts are in the API: a server has `state: "disconnected"`, `disconnectedSince` and `keptCopies`, and a
  copy has `status: "disconnected"`. A disconnected server takes no new work.
- The **needs-you list has no item** for it, and no notification is sent: the cut may be a blip. Its copies aren't
  counted as unhealthy or crash-looping either, and you can't run a command in them, open a terminal, or restart them.
- If the server is still away after `lostAfterSeconds`, Nomad calls its copies lost, the server becomes **down** and
  the `server_down` item opens, as it always did. The replacements stay and the copies on the returned server stop.
- If the server comes back first, it is ready again and nothing shows beyond the cut.

A server can be removed while it is disconnected, as one that is down can.

## After a deploy

The policy and the health check are part of the job, so a service gets them at its next deploy. Exporting an
app writes the `disconnect` block in the jobspec, and the health check is in the service's tags.