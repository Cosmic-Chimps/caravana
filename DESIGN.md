---
name: caravana console
description: A dark, dense instrument panel for a small fleet of apps on Nomad. Calm when everything is fine, impossible to miss when something isn't.
colors:
  unlit-panel: "oklch(0.165 0.006 255)"
  panel-face: "oklch(0.195 0.007 255)"
  raised-control: "oklch(0.225 0.008 255)"
  lit-control: "oklch(0.260 0.009 255)"
  hairline: "oklch(0.275 0.009 255)"
  control-edge: "oklch(0.340 0.010 255)"
  strong-edge: "oklch(0.440 0.012 255)"
  readout-white: "oklch(0.955 0.004 255)"
  body-text: "oklch(0.810 0.008 255)"
  label-text: "oklch(0.680 0.010 255)"
  dim-mark: "oklch(0.500 0.010 255)"
  signal-blue: "oklch(0.76 0.12 245)"
  signal-blue-ground: "oklch(0.27 0.05 245)"
  signal-blue-edge: "oklch(0.44 0.08 245)"
  transit-lilac: "oklch(0.84 0.09 300)"
  transit-lilac-ground: "oklch(0.28 0.045 300)"
  transit-lilac-edge: "oklch(0.46 0.07 300)"
  caution-amber: "oklch(0.90 0.13 88)"
  caution-amber-ground: "oklch(0.29 0.05 88)"
  caution-amber-edge: "oklch(0.50 0.08 88)"
  fault-ember: "oklch(0.68 0.17 40)"
  fault-ember-ground: "oklch(0.27 0.06 40)"
  fault-ember-edge: "oklch(0.46 0.10 40)"
  primary-press: "oklch(0.88 0.006 255)"
  danger-press: "oklch(0.31 0.07 40)"
typography:
  headline:
    fontFamily: "IBM Plex Sans, ui-sans-serif, system-ui, sans-serif"
    fontSize: "30px"
    fontWeight: 400
    lineHeight: 1.2
  title:
    fontFamily: "IBM Plex Sans, ui-sans-serif, system-ui, sans-serif"
    fontSize: "18px"
    fontWeight: 500
    lineHeight: 1.35
  lede:
    fontFamily: "IBM Plex Sans, ui-sans-serif, system-ui, sans-serif"
    fontSize: "15px"
    fontWeight: 400
    lineHeight: 1.5
  body:
    fontFamily: "IBM Plex Sans, ui-sans-serif, system-ui, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: 1.35
  label:
    fontFamily: "IBM Plex Mono, ui-monospace, SF Mono, Menlo, monospace"
    fontSize: "12px"
    fontWeight: 500
    lineHeight: 1
    letterSpacing: "0.06em"
  data:
    fontFamily: "IBM Plex Mono, ui-monospace, SF Mono, Menlo, monospace"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.35
    fontFeature: "\"tnum\" 1"
  metric:
    fontFamily: "IBM Plex Mono, ui-monospace, SF Mono, Menlo, monospace"
    fontSize: "24px"
    fontWeight: 500
    lineHeight: 1
    fontFeature: "\"tnum\" 1"
  wordmark:
    fontFamily: "IBM Plex Mono, ui-monospace, SF Mono, Menlo, monospace"
    fontSize: "20px"
    fontWeight: 600
    lineHeight: 1
    letterSpacing: "-0.01em"
rounded:
  xs: "2px"
  sm: "4px"
  md: "6px"
spacing:
  "1": "2px"
  "2": "4px"
  "3": "8px"
  "4": "10px"
  "5": "16px"
  "6": "20px"
  "7": "26px"
  "8": "32px"
  "9": "40px"
  "10": "60px"
components:
  button-primary:
    backgroundColor: "{colors.readout-white}"
    textColor: "{colors.unlit-panel}"
    typography: "{typography.body}"
    rounded: "{rounded.sm}"
    padding: "0 14px"
    height: "36px"
  button-primary-hover:
    backgroundColor: "{colors.primary-press}"
  button-secondary:
    backgroundColor: "{colors.raised-control}"
    textColor: "{colors.readout-white}"
    typography: "{typography.body}"
    rounded: "{rounded.sm}"
    padding: "0 14px"
    height: "36px"
  button-secondary-hover:
    backgroundColor: "{colors.lit-control}"
  button-ghost:
    backgroundColor: "transparent"
    textColor: "{colors.body-text}"
    rounded: "{rounded.sm}"
    padding: "0 10px"
    height: "28px"
  button-ghost-hover:
    backgroundColor: "{colors.lit-control}"
    textColor: "{colors.readout-white}"
  button-danger:
    backgroundColor: "{colors.fault-ember-ground}"
    textColor: "{colors.fault-ember}"
    rounded: "{rounded.sm}"
    padding: "0 14px"
    height: "36px"
  button-danger-hover:
    backgroundColor: "{colors.danger-press}"
  input:
    backgroundColor: "{colors.raised-control}"
    textColor: "{colors.readout-white}"
    typography: "{typography.data}"
    rounded: "{rounded.sm}"
    padding: "0 12px"
    height: "32px"
  input-sign-in:
    backgroundColor: "{colors.panel-face}"
    textColor: "{colors.readout-white}"
    typography: "{typography.data}"
    rounded: "{rounded.sm}"
    padding: "0 12px"
    height: "42px"
  command-block:
    backgroundColor: "{colors.raised-control}"
    textColor: "{colors.readout-white}"
    typography: "{typography.data}"
    rounded: "{rounded.sm}"
    padding: "8px 12px"
  notice:
    textColor: "{colors.readout-white}"
    typography: "{typography.body}"
  panel:
    backgroundColor: "{colors.panel-face}"
    rounded: "{rounded.md}"
  status-pill-healthy:
    backgroundColor: "{colors.signal-blue-ground}"
    textColor: "{colors.signal-blue}"
    typography: "{typography.data}"
    rounded: "{rounded.xs}"
    padding: "0 8px"
    height: "22px"
  status-pill-rolling:
    backgroundColor: "{colors.transit-lilac-ground}"
    textColor: "{colors.transit-lilac}"
    rounded: "{rounded.xs}"
    height: "22px"
  status-pill-warning:
    backgroundColor: "{colors.caution-amber-ground}"
    textColor: "{colors.caution-amber}"
    rounded: "{rounded.xs}"
    height: "22px"
  status-pill-failed:
    backgroundColor: "{colors.fault-ember-ground}"
    textColor: "{colors.fault-ember}"
    rounded: "{rounded.xs}"
    height: "22px"
  status-pill-idle:
    backgroundColor: "{colors.raised-control}"
    textColor: "{colors.label-text}"
    rounded: "{rounded.xs}"
    height: "22px"
  status-pill-quiet:
    backgroundColor: "transparent"
    typography: "{typography.data}"
    padding: "0"
    height: "22px"
  tab:
    textColor: "{colors.label-text}"
    height: "46px"
  tab-active:
    textColor: "{colors.readout-white}"
  sidebar:
    backgroundColor: "{colors.unlit-panel}"
    width: "248px"
  top-bar:
    backgroundColor: "{colors.unlit-panel}"
    height: "52px"
  confirm-body:
    backgroundColor: "{colors.raised-control}"
    textColor: "{colors.readout-white}"
    typography: "{typography.body}"
    rounded: "{rounded.sm}"
    padding: "14px 16px"
---

# Design System: caravana console

<!-- Source: the shipped CSS of features 006 and 007 (internal/ui/style/theme.css and input.css,
built into internal/ui/static/app.css by tools/ui-css.sh), the templates in internal/ui/templates
and internal/ui/static/js/console.js. It started from the Claude Design handoff "caravana console",
Handoff 2, 2026-10-04 (local only, in the gitignored design/console/), and keeps the handoff's
tokens 1:1. Two handoff values changed when it was built: the label is 12px, not 11px, and the
live-dot ping runs once, not forever. Pages built: /login, /settings, /apps, /apps/new and the four
tabs of /apps/{id}. The handoff's Developer/Operator switch, org and project levels, capacity meters,
the Switch control and the activity feed's drop-in are not built, so they are not recorded here. -->

## Overview

**Creative North Star: "The Quiet Instrument Panel"**

The console is an instrument panel for a small fleet of apps on one or a few VMs. When things are
fine it stays quiet, almost dull: one cool neutral ramp, hairline panels, mono readouts that
don't move. When something needs a human, that one thing lights up in a color it doesn't share
with anything else, with a shape you can read without seeing color, and a plain sentence next to
it. Every surface is either a readout or a control, and nothing is decoration.

The density belongs to a tool someone glances at between other work. Rows sit tight, panels are
flat, numbers sit in mono so they line up and so it's obvious they're real values, not prose.
The voice of the screen is a single headline sentence about the present. The raw Nomad truth sits
underneath it in small mono detail lines, there for anyone who wants it.

Confirmed rejections from the handoff: not a Dokploy look-alike, not a gradient "AI dashboard",
not a Grafana wall of charts. An earlier light "field notebook" direction was tried and turned
down. The information architecture survived, the look didn't.

**Key Characteristics:**
- Dark, cool neutral ground (hue 255, chroma ≤ 0.012), with depth from lightness steps, never shadow.
- Status is the only color, and every status pairs a shape with its color.
- IBM Plex Sans for sentences, IBM Plex Mono for anything Nomad-real.
- Flat hairline panels, small radii, no gradients.
- Motion only when an event arrives, and only once.

