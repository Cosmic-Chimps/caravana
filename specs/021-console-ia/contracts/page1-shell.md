# Contract: page 1, the shell, Projects and the app header

Written by the caravana_designer session on 2026-10-21 and approved by it. The values come from the
handoff's `Shell.jsx`, `AppScreen.jsx`, `Tabs.jsx`, `StatusPill.jsx`, `Button.jsx` and
`theme/caravana.css`, in `design/console/design/ui_kits/console/` and `design/console/theme/`.
Where this list and the handoff disagree, this list wins: it records the decisions the console
made.

## Corrections after rendering the handoff (designer, 2026-10-21)

The designer rendered the prototype (screenshots in `design/console/_shots/`, gitignored). These
override the sections below where they disagree:
1. **Crumbs on an app page:** `project / environment` only, as on its project page. The app's name
   is not a crumb; the h1 carries it. Projects stays the active sidebar item.
2. **The back link above the title:** "← Home", to `/apps`, in 400 13px mono fg-3. The handoff's
   "← Now" is the Operator home.
3. **Tab counts:** "Running" carries the copies count. "Deployments" (the handoff's Ship tab)
   carries the live version as its count, `v31`, in 400 12px mono fg-3. The status mark before
   "Running" is a 6px rolling ◐ or warning ◆, with no mark when healthy.
4. **The domain link in the title row** is underlined: the theme's `a` rule, fg-1, underline offset
   3px, healthy blue on hover, 400 13px mono. Today's bare `cv-data` links are not the model.

The render confirmed the rest:
- the sidebar ground is ink-0, like the page, separated only by the 1px line-1;
- the active item's bar is the accent;
- Settings has its own block above the account row;
- the email is ellipsized, with the log-out icon at the right;
- "New app" is at the top bar's right edge;
- the live indicator is a 6px dot plus "live";
- the title row's order: name, pill, domain, spacer, actions.

## Scope notes

- **Sidebar entries:** only entries that have a page:
  - Workspace › Home and Projects;
  - Settings and the account row at the foot.
- **Left out of the sidebar:**
  - Deploys, Previews, Logs, Monitoring and Schedules, which arrive with their own features; a nav
    item to "not designed yet" is worse than no item;
  - the handoff's "Configure" group (Domains, Variables & secrets, Backups), which is per-app in 021;
  - the Developer/Operator toggle and the organization switcher, until an Operator view and
    organizations exist (Phase 3/4). Nothing stands in for them.
- **Projects needs a landing page,** `/projects`:
  - a panel of rows: the project name in mono 500, its description 15px fg-3 if any, the
    environment names as small pills, and the app count;
  - each row links to `/projects/{project}`.

## Sidebar `<aside>`

- **The aside:** 248px wide, fixed, full height, z-index 6, column flex, background **ink-0** (the
  console's is ink-1 today: change it), border-right 1px line-1.
- **Top block:**
  - padding 14px 14px 12px, border-bottom 1px line-1;
  - the brand, in the handoff org switcher's exact shape, but as a link to Home: not a button, no
    chevron, no open state;
  - 40px tall, padding 0 8px, radius 4, gap 10;
  - a 26×26 mark: radius 4, ink-3 ground, 600 13px mono fg-1, holding the host's first letter in
    upper case;
  - beside it, two lines 4px apart: the host in 500 14px sans fg-1, then `caravana <version>` in
    400 12px mono fg-3;
  - without the mode toggle, the block's 12px inner gap goes.
- **Nav:**
  - flex 1, overflow auto, padding 16px 10px, 22px between groups, 2px between items;
  - group title "Workspace": padding 0 10px 8px, upper case, mono 500, letter-spacing 0.06em. Use
    the console's `.cv-label` (12px, fg-3), not the handoff's 11px fg-4. The 12px label is a
    recorded DESIGN.md deviation, and the handoff's own contrast rule keeps fg-4 for non-text.
- **Nav item:**
  - 34px tall, padding 0 10px, radius 4, gap 12, 400 14px sans; icon 16px, 1.5px stroke, in a 16px
    box;
  - **inactive:** text fg-2, icon fg-3;
  - **active:** ground ink-2, text and icon fg-1, and an inset 2px bar at the left edge in the
    accent (oklch 0.76 0.12 245), the handoff's `inset 2px 0 0 var(--accent)`. DESIGN.md says the
    bar is fg-1 today: update that sentence;
  - **hover:** ground ink-2, text fg-1, 120ms ease-cv;
  - **which item is active:** Home on `/apps` (and `/`); Projects on `/projects`, `/projects/*`
    and every `/apps/{id}*` page.
- **Settings row:** its own block, padding 8px 10px, border-top 1px line-1. It holds one item,
  styled exactly as a nav item (icon settings), active on `/settings*`.
- **Account row:**
  - padding 12px 14px, border-top 1px line-1, gap 10;
  - a 30px circle: ink-3, 500 12px mono fg-2, with initials (the first two letters of the email's
    local part, upper case);
  - two lines 4px apart: "Account" in 500 13px sans fg-1, and the email in 400 12px mono fg-3,
    ellipsized;
  - at the right, a 30×30 icon button: log-out icon 15px, fg-3, radius 4, hover ink-3 fg-1, with
    `aria-label` and `title` "Sign out";
  - the button is the submit of a `<form method="post" action="/logout">`, so sign-out stays a
    POST.
- **Icons:**
  - Lucide, as inline `<svg>` from a template function, with `currentColor`, `aria-hidden`, stroke
    1.5;
  - no icon font, no fetch;
  - needed now: house, folder, settings, log-out, plus, rotate-ccw, rocket;
  - Lucide is ISC: list it in `internal/ui/static/js/LICENSES.md`, or in a LICENSES.md beside the
    icons.

## Top bar `<header>`

- **The bar:** 52px, sticky top 0, z-index 5, ink-0, border-bottom 1px line-1, padding 0 26px,
  gap 20.
- **Breadcrumbs:**
  - project / environment / page, with no organization level;
  - each crumb 30px tall, padding 0 8px, radius 4, 500 14px mono;
  - the last crumb is fg-1; the others are fg-2 links, with an ink-2 ground on hover;
  - the separator is "/" in 400 16px mono fg-4, with 6px gaps;
  - no chevrons: the handoff's crumbs are switchers, the console's are links;
  - **examples:** an app page reads `Default / production`, the same as its project page. The
    app's name is not a crumb (the h1 carries it), and its tabs add none. Settings reads
    `Settings`, and New app reads `New app`;
  - today's "›" separators and 14px sans go.
- **Live indicator:**
  - a 6px healthy dot (not 8), gap 10, 400 12px mono fg-3, and the word "live";
  - no clock (it would need a ticking script);
  - keep the console's three states and its one-tick ping.
- **"New app":**
  - a primary sm button at the right edge, on every page: 28px, padding 0 10px, 500 13px, gap 7,
    with the plus icon at 14px;
  - the per-page `action` slot goes: app actions live in the app header, as the handoff draws.

## App header (AppScreen.jsx)

- **Where:** inside the app page's `main`, which is max 1440px, padding 26px 26px 62px, with a 23px
  stack gap. The other pages keep 36/64/28.
- **Header block:** 16px gap.
- **Back link:**
  - 400 13px mono fg-3, "← Home", to `/apps` (the handoff's "← Now" is the Operator home);
  - it replaces today's `cv-where` line, which duplicates the top bar's crumbs.
- **Title row:**
  - flex, align center, gap 18;
  - **h1:** 500 24px mono fg-1;
  - **status pill md:** 22px, padding 0 8px, radius 2, tinted ground plus a 1px tinted line,
    500 12px mono, a 7px mark, gap 6; idle reads "stopped";
  - **domains:** each a link in 400 13px mono, underlined: the theme's `a` rule, fg-1, underline
    offset 3px, healthy blue on hover (not today's bare `cv-data` links);
  - then a spacer, then right-aligned sm buttons:
    - **Restart:** secondary, rotate-ccw icon;
    - **Stop…:** secondary, no icon (the console's addition);
    - **Deploy:** primary, rocket icon. It's a link to the Deployments tab's deploy form, because a
      one-click redeploy from the header would be a surprise;
  - a stopped app shows **Start** (primary) and no Deploy;
  - today's `cv-actions` row under the lede goes.
- **Lede:**
  - 15px fg-2, one line, `text-wrap: pretty`, with the version in mono;
  - it keeps today's facts (version and last deploy, since there is one node), with the copies
    grammar:
    - **one service:** "1 copy running v7." / "2 copies running v7.";
    - **several, different counts:** "2 services running v2: hollo 2 copies, postgres 1.";
    - **several, same count:** "2 services running v2: hollo and postgres, 1 copy each." With three
      or more: "a, b and c, 1 copy each";
    - **a short service:** its part becomes "hollo 1 of 2 copies healthy", as in "2 services
      running v2: hollo 1 of 2 copies healthy, postgres 1.", and the pill is warning;
  - then " Last deployed 3 minutes ago from the console.", as today;
  - the rolling-out, stopped, never-deployed and Nomad-unreachable sentences stay as they are;
  - **data:** this needs per-service counts on `service.AppStatus` (name, wanted, healthy),
    computed where HealthyCopies is. The sentence stays a pure function in `view.go`, with table
    tests for the five shapes.
- **Environments line (Copy to / Promote to):** keep it, under the lede, as today. The handoff
  never draws it.
- **Tabs:**
  - gap 26, 46px tall, 500 13.5px sans, margin-bottom -1 over a 1px line-1 rule;
  - **active:** fg-1 with a 2px fg-1 underline; **inactive:** fg-3; **hover:** fg-2;
  - **count:** 400 12px mono fg-3, gap 9. "Running" carries the copies count; "Deployments" (the
    handoff's Ship tab) carries the live version, e.g. `v31`;
  - add the handoff's 6px status mark before "Running" when the app's state isn't healthy: rolling
    ◐ or warning ◆, none when healthy.

## May differ (server-rendered, no JavaScript)

- **Left out:** the org dropdown, the mode toggle, crumb switchers (as listed above) and the clock.
- **Hover and focus:** CSS only. Focus is a 2px accent ring at 55%, offset 2px, as both the handoff
  and DESIGN.md already say.
- **Narrow layout:** under 900px the console keeps its Menu `<details>` in the top bar, holding the
  same sidebar blocks. The handoff has no narrow layout.
- **Forms and links:** sign-out and the app actions are forms; Deploy is a link.
- **Icons:** inline SVG. The 120ms transitions and the 0.5px press are already in `.cv-btn`.
- **DESIGN.md deviations:** keep the two recorded ones, the 12px label and the one-tick live ping.
- **DESIGN.md text:** update its Shell, Navigation and "no icons" sentences in the same release.

## Review

The designer checks the page live at 127.0.0.1:7070, state by state:
- healthy, rolling, degraded, failed, stopped, never deployed;
- narrow.
