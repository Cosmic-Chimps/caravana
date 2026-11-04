# Feature 028: a light theme for the console

**Branch**: `028-light-theme` (cut from main at 0.16.0; the contract is on it). Built from the CodeAlta
task `2026-11-03-a-light-theme-for-the-console-zqj6` on the worktree branch `alta/fluent-topaz-6qk8`.
**Phase**: 2.6 (`docs/plan.md` lists "a light theme" among the things the Developer view still owes).
Numbered after 027.5, outside the plan's sequence like 019.5 and 020.5.

## Why

The maintainer, 2026-11-03: the console should have a light theme. Today it is dark only: every
colour comes from the dark tokens in `internal/ui/style/theme.css`, `color-scheme: dark` is set on the
root and on three controls, and `input.css` still writes some colours directly instead of through a
token. A light theme is for daylight, a projector, or an owner who prefers it.

It is a light variant of the same identity, not a new look. The "field notebook" direction DESIGN.md
records as turned down stays turned down. Kept: status is the only colour and every status pairs a
shape with it; Plex Sans and Plex Mono; flat hairline panels and small radii; depth from lightness
steps; a shadow only on overlays.

## Contract

`contracts/light-theme.md` is the designer's decision: how the theme is chosen, every token's light
value (computed with `contracts/cr.py`, oklch to WCAG), the terminal's exception, the DESIGN.md
changes, the tests and the review. Its rulings are this spec's requirements. Where this file and the
contract disagree, the contract wins.

## User stories

1. **(P1) The theme follows the system.** With no choice made, the console is dark when the browser
   prefers dark or says nothing, and light when it prefers light. Every colour on every page comes
   from a token with a dark and a light value, so charts, meters, pills, rows and native controls
   (date, select, checkbox) all follow. The terminal stays dark in both.
2. **(P2) A forced choice.** Settings › This browser › Appearance offers Match the system, Light and
   Dark. The choice is kept in this browser only (a cookie), holds on the sign-in page and after
   sign-out, is in the first byte of every page (no flash), and works without script.
3. **(P3) The checks and the docs.** Tests keep the rules: no colour outside the tokens (two named
   exceptions), both values for every token, the contract's contrast ratios recomputed from
   `theme.css`, the shell's attribute and meta, the form, and a lab step reading a page per forced
   theme. DESIGN.md says how the two themes relate (the contract's section 5).

## Requirements

Choosing (contract §1):
- FR-001 Without a cookie the page has no `data-cv-theme` and `<meta name="color-scheme">` says
  `dark light`; the stylesheet is dark unless `prefers-color-scheme: light`.
- FR-002 The cookie `caravana_theme` is `light` or `dark`, set like the mode cookie (Path `/`, one
  year, `SameSite=Lax`, `HttpOnly`, `Secure` over HTTPS); "Match the system" deletes it. Any other
  value reads as no choice.
- FR-003 With the cookie, `<html>` carries `data-cv-theme="light"` or `"dark"` (never `data-theme`,
  which daisyUI uses) and the meta says that one word, on every page, the sign-in page included.
- FR-004 `GET /settings/appearance` is the form (a `.cv-panel`, a settings row labelled "Theme" with
  the hint "Kept in this browser only.", three radios, Save as a secondary sm button); the Settings
  nav gains a last group "This browser" with the entry Appearance. "Match the system" gains
  " (now dark)" or " (now light)" only when script runs. `POST /settings/appearance` is protected
  like the other posts, sets or deletes the cookie, and answers 303 to `/settings/appearance?saved=1`,
  which draws "Saved.". A value that isn't one of the three is refused with the page drawn again.
- FR-005 `color-scheme` is set on `:root` per theme; the three hard-coded `color-scheme: dark` (the
  date field, `.cv-select`, `.cv-check`) go, so native controls inherit it.

The palette (contract §2 and §3):
- FR-006 Every token in the contract's tables has its light value: the neutral ramp, primary (Readout
  Ink in light), the daisyUI slots (pointing at the ramp), the four status colours with their grounds
  and edges, the six service hues, and the new tokens that replace the direct colours of `input.css`
  (`focus`, `failed-wash`, `failed-wash-hover`, `hue-0…5`, `tick`, `ping`, the overlay shadow).
  Dark keeps today's values.
- FR-007 Every colour `input.css` writes directly becomes a token. The direct colours the contract's
  table doesn't list (the rolling washes of the deploy rows, the fresh and anchor washes, the hatch
  fill, the failed wash of a copy chip) become tokens too, with light values proposed in the plan for
  the designer's review. The two `#000` stops inside `mask-image` are alpha masks and stay.
- FR-008 Charts, meters, the histogram and the sparklines follow through their tokens; nothing in
  them is changed unless the review asks for `--color-hatch` or `--color-band`.
- FR-009 The terminal's box keeps a dark ground in both themes, framed by `line-2`; `terminal.js`'s
  hex palette stays and says why in a comment.

Checks and docs (contract §5 and §6):
- FR-010 A test fails on any `oklch(`, `rgb(`, `hsl(` or hex colour in `input.css`, the templates or
  Caravana's own scripts, except `#000` inside `mask-image` and `terminal.js`; in `theme.css` a raw
  colour appears only in a `--color-*` declaration.
- FR-011 A test fails on a `--color-*` token without both values.
- FR-012 A test recomputes, from `theme.css`, for both palettes: fg-1 to fg-3 and the four status
  colours ≥ 4.5:1 on ink-0 to ink-3; each status colour ≥ 4.5:1 on its own `-bg`; the six hues ≥ 4.5:1
  on ink-1 and ink-2; `focus` ≥ 3:1 on ink-1 and ink-2; fg-4 ≥ 2.5:1 on ink-1.
- FR-013 Tests cover FR-001 to FR-004 in `internal/ui` and `internal/httpx`; `TestConsoleLab` has a
  step that forces each theme through the form and reads a page in it.
- FR-014 DESIGN.md changes as the contract's section 5 says; CHANGELOG's Unreleased and CLAUDE.md's
  "Where we are" record the feature.

## Scope

In: `theme.css`, `input.css` and the built `app.css`; `base.html`, `settings.html` and a Settings page
with its handler; the cookie in `internal/httpx`; a comment in `terminal.js`; a line in `console.js`
for the bracket word; the tests above; DESIGN.md, CHANGELOG.md, CLAUDE.md.

Out: a new look, per-user themes (the choice is per browser), theming the terminal, an API change
(none: the choice is a browser preference, like the view cookie), a timezone or any other "This
browser" setting.

## Success criteria

- SC-001 Every console page reads well in light and in dark, by the system's choice and when forced,
  at 1440 and 390: the designer's review (contract §7) approves after the impeccable critique, the
  rulings and the last look.
- SC-002 The contrast test passes for both palettes, and no colour is written outside the tokens.
- SC-003 `tools/ui-css.sh check`, `go test ./...`, `go vet`, staticcheck, gofmt and the cross-compile
  pass; the lab step passes.

## Constitution check

- V API-first: no endpoint changes; the theme is a per-browser preference the console keeps in a
  cookie, like the view cookie of 027, and no feature exists only in the UI (the API has no "look").
- VII: the cookie holds nothing to protect; it is HttpOnly and Lax all the same, like the mode cookie.
- VIII: table tests for the pure parts (the oklch math, the token parser), the pages through the
  package's tests, and the lab step. No new Go module, no migration, no Nomad change.
- Technology: `html/template`, the embedded stylesheet built by `tools/ui-css.sh`; no new build step.
