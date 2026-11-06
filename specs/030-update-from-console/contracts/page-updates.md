# Contract: Updates, updating caravana from the page (030)

Written 2026-11-06 for the page changes of feature 030, on top of 027's approved page 3 contract
(`specs/027-operator-view/contracts/page3-updates.md`, with its review, recheck, critique rulings and
last look). Everything there still holds unless this list changes it. The handoff's `UpdatesScreen.jsx`
gives the shapes: the Available panel, "What happens" with its two buttons and the pending line, Past runs.
The rules of the console hold: the time rule (UTC, local added by script), the No-Dim-Text Rule (text is
never fg-4), the Shape-and-Color Rule, the console never says "ship", forms and links work without
script, version and package tokens never break.

The case: on 2026-11-04 the maintainer read "caravana doesn't apply updates from the console yet." and
asked for the button. One host, caravana's own update only: OS packages and the reboot stay by hand.

## What changes

1. Available's caravana row gets **Update to 0.17.0…**, which opens a confirmation page.
2. A **run panel** says what the update is doing and how it ended, across caravana's own restart.
3. How to apply's caravana step and the panel's foot line get new words.
4. How this server updates gets **Automatic caravana updates**.
5. Past updates lists update runs.
6. The needs-you list gets `caravana_update_failed` (words in `api.md`).

## 1. The caravana row's action

In the notes column, after the notes and **Release notes on GitHub**, one action, 12px above it:

- **Update to 0.17.0…**: `cv-btn cv-btn-primary cv-btn-sm`, a link (`<a>`) to
  `/ops/updates/caravana?version=0.17.0`. The version is a nowrap token. Shown when the updater is
  installed, the build isn't a development one, and no run is open.
- While a run is open, the action is left out (the run panel says what happens).
- Without the updater, in its place, 14px fg-2: "To update from here, run the installer of `0.17.0` once
  on the server (How to apply). It adds the updater." with "How to apply" a link to `#apply`.
- A development build: nothing in its place (027 already says why there's no release to compare with).

## 2. The confirmation page (`/ops/updates/caravana?version=0.17.0`)

The Stop page's shape (`stop.html`): `cv-main`, the headline, one `.cv-panel` (padding 16px, gap 12px,
15px fg-1 sentences), then the form's buttons in `.cv-actions`. Crumb `Updates / Update caravana`.

- Headline: "Update caravana to `0.17.0`" (versions mono, as everywhere).
- The panel's sentences, in order, each its own paragraph:
  1. "caravana `0.16.0` → `0.17.0`. The server's updater starts it within a minute, as root, from the
     installer: the console only asks."
  2. "The console stops for about a minute while the new version starts. Your apps keep running and
     keep answering."
  3. Only when the release changes a pin, one sentence each:
     - Nomad: "Nomad `2.0.7` → `2.1.0` is installed in the same run. Nomad restarts; apps keep running."
     - Traefik: "Traefik `3.7.13` → `3.8.0` is installed in the same run. Traefik restarts, so every
       app's address stops answering for a few seconds."
     With no pin change: "Nomad and Traefik stay as they are, unless the installer changes their
     settings."
  4. "If `0.17.0` doesn't answer its health check within a minute, `0.16.0` comes back with its data as
     it was, and this page says why."
  5. 14px fg-2: "OS updates and the reboot aren't part of it: they stay on the server."
- The form posts to `/ops/updates/caravana` with the hidden `version`: **Update to `0.17.0`**
  (`cv-btn cv-btn-primary`) and **Cancel** (`cv-btn cv-btn-secondary`, a link to `/ops/updates`).
- The post answers 303 to `/ops/updates#run`. A refusal draws this page again with the sentence in a
  `.cv-error` (■, `role="alert"`) in place of the panel, and only **Back to Updates** (status 409, never a
  redirect). A version that isn't offered any more (a newer one came out, or it's installed) is a
  refusal too, with `api.md`'s sentence.

## 3. The run panel (`id="run"`)

Between the head (and its banners) and Available, a section with no `SectionHead` of its own: one
`.cv-panel` row, the domains banner's grid (`cv-dmbanner`: mark, text, action), so it reads like the
other banners on this page.

- Shown while a run is open, and for 24 hours after it finished. Otherwise absent.
- The mark (9px), the sentence 15px/1.5 fg-1, the explanation 14px fg-2, then the meta 400 12px mono
  fg-3, all from the run's state:

| State | Mark | Sentence | Explanation |
| --- | --- | --- | --- |
| requested | ◐ rolling | "Waiting for the server's updater to start the update to `0.17.0`." | "It looks for a request every minute." |
| running | ◐ rolling | "Updating caravana `0.16.0` → `0.17.0`." | "The console stops for about a minute while the new version starts. This page comes back by itself; your apps keep running." |
| updated | ● healthy | "caravana `0.17.0` is running." | "Updated from `0.16.0` in 1 min 40 s." |
| rolled_back | ◆ warning | "caravana `0.17.0` didn't start, so `0.16.0` runs again with its data as it was." | the reason, then "The whole log is on the server: `sudo cat <log>`" |
| failed | ■ failed | "The update to caravana `0.17.0` failed." | the reason, then the log sentence |
| refused | ◆ warning | "The server's updater didn't start the update to `0.17.0`." | the reason (no log sentence: nothing ran) |
| lost | ◆ warning | "The update to caravana `0.17.0` didn't finish." | the reason |

- Meta: who and when, the deployments' trigger words: "from the console · asked 14:02 UTC", "through
  the API by `ci-token` · asked 14:02 UTC", "automatic, at 03:00 UTC". Times through `up-time` (local
  added by script).
