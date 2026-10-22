# Contract: page 3, Settings with its grouped nav

Written by the caravana_designer session on 2026-10-22. The values come from the handoff's
`SettingsScreen.jsx` (`ST_NAV`, `StHead`, `StField`, `ConnList`, `TokenSettings`,
`NotifySettings`) and `theme/caravana.css`, rendered from the prototype (renders in
`design/console/_shots/p3-*.png`). Where this list and the handoff disagree, this list wins.
Page 1's contract still applies to the shell; page 2's to fields (32px), icons and buttons.

## IA: one page per entry, under a sticky left nav

- **Routes.** Each entry is its own page: `/settings` is the first entry (Account), then
  `/settings/registries`, `/settings/backup-storage`, `/settings/notifications`,
  `/settings/tokens`. `/settings/account` answers 301 to `/settings`; `/account` keeps its
  redirect. The existing POST routes keep their paths and answer 303 to the entry's page.
- **Groups and entries**, in this order, with what the console has today:
  - (no group) **Account** (icon `user`): the signed-in admin's email and the password form.
  - **Connections**: **Registries** (`package`), **Backup storage** (`hard-drive`),
    **Notifications** (`bell`).
  - **Security**: **API tokens** (`key`).
  - No People group (teams are Phase 4), no Organization, Git providers, DNS providers, SSH keys,
    Single sign-on or Audit log: each arrives with its feature (Phase 3 and 4). A group with no
    entries is not drawn, which is why Security has one item and People none.
- **The sidebar's Settings item** stays active on every `/settings*` page; the top bar's crumbs
  read `Settings / <entry>` (page 1's crumb shapes: "Settings" a link to `/settings`, the entry
  fg-1). On `/settings` the crumb is `Settings / Account`.
