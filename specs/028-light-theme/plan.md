# Implementation plan: 028 a light theme for the console

**Branch**: `028-light-theme` (built on the worktree branch `alta/fluent-topaz-6qk8`) | **Date**:
2026-11-03 | **Spec**: `spec.md` | **Contract**: `contracts/light-theme.md` (wins over both).

## Summary

Every colour of the console becomes a token in `theme.css` that carries its dark and its light value
in one declaration, `light-dark(<light>, <dark>)` (the light value first, as the function takes them).
The theme is then a matter of `color-scheme` on
`:root`: dark by default, light when the browser prefers light and no cookie forces dark, and forced
by the cookie `caravana_theme` through `data-cv-theme` on `<html>`. A Settings page sets the cookie.
Tests keep the rules (no colour outside the tokens, both values per token, the contract's contrast
ratios), and DESIGN.md says how the two themes relate.

## Technical context

- Go 1.27, `html/template`, HTMX; the stylesheet is Tailwind v4.3.3 + daisyUI 5.7.47 through
  `tools/ui-css.sh` (pinned binaries, no Node). No new Go module, no migration, no API change.
- Browsers: `light-dark()` is in every evergreen browser since 2024 (Chrome 123, Firefox 120,
  Safari 17.5). An older browser ignores the declaration and gets daisyUI's dark slots, which stay
  readable. Checked on 2026-11-03 against the pinned build: the standalone CLI emits
  `light-dark(oklch(…), oklch(…))` verbatim (no polyfill), a `var(--color-ink-0)` in the daisyUI
  plugin block comes out as written, and the two `color-scheme` rules are kept.

## Decisions (research)

**R1. One declaration per token, not a second block.** `--color-ink-0: light-dark(oklch(0.968 0.004
255), oklch(0.165 0.006 255));` (light first, dark second: the function's order, which the first build
got backwards and the live review caught) holds the contract's table's two values in one line, and the "both values"
test is a parse of each line. The alternative, a `@media (prefers-color-scheme: light)` block plus a
forced-light block plus a forced-dark block, writes the light ramp twice and the dark ramp twice.
`light-dark()` picks by the element's `color-scheme`, which only `:root` sets (FR-005), so every
token resolves the same way on every element. The contract's wording "the `@media` block adds light"
describes the outcome; the outcome is the same.

**R2. Switching.** In `theme.css`, unlayered, after `@theme`:

```css
/* The theme (028): dark unless the browser prefers light; a forced choice (the cookie, FR-002) wins. */
@media (prefers-color-scheme: light) { :root:not([data-cv-theme="dark"]) { color-scheme: light; } }
:root[data-cv-theme="light"] { color-scheme: light; }
```

The daisyUI plugin block keeps `color-scheme: dark` (it emits on `:where(:root)` and
`[data-theme=caravana]`, which both lose to these). `<html data-theme="caravana">` stays: it is
daisyUI's. The three hard-coded `color-scheme: dark` on controls are removed.

