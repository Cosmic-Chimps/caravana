# 060 — OpenSearch-based apps (Wazuh): host map count, ulimits, sysctls, files, a Wazuh template

From CodeAlta task `gwfq` (2026-11-26): a colleague's server plan runs Wazuh on Caravana, with "systemd,
without Caravana" as the fallback. Today Wazuh's Compose file is refused, and OpenSearch would not start
anyway. Worktree branch `alta/copper-pelion-48z3`. API 0.66.0 (0.65.1 today). No migration unless a phase
says so. No new Go module.

## What a user gets

1. Every server has `vm.max_map_count` of at least 262144, set by the installer; the server page says the
   value the agent read.
2. A service may set `ulimits` (`memlock`, `nofile`) and namespaced `net.*` `sysctls`, in its definition and
   from Compose. Anything else is refused naming the field; a `vm.max_map_count` in a file is answered by the
   host setting.
3. A service may have **files**: a config file whose content is in the app, or a file whose content is one of
   the service's secrets (a certificate's key), mounted read-only at a path in every copy. Compose's inline
   `configs` (`content:`) map to them, and the overlay takes `files`.
4. The catalog has a Wazuh single-node template (indexer, manager, dashboard) whose certificates and
   password hashes are made at install.
5. A gate step runs a single-node OpenSearch with ulimits and a mounted config; docs say what the agent
   ports need and what a volume backup of the indexer costs.

## Decisions

