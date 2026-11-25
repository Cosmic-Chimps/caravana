# Plan 056: Drop the earlier certificates an upload covers, from the console

Spec: `spec.md`. Migration 0047, API 0.64.0 (merged 2026-11-25 after 055 (0044, 0.61.0) and 057 (0045, 0.62.0); 034 is 0046 and 0.63.0).

## Decisions

**D1. The server agent does it, not Caravana.** Traefik's store is the host volume `traefik-acme`, the folder
`/var/lib/caravana-host/traefik` on the ingress server (the first). Caravana's policy denies the volume on purpose
and the console never gets root. The server agent (031) is root on that server and already applies what the console
hands it after checking every value (045's clean-up, 051's console address): it gets a third such request.

**D2. The request is an ID and names, nothing else.** The sync answer gains `certificateDrop: {id, names}`: `id` is 32
lowercase hex digits, `names` the names of every uploaded certificate (deduplicated, sorted). No path, file, command
or certificate: the agent knows its store folder. The agent checks the names as text: 1 to 1000 of them, each a
lowercase host name (labels of `a-z 0-9 -`, no dash at either end, 1–63 characters, at least two labels, at most 253
characters) or `*.` and such a host name. A request whose ID isn't 32 hex digits is ignored with one line on standard
error (it can't be answered); bad names, or an agent without the store folder, are the outcome `refused` with a fixed
sentence. The answer is not refused for them (051's rule), so the other steps of the sync go on.

Trust: a console that sent names no upload has could make the agent drop ACME certificates Traefik then orders
again at its restart (an extra order, a few seconds on Traefik's own certificate). That is less than what the console
can already do (change the issuer, 034), so no new power is given.

**D3. One rule, in Go, used by the console's request and by the installer.** A stored ACME certificate is dropped
when every one of its names (`domain.main` and `domain.sans`) is covered by a name of the request: equal (case
folded), or `*.rest` and the host is exactly one label more than `rest`. That is the installer's rule (and
`certs.Covers`). The store files are the regular files (not symlinks) of the folder named `acme.json` or
`acme-<12 hex>.json` (`deploy.ACMEStorage`), at most 32 MiB each. Each file is parsed as JSON keeping everything it
doesn't change (`json.RawMessage` for the resolvers' other members and for each kept certificate), and a changed
file is written through a temporary file in the same folder and a rename, mode, owner and group kept. A file that
isn't a Traefik store (not JSON, not an object of resolvers) is left alone and the outcome is `failed` naming the
file's base name only. Nothing of a certificate, key or account is ever printed, logged or reported: only the names
of what was dropped (`a.example.com` or `a.example.com+www.example.com` per certificate, at most 100 entries).

`caravana node drop-certificates --names a,b,*.c [--dry-run]` runs the same rule; `install.sh`'s
`drop_covered_acme_certificates` calls it (`--dry-run` to decide whether Traefik must stop, then without) instead of
its embedded Python, so the installer no longer needs `python3` for it.

**D4. Traefik is restarted, not stopped.** The agent rewrites the files while Traefik runs, then Caravana restarts
Traefik's allocation through Nomad (`deploy.RestartCopy`, `alloc-lifecycle`, in the policy since 016). Traefik reads
its store once at start and writes it whole only when it obtains or renews a certificate, so the drop holds unless
Traefik obtains or renews one in the seconds between the rewrite and the restart: then it writes its old store back,
the uploaded row still says "Still on their earlier certificate" after the recheck, and the owner drops again. A
write of Traefik's in the milliseconds of the rewrite itself would lose that new certificate from the file, and Traefik
would order it again at its restart. Rejected:
- Stopping the job from the console: Caravana would have to stop Traefik a sync ahead of the agent (up to 30 s
  without HTTP), and starting it again is the installer's `job run`.
- `docker kill` by the agent: Nomad counts it against the system job's restart attempts (`mode = "fail"` would leave
  no proxy).
- `docker pause` around the rewrite: an agent that died between pause and unpause would leave the proxy frozen, to
  shield a window of milliseconds.

**D5. The restart follows at once.** After a run whose outcome is `dropped`, the agent posts its report again right
away (the same `POST /v1/node/sync`), so Caravana restarts Traefik within a second rather than at the next 30 s
sync. The answer of that second post is not applied: the timer's next sync does that. A failed second post changes
nothing; the next sync reports the same outcome.

**D6. The agent's outcome.** `state.json` and the report gain `certificateDrop: {id, state, dropped, reason}` with
`state` one of `running` (saved before any file is touched), `dropped` (one or more certificates removed; `dropped`
lists them), `nothing` (no stored certificate is covered: nothing written), `failed` (a file couldn't be read, parsed
or written; `reason` a fixed sentence) or `refused` (D2). The rule is idempotent, so a request the agent finds
`running` (it was stopped in the middle) is simply run again when the console hands it again, and says `dropped`
(with no names) when nothing is left, since its files may already have been rewritten and Traefik must still be
restarted; an ID with a final outcome is never run twice. Only a certificate whose every name is a host name (D2's
rule, lower-cased) is dropped, so the names reported are clean; the installer passes only the uploads' names that
are host names. The report leaves the block out when there is none.