## Colors

One near-colorless steel ramp for everything structural, and four status colors kept for status
alone.

### Primary
- **Readout White** (oklch 0.955 0.004 255): primary text, and the fill of the primary button.
  The main action is "lit", not colored. caravana has no brand hue.

### Neutral
- **Unlit Panel** (oklch 0.165 0.006 255): the page ground, behind everything.
- **Panel Face** (oklch 0.195 0.007 255): panels, the Menu panel.
- **Raised Control** (oklch 0.225 0.008 255): inputs, chips, secondary buttons, table headers,
  row hover inside a panel.
- **Lit Control** (oklch 0.260 0.009 255): hover on raised controls, selected rows, ghost-button
  hover.
- **Hairline** (oklch 0.275 0.009 255): panel borders and row dividers.
- **Control Edge** (oklch 0.340 0.010 255): input and button borders, overlay rims.
- **Strong Edge** (oklch 0.440 0.012 255): the border of a hovered control, and link underlines.
- **Body Text** (oklch 0.810 0.008 255): ordinary text.
- **Label Text** (oklch 0.680 0.010 255): section labels, mono detail lines, timestamps.
- **Dim Mark** (oklch 0.500 0.010 255): disabled text and marks that aren't text (axis ticks,
  separators). It fails 4.5:1, so it never carries meaning on its own.

### Status
Each status has a full-strength color, a tinted ground and an edge. They differ in lightness as
well as hue (failed 0.68 < healthy 0.76 < rolling 0.84 < warning 0.90), so they still separate
in grayscale and for people with color vision deficiency.
- **Signal Blue** (oklch 0.76 0.12 245), healthy ●. It's also the focus ring (at 55%), the current
  sidebar item's bar and the live-tick flash (at 16%). Healthy is blue, not green.
- **Transit Lilac** (oklch 0.84 0.09 300), rolling ◐: a deploy in progress, a copy starting or
  draining.
- **Caution Amber** (oklch 0.90 0.13 88), warning ◆: a human should look soon: a restarted copy's count, a deploy
  warning, the Reconnecting indicator.
- **Fault Ember** (oklch 0.68 0.17 40), failed ■: something is broken or was reverted. It's an
  orange-red, deliberately not a pure red.

### Named Rules
**The Shape-and-Color Rule.** Status is never color alone. Every status shows its mark next to
its color: ● healthy, ◐ rolling, ◆ warning, ■ failed, ○ idle. Hide the color and the status must
still read. A mark is 7px, in pills and rows alike; the warning diamond is a 5.46px square turned
45°, with a 0.77px margin (0.78 and 0.11 of 7), so it fills the same box. A tab's mark is 6px,
by the same ratios.

**The Status-Only Color Rule.** The four status hues mean status and nothing else. Buttons, links,
tabs and decoration stay neutral. When a screen is fully healthy, the only color on it is Signal
Blue on the status marks.

**The Flat Ground Rule.** No gradients anywhere. The one exception is the hard-stop half fill of the
rolling mark, which is a shape, not a blend.

## Typography

**Body Font:** IBM Plex Sans (with ui-sans-serif, system-ui)
**Data Font:** IBM Plex Mono (with ui-monospace, SF Mono, Menlo)

**Character:** two cuts of one family, so sentences and readouts sit together without fighting.
Sans is how caravana talks; mono is what Nomad reported.

### Hierarchy
- **Headline** (400, 30px, 1.2; 24px under 640px): the page h1, written as the sentence about the
  present ("1 app needs you: whoami has a failed deploy. 2 apps healthy."). It's regular weight.
  The size carries it, not boldness.
- **Lede** (400, 15px, 1.5): the one-line description under the headline, in Body Text.
- **Title** (500, 18px, 1.35): panel titles.
- **Body** (400, 14px, 1.35): all UI text, buttons, tab labels.
- **Label** (500, 12px mono, 0.06em, uppercase): section heads like "COPIES" and "DEPLOY", and
  field labels above inputs. A name inside a label ("Type blog to confirm") drops the uppercase so a
  case-sensitive match can be read.
- **Data** (400, 13px mono, tabular figures): names, versions, digests, ids, ports, hosts,
  durations, timestamps, every number in a table. A name set inside a sentence is mono at 0.9em
  (`cv-inline-mono`). The log text alone is 12.5px mono, line height 1.6, to sit tighter than Data.
- **Metric** (500, 24px mono, tabular figures): the app page's name (`cv-apptitle`, 24px, line 1.15).
  No metric tile is built.
- **Wordmark** (600, 20px mono, -0.01em): the word "caravana", lowercase. There's no logo mark.

### Named Rules
**The Mono-Is-Real Rule.** If Nomad or the user's config produced it (`whoami`, `v24`, `node-1`,
`sha256:9f1c…`, `512 MB`, `14:02:11`), it's set in mono. If caravana is explaining something,
it's sans. Never mix the two inside one token.

**The Tabular Rule.** Every value that updates live uses tabular figures, so a number changing
in place never shifts its neighbors.

## Layout

- **Shell:** a fixed left sidebar (248px, Unlit Panel, a Hairline at its edge) with the brand block
  on top (the host's initial, the host and `caravana <version>`, a link to Home), a Workspace group
  (Home, Projects), Settings in a block of its own and, at the foot, the account row: initials,
  "Account", the email in mono and a Sign out icon button. A sticky top bar (52px, Unlit Panel, a
  Hairline under it) holds the breadcrumbs on the left (`Default / production`: project and
  environment, never the app, whose name is the page's h1), then the live indicator, then "New app"
  at the right edge on every page. A skip link to the page sits above all of it.
- **Content:** a centered column, max 1440px, with 26px side gutters, 36px top and 60px bottom
  padding. It's a vertical stack, sections 28px apart. An app's page is tighter, as the design
  draws it: 26px top, 62px bottom, sections 23px apart.
- **Page header:** the headline and its lede, stacked, 900px wide at most. An app's header is
  different: a back-link row ("← Home" in 13px mono; under 900px, where the top bar's crumbs are
  hidden, it continues "· Shop / staging": the project a link, the environment plain text, and
  nothing for an app in Default › production), then the name (24px mono), the status pill, the
  addresses and, at the right, the actions on one wrapping line, then the summary sentence, the
  "Other environments" line when the project has another, and the tabs. The actions are shown only
  when they can work: Restart and Stop… need a live version, and "Promote to <env>" needs a defined,
  running app whose newest deployment is healthy with its definition recorded; otherwise the
  environment's name is a plain link. The header's buttons and tabs have stable IDs, so the live
  swap keeps the focus.
- **Sections:** a mono label as the section's own heading over a panel. Rows inside panels are CSS
  grid with 1px dividers between them, 46-50px tall for list rows and 36px for table heads.
- **Apps page:** a main column plus a 400px right rail holding Activity (one deployment per row, the
  whole row a link to its app). What needs a human is said in the headline, not in a rail panel.
  The rail drops below the main column under 1452px, when the column beside it would be under 720px.
- **App pages:** five tabs by intent, in this order: Running, Deployments, Configure, Logs, Backups. The tab
  is named "Deployments", never "Ship". Running is the app's own page (the copies in a live
  table), Deployments holds the deploy form and the history, Configure the grid of Copies & size and
  Deploy, the Domains and Secrets panels, the webhook and Delete app, Logs the output of the copies,
  Backups the app's backed-up services. Each tab is its own URL. Variables & secrets
  (`/apps/{id}/variables`) and Domains (`/apps/{id}/domains`) are Configure's own pages: they draw the
  app header with the Configure tab current, and no second back link.
- **Login:** the only page without the shell. A centered 380px column on the bare ground: wordmark,
  the host in mono, "Sign in", two 42px fields, one primary button, and the reset command in its
  own block under it.
- **Settings:** one page per entry under a sticky 232px nav of its own, inside the shell (see
  "Settings (feature 021)" under Components). Account is `/settings`; the others have their own addresses.
- **Primary actions:** the frame has two, as the design draws it: "New app" in the top bar, and on an
  app's page the header's "Deploy" (a link to the Deployments tab's deploy form, or "Start" for a
  stopped app). A page's own content adds at most one more, in the form it submits ("Deploy" on
  Deployments, "Deploy" or "Save" on Configure), so pressing it deploys what that page holds. The
  header's Deploy never deploys by itself: a one-click redeploy from the header would be a surprise,
  and it would deploy without the edit in the form. An empty list doesn't repeat the top bar's New
  app. Everything else on a page is secondary, danger or ghost-weight.
- **Spacing rhythm:** 2 / 4 / 8 / 10 / 16 / 20 / 26 / 32 / 40 / 60px. Panel padding is 14-16px,
  gaps inside a grid 8-16px.
