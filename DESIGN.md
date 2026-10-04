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
    height: "36px"
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
  tab:
    textColor: "{colors.label-text}"
    height: "46px"
  tab-active:
    textColor: "{colors.readout-white}"
---

# Design System: caravana console

<!-- Source: the Claude Design handoff "caravana console", Handoff 2, 2026-10-04 (local only, in
the gitignored design/console/). No console code exists yet; re-run /impeccable document after
007 ships so this file describes the built CSS, not just the handoff. -->

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
- **Panel Face** (oklch 0.195 0.007 255): panels, the sidebar, drawers.
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
- **Signal Blue** (oklch 0.76 0.12 245), healthy ●. It's also the focus ring (at 55%) and the
  live-tick flash (at 16%). Healthy is blue, not green.
- **Transit Lilac** (oklch 0.84 0.09 300), rolling ◐: a deploy in progress, a copy starting or
  draining.
- **Caution Amber** (oklch 0.90 0.13 88), warning ◆: a human should look soon. Also the meter
  fill above 75%.
- **Fault Ember** (oklch 0.68 0.17 40), failed ■: something is broken or was reverted. It's an
  orange-red, deliberately not a pure red.

### Named Rules
**The Shape-and-Color Rule.** Status is never color alone. Every status shows its mark next to
its color: ● healthy, ◐ rolling, ◆ warning, ■ failed, ○ idle. Hide the color and the status must
still read.

**The Status-Only Color Rule.** The four status hues mean status and nothing else. Buttons, links,
tabs and decoration stay neutral. When a screen is fully healthy, the only color on it is Signal
Blue on the status marks.

**The Flat Ground Rule.** No gradients anywhere. The one exception is the diagonal hatch in a
meter, which marks what copies have reserved.

## Typography

**Body Font:** IBM Plex Sans (with ui-sans-serif, system-ui)
**Data Font:** IBM Plex Mono (with ui-monospace, SF Mono, Menlo)

**Character:** two cuts of one family, so sentences and readouts sit together without fighting.
Sans is how caravana talks; mono is what Nomad reported.

### Hierarchy
- **Headline** (400, 30px, 1.2): the page h1, written as the sentence about the present ("3 apps
  healthy, 1 rollout in progress."). It's regular weight. The size carries it, not boldness.
- **Lede** (400, 15px, 1.5): the one-line description under the headline, in Body Text.
- **Title** (500, 18px, 1.35): panel and drawer titles.
- **Body** (400, 14px, 1.35): all UI text, buttons, tab labels.
- **Label** (500, 12px mono, 0.06em, uppercase): section heads like "CLUSTER" and "NEEDS YOU",
  and field labels above inputs.
- **Data** (400, 13px mono, tabular figures): names, versions, digests, ids, ports, hosts,
  durations, timestamps, every number in a table.
- **Metric** (500, 24px mono, tabular figures): a live value in a metric tile.
- **Wordmark** (600, 20px mono, -0.01em): the word "caravana", lowercase. There's no logo mark.

### Named Rules
**The Mono-Is-Real Rule.** If Nomad or the user's config produced it (`whoami`, `v24`, `hel-1`,
`sha256:9f1c…`, `512 MB`, `14:02:11`), it's set in mono. If caravana is explaining something,
it's sans. Never mix the two inside one token.

**The Tabular Rule.** Every value that updates live uses tabular figures, so a number changing
in place never shifts its neighbors.

## Layout

- **Shell:** a fixed left sidebar (248px) holds the org name, the Developer/Operator switch,
  grouped navigation, settings and the account row. A sticky top bar (52px) holds the
  breadcrumbs (org › project › environment › page), a live clock with the live dot, and the
  page's main action.
- **Content:** a centered column, max 1440px, with 26px side gutters and about 36–40px of top
  padding. It's a vertical stack: page header, then sections 28px apart.
- **Page header:** the headline and lede on the left, the primary action aligned right.
- **Sections:** a section head (label, a muted meta line in Body, an action on the right) over a
  panel. Rows inside panels are CSS grid with 1px dividers between them, 46–52px tall for list
  rows and 36px for dense table rows.
- **Overview pages:** a main column plus a 400px right rail (needs you, activity, capacity).
- **App pages:** four tabs by intent (Running, Ship, Configure, Logs). Raw and advanced settings
  sit behind an explicit "Advanced settings" boundary.
- **Login:** the only page without the shell. A centered 380px column on the bare ground.
- **Spacing rhythm:** 2 / 4 / 8 / 10 / 16 / 20 / 26 / 32 / 40 / 60px. Panel padding is about
  12px, gaps inside a grid 8–10px.
- **Narrow screens:** the handoff is desktop-only and has no breakpoints. How the sidebar and
  grid rows collapse on a phone is still open, to be settled in 007.

### Named Rules
**The Problems-First Rule.** Every list sorts by status: failed, warning, rolling, healthy, idle.
What needs a human is always at the top.

## Elevation & Depth

Flat. Depth comes from four lightness steps (Unlit Panel → Panel Face → Raised Control → Lit
Control) and three border weights, never from shadow. Hovering a surface lifts it one step and
moves its border up to Strong Edge.

### Shadow Vocabulary
- **Overlay** (`box-shadow: 0 12px 32px oklch(0 0 0 / 0.45), 0 0 0 1px oklch(0.340 0.010 255)`):
  dropdowns, the org switcher, drawers, modals. Nothing else.