**D7. The console's record.** Table `certificate_drops` (migration 0047): `id`, `server` (the server row's index),
`status` (`requested`, `running`, `restarting`, `done`, `failed`, `lost`), `dropped` (JSON array of strings),
`error`, `actor` (the person's email, 054), `requested_at`, `started_at`, `rewritten_at`, `finished_at`; a partial
unique index allows one open drop (`requested`, `running`, `restarting`). `RequestCertificateDrop` needs an admin
(certificates are installation settings, 034), at least one uploaded certificate (`ErrNoUploadedCertificates`), the
ingress server's agent reporting that it holds the store (D8, else `ErrNoAgent` in the words "run the installer on
server <name>"), and no open drop (`ErrCertificateDropRunning`). It answers the record (API 202).

**D8. Which agent.** The ingress server is the managed server whose row is `First`. Its agent reports
`traefikStore: true` when the store folder exists; an agent older than this feature doesn't send it, so the console
refuses rather than hand a request nobody will run. The report must be fresh (045's `agentFresh`, 10 minutes) and
the server `ready`.

**D8b. The hand-off** (`certificateDropHandoff` in `NodeSync`, beside `cleanupHandoff`, with `serverMu` held): only
the ingress server's sync is concerned.
- A report whose `certificateDrop.id` is the open drop's and whose state is final settles it: `dropped` → status
  `restarting` with `dropped` and `rewritten_at` (the restart is D9's); `nothing` → `done` with no dropped names;
  `failed`/`refused` → `failed` with the agent's reason (checked: printable, at most 300 bytes).
- A `requested` drop is marked `running` when handed out.
- While a drop is `requested` or `running` and not settled, the answer carries `certificateDrop` with the names of
  the uploaded certificates read at that moment.

**D9. The restart and its pass.** Settling a drop as `restarting` starts the restart in the background (on the
service's context, never inside the agent's request): `ProxyCopy`, then `RestartCopy`. The drop is `done` once
Traefik's running copy has started after `rewritten_at` (`Copy.StartedAt`), whoever restarted it: the restart call
returning is not enough. The certificate pass (every minute, `SetCertificateDropPassGap`) settles what got stuck:
- `requested` not handed out within 10 minutes → `failed` "The server's agent didn't pick it up (is
  caravana-node.timer running?)";
- `running` without an outcome within 10 minutes → `lost` "The server's agent never told how it went.";
- `restarting`: `done` when Traefik has started since `rewritten_at`; else the restart is tried again; after 10
  minutes → `failed` "The earlier certificates were removed from Traefik's store, but Traefik couldn't be restarted:
  it still serves them, and writes them back when it next saves its store. Drop again once Traefik runs."
After `done` with dropped names, the covered hosts are checked again (`scheduleCertRecheck` with every upload's
names), so the uploaded rows update without waiting for the watcher.

**D10. API 0.64.0.** `POST /v1/certificates/drops` (`dropEarlierCertificates`, 202, `Location`),
`GET /v1/certificates/drops` (`listCertificateDrops`, newest first, at most 20), `GET /v1/certificates/drops/{id}`
(`getCertificateDrop`). Schema `CertificateDrop {id, server, status, dropped, error, actor, requestedAt, startedAt,
finishedAt}`. Codes: `no_agent` (409, exists since 045), `certificate_drop_running` (409),
`no_uploaded_certificates` (409), `certificate_drop_not_found` (404). Admin only (`accessRules`); deploy tokens are
denied by the default deny. The node sync's answer and report gain the blocks of D2 and D6 (`syncNode`'s schemas).

**D11. Console.** Settings › Certificates: when an uploaded certificate has hosts "Still on their earlier
certificate", the sentence about running the installer is replaced by a form `POST /settings/certificates/drop`
with the button **Drop the earlier certificates** and one sentence: "Traefik restarts once: a few seconds without
HTTPS." It answers 303 to `/settings/certificates?drop=<id>#drop`. A panel (`id="drop"`) shows the newest drop
while it is open or ended within the last hour: "Waiting for server <name>'s agent", "Dropping…", "Restarting
Traefik…", "Dropped the earlier certificates of a.example.com, b.example.com.", "Nothing to drop: every earlier
certificate also names a host no upload covers.", or the failure's sentence. It is a part
(`/parts/settings/certificates/drop`) polled every 3 s while the drop is open and answering 286 once it is not, with a
`noscript` refresh (051's panel). A refusal draws the page again with the sentence at the form (409), never a
redirect. Forms work without JavaScript. Admin only (`consoleAccess`).

**D12. Docs and tests.** `docs/certificates.md`'s "When a domain still shows its earlier certificate" says to press
the button (and that the installer's rerun does the same). Research R5's last sentence points here. A
`changelog.d/056-drop-earlier-certificates.md` note. Tests:
- `nodeagent`: the rule as a pure function over file bytes (table: exact, wildcard one label, two labels not
  covered, mixed names kept, several resolvers, unknown members kept, empty and `null` stores, not JSON), the folder
  walk (symlink and other files ignored, mode and owner kept), the request's validation, the outcomes and their
  saving, a `running` run again, the second post, and the CLI command;
- `service`: request refusals, the hand-off, settling every outcome, the restart and the pass with `deploytest`;
  no certificate PEM or key in any row, log or answer (a sentinel in the fake store never reaches the console:
  the agent only reports names);
- `api` and `ui`: the routes, the access walks, parity, the openapi sync test, the panel and the refusal;
- `deploy/dev/install-test.sh`: `check_certificates_host` gains a console drop after its rerun drop (inject the fake
  ACME certificates again, `POST /v1/certificates/drops`, wait for `done`, the store without `$APP` and with
  `other.test`, Traefik's allocation restarted, `$APP` verified with the upload) and runs under `ONLY=certificates`.