- **Narrow screens:**
  - Under 900px the sidebar goes. The top bar shows the wordmark and a "Menu" disclosure (a
    secondary small button) that opens a panel with the same brand, links and account row; the
    breadcrumbs hide and the gutters become 16px. Beside the 248px sidebar a 900px screen would
    leave only 600px for the page, so the switch is made before that.
  - A table stacks its rows by its own width, with a container query, not by the screen: the Apps
    table under 720px, the copies table under 660px. The column heads go; the name and the status
    stay on the first line, and each other fact keeps its column's name in small mono above its
    value (`data-label`).
  - Under 640px definition rows stack (label over value) and a deployment's time and trigger drop
    under its head. Under 480px the live indicator keeps its mark and hands its words to screen
    readers only.
  - On touch screens (`pointer: coarse`) every button and field is at least 44px tall, and so are
    the links that stand alone (an address, an app's name, the wordmark, the skip link, the sidebar
    links). A link inside a sentence keeps the line's height.
  - Tabs scroll sideways if they don't fit. The 006 pages' rules still hold: the login column stays
    380px at most, and a token row keeps its status pill and action in two fixed columns while its
    name and facts wrap.

### Named Rules
**The Problems-First Rule.** Every list sorts by status: failed, warning, rolling, healthy, idle.
What needs a human is always at the top.

## Elevation & Depth

Flat. Depth comes from four lightness steps (Unlit Panel → Panel Face → Raised Control → Lit
Control) and three border weights, never from shadow. Hovering a surface lifts it one step and
moves its border up to Strong Edge. A toggle that is on (Pause, Wrap lines) is the one pressed
state: Lit Control ground, a brighter edge and a 2px Readout White bar inside its bottom edge. That
bar is a mark, like the underline of an active tab, not depth.

### Shadow Vocabulary
- **Overlay** (`box-shadow: 0 12px 32px oklch(0 0 0 / 0.45), 0 0 0 1px oklch(0.340 0.010 255)`):
  the Menu panel under 900px and the Revoke confirmation that opens under a token row. Nothing
  else.

### Named Rules
**The Overlays-Only Shadow Rule.** A shadow means "this is floating above the page". Panels,
cards, buttons and inputs never get one. Translucency and blur aren't used, except for the faint
warning tint inside an inline prerequisite.

## Shapes

Small, square-ish corners that read as machined, not soft: 2px for pills, 4px
for buttons, inputs, confirmations and command blocks, 6px for panels and the Menu panel. Nothing is fully rounded except the
healthy, rolling and idle status marks and the live indicator's disc and ring. Borders are always 1px hairlines.

## Components

Precise and unadorned: controls feel like labeled switches on a panel, not soft app buttons. A disclosure under a button opens in the page, not over it.

### Buttons
- **Shape:** gently squared (4px). Heights 28 / 36 / 44px (sm / md / lg), text 13 / 14 / 15px,
  weight 500.
- **Primary:** a Readout White fill with Unlit Panel text. At most one per view, for the page's
  main action.
- **Secondary (default):** Raised Control fill, Control Edge border, Readout White text. Hover
  moves to Lit Control with a Strong Edge border.
- **Ghost:** no fill, Body Text. Hover adds a Lit Control fill and Readout White text. For "Why?"
  and inline toolbar actions.
- **Danger:** Fault Ember text on its tinted ground with the Fault Ember edge. The border goes
  full Fault Ember on hover. Destructive actions confirm in a `<details>` disclosure under their button (see
  Confirmation), never in a modal.
- **States:** transitions are 120ms on `cubic-bezier(0.2, 0.7, 0.2, 1)`. Pressing nudges the
  button down 0.5px, with no scale. Focus is a 2px Signal Blue ring at 55%, offset 2px.
