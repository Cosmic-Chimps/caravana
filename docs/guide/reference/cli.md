# The caravana command

`caravana` is one binary. Run with no command it is the Caravana server. With a command it does a
smaller job: managing the admin and tokens, running the server agent, serving coding agents, or
checking a template catalog.

`caravana help` prints:

```text
usage: caravana [flags]                 run the control plane (settings: CARAVANA_* and flags)
       caravana <command> [args]

commands:
  admin     the owner's CLI on the host: the admin, API tokens (caravana admin --help)
  node      the root agent on every server: join, sync, up (caravana node --help)
  server    register this machine as the cluster's first server (caravana server --help)
  mcp       a Model Context Protocol server for coding agents (caravana mcp --help)
  template  check and bundle an app template catalog (caravana template --help)
  version   print this build's version
  help      print this text
```

A first argument that is not a flag and not one of these commands is a usage error: `caravana servr`
does not start a second server by accident.

## On a host

The installer makes two small wrappers in `/usr/local/bin`:

- `caravana` runs the binary of the installed version as the `caravana` user, with the settings from
  `/etc/caravana/caravana.env` loaded, so it sees the same data directory as the server. It refuses to
  run without `sudo`. Use it for `version` and `admin`.
- `caravana-nomad` is Nomad's own command line, already pointed at this host with the management
  token: `sudo caravana-nomad status`.

The `node` commands need real root, not the `caravana` user, so they are run from the agent's own copy
of the binary, `/usr/local/libexec/caravana-node`, which the installer keeps outside the releases.

## The server

```bash
caravana -listen 127.0.0.1:7070 -data-dir ./data
```

Every setting is a flag and an environment variable: see [Settings](settings.md). On a host, systemd
starts the server; you do not run it by hand.

## `caravana version`

Prints the version of this build. A plain `go build` says `dev`; a release says its version.

```bash
sudo caravana version
```

## `caravana admin`

The owner's commands, run on the host. They open Caravana's database directly and work while the
server runs. Every command takes `-data-dir` (or `CARAVANA_DATA_DIR`), which the `sudo caravana`
wrapper already sets. `--email` names an active person. Without it, `reset-password`, `create-token`
and `revoke-token` act on the first owner; `passkeys list` lists everyone's passkeys and `passkeys
remove` finds the passkey by its ID alone (with `--email`, the passkey must be that person's).
`status` takes no `--email`.

| Command | What it does |
| --- | --- |
| `init --email <email>` | Creates the first owner. It asks for a password, 12 or more characters, twice. Refused once anyone exists. |
| `status` | Lists the people, their role and state, how each signs in, and when a guest's access ends. |
| `reset-password [--email <email>]` | Sets a new password, signs the person out everywhere and turns password sign-in back on. API tokens still work. |
| `passkeys list [--email <email>]` | Lists passkeys, with the ID `remove` needs. |
| `passkeys remove --id <id> [--email <email>]` | Removes a passkey. |
| `create-token --name <name> [--email <email>] [--expires-in <duration>]` | Makes an API token for a person and prints it on standard output. It is shown only once. `--expires-in` is a duration such as `720h`; the default is never. |
| `revoke-token --name <name> [--email <email>]` | Revokes the person's active token with that name. |

```bash
sudo caravana admin status
TOKEN="$(sudo caravana admin create-token --name ci --expires-in 720h)"
sudo caravana admin reset-password
```

Exit codes: 0 done, 1 failed, 2 a usage error, 3 the current state does not allow it (for example,
`init` when an owner exists). See [Sign-in](../host/sign-in.md) for forgotten passwords and passkeys.

## `caravana node`

The agent that runs as root on every server. The installer and a timer run it every 30 seconds; you
rarely need to. It asks the console for the server's peers, role and certificate and applies them.
Run it as root, from `/usr/local/libexec/caravana-node`.

| Command | What it does |
| --- | --- |
| `init-first --address ADDR [--cpu-total-compute N] [--issuer URL]` | Sets up the first server: keys, the cluster's certificate authority, Nomad's configuration and the network. The installer runs it. |
| `join --url URL --token TOKEN [--address ADDR] [--insecure] [--cpu-total-compute N]` | Joins this server to a cluster with a token made in the console. The installer runs it. |
| `up` | Makes the private network from what the agent last applied. Needs no network. |
| `sync [--renew] [--checks]` | Asks the console for this server's state and applies it. `--renew` renews the Nomad certificate now; `--checks` prints the checks the console reports. |
| `show FIELD` | Prints `public-key`, `name`, `index`, `role` or `first`. |
| `leave` | Takes this server out of the cluster. Refused on the first server. |
| `space` | Prints, as JSON, what Docker and Nomad's volume directories hold. It only reads. |
| `drop-certificates --names a.example.com,*.example.org [--dry-run]` | Removes from Traefik's store the certificates whose every name those names cover, which is what an uploaded certificate replaces. Prints the names it dropped. |

```bash
sudo /usr/local/libexec/caravana-node node show index
sudo /usr/local/libexec/caravana-node node sync --checks
```

`CARAVANA_INSECURE=1` (or `--insecure`) skips checking the console's certificate. To add or remove a
server, use the console and the installer: see [Servers](../host/servers.md).

## `caravana server`

```bash
caravana server adopt-first --name <name> --public-key <key> --address <ip>
```

Registers this machine as the cluster's first server. The installer runs it; you do not. It prints the
server's node token once, on standard output and nothing else. Run again, it rotates the token and
updates the key and address.

## `caravana mcp`

A Model Context Protocol server over standard input and output for coding agents. It runs on the
developer's machine, not on the host, and calls the API with a token.

```bash
CARAVANA_URL=https://caravana.example.com CARAVANA_TOKEN_FILE=~/.config/caravana/token caravana mcp
```

With `--allow-actions` it also offers the tools that change things. Setup, the token and the tools are
in [MCP for coding agents](../../mcp.md).

## `caravana template`

For people who maintain a template catalog. It runs anywhere and needs no server.

```bash
caravana template check ./caravana-templates
caravana template bundle --version 2026.11.26 ./caravana-templates ./out
```

- `check <dir>` reads every template, renders it with every combination of its choices and optional
  inputs, and runs it through Caravana's own Compose and job translators. A refusal, or a warning, fails
  it, and the command exits 1.
- `bundle [--version <v>] <dir> <outdir>` checks, then writes `catalog.json`, `catalog.v2.json` and
  `SHA256SUMS`, which is what a catalog release publishes. `--version` sets the catalog's version.

See [From a template](../deploy/templates.md).

## Next

- [Settings](settings.md)
- [Install](../install.md) and [Upgrade](../host/upgrade.md)
- [The HTTP API](api.md)
