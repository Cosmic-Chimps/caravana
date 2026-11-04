# Contract: a light theme for the console

Written by the caravana_designer session on 2026-11-03, before the spec, for the CodeAlta task "A light
theme for the console" (`.alta/tasks/2026-11-03-a-light-theme-for-the-console-zqj6.md`). It is a light
variant of today's identity, not a new look. The earlier light "field notebook" direction stays turned down.
Kept:
- status is the only colour, and every status pairs a shape with its colour;
- IBM Plex Sans for sentences and Plex Mono for what Nomad produced;
- flat hairline panels and small radii;
- depth from lightness steps, and a shadow only on overlays.

Every value below was computed, not eyeballed: oklch to sRGB to WCAG relative luminance. The calculator is
in the designer's tmp folder (`light/cr.py`). The builder adds a Go test that recomputes these ratios from
`theme.css` (see Tests), so a later edit can't quietly break one.

## 1. How the theme is chosen

- **The system decides by default.** Without a choice, the console follows `prefers-color-scheme`. Dark
  stays the default when the browser says nothing (the `@media` block adds light; it doesn't replace dark).
- **A switch can force one: System, Light or Dark.** It lives in **Settings › This browser › Appearance**
  (`/settings/appearance`), a new group listed last in the Settings nav.
  - It goes in Settings, not the sidebar. It's chosen once and rarely changed, and the sidebar is for moving
    around (the Developer/Operator toggle changes often; this doesn't).
  - The group's name says the choice belongs to this browser, not to the installation, unlike every other
    Settings page.
- **The form:** a `.cv-panel` with a `StField` row. The label is "Theme", with the hint "Kept in this
  browser only." Beside it are three radio options:
  - "Match the system (now dark)", where the word in brackets is the system's current choice. Without
    script the brackets are left out.
  - "Light".
  - "Dark".
  Then **Save** (secondary sm). `POST /settings/appearance` is protected like the other posts and answers
  303 to `/settings/appearance?saved=1`, which draws "Saved." (023's notice). It works without script.
- **The cookie:** `caravana_theme` = `light` | `dark`, set the same way as the mode cookie (Path `/`, one
  year, `SameSite=Lax`, `HttpOnly`, `Secure` over HTTPS). "Match the system" deletes it. The sign-in page
  reads it too, so a forced theme holds after sign-out.
- **The shell:** with the cookie set, the server writes `data-cv-theme="light"` or `"dark"` on `<html>`,
  and `<meta name="color-scheme">` says that one word. Without it there's no attribute, and the meta says
  `"dark light"`. The theme is in the first byte of the page, so it never flashes.
  - Use `data-cv-theme`, not `data-theme`: daisyUI gives `data-theme` its own meaning (it names a daisy
    theme).
- **Native controls follow:** `color-scheme` is set on `:root` per theme (`dark`, `light`). The three
  hard-coded `color-scheme: dark` (the date field, `.cv-select`, `.cv-check`) are removed so they inherit
  it.

## 2. The palette, token by token

### How depth works in light

Depth is still lightness steps, measured as **distance from the panel face**:
- In dark, the panel is a step lighter than the page, and controls step further up.
- In light, the panel is the brightest surface (near white, as in daylight), and controls step **down**
  from it.
- Hovering a surface moves it one more step away from the panel in both themes.

So `ink-2` still means "Raised Control" and `ink-3` "Lit Control". In light they are darker than the panel,
not lighter. Every class keeps its token, and only the value changes.

### The neutral ramp

| Token | Role | Dark (today) | Light |
| --- | --- | --- | --- |
| `ink-0` | Unlit Panel, the page | 0.165 0.006 255 | **0.968 0.004 255** |
| `ink-1` | Panel Face | 0.195 0.007 255 | **0.995 0.002 255** |
| `ink-2` | Raised Control (inputs, chips, secondary buttons, table heads, row hover) | 0.225 0.008 255 | **0.952 0.005 255** |
| `ink-3` | Lit Control (hover on raised, selected rows) | 0.260 0.009 255 | **0.922 0.007 255** |
| `line-1` | Hairline | 0.275 0.009 255 | **0.905 0.006 255** |
| `line-2` | Control Edge | 0.340 0.010 255 | **0.835 0.008 255** |
| `line-3` | Strong Edge, link underline | 0.440 0.012 255 | **0.700 0.010 255** |
| `fg-1` | Readout (primary text) | 0.955 0.004 255 | **0.215 0.010 255** |
| `fg-2` | Body Text | 0.810 0.008 255 | **0.360 0.012 255** |
| `fg-3` | Label Text | 0.680 0.010 255 | **0.475 0.012 255** |
| `fg-4` | Dim Mark, marks only | 0.500 0.010 255 | **0.640 0.010 255** |

Measured on the panel (ink-1). The light ramp keeps dark's ratios step for step:

| | fg-1 | fg-2 | fg-3 | fg-4 |
| --- | --- | --- | --- | --- |
| Dark on ink-1 | 16.0 | 10.1 | 6.35 | 3.05 |
| Light on ink-1 | 17.3 | 10.7 | 6.58 | 3.31 |
| Light, lowest of ink-0 to ink-3 | 13.9 | 8.6 | **5.30** (ink-3) | 2.67 (ink-3) |

fg-3 stays at or above 4.5:1 on every ground, so labels are legal text everywhere. fg-4 stays a mark colour,
as in dark.

### Primary, secondary, accent and the daisyUI slots

- The **main action keeps the most contrast with its ground.** In dark that's "Readout White" with dark
  text. In light it inverts to **Readout Ink**:
  - `primary` **0.255 0.012 255**, with `primary-content` **0.985 0.003 255** (15.1:1);
  - `primary-press` **0.330 0.014 255** (hover moves toward the ground, as dark's does).
- `secondary` = `ink-2`, `neutral` = `ink-3`, and their `-content` = `fg-1`.
- `base-100/200/300` = `ink-0/1/2` and `base-content` = `fg-2`, in both themes. The daisy slots point at
  the ramp, so the ramp is the only place with two values.
- `accent`, `success` = healthy, `info` = rolling, `warning` = warn, `error` = failed. In light each
  `-content` is `ink-1`, since the fills are dark there.

### Status: colour, ground and edge

All four status colours must reach **4.5:1 as text on every ground** (ink-0 to ink-3) and on their own
tinted ground. Pills set 12px mono words in them, and the same colour draws the 7px mark, which needs only
3:1.

| Status | Colour | Ground (`-bg`) | Edge (`-line`) | Lowest text contrast |
| --- | --- | --- | --- | --- |
| failed ■ | **0.530 0.165 38** | **0.952 0.022 40** | **0.800 0.075 40** | 4.53 (ink-3), 4.92 own ground |
| healthy ● | **0.505 0.135 250** | **0.952 0.022 245** | **0.800 0.060 245** | 4.67 (ink-3), 5.12 own ground |
| rolling ◐ | **0.525 0.130 300** | **0.955 0.022 300** | **0.810 0.060 300** | 4.53 (ink-3), 4.97 own ground |
| warn ◆ | **0.510 0.108 75** | **0.962 0.040 88** | **0.830 0.085 85** | 4.66 (ink-3), 5.25 own ground |
| idle ○ | `fg-3` | `ink-2` | `line-2` | 5.80 on ink-2 |

- `danger-press` (the danger button's hover ground) is **0.925 0.035 40**, with failed text on it at 4.52.
- **Warn becomes a dark amber** (hue 75, toward ochre). Caution Amber at 0.90 is unreadable on white, and
  a warn word must stay readable.
- **What changes about lightness separation:** in dark, the four sit at 0.68 to 0.90, so they separate in
  greyscale. In light, all four must sit near 0.51 to 0.53 to reach 4.5:1 on every ground, so greyscale
  separation is weak.
  - The shape carries it, which is what the Shape-and-Color Rule is for. A light page must never show a
    status as colour alone: a pill, a mark, a coloured word without its mark, nowhere.
  - That rule already holds console-wide. The review checks it again in light.

### The direct colours in `input.css`, as tokens

| New token | Replaces | Dark | Light |
| --- | --- | --- | --- |
| `--color-focus` | 7 × `oklch(0.76 0.12 245 / 0.55)` in `:focus-visible` outlines, and the base rule and `.cv-field:focus` in `theme.css` | `oklch(0.76 0.12 245 / 0.55)` (5.2:1 on ink-1) | **healthy at full strength, 0.505 0.135 250** (5.8:1). At 55% it would be 1.8:1, too faint to find. |
| `--color-failed-wash` | `.cv-row-error`, `.cv-row-open.cv-row-error` | failed / 0.07 | **failed / 0.06** (fg-1 on it 16.4:1) |
| `--color-failed-wash-hover` | `a.cv-row-error:hover` | failed / 0.11 | **failed / 0.10** |
| `--color-hue-0` … `-5` | `.cv-hue-0` … `-5` | today's six | the table below |
| `--color-tick` | `cv-tick` keyframes | healthy / 0.16 | **healthy / 0.12** |
| `--color-ping` | `cv-ping` keyframes | healthy / 0.6 | **healthy / 0.5** |
| `--shadow-overlay` | the overlay shadow | today's | **`0 12px 32px oklch(0.30 0.02 255 / 0.16), 0 0 0 1px` line-2's light value** |

- **Not colours:** the two `#000` stops in `mask-image` (the Settings nav and the logs rail fade) are alpha
  masks. They're the same in both themes and stay as written. The token test allows `#000` inside
  `mask-image` only.

### Service hues (024)

They keep their six hue angles and become dark enough to set a name in. Each is ≥4.5:1 on ink-1 and on ink-2
(the chosen rail item). None equals a status colour.

| | Dark | Light | On ink-1 | On ink-2 |
| --- | --- | --- | --- | --- |
| hue-0 | 0.78 0.08 250 | **0.500 0.095 250** | 5.89 | 5.20 |
| hue-1 | 0.78 0.09 300 | **0.500 0.100 300** | 6.16 | 5.43 |
| hue-2 | 0.80 0.07 200 | **0.515 0.080 200** | 5.34 | 4.71 |
| hue-3 | 0.80 0.08 150 | **0.515 0.095 150** | 5.31 | 4.68 |
| hue-4 | 0.80 0.08 80 | **0.520 0.095 80** | 5.49 | 4.85 |
| hue-5 | 0.78 0.08 20 | **0.510 0.105 20** | 5.98 | 5.28 |

hue-0 (250) and healthy (250) share an angle in light. They differ in chroma (0.095 against 0.135) and
in use: a hue is only ever a service's name and its 8px square, never a mark. That was already the rule in
dark (DESIGN.md, "The service hue").

## 3. Charts and meters

They draw from tokens through classes (25 `fill:`/`stroke:` rules read `var(--color-…)`, and none writes a
colour). So they follow the theme with no change:
- 025's charts, with the band and the deploy markers;
- the meters, with the hatched reserved part (a dashed fg-4 stroke);
- the logs histogram, with its hatched unread bins;
- the sparklines.

Points to check at the review, not exceptions:
- **The hatch:** fg-4 dashes on the track read 2.9:1 on ink-2 in light, the same as dark (2.85). It's a mark,
  so that's acceptable. If it looks lost in daylight, the hatch moves to line-3 in light only, as a token
  (`--color-hatch`).
- **A chart's band** (min to max of the past week) is drawn as a fill at low opacity. Its opacity was tuned
  on dark. If the band vanishes on white, it becomes a token too (`--color-band`). The review decides.

## 4. The terminal stays dark

xterm.js (020.6) takes its palette from `terminal.js` as hex, since it can't read CSS variables. The
terminal **stays dark in both themes**:
- Programs write their colours assuming a dark terminal. `ls --color`, prompts and editors pick colours
  that read on dark and wash out on white.
- A terminal is a device inside the page, like a screen set into a light panel. It isn't a surface of the
  console.
- The page around it follows the theme: the head, the shell picker and the buttons. The terminal's box
  keeps its dark ground, framed by a 1px `line-2` edge.

`terminal.js`'s hex palette is **the one permitted exception** to "every colour is a token". The token test
names the file, and a comment in it says why.

## 5. DESIGN.md changes (the builder writes them; I review)

- **Key Characteristics:** "Cool neutral ground (hue 255, chroma ≤ 0.012), dark by default with a light
  variant of the same ramp, depth from lightness steps, never shadow."
- **Colors:** each named colour gains its light value beside the dark one, in the tables above.
  - The names describe roles, so they stay: Unlit Panel, Panel Face, Raised Control and Lit Control.
  - Readout White gains its light counterpart, **Readout Ink**.
- **Elevation & Depth:** replace "Elevation goes up in lightness" with "**Depth is distance from the panel
  face**". In dark the steps go up in lightness, and in light they go down. Hover moves one more step away
  from the panel in both.
- **The Shape-and-Color Rule** gains one sentence: "In light, the four status colours sit close in lightness
  (0.51 to 0.53), so greyscale doesn't separate them; the shape always does."
- **The No-Dim-Text Rule:** no change. Light fg-4 is 3.3:1 on the panel, the same role and the same ban on
  text.
- **The focus ring:** 55% Signal Blue in dark, full-strength healthy in light.
- **New, The Token-Only Color Rule:** "Every colour is a token in `theme.css` with a dark and a light value.
  A class reads tokens; it never writes `oklch()`, `#hex` or `rgb()`. Exceptions, named: the alpha masks
  (`#000` in `mask-image`) and the terminal's palette in `terminal.js`."
- **The service hue:** the light six beside the dark six.
- **The terminal:** one line, "The terminal stays dark in both themes."

## 6. Tests the builder adds

- **No colour outside the tokens:** `input.css`, the templates and our scripts (not vendored) contain no
  `oklch(`, `#rgb`/`#rrggbb` or `rgb(`. The named exceptions are allowed: `#000` in a `mask-image`, and
  `terminal.js`.
- **Every token has both values:** each `--color-*` the dark block defines is also in the light block, and
  the forced dark block equals the default.
- **Contrast:** parse both blocks of `theme.css` and recompute:
  - fg-1 to fg-3 and the four status colours ≥4.5:1 on ink-0 to ink-3;
  - each status colour ≥4.5:1 on its own `-bg`;
  - the six hues ≥4.5:1 on ink-1 and ink-2;
  - `--color-focus` ≥3:1 on ink-1 and ink-2;
  - fg-4 ≥2.5:1 on ink-1 (a mark).
  The oklch math is about thirty lines (it is in `cr.py`).
- **The shell:** no cookie gives no `data-cv-theme` and `color-scheme` "dark light"; each cookie value gives
  its attribute; the sign-in page reads it too; the Appearance form answers 303 and sets or deletes the
  cookie.
- `tools/ui-css.sh check`, and a lab step that reads one page in each forced theme.

## 7. Review (the designer, after the build)

Captured at 1440 and 390, in light and dark, both by system preference (emulated) and forced by the cookie:
- the Apps home;
- an app's Running, Deployments, Configure and Domains;
- Logs, with the histogram and an error row;
- Monitoring, with its charts, meters, hatch and band;
- Schedules, Settings (including Appearance) and the sign-in page;
- the three Operator pages;
- the terminal page;
- one page without script.

On each, I check:
- every status shows its shape;
- no text falls under its target;
- focus is visible on a keyboard walk;
- native controls (date, select, checkbox) match the theme;
- the overlay shadow reads as floating, not dirty.

Then the impeccable critique (no detector), my rulings, the fixes and the last look.


## 8. The review, as it went (2026-11-04)

The builder (Opus, this session) staged the console on the lab (`/tmp/lt-review/stage.sh`: whoami with two
copies and a domain, a broken app whose deploy failed, an app never deployed, the project Shop Floor with
staging and production, a schedule) and read every page listed in section 7 in Microsoft Edge 156, by
emulated system preference and forced through the Appearance form, at 1440 and 390.

**Found and fixed before the review proper:** the first build wrote every token as `light-dark(<dark>,
<light>)`, but the function takes the light value first, so a light system got the dark palette and the
default dark page would have shown the light one. The sign-in page's first capture caught it; every token's
two values were swapped into the function's order (40705db), the contrast test's two sides with them.

**Checked, and right:** the sign-in page, the Apps home (needs-you rows, the tables, the host meters with
the hatched reserved part, Activity), Running (pills, meters), Deployments (the stages strip, a failed entry),
Configure (the danger panel), Domains (the Add panel), Logs (the histogram's hatched bins, the service hues,
the deploy rows in lilac), Monitoring (the gap hatch; the band couldn't be seen yet, the baseline was still
building: it is `ink-3` at 70 % with a `line-2` edge, which reads on the light panel, so it needs no token),
Schedules at 390, Settings › Appearance at 1440 and 390 ("(now light)" filled by script, "Saved." after a
post), API tokens with the Revoke overlay open (the shadow reads as floating, the date field is light), the
Operator Overview and Updates (the checkbox follows), the project page, the terminal page (the box stays
dark inside a light page, the shell select is light), the Menu overlay at 390, a keyboard walk (the
full-strength focus ring is visible on white), and dark pages after the fix (unchanged from before).
Forced Dark under a light system and forced Light under a dark system both hold, and the sign-in page
with the cookie alone carries the theme without script (curl).

**Points of section 3:** the fg-4 hatch on the meters and the gap hatch read on white, faint but present,
as a mark should; no `--color-hatch` or `--color-band` token was needed.

## 9. Critique rulings (2026-11-04)

The impeccable critique (a Sonnet session with browser tools, critique only, no detector) measured every
text node's contrast on the main pages in the three combinations and found no blocker: no text under
4.5:1 but the two items below, every status with its shape, the cookie and the sign-in page right, focus
visible, native controls following. Its nine findings, and what the designer ruled:

1. **The Logs level chip's off state is `fg-4` text** (3.06:1, an operable toggle). Fixed: the off chip is
   `fg-3`, and the on debug chip `fg-2`, so the on state still steps up.
2. **Placeholders are `fg-4`** (2.9:1 on the field fill). Fixed: `fg-3` in both themes (the field, the box
   field and the logs search). A placeholder stays a step lighter than a value.
3. **Field boundaries are faint in light** (`line-2` 1.6:1 on the panel). Kept, as the contract decided: a
   field is told by its fill and its edge together, hover moves the edge to `line-3`, and focus draws the
   full-strength ring. Recorded for the maintainer's eye on a real screen; a `--color-field-edge` token is
   the change if it proves too faint.
4. **The histogram's count bars are `ink-3` on the panel** (1.2:1 in light). Fixed with a token,
   `--color-bar` (`ink-3` in dark, Dim Mark's value in light, about 3:1) and `--color-bar-hover`.
5. **The level chips colour the words "error" and "warn" without a mark.** Ruled exempt: the word spells
   the status and the chip is a filter, not a status; the Shape-and-Color Rule is about a status shown.
6. **Checked radios and checkboxes take the healthy blue.** Kept: DESIGN.md already gives Signal Blue to the
   accent of a checked control, the focus ring and the sidebar's current bar; a selection in blue is a
   recorded exception of the Status-Only Color Rule, in both themes.
7. **Monitoring's hidden data table makes a phantom scroll area** (the page is about 4,700px tall at
   1440). Not a theme matter and verified; proposed as a follow-up task.
8. **The terminal shows xterm's own black viewport as a band inside the box**, visible once the page is
   light. Fixed: one unlayered rule paints the viewport with `--color-terminal`.
9. **The Menu overlay is a white panel over white cards** under 900px. Kept: the 16 % shadow and the rim
   read as floating in the review's capture; raising the shadow would make it dirty.
