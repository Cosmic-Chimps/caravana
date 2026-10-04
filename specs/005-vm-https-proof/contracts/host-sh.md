# Contract: `deploy/vm/host.sh`

The operator's interface for the VM host, in the style of `deploy/dev/lab.sh`. Every subcommand
reads `deploy/vm/host.env` (see [data-model.md](../data-model.md)); a missing or empty variable
fails at once, naming it.

| Command | Does | Output | Exit |
| --- | --- | --- | --- |
| `host.sh up` | Checks DNS for both names (warns if they don't resolve to the host); builds Caravana for the host's architecture; copies files; runs `deploy/vm/setup.sh` with `sudo` on the host | progress, then `changed: <list>` (empty when nothing changed) and the URLs | 0 on success; non-zero naming the failed step |
| `host.sh env` | Reads the management token over SSH | `export NOMAD_ADDR=http://127.0.0.1:14646`, `export NOMAD_TOKEN=…`, `export CARAVANA_URL=http://127.0.0.1:17070` | 0 |
| `host.sh tunnel` | `ssh -N` with the three forwards (Caravana API 17070, Nomad 14646, Traefik dashboard 18081) | the local URLs | runs until Ctrl-C |
| `host.sh prove` | Opens the tunnel in the background, runs `go test ./cmd/caravana -run TestGate` with the gate env, closes the tunnel | one `PASS <step>` / `FAIL <step>: <why>` line per step, then `GATE PASSED` or `GATE FAILED` | 0 only when every step passed |
| `host.sh ssh [cmd…]` | `ssh` to the host (a shell, or the command) | — | the command's |

## `deploy/vm/setup.sh` (runs on the host, as root)

Idempotent; prints `changed: <name>` for each thing it changed. Environment it receives from
`host.sh up`: the five host variables, plus `CARAVANA_BINARY=<path of the copied binary>`.
Order: Nomad config → `deploy/dev/setup.sh` (bootstrap) → dummy interface → firewall → ACL
policies and tokens → Traefik job → Caravana user, env file, binary, unit → service health
check (`curl 127.0.0.1:7070/v1/health`).

## Gate test (`cmd/caravana/gate_test.go`)

`CARAVANA_GATE=1 go test ./cmd/caravana -run TestGate -v` with:

| Variable | Meaning |
| --- | --- |
| `GATE_API` | Caravana's API base URL (through the tunnel) |
| `GATE_WEBHOOK` | public webhook base URL, `https://<webhook-host>` |
| `GATE_APP_HOST` | the app's host name |
| `GATE_PUBLIC_IP` | the host's public IP, for port checks |
| `GATE_ACME_STAGING` | `1` when certificates come from staging (trust checks report "staging") |
| `NOMAD_ADDR`, `NOMAD_TOKEN` | Nomad through the tunnel |

Steps (subtests, in order; a failure stops the rest): `clean`, `deploy`, `webhook-redeploy`,
`webhook-unchanged`, `https-trusted`, `http-redirects`, `webhook-host-only-webhook`,
`same-payload`, `broken-reverts`, `nomad-runs-last-healthy`, `missing-tag`, `ports-closed`.
`TestLab` runs the same list minus the public-only steps (`https-trusted`, `http-redirects`,
`ports-closed`).
