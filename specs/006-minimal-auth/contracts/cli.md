# Contract: the `caravana admin` CLI

Run on the host, as the user that owns the data directory (`caravana` on the VM). It works
whether the server is running or not (research R4). Every command takes `-data-dir`
(`CARAVANA_DATA_DIR`), default `data`, like the server.

Passwords are never flags. With a terminal, the CLI prompts twice without echo; without one, it
reads one line from standard input (R5).

| Exit code | Meaning |
| --- | --- |
| 0 | Done |
| 1 | Failed (the message says why) |
| 2 | Bad usage: unknown command or flag, missing required flag |
| 3 | The current state doesn't allow it: `init` when an admin exists, `status` when none does |

## `caravana admin init --email <email>`

Creates the one admin. Refuses (exit 3) when an admin exists, naming `reset-password`.

```console
$ caravana admin init --email you@example.com
Password (12+ characters):
Password again:
caravana has its admin: you@example.com. Sign in at /login.
```

## `caravana admin status`

```console
$ caravana admin status
admin: you@example.com
$ echo $?
0
```

With no admin it prints `no admin yet: run caravana admin init --email <email>` and exits 3.

## `caravana admin reset-password`

Sets a new password without the old one. Ends every session; API tokens keep working.

```console
$ caravana admin reset-password
New password (12+ characters):
New password again:
Password changed. Every browser was signed out; API tokens still work.
```

## `caravana admin create-token --name <name> [--expires-in <duration>]`

Prints only the token on standard output, so scripts can capture it. Everything else goes to
standard error. `--expires-in` is a Go duration (`720h`); without it the token never expires.
Needs an admin (exit 3 without one: tokens act as the admin).

```console
$ TOKEN="$(caravana admin create-token --name ci-github --expires-in 2160h)"
Token ci-github created; it expires 2027-01-02T10:00:00Z. It is shown only now.
```

## `caravana admin revoke-token --name <name>`

Revokes the active token with that name. Exit 1 if there is none.

## On the VM: `deploy/vm/host.sh admin <command> [args…]`

Runs `caravana admin <command> [args…]` on the host over SSH (with a terminal, for the prompts)
as the `caravana` user, with the service's environment file. For example:

```console
$ deploy/vm/host.sh admin init --email you@example.com
```

`deploy/vm/host.sh prove` uses `admin status`, `admin create-token --name gate-<run>` and,
when it ends, `admin revoke-token --name gate-<run>`.
