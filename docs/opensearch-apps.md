# Apps that need limits, kernel settings and files (OpenSearch, Wazuh)

Some apps are fussy about the machine they run on. OpenSearch and Elasticsearch need many memory maps, a high limit
of open files and a locked-memory limit. A security platform such as Wazuh wants its configuration in files, and its
certificates and keys in files too. This page says what Caravana offers for each, and what it refuses, so these apps
deploy without a hand-made setup on the server.

- **The server** gets `vm.max_map_count` set by the installer. See [The server's map count](#the-servers-map-count).
- **A service** may set its **ulimits**, a few **sysctls** and **files**.
- A **template** installs a whole stack: [Wazuh](#the-wazuh-template).

## The server's map count

OpenSearch maps a file for each shard and refuses to start when the kernel allows too few maps. Linux allows 65530
by default; OpenSearch wants at least **262144**.

The installer sets `vm.max_map_count` to 262144 on **every server** (the first one and the ones you add), in
`/etc/sysctl.d/05-caravana-map-count.conf`, and sets the running value too, so nothing needs a reboot. It is a limit,
not a reservation: it costs nothing until an app uses the maps. A file that sorts later and sets a higher number
(Ubuntu's `10-map-count.conf` sets 1048576) still wins, so Caravana's file is only a floor.

To see the value:

- Open **Operator › Servers**, then the server. The **Server** facts list `vm.max_map_count`. A number under 262144 is
  shown in the warning colour, with *262144 is the least OpenSearch and Elasticsearch need. Run the installer on this server again.*
- Or on the server: `sysctl vm.max_map_count`.

A server installed before this feature has the default until you **rerun the installer** (see
[Keeping Caravana up to date](guide/host/upgrade.md)). Nothing else changes, and nothing restarts.

A container can't set `vm.max_map_count`: it belongs to the whole server. So a Compose file that asks for it is
answered by the host: a value of 262144 or less is dropped, with a warning that says every server already has it; a
larger value is refused.

## Ulimits

A service may set two process limits for its copies: `memlock` and `nofile`.

```json
"ulimits": {
  "memlock": { "soft": -1, "hard": -1 },
  "nofile":  { "soft": 65536, "hard": 65536 }
}
```

- `memlock` is the locked-memory limit, in bytes. `-1` is unlimited.
- `nofile` is the number of open files, from 1 to 1048576 (the most the kernel allows). It is never `-1`.
- `soft` may not be above `hard`. A limit of `-1` counts as above every number.
- Any other name (`nproc`, `core`) is refused, naming `services[0].ulimits.<name>`. Caravana sets only these two.

In Compose, write `ulimits:` as Compose does: a number (`nofile: 65536`, the same soft and hard), or `soft` and `hard`.
A refusal names the path in your file, for example `services.web.ulimits.core`.

Copies of the service, and runs of its schedules, get the limits. **Configure › Services** shows them under **Also
set** as **Limits** (`nofile 65536 (soft) · 65536 (hard)`, or `memlock unlimited` when soft and hard are the same);
change them through the API or the Compose file.

## Sysctls

A service may set a few kernel settings inside its own copies, as `sysctls`:

```json
"sysctls": { "net.ipv4.tcp_keepalive_time": "600" }
```

Only **network** settings (`net.…`) are allowed. They belong to the container's own network, so changing one changes
nothing on the server. Any other setting is a setting of the whole server, and a container would change everyone's:
those are refused, naming the field (`services[0].sysctls.kernel.shmmax`), with *it isn't namespaced: it can only be
set on the server*.

- At most 16 sysctls. A value is 1 to 64 printable characters, with no space at either end.
- A name looks like `net.ipv4.tcp_keepalive_time`: lowercase, in parts separated by dots.
- A name the kernel doesn't know makes the copy fail to start, like any other bad setting.
- **Configure › Services** shows them as **Kernel settings**, read-only.
- In Compose, `sysctls:` takes a map or a list (`- net.core.somaxconn=1024`). `vm.max_map_count` is the one other
  setting Compose files carry: see [The server's map count](#the-servers-map-count).

## Files

A service may have **files**: a file at a path inside every copy, read-only. There are two kinds.

| Kind | What the file holds | Where the content lives |
| --- | --- | --- |
| **Content** | A config file: `opensearch.yml`, `nginx.conf`. | In the app. It is part of the app's definition and of its exports, so nothing secret goes there. |
| **Secret** | A certificate's key, a password file. | In one of the service's own **secrets**. The value is write-only, like any secret. |

```json
"secrets": [{ "name": "TLS_KEY", "value": "-----BEGIN PRIVATE KEY-----\n…\n-----END PRIVATE KEY-----\n" }],
"files": [
  { "path": "/usr/share/opensearch/config/opensearch.yml", "content": "cluster.name: shop\nnetwork.host: 0.0.0.0\n" },
  { "path": "/certs/node.key", "secret": "TLS_KEY", "mode": "0400", "uid": 1000, "gid": 1000 }
]
```

Each file has exactly one of `content` and `secret`. The other fields:

- `path`: absolute and clean (no `..`, no `//`, no trailing `/`), without spaces, up to 1024 bytes. Not `/`, not in
  `/proc`, `/sys`, `/dev`, `/alloc`, `/local` or `/secrets`, not `/etc/hosts`, `/etc/hostname` or
  `/etc/resolv.conf`. It may not be a volume's mount path; a path **inside** a volume is fine. A path is used once.
- `mode`: octal, `0640` or `640`. The default is **`0644`**: readable by every user in the container. Give a key
  `0400` and the owner that runs the app.
- `uid` and `gid`: 0 to 65535. The default is root. A key that the app reads as a normal user needs the app's own id
  (OpenSearch and Wazuh run as 1000).
- Limits: 16 files per service, 64 KiB of content per file, 256 KiB of content for the whole app. A content file is UTF-8
  text without NUL characters. `{{ … }}` and `${…}` in it stay as they are.

### A secret in a file is not in the environment

A secret that a file holds is written to that file and **not** to the environment. A key's line breaks don't
belong in an environment variable, and it should not be readable through `/proc/<pid>/environ`. Every other secret of
the service still reaches the app as an environment variable.

Apart from that, the secret behaves as any other: it is write-only, may come from a [secret
provider](secret-providers.md), may be a template, is kept when a deploy names it without a value, and a deploy
replaces it. The value is in Nomad's variable store and in the file inside the copy, and nowhere in Caravana's
database, the job, the export or a log. The **Variables** page marks it: *Written to /certs/node.key, not to the
environment.*.

### Changing a file

A file is part of the app, so changing it is a deploy: the new content, or the secret's new value, reaches the app
with the next copies. Configure shows each file read-only under **Files**: its path, `mode 0400`, and its size or `from secret TLS_KEY`.
Set files through the API or Compose. A [promotion](guide/operate/projects.md) takes the source's files (not the
secret values: those belong to each environment), and **Copy to** keeps them.

## Files and ulimits in a Compose file

Compose's `configs` with inline `content` become content files:

```yaml
services:
  indexer:
    image: opensearchproject/opensearch:2.19.1
    ulimits:
      memlock: { soft: -1, hard: -1 }
      nofile: { soft: 65536, hard: 65536 }
    sysctls:
      net.ipv4.tcp_keepalive_time: 600
    configs:
      - source: opensearch
        target: /usr/share/opensearch/config/opensearch.yml
        mode: 0644
        uid: "1000"
configs:
  opensearch:
    content: |
      cluster.name: shop
      network.host: 0.0.0.0
```

- A `configs` entry as a name alone (`- opensearch`) is mounted at `/opensearch`, as Compose does.
- `mode` is octal. `uid` and `gid` are numbers: a name can't be looked up in the image.
- A config with `file:`, `environment:` or `external:` is refused at `configs.<name>.<attribute>`. Caravana reads nothing
  from your disk or its own environment: paste the content inline, or send the file in the overlay (below).
- Compose treats `$` as the start of a variable: write `$$` for a literal `$`.
- Top-level `secrets` and a service's `secrets` stay refused: secret values travel in the overlay.

Keys and other secret files go in the overlay's `files`, next to the `secrets` they name:

```json
{
  "compose": "…the file…",
  "services": {
    "indexer": {
      "secrets": [{ "name": "TLS_KEY", "value": "-----BEGIN PRIVATE KEY-----\n…" }],
      "files": [{ "path": "/certs/node.key", "secret": "TLS_KEY", "mode": "0400", "uid": 1000 }]
    }
  }
}
```

An overlay file and a `configs` entry at the same path are refused at the overlay's entry
(`services.indexer.files[0].path`). The console's Compose form keeps the overlay's files when you deploy a file to an
app that has them; it has no input to set them.

## Ports for agents

Wazuh's agents connect to the manager on ports 1514 and 1515. Those are not web traffic, so Traefik doesn't carry
them. Publish them on the server's address: see [Published ports](published-ports.md). The template does it for you.
Open the two ports in your cloud provider's firewall, and point each agent at the server's IP address or a DNS name
for it (not the dashboard's domain, which goes through the HTTPS proxy).

## The Wazuh template

**New app › From a template › Wazuh** installs Wazuh 4.14 on one server as three services:

| Service | What it is |
| --- | --- |
| `indexer` | OpenSearch, where alerts are kept. 1.5 GB reserved, with ulimits and its config and keys as files. |
| `manager` | Receives the agents' events on 1514 and 1515 (published). 1 GB reserved. |
| `dashboard` | The web page, on the domain you type, with HTTPS from Caravana. 768 MB reserved. |

Plan for about **4 GB** of memory for the three. The template asks for:

- **A domain** for the dashboard. Its DNS must point at the server.
- **An admin password**. It is the dashboard's `admin` user, and the manager's login to the indexer. Keep a note of it:
  Caravana doesn't show it again. It is at most 72 bytes, since the indexer keeps its hash.

Everything else is made at install and never shown: a certificate authority and the certificates of the indexer,
the manager and the admin (RSA 2048, valid ten years; the authority's key is thrown away), the password of the dashboard's
user and the one of the API. Each certificate is a secret, mounted as a file with mode `0400` where it has a key.
The indexer's users file holds only `admin` and `kibanaserver`, with the bcrypt hashes of their passwords: Wazuh's demo
users are left out.

**Backups.** The manager's volumes are backed up every day at 03:00 UTC. A volume backup stops the service while it
runs, so the manager stops for a moment (agents reconnect on their own). The **indexer is not backed up**: stopping it
for a volume backup would stop the dashboard's searches, and its alerts come from the manager. If you need the
indexer's data kept, use OpenSearch's own snapshots to a repository; Caravana doesn't build that yet.

The template is in the catalog repository, not necessarily in the catalog built into your Caravana: **From a template**
fetches a newer catalog once a day, and **Check now** does it at once (see [From a template](guide/deploy/templates.md)).
It needs a Caravana that has the features on this page. An older one lists it as unavailable.

## A single OpenSearch node of your own

This definition is what the project's own tests deploy, through the API. It runs one node with the security plugin off,
so keep it on a private network or behind Team only:

```json
{
  "source": { "kind": "image" },
  "services": [{
    "name": "search",
    "image": "opensearchproject/opensearch:2.19.1",
    "env": [
      { "name": "discovery.type", "value": "single-node" },
      { "name": "DISABLE_INSTALL_DEMO_CONFIG", "value": "true" },
      { "name": "OPENSEARCH_JAVA_OPTS", "value": "-Xms512m -Xmx512m" }
    ],
    "files": [{
      "path": "/usr/share/opensearch/config/opensearch.yml",
      "content": "cluster.name: shop\nnetwork.host: 0.0.0.0\nplugins.security.disabled: true\n"
    }],
    "ulimits": { "memlock": { "soft": -1, "hard": -1 }, "nofile": { "soft": 65536, "hard": 65536 } },
    "ports": [{ "name": "http", "containerPort": 9200 }],
    "health": { "path": "/_cluster/health" },
    "resources": { "memoryMB": 1024, "memoryMaxMB": 1536 },
    "domains": [{ "host": "search.example.com", "port": "http" }]
  }]
}
```

The security plugin is turned off in the file, not with `DISABLE_SECURITY_PLUGIN`: the image appends that setting to
`opensearch.yml` when the variable is set, and it can't append to a file that is mounted read-only.

## See also

- [Published ports](published-ports.md)
- [From a Compose file](guide/deploy/compose.md)
- [Variables and secrets](guide/configure/variables.md)
- [Volumes](guide/configure/volumes.md) and [Backups](guide/operate/backups.md)