### Named Rules
**The Overlays-Only Shadow Rule.** A shadow means "this is floating above the page". Panels,
cards, buttons and inputs never get one. Translucency and blur aren't used, except for the faint
warning tint inside an inline prerequisite.

## Shapes

Small, square-ish corners that read as machined, not soft: 2px for chips, pills and meters, 4px
for buttons and inputs, 6px for panels, drawers and modals. Nothing is fully rounded except the
switch track, the account avatar and the healthy/rolling/idle status marks. Borders are always 1px hairlines.

## Components

Precise and unadorned: controls feel like labeled switches on a panel, not soft app buttons.

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
  full Fault Ember on hover. Destructive actions confirm in a modal.
- **States:** transitions are 120ms on `cubic-bezier(0.2, 0.7, 0.2, 1)`. Pressing nudges the
  button down 0.5px, with no scale. Focus is a 2px Signal Blue ring at 55%, offset 2px.
- **Labels:** a verb that names the outcome ("Revert to v22", "Raise to 2 GB"). An optional
  16px Lucide icon at 1.5px stroke sits before the label.
- **Icon button:** a square ghost button (28px), always with `aria-label` and `title`.

### Status pill and mark
- **Mark:** an 8px shape. Healthy is a filled circle, rolling a circle with its left half filled,
  warning a rotated square, failed a square, idle an empty ring.
- **Pill:** 22px tall, 2px corners, the status ground and edge, the status color as text, in Data
  mono at 12px, with the mark first. A "quiet" variant drops the tinted ground inside dense
  tables.

### Panels
- **Corner Style:** 6px.
- **Background:** Panel Face with a 1px Hairline border, overflow clipped.
- **Shadow Strategy:** none (see Elevation & Depth).
- **Rows:** a 1px Hairline divider between rows. A row turns Raised Control on hover.

### Inputs / Fields
- **Style:** Raised Control fill, Control Edge border, 4px corners, Readout White text (mono at
  12.5px for values), 12px side padding, 36px tall. The label sits above in Label style. A prefix
  or suffix (`https://`, `MB`) sits inside the field in Label Text.
- **Focus:** the border moves to Strong Edge and the 2px Signal Blue ring appears around it.
- **Hint / Error:** a hint below in Label Text. An error below in Fault Ember with the failed
  mark, written as a sentence about what's wrong ("DNS doesn't point here yet").
- **Switch:** the only fully rounded control. Readout White track when on, label to the right,
  hint underneath.

### Navigation
- **Sidebar:** grouped links in Body, 34px tall with 4px corners. The active link sits on Raised
  Control in Readout White, others in Body Text. Group names are Label style. A small Developer/Operator segmented switch
  sits at the top.
- **Tabs:** by intent, usually four. 46px tall, 26px apart, sans 500 at 13.5px, over a Hairline
  rule. Inactive tabs use Label Text, turn Body Text on hover, and the active tab is Readout White
  with a 2px Readout White underline. A count in mono can follow the label, and a status mark can
  ride on a tab when something there needs attention.
- **Breadcrumbs:** in the top bar, segments in mono where they're literal names, separated by ›.

### Meter
A thin 6px bar on Lit Control with 1px corners and a mono uppercase label to its left. The solid
fill is live usage, in the status color: Signal Blue normally, Caution Amber from 75%, Fault Ember
from 90%. A band of fine vertical Strong Edge lines behind it is what copies have reserved, and a
dashed line marks the limit. The fill width animates over 180ms.

### Needs-you item and problem banner
A panel with the status mark, one plain sentence (what happened, what caravana did), a Data mono
detail line underneath with the raw Nomad facts and time, and exactly one action button that
fixes it. It never uses a solid colored alert fill.

### Ledger row (deployment history)
One row per version, newest first. The version (`v24`) and time in mono, the trigger in sans,
the changes in mono diffs (`9f1c2a → 4e0a77`). A failed version carries the failed pill and a
sentence explaining the revert, plus a "Revert to" button on older healthy versions.

### Live updates
When a server-sent event changes a value, that value's background flashes Signal Blue at 16%
once and fades over 700ms. New feed rows drop in 3px over 180ms. The live dot in the top bar
pings once per event. Reduced-motion settings turn all of it off.

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
- **Do** explain a problem in a sentence plus one fix button.
- **Do** take every color from the theme tokens. If a color is missing, add a token instead of
  writing a raw oklch value in a component.

### Don't:
- **Don't** use the status hues for decoration, links, tabs or the main action.
- **Don't** show status as a colored dot or color alone, and don't rely on telling red from green.
- **Don't** add drop shadows to panels, cards, buttons or inputs. Shadows are for overlays only.
- **Don't** use gradients, texture, imagery or illustration. The meter hatch is the only pattern.
- **Don't** add a colored left-border accent to panels, rows or banners.
- **Don't** round anything fully except the switch, the account avatar and the circular status
  marks.
- **Don't** use daisyUI's solid `alert-*` fills for problems. Use a panel with a mark, a sentence
  and an action.
- **Don't** loop any animation, including the live dot.
- **Don't** put a label above a page headline or a panel title. The mono label is a section's own
  heading, never a kicker.
- **Don't** use emoji or unicode glyphs as icons. The only glyphs are → in diffs and · as a
  separator. Icons are Lucide line icons, and only in buttons and toolbars.
- **Don't** draw a logo mark. The wordmark is the word "caravana" in Plex Mono 600.
- **Don't** make it look like Dokploy, a gradient "AI dashboard", or a Grafana wall of charts.
