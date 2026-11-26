# Placement and drain: say where services run, and move work off a server

With more than one server, Caravana places copies where Nomad finds room, spreading the copies of a service over
the servers. Three things let you say more: **labels** on servers, **placement rules** on services, and **moving work
off** a server you want to empty.

## Labels

A label is a word, with a value, on a server: `ssd=true`, `region=eu`, `gpu=a10`. Give a server its labels on its
page (Operator › Servers › the server › Labels) or through the API:

```http
PUT /v1/servers/two/labels
{"labels": {"ssd": "true", "region": "eu"}}
```

A name is lowercase letters, digits and dashes (32 at most); a value is letters, digits and `. _ : / -` (64 at most);
a server has 16 labels at most. In the console a bare word (`ssd`) means `ssd=true`.

Caravana writes the labels to the server's Nomad node as node metadata (`caravana.label.<name>`) with the server's
own name (`caravana.server`), and keeps them equal to what you saved: at the save, when a server joins, when
Caravana starts, and every five minutes. Nomad's servers see a new label a few seconds after it is written; the save
waits for that, so a deploy right after it already counts the label. A server that is down keeps its labels and gets
them back when it returns.

## Placement rules

A service may carry `placement` with three rules, through the API, the Compose overlay
(`services.<name>.placement`) or the Placement panel of Configure › Services:

```json
{
  "name": "db",
  "image": "postgres:17",
  "placement": {
    "require": {"ssd": "true"},
    "prefer": [{"label": "region", "value": "eu", "weight": 80}],
    "avoid": ["three"]
  }
}
```

- **require**: labels a server must have. A copy runs only on a server that has every one. At most 8.
- **prefer**: labels the scheduler favours when more than one server fits, each with a weight from 1 to 100 (50
  when left out). A copy still runs elsewhere when no preferred server has room. At most 8. A preference changes
  where the *next* copies go: the copies of the next deploy land on the preferred server; running copies don't move.
- **avoid**: names of servers the service never runs on. At most 16. It is a `!=` test on the name Caravana writes
  on every managed server, so it can only keep copies off servers Caravana manages (a Nomad node it doesn't manage,
  like the lab's, is never avoided).

The rules become `constraint` and `affinity` blocks on the service's group in the Nomad job (the export writes them),
and a schedule's runs of the service carry them too. A deploy whose rule no server satisfies is refused before anything
is registered, with `409 placement_impossible` and the field (`services[0].placement.require.gpu`): the running
copies keep running. As with any deploy Nomad turns down, the definition is saved with the rule and the failed
deployment is on the Deployments tab, so fix the rule or label a server and deploy again. Lack of room is not a refusal: Nomad places the copies when room frees up, as before, and the
needs-you list says `copies can't be placed` while a service with rules has copies waiting.

A promotion takes the source's rules (labels and server names belong to the cluster, not to an environment); Copy to
keeps them.

## Moving work off a server

To empty a server before a reboot, an upgrade or retiring it: Operator › Servers › the server › **Move work off…**,
or `POST /v1/servers/two/drain` with `{"deadlineSeconds": 900}`. The confirmation page lists what will move and
what will stay. Caravana then:

1. tells Nomad the server takes no new work (it is *ineligible*);
2. moves each movable copy, one at a time: the copy is stopped and rescheduled, which Nomad places on a server that
   takes work, and Caravana waits for the new copy to run and pass its health checks before the next one;
3. ends when nothing movable is left (`done`), or when the deadline passes with copies left (`incomplete`, a needs-you
   item), or when you cancel it.

The server page follows the move and shows what moved and what stays. Afterwards the server still takes no work until
you choose **Accept work again** (`DELETE /v1/servers/two/drain`), which also cancels a move in progress; copies already
moved stay where they are.

**What never moves**, each listed with its reason:

- a service with **volumes**: its data is on that server (moving a volume between servers is not built);
- a service with a **published port**: the port is on the first server's address;
- **Traefik** on the first server: it is the ingress, and Caravana itself runs there, so the first server is drained
  of apps only;
- a **backup or schedule run** that ends on its own, and anything Caravana didn't deploy.

**A single copy has a gap.** Nomad places the replacement and stops the old copy in one step, so a service with one
copy is unreachable while its new copy starts elsewhere. A service with two or more copies keeps answering: the copies
move one at a time. The move needs at least one other server that accepts work, or it is refused
(`409 no_other_server`).

Why not Nomad's own `node drain`? It migrates every allocation on the node and has no way to leave a job out: it would
stop Traefik on the first server and take the console down, and stop a service whose volume is on that server with no
server to place it on. Caravana uses Nomad's eligibility and its allocation stop instead, and keeps the deadline and the
order itself.

## Hosts

Writing node metadata and eligibility needs `node:write` in Caravana's Nomad policy. The installer applies the policy
on every run, so a host installed before this feature has it after you **rerun the installer**. Until then, saving a
label or moving work off says that Nomad refused and to run the installer again.