- The action column: empty while open. After a rolled_back, failed, refused or lost run, when the release
  is still newer: **Try again…** (`cv-btn cv-btn-secondary cv-btn-sm`, the confirmation page's link).
- The reason is plain text (escaped); the log path is a code span that may break anywhere (it's long).
- Live: while the run is open the section is
  `hx-get="/parts/ops/updates/run" hx-trigger="every 5s" hx-swap="outerHTML"`; failed requests (the
  console is down) are ignored and it keeps asking. When the answer is a finished run, the main region
  (`#up-top`) and Past updates are also swapped (by id, as `up-part` does), so the caravana row and its
  step agree with the new version. The page's existing SSE region is reconnected by its own script.
- Without script: the page's head has `<noscript><meta http-equiv="refresh" content="15"></noscript>`
  while a run is open (only then).
- The needs-you banner for `caravana_update_failed` is not drawn on this page: the run panel says it
  (on /ops and the Developer home it shows as every item does).

## 4. How to apply

- The caravana step, when the updater is installed: "Update from the caravana row above, or run the
  installer of `0.17.0` on the server:" and the command, as today. Without the updater: 027's sentence,
  then "Running it once also adds the updater, so later updates can start from this page."
- The foot line becomes: "caravana updates only itself from here. The OS's updates and the reboot stay
  on the server."

## 5. How this server updates: Automatic caravana updates

A `StField` row after **New caravana versions**, in the same form (one **Save** for both; the form's
existing 303 to `?saved=1` and "Saved." stay):

- Label **Automatic caravana updates**, hint "Only patch releases (`0.16.x`), which fix without changing
  how things work. A newer minor release waits for you, and a version that failed is never tried again
  by itself."
- The control: the checkbox **Install patch releases by themselves** (`autoUpdate`), then
  "at" and a `cv-select` (the console's one 32px select) of the 24 hours `00:00 UTC` … `23:00 UTC`
  (`autoUpdateHour`, default 03:00).
- Under it, 12px mono fg-3: "next: Nov 7, 03:00 UTC" when one is due; "off"; or why it can't run, in
  14px fg-2: "It needs the release check: turn on Check GitHub for new releases." / "It needs the
  server's updater: run the installer once on the server." / "This is a development build."
- Without the updater the checkbox and select are `disabled` (the hint line says why).

## 6. Past updates

- An update run is a row: mark ● updated, ◆ rolled back / refused / lost, ■ failed; the date; the
  sentence "caravana `0.16.0` → `0.17.0`"; the detail "from the console · 1 min 40 s", "automatic ·
  2 min", "rolled back · didn't answer its health check" (the reason cut at its first `;` or `.`, at most
  60 characters, then "…"), "refused", "didn't finish".
- A version row ("caravana 0.16.0 → 0.17.0") explained by an `updated` run within an hour isn't drawn
  again.
- Still the last 10, newest first.

## Narrow (390px)

- The run panel stacks as the banners do (023's rule): the action under the text.
- The confirmation page's buttons wrap under each other at 390.
- The Automatic row: checkbox, then "at" and the select on the next line.

## May differ

- No Run in next window: the update starts within a minute or at the automatic hour.
- No rolling plan across servers (Phase 3).

## API (names only)

`requestUpdate`, `listUpdateRuns`, `getUpdates.caravana.{updater, run, autoUpdate}`,
`putUpdateSettings.{autoUpdate, autoUpdateHour}`, attention kind `caravana_update_failed` (`api.md`).

## Review

At 127.0.0.1:7070 with a fake updater directory (`CARAVANA_UPDATER_DIR`), a fake release
(`CARAVANA_RELEASE_URL`) and the builder playing the updater by writing `status`: each state of the run
panel at 1440, 640 and 390 and without script; the confirmation page with and without pin changes; the
refusals; the automatic row in each of its states; Past updates with runs; the nav and /ops needs-you
item after a rolled-back run. On a VM, `install-test.sh` runs the real thing.

## Review rulings (2026-11-06, build 8af3568)

Checked live at 1440 and 500 (the narrowest the browser window took; nothing scrolls sideways) on a
staged 0.18.0 with a fake 0.18.1 release and a fake updater folder, the status file written by hand:
requested, running, Caravana stopped for 12 s and started again, then rolled back. The page came back
by itself after the restart and drew the rollback; Past updates, the needs-you item on Overview (◆,
Open updates) and the Activity line were right.

Verified: the confirmation page (no pin change), the requested and running panels, the poll, the
automatic row (off, the hour select), Past updates' run row, the departures the builder listed (own
regions for the banners and the panel, one Save through `form=`, "on · nothing due yet").

Fixes, made in the same pass:
1. The version in **Update to 0.18.1…** and the confirmation page's button sat in a nowrap span, which the
   button's gap drew as "Update to  0.18.1  …". Plain text in buttons.
2. `/ops/updates#run` stopped the panel under the sticky top bar: `scroll-margin-top` as the log rows have.
3. A rollback's explanation repeated the line ("…; 0.18.0 runs again, with its data as it was"). The panel
   and Activity show the installer's reason up to its first ";" (`service.RollbackCause`).
4. The log sentence is its own line, so the long path doesn't run on from the reason.
5. After a failed run of the newest release the panel's **Try again…** is the one way to ask again: the
   caravana row leaves its button out.
6. While a run is open, How to apply's caravana step no longer points at the row's button: "Run the
   installer of `0.18.1` on the server:".

Not checked here: a release with pin changes (the table test covers its sentences) and the dark theme
(no new colour).