**R3. Where the daisyUI slots point.** In the plugin block: `--color-base-100/200/300` are
`var(--color-ink-0/1/2)`, `--color-base-content` is `var(--color-fg-2)`, `--color-secondary` is
`var(--color-ink-2)`, `--color-neutral` `var(--color-ink-3)`, both `-content` `var(--color-fg-1)`;
`--color-accent` and `--color-success` are `var(--color-healthy)`, `--color-info`
`var(--color-rolling)`, `--color-warning` `var(--color-warn)`, `--color-error` `var(--color-failed)`;
their `-content` is `light-dark(var(--color-ink-1), var(--color-ink-0))` (the fills are dark in
light, so the word on them is the panel's near-white). Primary isn't on the ramp, so the plugin block
holds its two values: `--color-primary: light-dark(oklch(0.255 0.012 255), oklch(0.955 0.004 255))`,
`--color-primary-content: light-dark(oklch(0.985 0.003 255), oklch(0.165 0.006 255))`; in `@theme`,
`--color-primary-press: light-dark(oklch(0.330 0.014 255), oklch(0.88 0.006 255))` and
`--color-danger-press: light-dark(oklch(0.925 0.035 40), oklch(0.31 0.07 40))`.

**R4. The tokens.** `@theme` in `theme.css`; in the file the light value (the contract's) comes first and
the dark one (today's) second, as `light-dark()` takes them. The tables keep dark first for reading:

| Token | Dark | Light |
| --- | --- | --- |
| ink-0 | 0.165 0.006 255 | 0.968 0.004 255 |
| ink-1 | 0.195 0.007 255 | 0.995 0.002 255 |
| ink-2 | 0.225 0.008 255 | 0.952 0.005 255 |
| ink-3 | 0.260 0.009 255 | 0.922 0.007 255 |
| line-1 | 0.275 0.009 255 | 0.905 0.006 255 |
| line-2 | 0.340 0.010 255 | 0.835 0.008 255 |
| line-3 | 0.440 0.012 255 | 0.700 0.010 255 |
| fg-1 | 0.955 0.004 255 | 0.215 0.010 255 |
| fg-2 | 0.810 0.008 255 | 0.360 0.012 255 |
| fg-3 | 0.680 0.010 255 | 0.475 0.012 255 |
| fg-4 | 0.500 0.010 255 | 0.640 0.010 255 |
| healthy | 0.76 0.12 245 | 0.505 0.135 250 |
| rolling | 0.84 0.09 300 | 0.525 0.130 300 |
| warn | 0.90 0.13 88 | 0.510 0.108 75 |
| failed | 0.68 0.17 40 | 0.530 0.165 38 |
| healthy-bg | 0.27 0.05 245 | 0.952 0.022 245 |
| rolling-bg | 0.28 0.045 300 | 0.955 0.022 300 |
| warn-bg | 0.29 0.05 88 | 0.962 0.040 88 |
| failed-bg | 0.27 0.06 40 | 0.952 0.022 40 |
| healthy-line | 0.44 0.08 245 | 0.800 0.060 245 |
| rolling-line | 0.46 0.07 300 | 0.810 0.060 300 |
| warn-line | 0.50 0.08 88 | 0.830 0.085 85 |
| failed-line | 0.46 0.10 40 | 0.800 0.075 40 |
| hue-0 | 0.78 0.08 250 | 0.500 0.095 250 |
| hue-1 | 0.78 0.09 300 | 0.500 0.100 300 |
| hue-2 | 0.80 0.07 200 | 0.515 0.080 200 |
| hue-3 | 0.80 0.08 150 | 0.515 0.095 150 |
| hue-4 | 0.80 0.08 80 | 0.520 0.095 80 |
| hue-5 | 0.78 0.08 20 | 0.510 0.105 20 |

New tokens, replacing every direct colour of `input.css` and the two keyframes and the shadow of
`theme.css` (the contract's table, plus the lines it didn't list, marked *proposed* for the review):

| Token | Replaces | Dark | Light |
| --- | --- | --- | --- |
| `focus` | the 7 `:focus-visible` outlines in `input.css`, `:focus-visible` and `.cv-field:focus` in `theme.css` | `oklch(0.76 0.12 245 / 0.55)` | `oklch(0.505 0.135 250)` |
| `failed-wash` | `.cv-row-error`, `.cv-row-open.cv-row-error`; *also* `.cv-row-deploy-failed` and `.cv-copychip-failed` (today 0.08: one token, the 0.01 is invisible) | `oklch(0.68 0.17 40 / 0.07)` | `oklch(0.530 0.165 38 / 0.06)` |
| `failed-wash-hover` | `a.cv-row-error:hover`; *also* `a.cv-row-deploy-failed:hover` (today 0.13) | `oklch(0.68 0.17 40 / 0.11)` | `oklch(0.530 0.165 38 / 0.10)` |
| `rolling-wash` *proposed* | `.cv-row-deploy` | `oklch(0.84 0.09 300 / 0.08)` | `oklch(0.525 0.130 300 / 0.06)` |
| `rolling-wash-hover` *proposed* | `a.cv-row-deploy:hover` | `oklch(0.84 0.09 300 / 0.13)` | `oklch(0.525 0.130 300 / 0.10)` |
| `fresh-wash` *proposed* | `.cv-row-fresh::after` (fg-1 at 5 %) | `oklch(0.955 0.004 255 / 0.05)` | `oklch(0.215 0.010 255 / 0.05)` |
| `anchor-wash` *proposed* | `.cv-row-anchor`, `.cv-lrow:target` (fg-1 at 9 %) | `oklch(0.955 0.004 255 / 0.09)` | `oklch(0.215 0.010 255 / 0.09)` |
| `hatch-fill` *proposed* | the `fill` of `.cv-hbar-unread` and `.cv-mngap` (fg-4 at 12 %; the dashes stay `fg-4`) | `oklch(0.500 0.010 255 / 0.12)` | `oklch(0.640 0.010 255 / 0.12)` |
| `hue-0…5` | `.cv-hue-0…5` (`--cv-hue: var(--color-hue-N)`) | the table above | the table above |
| `tick` | the `cv-tick` keyframe | `oklch(0.76 0.12 245 / 0.16)` | `oklch(0.505 0.135 250 / 0.12)` |
| `ping` | the `cv-ping` keyframe | `oklch(0.76 0.12 245 / 0.6)` | `oklch(0.505 0.135 250 / 0.5)` |
| `overlay-shade` | the first colour of `--shadow-overlay`, which becomes `0 12px 32px var(--color-overlay-shade), 0 0 0 1px var(--color-line-2)` (`light-dark()` takes colours, not a shadow list) | `oklch(0 0 0 / 0.45)` | `oklch(0.30 0.02 255 / 0.16)` |
| `terminal` | `.cv-terminal`'s ground (contract §4: dark in both themes) | `oklch(0.165 0.006 255)` | `oklch(0.165 0.006 255)` |

The `#000` stops of the two `mask-image` rules stay (alpha masks). After the change `input.css` has
no `oklch(` at all, and `theme.css` has `oklch(` only inside `--color-*` declarations.

**R5. The cookie and the page.** `internal/httpx/cookies.go` gains, beside the mode cookie,
`ThemeCookie(r) string` (`"light"`, `"dark"`, or `""` for none or any other value) and
`SetThemeCookie(w, theme, https)` (the mode cookie's attributes; `""` deletes it with `MaxAge: -1`).
`pageData` gains `Theme string`, filled by `shellPage`, `loginPage` and the plain message page from
the cookie (a 403 or 404 page is a page too: `message` takes the request). `base.html`:
`<html lang="en" data-theme="caravana"{{with .Theme}} data-cv-theme="{{.}}"{{end}}>` and
`<meta name="color-scheme" content="{{if .Theme}}{{.Theme}}{{else}}dark light{{end}}">`.

**R6. The Settings page.** `settingsnav.go`: `entryAppearance = "appearance"`, a last group
`{"This browser", [{entryAppearance, "Appearance", "/settings/appearance", "sun-moon"}]}` (a new
lucide outline in `icons.go`). Routes in `ui.go`: `GET /settings/appearance` (`requireSession`) and
`POST /settings/appearance` (`protected(requireSession(…))`). `settingsData` gains
`Appearance appearanceForm{Choice string; Saved bool; Err *fieldError}` where Choice is `system`,
`light` or `dark` from the cookie. The template `settings-appearance`: the `cv-sthead` ("Appearance",
lede "How the console looks in this browser."), then a `.cv-panel` with one setting row: label
"Theme", hint "Kept in this browser only.", and in the control column a `fieldset` (legend `sr-only`
"Theme") of three `.cv-radio` labels (`<input type="radio" name="theme" value="system|light|dark">`):
"Match the system" followed by `<span data-system-theme></span>` (empty without script), "Light",
"Dark"; then the Save button (`cv-btn cv-btn-secondary cv-btn-sm`). `?saved=1` draws the notice
"Saved." with the healthy mark above the panel, as the Backup storage page does. A posted value that
isn't one of the three draws the page again (422) with a `.cv-error` under the radios ("Choose one of
the three."). `console.js` fills every `[data-system-theme]` with ` (now light)` or ` (now dark)`
from `matchMedia("(prefers-color-scheme: light)")`. `.cv-radio` is `.cv-check`'s rule for a radio
(14 px, `accent-color: var(--color-accent)`), and `.cv-radios` stacks them with 8 px.

**R7. Tests.** `internal/ui/theme_test.go` reads `style/theme.css` and `style/input.css` through
the package's embedded or on-disk files (the templates are embedded; the style folder is read from
disk relative to the test, as `tools/ui-css.sh` does):
- `TestEveryColorTokenHasBothValues` parses each `--color-*:` declaration of the plugin block and
  `@theme`: it is `light-dark(a, b)` or `var(--color-x)`; nothing else.
- `TestThemeContrast` ports `cr.py` (oklch → linear sRGB → relative luminance → ratio, about 30
  lines) and checks FR-012 for the dark values and for the light values, resolving `var()` chains.
- `TestNoColorOutsideTokens` (FR-010) walks `input.css`, `templates/*.html`, `static/js/console.js`,
  `localtime.js`, `monitoring.js`, `ops.js`: no `oklch(`, `rgb(`, `rgba(`, `hsl(`, and no
  `#[0-9a-fA-F]{6}\b` or `#[0-9a-fA-F]{3}\b` in the CSS (outside `mask-image(...)`), no
  `#[0-9a-fA-F]{6}\b` in templates and scripts (a 3-digit check there would hit anchors like `#add`);
  `theme.css` may have a raw colour only on a `--color-*` line. `terminal.js` is skipped by name.
- `shell_test.go` or a new `theme_shell_test.go`: FR-001 and FR-003 over the pages `TestNoShipWords`
  walks plus `/login` and a 404 page; `settings_test.go`: the nav's last group, the form, the post's
  303 and `Set-Cookie`, the deletion, the refusal (FR-004); `internal/httpx/cookies_test.go`: the two
  functions. `TestNoShipWords` and `TestSettingsNavOnEveryPage` take the new entry.
- `cmd/caravana/console_appearance_test.go`, step `console-appearance` before `console-delete`.

## Constitution check

V (same service layer: nothing to call; a per-browser preference, like 027's view cookie), VII (the
cookie protects nothing and is HttpOnly and Lax anyway), VIII (table tests, no module, no migration).
No complexity tracking needed.

## Project structure

```
internal/httpx/cookies.go, cookies_test.go       ThemeCookie, SetThemeCookie
internal/ui/style/theme.css                      every token with both values; the switching rules
internal/ui/style/input.css                      tokens instead of colours; .cv-radio; no color-scheme
internal/ui/static/app.css                       rebuilt (tools/ui-css.sh build)
internal/ui/templates/base.html                  data-cv-theme and the meta
internal/ui/templates/settings.html              settings-appearance
internal/ui/settings.go, settingsnav.go, ui.go   the entry, the two handlers, the routes
internal/ui/icons.go                             sun-moon
internal/ui/shell.go, login.go, ui.go            pageData.Theme
internal/ui/static/js/console.js                 the bracket word
internal/ui/static/js/terminal.js                the exception's comment
internal/ui/theme_test.go, settings_test.go, shell_test.go, words_test.go
cmd/caravana/console_appearance_test.go, console_lab_test.go
DESIGN.md, CHANGELOG.md, CLAUDE.md
```
