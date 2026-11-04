# Tasks: 028 a light theme for the console

**Input**: `spec.md`, `plan.md`, `contracts/light-theme.md`. Each task names the requirements it
implements. Verification for every task: `go build ./... && go vet ./... && go test ./internal/ui
./internal/httpx` and, after a style change, `tools/ui-css.sh build`.

## US1 (P1): the theme follows the system

- [x] T001 [US1] [FR-005, FR-006] `internal/ui/style/theme.css`: every `--color-*` of the daisyUI
  plugin block and `@theme` as plan R3 and R4 say (`light-dark(dark, light)` or `var(--color-…)`);
  the switching rules of R2; the keyframes and `--shadow-overlay` through the new tokens; the
  `:focus-visible` and `.cv-field:focus` outlines through `--color-focus`; the date field's
  `color-scheme: dark` removed; the header comment updated ("Elevation goes UP" is no longer the
  whole story: depth is distance from the panel face).
- [x] T002 [US1] [FR-005, FR-007, FR-009] `internal/ui/style/input.css`: every direct colour
  replaced by its token (R4's second table), `.cv-select` and `.cv-check` lose `color-scheme: dark`,
  `.cv-terminal` takes `var(--color-terminal)`; `tools/ui-css.sh build`.
- [x] T003 [US1] [FR-009] `internal/ui/static/js/terminal.js`: the comment on the hex palette says
  it is the one exception to the Token-Only Color Rule and why (contract §4).
- [x] T004 [US1] [FR-010, FR-011, FR-012] `internal/ui/theme_test.go` (plan R7): both values per
  token, the contrast ratios for both palettes, no colour outside the tokens.

## US2 (P2): a forced choice

- [x] T005 [US2] [FR-002] `internal/httpx/cookies.go` + `cookies_test.go`: `ThemeCookie`,
  `SetThemeCookie` (R5).
- [x] T006 [US2] [FR-001, FR-003] `pageData.Theme` set by `shellPage`, `loginPage` and the message
  page; `base.html`'s attribute and meta (R5); tests over every page and `/login` (FR-001, FR-003).
- [x] T007 [US2] [FR-004] Settings › This browser › Appearance: `settingsnav.go` entry and group,
  `icons.go` `sun-moon`, `ui.go` routes, `settings.go` handlers and `appearanceForm`,
  `settings.html` `settings-appearance`, `.cv-radio`/`.cv-radios` in `input.css` (then
  `tools/ui-css.sh build`), `console.js`'s bracket word (R6); tests in `settings_test.go` (the nav,
  the form's radios and the checked one, the 303 and the cookie, the deletion, the refusal,
  "Saved."); `TestNoShipWords` and `TestSettingsNavOnEveryPage` updated.

## US3 (P3): the checks and the docs

- [x] T008 [US3] [FR-013] `cmd/caravana/console_appearance_test.go`: the step `console-appearance`
  (the form → light → a page says `data-cv-theme="light"` → dark → system → no attribute; the
  sign-in page with the cookie), listed in `console_lab_test.go` before `console-delete`.
- [x] T009 [US3] [FR-014] DESIGN.md as contract §5: Key Characteristics, Colors (light value beside
  each dark one; Readout Ink), Elevation & Depth ("Depth is distance from the panel face"), the
  Shape-and-Color Rule's sentence, the focus ring, the new Token-Only Color Rule, the service hue's
  light six, the terminal's line, the Settings section's new group.
- [x] T010 [US3] [FR-014] CHANGELOG.md Unreleased (Added: the light theme and the Appearance
  setting; Changed: the status colours in light are darker), CLAUDE.md "Where we are" (a short 028
  paragraph), `docs/plan.md`'s "a light theme" mention marked done.
- [ ] T011 The full check: `go test ./...`, `go vet ./...`, `go tool staticcheck ./...`, `gofmt -l .`,
  `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`, `tools/ui-css.sh check`,
  `tools/ui-vendor.sh check`; `CARAVANA_LAB=1 go test ./cmd/caravana -run TestConsoleLab -v`.

## Review

- [ ] T012 [SC-001] The designer's review (contract §7) at 1440 and 390, light and dark, by system
  and forced, on the listed pages; the impeccable critique (no detector); rulings written into the
  contract ("Critique rulings"); fixes; the last look ("Last look", dated).
