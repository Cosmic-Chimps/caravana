# Feature 027.5: the project page as the handoff's `ProjectScreen`

**Branch**: the CodeAlta task `2026-10-28-build-the-project-page-as-the-handoff-s-projectscreen-with-t-49mo`
(worktree `alta/cheery-kingfisher-26nx`). **Phase**: 2.6 (a Developer screen that fell through
021's scope: the plan assigned `ProjectScreen` to 021, whose spec put it "page 4 or later").
Numbered outside the plan's sequence, as 019.5 and 020.5 were.

## Why

The maintainer, 2026-10-28, on `/projects/default`: "I find the projects page a little bit hard
to notice the apps." The page (019) was mostly forms; the apps were plain names with no status,
copies, address or last deploy.

## User stories

1. **See an environment at a glance.** The owner opens a project and sees, per environment, each
   app's status, its copies, its address and its version with when it was last deployed, the
   whole row leading to the app. Problems sort first.
2. **Switch environments.** One badge per environment, a link each, so the page works without
   JavaScript and a stale link falls back to a known environment.
3. **Know how the environment deploys and what needs attention.** The rail says how it was last
   deployed (in the console's who-words), what needs the owner in that environment, and its
   newest deploys. From staging, the apps that can be promoted are one link each.
4. **Change the project from one place.** Display name, environments (add, delete) and Delete
   project live on a settings page behind "Project settings".
5. **The sidebar reads in the handoff's order**, with a Configure group holding Domains.

## Scope

In: `/projects/{project}` (`?env=`), its two live parts, `/projects/{project}/settings`, the
sidebar's order and group, DESIGN.md, `docs/plan.md`'s screen row, a `TestConsoleLab` step. A
read-only store query for an environment's newest deployments.

Out: an API change (none is needed), shared variables, a Backups workspace page, branch and
deploy rules (CI's), Deploys and Previews sidebar entries.

## Contract

`contracts/project-screen.md`: the designer's values and rulings. Where it and this file
disagree, the contract wins.

## Constitution check

- V API-first: the page reads `internal/service` only; the new store read is served to the
  console through the service, and the API's `listRecentDeployments` is unchanged (an
  environment filter can be added when a client needs it).
- VIII: table tests for the pure parts (the copy marks, the Apps clause, the Deploys row), the
  template through the package's tests, and the lab step.
- No new module, no migration, no Nomad change.