- **Counts and marks** on the nav, per entry, right-aligned in the item: a count in 400 11px mono
  fg-4 when there is nothing wrong, or a 7px status mark instead when there is. The count and the
  mark are decided per entry on every render:
  - Registries: the count of registries; ■ failed when any registry's last test was refused;
  - Backup storage: no count (one destination); ◆ warning when no destination is set and at least
    one service has a backup policy, or when the last run against it failed;
  - Notifications: the count of channels (paused ones included); ◆ warning when any channel's
    last delivery failed, or a channel is paused;
  - API tokens: the count of active tokens; ◆ warning when any active full token has no expiry
    (the handoff's "Admin token with no expiry") or was last used more than 90 days ago;
  - Account: nothing.
  A mark wins over a count. The same rule gives the entry's rows their marks, so the nav and the
  page never disagree. (The Apps home's needs-you list is not changed by this page: the failed
  backup destination and failed registry already reach it through 014's Attention.)

## Layout (every Settings page)

- The page is a grid `232px minmax(0, 1fr)`, `align-items: start`, inside the shell's frame
  (so under the 52px top bar, beside the 248px sidebar).
- **Settings nav** `<nav aria-label="Settings">`: sticky at `top: 52px`, height
  `calc(100vh - 52px)`, overflow auto, border-right 1px line-1, padding 28px 12px, column flex
  with 20px between blocks. First block: the word "Settings" in 500 18px sans fg-1, padding
  0 10px. Each group: a column with 2px gap; the group title "Connections" / "Security" as
  `.cv-label` (12px fg-3, the page 1 deviation) with padding 0 10px 8px; no title for the first
  block. An entry is a link 32px tall (not 34: the handoff's Settings nav is tighter than the
  sidebar's), padding 0 10px, radius 4, gap 10, 400 14px sans; icon 15px in a 15px box, fg-3;
  the label `flex: 1`; then the count or mark. Inactive fg-2; active (`aria-current="page"`)
  ink-2 ground, text and icon fg-1, no left bar (the handoff draws none here: the bar is the
  sidebar's). Hover ink-2 fg-1.
- **Content** `<main>`: max-width 1180, width 100%, padding 36px 32px 64px, column flex, 22px
  gap. (Not 1440/26: the handoff's Settings main is narrower and tighter.)
- **Page head** (`StHead`): a flex row, align flex-end, gap 16: a column (gap 10, flex 1) with
  the h1 in the headline type (400 30px sans fg-1, `.cv-headline`) and a description 15px fg-2,
  `text-wrap: pretty`; then the page's one primary action at the right (sm, plus icon), or
  nothing.
- **Panels** are `.cv-panel`. Two row shapes:
  - **Connection row** (`ConnList`, for Registries, Backup storage and Notifications): grid
    `14px minmax(0, 1.5fr) minmax(0, 1fr) 150px auto`, gap 18, align center, min-height 64,
    padding 10px 18px, 1px line-1 between rows: a 9px mark; name 500 14px/1.2 mono fg-1
    ellipsized over a kind line 13px fg-3; a two-line mono 12px/1.3 column: the state (fg-2;
    failed or warn hue when it is one) over the last event fg-3; a "used by" cell 400 12px mono
    fg-3; then the actions, right-aligned, gap 6. Under a row that needs the owner, a note
    13px/1.5 fg-2 with padding 0 18px 14px 50px (indented past the mark).
  - **Setting row** (`StField`, for Account and the destination form): grid `220px
    minmax(0, 1fr)`, gap 24, padding 18px 20px, border-top 1px line-1 (none on the first): the
    label 500 14px/1.2 sans fg-1 over a hint 13px/1.45 fg-3; the control column, align
    flex-start, gap 10. Inputs inside are the page 2 field at a fixed width of 360px.
- **Confirmations** are `<details>` as today; the handoff's "…" menus are not drawn: every
  action is a visible sm button. Secondary for Test, Edit, Pause, Resume; danger for Remove and
  Revoke (inside the confirmation; the summary itself is secondary); primary only for the one fix
  a row asks for ("Update password" on a refused registry) and the page's add action.
- **"Shown once" panels** (a new token, a new signing secret): a `.cv-panel` with padding
  16px 18px, column gap 10, drawn first on the page, as today: the sentence 14px fg-1 ("Copy
  `name` now. Caravana only keeps a hash and can't show it again."), then a row with the value
  in a box (flex 1, padding 10px 12px, radius 2, ink-0 ground, 1px line-2, 400 13px mono fg-1,
  `select-all`) and, after it, the curl example as today in `.cv-cmd-quiet`. The handoff's Copy
  button stays hidden without script, as the console already does.
- Under 900px the Settings nav becomes a row of links above the content (horizontal scroll,
  same item shape), not a second column.

## Account (`/settings`)

- Head: "Account" / "Who you are on this caravana, and your password."
- One panel of setting rows:
  - **Email**: the email in 400 14px mono fg-1 and the hint "The one admin. Teams come later."
    (read-only; no control);
  - **Password**: the form as today, laid as a control column: Current password, New password
    (hint "At least 12 characters."), then **Change password** (primary sm). The success notice
    "Password changed. Other browsers were signed out." draws above the fields as today.
- Nothing else on this page. Signing out stays in the sidebar's account row.

## Registries (`/settings/registries`)

- Head: "Registries" / "Credentials Caravana uses to read private images." and **Add registry**
  (primary sm, plus icon, link to `#add`).
- Under the head, the one-line sentence today's section has about the read-only token and the
  two container variables, 13px fg-3.
- Panel of connection rows, sorted problems first then by host:
  - mark: ■ when the last test was refused, ● when it passed, ○ when never tested;
  - name: the host; kind: "user `<username>`" (13px fg-3, the name in mono);
  - state: "works · tested 2 h ago", or "refused · tested 2 h ago" in the failed hue with the
    message as the row's note ("401 unauthorized: the user name or password is wrong"), or "not
    tested yet";
  - used by: "used by 3 apps" (the count of apps whose services' image host is this registry;
    "not used" for none);
  - actions: **Update password** (primary sm) only on a refused row, which opens the same Edit
    confirmation; **Test** (secondary sm, a plain post); **Edit** (`<details>` with User name and
    Password or token, "Leave empty to keep the current one.", **Save**); **Remove** (`<details>`
    with today's sentence and a danger **Remove**).
  - Empty: the `.cv-empty` block "No registries yet. Public images from any registry deploy
    without one." and Add registry.
- **Add** (`id="add"`): a `.cv-panel` of setting rows: Registry (host, the hint as today), User
  name, Password or token; then **Save** (primary sm) in the last row's control column.

## Backup storage (`/settings/backup-storage`)

- Head: "Backup storage" / "One S3-compatible bucket for database and volume backups." No add
  action: there is one destination.
- With a destination: a panel with one connection row: mark ● when the last run that used it
  succeeded, ■ when it failed, ○ when none ran yet; name `endpoint/bucket` (prefix appended with
  "/" when set); kind "S3-compatible · region `<region>`"; state "last backup 02:00 today ·
  1.9 GB" or "last backup failed 3 h ago" (failed hue) or "no backup yet"; used by "4 services"
  (the count with a backup policy); actions: **Edit** (`<details>` opening the form below in
  place) and **Remove** (`<details>`, danger, "Backups stop until a destination is set again.").
  `Remove` needs `DELETE /v1/settings/backup-destination` (API 0.22.x) if it doesn't exist: say
  so in the tasks; otherwise leave Remove out and note it.
- Without a destination: the problem panel from page 2 ("No backup destination is set. Backups
  can't run until there is one.") only when a service has a policy; otherwise the form alone.
- **The form**: a `.cv-panel` of setting rows, as today's fields: Endpoint, Region, Bucket,
  Prefix (optional), Access key ID, Secret access key ("Leave empty to keep the current one."
  when one is kept); **Save destination** (primary sm). The saved notice as today.

## Notifications (`/settings/notifications`)

- Head: "Notifications" / "Where Caravana tells you about problems and how deploys end." and
  **Add channel** (primary sm, plus icon, link to `#add`).
- The shown-once signing-secret panel first, when there is one.
- `SectionHead` "Channels" over a panel of connection rows, sorted problems first then by name:
  - grid `14px 20px minmax(0, 1fr) 90px 200px auto`, gap 14, min-height 52, padding 0 18px (the
    handoff's channel row, with a 15px kind icon fg-3 after the mark: `hash` for Slack,
    `message-square` for Discord, `bell` for ntfy, `webhook` for a webhook);
  - mark: ◆ when the last delivery failed or the channel is paused, ● otherwise, ○ when nothing
    was sent yet;
  - name 500 14px/1.2 mono fg-1 ellipsized, with " paused" in fg-3 when paused; kind 13px fg-3
    ("Slack", "Discord", "ntfy", "Webhook"); last 400 12px mono: "sent 6 min ago" fg-3, "failed
    1 h ago" warn hue (the error as the row's note), "nothing sent yet";
  - actions: **Send a test** (secondary sm, post), **Pause**/**Resume** (secondary sm, post),
    **Edit** (`<details>` with today's fields and **Save**), **New signing secret** (webhook only,
    `<details>`), **Remove** (`<details>`, danger);
  - **Recent deliveries** stays a `<details>` under the row (today's list), as the row's note
    area, closed by default.
  - Empty: `.cv-empty` "No channels yet. Add one and Caravana tells you when something needs
    you." with Add channel.
- The handoff's "What goes where" grid is **not drawn**: every channel receives every event
  today (018 has no per-event rules). In its place, one sentence under the Channels panel, 13px
  fg-3: "Every channel gets what needs you and how deploys end. Repeats of the same problem are
  sent once." Per-event rules are a later feature.
- **Add** (`id="add"`): a `.cv-panel` of setting rows: Kind (select), Name, URL or topic, ntfy
  access token (the fields today's `notify-fields` draws, with their hints); **Add channel**
  (primary sm).

## API tokens (`/settings/tokens`)

- Head: "API tokens" / "For CI, scripts and `caravana mcp`. A token can do everything, or deploy
  one app." and **New token** (primary sm, plus icon, link to `#new`).
- The shown-once panel first, when a token was just made (today's, with the curl example).
- Panel with the handoff's token table: head 38px ink-2 `.cv-label` cells "Token · Can do · Last
  used · Expiry" over columns `14px minmax(0, 1fr) minmax(0, 1.2fr) 110px 150px auto`, gap 18,
  min-height 54, padding 0 18px, 1px line-1 between rows:
  - mark 9px: ◆ for an active full token with no expiry or unused for 90 days, ○ for an expired
    or revoked one, ● otherwise;
  - Token: name 500 14px mono fg-1 over "made 12 d ago" 400 12px mono fg-3 (the console has no
    "by": one admin);
  - Can do: 400 13px/1.3 mono fg-2: "Everything" for a full token, "Deploy · app web of project
    shop" for a deploy token;
  - Last used: 12px mono fg-3 ("never" when never);
  - Expiry: 12px mono: "expires in 80 d" fg-3, "never expires" in the warn hue, "expired 3 d
    ago" / "revoked 3 d ago" fg-3;
  - actions: **Revoke** (`<details>` as today, danger inside) for an active token; nothing for
    the others;
  - under a ◆ row, the note 13px/1.5 fg-2, padding 0 18px 14px 50px: "A token that can do
    everything and never expires. Give it an end date, or make one that only deploys an app."
    or "Not used in 94 days. Revoke it if nothing needs it.";
  - sorted: active first (problems first within), then expired, then revoked; the console's
    "Show older" rule for revoked ones, if there is one, stays.
- **New** (`id="new"`): a `.cv-panel` of setting rows: Name, Expires (optional), then **Only for
  one app** as a row whose control column holds Project (select, when more than one) and App
  name with today's hints; **Create token** (primary sm).

## May differ (server-rendered, no JavaScript)

- No "…" menus, no drawers, no org switcher in the crumbs; confirmations are `<details>`; Copy
  buttons stay hidden without script.
- The notifications rules grid is left out (no feature behind it), as is every entry whose
  feature doesn't exist.
- Counts and marks on the nav are computed per render; no live region on Settings pages.
- Under 900px the Settings nav is a scrolling row above the content.

## Review

The designer checks each page live at 127.0.0.1:7070 against `p3-*.png`:
- the nav with a count on Registries and Notifications, a ■ on Registries (a refused test) and
  a ◆ on API tokens (a token with no expiry);
- Account; Registries with a working, a refused and a never-tested row, and empty; Backup
  storage with and without a destination; Notifications with a working, a failed and a paused
  channel, and empty; API tokens with a full token without expiry, a deploy token, an expired
  and a revoked one, and the shown-once panel;
- the crumbs on each page; narrow (390px) for two of them.

## After the designer's review (2026-10-22)

Fixes:
1. **Narrow (under 900px).** Settings pages follow the shell's narrow rule like every other page: no sidebar, and the Menu bar instead. The Settings nav becomes a horizontally scrolling row of the same items above the content. Check at 390px and 800px.
2. **Notifications.** "New signing secret" moves inside the Edit `<details>` (under the fields, before Save), so a row has at most four buttons on one line.
3. **API tokens.** The Expiry column is 170px, so "expired 9 minutes ago" stays on one line.
4. **Add panels.** Each opens with a `.cv-label` first row, so `#add` and `#new` land on a named thing:
   - "Add a registry";
   - "Add a channel";
   - "New token".

Rulings on the build's departures, all yes, with these details:
- **Per-channel events** are kept, with the sentence "…as ticked in its form" and the events as a tooltip on the kind. When a channel has fewer than all three events, the kind line also says them in words, e.g. "Webhook · problems only".
- **Unused for 90 days:** a never-used token counts from its creation, and only after 90 days, so a fresh token is never ◆.
- **Also yes:** the address under the name; relative words, with a date past 60 days; the nav inside `<main>`; "Only for one app" as a plain row; `<label for>`; the four Backup storage choices; the "Settings · <entry>" titles; shown-once values only in the 200 answer, with no-store.