- **Shipped as:** `cv-btn` with `cv-btn-primary`, `-secondary`, `-danger`, and the sizes `cv-btn-sm`
  (28px) and `cv-btn-lg` (42px, the sign-in button). A link that does a button's job (Deploy, Show
  older, Deploy again) is an `<a>` with the same classes. A `<summary>` that opens a confirmation
  wears them too. Icons are Lucide, inline `<svg>` from the `icon` template function (`icons.go`),
  and only in buttons and the sidebar: plus (New app, 14px), rotate-ccw (Restart) and rocket
  (Deploy) before a label, house, folder and settings in the sidebar, log-out in the one icon-only
  button (Sign out, 30×30, with an `aria-label`). The app pages add lock (a secret's name), external-link
  (open a domain, 30×30, with an `aria-label`), file-text (Edit as .env), history (Restore), play (Back up
  now) and chevron-right and chevron-down (a `<details>` row's marker). There are no ghost buttons in the build.
- **Toggle:** a secondary button with `aria-pressed`; on, it shows the pressed state described
  under Elevation & Depth. Its label changes with the state only for Pause / Resume.
- **Labels:** a verb that names the outcome ("Revert to v22", "Raise to 2 GB"). An optional
  Lucide icon at 1.5px stroke may sit before the label (14px in a small button).

### Status pill and mark
- **Marks in the build:** ● healthy, ◐ rolling (a ring with its left half filled: a deploy copy
  starting, a rollout's progress line), ◆ warning, ■ failed, ○ idle. Also used bare, without a pill,
  before a sentence (a progress line, a warning, an error under a field).
- **Mark:** an 8px shape drawn in CSS, not a font glyph, so it never depends on which font loaded
  (`cv-mark-healthy`, `-rolling`, `-warning`, `-failed`, `-idle`). Healthy is a filled circle,
  rolling a circle with its left half filled (the one hard-stop gradient in the system), warning a
  rotated square, failed a square, idle an empty ring. Marks are `aria-hidden`; the word beside
  them carries the meaning for a screen reader.
- **Pill:** 22px tall, 2px corners, the status ground and edge, the status color as text, in Data
  mono at 12px, with the mark first (`cv-pill-healthy`, `-warning`, `-failed`, `-idle`; rolling is
  there too, `cv-pill-rolling`). An "idle" pill uses Raised Control and Label Text. The quiet variant
  (`cv-pill-quiet`) drops the tinted ground, edge and padding inside the Apps, copies and Activity
  tables; the mark and the word stay. The words are the status's own: healthy, failed, reverted,
  not running. A reverted deployment wears the failed look.
- **An app whose newest deployment failed or was reverted** is failed even while copies of an older
  version run: its pill reads "deploy failed" in the failed look, in the app's header and in the
  Apps list, where the row sorts first. With no healthy copy it stays "not running". The summary
  then leads with the one shared sentence, "v3 failed its health checks. v2 keeps running." (or
  "… No copies are running."), which the needs-you list says too. The Running tab's mark carries
  the pill's words as visually hidden text.

### Panels
- **Corner Style:** 6px.
- **Background:** Panel Face with a 1px Hairline border, overflow clipped.
- **Shadow Strategy:** none (see Elevation & Depth).
- **Rows:** a 1px Hairline divider between rows. A row turns Raised Control on hover.

### Inputs / Fields
- **Style:** Raised Control fill, Control Edge border, 4px corners, Readout White text, 12px side
  padding, **32px** tall (`cv-field`, the handoff's Input md). Values are sans 400 at 13px; a name, a
  path, a host or a number adds `cv-field-mono` (mono 400 at 12.5px). The sign-in page uses a 42px
  field on Panel Face (`cv-field-lg`, mono 14px). The label sits above in Label style, 7px over the
  field (`cv-fld`). A prefix or suffix (`GET`, `MB`, `s`) sits inside the field in Label Text, mono
  13px with 12px of padding on its side: the box is the field (`cv-field-box`) and its input is bare
  (`cv-field-input`), so focus and error draw around the whole.
- **Focus:** the border moves to Strong Edge and the 2px Signal Blue ring appears around it.
- **Hint / Error:** a hint below in 13px/1.4 Label Text (`cv-hint`). An error below in Fault Ember
  with the failed mark first, written as a sentence about what's wrong ("The new password must be at
  least 12 characters."). The field's border turns Fault Ember Edge (`aria-invalid="true"`), the
  error is announced (`role="alert"`), and the field is autofocused so the page lands on it.
- **Date:** the same field, its value in mono and centered in the box, in the dark color scheme.
- **Select:** the same ground and edge with a native menu in the dark color scheme (`cv-select`),
  **32px** tall like the field, 2px corners, mono 13px (the field's size), 11rem at least, label above.
  It is the console's only select: the Apps filter's State and Kind, the Logs toolbar (Copy, Stream), the
  Variables page's Service, the Domains page's "Sends traffic to", Settings' Kind and Project. The
  buttons that sit in a row with selects (the filter's Apply and Clear, the Logs toolbar's) are 32px
  too, so the row is one height.
- **The .env editor:** a textarea on the darkest ground (Unlit Panel), Control Edge border, mono 13px
  on 24px lines, 300px tall at least (`cv-field-env`).
- **Checkbox:** `cv-check`, the accent on the box, the sentence in Readout White beside it.

### Notice
One line of status, in the voice of the screen: the mark first, then a plain sentence in Readout
White (`cv-notice`), balanced so no word is left alone on a line. A refusal is `role="alert"`; a
calm fact ("Your session ended. Sign in again.", "Password changed.") is `role="status"`. It never
has a solid fill and never a colored border.

### Command block
A command someone has to copy gets its own block (`cv-cmd`): Raised Control fill, Control Edge
border, 4px corners, Data mono 13px, selected whole with one click, wrapping only when it must. It
is never run into a sentence, where it would break across lines. A quiet variant (`cv-cmd-quiet`)
drops the fill and uses Label Text for secondary examples such as a `curl` line.

### Token row
The API tokens page's table (see Settings, feature 021): a grid row inside a Panel with a 9px mark, the name in
Data mono with "made …" under it, what the token can do, when it was last used and its expiry ("expires in
80 days", "never expires", "expired …", "revoked …"), then the action. Times are relative, with the exact
time in the `title`. Only an active row has "Revoke", which opens a confirmation under the row.

### Navigation
- **Sidebar:** only pages that exist are linked: Home and Projects under a "Workspace" Label, and
  Settings below. Each item is 34px tall with 4px corners, a 16px Lucide icon in Label Text and the
  name in Body. The current one sits on Raised Control with its text and icon in Readout White and a
  2px bar inside its left edge in Signal Blue (a bar, not a fill: the accent is kept for this and
  focus). Home is current on the Apps list, Projects on the Projects pages and on every page of an
  app, Settings on Settings. The brand block above links to Home (a 26px mark with the host's
  initial, the host in 14px sans, `caravana <version>` in mono); the account row below shows
  initials in a 30px disc, "Account", the email in mono (cut with an ellipsis) and a Sign out
  icon button, which stays a POST form.
- **Menu (under 900px):** a `<details>` whose summary is a small secondary button, opening a panel
  of the same three blocks on Panel Face with 6px corners and the overlay shadow, 360px at most.
- **Tabs:** five, by intent (Running, Deployments, Configure, Logs, Backups). They are links to their own pages, 46px tall, 26px apart, sans 500 at
  13.5px, over a Hairline rule. Inactive tabs use Label Text, turn Body Text on hover, and the
  current one (`aria-current="page"`) is Readout White with a 2px Readout White underline. A mono
  count may follow a label: Running carries its copies, Deployments the live version. Running
  also carries a 6px status mark before its label (rolling ◐ or warning ◆) while the app isn't
  healthy, and none when it is.
- **Breadcrumbs:** in the top bar, every segment a 30px mono 500 link (Body Text, Raised Control
  ground on hover), separated by a "/" in 16px mono, the last one current in Readout White and cut
  with an ellipsis if long. An app's pages read `project / environment`; its tabs add no segment.

### Live indicator
The top bar's readout of the page's event stream (`cv-live`). It is hidden until the script knows
the stream's state, and each state has its own shape and words, so none depends on color:
- **Live:** the 6px healthy disc and the word "live". The disc pings once when the stream opens.
- **Reconnecting:** the tilted warning square and "Reconnecting…". The browser retries by itself.
- **Signed out:** the idle ring and "Signed out. Sign in again.", the second sentence a link back to
  the page the admin was on.
It sits in Data mono at 12px in Label Text; under 480px only the mark shows, with the words kept for
screen readers. It is a polite `role="status"`.

### Empty state
A list with nothing in it (`cv-empty`) says what is missing in a 15px sentence, one line in Label
Text on what to do, and the one primary button that does it. It never shows a column of "none".

### Table and rows
The Apps table, the copies table and the history are grid rows inside a panel with 1px dividers and
a Raised Control head of 36px in Label style (the history has no head). Names, versions, times and
counts are Data mono. A cell of several lines (images, addresses) puts each on its own line, cut with
an ellipsis and with the full value in the `title`. A copy that restarted shows its count in Caution
Amber; the number is there either way. Stacking is described under Layout.

### Deployment entry (history)
One entry per deployment, newest first, in a panel row. The head line: the version in mono, the
status pill, then at the right the trigger, time and duration in muted mono. Under it, in order:
- one sentence in Readout White, 14px, with names and versions in mono ("v8 failed its health
  checks. Nomad rolled back to v7, which is serving.");
- the rollout's progress with the rolling mark, while it runs;
- the raw Nomad error in muted mono, set apart;
- at most one action button (a secondary small button);
- deploy warnings, each with the warning mark, the field in mono, then what changed in meaning;
- the images as deployed, pinned digests in muted mono, the full value in the `title`.
A "Show older" secondary button ends the list when there is more.

### Failed-deploy treatment
A deploy that didn't hold is said once, in a sentence, and fixed with one action, never in an alert
box. Wherever it appears it is the same:
- the Apps headline names the app first: "1 app needs you: whoami has a failed deploy." An app with
  healthy copies and a failed newest deploy still counts. So does a reverted one, as the needs-you list
  holds it (what runs is what was running, but it is a deploy that didn't hold); its pill reads
  "reverted" in the failed look;
- the app's list row and its page header carry the failed pill, and the row sorts above healthy
  ones (The Problems-First Rule);
- on Running, an app with no copies says "No copies are running." and, under it, "v8 failed: see
  Deployments." with the link;
- on Deployments the entry carries the sentence, the raw error and the one action on the newest
  entry only: "Deploy again", or "Change the image" when the image wasn't found. The action links to
  the deploy form above it.

### Log view
The Logs tab: a toolbar, then a panel of mono lines.
- **Toolbar:** two labeled selects (Copy, Stream) and an Apply secondary button, a plain GET form
  that works without script. At the right, two toggles that exist only with script: Pause and Wrap
  lines.
- **A line:** a 3-column grid of Data-like mono at 12.5px: the copy's short id (8 characters, Label
  Text, full id in the `title`), the stream tag, and the text in Readout White. A stderr line carries
  the word "stderr" in its tag column; a stdout line leaves that column empty. The tag is a word in
  the line's own column, not a color. A line is always set as text, never markup.
- **Text:** keeps its spaces and doesn't wrap, so the list scrolls sideways; Wrap lines lets it
  wrap. The list is 70vh tall at most and scrolls; it follows new lines only while the reader is
  within 24px of the bottom.
- **Pause:** the button becomes "Resume" and a note at the top of the panel counts what arrived:
  "Paused. 12 new lines." The lines are held and drawn on Resume.
- **Cap:** the list keeps the newest 5,000 lines; when older ones go, a note says "Older lines were
  dropped."
- **Empty:** "No output yet. New lines appear here."

### Confirmation
A consequence is confirmed in the page, under its button, with a `<details>` disclosure (`cv-confirm`),
so it works without script. The summary is the button; the body opens below it on Raised Control:
- the body is 520px at most, 4px corners, a Control Edge border, 14px 16px padding, a plain sentence in
  Readout White about what happens, then one button;
- **Replace token** and **Revoke token** say that the current token stops working at once. Revoke's
  confirming button is danger; so is Revoke in a token row, where the body opens as an overlay under
  the row's right edge so the three columns keep their widths;
- **Delete app** sits last and apart, in a panel with a Fault Ember Edge border, as a danger small
  button. Its body says what goes with the app, asks for the name ("Type blog to confirm"), and its
  "Delete blog" button is disabled until the field holds exactly that name, case and all. The server
  checks the name again. After a refusal the disclosure opens by itself with the error under the
  field.

### Configure, Variables, Domains and Backups (feature 021)
The handoff's app pages, with what the console can do today. Every change on them is a deploy, so each
submit says so ("Save and deploy", "Set and deploy", "Remove and deploy", "Add domain and deploy"); there is
no pending state and no Discard. A stopped app's forms say "The app is stopped: start it first." where the
submit would be, and an app edited through its Compose file shows its tables read-only with "This app is
edited through its Compose file." (secrets can still be set and removed). A secret's value is never
drawn, not its length either: ten dots and a lock icon.
- **Configure:** per service a grid of two panels (`cv-grid2`, gap 16, one column under 860px of room), each
  `cv-panel` with 18px padding and a Label head: Copies & size (Copies, CPU in MHz, Memory in MB, Ceiling in
  MB, in four columns) and Deploy (Image, Container port, then the health check as a path with a `GET`
  prefix, Every in s and Timeout in s, and one sentence that new copies must pass it or the deploy is
  reverted). "Also set" is a read-only panel under Deploy for what the form leaves alone (entrypoint,
  command, arguments, volumes, backup). With several services the grid is drawn once per service under
  its name in mono. Domains and Secrets follow, once per app, each a panel with its link ("Manage
  domains", "Manage variables and secrets"); a service whose image can't be pulled for want of credentials
  gets the dashed "Needs a container registry" notice. Neither of the handoff's switches (auto-revert,
  canary) is drawn: they are not choices here.
- **Page head** (Variables, Domains, Backups): the title in sans 500 at 18px (the app header owns the h1),
  one sentence in Body, the page's actions at the right, secondary small buttons and one primary
  (`cv-pghead`).
- **Tables** (Variables, Domains): a `cv-panel` of grid rows, a 38px Raised Control head in Label style,
  rows 52px at least with 1px dividers, Raised Control on hover. A Variables confirmation opens as an overlay
  under its row's button, so the panel doesn't clip; a Domains row's (feature 023) opens inline, in the open
  row's detail (see Domains). A narrow table stacks its cells by its own width.
- **Backups:** a banner when there is no destination (Panel shape, warning mark, one button), a row for
  each service that could be backed up and isn't (warning mark, a sentence, "Back up daily"), then one
  `<details>` per backed-up service: the summary row (chevron, status mark, what, how often and kept, last
  backup, size), and under it the restore-points strip (30 days, a bar for a healthy run linking to its
  row, a gap for none), the host path and size of a volume, the last ten runs with "Restore…", and the
  buttons. Restores come after. "Back up daily" is a confirmation that says what it will do; a failed run's row
  leads with a plain cause (from a fixed table) before the mono detail; the strip's bar is restorable when
  the day has any healthy run, with a failed cap when its newest run failed. A summary row is read to a
  screen reader as "<service>, last backup failed" before its visible text.
- **Variables:** "Keep as a secret" is ticked when the form is drawn, because most values are secrets; the
  form sends a hidden `secretChoice` so an unticked box (which sends nothing) differs from a post that
  isn't this form, which is read as a secret.

### Settings (feature 021)
One page per entry, as the handoff draws Settings, with what the console has today. A grid of a 232px nav
and the content, in the shell's frame; the shell's own page padding is off for these pages (`cv-page-settings`).
- **Nav:** `<nav aria-label="Settings">`, sticky under the top bar (`top: 52px`, the rest of the window
  tall, scrolling by itself), a Hairline at its right. The word "Settings" at 18px, then the groups: Account
  alone, **Connections** (Registries, Backup storage, Certificates, Notifications) and **Security** (API tokens). A group
  with no entries isn't drawn, and no entry is drawn for a feature that doesn't exist yet. An entry is a link
  32px tall with a 15px icon, its label, and at the right a count or a mark. The current one has the Raised
  Control ground and no left bar (the bar is the sidebar's). Under 900px the shell's own narrow rule applies
  (no sidebar, the Menu bar) and the nav is a row of links above the content, scrolling sideways, with the
  current entry first (CSS `order` on the same links, the groups dissolved with `display: contents`) and a
  fade at the right edge (`mask-image`); it is
  positioned so its screen-reader-only spans stay inside the scroller, and `.cv-page-settings` drops the
  shell's narrow padding after the layer, which would otherwise win. The sidebar's Settings stays current on every Settings page, and the crumbs
  read "Settings / <entry>".
- **The count-or-mark rule.** Each entry has one function (`registriesState`, `backupStorageState`,
  `certificatesState`, `notificationsState`, `tokensState`) that gives its count, its nav mark, and the mark of each of its rows; the
  nav and the page's rows both read it. The nav marks only what is breaking, and a mark wins over the count;
  the rows mark more: a state the owner chose (a paused channel) or one that is chronic (a full token that
  never expires) marks its row and has its note there, and leaves the nav showing the count. Registries: ■
  when a registry's last test was refused (○ never tested, or changed since: a save that changes the
  credentials forgets the last test; ● passed), else how many. Backup storage has no count: ■ in the nav and
  on the row when the last backup failed, ◆ when no destination is set while a service has a backup policy.
  Certificates (feature 023): ◆ when any certificate is expiring, else how many are issued (Traefik's own and
  pending ones aren't counted); a row is ● issued, ◐ pending, ◆ expiring, ○ Traefik's own or a failed
  handshake. Notifications: ◆ when a channel's last delivery failed, else how many (paused ones included; a paused
  channel's row is ◆). API tokens: ◆ when an active token went unused for more than 90 days (a token never used
  counts from when it was made, so a fresh one is never flagged), else how many are active; a full token with
  no end date is ◆ on its row only; an expired or revoked token is ○. The nav's spoken reason puts a failure
  first and names both when both hold.
- **Content:** 1180px at most, padding 36px 32px 64px, 22px between blocks. The head is the headline
  (30px, 400) over a 15px sentence, and the page's one primary action at the right (small, with a plus).
- **Setting rows** (Account, the add and edit forms): a panel of rows, a 220px label with its 13px hint at
  the left and the controls at the right, 24px between, 18px 20px of padding, a Hairline between rows. Fields
  are the 32px field at 360px. The form's primary button ends its last row.
- **Connection rows** (Registries, Backup storage, Notifications): a 9px mark, the name in mono over its kind,
  the state in two lines of mono, "used by N", and the actions as small buttons (a channel's kind line also
  says in words what it sends when that isn't everything: "Webhook · problems only") (Update password is the one
  primary, only on a refused registry). A row that needs the owner has its note under it, indented past the
  mark. Edit, Remove, New signing secret and Revoke are `<details>` confirmations that open over the rows
  below, so these panels don't clip; a row's confirmations close each other. The backup destination's row is
  itself the summary of the details that opens its form. A narrow panel stacks a row's cells.
- **Add panels** (Registries, Notifications, API tokens) open with a Label row naming what they add, so `#add`
  and `#new` land on a named thing.
- **Token table:** a 38px head (Expiry column 170px), then rows of mark, name over "made …", what it can do, last used, expiry, and
  Revoke; sorted active (problems first), expired, revoked, the last five expired or revoked and then a "Show
  older" link (`?older=1`). "never expires" is in the warning hue for a full
  token.
- **Certificates** (feature 023, `/settings/certificates`; values in `specs/023-domains/contracts/domains-screen.md`):
  the head "Certificates" over "HTTPS certificates for every domain come from Let's Encrypt and renew 30 days
  before they expire." (without Let's Encrypt: "This caravana was installed without Let's Encrypt: Traefik serves
  its own certificate."; on the staging issuer: "Certificates come from Let's Encrypt's staging issuer: browsers don't
  trust them. Run the installer with the real issuer when the domains are ready."), no action. The Domains page's banners stand above for each expiring certificate
  (same markup, Check again). Then a Label "Domain certificates · 12 certificates" (it counts issued and expiring certificates only, as the nav
  does; Traefik's own and pending ones are rows but not certificates: with none the Label says "· 8 domains · none issued yet",
  or "· 8 domains · no certificates: installed without Let's Encrypt" when the issuer is none) over a panel: a 34px ink-2
  head and rows of 46px, columns `14px minmax(0, 1.4fr) minmax(0, 1fr) 150px minmax(0, 1.1fr)`, gap 18: the 9px
  mark, the domain (13px mono, a link to its row on `/domains`), the app (mono fg-2), the issuer ("Let's
  Encrypt", "Traefik's own", fg-3), the expiry (12px mono: "renews in 61 d" fg-3, "expires in 6 d" in the warn hue,
  "waiting for Let's Encrypt", "—"); problems first. Under 640px a row folds to label and value. Then the Label "How
  certificates are issued" with **Edit** at its right, a `<details>` whose form opens inline under the button
  and pushes the panel down (the heading and the `<details>` share the head's one grid cell, so the summary
  stays on the heading's line; no overlay), holding Warn when alone (a select of 7, 14 and 30 days, Save), over a panel of three rows (`220px 260px
  minmax(0, 1fr)`, gap 24, padding 14 20): **Issuer** ("Let's Encrypt", "none") and **Contact email**, both
  read-only because they are Traefik's static configuration ("Set when caravana was installed; run the
  installer again to change it."), and **Warn when** ("14 d before expiry", "Sends to the channels set up in
  Notifications."). Without Let's Encrypt (`acme=none`) Warn when is drawn disabled: the Edit form's select
  (fg-3) and Save (faded) are disabled, so it posts nothing, with "Applies once certificates are issued." under
  the select and before the row's note; the API still takes the setting. A saved window answers 303 with a notice; the checks already stored are judged again at once.
  Nothing here pushes the page wider: every track, fixed ones included, is `minmax(0, …)` (so the 220/260px columns and
  the table's 150px one shrink first), the hint column wraps (`overflow-wrap: anywhere`), and the section head's children
  have `min-width: 0` so Edit always stays inside.
- **Shown once** (a new token, a new signing secret): a panel drawn first, in the response of the post that
  made it: the sentence, the value in a box that selects whole, and for a token its `curl` example. It is in
  no address and no later page; the Copy button stays hidden without script.

### The Apps home and the deployment's stages (feature 021, page 4)
Values from `contracts/page4-home-deploys.md`. The home's main column reads: headline, needs-you,
Deploying now, the filter, the groups.
- **Deploying now.** A Label, then one panel per app with a rollout in progress, newest first, padding 20,
  gap 16, the border in Rolling Line. Head row (gap 14, wraps): the app's name in mono 500 18px, its
  `project / environment` in muted mono 13px (left out in Default / production, like the rail's), the
  rolling pill ("v24 rolling out"), a spacer, "View deployment" (secondary small). No Roll back: Nomad
  reverts a failed rollout itself. Facts grid (`auto 1fr`, gap 10/20, aligned to the start, every row's
  first line 26px so a label sits level with the first chip): **Change** (what the deploy carries:
  "Pushed to `main` by ana · `4e0a77a`", "Promoted from `staging`", "`v3` deployed again", "Deployed from
  the console", "Deployed through the API by `ci-github`", "Deployed by the webhook") and **Copies**: the
  chips, and under them, always on a line of its own, "1 of 2 new copies healthy". A chip is 26px tall,
  ink-2 ground, 1px line-2 edge, radius 2, mono 12px: a 6px mark, the app's name (the service's, with
  several), the version the copy runs (`v6`, a step brighter than the ID) and the copy's short ID
  (`slow v6 89038f01`). The new version's copies come first, then the ones still serving the old
  version, then the ones Nomad is stopping. A starting copy has a dashed Rolling Line edge and the half
  mark; a stopping one ("draining": only a copy told to stop, never an old copy that still serves) a
  dashed edge, the idle ring and struck-through quiet text. Each chip says its role to a screen reader:
  "new, healthy", "new, starting", "serving, v5", "draining, v5". No chips: "Waiting for Nomad to place
  the first copy." The region is its own live region on the deployments and copies notices and is empty
  (hidden when nothing rolls out).
- **The filter.** A GET form between the needs-you list and the groups: a search (32px mono, 280 wide),
  a state select (All states, Needs you, Rolling out, Healthy, Stopped, Not deployed), a kind select (All
  kinds, Image, Compose, Database, Volume), Apply, and Clear when any is set; the whole row is 32px.
  "Needs you" is the apps with an item in the needs-you list above (a failed or reverted deploy, a failed
  backup or restore, a crash loop, unhealthy copies, ...); a stopped app is only Stopped. Drawn only with more than
  5 apps, and ignored under that. It matches name, project, environment, images and domains in any case.
  The groups hold only what matches, an empty group isn't drawn, and nothing matching is "No app matches."
  with Clear. The headline and the needs-you list are about the whole caravana and never filtered. The form
  sits outside the reload's way (`hx-preserve`), so a refresh never takes away what is being typed, and
  the region's refresh address carries the query.
- **The kind word.** After an app's name in the table, mono 12px muted, gap 10, baseline-aligned. One rule
  (`kindOf`), shared with the kind filter, the first that applies: **compose** (source Compose or a kept
  file), **database** (a Postgres backup, or an image named postgres, mysql, mariadb, redis or valkey: the
  last path segment before the tag or digest), **volume** (any volume), else nothing: an image app has no
  word.
- **Who deployed**: one set of words (`deployWho`), lowercase, for the second line of an Activity row
  ("`<who>` · when"), the history entry's meta line and the app's lede. An origin says "pushed to `main`
  by ana · `4e0a77a`"; a promotion "promoted from `staging`"; the console "from the console"; the API
  "through the API" and, when the deployment recorded the token, "through the API by `ci-github`"; the
  webhook "by the webhook". The entry's meta reads "pushed to `main` by ana · `4e0a77a` · run · 3 minutes
  ago · took 42 s", the run a link (https only). The lede reads "Last deployed 26 minutes ago, promoted
  from `staging`." (no commit). Only the card capitalizes them or puts "Deployed" in front.
- **The stages strip.** Under the head line of a history entry that is going on (pending or running), is
  the app's newest, or failed or was reverted (its failed cell explains it): a panel of three cells
  (Queued, Roll out, Live), 18/20 padding, 1px line-1 between them. A cell is an 8px mark and the stage's name (500 15px;
  Readout White when done or active, quiet when still to come), and under it a mono 13px value, "—" when
  nothing. Marks: done disc, active half mark, to come ring, failed square. The active cell has a 2px
  Rolling underline. Queued: the wait ("2 s"), "waiting", or a failed square and "didn't start" when the
  deploy failed before Nomad had it ("superseded" when a newer one won). Roll out: "1 of 2 new copies
  healthy" while it runs (the card says the same; the running entry's progress line keeps both numbers:
  "2 of 2 new copies started, 1 healthy."), its duration, or "failed". Live: the time it ended ("14:03:05", the date in the `title`),
  and a ring and "—" for a deployment that failed or was reverted. Under 660px of room the cells stack, one
  line each. Each mark also says its meaning in words for a screen reader. An older healthy or superseded
  entry gets the same three stages as one line of quiet mono ("● Queued under 1 s · ● Roll out 28 s ·
  ● Live 06:28:21 UTC", wrapping) in the panel's place, and drops "took ..." from its meta, which the line
  carries. A reverted entry's raw Nomad line is prefixed "Nomad said: " (the words after it stay mono).
- **The Version cell** of an app that is rolling out reads `v5 → v6`: the arrow and the new version in
  quiet text (the arrow is for the eye; a screen reader hears "rolling out v6").
- **A rollback says what was deployed again.** After a revert Nomad's live version is a copy of an older
  one: "`v3` (`v1` again) keeps running." on the app, "rolled back to `v3` (`v1` again)" in the entry,
  and the same in the needs-you list. The older deployment is the newest healthy one before the failed
  one with a recorded definition and pinned images (the one Nomad's message names, when the history has
  it). When none matches, the wording stays "v3".

### Schedules (feature 022, page 5)
Values from `specs/022-schedules/contracts/schedules-screen.md`. A workspace page, `/schedules`: the
sidebar's third Workspace item (`clock` icon), current on every `/schedules…` page.
- **The time rule.** Every clock time the console draws (a schedule's next run, a run's start, the form's
  preview, a backup's time, the deployment entry's Live stage) is a `<time class="cv-clock"
  datetime="…Z">` whose text is UTC first and named: "14:15 UTC", with the console's relative-day words
  computed in UTC on the server ("tomorrow 02:00 UTC", "yesterday 14:00 UTC", "in 2 d · Sun 03:00 UTC",
  "3 d ago · Tue 09:30 UTC", the date beyond a week). One helper (`clockOf` in `view.go`, the `clock`
  template) makes them. `static/js/localtime.js`, ours and listed in `tools/ui-vendor.versions` with its
  sum, adds the viewer's local time after each in fg-3, " · 16:15 local", in the shape the element's
  `data-local` names (time, time with seconds, weekday, date; a time whose local day isn't its UTC day
  gains the weekday). It never names the zone: the word is "local". A list of times (the preview's next
  three runs) says "UTC" once after the list and gets one " · 16:15 · 16:30 · 16:45 local". Without
  script the UTC text stands alone and is right. Clock times are mono with tabular figures, and the UTC text never breaks before "UTC" (`white-space: nowrap` on the `<time>`). In the table's Next and Last run cells (`data-local-line`) the local time is a line of its own under the UTC one, fg-3, with no separator ("Mon 03:00 local" under "in 2 d · Mon 09:00 UTC"); the count line keeps its one-line form. A title's clock (`data-local="none"`) gets no local time.
- **Page head** (Settings' shape): "Schedules" 30px, the sentence "Commands that run on a timer in a
  fresh copy of an app, then stop. Times are UTC." (the script adds "Your local time is shown beside
  them."), New schedule (primary small, plus). Then the filter (a project select only with more than
  one project, the app select, Apply, Clear) and the count line in 13px mono fg-3: "7 schedules · 1
  failing · next run 14:15 UTC" ("none failing", "no runs scheduled").
- **The table:** a 38px ink-2 head, columns `14px 14px minmax(0, 1.3fr) minmax(0, 1.2fr) 170px 170px
  minmax(120px, 200px)`, gap 18, every cell `min-width: 0` so the grid shrinks (the command line and cron line cut or wrap, never push the page wider). Each row is a native `<details>` (`id="schedule-<id>"`; `console.js` keeps a
  refreshed region's rows as open or closed as they were): chevron, a 9px mark (half disc while a run
  goes, ring when paused or never run, else the newest run's: disc, square, diamond for a skipped slot),
  the name (500 14px mono) with a small pill ("running now", "paused", "last run failed") over "app ·
  service · command" (12px mono fg-3, one line), the words over the cron line (fg-4), Next, Last run
  ("started 14:00:02 UTC" over a 120×4 `<meter>` of the usual time, or the time over "took 48 s" /
  "stopped at 5 min"), and the last 14 runs as 7px marks 4px apart, oldest first, each a link to its
  run (`role="group"`, "Last runs, oldest first"). Sorted failed, running, next run, paused. The first
  row opens by itself when its last run failed; `?open=` and `?created=` open another.
- **The detail** (ink-0, `minmax(0, 1.2fr) minmax(0, 1fr)`, padding 16 18 20 82): the state sentence
  (a failure's cause from a fixed table with "3 of the last 4 runs failed the same way.", "Running for 1
  min 20 s. Usually takes 48 s.", "Paused 3 days ago.", "Last run finished in 48 s." (no "The", no colon), "Couldn't start: no `sh` in the image." (the run's stored reason, a backticked program in mono) and the skip note quieter), "How long each run
  took" as bars 56px tall at most (fg-3 ok, ember for every failed kind, a stop at the limit too (its title and
  the caption tell the limit: "yesterday 14:00 · stopped at 5 min"), a 2px line-2 stub for a skip, the only
  other look; heights
  in fourteen 4px classes, since the policy allows no style attribute) over "usually 48 s · stops at 5
  min" ("no completed runs · stops at 5 min" when runs exist but none finished ok, "no runs yet" only
  when there are none); then the facts grid (130px keys at every width; the project fact is the project's
  display name as the crumbs say it, "Shop / production", and is left out in Default's production) and the
  buttons: Stop this run (a confirmation) while a run goes, else Run now (primary only in the row the page
  opened by itself, the first failed one; secondary in a row the owner opens with `?open=`, `?created=` or
  `?saved=`, and in every other), Logs of last run, Pause or Resume, Edit, Delete… (a confirmation). A
  confirmation opens over what is below. A built-in (a service's backup, "web backup", "pg_dump →
  backups") has "Made by Backups…" and one button, Change in Backups (no Logs: the Backups tab shows
  its runs), and its marks link to that tab. The row's command line is one line with an ellipsis at every
  width; the detail holds the whole command.
- **A run's page:** the crumbs "Schedules / rates-sync" (both links), the title "`rates-sync` ·
  yesterday 14:00 UTC" (UTC only; the meta line has the local time), the run's sentence, "started … · took … · `web`
  of `booking-api` · v24", "Still running: reload for more." while it goes, and the output as the Logs
  tab draws it without the copy column: stdout, then stderr marked, the cut line when a stream lost its
  start, "No output." It is never cached, and no other page holds a run's output.
- **The form** (New schedule, Edit): Settings' setting rows in one panel, 1180px at most: Name, Run
  inside (the app select, stopped apps disabled; the service select of a picked app with several, or
  every app's services as "app › service" until one is picked), Command (2-row mono textarea), When
  (a sans field and a Check submit, the four presets as 26px chips that are links, the preview box:
  cron, next, timezone, or "Couldn't read that…"), Limits (Stop after, Memory, the skip box), Create
  or Save and Cancel. A refused submit draws the form again with the reason under its field. The text
  controls (Name, Command, When, the preview) are 560px and the selects 360, so their right edge is one
  line; Limits is two columns of 274px (560 with the 12px gap). When the schedule exists (Edit), Run inside
  is text, "`web` of `clock`" in mono (it can't move; hidden inputs carry the values), not selects. The
  When hint says what the typed time of day is locally ("09:00 UTC is 11:00 local today"), worked out on the
  first of the preview's next runs when the page has them, else on the next day that time comes, so a
  clock change never gives two local times for one schedule.
- **Configure** gains a Schedules panel after Variables & secrets: "`name` · words" lines, each after a
  7px mark (the row's mark: disc ok, square failed, half disc running, ring paused or never run; failing
  lines first, then the list's order), "No schedules." or the note "6 schedules · 2 failing" ("1
  schedule", "none failing") as a link to `/schedules?app=<id>`, then Manage schedules and New schedule.
- **Narrow:** under 900px of room a row folds (mark, name and pill; app and command; Runs, Next and
  Last run as label and value; the strip on its own line) and the detail stacks; the form's rows stack
  under 640px (page 3's rule).

### Domains (feature 023, page 6)
Values from `specs/023-domains/contracts/domains-screen.md`. A workspace page, `/domains`: the sidebar's
fourth Workspace item (`globe` icon, after Schedules), current on every `/domains…` page. The app's Domains
page (`/apps/{id}/domains`) draws the same rows, detail and Add panel from the same template
(`templates/domains.html`), filtered to the app, its head sentence ending with the link "All domains".
- **The states.** Each row's state comes from Caravana's own last check (DNS against this host's
  addresses, and the certificate Traefik's edge presents), cached and refreshed every minute while a
  domains page is open. Marks and words, in the order the list sorts them (problems first, then live, then
  idle, by host within): ■ "DNS points elsewhere"; ◆ "certificate expiring"; ◆ "no DNS record"; ◐ "getting
  certificate"; ● "live · HTTPS"; ○ "Traefik's own certificate" (the lab, or a host installed without Let's
  Encrypt); ○ "redirects to `<host>`" (the other half of a www pair); ○ "app stopped" / "not deployed" (the
  app isn't served, whatever the last check said); ○ "couldn't check", or "not checked yet" for a row never
  checked. The words are `edge.State.Words()`; the console words the redirect target, the not-served reason
  and the never-checked row. **A redirect row sorts directly under the host it redirects to** (a www pair
  reads together; `redirectsUnderTargets`), whatever its own state; one whose target isn't listed keeps its
  place.
- **One state table** (`internal/ui/domainstates.go`, `domainStates`): state → mark, pill words, the Certificate
  column's words when there is no date ("waiting for Let's Encrypt", "Traefik's own", "—"), the Certificates
  table's expiry words, and the fixed detail sentence. The list, the Add panel's check lines and Settings ›
  Certificates all read it: a DNS or certificate result is first named as the state it gives (`dnsState`,
  `certState`), so "no DNS record" is ◆ with the same words in the list, the check line and the count, a pending
  certificate says "waiting for Let's Encrypt" everywhere, and a certificate that couldn't be read is ○ "couldn't
  check", never ■. A test holds the three places to the table, and the table to `edge.State`.
- **Needs-you items** lead with the app, as every item does (its place first, then a colon): "blog: `example.com`
  points at 104.20.23.154, which isn't this host." and "blog: the certificate for `example.com` expires in 6 days and
  hasn't been renewed."; the Domains page's own banners, which are on the domain's page, start with the domain.
- **Page head** (Settings' shape): "Domains" 30px, "Every address people use to reach your apps.
  Certificates are issued and renewed for you." and Add domain (primary small, plus, a link to `#add`). Then
  the filter (Schedules': a project select only with more than one project, the app select, Apply, Clear)
  and the count line, 13px mono fg-3: "12 domains · 2 need you · 1 getting a certificate", "· all healthy"
  only when every row is live or redirects.
- **Banners:** above the table, one per DNS-elsewhere or expiring row, at most three, then "and N more" (a
  link to the first row without a banner). A panel, padding 16 18, grid `14px minmax(0, 1fr) auto`, gap 14:
  a 9px mark, the sentence (15px fg-1: "`a.example.com` points at `185.199.108.153`, which isn't this
  host." / "`b.example.com` expires in 6 days and hasn't been renewed.") over its fixed explanation (14px
  fg-2), and Check again (secondary small, a post that checks the host now and comes back with its row
  open). There is no retry into ACME: Traefik retries on its own.
- **The table:** a 38px ink-2 head "Domain · Status · Goes to · Certificate", columns `14px minmax(0, 1.6fr)
  190px minmax(0, 1.2fr) 150px 36px`, gap 18, padding 0 18, rows 52px at least with 1px line-1 dividers, every
  cell `min-width: 0` (the fixed 190px and 150px tracks are `minmax(0, …)` too). An open row's facts grid is `minmax(0, 90px) minmax(0, 1fr)`
  (110px for a live domain); its keys and values, the state sentence and the notes wrap with `overflow-wrap: anywhere`, so a
  four-address "now" value or a long host never widens the page at any width from 390 up. Each row is a native `<details id="domain-<host>">` (console.js keeps it open or
  closed across a refresh); the open row has the ink-2 ground. Cells: the chevron; the host (500 14px mono,
  one line, ellipsis) with a 12px lock (fg-3, `title` "Team only") for a Team-only domain and its path
  (`/api`, 12px mono fg-3); the quiet small pill (no ground, edge or padding); Goes to (13px mono fg-2:
  "`booking-api` `:8080 · production`", the port and environment fg-3, "`hollo` › `web`" when the app has
  several services, "`Shop` › `web` :80 · staging" when the app is outside Default › production (the project's
  display name, as the rail names it, since two projects may each have a `web`), "→ `example.com`" for a
  redirect row); the certificate (12px mono fg-3: "renews in 61 d",
  which is the expiry less the 30 days ACME renews before, "expires in 6 d" in the warn hue when expiring,
  "Traefik's own", "—"; the expiry date in the `title`); the `external-link` icon link ("Open
  https://<host>"). The first row whose state needs the owner (■ or ◆) opens by itself; `?open=<host>`
  opens another. Empty: "No domains yet. Give an app a domain and HTTPS is set up for it." with Add
  domain. Footer 13px fg-3: "Checks run every minute while this page is open, and every 15 minutes otherwise.
  Certificates come from Let's Encrypt and renew by themselves." (the watcher checks every 60 s only while a
  domains page's stream is open)
- **The detail** (ink-0, grid `minmax(0, 1fr) minmax(0, 1fr)`, gap 24, padding 18 18 20 46, a line-1 bottom):
  the left column by state: DNS elsewhere has its sentence ("The domain points at …, which isn't this host.
  Until it changes, caravana can't get a certificate and visitors won't reach `<app>`.") and a facts grid
  with 90px keys (now, in the failed hue; should be; last check "40 s ago · checks every minute while this page is open");
  expiring has its sentence and Check again; Traefik's own a quiet sentence, and getting a certificate one with
  **Open it once** (a link to `https://<host>/`, new tab, `rel="noopener"`: Traefik asks Let's Encrypt for the
  certificate on the first request it gets); no
  DNS record "No DNS record yet. Add this record where you manage DNS:" and the record box; live and
  redirect a facts grid with 110px keys (certificate "Let's Encrypt · renews in 61 d · expires
  `<clock time>`", DNS "A → 203.0.113.1", router "`<app>-<service>`" or "`<app>-<service>-<hash8>` (its own,
  for its options)", checked); not served "The app is stopped: start it to serve this domain." / "Nothing
  is deployed yet." with a link. **A problem is said once:** when a row's banner is on the page (at most three),
  its detail drops the sentence the banner already says and the second Check again. A DNS-elsewhere row leads with
  "Change the record to:" and the record box (A · Name per `DNSRecordName` · this host's address, the box a
  no-record row draws), then "now" (the failed hue) and "last check"; an expiring one keeps its facts (certificate,
  DNS, checked). A row beyond the three banners keeps its sentence and its Check again. A DNS-elsewhere row has no
  external-link icon: it would open whatever DNS points at today. The expiry is a clock time (the time rule); "checked" is relative ("40 s
  ago" under a minute). The right column is "Options" (a Label), a form of its own (below), then Remove… and
  New password; or why there is none (a stopped app, a redirect row). A row, banner or detail never holds a
  password or a hash.
- **The Options form** (one `<form method="post" action="/domains/<host>/options">` per row, on `/domains` and
  on the app's page, carrying the app, the service, the page's revision and, on the app's page, `page=app`; it
  works without script and saves with a button, never on toggle): Force HTTPS first, a locked-on switch with
  "On for every domain." under it in 13px fg-3 (not a field); **Team only (a shared password)**; the redirect
  switch, worded by what it redirects: "Redirect `example.com` here" on a `www.` host, "Redirect
  `www.example.com` here" on an apex, absent on any other host; **Path**, the 32px mono field with "Only
  requests under this path reach the app; the path is kept. Saving deploys the app." under it; **Save and deploy** (primary small).
  Each switch is a checkbox with `role="switch"` and `.cv-switch`, its label wrapping it, so the whole line
  is the target. A refused path draws the row's form again with what was typed and the reason under the field
  (the row opens). Below the form, **Remove…** (a confirmation, "Remove and deploy") and, for a Team-only
  domain, **New password** (a confirmation: "The old password stops working at once. The new one is shown
  once."); each is a form of its own, since forms don't nest. **Inline confirmations:** both open in the
  flow, never as an overlay: an open one takes its whole line (`flex-basis: 100%`), its form a block under
  its button (ink-1, 26rem at most) that pushes the row down, and the other button wraps under it, so
  nothing clips at any width. Turning Team only on, or New password, answers
  200 with the shown-once panel above the page ("Copy the password for `<host>` now. User name `team`.", the
  value in a box that selects whole, Copy hidden without script): the password is in no address, no later page
  and no log. A Team-only domain says "Visitors sign in as `team`." in 13px fg-3 above New password. The panel and
  every sentence write "caravana" in lowercase. A stopped app's rows show "The app is stopped: start it first." in place of the form.
- **Domains go to** (Configure's Deploy panel): a service with several ports and domains has a select of its ports
  ("http · :80") after Image; saving sets the port of every one of its domains and nothing else of them. A
  service with one port has the Container port input, as before.
- **The switch** (the Options form's): a checkbox drawn as one, `.cv-switch`, 30×18, radius 9. Off: ink-2
  ground, line-2 edge, a 12px fg-3 knob; on: the healthy ground, edge and knob, moved 12px (the handoff's
  hue, for a real switch). **The locked switch** (Force HTTPS: on, disabled, "On for every domain.") is drawn
  in fg-3, knob and ground (the ground fg-3 at 22%, the edge at 45%), so it reads as fixed, not as a choice
  that is on. Saved by a button, never on toggle.
- **The record box** (page 2's, keys 80px): Type, Name, Value per record, one group for the A record and
  one for the AAAA when this host has a public IPv6 address, a line-2 rule between groups. Name is "@" for an
  apex (one dot), else the first label, "@ or the first label" before a host is typed; Value is the address,
  or "this host's public IP address" in sans fg-2 when none is known.
- **Add a domain** (`#add`, a panel, padding 22, gap 22, "Add a domain" 500 18px): Domain (mono; capitals are
  read as lowercase, since DNS names are case-insensitive: "Shop.Example.COM" is `shop.example.com`, never refused for
  its case, and the API does the same), Sends
  traffic to (the app select, stopped apps disabled and an app with no HTTP port disabled as "(no HTTP port)";
  the service select only once an app is picked: picking one fetches `/parts/domains/add-service?app=` into
  `#domain-service-field` (a `display: contents` wrapper) and, without script, Check now draws it; before a
  pick the words "The app's first service. An app with several offers them here once it is picked." stand there
  and the post goes to the app's first HTTP service; one service's port as text, "`:8080`, set in Configure";
  on an app's page the app as text). **Not an HTTP port:** a service whose port is 5432, 3306, 6379, 27017, 5672
  or 11211, or that has a TCP-only health check and an image naming postgres, mysql, mariadb, redis, mongo,
  rabbitmq or memcached, is listed disabled as "db · :5432 · not an HTTP port" (`notHTTPPort`; the console's
  guess, never a refusal: the API takes any port). The filter's project select and every app select name an app
  outside Default › production the same way, "Shop / staging / web" (the project's display name, the
  environment, the name; `places.appLabel`, which Schedules uses too). Path (optional, mono), "Point it at
  caravana" with the record box and "The root of a domain needs an A record. A subdomain can use one too.",
  then Check: two lines of 14px, each an 8px mark and words, a 13px fg-3 reason under them. Before a check
  both are rings in fg-3; Check now (a submit of its own, `action=check`) runs the check on the post and
  answers 303 to the same page (`/domains` or `/apps/{id}/domains`) with `?checked=<host>#add`, so the
  browser lands on the panel and a reload doesn't post again; the page draws the panel from the check the
  console kept for that host for 60 seconds (with what was typed: the app, the service, the path), with ● or
  ■ or ◆ "DNS points at caravana" ("points at `185.…`" ■, "no DNS record" ◆) and the certificate line ("issued after
  you add it" for a new host, else its state). An unknown or expired `checked` draws the empty panel; a host
  that isn't a name is still refused in place (422) with the reason under the field. Add domain and deploy is primary when DNS was checked and
  points here or no check ran, secondary otherwise, and always there: the list shows the rest. Cancel is a link to
  the page itself (`/domains` or `/apps/{id}/domains`), without the panel's fragment. A refusal
  draws the form again with the reason under its field: another app's host (naming it), the console's own
  host, a host that isn't one, a bad path.
- **Live refresh swaps per row, never the forms.** The region's own swap is `none`. A notice (`sse:domains`) makes
  it fetch `/parts/domains` with `known`, a short key of the hosts it was drawn with (a hidden `#domains-known`
  inside the region, taken by `hx-include`). While the key still names the hosts there are, the answer is
  out-of-band fragments (`hx-swap-oob`) for the summary above the table (`#domains-summary`: count line, banners),
  each row's cells (`#domain-status-<host>`: host, status, Goes to, certificate, open link; a wrapper with
  `display: contents`) and each row's left column (`#domain-left-<host>`: the state's sentence, facts and its
  buttons), and nothing else. The Options form, Remove…, New password and the Add panel are never in it, so a
  refresh never takes what is being typed or closes what is open. When a host was added or removed the answer is
  the whole region with `HX-Reswap: innerHTML`; console.js puts back which `<details>` were open, by id. Every
  `<details>` on these pages has a stable id (`domain-<host>`, `domain-remove-<host>`, `domain-password-<host>`).
- **Narrow:** under 900px of room a row folds (chevron, host and the open link; then Status, Goes to and
  Certificate as label and value) and the detail stacks. Below 640px the banner stacks too: the mark and the
  text, then Check again under the text.

### Live updates
Regions that change (the apps table, copies, history, activity) refresh in place from the page's
event stream; what is being typed, such as the deploy form, sits outside every live region. Motion is
the live disc's single ping when the stream opens, the 120ms hover and focus transitions and the
button press. The theme also defines a 700ms tick flash and a 180ms drop-in, but no 007 surface
applies them yet. Reduced-motion settings turn all of it off: the stylesheet ends with a
`prefers-reduced-motion: reduce` rule that removes every animation and transition.

**The One Tick Rule.** Motion only answers an event, and only once. No looping animation, no
pulsing, no skeleton shimmer. A spinner only where a real wait is happening.

## Do's and Don'ts

### Do:
- **Do** pair every status color with its mark (● ◐ ◆ ■ ○).
- **Do** set every name, version, id, number and time in IBM Plex Mono with tabular figures.
- **Do** open every overview with the headline sentence (30px, weight 400), with raw data in mono
  underneath.
- **Do** build depth only from the four ink steps and the hairlines. Hover lifts a surface one
  step.
- **Do** keep the main action neutral: a Readout White fill, at most one per view.
- **Do** sort lists failed → warning → rolling → healthy → idle.
- **Do** explain a problem in a sentence plus one fix button, and put the raw Nomad error under it in
  muted mono.
- **Do** give a view's content at most one primary button, and put it in the form it submits; the
  frame's New app and an app header's Deploy link are the only others.
- **Do** confirm a consequence in a `<details>` disclosure under its button, and make Delete app ask
  for the app's name.
- **Do** label stacked-row facts with their column's name (`data-label`) and give every standalone
  link and control a 44px target on touch screens.
- **Do** tag a stderr line with the word "stderr" and keep stdout lines unmarked; set a log line as
  text, never markup.
- **Do** put every command to copy in its own `cv-cmd` block, and every error under its field with
  the field autofocused.
- **Do** take every color from the theme tokens. If a color is missing, add a token instead of
  writing a raw oklch value in a component.

### Don't:
- **Don't** use the status hues for decoration, links, tabs or the main action.
- **Don't** show status as a colored dot or color alone, and don't rely on telling red from green.
- **Don't** add drop shadows to panels, cards, buttons or inputs. Shadows are for overlays only.
- **Don't** use gradients, texture, imagery or illustration. The rolling mark's half fill is the only pattern.
- **Don't** add a colored left-border accent to panels, rows, banners or log lines. (The delete panel's full Fault Ember Edge outline is a border, not an accent.)
- **Don't** round anything fully except the circular status marks and the live indicator's disc
  and ring.
- **Don't** use daisyUI's solid `alert-*` fills for problems. Use a panel with a mark, a sentence
  and an action.
- **Don't** loop any animation, including the live dot.
- **Don't** put a label above a page headline or a panel title. The mono label is a section's own
  heading, never a kicker.
- **Don't** use emoji or unicode glyphs as icons. The only glyphs are → in diffs and · as a
  separator. Icons are Lucide line icons, and only in buttons, toolbars and the sidebar.
- **Don't** draw a logo mark. The wordmark is the word "caravana" in Plex Mono 600.
- **Don't** run a command into a sentence, or let a long one break at a hyphen mid-line.
- **Don't** show a status glyph from a font (● ◐ ◆ ■ ○). Draw the mark in CSS.
- **Don't** make it look like Dokploy, a gradient "AI dashboard", or a Grafana wall of charts.
