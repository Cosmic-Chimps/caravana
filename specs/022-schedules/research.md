# Research: Schedules (022)

Read-only research on the code at `main` + `022-schedules` (0.12.0) and on the vendored Nomad
(`hashicorp/nomad/api@v0.0.0-20260917172403-9dcbdc5e64ec`, and the matching server source
`nomad@v1.11.0-rc.1.0.20260917172403-9dcbdc5e64ec` in the module cache, cited below as `nomad:`
paths). Nothing was run in the lab; where a claim needs the lab it says so ("lab check").

One finding changes the plan's wording: `docs/plan.md` row 022 and the spec's Input section say
"as a Nomad periodic batch job (the shape backups already use)". R1 recommends against that shape
for schedules. The spec's user stories and requirements do not depend on it.

## R1. Job shape for a run

### What backups do today

- A Postgres backup is a periodic batch job per service: `nomadjob.BackupJob`
  (`internal/nomadjob/backup.go:85-115`) with `Periodic{Specs, ProhibitOverlap: true, TimeZone:
  "UTC"}`. `batchJob`/`batchGroup` (`backup.go:147-169`) give it `Datacenters ["*"]`, bridge
  networking, restart attempts 0 / mode fail and no reschedule, so a failed run ends instead of
  retrying for minutes.
- Its credentials are not the app's: the job reads its own variable `nomad/jobs/<job>`
  (`BackupVariablePath`, `backup.go:35`), rendered as env by `variableTemplate` (`backup.go:173-181`,
  `Envvars: true`, `ChangeMode: noop`). `deploy.SyncBackups` writes that variable
  (`internal/deploy/backups.go:103-152`: `writeVariable`, then `Jobs().Register`) after every
  deploy (`internal/service/deploy.go:352`, `syncBackups` in `service/backups.go:152`), copying the
  service's `POSTGRES_*` from its env or its secrets.
- A volume backup is not a job at all: Caravana keeps the clock (`internal/service/schedule.go`,
  `dueSlot` with `hashicorp/cronexpr`, one pass every `scheduleGap` = 30 s, `service.go:109`).
- "Run now" is `Jobs().PeriodicForce` (`deploy/backups.go:218-229`); Nomad accepts it with
  `submit-job` (`nomad:nomad/periodic_endpoint.go:44-49` allows dispatch-job, submit-job or
  force-periodic-job), so the policy needed nothing new.

### Why a run job can't just read the app's secrets

The app's secrets are at `nomad/jobs/<app>/<service>` (`nomadjob.SecretsPath`,
`translate.go:421-423`; the registry copy at `.../<service>/<service>`, `PullCredentialsPath`,
`translate.go:486`). Nomad gives a task implicit read access to exactly four paths built from the
task's own claim: `nomad/jobs`, `nomad/jobs/<job>`, `nomad/jobs/<job>/<group>`,
`nomad/jobs/<job>/<group>/<task>` (`nomad:acl/acl.go:698-717`, the `workloadVariablesCapabilitySet`
branch). The `<job>` there is `Job.GetIDforWorkloadIdentity()`: the **parent** ID for a periodic or
dispatched child, else the job's own ID (`nomad:nomad/structs/structs.go:4629-4636`, used by
`structs/identity.go:179` and `nomad/auth/auth.go:466,915`).

So:

- a periodic child `<app>_<svc>_backup/periodic-<ts>` reads `nomad/jobs/<app>_<svc>_backup`: this
  is why backups work, and why `BackupVariablePath(jobID)` is the whole trick;
- a job with any other ID cannot read `nomad/jobs/<app>/...`, and a template that names a variable
  it can't read does not fail: its copy waits for it (`internal/deploy/restore.go:13-17`, and the
  reason for `restoreTimeout`, `internal/service/backups.go:39-42`). `Restore` therefore copies the
  backup job's variable to the restore job's own path first (`restore.go:23-37`);
- the other way (an ACL policy attached to the run job so it can read the app's path) needs
  `acl policy apply -job`, a management capability. Caravana's token is deliberately not
  management (`deploy/install/caravana-policy.hcl` header). Rejected.

For a schedule this means: the run's secrets are a **copy** of the service's current set (read with
`secrets.Provider.Read(appID)[service]`, `internal/deploy/secrets.go:44-64`) written to the run
job's own variable. Where that copy lives and for how long is the real difference between the
options below. The registry credentials follow the backup path: `withPullCredentials`
(`deploy/registries.go:118-131`) adds `CARAVANA_REGISTRY_USERNAME/PASSWORD` to the same variable,
and `withPullAuth` (`backup.go:74-79`) adds the docker `auth` block, exactly as the backup and
restore tasks do. `variableTemplate` renders every item of the variable as env, so one variable
carries both.

### Options

**(a) One periodic batch job per schedule** (`Periodic{Spec, TimeZone, ProhibitOverlap}`).

- Run once now: `PeriodicForce` (works today). Pause/Resume: `Periodic.Enabled=false` and register
  again (a new job version each time), or `Deregister` without purge. Next run due:
  `PeriodicConfig.Next` (api `jobs.go:930`) or cronexpr. Overlap: `ProhibitOverlap` is Nomad's, and
  when it skips a launch it only writes a debug line (`nomad:nomad/periodic.go:350-362`); Caravana
  can't record the "skipped" run the spec shows as a mark (FR-006) except by guessing from missing
  children.
- Timezone: Nomad calls `time.LoadLocation(TimeZone)` on its own host (`nomad:nomad/structs/
  structs.go:5927,5958`); the Nomad binary does not import `time/tzdata` (no hit in the module), so a
  non-UTC zone needs zoneinfo on the host and fails the register otherwise (R4).
- Stop-after: Nomad has no maximum run time for a batch task (`api.Task` has only `KillTimeout`,
  `tasks.go:776`; no max-run field anywhere in the api package), so Caravana's own timer and a stop
  are needed anyway.
- Staleness: the periodic job carries the image digest, env and resources, so it must be registered
  again after every deploy, rollback, promotion and secret edit (the `syncBackups` hook). When that
  fails, the code's answer is a warning ("backups weren't scheduled ... deploy again to retry",
  `service/backups.go:179-182`) and the next run silently uses the old image and old secrets, which
  breaks US1 scenario 5. The secrets copy sits at rest in Nomad for as long as the schedule lives and
  must be rewritten at every deploy too.