**D1 Host setting: always, never lower.** The installer sets `vm.max_map_count = 262144` on every server
(first and joined), not only for services that ask: it is a limit, not a reservation, and Docker Desktop,
Ubuntu 24.04 (1048576) and Fedora already ship it at or above that. It is written as
`/etc/sysctl.d/05-caravana-map-count.conf`: files are read in name order and a later one wins, so a distro
file that sets more (Ubuntu's `10-map-count.conf`) still wins and ours is only a floor. When the running value
is under 262144 the installer also runs `sysctl -w vm.max_map_count=262144`, so no reboot and no
`sysctl --system`. The lab's `deploy/dev/setup.sh` does the same (the gate step needs it), and `lab.sh up`
applies it to an existing lab VM. No "only when needed" mode: a service can't say it needs it before it runs.

**D2 The agent reports the value.** `nodeagent.Report.VMMaxMapCount` (`vmMaxMapCount`, omitempty, read from
`<Root>/proc/sys/vm/max_map_count`; unreadable leaves it 0 = unknown). The console keeps it with the server's
report and shows it on the server page's facts line ("vm.max_map_count 262144"), in the warning colour with
"OpenSearch and Elasticsearch need at least 262144: rerun the installer" when lower. API `Server.vmMaxMapCount`
(null when unknown). No needs-you item: nothing on the server is broken until such an app runs, and the
app's own crash loop already is one.

**D3 Ulimits: `memlock` and `nofile` only.** `app.Service.Ulimits map[string]app.Ulimit` (`ulimits`,
omitempty), `Ulimit{Soft int64 "soft"; Hard int64 "hard"}`. Rules, each refused naming
`services[i].ulimits.<name>[.soft|.hard]`: name is `memlock` or `nofile` (others: "Caravana sets only memlock
and nofile"); `memlock` soft/hard are -1 (unlimited) or a non-negative number of bytes; `nofile`
1..1048576 (the kernel's default `nr_open`), never -1; soft ≤ hard unless hard is -1. Translate writes the
docker driver's `ulimit` (`{"memlock": "-1:-1", "nofile": "65536:65536"}` — the JSON job takes the block as a
list holding one map, as `auth`). Compose `services.*.ulimits.*` (single value and soft/hard forms) map to it;
paths `services.*.ulimits`, `.ulimits.*`, `.ulimits.*.soft`, `.ulimits.*.hard` join the allowlist and an
unknown name is refused at `services.<svc>.ulimits.<name>`.

**D4 Sysctls: namespaced `net.*` only, `vm.max_map_count` answered by the host.**
`app.Service.Sysctls map[string]string` (`sysctls`, omitempty): keys `^net\.[a-z0-9_]+(\.[a-z0-9_-]+)+$`, at
most 16, values 1..64 printable bytes without spaces at the ends. Any other key is refused at
`services[i].sysctls.<key>` with "<key> isn't namespaced: it can only be set on the server". Translate writes
the docker driver's `sysctl` (list holding one map). Compose `services.*.sysctls` (map or list form) maps to
it, with one exception: `vm.max_map_count` with a value ≤ 262144 is dropped **with a warning** on
`services.<svc>.sysctls.vm.max_map_count` ("set on every server by the installer (262144); a container can't
set it"), since the host already satisfies it (constitution IV: changed meaning → warning). A larger value is
refused. A bad `net.*` key that the kernel then refuses fails the copy's start as any bad config would.

**D5 Files: one model, two sources of content.** `app.Service.Files []app.File` (`files`, omitempty):
`File{Path string "path"; Content string "content,omitempty"; Secret string "secret,omitempty"; Mode string
"mode,omitempty"; UID *int "uid,omitempty"; GID *int "gid,omitempty"}`. Exactly one of Content and Secret
(both empty is refused at `files[k]` with "give content or secret"). Rules, each named at `services[i].files[k].<field>`:
- `path`: absolute, `path.Clean(p) == p`, ≤ 1024 bytes, printable without spaces or NUL, not `/`, not under
  `/proc`, `/sys`, `/dev`, `/alloc`, `/local`, `/secrets`, not `/etc/hosts`, `/etc/hostname`,
  `/etc/resolv.conf`, not equal to a volume's mount path (a file **inside** a volume's mount is allowed: Docker
  mounts the deeper path last, and Wazuh's dashboard does this), unique in the service; at most 16 files.
- `content`: valid UTF-8 without NUL, ≤ 64 KiB; all plain contents of an app ≤ 256 KiB.
- `secret`: the name of one of the service's own `secrets`. **A secret a file names is written to that file and
  not into the environment** (a PEM's line breaks don't belong in an env var, and a key shouldn't be readable
  through `/proc/<pid>/environ`). It keeps every other secret behaviour: write-only, a provider reference or a
  template, keep-when-unnamed, previous sets kept for a revert, Refresh secrets.
- `mode`: octal `^0?[0-7]{3}$`, default `0644`; `uid`, `gid`: 0..65535, default Nomad's (root).

**D6 Files in the job.** Each file is a Nomad `template` plus a docker `mount` (`type = "bind"`, `source`
relative to the task directory, `readonly = true`):
- content file: `DestPath local/caravana-files/<k>`, `EmbeddedTmpl` the content as is, with `LeftDelim` and
  `RightDelim` chosen purely so neither occurs in the content (`[[cv0` / `cv0]]`, then `cv1`, …), so Nomad's
  template engine copies it byte for byte; `ChangeMode noop` (a new content is a new job version, so a new
  copy).
- secret file: `DestPath secrets/caravana-files/<k>` (Nomad's fs API can't read `secrets/`),
  `EmbeddedTmpl` reads the one item from the service's variable (`SecretsPath`, or the run's own variable for a
  schedule run) by ranging over `.Tuples` like the others, `ChangeMode noop`.
- `Perms` from `mode`, `Uid`/`Gid` when set. The env template (`secretsTemplate`) skips the items a file names
  (`{{ if not (eq .K "A" "B") }}`; the skip list is sorted so the job is stable).
- Bind mounts from inside the task directory work with Caravana's `volumes.enabled = false` docker plugin
  config (to be confirmed in the lab, phase E; if not, the plan changes before the gate).
`Translate` stays pure; `ScheduleRunJob` gets the same (secret files read the run's variable); the HCL export
writes `ulimit`, `sysctl`, `mount` and the template's `perms`, `uid`, `gid`, `left_delimiter`,
`right_delimiter` (goldens `ulimits`, `files`; the reflection test keeps it honest).

**D7 Compose.** Top-level `configs.<name>` with inline `content` (and optional `name`) plus a service's
`configs` (short form `- name` → target `/<name>`, long form `source`, `target`, `uid`, `gid`, `mode`) map to
content files. A config with `file`, `environment` or `external` is refused at `configs.<name>.<attr>`: "paste
the file's content inline (content: |) or send it in the overlay's files". Top-level `secrets` and a service's
`secrets` stay refused as today (secret values travel in the overlay). The overlay gains
`files []app.File` per service (content or secret); a file the overlay and the Compose file both put at one
path is refused at the overlay's entry. The kept Compose file holds content files (they are config, not
secrets); `composeDraft.keepAll` carries the overlay's files (`keepFiles`, like `keepIdentities`) so a Compose
deploy from the console never drops them.

**D8 Promotion, copy, Configure.** A promotion takes the source's `ulimits`, `sysctls` and `files` (they
belong to the image's configuration, like `entrypoint` and `dns`): `Changed` lists them; a file naming a
secret the target lacks fails validation at its field. Copy to keeps all three (secret values are names only,
so `missingSecrets` already lists what a file needs). Configure shows them read-only per service (files: path,
mode, "from secret NAME" or the size), the per-service form keeps them (`applyEdit` leaves what it doesn't
offer), and the Variables page marks a secret a file names "written to /path, not to the environment". No
editor in the console.

**D9 Template format: generated certificates and bcrypt.** Format 2 gains two things, used by Wazuh:
- `certificates`: one generated CA and its leaves, made at install with `crypto/x509` (RSA 2048, PKCS#8 keys,
  10 years, SHA-256; leaf usages server and client auth). Manifest: `certificates: {ca: {subject}, leaves:
  [{name, subject, dnsNames}]}` with `subject` an ordered list of `{type: CN|OU|O|L|ST|C, value}` so DNs like
  `CN=admin,OU=Wazuh,O=Wazuh,L=California,C=US` come out exactly. It yields secret-valued names `ca_cert`,
  `<name>_cert`, `<name>_key`, usable where generated secrets are (overlay secret values only). The CA's key is
  thrown away after signing. `Check` renders with stand-in PEMs, so it stays fast.
- a derived placeholder `{{<secret>|bcrypt}}` (`golang.org/x/crypto/bcrypt`, already a dependency, cost 12;
  OpenSearch reads its `$2a$` hashes) allowed only where its secret is (an overlay secret's value), so
  `internal_users.yml` can be a secret file with the generated passwords' hashes. Overlay secret values already
  take text around placeholders, so a whole config file can be one secret's value.
- Key generation and bcrypt salts take their randomness from the platform (Go's crypto ignores a caller's
  reader for keys since 1.26), so the package doc's "only randomness is the io.Reader" gains that exception;
  tests check structure (chain verifies, DNs exact, key matches, hash verifies), never bytes.
- Wazuh's API password policy wants a symbol, upper, lower and digit: the template writes
  `{{api_password}}.Wz1` wherever it uses it, so no new alphabet is needed.
A template using these sets `requires` to the release that has 060, so older hosts list it as a stub.

**D10 The Wazuh template** (`../caravana-templates`, branch `wazuh-template`, never pushed by me), from
`wazuh-docker` v4.14.11 single-node: services `indexer`, `manager`, `dashboard` (Caravana service names are
DNS labels, so `wazuh.indexer` becomes `indexer` everywhere in the configs; the certificates name the new
hosts), ulimits as Wazuh writes them, `OPENSEARCH_JAVA_OPTS=-Xms1g -Xmx1g`, memory reservations (indexer
1536 MB, manager 1024 MB, dashboard 768 MB), the manager's agent ports 1514 and 1515 published (55000 stays
internal: only the dashboard calls it), the dashboard on a domain input with `server.ssl.enabled: false`
(Traefik terminates TLS), every password generated (indexer admin, kibanaserver, the Wazuh API user), and a
volume backup policy for the manager only, with the indexer's trade-off in the notes. `lab: false` (it needs
about 4 GB). Its notes say: the agent ports and the host setting, the admin user, and that a volume backup
of the indexer stops it (use OpenSearch snapshots to a repository instead; not built here).

**D11 Gate step `opensearch`** (`cmd/caravana/gate_opensearch_test.go`): an `opensearchproject/opensearch`
single node (security plugin off, heap 512m, `memlock` -1 and `nofile` 65536) with `opensearch.yml` as a
content file and a `net.ipv4.tcp_keepalive_time` sysctl, plus a secret file read back with Run a command; it
deploys healthy (`/_cluster/health` through its domain), the copy's `ulimit -n` and `/proc/sys/net/...`
show the values, the secret file is in no job, export, answer, log or SQLite file, and a deploy with
`sysctls: {kernel.shmmax: ...}` is 400 naming `services[0].sysctls.kernel.shmmax`. Runs alone with
`-run 'TestLab/^(api-needs-token|opensearch)$'`; the lab VM (2 GiB) fits one 512m node.

**D12 Docs.** `docs/opensearch-apps.md` (operator doc: the host setting, ulimits, sysctls, files, the
agent-port publishing with a pointer to `docs/published-ports.md`, the backup trade-off), placed in the docs
site (`site/pages.mjs`, `site/sidebar.mjs`), the `docs/guide/` page that lists Compose support updated if it
lists attributes, `changelog.d/opensearch-apps.md`, `api/openapi.yaml` 0.66.0, and the CLAUDE.md paragraph.

## Phases

- **A** Host setting: installer, `deploy/dev/setup.sh` + `lab.sh up`, agent report, store/service/API/server page
  (D1, D2). `install-test.sh` gets a check that the file exists and `sysctl -n` ≥ 262144 (written, shellchecked).
- **B** Ulimits and sysctls through every layer (D3, D4, D8's parts).
- **C** Files through every layer (D5–D8).
- **D** Template format additions and the Wazuh template (D9, D10).
- **E** Gate step, docs, changelog, CLAUDE.md (D11, D12); lab runs.

Each phase: tests in the touched packages, `go vet`, `staticcheck`, `gofmt -l .`, the CGO-free cross-compile,
`tools/ui-css.sh check` when templates change. Commits per phase on the worktree branch.

## Constitution Check

- I Small by default: no service, no module. II One model: every source normalizes to `app.Service` fields;
  Translate pure. III Safe deploys: files and limits are part of the job version; secrets keep their
  declarative replacement. IV Fail loudly: every refusal names its field; the one dropped input
  (`vm.max_map_count` ≤ 262144) is a warning. V API-first: openapi in the same commits. VI Nomad-native: docker
  driver `ulimit`/`sysctl`/`mount` and Nomad templates, exportable. VII Secure: a secret file's value lives only
  in the service's Nomad Variable, rendered under `secrets/`; never in the job, the store, a log or a page; the
  template's CA key is never kept. VIII tests per package.

## Complexity tracking

None.

## Research

**R1 (lab, observed 2026-11-26, Nomad in the lab VM, `volumes.enabled` false):** a JSON job with
`"ulimit": [{"memlock": "-1:-1", "nofile": "65536:65536"}]`, `"sysctl": [{"net.ipv4.tcp_keepalive_time": "333"}]`
and two `"mount"` entries of type `bind` with sources `local/caravana-files/0` and `secrets/caravana-files/1`
registered and ran: `ulimit -n` 65536, `ulimit -l` unlimited, the sysctl 333, both files mounted read-only.
A template with `LeftDelim` `[[cv0` / `RightDelim` `cv0]]` rendered `a: {{ not a template }} ${HOME} ${X} [[x]]`
byte for byte (Nomad doesn't interpolate `${…}` in `EmbeddedTmpl`), with `perms` 0640 and `uid`/`gid` 1000 kept
on the mounted file (and 0400 root for the secret one). In HCL, `${` in a template's `data` must be written `$${`
(the HCL parser read `$${X}` as `${X}`), so the export escapes it. Docker didn't keep the output of a container
that exited within a second; the gate's checks run in a long-lived copy. The lab VM is Ubuntu 24.04, whose
`vm.max_map_count` is already 1048576.
