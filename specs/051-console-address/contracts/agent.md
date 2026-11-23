# Agent contract — console address (extends 031's contracts/node.md)

## Report (agent → console), new members

```json
"runsConsole": true,
"consoleAddress": {"id": "<32 hex>", "state": "applied|failed|refused", "reason": "<one printable line, ≤300 bytes>"}
```

- `runsConsole` is true when this agent's node env URL is `http://127.0.0.1:7070` and `/etc/caravana/caravana.env`
  exists. Left out (false) otherwise.
- `consoleAddress` is the outcome of the last request this agent handled, from `state.json`; left out when none.

## Answer (console → agent), new members

```json
"consoleURL": "https://console.example.com",
"consoleAddress": {"id": "<32 hex>", "domain": "console.example.com"}
```

- `consoleURL`: the console's public URL when it is `https://host[:port]` (`ValidConsoleURL`), else left out.
- `consoleAddress`: present only for an agent that reported `runsConsole`, while a change is `switching`. An older
  agent ignores both (the answer is decoded without unknown-member checks).

## Validation (nothing is applied unless all pass; a failure is the outcome `refused`)

- `id` matches `^[0-9a-f]{32}$`; when invalid the outcome can't name it, so nothing is recorded and the agent logs one
  line.
- `domain` is a host name by the installer's rule (lower case letters, digits, dashes and dots; labels 1–63 bytes,
  no dash at either end; at least one dot; at most 253 bytes; not all-numeric labels like an IPv4 address). Nothing
  else is accepted: no port, path, scheme, space, quote, newline or NUL.
- This agent `runsConsole`.
- An `id` whose outcome is already in `state.json` is not applied again (its outcome keeps being reported).

## Apply (the server that runs Caravana)

1. Save `consoleAddress {id, state: "running"}` in `state.json` first.
2. `/etc/caravana/caravana.env`: it must hold exactly one line starting `CARAVANA_PUBLIC_URL=`; replace that line with
   `CARAVANA_PUBLIC_URL=https://<domain>`. `/var/lib/caravana-host/install.env`: replace the `CARAVANA_DOMAIN=` line
   with `CARAVANA_DOMAIN=<domain>`, or append one when missing. Each file is written to a temporary file in the same
   directory with the original's mode, owner and group, then renamed over it. The values need no quoting: the domain's
   characters are all in the installer's `envq` plain set. Nothing else in either file changes.
3. `systemctl restart caravana`; then `GET http://127.0.0.1:7070/v1/health` must answer 200 within 60 s.
4. Healthy: outcome `applied`. Not healthy: put both original files back, restart Caravana, outcome `failed` with
   "Caravana didn't answer at https://<domain> within 60 s; the old address is back". A file that can't be read,
   holds no or several `CARAVANA_PUBLIC_URL=` lines, or can't be written: `failed`/`refused` with a sentence, files
   untouched.
5. A `running` outcome found at the start of a sync (the agent stopped mid-apply): if the env file already names the
   domain and Caravana is healthy, `applied`; else put back what can be (the files are only renamed when complete) and
   `failed` "the agent stopped during the change".

## Follow (joined servers)

When the node env URL isn't loopback and `consoleURL` differs from it: `GET <consoleURL>/v1/health` with the agent's
usual TLS setting (`CARAVANA_INSECURE`); on 200 save the node env with the new URL (token and insecure unchanged) and
log one line. On any failure keep the URL and try at the next sync.