- Plus: Nomad runs it while Caravana is down. Minus: no `skipped` mark, and a second clock to keep in
  step with the volume scheduler's.

**(b) A parameterized batch job per schedule, dispatched by Caravana** (`Jobs().Dispatch`).

- Caravana keeps the clock (cronexpr, as `schedule.go`), so skip-or-overlap, "skipped" marks, Pause (a
  flag in SQLite) and Run once now are all in its hands. The dispatch call returns the child's ID and
  eval directly (`api/jobs.go:543-575`); the child's ID is `<parent>/dispatch-<unix>-<8 hex>`
  (`nomad:nomad/structs/structs.go:6121-6129`), unique even for two dispatches in the same second,
  which `PeriodicForce` is not (`BackUpNow`'s comment, `service/backups.go:223`). The child reads the
  parent's variable (identity rule above).
- Costs: dispatch needs the `dispatch-job` capability (`nomad:nomad/job_endpoint.go:1999`), which
  `caravana-policy.hcl` does not have (it has `submit-job`; the periodic force endpoint accepts
  either, dispatch does not): every host must rerun the installer, and the lab policy changes. The
  parent still carries digest, env, resources and (at rest) the secrets copy, so it has the same
  staleness problem as (a), plus a parent job that never dies and must be purged with its children
  (`DeleteAppJobs` already purges by prefix, `deploy/backups.go:335-355`).

**(c) One one-off batch job registered per run, Caravana's clock** (recommended).

- Every run builds its job from what is stored right now (the app, the newest healthy deployment's
  pinned digest for the service, the service's current secret set), writes the run's own variable,
  registers the job, and records the run. Nothing is registered between runs, so there is nothing to
  keep in step after a deploy.
- Run once now: the same code with trigger `owner`. Pause/Resume: `armed_at` and a paused flag in
  SQLite (R4, R6). Skip-vs-overlap: Caravana looks at its own `running` rows (as `backupBusy` does for
  volume services, `service/backups.go:275-302`) and records a `skipped` row for the slot. Next run
  due: `cronexpr.Next` in the installation's zone, no Nomad object. Stop this run and the stop-after
  limit: `Jobs().Deregister` of the run's job (R3).
- Policy: `submit-job` (register and deregister), `read-job`, `read-logs`, and
  `variables "nomad/jobs/*"` are all in the policy today (`caravana-policy.hcl`). **No capability is
  added**: no installer rerun, no lab policy change.
- Secrets: the run's variable is `nomad/jobs/<runJobID>` (`BackupVariablePath(runJobID)`, unchanged),
  written just before `Register`, deleted when the run is settled (R2). So secrets copies exist only
  while a run does, hold the values the live version has at that second, and are swept by
  `DeleteAppJobs` (prefix `nomad/jobs/<app>_`, `deploy/backups.go:345-353`) if Caravana dies in
  between. Contrast (a) and (b), where a copy lives as long as the schedule.
- Cost of one job per run: an every-minute schedule is 1,440 register + purge pairs a day, a few Raft
  writes each, trivial for one node. A cap on concurrent runs per schedule (3 is suggested; the plan
  decides) keeps an "overlap allowed" schedule that sleeps from stacking copies on a 2 GB host.
- Cost beyond that: no runs while Caravana is down. That is the volume scheduler's accepted trade
  (`schedule.go:13-30`) and the catch-up window (R4/R6) covers short outages.

### Decision

Option (c): a one-off batch job per run, with Caravana's own timer and watcher.

- **ID**: `nomadjob.ScheduleRunJobID(appID, runID) = appID + "_run-" + runID`, runID 12 lowercase hex
  (the `newID` shape, `internal/service/notifications.go:452`). It starts with `AppJobPrefix(app)`
  (`backup.go:32`), so app delete sweeps it, and it can't be mistaken for a backup: it doesn't end in
  `_backup` (`deploy/backups.go:140`), and `splitBackupJobID` (`backups.go:198-204`) needs three `_`
  parts. The job ID is derived from the run ID, so a run row with no `job_id` yet is still found.
- **Job** (`nomadjob/schedule.go`, pure like `Translate`, golden-file tested): `batchJob` + `batchGroup`
  reused; group and task are named after the service (Caravana names every task after its group,
  `deploy/secrets.go:19-23`); no `Services` block, no ports, so nothing registers with Traefik: "no
  traffic" holds by construction. The pinned image of the service; docker `entrypoint = [program]` and
  `args = rest` (so the image's own ENTRYPOINT can't swallow the command, matching what exec does for
  Run a command; the docker driver builds `Cmd` from `command`+`args` only,
  `nomad:drivers/docker/driver.go:1478-1491`); `Env` from the service's non-secret env;
  `Templates: [variableTemplate(runJobID)]`; `taskResources(svc.Resources, s)` (the service's memory
  settings, as the spec assumes); `KillTimeout` 10 s; `Logs{MaxFiles: 2, MaxFileSizeMB: 1}` so a runaway
  command can't fill the disk before it is stopped and the 64 KiB tail (R2) always fits; `Meta`
  `caravana.app`, `caravana.schedule`, `caravana.run`; the `names` sidecar added exactly when
  `sharesNames(app)` (`names.go:53`), so a run in a multi-service app resolves its siblings by name.
- **No-go case found**: a service with `volumes` can't have a run copy beside the live one: the volumes
  are requested `single-node-single-writer` (`translate.go:308-320`, `deploy/volumes.go:58`) and Nomad
  refuses a second allocation on one (`nomad:scheduler/feasible/feasible.go:459-480`), so the run would
  sit unplaced forever. Refuse the schedule when the chosen service mounts a volume, as a field error
  naming `services.<name>.volumes` (constitution IV; the spec's out-of-scope list can say so). Running
  in the live copy (alloc exec) is the later alternative for those.
- **Which version runs**: the service's image from the newest deployment with `StatusHealthy`
  (`runningImage`, `service/backups.go:366-377`) and only when the app's newest deployment is not
  `pending`/`running`. During a rollout the slot is retried each pass (the volume scheduler's behaviour
  for a slot it can't start, `schedule.go:121-126`) instead of mixing an old image with the new secrets
  `Replace` has already written (`service/deploy.go:325-329`). A stopped app (`StoppedAt`), an app with
  no healthy deployment, or Nomad down: no run, no failure recorded, and the row says why (FR-011); the
  status line is computed, not stored.
- **Start deadline**: a run whose task hasn't started within 5 minutes (a missing variable makes the
  copy wait, an unplaceable job stays pending) is failed ("didn't start within 5 minutes") and its job
  stopped; `restoreTimeout` is the precedent.
- **Nomad quirk**: Nomad interpolates `${NAME}` in task `args` when NAME is one of its own variables
  (`nomad:helper/args/args.go:11-24`; unknown names stay as written): the page can say so next to "no
  shell is implied".

### Alternatives considered

(a) and (b) above. Also: running the command with alloc exec in a live copy on a timer. It needs no new
job and no secrets copy and works for services with volumes, but it is not a fresh copy (FR-004), it
uses the live copy's memory and can be OOM-killed with it, and a run blocks on the copy being up. Kept
as a possible second mode.

## R2. Capturing output and exit code

### How backups read a run's outcome today

`deploy.batchOutcome` (`deploy/backups.go:283-315`): list the job's allocations with
`Jobs().Allocations(jobID, true)`; `ClientStatus == "complete"` is success, and the main task's stdout
(last 16 KiB, `taskOutput`, `backups.go:318-330`, over `Deployer.Logs`, `deploy/logs.go:52-137`) is
parsed for the `caravana-backup key=` marker. Failure: the failed task's `TaskStates[name].Failed`, then
the last non-empty stderr line, else the task's last event's `DisplayMessage`. No allocation at all:
"Nomad has no copy of this run: it never started". There is no exit code today, only success or
failure. The watcher is stateless: every `backupPoll` (15 s) `recordBackups` reads Nomad and writes what
isn't yet final (`service/backups.go:399-460`).

### What a schedule needs

- **Exit code**: the newest `Terminated` task event of the service's task in `Allocation.TaskStates`
  (the stub from `Jobs().Allocations(..., true)` carries them, as `batchOutcome` already relies on).
  `Details["exit_code"]` is the documented key (`nomad:nomad/structs/structs.go:9688-9692`);
  `TaskEvent.ExitCode` is the deprecated field (`api/tasks.go:1178`). `Details["oom_killed"]` says the
  kernel killed it (the code reads it for the needs-you list, `deploy/copies.go:137-146`). The sidecar
  and prestart tasks have their own states: read only the service's task.
- **Could not start**: a `Driver Failure` or `Setup Failure` event (image pull refused; a missing program
  arrives as a Driver Failure, or as exit 126/127 from a started container: 020.5 saw 127,
  `specs/020.5-app-actions/research.md` R4 lab note), or no allocation: failed in Caravana's words, never
  Nomad's text in the record's `reason` (the `execFailure` rule, `service/actions.go:189-201`). Nomad's
  `DisplayMessage` is not the command's output, but it can quote an argument, so the run's page may show
  it and nothing else may.
- **Output**: after the allocation is terminal, `Deployer.Logs(ctx, allocID, task, stream, 64<<10, false,
  out)` for stdout and for stderr. That reads the last 64 KiB (tail, origin `end`) and drops the cut
  first line (`logs.go:63-70,110-125`). A tail, not the head: the end of the output is what says why a
  command failed. Each stream is kept separately (Nomad has no interleaved log). `Logs` must also say
  whether it cut (its first frame's `Offset - len(data) > 0` is the signal it already computes at
  `logs.go:125`), a small addition. Invalid UTF-8 is replaced as `Logs` does (`logs.go:99`). Stored as
  TEXT in the run's row with `stdout_cut`/`stderr_cut`, and `output_kept` false when the read failed
  ("output not kept", spec edge case).
- **When**: the watcher (every 5 s; SC-002 asks for 10 s) settles a run the moment its allocation is
  terminal, then **purges the job** (`purgeJob`, `deploy/backups.go:373-379`) and deletes the run's
  variable. Purging after capture keeps Nomad small and takes the run out of Nomad's retention.

### How long Nomad keeps a finished batch allocation

Defaults apply on both the lab and the installer host: neither `deploy/dev/nomad.hcl` nor
`deploy/install/nomad.hcl.tpl` sets any GC option (the non-default blocks are `telemetry`, `acl`, host
volumes and the docker plugin).

- Server: a dead batch job is GC'd by `job_gc_threshold` = **4 h** after its submit time, when all its
  evals are old enough (`nomad:nomad/config.go:653`, `nomad/core_sched.go:132-182`); evals of batch
  jobs go after `batch_eval_gc_threshold` = **24 h** (`config.go:651`, `core_sched.go:288`);
  `eval_gc_threshold` 1 h applies to non-batch (`config.go:650`).
- Client: terminal allocations' directories (and so their logs) go at `gc_max_allocs` = **50**
  terminal allocations on the node, or disk use over 80 % (`nomad:client/config/config.go:950-954`).
  An every-minute schedule fills 50 slots in under an hour, so logs of an uncollected run can vanish
  long before the server forgets the job. This is why the output is captured within seconds and why
  Caravana's record, not Nomad's, is the history (spec: "the orchestrator's own history of a run is
  gone").

### Secrets in output

020.5's rule is that Run a command's output "exists only in that return value" (its research R2: not
logged, stored, put in an error, notice, URL or cache; tests search every row, the SQLite file and the
log for a sentinel). Schedules cannot follow it: the spec requires the output to be recorded (FR-005)
and shown later. What can still hold:

- **Never** reaches a log line, a needs-you item (`AttentionItem.Detail` is "exit 3" or a reason in
  Caravana's words), a notification (the event's `detail` likewise), a live notice (notices carry names
  only, `service/changes.go`), a URL, an error string, or the schedule list's JSON: output is returned
  only by the run's own read (`getScheduleRun`) and drawn only on the run's page, both `Cache-Control:
  no-store`.
- **Is** in SQLite, up to 128 KiB per run, until the run is pruned (about 100 per schedule) or the
  schedule or app is deleted (`store.DeleteApp` gets one more `DELETE`, `store/apps.go:371-379`). This
  is the first place a value the app's code printed can rest on the host outside Nomad. The CLAUDE.md
  invariant "the stored app, responses, logs and the Nomad job hold names only" (feature 008) is about
  secret values Caravana *writes*; a command that prints its environment writes one.
- **Mitigation, recommended**: before storing, replace every occurrence of each of the service's secret
  values of 6 bytes or more with `[secret NAME]` (the values are at hand: the run's variable was written
  from them). Exact substring only, so a transformed value (base64, URL-encoded) still gets through; the
  run's page says the output is kept on this host and can show anything the command printed. Values
  under 6 bytes are left alone: replacing `1` or `on` would wreck the output and protects nothing.
- Tests: a sentinel the command prints is searched in every log line, needs-you item, delivery payload,
  live notice and list answer (must be absent) and found only in the run's read; a secret *value* the
  command echoes (`sh -c 'echo $PASSWORD'`) must be absent from the SQLite file too.

### Alternatives considered

Reading the log files with `AllocFS().Cat` (`read-fs` is already in the policy) instead of `Logs`: no
advantage, and `Logs` has the cut-line handling and tests (`logs_test.go`). Head instead of tail (what
`RunCommand` keeps): a long job's useful lines are its last. Storing nothing and reading Nomad on
demand: the 50-allocation client GC makes that wrong for any busy schedule.

## R3. Stopping a run

### The owner's Stop this run, and the stop-after limit

- **Use `Jobs().Deregister(runJobID, purge=false)`** (`api/jobs.go:398-410`): Nomad stops that one run's
  allocation, sends the task its kill signal and waits `KillTimeout`. It affects nothing else, because
  each run is its own job (R1).
- **Do not use `Allocations().Stop`** (`api/allocations.go:189`). It sets the allocation's desired
  transition to *migrate* (`nomad:nomad/alloc_endpoint.go:321-329`); for a batch job without the
  reschedule flag (the api method can't pass it) the reconciler stops the allocation **and places a
  replacement** (`nomad:scheduler/reconciler/reconcile_cluster.go:1008-1029`: batch and not
  `ShouldReschedule`), so the command would start again. A lab check is worth making once, but the
  source is unambiguous.
- Purge only after the outcome is recorded (R2), so the allocation's task events and logs are still
  readable at settle time.
- **Grace**: `Task.KillTimeout` 10 s (Nomad's default is 5 s, `api/tasks.go:810-812`; the client caps it
  at `max_kill_timeout`, 30 s by default, `nomad:client/config/config.go:151`). Stop to stopped is then
  about 10 s plus the watcher's poll, inside SC-004's 30 s. A container whose PID 1 ignores SIGTERM
  (busybox `sleep 180` as PID 1 does) is killed after the full 10 s; lab check whether the docker
  driver's `init = true` (tini) is worth adding so TERM is delivered; leave it out until the lab says
  so.
- **Stop-after**: measured from the task's own start (`TaskState.StartedAt`), not from registration, so a
  slow image pull doesn't eat the limit. The watcher compares on each pass, and when over: write the
  intent, then deregister.

### Telling the end reasons apart

Do not infer from Nomad's events; **write the intent first**. The run row gets `stop_reason` (`owner`,
`limit`, `app_stopped`, `schedule_removed`) and `stop_requested_at` in the same transaction that decides
to stop, then the deregister call. Settling reads Nomad only for the exit code and output:

| What happened | Recorded as | From |
| --- | --- | --- |
| exit 0 | `succeeded` | `Terminated` event `exit_code` 0, alloc `complete` |
| exit n, n > 0 (including 126, 127) | `failed`, "exit n" | `Terminated` event, alloc `failed` |
| killed by the kernel for memory | `failed`, "ran out of memory" | `Details["oom_killed"] == "true"` |
| owner pressed Stop | `stopped`, not a failure | `stop_reason = owner` (exit code ignored: 137/143) |
| passed the limit | `failed`, "stopped after N minutes" | `stop_reason = limit` |
| image pull or setup failed | `failed`, "could not start" | `Driver Failure` / `Setup Failure` event, no `Terminated` |
| never reached running in 5 minutes | `failed`, "didn't start within 5 minutes" | the deadline in R1 |
| allocation or job gone | `failed`, "lost" | R6 |
| stopped by someone in Nomad (no intent, `DesiredStatus` stop) | `failed`, "stopped outside Caravana" | `Allocation.DesiredStatus` |

A deregistered run's task events look like `Killing`, `Terminated` (signal or 137/143) and `Killed`; the
`Killing` event's `kill_reason` is for Nomad's own kills (templates, Vault, identity;
`nomad:client/allocrunner/taskrunner/*_hook.go`), not user stops. That is why intent, not events, names
the reason. Stop app (020.5) deregisters the app's job and its backup jobs (`deploy/actions.go:154-168`);
it should stop in-flight runs too: `StopApp` takes the run job IDs as it takes `backupJobIDs`, and the
run is `stopped` ("the app was stopped").

### Alternatives considered

`Allocations().Stop` (above, wrong for batch); `Allocations().Signal` to the task (the process may
ignore it and Nomad would not kill it, so no bound); wrapping the command in `timeout N` for the limit
(needs a `timeout` binary in the image: busybox and coreutils have one, distroless images don't).

## R4. Timezone

> **Superseded by the maintainer's ruling (2026-10-23): "we should show UTC and localtime".** There
> is no installation timezone, no `CARAVANA_TIMEZONE` and no `time/tzdata` import. Schedules are
> written, stored and answered in UTC, as backups are; the console draws the viewer's local time
> beside each UTC time with a small script (`plan.md`, "Decisions carried"). The findings below
> still stand as facts (where UTC is assumed, cronexpr's zone behaviour, the static binary's lack
> of zoneinfo, why backups must not be rewritten); the decision section does not.

### Where UTC is assumed today

- `internal/app/app.go:151` ("Schedule is a five-field cron expression, in UTC"), `ScheduleOrDefault`
  `app.go:165-170`, default `0 3 * * *`.
- `internal/nomadjob/backup.go:94` `TimeZone: new("UTC")` (hard-coded, so Nomad never loads a zone).
- `internal/service/schedule.go:38,52-53`: `dueSlot` "Slots are in UTC", `now.UTC()`.
- Console text: `internal/ui/backups.go:83,218,224,581-607` (`scheduleText` writes "UTC" into the words),
  `ui/configure.go:467`, `ui/composepage.go:109` ("Backup schedule (UTC)"), and `ui/backups.go:415-426`
  (`stripDay`/`daysAgo`: the 14 marks are UTC days).
- Time display: `ui/view.go:67-71` (`whenOf`: `Exact` is `t.UTC().Format("... UTC")`) and `view.go:92`; the
  021 designer's rule "06:13:29 UTC" is `specs/021-console-ia/contracts/page4-home-deploys.md:193` and
  `DESIGN.md:764`. Those are deploy times, which FR-014 does not cover. Token expiry ("End of that day,
  UTC", `ui/settings.go:445`, `templates/settings.html:450`) is a date, not a timer. The designer's
  contract should say whether deploy times stay UTC while schedule and backup times use the zone; two
  zones on one page is the thing to avoid.
- No `time.LoadLocation` and no `time/tzdata` anywhere in the repo (searched `internal` and `cmd`).
- cronexpr evaluates in the location of the `fromTime` it is given (`cronexpr@v1.1.3/cronexpr.go:159-166`:
  "the `time.Location` of the returned time instant is the same as that of `fromTime`"), including
  hour-skipping and repeated hours around DST (`cronexpr_next.go:147-160`, the `SLOW_CLOCK` path). So
  zone support in Caravana's own scheduler is `expr.Next(t.In(loc))`.

### Go's zone data and the static binary

`time.LoadLocation` looks in `$ZONEINFO`, the system's zoneinfo, `$GOROOT/lib/time/zoneinfo.zip`, then
embedded data **only if `time/tzdata` was imported** (`$GOROOT/src/time/zoneinfo.go:657-663`,
`zoneinfo_read.go:531-563`). The release binary is built with `-trimpath` for linux amd64 and arm64
without CGO (`tools/release.sh:62,79`) and runs on a host that may have no `/usr/share/zoneinfo` (Ubuntu
server images have `tzdata`, minimal ones may not) and has no GOROOT. Therefore Caravana must `import _
"time/tzdata"` once (in `cmd/caravana/main.go`): about 400 KB (the zip is 408,467 bytes in this
toolchain), no CGO, no runtime dependency (constitution I). Without it "Europe/Madrid" works on the
maintainer's Mac and fails on a bare VPS.

Nomad is a separate binary with no `time/tzdata` import: a periodic job's non-UTC `TimeZone` depends on
the host's zoneinfo and is validated at register time (`nomad:nomad/structs/structs.go:5927`). This is
one more reason schedules do not use Nomad's periodic (R1), and why backups must not be given a non-UTC
`TimeZone` without an installer change (`apt-get install tzdata`, as `install.sh:205` installs other
packages).

### Decision

1. **One setting, stored once, in SQLite**: a single-row table like `backup_destination`
   (`migrations/0005_backups.sql:3-12`: `id INTEGER PRIMARY KEY CHECK (id = 1)`), in the feature's
   migration: `installation_settings(id, timezone TEXT NOT NULL DEFAULT 'UTC', updated_at)`. It is
   settable in Settings and through the API, validated with `time.LoadLocation`, refusing `""`, `Local`
   and the legacy abbreviations that have no `/` (`EST`, `CST6CDT`: they don't follow DST as people
   expect) except `UTC`; `Service` caches the `*time.Location`. **Not read from the host**:
   `/etc/localtime` on a VPS is almost always UTC and a silent change of meaning on reinstall is worse
   than a setting. The installer may offer to set it later.
2. **A schedule stores a five-field cron line and no zone.** It means "wall clock in the installation's
   zone, whatever that is when the slot is computed". "A zone change keeps each schedule's wall-clock
   time" (FR-014) is then true by construction; nothing is rewritten. On a zone change the service sets
   each schedule's `armed_at = now` (so no catch-up run is made for slots the new zone calls past) and
   answers with each schedule's next run before and after ("next run moved"). Validation (FR-010):
   exactly five fields (cronexpr accepts 5, 6 and 7, so seconds and years are refused by field count
   first), `Parse` ok, and `Next(now)` non-zero within a year (31 February never fires).
3. **DST** (risk, needs table tests with `Europe/Madrid`, 2026-10-25 and 2027-03-28, at 02:30 and
   03:00): a slot in the spring-forward gap runs once at the next valid time or is skipped, never twice;
   in the fall-back repeat of an hour a slot must not run twice: guard by ignoring a slot whose local
   wall-clock text equals the previous slot's within two hours.
4. **Backups keep their instant: do not rewrite, and do not store a zone on them.** A cron line in UTC
   cannot be turned into an equivalent line in a DST zone (03:00 UTC is 04:00 in a Madrid winter and
   05:00 in its summer), so "convert" would change the instant for half the year. Pinning a
   `Backup.Timezone` field would work but changes the app document (every backup app's `revision`, the
   Compose overlay's `backupSchedule`, the export, `validate.go`) and Nomad would then need zoneinfo for
   the Postgres job (above). Instead: the backup rule keeps saying UTC and the page shows the instants
   (next run, last runs, the 14 marks by day) **in the installation's zone**, naming it: "every day at
   03:00 UTC · next Fri 04:00 Europe/Madrid". `scheduleText` keeps "UTC" in its words; the instants go
   through one new helper `whenIn(loc)`. `dueSlot` and `nomadjob/backup.go` stay as they are. The
   schedules list's built-in backup row and the Backups page then say the same thing, which is what US5
   asks ("the two pages never disagree"). This reads US5 scenario 3's "shown in the installation's zone"
   as the *times*, not a rewritten rule: the spec should say so. If the maintainer wants the rule itself
   in local time, the way is the `Backup.Timezone` field above, in its own feature.

### Alternatives considered

Per-schedule zone (more to explain, and the spec wants one installation zone); converting backup cron
lines (lossy across DST); setting Nomad's `TimeZone` for backups (host zoneinfo, installer change);
reading the zone from the host.

## R5. Attention and notifications

### How a failed backup reaches the owner now

1. `Service.Attention` (`service/attention.go:70-92`) reads stored runs: `store.LatestBackupRuns`
   (`store/backups.go:295-324`, newest run per app and service via `ROW_NUMBER()`) and passes them to
   the pure `attentionOf` (`attention.go:102-179`), which emits `AttentionBackupFailed`
   (`attention.go:25`, `142-150`) for a service whose newest backup failed and that still has a policy.
   It is derived on every read: the item stays while the newest run is failed and leaves when a later
   run succeeds.
2. `runNotifier` (`service/notifier.go:379`) every 30 s calls `checkProblems` (`notifier.go:401`):
   `diffProblems` (`notifier.go:328-368`) compares the items with the `sent_problems` rows: new key =
   `Opened`, present = `Seen`, absent for `resolveAfter` = `Resolved`. The key is
   `kind|app|service|deployment` (`problemKey`, `notifier.go:292`).
3. `problemEvent` (`notifier.go:41-88`) words the event; `problemPath` (`notifier.go:176-186`) is its
   link; `ui/attention.go:103,145` words it in the console; the event type is `problem.opened` with
   `kind: "backup_failed"`.
4. Deliveries go to channels that subscribe to `problems` (`channelsWanting`, `notifier.go:487`).

### What schedule failures need

- **`AttentionScheduleFailed AttentionKind = "schedule_failed"`**, from a new `store.LatestScheduleRuns`
  (newest *ended, not skipped, not owner-stopped* run per schedule). The item is present while that run is
  `failed` and its schedule still exists; a later `succeeded` clears it. A `skipped` or owner-`stopped`
  run neither opens nor clears one. `When` is the run's finish time; `Detail` is in Caravana's words
  ("exit 3", "stopped after 1 minute", "could not start", "lost"), never the output (R2).
- **Key**: `schedule_failed|<app>|<service>|<schedule id>`. Use the fourth segment for the schedule ID
  (it is "the deployment" only for `deploy_failed`; rename it "subject" in the comment). No new column,
  no migration: `sent_problems.key` is the text primary key and `kind` has no CHECK
  (`migrations/0010_notifications.sql`); `resolvedEvent` already parses the fourth part. The schedule's
  **name** is in `Summary` (the title sent), so renaming a schedule later doesn't change the key. A
  schedule that fails every minute stays one key, so one `problem.opened` (SC-003). A schedule removed
  with a failed last run: its rows and the item go, and the notifier resolves it after `resolveAfter`;
  pausing keeps the item, since the failure still needs the owner (the plan can choose).
- **Not a copy kind**: `isCopyKind` (`notifier.go:299-305`) must not list it. It comes from stored runs,
  so it is judged while Nomad is down, like `backup_failed`.
- **Event**: keep `problem.opened` / `problem.resolved` with `kind: "schedule_failed"`. The spec text
  "receives a `schedule.failed` notification" (US3 independent test) should read this way: a separate
  per-run event type would send one notification per failing run, which SC-003 forbids, and would need
  its own subscription option on channels. Add additive `schedule` (name) and `exitCode` fields to
  `notify.Event` (`internal/notify/notify.go:41-67`), document them in the `NotificationEvent` component
  and extend its `kind` description and the `AttentionItem.kind` enum (`api/openapi.yaml:3207-3209,3889`),
  plus `schedule`/`scheduleId` on `AttentionItem`. `Render` (slack, discord, ntfy) takes the words from
  `Title`/`Text` already. API version bump with the rest of the feature's API.
- `problemPath` points at the schedule's page (the designer's contract names it); `problemEvent` adds the
  `schedule_failed` case ("<app>: <schedule> failed (exit 3)").
- `AttentionItem` gains `Schedule`/`ScheduleID`; `attentionOf` and `store.ApplyProblemCheck` need no change
  beyond the new kind. `s.publish(Change{What: ChangeAttention})` after a run settles, as `backupsChanged`
  does (`service/backups.go:508-512`).
- Deploy-style per-run notifications ("run succeeded") are out of scope.

## R6. Restart recovery

### What exists

- 020.5 actions: `Start` begins with `failInterruptedActions` (`service/service.go:176`,
  `actions.go:506-516`), which fails **every** `running` row of `app_actions` ("Caravana restarted during
  it", `store/actions.go:120-132`). Right for a restart or a command, which need the process that started
  them.
- 016 volume runs: `resumeVolumeRuns` (`service/resume.go:73-117`) walks the stored `running` backups and
  restores, one goroutine per app under `s.watchers`; `settleVolumeRuns`/`settleOneVolumeRun`
  (`resume.go:119-164`) find the run's result in its copy until its time is up, else fail it with
  `restartedMessage` ("Caravana restarted during the run"). It holds the app's volume claim meanwhile.
- 010 Postgres backups: no resume code at all; `watchBackups` is stateless, so a restart changes nothing
  (`service/backups.go:399-427`).

### Decision

A schedule run's Nomad job outlives Caravana, so **do not use `app_actions` or its blanket failure**; use
the stateless watcher shape of `watchBackups` with its own tables and add nothing special for the
restart:

- **Tables** `schedules` and `schedule_runs` (the feature's migration): a run row has `id`,
  `schedule_id`, `app_id`, `service`, `command` (as run, JSON, kept even if the schedule is edited),
  `trigger` (`timer`/`owner`), `slot_at` (the cron instant it answers; NULL for owner runs), `status`
  (`starting`, `running`, `succeeded`, `failed`, `stopped`, `skipped`), `exit_code`, `reason` (Caravana's
  words), `stop_reason`, `stop_requested_at`, `started_at`, `task_started_at`, `finished_at`, `stdout`,
  `stderr`, the two cut flags and `output_kept`. Deleting an app deletes them (one more line in
  `store.DeleteApp`).
- **Write-ahead**: the row is inserted `starting` **before** `Register` (the job ID derives from the run
  ID, R1), then flipped to `running` once Nomad accepted. A crash in between leaves `starting`.
- **`watchScheduleRuns(ctx, gap)`**, started in `Start` beside the others (`service.go:182-205`, in the
  `backupWatcher` group), every 5 s: for each non-final row read Nomad (`Jobs().Info`, then
  `Jobs().Allocations`): a terminal allocation settles it (the R2/R3 table); a running one is left alone
  and checked against the limit and the start deadline; a job that 404s while the row is `running` or
  `starting` is `failed`, "lost while Caravana was restarting" (a job Nomad can't find can't be waited
  for, and the slot is already recorded so it is not run again). The same pass is what runs after a
  restart, so FR-018 needs no second code path; the run's variable is deleted at settle, and
  `DeleteAppJobs`'s prefix sweep (`deploy/backups.go:335-355`) covers anything a crash left.
- **The timer after a restart** reuses `dueSlot` (`schedule.go:47-73`) generalised with a
  `*time.Location` and the catch-up window `catchUp` (1 h, `schedule.go:30`): Caravana down at 03:00 and
  back at 03:20 runs the 03:00 slot; back at noon, it records nothing and waits. Differences from the
  backup scheduler: the base is `max(armed_at, newest slot_at)`, not the newest run's start
  (`lastBackupStart`, `schedule.go:135`), because a **skipped** slot is a recorded row (`slot_at` set,
  status `skipped`), which also stops it being tried again every pass; and `armed_at` is set at create,
  resume, and when the cron line or the zone changes, so Resume "takes the next time only" (FR-008) and
  an edit never fires a slot the old line owed. The scan gap for schedules should be 10 s, not the
  volume scheduler's 30 s (SC-002: a run starts within 60 s of its due time; Run once now needs no scan).
- Order in `Start`: settle first (an unsettled run may still be running in Nomad and counts for
  skip-or-overlap), then start the timer.
- `Copy.RequestedRestarts` and the crash-loop rule need nothing: run copies are separate jobs, and
  `Attention` reads copies by the app's job ID (`AllCopies` is keyed by job, `deploy/copies.go:170-183`,
  and `attentionOf` looks up `copies[a.ID]`), so a failing run never shows as a crash loop of the app.

### Alternatives considered

A `schedule` action in `app_actions` (blanket failure at start is wrong: the run is still alive); a
goroutine per run holding its state in memory (lost at restart, which is what `resume.go` exists to
repair).

## R7. Scoped deploy tokens

Confirmed: no change. `scopeRules` (`internal/api/scope.go:24-37`) lists the eleven routes a deploy token
may use and `scopeAllows` (`scope.go:103-110`) answers 403 `token_scope` for any route not in it.
Schedule routes (`/v1/apps/{id}/schedules...`, the runs, run now, pause, resume, stop, the timezone
setting) are not in the table, so they are denied with no edit. `TestScopedTokenWalksEveryRoute`
(`scope_test.go:77-134`) walks `protectedRoutes` and fills every unknown path parameter with a filler
(`fill`, `scope_test.go:101-105`), so new routes with `{scheduleId}`/`{runId}` are covered automatically
and must answer 403 `token_scope` for a token's own app; `allowedForDeployTokens` (`scope_test.go:17-29`)
is untouched. The plan should still list the new routes in the contract as "denied to deploy tokens".
Running a command on a timer is arbitrary code in the app's containers, the same reason Run a command and
the terminal are denied.

## R8. Lab constraints and tests

- **Lab size**: `deploy/dev/lab.yaml:12-14`: 2 CPUs, 2 GiB RAM, 20 GiB disk, shared with Nomad, Traefik,
  Docker, the in-process Caravana and the step's other apps. A run is one more allocation with a bridge
  network; it must reserve little.
- **Smallest image and memory**: `busybox:1.37` (about 4 MB; `actionsWeb`, `gate_actions_test.go:27`),
  with `labResources = {memoryMB: 16, memoryMaxMB: 32}` (`gate_projects_test.go:175`) on the service.
  The run takes the service's memory settings (`taskResources`), so a test app built with `labResources`
  runs its runs at 16 MB. The `names` sidecar adds 16 MB (`names.go:30`) only in multi-service apps.
  Commands: `sh -c 'echo hello'`, `sh -c 'echo boom; exit 3'`, `sleep 180` (limit 1 minute, stopped by
  Caravana), `sh -c 'echo "$SECRET_NAME"'` for the redaction check. Keep the cap on concurrent runs low
  in the tests too.
- **Timing**: use Run once now for almost everything (a run takes a few seconds on busybox; the watcher
  settles within 5 s). Only two steps need the clock: one "every minute" schedule seen to run on its own
  (up to about 70 s with a 10 s scan gap) and the 1-minute limit (about 1 min + poll + the 10 s kill
  grace). The needs-you and notification half waits for the notifier's fixed 30 s gap and
  `CARAVANA_NOTIFY_RESOLVE_AFTER=10s` (`lab_test.go:64-67`), as `console-notifications` does (about 2
  min). Budget roughly 5 to 6 minutes more for `TestConsoleLab`; make the scan gap and the start
  deadline settable on `Service` (as `scheduleGap`, `service.go:52,109` and the actions' `commandTimeout`
  are) so the lab can shorten them.
- **Where steps go**:
  - `TestConsoleLab`: add `{"console-schedules", c.schedules}` in the list at
    `cmd/caravana/console_lab_test.go:87-113`, after `console-backup-daily` and before `console-delete`,
    with its code in a new `console_schedules_test.go` like `console_actions_test.go`. It uses the forms
    (no JavaScript), as the others do.
  - The gate (`runGate`, `cmd/caravana/gate_test.go:111-134`): a step `{"schedules", false,
    g.schedulesStep}` after `app-actions`, in `gate_schedules_test.go`, through the public API with the
    gate's token (it also checks that a deploy token is refused, R7).
  - Restart of Caravana during a run (SC-006): no helper exists for the in-process server (only
    `RestartNomad`, `lab_test.go:97-107`). `startLab` runs `runServer` with a `ctx` it cancels in cleanup
    (`lab_test.go:67-82`); expose a `RestartCaravana` on `gateTarget` that cancels that context and calls
    `runServer` again on the same `dataDir`, then waits healthy. The test starts a `sleep 20` run,
    restarts, and expects the same run to end `succeeded` (not `lost`).
  - Policy: nothing new, so `labCaravanaToken` (`lab_test.go:127-`) already tests the right capabilities
    (`submit-job`, `read-logs`, `read-job`, variables). Run `host.sh prove` once as for every gate.
- **Unit tests**: the pure job shape as a golden file (`nomadjob/testdata`), `dueSlot` with zones and DST
  tables, the settle table of R3 with `deploytest` (it needs a way to finish a run with an exit code and
  to answer a deregister: `FinishRun` today only knows ok/failed, `deploytest/nomad.go:909-948`, and has
  no exit code), `diffProblems` with the new kind, a test that no sentinel reaches logs, deliveries, items
  or notices, and that `scope_test` still passes.

## Summary of decisions

| Question | Decision |
| --- | --- |
| R1 job shape | One one-off batch job per run, registered by Caravana, Caravana's own clock; no periodic, no dispatch, no policy change |
| R1 secrets | Per-run variable `nomad/jobs/<runJobID>` written at start from the live set, deleted at settle; services with volumes refused |
| R2 output | Last 64 KiB per stream read after the run ends, stored in SQLite, secrets of 6+ bytes masked; Nomad job purged after capture |
| R3 stop | `Jobs().Deregister` of the run's job (never `Allocations().Stop`), intent recorded first, kill grace 10 s |
| R4 timezone | One SQLite setting, `time/tzdata` embedded, schedules store a zone-less cron (wall clock), backups stay UTC and show their instants in the zone |
| R5 attention | New kind `schedule_failed`, key `kind\|app\|service\|scheduleID`, event stays `problem.opened/resolved` with additive fields |
| R6 restart | Stateless watcher over a `schedule_runs` table, write-ahead row, `dueSlot` generalised with `armed_at` and recorded skipped slots |
| R7 tokens | No change; default deny and the route walk cover it |
| R8 lab | busybox, 16/32 MB, mostly Run once now; two clock-bound steps; add a `RestartCaravana` helper |
