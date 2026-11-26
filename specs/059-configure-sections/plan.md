# Plan 059: Configure in sections (CodeAlta task 4mvb)

The task: `.alta/tasks/2026-11-26-configure-split-into-sections-one-concern-per-page-one-deplo-4mvb.md`. The page
contract: `contracts/configure-sections.md`. Console only: no migration, no new module, no Nomad policy change, no API
route. The service gains two methods the API can already reach by deploying a whole app or Compose file.

## Decisions

- **D1 Addresses.** `/apps/{id}/configure` is Overview; `/configure/source`, `/configure/services`,
  `/configure/integrations` are new GET routes; Variables (`/apps/{id}/variables`) and Domains (`/apps/{id}/domains`)
  keep theirs. Every POST keeps its route; each answers or redirects to the section that holds its form.
- **D2 One form, one concern.** The existing `POST /apps/{id}/configure` serves both the Source image form and the
  Services form. `parseEditForm` reads a service when the post carries its marker `svc.<name>.form`, and each field
  only when the post has it (`post.Has`): an absent field keeps the stored value (021's #4 rule, which also covers an
  old Configure form posted after this change: it names `image`, so the old `image`-keyed rule still applies to it).
- **D3 The Compose form of an app that exists has only the file.** Source (kept file) and `/apps/{id}/compose` draw no
  per-service inputs. `composeDraft` keeps, from the stored record, what the form doesn't offer, as `keepDomains` does:
  `keepHealth` (overlay's as written for a kept file, else the services' as `compose.Health`), `keepBackup`
  (likewise), and the secrets by name (`keepStored`, already). New app › Compose keeps today's form, with secret rows
  and backup (a new app has nothing to keep).
- **D4 A kept-file app's copies and size stay in the file.** The mockup draws them as inputs; writing `replicas` and
  `mem_*` into the YAML text when the key is absent can't keep the owner's formatting in all cases, so this pass shows
  them read-only, "Set in the Compose file", as for placement and scaling. Follow-up task if wanted.
- **D5 Health of a kept-file app** goes into the kept overlay: `Service.SetHealthIf(ctx, appID, service, path,
  interval, timeout, cond, trigger)` rewrites `overlay.Health` (keeping its port), translates the file and deploys with
  it kept, as `SetDomainsIf` does; errors renamed by `inFile`. Route `POST /apps/{id}/services/{service}/health`
  (`appConfigure()`). An app without a kept file uses `POST /apps/{id}/configure`.
- **D6 Change the policy.** `Service.ChangeBackupPolicy(ctx, appID, service, engine, file, schedule, keep, cond,
  trigger)`: the service must have a policy; restore, destination, copyTo and copyKeep stay; a kept file's overlay
  follows (`withBackup`). Route `POST /apps/{id}/backups/policy/change` (`appConfigure()`).
- **D7 Section nav** is Settings' `cv-snav` items with an in-page modifier; no new component.
- **D8 Overview replaces Definition.** The read-only Definition panels go; their facts are summarised once on
  Overview and in Services' read-only groups.
- **D9 Delete** stays on Overview (`POST /apps/{id}/delete` refusals draw Overview), its panel without the danger edge.

## Phases

- **A** Routes, nav, Overview, Integrations, Variables/Domains in the frame; Services and Source split (D2); access
  rules and the roles walk; parity table if the console's route list needs it; tests.
- **B** D3 (compose form of an existing app: only the file, the keepers), D5 (health service method, route, form),
  D6 (Change the policy), tests (service and ui; sentinel tests where secrets pass).
- **C** CSS (`tools/ui-css.sh build`), `DESIGN.md` (App pages, Configure section), the console lab steps that read
  Configure (`cmd/caravana/console_*_test.go`), `changelog.d/059-configure-sections.md`, `CLAUDE.md` note, full checks.

## Constitution check

II, III (declarative deploys: every section deploys the whole stored app with one change), IV (absent fields keep,
never drop: D2, D3), V (UI and API share `internal/service`; both new methods are service methods; no new API route is
needed since `deployApp`/`deployCompose` already set health and backup), VIII. No violation.
